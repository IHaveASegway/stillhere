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
	blockDays       = 7   // simplifying@1's bootstrap blocks are weeks
	maxTopAuthors   = 5   // who_knows@1 lists up to five top authors
	majorityPercent = 50  // majority_owner@1: at least half of a file's lines
)

// view is the projection of one FactSet.
type view struct {
	now   time.Time
	days  int // landed@1's window: Input.Days, else the policy's [week] days
	who   *identity.Directory
	repos []*repoView // the repositories with facts, in FactSet order (by key)
}

// repoView is one repository's indexes.
type repoView struct {
	key, group string
	scopes     []domain.Scope // the scopes it belongs to: its repo, its group, all
	history    domain.Window  // the landing window the history covers (RepoFacts.History)
	// lines is true when blame ran (RepoFacts.LineFacts), so surviving
	// lines are known; without it they are unknown, not zero.
	lines bool
	// patches is true when every non-merge commit carries the counts of its
	// own -U0 -w patch: false for week runs (AddedNonBlank is -1) and
	// partial clones (no changes at all). Without them, added and removed
	// lines are unknown, not zero.
	patches  bool
	role     map[string]domain.Role // every classified path → its role, "" when skipped
	commits  map[string]*commitView
	landings []*landingView       // in FactSet order
	files    map[string]*fileView // prod paths with surviving lines at HEAD
	owners   []ownerRow           // blamed lines on paths with a role, credited
}

// commitView is one commit of the history, with its credit and counts.
type commitView struct {
	credit  string // identity.Credit over its author and co-authors
	ai      bool   // identity.AIAssisted
	merge   bool   // more than one parent: excluded from both change metrics
	ignored bool   // listed in the ignore-revs file blame used
	changes []*domain.FileChange
	// added and removed count non-blank lines on prod paths in the commit's
	// own patch; surviving counts the non-blank prod lines blame attributes
	// to it at HEAD. traced is the part of added whose fate blame can
	// tell: the lines on paths whose blame did not fail (untracedPaths).
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
	v := &view{now: in.Facts.Now, days: in.Days, who: who}
	if v.days <= 0 && in.Policy != nil {
		v.days = in.Policy.Week.Days
	}
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
		lines:   f.LineFacts,
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
	untraced := untracedPaths(f, r.role)
	for i := range f.Commits {
		c := &f.Commits[i]
		r.commits[c.SHA] = &commitView{
			credit:  who.Credit(c.Author, c.CoAuthors),
			ai:      who.AIAssisted(c),
			merge:   len(c.Parents) > 1,
			ignored: ignored[strings.ToLower(c.SHA)],
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
			if !untraced[fc.Path] {
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
		}
		r.landings = append(r.landings, lv)
	}

	if r.lines {
		r.indexLines(f, who)
	}
	return r
}

// untracedPaths returns the paths on which blame cannot tell whether a
// commit's added lines survive: every prod path whose blame failed
// (RepoFacts.BlameErrors: a timeout, a failed git blame, porcelain that did
// not parse), and every path the history renamed into one of them, because
// blame follows a file back through its renames, so lines added to old.py
// are blamed in its successor. It is nil when no prod path failed.
//
// Blame treats each file as a bulkhead: one that fails becomes a FileError
// while the other files are blamed, and LineFacts stays true. The metrics
// keep the failure inside the same bulkhead. A failed file has no line
// owners, so its surviving lines are unknown; durable@1 must then leave
// the lines added to it out of its denominator too, or their authors would
// score as keeping nothing. Only prod paths seed the set: surviving lines
// count prod paths at HEAD only, so a failed path with another role hides
// no surviving line.
func untracedPaths(f *domain.RepoFacts, role map[string]domain.Role) map[string]bool {
	var queue []string
	for _, e := range f.BlameErrors {
		if role[e.Path] == domain.RoleProd {
			queue = append(queue, e.Path)
		}
	}
	if len(queue) == 0 {
		return nil
	}
	sources := make(map[string][]string) // a rename's destination → its sources
	for i := range f.Changes {
		if fc := &f.Changes[i]; fc.OldPath != "" && fc.OldPath != fc.Path {
			sources[fc.Path] = append(sources[fc.Path], fc.OldPath)
		}
	}
	out := make(map[string]bool, len(queue))
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if out[p] {
			continue
		}
		out[p] = true
		queue = append(queue, sources[p]...)
	}
	return out
}

// indexLines credits every blamed row once per blamed commit and builds the
// prod files with their majority owners.
func (r *repoView) indexLines(f *domain.RepoFacts, who *identity.Directory) {
	credits := make(map[string]string) // blamed commit → credited email
	r.files = make(map[string]*fileView)
	for i := range f.Owners {
		o := &f.Owners[i]
		role := r.role[o.Path]
		if role == "" || o.Lines <= 0 {
			continue
		}
		credit, ok := credits[o.Commit]
		if !ok {
			credit = who.Credit(o.Author, o.CoAuthors)
			credits[o.Commit] = credit
		}
		r.owners = append(r.owners, ownerRow{path: o.Path, role: role, credit: credit, lines: o.Lines})
		if role != domain.RoleProd {
			continue
		}
		if c := r.commits[o.Commit]; c != nil {
			c.surviving += o.Lines
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
		for email, n := range fv.byPerson {
			if n*100 >= fv.total*majorityPercent {
				fv.owners = append(fv.owners, email)
			}
		}
		slices.Sort(fv.owners)
	}
}

// covers reports whether the repository's history holds every landing in w.
// A week run's history spans only its own days, so a 365-day share computed
// from it would be a week's share under a year's label: such a repository
// contributes nothing to a window it does not cover. An unset history (zero
// window) is trusted as covering everything.
func (r *repoView) covers(w domain.Window) bool {
	if r.history == (domain.Window{}) {
		return true
	}
	return !r.history.Since.After(w.Since) && !r.history.Until.Before(w.Until)
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

// block returns the week, counted back from now, that t falls in: 0 for
// [now-7d, now). simplifying@1's bootstrap resamples these blocks.
func (v *view) block(t time.Time) int {
	return int(v.now.Sub(t) / (blockDays * day))
}

// active returns the people credited with any commit, merge commits
// included, in a landing within w, in any repository of the view: "has had
// a landing" in docs/metrics.md. Through the landing map, a side-branch
// author is active when the branch lands, whoever merged it.
func (v *view) active(w domain.Window) map[string]bool {
	out := make(map[string]bool)
	for _, r := range v.repos {
		if !r.covers(w) {
			continue
		}
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
