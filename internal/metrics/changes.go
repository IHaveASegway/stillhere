package metrics

import (
	"cmp"
	"errors"
	"slices"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file is the grain a ranked metric is validated at (#77,
// docs/metrics.md#validity-checks): the run's changes, a landing or a
// pull request with the landings that put it on the default branch
// (docs/metrics.md#units), and each ranked metric's measure of one change,
// which stillhere validate compares with a person's labels and stillhere
// label samples from. The measure is what the change adds to the metric's
// clusters, so validate scores the numbers the ranks are made of, or, for a
// metric whose clusters are not keyed by the change's landings, the
// metric's own measure in its own file; no second definition of any metric
// lives here.

// betaValidity is every ranked metric's validity note until a lead's labels
// clear the bar (docs/metrics.md#validity-checks, ADR 0014): the rank
// tables, explain and the MCP server print it wherever the metric's values
// are, one caveat for the metrics that share it. A reference result, once
// the model panel has run, is added beside it, never in its place.
const betaValidity = "Beta: not yet checked against a lead's labels (stillhere label, then stillhere validate: " +
	"AUC >= 0.7 on at least 50 scored labels; docs/validation.md). The model-panel reference has not run yet."

// Change is one change of a run: a merged pull request of the run's GitHub
// data with the landings that put it on its repository's default branch
// (the rule who_knows@1's reviewers read: a merge commit's landing, a
// squash's, or a rebase merge's several), or a landing that no such pull
// request landed.
type Change struct {
	Repo     string    // Repo.Key
	Landings []string  // full SHAs, the pull request's merge commit (or the landing) first, then the earlier ones a rebase merge put on the branch
	Time     time.Time // when it landed: the committer time of Landings[0]
	Pull     *Pull     // nil for a landing that no pull request of the run's GitHub data landed
	// Bot is true when the change is a bot's (botLandings): every commit
	// its landings brought in, merge commits aside, is credited to a bot,
	// such as a dependency bump, whoever merged it.
	Bot bool
	// Added and Removed are the numstat lines, blank ones included, of its
	// landings' own diffs (a merge's first-parent diff, as landed@1 reads
	// it) on paths that received a role; Files counts those paths.
	Added, Removed, Files int
}

// Pull is the pull request a Change is.
type Pull struct {
	ID     string // PullRequest.ID: what reviews_given@1's clusters are keyed by
	Repo   string // its forge repository, "owner/name"
	Number int
	Title  string // local only; never sent to an AI
}

// Changes returns the run's changes, every landing of every repository the
// run collected in exactly one: a pull request's landings in its change,
// every other landing in one of its own. They are sorted by repository,
// then by landing time, then by first SHA.
func Changes(in *Input) ([]Change, error) {
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	names := map[string]string{} // ForgeRepo.ID → owner/name
	if v.facts.Forge != nil {
		for _, r := range v.facts.Forge.Repos {
			names[r.ID] = r.Owner + "/" + r.Name
		}
	}
	x := v.reviews()
	var out []Change
	for _, r := range v.repos {
		for _, cv := range x.changes(r) {
			if cv.via != nil {
				// A pull request merged into another branch has no landing
				// of its own: its merge's landing is a change already.
				continue
			}
			c := Change{Repo: r.key, Bot: v.botLandings(cv.landings...)}
			if p := cv.pull; p != nil {
				c.Pull = &Pull{ID: p.ID, Repo: names[p.Repo], Number: p.Number, Title: p.Title}
			}
			for _, l := range cv.landings {
				c.add(r, l)
			}
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), a.Time.Compare(b.Time), cmp.Compare(a.Landings[0], b.Landings[0]))
	})
	return out, nil
}

// changeView is one change of a repository, as the one grouping makes it
// (reviewsIndex.changes): what Changes lists, and what review_coverage@1
// counts.
type changeView struct {
	// landings are the change's own landings, its pull request's merge
	// commit's first, as Change.Landings; nil for a pull request merged
	// into another branch, which has none.
	landings []*landingView
	// via is, for a pull request merged into another branch, the merge
	// landing whose map holds its merge commit: how its change reached the
	// default branch, and when. nil for any other change.
	via  *landingView
	pull *domain.PullRequest // nil for a landing no pull request of the GitHub data landed
	// commits are the non-merge commits the change brought: its landings'
	// maps' less those of the pull requests merged into another branch
	// that its landings brought (brought), or, for such a pull request,
	// its own in its via landing's map: its merge commit, or a squash's or
	// a rebase's last commit, and its own commits a merge commit keeps.
	commits []*commitView
	// brought are the pull requests merged into another branch whose merge
	// commits its landings' maps hold, each a change of its own.
	brought []*changeView
}

// time is when the change reached the default branch: its first
// landing's committer time, or its via landing's.
func (c *changeView) time() time.Time {
	if c.via != nil {
		return c.via.time
	}
	return c.landings[0].time
}

// landing is the landing a change is named by: its first, or its via
// landing.
func (c *changeView) landing() *landingView {
	if c.via != nil {
		return c.via
	}
	return c.landings[0]
}

// own returns the change's own work: its commits that are no copy of an
// earlier landing's (markCopies), so a merge that only brought the pull
// requests of another branch, each counted on its own, or only copies,
// has none.
func (c *changeView) own() []*commitView {
	var out []*commitView
	for _, k := range c.commits {
		if !k.copy {
			out = append(out, k)
		}
	}
	return out
}

// changes returns repository r's changes, in the one grouping every reader
// of changes shares (docs/metrics.md#units): each merged pull request of
// the GitHub data that landed on the default branch, with its landings as
// landings finds them; each landing that none landed; and each pull
// request merged into another branch (a maintenance branch, a release
// branch, a long-lived feature branch) whose merge commit a merge landing's
// map holds, so it reached the default branch when that branch was merged
// into it. Every landing is in exactly one change that has landings, and
// every commit of a landing's map in one change's commits at most. Without
// GitHub data for r (or with x nil), every landing is a change of its own.
//
// A pull request merged into another branch claims, in its via landing's
// map, its merge commit and those of its own commits (PullRequest.Commits)
// the map holds, which a merge commit keeps; a squash's and a rebase's
// own commits are not on any branch, and of a rebase only the last commit
// is claimed, as for a rebase merge GitHub committed in several seconds on
// the default branch (#67). Pull requests claim in the order the GitHub
// data lists them, so one that holds another's commits takes none of them
// twice.
func (x *reviewsIndex) changes(r *repoView) []*changeView {
	pulls, known := x.landings(r)
	var out []*changeView
	taken := make(map[string]bool, len(r.landings))
	for _, pl := range pulls {
		c := &changeView{pull: pl.pull}
		for _, l := range pl.landings {
			if !taken[l.sha] {
				taken[l.sha] = true
				c.landings = append(c.landings, l)
			}
		}
		if len(c.landings) > 0 {
			out = append(out, c)
		}
	}
	for _, l := range r.landings {
		if !taken[l.sha] {
			out = append(out, &changeView{landings: []*landingView{l}})
		}
	}
	// The merged pull requests that landed no landing, by merge commit, in
	// the GitHub data's order.
	other := map[string]int{}
	var otherPulls []*domain.PullRequest
	if known {
		for _, p := range x.merged[r.key] {
			if _, dup := other[p.MergeCommit.SHA]; !dup && !taken[p.MergeCommit.SHA] {
				other[p.MergeCommit.SHA] = len(otherPulls)
				otherPulls = append(otherPulls, p)
			}
		}
	}
	n := len(out)
	for _, c := range out[:n] {
		claimed := map[*commitView]bool{}
		for _, l := range c.landings {
			if len(otherPulls) > 0 && l.commit != nil && l.commit.merge {
				inMap := make(map[string]*commitView, len(l.commits))
				var found []int // indexes into otherPulls of the pull requests whose merge commit l's map holds
				for _, k := range l.commits {
					inMap[k.sha] = k
					if i, ok := other[k.sha]; ok && k != l.commit {
						found = append(found, i)
					}
				}
				slices.Sort(found)
				for _, i := range found {
					p := otherPulls[i]
					b := &changeView{via: l, pull: p}
					mine := []*commitView{inMap[p.MergeCommit.SHA]}
					for _, pc := range p.Commits {
						if m := inMap[pc.SHA]; m != nil && pc.SHA != p.MergeCommit.SHA {
							mine = append(mine, m)
						}
					}
					for _, m := range mine {
						if !claimed[m] {
							claimed[m] = true
							if !m.merge {
								b.commits = append(b.commits, m)
							}
						}
					}
					c.brought = append(c.brought, b)
					out = append(out, b)
				}
			}
			for _, k := range l.commits {
				if !k.merge && !claimed[k] {
					c.commits = append(c.commits, k)
				}
			}
		}
	}
	return out
}

// add puts landing l of repository r into c: its SHA, its time when it is
// c's first, and its lines.
func (c *Change) add(r *repoView, l *landingView) {
	if len(c.Landings) == 0 {
		c.Time = l.time
	}
	c.Landings = append(c.Landings, l.sha)
	if l.commit == nil {
		return
	}
	for _, fc := range l.commit.changes {
		if r.role[fc.Path] != "" {
			c.Added += fc.Added
			c.Removed += fc.Removed
			c.Files++
		}
	}
}

// botLandings is the one rule for whether a change, or a landing, is a
// bot's (docs/metrics.md#units): every commit the landings brought in,
// merge commits aside, is credited to a bot (identity.Credit, so an
// agent's commit counts for its first human co-author, ADR 0007, and
// identity.Directory.Bot); a landing that brought in none, an empty
// merge, by its own commit's credit. It reads who wrote the change, as
// landed@2 credits it, not who merged it or opened its pull request: a
// person's merge of a dependency bump is the bot's, and a pull request an
// agent opened for a person is the person's. With no commit it can read,
// it is not a bot's.
func (v *view) botLandings(landings ...*landingView) bool {
	var credited []*commitView
	for _, l := range landings {
		brought := false
		for _, k := range l.commits {
			if !k.merge {
				brought = true
				credited = append(credited, k)
			}
		}
		if !brought && l.commit != nil {
			credited = append(credited, l.commit)
		}
	}
	return v.botCommits(credited)
}

// botCommits is the rule botLandings applies to the commits it reads: every
// one is credited to a bot, and there is one. review_coverage@1 applies it
// to a change's own commits (changeView.own).
func (v *view) botCommits(commits []*commitView) bool {
	for _, k := range commits {
		if !v.who.Bot(k.credit) {
			return false
		}
	}
	return len(commits) > 0
}

// Scorer is one ranked metric's measure of a change, which is how
// stillhere validate scores a labelled change (docs/metrics.md#validity-checks).
// It is what the change adds to the metric's clusters in scope all: for a
// ratio (Definition.Ratio), the clusters' numerators over their
// denominators, summed over every person and every landing of the change;
// for a sum, their sum. A change with no cluster is not measured. A metric
// whose clusters are not the change's own measures the change itself
// (changeMeasured): reviews_given@1, whose clusters are a reviewer's in its
// window. Only a change that landed in the metric's window is measured.
type Scorer struct {
	ratio    bool
	num, den map[string]float64            // cluster Change → its clusters' sums, over every person
	measure  func(*Change) (float64, bool) // a metric's own measure of a change (changeMeasured); nil: by its clusters
	window   domain.Window
	scores   string // which changes it measures, for a message that says why one was not
}

// changeMeasured is a ranked metric that measures a change itself, rather
// than by the clusters keyed by its landings. Score has already checked
// that the change landed in the metric's window.
type changeMeasured interface {
	measureChanges(in *Input) func(*Change) (float64, bool)
}

// scoring is a ranked metric's description of the changes it measures,
// for a message that says why a change was not measured.
type scoring interface {
	scores() string
}

// NewScorer computes m's clusters over in, in scope all, once.
func NewScorer(m Ranked, in *Input) (*Scorer, error) {
	if m == nil {
		return nil, errors.New("metrics: no metric to score changes with")
	}
	ri, err := m.Clusters(in, domain.Scope{Kind: domain.ScopeAll})
	if err != nil {
		return nil, err
	}
	s := &Scorer{ratio: m.Definition().Ratio, num: map[string]float64{}, den: map[string]float64{}, window: ri.Window}
	if d, ok := m.(scoring); ok {
		s.scores = d.scores()
	}
	if cm, ok := m.(changeMeasured); ok {
		s.measure = cm.measureChanges(in)
		return s, nil
	}
	for _, cs := range ri.Data {
		for _, c := range cs {
			s.num[c.Change] += c.Num
			s.den[c.Change] += c.Den
		}
	}
	return s, nil
}

// Window is the metric's window: the changes it measures landed in it.
func (s *Scorer) Window() domain.Window { return s.window }

// Scores says which changes of its window the metric measures, as a phrase
// ("a pull request of the run's GitHub data"), for a message that says why
// a change was not measured.
func (s *Scorer) Scores() string { return s.scores }

// Score is the metric's measure of c, and whether it measures c at all: a
// change that landed outside the metric's window is not measured, whatever
// its clusters, so a pull request merged before the window and reviewed in
// it is not reviews_given@1's.
func (s *Scorer) Score(c *Change) (float64, bool) {
	if !s.window.Contains(c.Time) {
		return 0, false
	}
	if s.measure != nil {
		return s.measure(c)
	}
	num, den, found := 0.0, 0.0, false
	for _, k := range c.Landings {
		if n, ok := s.num[k]; ok {
			num += n
			den += s.den[k]
			found = true
		}
	}
	switch {
	case !found:
		return 0, false
	case s.ratio:
		if den == 0 {
			return 0, false
		}
		return num / den, true
	}
	return num, true
}
