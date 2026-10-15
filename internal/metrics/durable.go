package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// durableV1 is durable@1: of the non-blank prod lines a person's commits
// added in the cohort, the share that blame still attributes to those
// commits at HEAD. It is a Ratio metric: the bootstrap resamples a person's
// clusters (their counted commits in one landing), because lines inside one
// change are not independent. Both sides of the ratio classify a line by
// the path it had in its commit, so a file moved onto a prod path keeps no
// line its commit did not add as prod (#70), and both leave out the same
// lines, those whose survival blame cannot tell (untracedLines), so a file
// whose blame failed leaves the ratio instead of scoring its lines as lost,
// and a change git took for binary, whose added lines are unknown, leaves it
// instead of scoring its blamed lines as kept beyond what was added. That
// keeps the two sums in step; it does not bound the ratio by 1, because
// blame can still give a commit lines its own patch did not add: a copy
// inside one file goes to the commit that wrote the block (#63), and a
// reformat in the ignore-revs file that splits one line into several gives
// every part to the commit that wrote the line.
type durableV1 struct{}

var durableID = domain.MetricID{Name: "durable", Version: 1}

// Definition returns durable@1's catalog entry.
func (durableV1) Definition() Definition {
	return Definition{
		ID:       durableID,
		Question: "Did the code a person wrote last?",
		Unit: "Σ surviving / Σ added per person over their clusters in the cohort, landings in [now - 365d, now - 90d), per " +
			"repository, group and all. added: non-blank prod lines of each counted commit's own -U0 -w patch; surviving: the " +
			"non-blank lines blame -w -M (with the repository's ignore-revs file) attributes to it at HEAD whose path in that " +
			"commit is prod, whatever their path's role at HEAD. Both parts classify a line by the path it had in its commit " +
			"(the history's change, blame's filename), and lines whose survival blame cannot tell are in neither, matched by that " +
			"path: those of a change git took for binary (its added lines are unknown), wherever HEAD holds them, and those of a " +
			"file whose blame failed, or of a path the history renamed into one. A cluster is one person's counted commits in " +
			"one landing and needs at least one added line in the ratio. Merge commits, ignore-revs commits and revert landings " +
			"are excluded; a reverted landing stays and scores what blame gives it. Detail: reverted_landings.",
		Ratio:       true,
		Ranked:      true,
		Eligibility: "At least 10 clusters in the cohort (open decision 5).",
		BlindSpots: "A planned replacement of correct code looks not durable. Moves across files credit the mover; a move inside " +
			"one file counts as the mover's added lines, which blame gives back to their first author, so the mover scores 0 on " +
			"them (open decision 6). Blame can give a commit more lines than it added, so a value can pass 1: a copy inside one " +
			"file credits the block's original author (#63), and a reformat in the ignore-revs file that splits a line into " +
			"several gives every part to the line's author. A revert that restores deleted lines takes their blame from the " +
			"original author. A prod file later moved to a path blame does not read, such as a denied one under vendor/, keeps " +
			"its lines in added and none in surviving, so its author looks as if they kept nothing. Changes younger than 90 days " +
			"are not scored. Lines blamed to a merge commit (conflict resolutions) are left out in v1. A change git takes for " +
			"binary scores none of its lines.",
		Gaming: "Writing in stable, low-traffic code and avoiding risky areas. Load-bearing (M5) is the counterweight.",
		Oracle: "testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles (main fixture and sibling) and " +
			"testdata/fixture/SPEC.md#binary-files-fixturebinary (binary files, and text files moved across roles)",
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
			a.added += k.traced
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
			return rank.Cluster{Change: k.landing.sha, Num: float64(k.surviving), Den: float64(k.traced)}
		}),
	}, nil
}

// durableClusters returns the cohort's clusters that added a prod line
// whose survival blame can tell: a landing that adds none (a pure rename, a
// whitespace-only or a test-only change) says nothing about durability, and
// neither does one whose added lines are all untraced (on files whose blame
// failed, or in changes git took for binary).
func durableClusters(v *view) []*cluster {
	var out []*cluster
	for _, k := range changeClusters(v, v.window(cohortFrom, cohortTo), true, false) {
		if k.traced > 0 {
			out = append(out, k)
		}
	}
	return out
}
