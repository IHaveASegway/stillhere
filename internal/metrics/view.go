package metrics

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
)

// This file is the projection every metric reads: one pass over a FactSet
// that joins its slices into per-repository indexes (commit by SHA, each
// landing's commit map, the changes of each commit, the surviving lines of
// each commit and file) and applies the credit rule once per commit and per
// blamed commit. Each metric is then a pass over maps rather than a join of
// its own, and every metric reads the same joins, so two metrics cannot
// disagree about which commits a landing holds or who is credited for them.
// In CQRS terms it is a read model, built from the facts for one query.
//
// The projection is rebuilt for each call: package Compute builds one and
// shares it across every metric, and Clusters and WhoKnows build one over
// the repositories they need. It is never cached on Input, which callers
// build as a plain struct literal: a hidden cache would be mutable state
// behind a value that looks immutable.

// day is the unit of every metric window: 24-hour UTC days
// (docs/metrics.md#change-facts-shared-by-durable1-and-simplifying1).
const day = 24 * time.Hour

// The numbers of the versioned definitions in docs/metrics.md. A change to
// any of them is a new metric version, never an edit here. Windows are in
// days before Facts.Now.
const (
	cohortFrom      = 365 // durable@1 cohort: [now-365d, now-90d)
	cohortTo        = 90
	recentDays      = 90  // simplifying@1 window and who_knows@1 recent landers
	activityDays    = 180 // knowledge_loss@1 activity
	yearDays        = 365 // landings_365d, ai_assisted@1
	minClusters     = 10  // eligibility of both ranked metrics
	simplifyingNet  = -20 // a landing is simplifying when its net is at most this
	maxTopAuthors   = 5   // who_knows@1 lists up to five top authors
	majorityPercent = 50  // majority_owner@1: at least half of a file's lines
)

// warnShallowWindow is the warning the history collector gives a shallow
// clone whose boundary falls inside the window it collected, or cuts a
// landing's commit map there (docs/spec/discovery.md#clone-health).
const warnShallowWindow = "shallow-window"

// view is the projection of one FactSet.
type view struct {
	now     time.Time
	days    int   // landed@1's window: Input.Days, else the policy's [week] days
	daysErr error // why days is unknown (Input.weekDays); only landed@1 needs it
	who     *identity.Directory
	repos   []*repoView     // the repositories with facts, in FactSet order (by key)
	facts   *domain.FactSet // the facts projected, whose GitHub data (Forge) the review metrics read (reviews.go)
	merges  *identityMerges // Input.Candidates indexed (identities.go); nil without any
}

// repoView is one repository's indexes.
type repoView struct {
	key, group string
	scopes     []domain.Scope // the scopes it belongs to: its repo, its group, all
	history    domain.Window  // the landing window the history covers (RepoFacts.History)
	// cut is true when a shallow clone's boundary cuts the history (warning
	// shallow-window): landings before the boundary are missing.
	cut bool
	// boundary is the newest committer time of a boundary commit the
	// history holds (domain.Repo.Boundary), zero when it holds none: the
	// clone holds every landing after it, so a window that starts later
	// is inside its history (covers).
	boundary time.Time
	// lines is true when blame ran (RepoFacts.LineFacts), so surviving
	// lines are known; without it they are unknown, not zero.
	lines bool
	// numstat is true when the history counted each commit's numstat, the
	// lines landed@1 and landed@2 add up: false for a partial clone, whose
	// blobs are not local, so its lines are unknown, not zero (#74).
	numstat bool
	// patches is true when every non-merge commit carries the counts of its
	// own -U0 -w patch: false for week runs (AddedNonBlank is -1) and
	// partial clones (no changes at all). Without them, added and removed
	// lines are unknown, not zero.
	patches  bool
	role     map[string]domain.Role // every classified path → its role, "" when skipped
	commits  map[string]*commitView
	landings []*landingView       // in FactSet order
	files    map[string]*fileView // prod paths with surviving lines at HEAD
	owners   []ownerRow           // blamed lines on paths with a role at HEAD, credited
}

// commitView is one commit of the history, with its credit and counts.
type commitView struct {
	credit   string // identity.Credit over its author and co-authors
	ai       bool   // identity.AIAssisted
	merge    bool   // more than one parent: excluded from both change metrics
	ignored  bool   // listed in the ignore-revs file blame used
	boundary bool   // a shallow clone's boundary (domain.Repo.Boundary): its parents are not in the clone
	changes  []*domain.FileChange
	// added and removed count non-blank lines on prod paths in the commit's
	// own patch. traced is the part of added whose fate blame can tell, and
	// surviving the non-blank lines blame attributes to the commit at HEAD
	// on the same terms: both classify a line by the path it had in the
	// commit, where the commit added it, so only a line on a prod path
	// there counts, whatever its path's role at HEAD; and neither counts a
	// line untracedLines leaves out, which both ask by that path too.
	added, removed, traced, surviving int
}

// landingView is one landing with its commit map.
type landingView struct {
	sha      string
	time     time.Time
	commit   *commitView   // the landing commit; nil when the history lacks it
	commits  []*commitView // the commit map, in Landing.Commits order, the landing included
	revert   bool          // a revert landing: its message names the commit it reverts
	reverted bool          // a revert landing names a commit in its map
	ai       bool          // a commit in its map is AI-assisted
	cut      bool          // its map holds a shallow clone's boundary, behind which its commits are missing
}

// fileView is one prod file at HEAD and who holds its lines.
type fileView struct {
	path     string
	total    int            // non-blank lines at HEAD
	byPerson map[string]int // credited email → non-blank lines
	owners   []string       // majority owners (at least half the lines), sorted
}

// ownerRow is one blamed (path, commit) row on a path with a role.
type ownerRow struct {
	path   string
	role   domain.Role
	credit string
	lines  int
	commit string    // the blamed commit, for a trace (surviving@1)
	at     time.Time // its author time, as blame gives it
}

// errNoFacts reports an Input that cannot be computed over.
var errNoFacts = errors.New("metrics: the input has no fact set")

// newView projects in.Facts, keeping the repositories keep accepts (nil
// keeps all). Repositories whose collection failed (status error) are left
// out: their facts are incomplete in ways no metric can see.
func newView(in *Input, keep func(*domain.RepoFacts) bool) (*view, error) {
	if in == nil || in.Facts == nil {
		return nil, errNoFacts
	}
	if in.Facts.Now.IsZero() {
		return nil, errors.New("metrics: the fact set has no Now, so no window can be placed")
	}
	who := in.Who
	if who == nil {
		who = identity.New(in.Facts, in.Policy)
	}
	v := &view{now: in.Facts.Now, who: who, facts: in.Facts, merges: newIdentityMerges(in.Candidates)}
	v.days, v.daysErr = in.weekDays()
	for i := range in.Facts.Repos {
		f := &in.Facts.Repos[i]
		if f.Status == domain.RepoStatusError || (keep != nil && !keep(f)) {
			continue
		}
		v.repos = append(v.repos, newRepoView(f, who))
	}
	return v, nil
}

// newRepoView builds one repository's indexes.
func newRepoView(f *domain.RepoFacts, who *identity.Directory) *repoView {
	r := &repoView{
		key:     f.Repo.Key,
		group:   f.Repo.Group,
		history: f.History,
		cut:     slices.ContainsFunc(f.Warnings, func(w domain.Warning) bool { return w.Code == warnShallowWindow }),
		lines:   f.LineFacts,
		numstat: !f.Repo.Partial,
		patches: !f.Repo.Partial,
		role:    make(map[string]domain.Role, len(f.Paths)),
		commits: make(map[string]*commitView, len(f.Commits)),
	}
	r.scopes = []domain.Scope{{Kind: domain.ScopeRepo, Key: r.key}}
	if r.group != "" {
		r.scopes = append(r.scopes, domain.Scope{Kind: domain.ScopeGroup, Key: r.group})
	}
	r.scopes = append(r.scopes, domain.Scope{Kind: domain.ScopeAll})

	for _, p := range f.Paths {
		r.role[p.Path] = p.Role
	}
	ignored := make(map[string]bool, len(f.IgnoreRevs.Revs))
	for _, sha := range f.IgnoreRevs.Revs {
		ignored[strings.ToLower(sha)] = true
	}
	untraced := untracedLines(f)
	for i := range f.Commits {
		c := &f.Commits[i]
		r.commits[c.SHA] = &commitView{
			credit:   who.Credit(c.Author, c.CoAuthors),
			ai:       who.AIAssisted(c),
			merge:    len(c.Parents) > 1,
			ignored:  ignored[strings.ToLower(c.SHA)],
			boundary: f.Repo.Boundary(c),
		}
		if f.Repo.Boundary(c) && c.CommitTime.After(r.boundary) {
			r.boundary = c.CommitTime
		}
	}
	for i := range f.Changes {
		fc := &f.Changes[i]
		c := r.commits[fc.Commit]
		if c == nil {
			continue
		}
		c.changes = append(c.changes, fc)
		if c.merge {
			continue // a merge has no patch of its own in v1
		}
		if fc.AddedNonBlank < 0 || fc.RemovedNonBlank < 0 {
			r.patches = false // a week run: patch counts were not collected
			continue
		}
		if r.role[fc.Path] == domain.RoleProd {
			c.added += fc.AddedNonBlank
			c.removed += fc.RemovedNonBlank
			if !untraced.has(fc.Path, fc.Commit) {
				c.traced += fc.AddedNonBlank
			}
		}
	}

	revertLandings := make(map[string]bool, len(f.Reverts))
	revertedCommits := make(map[string]bool, len(f.Reverts))
	for _, rv := range f.Reverts {
		revertLandings[rv.SHA] = true
		revertedCommits[rv.Reverted] = true
	}
	for i := range f.Landings {
		l := &f.Landings[i]
		lv := &landingView{sha: l.SHA, time: l.Time, commit: r.commits[l.SHA], revert: revertLandings[l.SHA]}
		shas := l.Commits
		if len(shas) == 0 {
			shas = []string{l.SHA} // a week run may leave the map empty: a landing always owns itself
		}
		for _, sha := range shas {
			if revertedCommits[sha] {
				lv.reverted = true
			}
			c := r.commits[sha]
			if c == nil {
				continue
			}
			lv.commits = append(lv.commits, c)
			lv.ai = lv.ai || c.ai
			lv.cut = lv.cut || c.boundary
		}
		r.landings = append(r.landings, lv)
	}

	if r.lines {
		r.indexLines(f, who, untraced)
	}
	return r
}

// untracedIndex holds the lines whose survival blame cannot tell. Both
// sides of durable@1 ask it about a commit's lines by the path they had in
// that commit: the added side with a change's own (Path, Commit), the
// blamed side with a line owner's (OrigPath, Commit), which blame names
// itself (domain.LineOwner). A line is untraced when its path is in paths,
// or when lines holds its (path, commit). durable@1 counts an untraced line
// in neither of its sums (untracedLines). The zero value holds none.
type untracedIndex struct {
	paths map[string]bool     // every commit's lines on these paths are untraced: a failed blame
	lines map[pathCommit]bool // one commit's lines on one path are untraced: a binary change
}

// pathCommit names a commit's lines by the path they had in that commit.
type pathCommit struct{ path, commit string }

// has reports whether commit's lines on path, the path they had in commit,
// are untraced.
func (u untracedIndex) has(path, commit string) bool {
	return u.paths[path] || u.lines[pathCommit{path, commit}]
}

// untracedLines returns the lines on which blame cannot tell whether a
// commit's added lines survive (docs/metrics.md, "Missing facts are not
// zeros"). Two facts make lines untraced:
//
//   - A binary change (FileChange.Binary: numstat "-", an own patch with no
//     hunks) has unknown added lines, while blame still attributes every
//     line of the file, the commit's among them. The fact is about one
//     path in one commit, and blame names that pair for each line it gives
//     the commit (LineOwner.OrigPath), whichever renames lie between the
//     commit and HEAD and whether or not the history saw them: the history
//     sees none outside its window, and gives up on a commit with too many
//     rename candidates. So only that pair is untraced, in both sums,
//     with no walk. Every binary change is held, whatever its path's role:
//     one on a path with no prod role, such as a binary file under vendor/
//     that a later commit moves onto a prod path, leaves both sums by that
//     role already, since both classify a line by the path it had in its
//     commit, so holding it changes nothing and keeps the rule one line.
//   - A path whose blame failed (RepoFacts.BlameErrors: a timeout, a failed
//     git blame, porcelain that did not parse) has no line owners, so its
//     surviving lines are unknown. The fact is about a path at HEAD, and
//     with no owners no line names the paths its lines had before; the
//     renames the history recorded are the only link, so the walk goes
//     back through them: every commit's lines on the path, and on each
//     path renamed into it, are untraced. A rename the history did not
//     record leaves lines added before it in Σ added with no surviving
//     line, which lowers a ratio and never raises one. The walk ignores
//     when a rename happened, so a path that a rename vacated and a new
//     file took is untraced along with the old file, in both sums alike.
//     Every failed path seeds it, whatever its role at HEAD: a line counts
//     by the path it had in its commit, so the lines of a prod file moved
//     into tests/ are surviving lines too, and a failed blame of the test
//     file hides them.
//
// Both facts feed one predicate, untracedIndex.has, which the added and the
// blamed side of durable@1 both ask with the same key, so its two sums
// leave out the same lines, whichever fact left a line out (the
// Specification pattern). Without it a failed file would score its authors
// as keeping nothing, and a binary one as keeping more than they added.
// Blame treats each file as a bulkhead (a failed one becomes a FileError
// while LineFacts stays true), and the metrics keep the failure inside it.
func untracedLines(f *domain.RepoFacts) untracedIndex {
	var u untracedIndex
	for i := range f.Changes {
		if fc := &f.Changes[i]; fc.Binary {
			if u.lines == nil {
				u.lines = make(map[pathCommit]bool)
			}
			u.lines[pathCommit{fc.Path, fc.Commit}] = true
		}
	}
	failed := make([]string, 0, len(f.BlameErrors))
	for _, e := range f.BlameErrors {
		failed = append(failed, e.Path)
	}
	if len(failed) == 0 {
		return u
	}
	sources := make(map[string][]string) // a rename's destination → its sources
	for i := range f.Changes {
		if fc := &f.Changes[i]; fc.OldPath != "" && fc.OldPath != fc.Path {
			sources[fc.Path] = append(sources[fc.Path], fc.OldPath)
		}
	}
	u.paths = make(map[string]bool, len(failed))
	for len(failed) > 0 {
		p := failed[len(failed)-1]
		failed = failed[:len(failed)-1]
		if u.paths[p] {
			continue
		}
		u.paths[p] = true
		failed = append(failed, sources[p]...)
	}
	return u
}

// indexLines credits every blamed row once per blamed commit and builds the
// prod files with their majority owners, both by the row's path at HEAD:
// they describe the code as it is.
//
// A commit's surviving lines are the other side of durable@1's ratio, so
// they classify a row as its added lines classify a change: by the path the
// line had in the commit (LineOwner.OrigPath), which blame names whatever
// renames lie between. A line counts when that path is prod, whatever its
// role at HEAD, and leaves out the untraced lines, by the same path, as the
// commit's traced lines do. So a file added on a path with no prod role
// (under vendor/ or tests/, or with no role at all) and later moved onto a
// prod path keeps none of the lines its commit never added, and one moved
// from a prod path into tests/ keeps the lines it added, where blame reads
// the file. A path blame does not read at HEAD, such as a denied one, has
// no rows: lines moved there count as added and never as surviving
// (docs/metrics.md, durable@1's blind spots). A commit the history does not
// hold has no commitView and no added lines to keep in step, so its rows
// count for no change, whether or not the facts classify its path.
func (r *repoView) indexLines(f *domain.RepoFacts, who *identity.Directory, untraced untracedIndex) {
	credits := make(map[string]string) // blamed commit → credited email
	r.files = make(map[string]*fileView)
	for i := range f.Owners {
		o := &f.Owners[i]
		if o.Lines <= 0 {
			continue
		}
		if c := r.commits[o.Commit]; c != nil {
			if at := cmp.Or(o.OrigPath, o.Path); r.role[at] == domain.RoleProd && !untraced.has(at, o.Commit) {
				c.surviving += o.Lines
			}
		}
		role := r.role[o.Path]
		if role == "" {
			continue
		}
		credit, ok := credits[o.Commit]
		if !ok {
			credit = who.Credit(o.Author, o.CoAuthors)
			credits[o.Commit] = credit
		}
		r.owners = append(r.owners, ownerRow{path: o.Path, role: role, credit: credit, lines: o.Lines, commit: o.Commit, at: o.AuthorTime})
		if role != domain.RoleProd {
			continue
		}
		fv := r.files[o.Path]
		if fv == nil {
			fv = &fileView{path: o.Path, byPerson: make(map[string]int)}
			r.files[o.Path] = fv
		}
		fv.total += o.Lines
		fv.byPerson[credit] += o.Lines
	}
	for _, fv := range r.files {
		fv.owners = majorityOwners(fv.byPerson, fv.total)
	}
}

// majorityOwners returns, sorted, the people holding at least half of a
// file's total lines (majority_owner@1's rule): one, or two at exactly
// half each.
func majorityOwners(byPerson map[string]int, total int) []string {
	var out []string
	for email, n := range byPerson {
		if n*100 >= total*majorityPercent {
			out = append(out, email)
		}
	}
	slices.Sort(out)
	return out
}

// covers reports whether the repository's history holds every landing in w,
// as a count, a sum or a share over w needs. A week run's history spans
// only its own days, so a 365-day share computed from it would be a week's
// share under a year's label: such a repository contributes nothing to a
// window it does not cover. An unset history (zero window) is trusted as
// covering everything.
//
// A history a shallow boundary cuts (#74) covers a window that lies wholly
// after the boundary: every boundary commit the history holds was
// committed before the window starts, and no landing in the window brought
// one in, whose commits behind it would be missing. The clone then holds
// every landing of the window and every commit they brought in, so its
// numbers there are exact, not partial: a depth-50 clone has a week's
// landers, not none. Any other window is not covered, and gets no value
// rather than a count of what the clone happens to hold; so is every
// window when the history holds no boundary commit, as when the boundary
// is dated after --now, since then nothing places it.
//
// Activity needs no coverage (view.active, landings_365d, who_knows@1's
// recent landers): a landing seen in a window proves activity however much
// history is missing, so it counts wherever it is seen.
func (r *repoView) covers(w domain.Window) bool {
	if r.history != (domain.Window{}) && (r.history.Since.After(w.Since) || r.history.Until.Before(w.Until)) {
		return false
	}
	if !r.cut {
		return true
	}
	if r.boundary.IsZero() || !r.boundary.Before(w.Since) {
		return false
	}
	return !slices.ContainsFunc(r.landings, func(l *landingView) bool { return l.cut && w.Contains(l.time) })
}

// inScope reports whether the repository with this key and group belongs to
// scope s.
func inScope(s domain.Scope, key, group string) bool {
	switch s.Kind {
	case domain.ScopeRepo:
		return key == s.Key
	case domain.ScopeGroup:
		return group != "" && group == s.Key
	case domain.ScopeAll:
		return true
	default:
		return false
	}
}

// people returns the distinct people credited with a commit in the
// landing's map, merge commits included: who "has had" this landing.
func (l *landingView) people() []string {
	var out []string
	for _, c := range l.commits {
		if !slices.Contains(out, c.credit) {
			out = append(out, c.credit)
		}
	}
	return out
}

// sortedFiles returns the repository's prod files, sorted by path.
func (r *repoView) sortedFiles() []*fileView {
	out := make([]*fileView, 0, len(r.files))
	for _, fv := range r.files {
		out = append(out, fv)
	}
	slices.SortFunc(out, func(a, b *fileView) int { return cmp.Compare(a.path, b.path) })
	return out
}

// window returns [now - from days, now - to days).
func (v *view) window(from, to int) domain.Window {
	return domain.Window{
		Since: v.now.Add(-time.Duration(from) * day),
		Until: v.now.Add(-time.Duration(to) * day),
	}
}

// active returns the people credited with any commit, merge commits
// included, in a landing within w, in any repository of the view: "has had
// a landing" in docs/metrics.md. Through the landing map, a side-branch
// author is active when the branch lands, whoever merged it. Every landing
// seen in w counts, whether or not the history covers all of w: skipping a
// repository whose history is short would report its active people as
// gone.
func (v *view) active(w domain.Window) map[string]bool {
	out := make(map[string]bool)
	for _, r := range v.repos {
		for _, l := range r.landings {
			if !w.Contains(l.time) {
				continue
			}
			for _, c := range l.commits {
				out[c.credit] = true
			}
		}
	}
	return out
}

// personKey identifies one person's value in one scope.
type personKey struct {
	scope domain.Scope
	email string
}

// lookup returns m[k], creating it on first use.
func lookup[K comparable, V any](m map[K]*V, k K) *V {
	p := m[k]
	if p == nil {
		p = new(V)
		m[k] = p
	}
	return p
}

// The subjects of the values that describe a code population rather than a
// person.
func repoSubject(key string) domain.Subject {
	return domain.Subject{Kind: domain.SubjectRepo, Key: key}
}

func groupSubject(name string) domain.Subject {
	return domain.Subject{Kind: domain.SubjectGroup, Key: name}
}

// areaSubject names a path prefix inside a repository: Repo.Key + ":" +
// prefix, where a directory's prefix ends in a slash ("src/") and a file's is
// its path.
func areaSubject(key, prefix string) domain.Subject {
	return domain.Subject{Kind: domain.SubjectArea, Key: key + ":" + prefix}
}

func personSubject(email string) domain.Subject {
	return domain.Subject{Kind: domain.SubjectPerson, Key: email}
}

// scopeSubject names a scope's own totals: its repository, its group, or
// the whole run (domain.SubjectAll).
func scopeSubject(s domain.Scope) domain.Subject {
	switch s.Kind {
	case domain.ScopeRepo:
		return repoSubject(s.Key)
	case domain.ScopeGroup:
		return groupSubject(s.Key)
	}
	return domain.Subject{Kind: domain.SubjectAll}
}

// underPrefix returns the rule for the paths under a repo-relative prefix
// as cleanPrefix leaves it, matching whole segments: "src" holds src and
// src/pay.go but not srcutil/x.go, and "" holds every path. who_knows@1
// and PathKnowledge ask about a path by it.
func underPrefix(prefix string) func(path string) bool {
	return func(p string) bool {
		return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
	}
}

// topDir returns the top-level directory of a repo-relative path with its
// trailing slash ("src/"), or "" for a file at the root.
func topDir(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i+1]
	}
	return ""
}

// count returns a value whose Num is the value and whose Den is 1, as
// MetricValue documents for metrics that are not ratios.
func count(id domain.MetricID, s domain.Scope, sub domain.Subject, w domain.Window, value, n int, detail map[string]float64) domain.MetricValue {
	return domain.MetricValue{
		Metric: id, Scope: s, Subject: sub, Window: w,
		Value: float64(value), Num: float64(value), Den: 1, N: n, Detail: detail,
	}
}

// ratio returns num/den with both parts. The caller guarantees den > 0:
// encoding/json cannot encode the NaN of 0/0, and a ratio over nothing says
// nothing, so such values are not emitted at all.
func ratio(id domain.MetricID, s domain.Scope, sub domain.Subject, w domain.Window, num, den, n int, detail map[string]float64) domain.MetricValue {
	return domain.MetricValue{
		Metric: id, Scope: s, Subject: sub, Window: w,
		Value: float64(num) / float64(den), Num: float64(num), Den: float64(den), N: n, Detail: detail,
	}
}

// compareValues is the total order of every value this package returns:
// metric, scope, subject, then window.
func compareValues(a, b domain.MetricValue) int {
	return cmp.Or(
		cmp.Compare(a.Metric.Name, b.Metric.Name),
		cmp.Compare(a.Metric.Version, b.Metric.Version),
		cmp.Compare(a.Scope.Kind, b.Scope.Kind),
		cmp.Compare(a.Scope.Key, b.Scope.Key),
		cmp.Compare(a.Subject.Kind, b.Subject.Kind),
		cmp.Compare(a.Subject.Key, b.Subject.Key),
		a.Window.Since.Compare(b.Window.Since),
		a.Window.Until.Compare(b.Window.Until),
	)
}

// sorted sorts vs in place and returns it, nil when empty.
func sorted(vs []domain.MetricValue) []domain.MetricValue {
	if len(vs) == 0 {
		return nil
	}
	slices.SortFunc(vs, compareValues)
	return vs
}
