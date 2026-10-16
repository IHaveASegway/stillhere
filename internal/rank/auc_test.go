package rank_test

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// TestAUC is the contract test of the validity check's statistic
// (docs/metrics.md#validity-checks, docs/spec/codebase.md#tests): the share
// of (meaningful, routine) pairs that a metric orders correctly, a tie
// counting one half. It decides whether a ranked metric leaves "beta".
func TestAUC(t *testing.T) {
	for _, c := range []struct {
		name     string
		pos, neg []float64
		want     float64
	}{
		// 6 pairs: 3 beats both negatives, and each 2 beats the 1 and ties
		// the other 2, so 2U = 2·4 + 2 and AUC = 10/12.
		{"a hand-counted example with ties", []float64{3, 2, 2}, []float64{2, 1}, 5.0 / 6},
		{"every positive above every negative", []float64{5, 4}, []float64{3, 1, 2}, 1},
		{"every positive below every negative", []float64{1, 2}, []float64{4, 3}, 0},
		{"every value tied", []float64{1, 1}, []float64{1, 1, 1}, 0.5},
		// With no pair to compare there is no evidence either way: 0.5,
		// never NaN, which encoding/json would refuse.
		{"no positives", nil, []float64{1}, 0.5},
		{"no negatives", []float64{1}, nil, 0.5},
		{"no labels", nil, nil, 0.5},
	} {
		pos, neg := slices.Clone(c.pos), slices.Clone(c.neg)
		if got := rank.AUC(pos, neg); got != c.want {
			t.Errorf("%s: AUC(%v, %v) = %v, want %v", c.name, c.pos, c.neg, got, c.want)
		}
		if !slices.Equal(pos, c.pos) || !slices.Equal(neg, c.neg) {
			t.Errorf("%s: AUC modified its input", c.name)
		}
	}

	// AUC's one walk over both sorted inputs against counting every pair, on
	// values from a few levels so that ties are common. NaN is a level
	// because AUC orders values as cmp.Compare does, NaN first.
	src := rand.New(rand.NewPCG(10, 13))
	levels := []float64{math.NaN(), math.Inf(-1), -1, 0, 0.5, 1, 2, math.Inf(1)}
	draw := func() []float64 {
		xs := make([]float64, src.IntN(12))
		for i := range xs {
			xs[i] = levels[src.IntN(len(levels))]
		}
		return xs
	}
	for range 2000 {
		pos, neg := draw(), draw()
		if got, want := rank.AUC(pos, neg), countPairs(pos, neg); got != want {
			t.Fatalf("AUC(%v, %v) = %v, but counting every pair gives %v", pos, neg, got, want)
		}
	}

	// Its 95% interval (validate prints it beside the AUC): a perfect
	// separation leaves no replicate below 1, a sample holds its own AUC,
	// the same labels give the same interval, in any order, and with no
	// pair to compare both ends are 0.5, as the AUC is.
	id := domain.MetricID{Name: "durable", Version: 1}
	if lo, hi := rank.AUCInterval(id, []float64{5, 4}, []float64{3, 1, 2}); lo != 1 || hi != 1 {
		t.Errorf("AUCInterval of a perfect separation is [%v, %v], want [1, 1]", lo, hi)
	}
	pos, neg := []float64{3, 2, 2, 5, 1, 4}, []float64{2, 1, 0, 4, 3}
	lo, hi := rank.AUCInterval(id, pos, neg)
	if auc := rank.AUC(pos, neg); lo > auc || hi < auc || lo >= hi {
		t.Errorf("AUCInterval(%v, %v) is [%v, %v], which does not hold the AUC %v with some width", pos, neg, lo, hi, auc)
	}
	if lo2, hi2 := rank.AUCInterval(id, pos, neg); lo2 != lo || hi2 != hi {
		t.Errorf("AUCInterval gives [%v, %v] and then [%v, %v] for the same labels", lo, hi, lo2, hi2)
	}
	rpos, rneg := slices.Clone(pos), slices.Clone(neg)
	slices.Reverse(rpos)
	slices.Reverse(rneg)
	if lo2, hi2 := rank.AUCInterval(id, rpos, rneg); lo2 != lo || hi2 != hi {
		t.Errorf("AUCInterval gives [%v, %v] for the labels and [%v, %v] for the same labels in reverse order", lo, hi, lo2, hi2)
	}
	if lo, hi := rank.AUCInterval(id, nil, neg); lo != 0.5 || hi != 0.5 {
		t.Errorf("AUCInterval with no positives is [%v, %v], want [0.5, 0.5]", lo, hi)
	}

	// The paired interval of one score's AUC less another's on the same
	// changes (validate's metric against change size alone): two equal
	// scores differ by 0 in every replicate; a perfect separation against
	// its reverse by 1 in every one, since each replicate keeps both
	// classes; the same pairs give the same interval in any order; and a
	// sample's interval holds its own difference. The pairing is what
	// matters: b is a plus noise, so the two AUCs move together over the
	// replicates and their difference varies less than either.
	paired := func(as, bs []float64) [][2]float64 {
		out := make([][2]float64, len(as))
		for i := range as {
			out[i] = [2]float64{as[i], bs[i]}
		}
		return out
	}
	same := paired(pos, pos)
	if lo, hi := rank.AUCDiffInterval(id, same, paired(neg, neg)); lo != 0 || hi != 0 {
		t.Errorf("AUCDiffInterval of a score against itself is [%v, %v], want [0, 0]", lo, hi)
	}
	if lo, hi := rank.AUCDiffInterval(id, paired([]float64{5, 4}, []float64{0, 1}), paired([]float64{3, 1, 2}, []float64{4, 3, 5})); lo != 1 || hi != 1 {
		t.Errorf("AUCDiffInterval of a perfect separation against its reverse is [%v, %v], want [1, 1]", lo, hi)
	}
	ppos, pneg := paired(pos, []float64{3.5, 1, 2.5, 4, 1.5, 3}), paired(neg, []float64{2.5, 0.5, 1, 3, 3.5})
	dlo, dhi := rank.AUCDiffInterval(id, ppos, pneg)
	diff := rank.AUC(pos, neg) - rank.AUC([]float64{3.5, 1, 2.5, 4, 1.5, 3}, []float64{2.5, 0.5, 1, 3, 3.5})
	if dlo > diff || dhi < diff {
		t.Errorf("AUCDiffInterval is [%v, %v], which does not hold the difference %v", dlo, dhi, diff)
	}
	rppos, rpneg := slices.Clone(ppos), slices.Clone(pneg)
	slices.Reverse(rppos)
	slices.Reverse(rpneg)
	if lo2, hi2 := rank.AUCDiffInterval(id, rppos, rpneg); lo2 != dlo || hi2 != dhi {
		t.Errorf("AUCDiffInterval gives [%v, %v] for the pairs and [%v, %v] for the same pairs in reverse order", dlo, dhi, lo2, hi2)
	}
	if lo, hi := rank.AUCDiffInterval(id, nil, pneg); lo != 0 || hi != 0 {
		t.Errorf("AUCDiffInterval with no positives is [%v, %v], want [0, 0]", lo, hi)
	}
	near := func(xs []float64) []float64 {
		out := make([]float64, len(xs))
		for i, x := range xs {
			out[i] = x + 0.01*float64(i%3)
		}
		return out
	}
	big := []float64{9, 8, 8, 7, 6, 6, 5, 5, 4, 3, 7, 9, 2, 6, 8}
	small := []float64{1, 2, 3, 3, 4, 4, 5, 6, 2, 1, 3, 5, 4, 2, 7}
	plo, phi := rank.AUCDiffInterval(id, paired(big, near(big)), paired(small, near(small)))
	alo, ahi := rank.AUCInterval(id, big, small)
	if phi-plo >= ahi-alo {
		t.Errorf("AUCDiffInterval of two scores that move together is %v wide, no narrower than either AUC's interval (%v)", phi-plo, ahi-alo)
	}

	// The verdict a Validity line and validate publish (docs/metrics.md's
	// rule), the first that holds, at each boundary.
	for _, c := range []struct {
		name string
		s    rank.Separation
		want string
	}{
		{"n under the bar", rank.Separation{AUC: 0.9, Lo: 0.8, Hi: 0.97, N: 49, Size: 0.5, DiffLo: 0.2, DiffHi: 0.5}, rank.VerdictTooFew},
		{"an interval that holds chance", rank.Separation{AUC: 0.72, Lo: 0.5, Hi: 0.9, N: 50, Size: 0.5}, rank.VerdictNone},
		{"an interval below chance", rank.Separation{AUC: 0.3, Lo: 0.18, Hi: 0.42, N: 80, Size: 0.8}, rank.VerdictReverse},
		{"an interval above chance, an AUC below the bar", rank.Separation{AUC: 0.65, Lo: 0.58, Hi: 0.72, N: 200, Size: 0.6}, rank.VerdictWeak},
		{"at the bar, a difference that could be 0", rank.Separation{AUC: 0.7, Lo: 0.6, Hi: 0.8, N: 50, Size: 0.6, DiffLo: 0, DiffHi: 0.2},
			rank.VerdictLikeSize},
		{"at the bar, below size", rank.Separation{AUC: 0.76, Lo: 0.63, Hi: 0.88, N: 65, Size: 0.87, DiffLo: -0.22, DiffHi: 0.01},
			rank.VerdictLikeSize},
		{"at the bar, a difference above 0", rank.Separation{AUC: 0.85, Lo: 0.75, Hi: 0.93, N: 60, Size: 0.7, DiffLo: 0.01, DiffHi: 0.3},
			rank.VerdictBeyond},
	} {
		if got := c.s.Verdict(); got != c.want {
			t.Errorf("%s: %+v's verdict is %q, want %q", c.name, c.s, got, c.want)
		}
		if got := c.s.Result(); !strings.HasPrefix(got, c.want+": ") {
			t.Errorf("%s: %+v's result %q does not start with its verdict %q", c.name, c.s, got, c.want)
		}
	}
	for _, c := range []struct {
		s    rank.Separation
		want string
	}{
		{rank.Separation{AUC: 0.4215, Lo: 0.2962, Hi: 0.5451, N: 76, Size: 0.8160, DiffLo: -0.55, DiffHi: -0.23},
			"does not separate meaningful from routine changes: AUC 0.42 (0.30–0.55) on 76, while change size alone gives 0.82"},
		{rank.Separation{AUC: 0.5509, Lo: 0.4018, Hi: 0.7070, N: 49, Size: 0.8851},
			"cannot tell yet: n 49 is under 50, and AUC 0.55 (0.40–0.71) spans chance, while change size alone gives 0.89"},
		{rank.Separation{AUC: 0.8, Lo: 0.6, Hi: 0.9, N: 40, Size: 0.6},
			"cannot tell yet: n 40 is under 50: AUC 0.80 (0.60–0.90), while change size alone gives 0.60"},
		{rank.Separation{AUC: 0.7603, Lo: 0.6277, Hi: 0.8766, N: 65, Size: 0.8674, DiffLo: -0.2202, DiffHi: -0.0001},
			"separates, but no better than change size: AUC 0.76 (0.63–0.88) on 65, while change size alone gives 0.87, " +
				"a difference of -0.11 (-0.22 to 0.00)"},
	} {
		if got := c.s.Result(); got != c.want {
			t.Errorf("%+v's result is %q, want %q", c.s, got, c.want)
		}
	}
}

// countPairs is AUC by its definition: over every (positive, negative) pair,
// a larger positive counts 1 and a tie 1/2.
func countPairs(pos, neg []float64) float64 {
	if len(pos) == 0 || len(neg) == 0 {
		return 0.5
	}
	var twice int
	for _, x := range pos {
		for _, y := range neg {
			switch cmp.Compare(x, y) {
			case 1:
				twice += 2
			case 0:
				twice++
			}
		}
	}
	return float64(twice) / (2 * float64(len(pos)) * float64(len(neg)))
}
