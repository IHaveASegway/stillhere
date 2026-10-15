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
// clusters, so validate scores the numbers the ranks are made of, and no
// second definition of any metric lives here.

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
	// Bot is true when every commit its landings brought in, merge commits
	// aside, is credited to a bot or an agent (identity.Directory.Bot): a
	// dependency bump, say, whoever merged it.
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
		taken := make(map[string]bool, len(r.landings))
		pulls, _ := x.landings(r)
		for _, pl := range pulls {
			c := Change{Repo: r.key, Pull: &Pull{ID: pl.pull.ID, Repo: names[pl.pull.Repo], Number: pl.pull.Number, Title: pl.pull.Title}}
			for _, l := range pl.landings {
				if !taken[l.sha] {
					taken[l.sha] = true
					c.add(r, l, v)
				}
			}
			if len(c.Landings) > 0 {
				out = append(out, c)
			}
		}
		for _, l := range r.landings {
			if !taken[l.sha] {
				c := Change{Repo: r.key}
				c.add(r, l, v)
				out = append(out, c)
			}
		}
	}
	slices.SortFunc(out, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), a.Time.Compare(b.Time), cmp.Compare(a.Landings[0], b.Landings[0]))
	})
	return out, nil
}

// add puts landing l of repository r into c: its SHA, its time when it is
// c's first, its lines, and whether a person wrote any of it.
func (c *Change) add(r *repoView, l *landingView, v *view) {
	if len(c.Landings) == 0 {
		c.Time = l.time
		c.Bot = true
	}
	c.Landings = append(c.Landings, l.sha)
	human, brought := false, false
	for _, k := range l.commits {
		if k.merge {
			continue
		}
		brought = true
		human = human || !v.who.Bot(k.credit)
	}
	if !brought && l.commit != nil {
		human = !v.who.Bot(l.commit.credit)
	}
	c.Bot = c.Bot && !human
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

// Scorer is one ranked metric's measure of a change: what the change adds
// to the metric's clusters in scope all, which is how stillhere validate
// scores a labelled change (docs/metrics.md#validity-checks). For a ratio
// (Definition.Ratio), the clusters' numerators over their denominators,
// summed over every person and every landing of the change; for a sum,
// their sum. A change with no cluster is not measured, unless the metric
// measures it as 0: reviews_given@1 has one cluster per reviewer, so a
// change no one else reviewed has none.
type Scorer struct {
	ratio    bool
	num, den map[string]float64 // cluster Change → its clusters' sums, over every person
	zero     func(*Change) bool // the changes without a cluster that the metric measures as 0; nil for none
	window   domain.Window
}

// zeroMeasured is a ranked metric some of whose changes have no cluster
// although it measures them: it says which, as a 0.
type zeroMeasured interface {
	measuredZero(in *Input, w domain.Window) func(*Change) bool
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
	for _, cs := range ri.Data {
		for _, c := range cs {
			s.num[c.Change] += c.Num
			s.den[c.Change] += c.Den
		}
	}
	if z, ok := m.(zeroMeasured); ok {
		s.zero = z.measuredZero(in, ri.Window)
	}
	return s, nil
}

// Window is the metric's window, which the changes it measures landed in.
func (s *Scorer) Window() domain.Window { return s.window }

// Score is the metric's measure of c, and whether it measures c at all.
func (s *Scorer) Score(c *Change) (float64, bool) {
	keys := c.Landings
	if c.Pull != nil {
		keys = append(slices.Clip(keys), c.Pull.ID)
	}
	num, den, found := 0.0, 0.0, false
	for _, k := range keys {
		if n, ok := s.num[k]; ok {
			num += n
			den += s.den[k]
			found = true
		}
	}
	switch {
	case !found:
		if s.zero != nil && s.zero(c) {
			return 0, true
		}
		return 0, false
	case s.ratio:
		if den == 0 {
			return 0, false
		}
		return num / den, true
	}
	return num, true
}
