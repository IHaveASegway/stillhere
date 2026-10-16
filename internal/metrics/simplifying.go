package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// simplifyingV1 is simplifying@1: the non-blank prod lines a person's
// counted commits removed in the last 90 days, leaving out every commit of a
// reverted landing. It is a Sum metric: resampling a person's own clusters
// would always return the same sum, so the bootstrap resamples week blocks
// shared by everyone (#10), and Clusters tags each cluster with its block.
type simplifyingV1 struct{}

var simplifyingID = domain.MetricID{Name: "simplifying", Version: 1}

// Definition returns simplifying@1's catalog entry.
func (simplifyingV1) Definition() Definition {
	return Definition{
		ID:       simplifyingID,
		Question: "Did a person remove code, and did the removal stick?",
		Unit: "Σ removed per person over their clusters in [now - 90d, now), per repository, group and all: non-blank prod " +
			"lines removed by each counted commit's own -U0 -w patch. Every cluster counts, including one that removed nothing. " +
			"Merge commits, ignore-revs commits and revert landings are excluded, and so is every commit of a reverted landing. " +
			"Detail: simplifying_landings, the person's landings whose counted commits net Σ added - Σ removed <= -20.",
		Ranked:      true,
		Subjects:    []string{domain.SubjectPerson},
		Eligibility: "At least 10 clusters in the window.",
		BlindSpots: "Deleting a feature looks like simplifying (deleting its tests does not: only prod lines count). A move counts " +
			"its lines as removed, though its net is 0, so it is never a simplifying landing by itself. Removed code that comes " +
			"back later is not detected in v1.",
		Gaming:   "Deleting then re-adding. Revert detection catches the plain case: neither the reverted landing nor the revert counts.",
		Oracle:   "testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles (main fixture and sibling)",
		Validity: "Unvalidated until the M2 labelled-sample check (stillhere validate, AUC >= 0.7 on at least 50 labelled changes).",
	}
}

// Compute returns simplifying@1's values, bots included: they are listed in
// the bot table, never ranked.
func (m simplifyingV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (simplifyingV1) compute(v *view) ([]domain.MetricValue, error) {
	clusters, simplifying := simplifyingClusters(v)
	type acc struct{ removed, n, simplifying int }
	accs := make(map[personKey]*acc)
	for _, k := range clusters {
		for _, s := range k.repo.scopes {
			a := lookup(accs, personKey{s, k.person})
			a.removed += k.removed
			a.n++
			if simplifying[k.landing] {
				a.simplifying++
			}
		}
	}
	w := v.window(recentDays, 0)
	out := make([]domain.MetricValue, 0, len(accs))
	for k, a := range accs {
		out = append(out, count(simplifyingID, k.scope, personSubject(k.email), w, a.removed, a.n, map[string]float64{
			"simplifying_landings": float64(a.simplifying),
		}))
	}
	return sorted(out), nil
}

// Clusters returns simplifying@1's rank input for one scope: each cluster's
// removed lines, tagged with the window's week block its landing is in
// (domain.Window.Week, the rule rank checks).
func (simplifyingV1) Clusters(in *Input, scope domain.Scope) (RankInput, error) {
	v, err := scopeView(in, scope)
	if err != nil {
		return RankInput{}, err
	}
	clusters, _ := simplifyingClusters(v)
	w := v.window(recentDays, 0)
	return RankInput{
		Window:  w,
		Options: rank.Options{MinN: minClusters, Blocks: w.Weeks()},
		Data: rankData(clusters, func(k *cluster) rank.Cluster {
			return rank.Cluster{Change: k.landing.sha, Num: float64(k.removed), Block: w.Week(k.landing.time)}
		}),
	}, nil
}

// simplifyingClusters returns the window's clusters and the landings among
// them that are simplifying: the net of a landing's counted commits, all
// people together, is -20 lines or fewer, and -20 counts.
func simplifyingClusters(v *view) ([]*cluster, map[*landingView]bool) {
	clusters := changeClusters(v, v.window(recentDays, 0), false, true)
	net := make(map[*landingView]int)
	for _, k := range clusters {
		net[k.landing] += k.added - k.removed
	}
	simplifying := make(map[*landingView]bool)
	for l, n := range net {
		if n <= simplifyingNet {
			simplifying[l] = true
		}
	}
	return clusters, simplifying
}
