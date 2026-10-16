package rank

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

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

// AUCDiffInterval returns a 95% percentile interval of AUC(a) − AUC(b),
// where a and b are two scores of the same labelled changes: each element
// of pos is one meaningful change's pair {a, b}, and of neg one routine
// change's. Each of 1,000 replicates resamples the changes once, the
// meaningful and the routine apart as AUCInterval does, and takes both AUCs
// over the very changes it drew, so the two stay paired: their correlation
// on the same changes narrows the interval, which two separate intervals
// ignore. It is the bootstrap form of the paired comparison of two AUCs on
// one sample (DeLong, DeLong and Clarke-Pearson, "Comparing the Areas under
// Two or More Correlated Receiver Operating Characteristic Curves",
// Biometrics 44(3), 1988). validate compares a ranked metric with change
// size alone by it: the metric says more than size only when the interval
// lies above 0 (Separation.Verdict). The pairs are sorted first and the
// draws come from a stream of their own, derived from id, so the interval
// depends on the scores alone, as AUCInterval's does. With pos or neg empty
// there is no pair to compare, and both ends are 0. The inputs are not
// modified.
func AUCDiffInterval(id domain.MetricID, pos, neg [][2]float64) (lo, hi float64) {
	if len(pos) == 0 || len(neg) == 0 {
		return 0, 0
	}
	src := newStream(Seed(id, domain.Scope{Kind: domain.ScopeAll}, domain.Window{}), "auc-diff")
	byPair := func(x, y [2]float64) int { return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1])) }
	pos, neg = slices.SortedFunc(slices.Values(pos), byPair), slices.SortedFunc(slices.Values(neg), byPair)
	pa, pb, qa, qb := make([]float64, len(pos)), make([]float64, len(pos)), make([]float64, len(neg)), make([]float64, len(neg))
	replicates := make([]float64, defaultReplicates)
	for r := range replicates {
		for i := range pa {
			x := pos[bounded(src, uint64(len(pos)))]
			pa[i], pb[i] = x[0], x[1]
		}
		for i := range qa {
			x := neg[bounded(src, uint64(len(neg)))]
			qa[i], qb[i] = x[0], x[1]
		}
		replicates[r] = AUC(pa, qa) - AUC(pb, qb)
	}
	slices.Sort(replicates)
	return replicates[orderStatistic(len(replicates), loPerMille)], replicates[orderStatistic(len(replicates), hiPerMille)]
}

// The bar a ranked metric's labels clear (product.md, ADR 0014): an AUC of
// at least BarAUC on at least BarLabels of its scored labels. validate says
// whether labels clear it; label asks until each metric has BarLabels.
const (
	BarAUC    = 0.7
	BarLabels = 50
)

// Separation is what validate finds for one ranked metric on one set of
// labels: its AUC with the 95% interval and n, and the AUC of change size
// alone over the very changes it scored, with the paired interval of the
// difference. Verdict and Result say what it means, by the one rule
// docs/metrics.md#validity-checks states, for a lead's labels and for a
// published reference alike.
type Separation struct {
	AUC, Lo, Hi    float64 // AUC and AUCInterval of the metric's scores
	N              int     // the changes labelled meaningful or routine that it scored
	Size           float64 // AUC of change size alone over the same changes
	DiffLo, DiffHi float64 // AUCDiffInterval of the metric's score and size: AUC less Size
}

// The verdicts, in docs/metrics.md's words.
const (
	VerdictTooFew   = "cannot tell yet"
	VerdictNone     = "does not separate meaningful from routine changes"
	VerdictReverse  = "separates in reverse (meaningful changes score lower)"
	VerdictWeak     = "separates weakly, below the bar"
	VerdictBeyond   = "separates, beyond change size"
	VerdictLikeSize = "separates, but no better than change size"
)

// Verdict is the first of these that holds: fewer than BarLabels changes
// cannot tell yet; an interval that holds 0.5 does not separate; one below
// 0.5 separates in reverse; an AUC under BarAUC separates weakly, below the
// bar; and a metric at the bar separates beyond change size when the paired
// interval of its AUC less size alone's lies above 0, else no better than
// change size, since a metric that grows with size can separate the
// classes through size alone. So "separates" is never said of an interval
// that holds chance, and "beyond change size" never of a difference that
// could be 0.
func (s Separation) Verdict() string {
	switch {
	case s.N < BarLabels:
		return VerdictTooFew
	case s.Lo <= 0.5 && 0.5 <= s.Hi:
		return VerdictNone
	case s.Hi < 0.5:
		return VerdictReverse
	case s.AUC < BarAUC:
		return VerdictWeak
	case s.DiffLo > 0:
		return VerdictBeyond
	}
	return VerdictLikeSize
}

// Result is the verdict with the numbers it rests on, as a metric's Validity
// line publishes it: "does not separate meaningful from routine changes:
// AUC 0.42 (0.30–0.55) on 76, while change size alone gives 0.82". The two
// verdicts that compare with size add the difference and its paired
// interval, which decide between them.
func (s Separation) Result() string {
	v := s.Verdict()
	auc := "AUC " + twoPlaces(s.AUC) + " (" + twoPlaces(s.Lo) + "–" + twoPlaces(s.Hi) + ")"
	size := "while change size alone gives " + twoPlaces(s.Size)
	switch v {
	case VerdictTooFew:
		if s.Lo <= 0.5 && 0.5 <= s.Hi {
			return fmt.Sprintf("%s: n %d is under %d, and %s spans chance, %s", v, s.N, BarLabels, auc, size)
		}
		return fmt.Sprintf("%s: n %d is under %d: %s, %s", v, s.N, BarLabels, auc, size)
	case VerdictBeyond, VerdictLikeSize:
		return fmt.Sprintf("%s: %s on %d, %s, a difference of %s (%s to %s)", v, auc, s.N, size,
			twoPlaces(s.AUC-s.Size), twoPlaces(s.DiffLo), twoPlaces(s.DiffHi))
	}
	return fmt.Sprintf("%s: %s on %d, %s", v, auc, s.N, size)
}

// twoPlaces is x to two decimal places, never "-0.00".
func twoPlaces(x float64) string {
	s := strconv.FormatFloat(x, 'f', 2, 64)
	if s == "-0.00" {
		return "0.00"
	}
	return s
}
