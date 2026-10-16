package rank_test

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
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
