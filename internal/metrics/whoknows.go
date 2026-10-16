package metrics

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// whoKnowsV1 is who_knows@1: a routing answer for one path, not a rank. It
// answers a question about any path prefix, so it is computed per query by
// WhoKnows, over a stored run's facts; precomputing it for every directory
// of every repository would store more rows than the facts themselves.
type whoKnowsV1 struct{}

var whoKnowsID = domain.MetricID{Name: "who_knows", Version: 1}

// Definition returns who_knows@1's catalog entry.
func (whoKnowsV1) Definition() Definition {
	return Definition{
		ID:       whoKnowsID,
		Question: "Who knows the code under this path?",
		Unit: "Computed per query (who-knows PATH), never stored by a scan. It counts the path's prod lines, or, under a " +
			"path that holds no surviving prod line, the lines of every other role that has some there (test, doc, iac, ...), " +
			"so a test or doc directory is answered for its own lines rather than as nobody's. Per repository: one value about " +
			"the path (subject repo for the whole repository, else area \"<repo key>:<file>\" or \"<repo key>:<dir>/\") whose " +
			"Value is its counted lines, with Detail prod_files, files_with_majority and, when other roles are counted, " +
			"<role>_files for each, or, when prod lines are counted and other roles hold lines there too, " +
			"uncounted_<role>_lines and uncounted_<role>_files for each, the lines the answer leaves out; and one value per " +
			"person listed, whose Value is their counted " +
			"lines under the path, with Detail majority_files (files under the path they hold at least half of), recent_landings " +
			"(landings in [now - 90d, now) in which a non-merge commit credited to them changed non-blank lines on a path with a " +
			"role under the path, ignore-revs commits excluded), top_author (1 to 5, by counted lines then email, when they are " +
			"one of the top five) and, when the run has GitHub data for the repository, recent_reviews (merged pull requests " +
			"authored by someone else whose landings changed the path as recent_landings counts a change, on which they " +
			"submitted a review, approved, changes requested or commented, in [now - 90d, now)). A person is listed when any " +
			"of the four applies; a reviewer the run cannot link to an email is listed as @login. Bots are ranked among bots, " +
			"apart.",
		Ranked:      false,
		Forge:       true,
		Subjects:    nil, // computed per query by WhoKnows, for any path: a scan stores no value of it
		Eligibility: "Not ranked: it routes questions to people.",
		BlindSpots: "Moves across files credit the mover. Someone who knows the code but has neither written its surviving lines, " +
			"nor landed or reviewed a change under it recently, does not appear. Reviews count only in repositories whose " +
			"GitHub data the run read, on pull requests that landed on the default branch: one merged into another branch, " +
			"which reached it inside another landing, does not count. A rebase merge whose commits GitHub committed in " +
			"different seconds counts its last commit only, and a commit pushed straight to the branch, with no pull " +
			"request, in the same second as a squash of several commits and just before it counts as that pull request's.",
		Gaming:   "Not a target: appearing in it earns nothing but questions.",
		Oracle:   "testdata/fixture/SPEC.md#who_knows1-main-fixture and #reviews-in-knowledge_loss1-and-who_knows1-main-fixture",
		Validity: "Context. Mockus and Herbsleb, \"Expertise Browser\" (ICSE 2002), routed questions by the same experience atoms (changes). " + survivingValidity,
	}
}

// Compute returns no values: who_knows@1 is answered per path by WhoKnows.
func (whoKnowsV1) Compute(in *Input) ([]domain.MetricValue, error) {
	if in == nil || in.Facts == nil {
		return nil, errNoFacts
	}
	return nil, nil
}

func (whoKnowsV1) compute(*view) ([]domain.MetricValue, error) { return nil, nil }

// WhoKnows answers who_knows@1 for the code under prefix, a repo-relative
// path prefix such as "src", "src/" or "src/pay.go" ("" or "." for the whole
// repository), in the repository whose Repo.Key is repo, or in every
// repository when repo is "". A prefix matches whole path segments: "src"
// covers src/pay.go but not srcutil/x.go. A repo that is no key is matched
// by its scrubbed form (redact.URL), so a remote pasted with its credentials
// finds its repository; one that matches neither wraps domain.ErrNotFound.
// A prefix nothing is known under gives no values.
func WhoKnows(in *Input, repo, prefix string) ([]domain.MetricValue, error) {
	if in == nil || in.Facts == nil {
		return nil, errNoFacts
	}
	key, err := repoKey(in.Facts, repo)
	if err != nil {
		return nil, err
	}
	v, err := newView(in, func(f *domain.RepoFacts) bool { return key == "" || f.Repo.Key == key })
	if err != nil {
		return nil, err
	}
	clean := cleanPrefix(prefix)
	w := v.window(recentDays, 0)
	reviews := v.reviews() // once for every repository asked about
	var out []domain.MetricValue
	for _, r := range v.repos {
		out = append(out, whoKnows(v, reviews, r, clean, w)...)
	}
	return sorted(out), nil
}

// repoKey returns the Repo.Key that repo names: repo itself when it is a
// key, else the key its scrubbed form is, since a key is a scrubbed origin
// URL (or "local:" and a name). "" names every repository. The not-found
// error quotes the scrubbed form only: the argument comes from the command
// line or an MCP client and may hold a token, and the error is printed and
// may be logged (docs/spec/git-invocation.md#url-scrubbing).
func repoKey(fs *domain.FactSet, repo string) (string, error) {
	if repo == "" {
		return "", nil
	}
	known := func(key string) bool {
		return slices.ContainsFunc(fs.Repos, func(f domain.RepoFacts) bool { return f.Repo.Key == key })
	}
	if known(repo) {
		return repo, nil
	}
	scrubbed := redact.URL(repo)
	if known(scrubbed) {
		return scrubbed, nil
	}
	return "", fmt.Errorf("metrics: who_knows@1: repository %q: %w", scrubbed, domain.ErrNotFound)
}

// cleanPrefix returns a repo-relative prefix without leading or trailing
// slashes, "" for the repository root.
func cleanPrefix(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

// whoKnows answers for one repository, its reviewers from the view's
// GitHub data, indexed once per query.
func whoKnows(v *view, reviews *reviewsIndex, r *repoView, prefix string, w domain.Window) []domain.MetricValue {
	under := underPrefix(prefix)
	type person struct {
		email                              string
		lines, majority, landings, reviews int
		top                                int
	}
	people := make(map[string]*person)
	files, withMajority, lines := 0, 0, 0
	tally := func(fs []*fileView) {
		for _, f := range fs {
			files++
			lines += f.total
			if len(f.owners) > 0 {
				withMajority++
			}
			for email, n := range f.byPerson {
				lookup(people, email).lines += n
			}
			for _, email := range f.owners {
				people[email].majority++
			}
		}
	}
	var prod []*fileView
	for _, f := range r.files {
		if under(f.path) {
			prod = append(prod, f)
		}
	}
	tally(prod)
	prodFiles := files
	byRole := map[domain.Role]int{} // the other roles' files counted, when the path holds no prod line
	// The other roles' lines and files a path with prod lines leaves out,
	// which the path value names (uncounted_<role>_lines and _files), so an
	// answer of a docs/ that holds one prod file says it counted 53 lines of
	// its 2,744 (wave 10's review: werkzeug's docs/conf.py).
	uncounted := map[domain.Role][2]int{}
	if lines > 0 {
		for _, f := range r.roleFiles(under) {
			u := uncounted[r.role[f.path]]
			uncounted[r.role[f.path]] = [2]int{u[0] + f.total, u[1] + 1}
		}
	}
	if lines == 0 {
		// No surviving prod line under the path: its tests, docs or build
		// files are what is known there, so the answer counts their lines,
		// which blame read as it read the prod ones, rather than say no one
		// knows it.
		other := r.roleFiles(under)
		for _, f := range other {
			byRole[r.role[f.path]]++
		}
		files, withMajority = 0, 0
		tally(other)
	}
	// Recent landers are activity: every landing seen in w counts, however
	// much of w the history covers (repoView.covers).
	for _, l := range r.landings {
		if !w.Contains(l.time) {
			continue
		}
		var landers []string
		for _, c := range l.commits {
			if !c.merge && !c.ignored && !slices.Contains(landers, c.credit) && c.changedUnder(r, under) {
				landers = append(landers, c.credit)
			}
		}
		for _, email := range landers {
			lookup(people, email).landings++
		}
	}
	// Recent reviewers: who reviewed, in w, a merged pull request whose
	// landings changed a path under the query, as a lander's change counts.
	// The review's time is what makes it recent, so a pull request that
	// landed earlier counts for a review in w.
	pulls, reviewed := reviews.landings(r)
	for _, pl := range pulls {
		if !slices.ContainsFunc(pl.landings, func(l *landingView) bool { return l.changedUnder(r, under) }) {
			continue
		}
		var reviewers []string
		reviewsByOthers(pl.pull, w, v.who, func(_ *domain.Review, s domain.Subject) {
			if !slices.Contains(reviewers, s.Key) {
				reviewers = append(reviewers, s.Key)
			}
		})
		for _, key := range reviewers {
			lookup(people, key).reviews++
		}
	}
	if files == 0 && len(people) == 0 {
		return nil
	}

	// The top authors, humans and bots ranked apart.
	var humans, bots []*person
	for email, p := range people {
		p.email = email
		switch {
		case p.lines == 0:
		case v.who.Bot(email):
			bots = append(bots, p)
		default:
			humans = append(humans, p)
		}
	}
	for _, list := range [][]*person{humans, bots} {
		slices.SortFunc(list, func(a, b *person) int {
			return cmp.Or(cmp.Compare(b.lines, a.lines), cmp.Compare(a.email, b.email))
		})
		for i, p := range list[:min(len(list), maxTopAuthors)] {
			p.top = i + 1
		}
	}

	scope := domain.Scope{Kind: domain.ScopeRepo, Key: r.key}
	pathDetail := map[string]float64{
		"prod_files":          float64(prodFiles),
		"files_with_majority": float64(withMajority),
	}
	for role, n := range byRole {
		pathDetail[string(role)+"_files"] = float64(n)
	}
	for role, u := range uncounted {
		pathDetail["uncounted_"+string(role)+"_lines"] = float64(u[0])
		pathDetail["uncounted_"+string(role)+"_files"] = float64(u[1])
	}
	out := []domain.MetricValue{count(whoKnowsID, scope, pathSubject(r, prefix), w, lines, 0, pathDetail)}
	for email, p := range people {
		if p.majority == 0 && p.landings == 0 && p.top == 0 && p.reviews == 0 {
			continue
		}
		detail := map[string]float64{
			"majority_files":  float64(p.majority),
			"recent_landings": float64(p.landings),
		}
		if p.top > 0 {
			detail["top_author"] = float64(p.top)
		}
		if reviewed {
			// Without GitHub data for the repository its reviews are
			// unknown, so the key is absent, never 0.
			detail["recent_reviews"] = float64(p.reviews)
		}
		out = append(out, count(whoKnowsID, scope, personSubject(email), w, p.lines, 0, detail))
	}
	return out
}

// roleFiles returns the files under a path whose role is neither prod nor
// none, each with who holds its surviving lines and its majority owners,
// from blame's rows as indexLines builds the prod files: who_knows@1 of a
// path that holds no prod line counts them. Sorted by path.
func (r *repoView) roleFiles(under func(string) bool) []*fileView {
	byPath := map[string]*fileView{}
	for _, o := range r.owners {
		if o.role == domain.RoleProd || !under(o.path) {
			continue
		}
		f := byPath[o.path]
		if f == nil {
			f = &fileView{path: o.path, byPerson: map[string]int{}}
			byPath[o.path] = f
		}
		f.total += o.lines
		f.byPerson[o.credit] += o.lines
	}
	out := make([]*fileView, 0, len(byPath))
	for _, f := range byPath {
		f.owners = majorityOwners(f.byPerson, f.total)
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b *fileView) int { return cmp.Compare(a.path, b.path) })
	return out
}

// changedUnder reports whether the landing changed a path under the query
// as recent landers count a change: a commit of its map, neither a merge
// nor listed in the ignore-revs file, whose own patch changed one
// (commitView.changedUnder).
func (l *landingView) changedUnder(r *repoView, under func(string) bool) bool {
	return slices.ContainsFunc(l.commits, func(c *commitView) bool {
		return !c.merge && !c.ignored && c.changedUnder(r, under)
	})
}

// pathSubject names what a who_knows@1 query asked about: the repository
// for its root, a file by its path, a directory by its path and a slash.
func pathSubject(r *repoView, prefix string) domain.Subject {
	if prefix == "" {
		return repoSubject(r.key)
	}
	if _, isFile := r.role[prefix]; isFile {
		return areaSubject(r.key, prefix)
	}
	return areaSubject(r.key, prefix+"/")
}

// changedUnder reports whether the commit's own patch changed non-blank
// lines on a path with a role that under accepts, as its destination or as
// a rename's source. Without patch counts (a week run) numstat stands in.
func (c *commitView) changedUnder(r *repoView, under func(string) bool) bool {
	for _, fc := range c.changes {
		var role domain.Role
		switch {
		case under(fc.Path):
			role = r.role[fc.Path]
		case fc.OldPath != "" && under(fc.OldPath):
			known := false
			if role, known = r.role[fc.OldPath]; !known {
				role = r.role[fc.Path] // the source was never classified: its destination's role
			}
		default:
			continue
		}
		if role == "" {
			continue
		}
		if fc.AddedNonBlank >= 0 && fc.RemovedNonBlank >= 0 {
			if fc.AddedNonBlank+fc.RemovedNonBlank > 0 {
				return true
			}
		} else if fc.Added+fc.Removed > 0 {
			return true
		}
	}
	return false
}
