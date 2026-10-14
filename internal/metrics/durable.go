package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// durableV1 is durable@1: of the non-blank prod lines a person's commits
// added in the cohort, the share that blame still attributes to those
// commits at HEAD. It is a Ratio metric: the bootstrap resamples a person's
// clusters (their counted commits in one landing), because lines inside one
// change are not independent.
type durableV1 struct{}

var durableID = domain.MetricID{Name: "durable", Version: 1}

// Definition returns durable@1's catalog entry.
func (durableV1) Definition() Definition {
	return Definition{
		ID:       durableID,
		Question: "Did the code a person wrote last?",
		Unit: "Σ surviving / Σ added per person over their clusters in the cohort, landings in [now - 365d, now - 90d), per " +
			"repository, group and all. added: non-blank prod lines of each counted commit's own -U0 -w patch; surviving: the " +
			"non-blank prod lines blame -w -M (with the repository's ignore-revs file) attributes to it at HEAD. A cluster is one " +
			"person's counted commits in one landing and needs at least one added line. Merge commits, ignore-revs commits and " +
			"revert landings are excluded; a reverted landing stays and scores what blame gives it. Detail: reverted_landings.",
		Ranked:      true,
		Eligibility: "At least 10 clusters in the cohort (open decision 5).",
		BlindSpots: "A planned replacement of correct code looks not durable. Moves across files credit the mover; a move inside " +
			"one file counts as the mover's added lines, which blame gives back to their first author, so the mover scores 0 on " +
			"them (open decision 6). A revert that restores deleted lines takes their blame from the original author. Changes " +
			"younger than 90 days are not scored. Lines blamed to a merge commit (conflict resolutions) are left out in v1.",
		Gaming:   "Writing in stable, low-traffic code and avoiding risky areas. Load-bearing (M5) is the counterweight.",
		Oracle:   "testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles (main fixture and sibling)",
		Validity: "Unvalidated until the M2 labelled-sample check (stillhere validate, AUC >= 0.7 on at least 50 labelled changes).",
	}
}

// Compute returns durable@1's values, bots included: they are listed in the
// bot table, never ranked.
func (m durableV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (durableV1) compute(v *view) ([]domain.MetricValue, error) {
	type acc struct{ surviving, added, n, reverted int }
	accs := make(map[personKey]*acc)
	for _, k := range durableClusters(v) {
		for _, s := range k.repo.scopes {
			a := lookup(accs, personKey{s, k.person})
			a.surviving += k.surviving
			a.added += k.added
			a.n++
			if k.landing.reverted {
				a.reverted++
			}
		}
	}
	w := v.window(cohortFrom, cohortTo)
	out := make([]domain.MetricValue, 0, len(accs))
	for k, a := range accs {
		out = append(out, ratio(durableID, k.scope, personSubject(k.email), w, a.surviving, a.added, a.n, map[string]float64{
			"reverted_landings": float64(a.reverted),
		}))
	}
	return sorted(out), nil
}

// Clusters returns durable@1's rank input for one scope.
func (durableV1) Clusters(in *Input, scope domain.Scope) (RankInput, error) {
	v, err := scopeView(in, scope)
	if err != nil {
		return RankInput{}, err
	}
	return RankInput{
		Kind:    rank.Ratio,
		Window:  v.window(cohortFrom, cohortTo),
		Options: rank.Options{MinN: minClusters},
		Data: rankData(v, durableClusters(v), func(k *cluster) rank.Cluster {
			return rank.Cluster{Change: k.landing.sha, Num: float64(k.surviving), Den: float64(k.added)}
		}),
	}, nil
}

// durableClusters returns the cohort's clusters that added a prod line: a
// landing that adds none (a pure rename, a whitespace-only or a test-only
// change) says nothing about durability.
func durableClusters(v *view) []*cluster {
	var out []*cluster
	for _, k := range changeClusters(v, v.window(cohortFrom, cohortTo), true, false) {
		if k.added > 0 {
			out = append(out, k)
		}
	}
	return out
}
