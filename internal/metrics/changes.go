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

// betaValidity opens every ranked metric's validity note until a lead's
// labels clear the bar (docs/metrics.md#validity-checks, ADR 0014): the rank
// tables, explain and the MCP server print the note wherever the metric's
// values are. Each metric's note adds its own model-panel reference beside
// the mark, never in its place, so the notes differ and each ranked metric
// gets its own caveat.
const betaValidity = "Beta: not yet checked against a lead's labels (stillhere label, then stillhere validate: " +
	"AUC >= 0.7 on at least 50 scored labels; docs/validation.md). "

// The model panel's reference results (docs/validation.md#reference-results):
// validate's AUC of each ranked metric on the panel's majority labels, pooled
// over four public repositories, with its interval, n and the AUC of plain
// size on the same changes. They are docs/metrics.md's Validity lines, word
// for word; a new panel run changes the three together.
const (
	durableReference = "Reference: AUC 0.42 (0.30–0.55) on 76 model-panel labels " +
		"(2026-10-15; 4 public repositories; size baseline 0.82), not yet a lead's labels."
	simplifyingReference = "Reference: AUC 0.76 (0.63–0.88) on 65 model-panel labels " +
		"(2026-10-15; 4 public repositories; size baseline 0.87), not yet a lead's labels."
	reviewsReference = "Reference: AUC 0.55 (0.40–0.71) on 49 model-panel labels " +
		"(2026-10-15; 4 public repositories; size baseline 0.89), not yet a lead's labels."
)

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
		taken := make(map[string]bool, len(r.landings))
		pulls, _ := x.landings(r)
		for _, pl := range pulls {
			c := Change{Repo: r.key, Pull: &Pull{ID: pl.pull.ID, Repo: names[pl.pull.Repo], Number: pl.pull.Number, Title: pl.pull.Title}}
			var mine []*landingView
			for _, l := range pl.landings {
				if !taken[l.sha] {
					taken[l.sha] = true
					c.add(r, l)
					mine = append(mine, l)
				}
			}
			if len(mine) > 0 {
				c.Bot = v.botLandings(mine...)
				out = append(out, c)
			}
		}
		for _, l := range r.landings {
			if !taken[l.sha] {
				c := Change{Repo: r.key, Bot: v.botLandings(l)}
				c.add(r, l)
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
	human, known := false, false
	for _, l := range landings {
		brought := false
		for _, k := range l.commits {
			if !k.merge {
				brought, known = true, true
				human = human || !v.who.Bot(k.credit)
			}
		}
		if !brought && l.commit != nil {
			known = true
			human = human || !v.who.Bot(l.commit.credit)
		}
	}
	return known && !human
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
