// Package rank turns per-change measurements into honest ranks, one metric
// at a time (ADR 0001, docs/metrics.md#ranking-rules): a bootstrap gives each
// subject a 95% interval for its value and for its rank, subjects below the
// metric's minimum sample are listed and not ranked, and AUC scores a metric
// against hand labels for the validity check.
//
// It is part of the functional core: plain functions over the clusters a
// ranked metric hands it (metrics.RankInput), with no I/O. How a replicate is
// drawn depends on the metric's Kind:
//
//   - Ratio metrics, such as durable@1, resample each subject's clusters (its
//     changes) with replacement: a cluster bootstrap (Efron and Tibshirani,
//     An Introduction to the Bootstrap, 1993). Lines inside a change are not
//     independent, so resampling lines would give falsely narrow intervals.
//   - Sum metrics, such as simplifying@1, resample the window's week blocks,
//     the same blocks for every subject: a block bootstrap (Künsch, "The
//     Jackknife and the Bootstrap for General Stationary Observations", Ann.
//     Statist., 1989). A total depends on how many changes landed, which
//     resampling a subject's own clusters would hold fixed: for a pure count
//     every replicate would return the observed count.
//
// Every draw is reproducible. The seed derives from the metric, scope and
// window (Seed), each random stream from the seed, the generator is
// math/rand/v2's PCG, and the bounded draw is this package's own, so the same
// inputs give the same intervals on every platform and Go release.
package rank

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Kind says how a ranked metric aggregates its clusters.
type Kind int

// The kinds of ranked metric.
const (
	Ratio Kind = iota // Σ Num / Σ Den over a subject's clusters, such as durable@1
	Sum               // Σ Num, resampled by week block, such as simplifying@1
)

// Cluster is one change's contribution to one subject's value. Changes, not
// lines, are resampled, because lines inside a change are not independent.
type Cluster struct {
	Change string  // landing SHA or PR id
	Num    float64 // the change's numerator
	Den    float64 // Ratio only
	Block  int     // Sum only: the week (counted back from Now) the change landed in; 0 is the 7 days before Now
}

// Options tunes the bootstrap. The ranked metric supplies them
// (metrics.RankInput), because eligibility is part of its definition.
type Options struct {
	Replicates int // B; 0 means the default, 1000
	MinN       int // eligibility: clusters per subject, from the metric's definition
	Blocks     int // Sum only: week blocks in the window, numbered 0 to Blocks-1
}

const (
	// defaultReplicates is B when Options.Replicates is 0
	// (docs/metrics.md#ranking-rules).
	defaultReplicates = 1000

	// The interval is the central 95% of the replicates. Its ends are given
	// in thousandths, so the order statistics are computed in integers.
	loPerMille = 25
	hiPerMille = 975
)

// Intervals returns one rank interval per subject in data, sorted by subject
// (Kind, then Key), for metric id in scope over window w. It returns nil when
// data is empty.
//
// A subject is eligible with at least opt.MinN clusters, and at least one.
// Each of B replicates (opt.Replicates, or 1,000) resamples the eligible
// subjects' clusters as their Kind says (see the package comment) and ranks
// them by value, the largest first. Tied values share the best rank they
// cover, as in "1, 1, 3" (standard competition ranking), so a tie never
// ranks anyone below the subjects it ties with. Lo and Hi are the
// ⌈0.025·B⌉-th and ⌈0.975·B⌉-th of the sorted replicate values, the 25th and
// the 975th of 1,000 (the percentile interval of Efron and Tibshirani, 1993,
// ch. 13), and RankLo and RankHi are the same order statistics of the
// subject's ranks.
//
// A subject that is not eligible is listed and not ranked: Estimate and N are
// set, it takes no rank in any replicate, and Lo, Hi, RankLo and RankHi are 0.
// Below the minimum sample a bootstrap understates its own uncertainty (a
// ratio over one cluster always gets a zero-width interval), so no interval
// is reported.
//
// Estimate is Σ Num over the subject's clusters for Sum, and Σ Num / Σ Den
// for Ratio, where a zero Σ Den gives 0 so that no NaN reaches a report.
// Values are compared exactly: sums of counts are exact in float64, and equal
// ratios of counts round to the same float64, so they tie. The result
// depends on each subject's set of clusters, not on their order, and a Ratio
// subject's value interval depends on its own clusters only. Intervals ranks
// every subject it is given: leaving bots out is the caller's job
// (docs/metrics.md#ranking-rules).
func Intervals(id domain.MetricID, scope domain.Scope, w domain.Window, kind Kind, data map[domain.Subject][]Cluster, opt Options) []domain.RankInterval {
	if len(data) == 0 {
		return nil
	}
	subjects := make([]domain.Subject, 0, len(data))
	for s := range data {
		subjects = append(subjects, s)
	}
	slices.SortFunc(subjects, compareSubjects)

	minN := max(opt.MinN, 1)
	out := make([]domain.RankInterval, len(subjects))
	all := make([][]Cluster, len(subjects)) // each subject's clusters, sorted
	var eligible []int                      // indexes into subjects
	for i, s := range subjects {
		all[i] = sortedClusters(data[s])
		out[i] = domain.RankInterval{
			Metric:   id,
			Scope:    scope,
			Subject:  s,
			Window:   w,
			Estimate: canonical(estimate(kind, all[i])),
			N:        len(all[i]),
			Eligible: len(all[i]) >= minN,
		}
		if out[i].Eligible {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) == 0 {
		return out
	}

	b := opt.Replicates
	if b <= 0 {
		b = defaultReplicates
	}
	seed := Seed(id, scope, w)
	var values [][]float64 // values[j][r]: eligible subject j's value in replicate r
	if kind == Sum {
		values = blockReplicates(seed, weekBlocks(opt.Blocks, all), pick(all, eligible), b)
	} else {
		values = clusterReplicates(seed, pick(subjects, eligible), pick(all, eligible), b)
	}
	ranks := competitionRanks(values)

	lo, hi := orderStatistic(b, loPerMille), orderStatistic(b, hiPerMille)
	for j, i := range eligible {
		slices.Sort(values[j])
		slices.Sort(ranks[j])
		out[i].Lo, out[i].Hi = canonical(values[j][lo]), canonical(values[j][hi])
		out[i].RankLo, out[i].RankHi = ranks[j][lo], ranks[j][hi]
	}
	return out
}

// clusterReplicates draws b replicates of each Ratio subject's value. A
// subject's replicate resamples as many of its clusters as it has, with
// replacement, from the subject's own stream, so its draws do not depend on
// the other subjects.
func clusterReplicates(seed uint64, subjects []domain.Subject, clusters [][]Cluster, b int) [][]float64 {
	values := make([][]float64, len(clusters))
	for j, cs := range clusters {
		src := newStream(seed, "subject", subjects[j].Kind, subjects[j].Key)
		n := uint64(len(cs))
		values[j] = make([]float64, b)
		for r := range b {
			var num, den float64
			for range cs {
				c := cs[bounded(src, n)]
				num += c.Num
				den += c.Den
			}
			values[j][r] = ratio(num, den)
		}
	}
	return values
}

// blockReplicates draws b replicates of each Sum subject's value. A
// replicate draws as many week blocks as labels holds, with replacement, the
// same blocks for every subject, and each subject's value is the sum of its
// totals in the drawn blocks. Drawing the same weeks for everyone keeps what
// a week did to the whole team, such as a holiday, out of the comparison
// between subjects.
func blockReplicates(seed uint64, labels []int, clusters [][]Cluster, b int) [][]float64 {
	column := make(map[int]int, len(labels))
	for i, l := range labels {
		column[l] = i
	}
	totals := make([][]float64, len(clusters)) // totals[j][k]: subject j's Σ Num in block labels[k]
	for j, cs := range clusters {
		totals[j] = make([]float64, len(labels))
		for _, c := range cs {
			totals[j][column[c.Block]] += c.Num
		}
	}

	src := newStream(seed, "blocks")
	drawn := make([]uint64, len(labels))
	values := make([][]float64, len(clusters))
	for j := range values {
		values[j] = make([]float64, b)
	}
	for r := range b {
		for k := range drawn {
			drawn[k] = bounded(src, uint64(len(labels)))
		}
		for j, t := range totals {
			var v float64
			for _, k := range drawn {
				v += t[k]
			}
			values[j][r] = v
		}
	}
	return values
}

// weekBlocks returns the block labels a Sum replicate draws from, sorted:
// every week of the window, 0 to blocks-1, including weeks in which nothing
// landed, plus any other Block a cluster names, so that no cluster is
// dropped.
func weekBlocks(blocks int, all [][]Cluster) []int {
	labels := make(map[int]bool, max(blocks, 0))
	for k := range max(blocks, 0) {
		labels[k] = true
	}
	for _, cs := range all {
		for _, c := range cs {
			labels[c.Block] = true
		}
	}
	return slices.Sorted(maps.Keys(labels))
}

// competitionRanks ranks the subjects within each replicate, the largest
// value first. Tied values share the best rank they cover, as in "1, 1, 3",
// so the order the sort leaves tied subjects in does not matter.
func competitionRanks(values [][]float64) [][]int {
	if len(values) == 0 {
		return nil
	}
	ranks := make([][]int, len(values))
	for j := range ranks {
		ranks[j] = make([]int, len(values[j]))
	}
	order := make([]int, len(values))
	for r := range values[0] {
		for j := range order {
			order[j] = j
		}
		slices.SortFunc(order, func(x, y int) int { return cmp.Compare(values[y][r], values[x][r]) })
		for pos, j := range order {
			ranks[j][r] = pos + 1
			if pos > 0 {
				if prev := order[pos-1]; cmp.Compare(values[j][r], values[prev][r]) == 0 {
					ranks[j][r] = ranks[prev][r]
				}
			}
		}
	}
	return ranks
}

// orderStatistic returns the 0-based index of the ⌈b·perMille/1000⌉-th
// smallest of b sorted values: the inverse of their empirical distribution
// function at perMille/1000.
func orderStatistic(b, perMille int) int {
	k := (int64(b)*int64(perMille) + 999) / 1000
	return int(min(max(k, 1), int64(b))) - 1
}

// estimate is the subject's value over its clusters.
func estimate(kind Kind, cs []Cluster) float64 {
	var num, den float64
	for _, c := range cs {
		num += c.Num
		den += c.Den
	}
	if kind == Sum {
		return num
	}
	return ratio(num, den)
}

// ratio is num/den, and 0 when den is 0: a ratio over no denominator is
// undefined, and a NaN would make the report unencodable.
func ratio(num, den float64) float64 {
	if den == 0 {
		return 0
	}
	return num / den
}

// canonical turns -0 into 0, so that a value prints the same whichever
// order of equal values produced it.
func canonical(x float64) float64 {
	if x == 0 {
		return 0
	}
	return x
}

// sortedClusters returns a sorted copy of cs, so that a subject's replicates
// depend on its set of clusters and not on the order the caller built them
// in, such as a walk over a map.
func sortedClusters(cs []Cluster) []Cluster {
	cs = slices.Clone(cs)
	slices.SortFunc(cs, func(a, b Cluster) int {
		return cmp.Or(
			strings.Compare(a.Change, b.Change),
			cmp.Compare(a.Block, b.Block),
			cmp.Compare(a.Num, b.Num),
			cmp.Compare(a.Den, b.Den),
		)
	})
	return cs
}

func compareSubjects(a, b domain.Subject) int {
	return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Key, b.Key))
}

// pick returns the elements of xs at the given indexes, in order.
func pick[T any](xs []T, indexes []int) []T {
	out := make([]T, len(indexes))
	for j, i := range indexes {
		out[j] = xs[i]
	}
	return out
}
