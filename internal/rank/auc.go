package rank

import (
	"cmp"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// AUC returns the Mann-Whitney area under the ROC curve: the probability that
// a value from pos exceeds one from neg, counting ties as one half. It is the
// Mann-Whitney U statistic over len(pos)·len(neg) pairs (Mann and Whitney,
// Ann. Math. Statist., 1947), which equals the area under the empirical ROC
// curve (Hanley and McNeil, "The Meaning and Use of the Area under a Receiver
// Operating Characteristic (ROC) Curve", Radiology, 1982): 1 when every
// positive scores above every negative, 0.5 at chance, 0 when the order is
// reversed. stillhere validate reports it per metric version, with pos the
// values of changes labelled meaningful and neg those labelled routine
// (docs/metrics.md#validity-checks).
//
// Values are ordered as cmp.Compare orders them, and the pairs are counted in
// integers, so the result is exact up to its one division. With no pair to
// compare (pos or neg empty) there is no evidence either way, and AUC returns
// 0.5, never NaN; callers report the sample size next to it. The inputs are
// not modified.
func AUC(pos, neg []float64) float64 {
	if len(pos) == 0 || len(neg) == 0 {
		return 0.5
	}
	p, q := slices.Clone(pos), slices.Clone(neg)
	slices.Sort(p)
	slices.Sort(q)

	// One ascending walk over both: for each positive x, below counts the
	// negatives smaller than x and upTo those no larger, so upTo-below are
	// its ties. 2U counts two per pair a positive wins and one per tie.
	var twiceU uint64
	below, upTo := 0, 0
	for _, x := range p {
		for below < len(q) && cmp.Less(q[below], x) {
			below++
		}
		for upTo < len(q) && cmp.Compare(q[upTo], x) <= 0 {
			upTo++
		}
		twiceU += 2*uint64(below) + uint64(upTo-below)
	}
	return float64(twiceU) / (2 * float64(len(p)) * float64(len(q)))
}

// AUCInterval returns a 95% percentile interval of AUC(pos, neg) for the
// metric id: 1,000 bootstrap replicates, each resampling pos and neg with
// replacement and apart, so every replicate keeps as many meaningful and as
// many routine labels as the sample (a bootstrap stratified by class), and
// the interval runs from the 25th to the 975th of the sorted replicates, as
// the rank intervals do (Efron and Tibshirani, An Introduction to the
// Bootstrap, 1993, ch. 13). DeLong, DeLong and Clarke-Pearson's variance
// (Biometrics, 1988) is the analytic alternative; the bootstrap needs no
// normal approximation, which a sample of 50 labels and an AUC near 1
// strain. The draws come from a stream derived from id alone, and resample
// pos and neg sorted, so the interval depends on the scores alone, not on
// the order of the labels file's rows: the same labels give the same
// interval in any order, on every platform and Go release. With pos or neg
// empty there is no pair to compare, and both ends are 0.5, as AUC is. The
// inputs are not modified.
func AUCInterval(id domain.MetricID, pos, neg []float64) (lo, hi float64) {
	if len(pos) == 0 || len(neg) == 0 {
		return 0.5, 0.5
	}
	src := newStream(Seed(id, domain.Scope{Kind: domain.ScopeAll}, domain.Window{}), "auc")
	pos, neg = slices.Sorted(slices.Values(pos)), slices.Sorted(slices.Values(neg))
	p, q := make([]float64, len(pos)), make([]float64, len(neg))
	replicates := make([]float64, defaultReplicates)
	for r := range replicates {
		for i := range p {
			p[i] = pos[bounded(src, uint64(len(pos)))]
		}
		for i := range q {
			q[i] = neg[bounded(src, uint64(len(neg)))]
		}
		replicates[r] = AUC(p, q)
	}
	slices.Sort(replicates)
	return replicates[orderStatistic(len(replicates), loPerMille)], replicates[orderStatistic(len(replicates), hiPerMille)]
}
