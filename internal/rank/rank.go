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
	"fmt"
	"slices"
	"strings"
	"time"

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

	// Block is Sum only: the window's 7-day block the change landed in,
	// counted back from the window's end, which is Now for every Sum metric.
	// Block k holds the landings in [Until − 7(k+1) days, Until − 7k days),
	// half-open like the window, so a landing at time t is in block
	// ⌈(Until − t) / 7 days⌉ − 1, and 0 is the last 7 days. It must be in
	// [0, Options.Blocks).
	Block int
}

// Options tunes the bootstrap. The ranked metric supplies them
// (metrics.RankInput), because eligibility is part of its definition.
type Options struct {
	Replicates int // B; 0 means the default, 1000
	MinN       int // eligibility: clusters per subject, from the metric's definition

	// Blocks is Sum only: the number of 7-day blocks the window holds,
	// ⌈(Until − Since) / 7 days⌉, the oldest one short when the window is
	// not a whole number of weeks: 13 for 90 days. Intervals checks it
	// against the window.
	Blocks int
}

const (
	// defaultReplicates is B when Options.Replicates is 0
	// (docs/metrics.md#ranking-rules).
	defaultReplicates = 1000

	// week is the length of a Sum metric's blocks
	// (docs/metrics.md#ranking-rules).
	week = 7 * 24 * time.Hour

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
// subject's value interval depends on its own clusters only. Two Sum
// subjects with the same totals in every block get the same intervals,
// because every replicate draws the same blocks for both; two Ratio subjects
// with the same clusters get intervals that agree only up to Monte Carlo
// error, because each draws from its own stream. Intervals ranks every
// subject it is given: leaving bots out is the caller's job
// (docs/metrics.md#ranking-rules).
//
// Intervals panics on input that only a bug in the ranked metric can
// produce, because such input would otherwise give plausible but wrong
// intervals: a Kind other than Ratio and Sum and, for Sum, an empty window,
// an Options.Blocks other than the number of blocks in w, or a cluster whose
// Block is outside [0, Blocks), whether or not its subject is eligible. Too
// few blocks hide a count's uncertainty, and too many inflate it. The
// options are checked even when data is empty, so a misconfigured metric
// fails on its first call.
func Intervals(id domain.MetricID, scope domain.Scope, w domain.Window, kind Kind, data map[domain.Subject][]Cluster, opt Options) []domain.RankInterval {
	blocks := checkOptions(kind, w, opt)
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
		if kind == Sum {
			checkBlocks(all[i], blocks)
		}
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
		values = blockReplicates(seed, blocks, pick(all, eligible), b)
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
// replicate draws the window's blocks, as many as it holds, with
// replacement, the same blocks for every subject, and each subject's value
// is the sum of its totals in the drawn blocks. Drawing the same weeks for
// everyone keeps what a week did to the whole team, such as a holiday, out
// of the comparison between subjects. Every block of the window can be
// drawn, including one in which nothing landed, and checkBlocks has put
// every cluster in one of them, so no subject's data changes how many
// blocks a replicate draws.
func blockReplicates(seed uint64, blocks int, clusters [][]Cluster, b int) [][]float64 {
	totals := make([][]float64, len(clusters)) // totals[j][k]: subject j's Σ Num in block k
	for j, cs := range clusters {
		totals[j] = make([]float64, blocks)
		for _, c := range cs {
			totals[j][c.Block] += c.Num
		}
	}

	src := newStream(seed, "blocks")
	drawn := make([]uint64, blocks)
	values := make([][]float64, len(clusters))
	for j := range values {
		values[j] = make([]float64, b)
	}
	for r := range b {
		for k := range drawn {
			drawn[k] = bounded(src, uint64(blocks))
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

// checkOptions returns the number of 7-day blocks a Sum replicate draws over
// w, and 0 for Ratio, which has no blocks. It panics on what only a bug in
// the ranked metric can cause: an unknown kind or, for Sum, an empty window
// or an opt.Blocks that is not the window's count, such as a count of the
// weeks in which something landed (fail fast: Shore, "Fail Fast", IEEE
// Software, 2004).
func checkOptions(kind Kind, w domain.Window, opt Options) int {
	switch kind {
	case Ratio:
		return 0
	case Sum:
		d := w.Until.Sub(w.Since)
		if d <= 0 {
			panic(fmt.Sprintf("rank: a Sum metric's window [%s, %s) is empty",
				w.Since.Format(time.RFC3339), w.Until.Format(time.RFC3339)))
		}
		blocks := int((d + week - 1) / week)
		if opt.Blocks != blocks {
			panic(fmt.Sprintf("rank: a Sum metric's %g-day window holds %d week blocks, but Options.Blocks is %d",
				d.Hours()/24, blocks, opt.Blocks))
		}
		return blocks
	default:
		panic(fmt.Sprintf("rank: unknown Kind %d", kind))
	}
}

// checkBlocks panics when a Sum cluster's Block is outside [0, blocks): only
// a bug in the ranked metric puts a change outside its window's weeks, such
// as a landing from outside the window, or a block counted in days.
func checkBlocks(cs []Cluster, blocks int) {
	for _, c := range cs {
		if c.Block < 0 || c.Block >= blocks {
			panic(fmt.Sprintf("rank: change %q is in week block %d, outside the window's blocks 0 to %d",
				c.Change, c.Block, blocks-1))
		}
	}
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
