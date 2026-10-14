package rank

import (
	"cmp"
	"slices"
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
