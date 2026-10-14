package rank_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

var (
	durable     = domain.MetricID{Name: "durable", Version: 1}
	simplifying = domain.MetricID{Name: "simplifying", Version: 1}
	sibling     = domain.Scope{Kind: domain.ScopeRepo, Key: "local:sibling"}
	// The windows of testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles.
	cohort = domain.Window{Since: fixture.Now.AddDate(0, 0, -365), Until: fixture.Now.AddDate(0, 0, -90)} // durable@1
	window = domain.Window{Since: fixture.Now.AddDate(0, 0, -90), Until: fixture.Now}                     // simplifying@1
)

// TestRankIntervals is the contract test of the bootstrap
// (docs/metrics.md#ranking-rules, docs/spec/codebase.md#tests): the same
// inputs give the same intervals, on every machine and Go release, and
// subjects below the minimum sample are listed and not ranked.
//
// Except where a case says it pins this implementation's output, every
// expected interval is the exact 2.5% or 97.5% point of the bootstrap
// distribution, worked out by hand, which B = 1,000 replicates reproduce.
func TestRankIntervals(t *testing.T) {
	t.Run("the sibling's durable@1 clusters", func(t *testing.T) {
		// Lena has exactly 10 clusters, the minimum, and is the only one
		// ranked. Every cluster adds 3 lines, so a replicate is
		// (3a + 2b)/30 for (a, b, c) ~ Multinomial(10; 0.8, 0.1, 0.1): the
		// counts of her 3/3 clusters, of L2 (2/3) and of L7 (0/3). Its
		// distribution puts 2.3% below 20/30 and 4.7% at or below it, and
		// 89% below 1.
		got := rank.Intervals(durable, sibling, cohort, rank.Ratio, siblingDurable(), rank.Options{MinN: 10})
		check(t, got, durable, sibling, cohort,
			row{"lena@example.com", 26.0 / 30, 20.0 / 30, 1, 1, 1, 10, true},
			row{key: "omar@example.com", estimate: 1, n: 9},
			row{key: "pia@example.com", estimate: 1, n: 2},
			row{key: "quinn@example.com", estimate: 1, n: 1},
		)
	})

	t.Run("the sibling's simplifying@1 clusters", func(t *testing.T) {
		// Lena removed 22 lines in week 3 (S1) and 1 in week 10 (W2), so a
		// replicate of 13 drawn weeks is 22·c₃ + c₁₀. Missing both weeks has
		// probability (11/13)¹³ = 11%, and the 97.5% point is S1's week drawn
		// three times and W2's twice.
		got := rank.Intervals(simplifying, sibling, window, rank.Sum, siblingSimplifying(), rank.Options{MinN: 10, Blocks: 13})
		check(t, got, simplifying, sibling, window,
			row{"lena@example.com", 23, 0, 68, 1, 1, 10, true},
			row{key: "omar@example.com", estimate: 21, n: 2},
			row{key: "pia@example.com", estimate: 0, n: 1},
		)
	})

	t.Run("subjects below the minimum are listed and take no rank", func(t *testing.T) {
		data := map[domain.Subject][]rank.Cluster{
			person("ada@example.com"): repeat("a", 3, 1, 2), // 0.5 in every replicate
			person("bob@example.com"): repeat("b", 3, 0, 2), // 0
			person("zoe@example.com"): repeat("z", 2, 1, 1), // 1, the best value, from too few clusters
			// Σ Num / Σ Den, not the mean of the two ratios, which is 0.5.
			person("dan@example.com"): {{Change: "d1", Num: 1, Den: 1}, {Change: "d2", Num: 0, Den: 9}},
			person("eve@example.com"): nil,
		}
		got := rank.Intervals(durable, sibling, cohort, rank.Ratio, data, rank.Options{MinN: 3})
		check(t, got, durable, sibling, cohort,
			row{"ada@example.com", 0.5, 0.5, 0.5, 1, 1, 3, true}, // Zoe's 1 does not push Ada down
			row{"bob@example.com", 0, 0, 0, 2, 2, 3, true},
			row{key: "dan@example.com", estimate: 0.1, n: 2},
			row{key: "eve@example.com"},
			row{key: "zoe@example.com", estimate: 1, n: 2},
		)

		// Whatever MinN says, a subject needs a cluster to be ranked.
		for _, r := range rank.Intervals(durable, sibling, cohort, rank.Ratio, data, rank.Options{}) {
			if r.Eligible != (r.N > 0) {
				t.Errorf("MinN 0: %s with %d clusters has Eligible %v", r.Subject.Key, r.N, r.Eligible)
			}
		}
		if got := rank.Intervals(durable, sibling, cohort, rank.Ratio, nil, rank.Options{MinN: 3}); got != nil {
			t.Errorf("Intervals(no data) = %v, want nil", got)
		}
	})

	t.Run("tied subjects share the best rank they cover", func(t *testing.T) {
		data := map[domain.Subject][]rank.Cluster{
			person("ada@example.com"):  repeat("a", 3, 2, 2), // 1 in every replicate
			person("bob@example.com"):  repeat("b", 4, 1, 1), // 1 in every replicate, so tied with Ada
			person("cara@example.com"): repeat("c", 3, 1, 2), // 0.5
			person("dan@example.com"):  repeat("d", 3, 0, 1), // 0
		}
		got := rank.Intervals(durable, sibling, cohort, rank.Ratio, data, rank.Options{MinN: 3})
		check(t, got, durable, sibling, cohort,
			row{"ada@example.com", 1, 1, 1, 1, 1, 3, true},
			row{"bob@example.com", 1, 1, 1, 1, 1, 4, true},
			row{"cara@example.com", 0.5, 0.5, 0.5, 3, 3, 3, true}, // "1, 1, 3": nobody is second
			row{"dan@example.com", 0, 0, 0, 4, 4, 3, true},
		)

		// Subjects with the same clusters by week tie in every replicate of
		// a sum, so they get the same interval.
		sum := map[domain.Subject][]rank.Cluster{
			person("ada@example.com"): {{Change: "a1", Num: 4, Block: 1}, {Change: "a2", Num: 2, Block: 5}},
			person("bob@example.com"): {{Change: "b1", Num: 4, Block: 1}, {Change: "b2", Num: 2, Block: 5}},
		}
		got = rank.Intervals(simplifying, sibling, window, rank.Sum, sum, rank.Options{MinN: 2, Blocks: 13})
		if a, b := got[0], got[1]; a.RankLo != 1 || a.RankHi != 1 || b.RankLo != 1 || b.RankHi != 1 || a.Lo != b.Lo || a.Hi != b.Hi {
			t.Errorf("identical sum subjects got\n%s", format(got))
		}
	})

	t.Run("a sum draws the same weeks for every subject", func(t *testing.T) {
		// Ada removed 10 lines and Bob 5, in the same week. Drawn together,
		// Ada's total is twice Bob's in every replicate, so she never ranks
		// below him; drawn apart, she would in about a quarter of them. The
		// week is drawn c ~ Binomial(13, 1/13) times: never with probability
		// 35%, when both are 0 and tied, and three times or more with 7.3%.
		data := map[domain.Subject][]rank.Cluster{
			person("ada@example.com"): {{Change: "a", Num: 10, Block: 0}},
			person("bob@example.com"): {{Change: "b", Num: 5, Block: 0}},
		}
		got := rank.Intervals(simplifying, sibling, window, rank.Sum, data, rank.Options{MinN: 1, Blocks: 13})
		check(t, got, simplifying, sibling, window,
			row{"ada@example.com", 10, 0, 30, 1, 1, 1, true},
			row{"bob@example.com", 5, 0, 15, 1, 2, 1, true},
		)
	})

	t.Run("a count takes its uncertainty from the weeks", func(t *testing.T) {
		// One change a week in 10 of the 13 weeks. Resampling the subject's
		// own 10 clusters would return 10 in every replicate; resampling
		// weeks draws X ~ Binomial(13, 10/13) active weeks, with
		// P(X ≤ 6) = 1.6%, P(X ≤ 7) = 5.7% and P(X = 13) = 3.3%.
		var cs []rank.Cluster
		for k := range 10 {
			cs = append(cs, rank.Cluster{Change: fmt.Sprint("c", k), Num: 1, Block: k})
		}
		data := map[domain.Subject][]rank.Cluster{person("ada@example.com"): cs}
		got := rank.Intervals(simplifying, sibling, window, rank.Sum, data, rank.Options{MinN: 10, Blocks: 13})
		check(t, got, simplifying, sibling, window, row{"ada@example.com", 10, 7, 13, 1, 1, 10, true})
	})

	t.Run("intervals depend on each subject's clusters, not their order or the other subjects", func(t *testing.T) {
		// spread's replicates are finely spread, so any change to a
		// subject's draws moves its interval.
		for _, kind := range []rank.Kind{rank.Ratio, rank.Sum} {
			opt := rank.Options{MinN: 10, Blocks: 13}
			data := spread()
			first := rank.Intervals(durable, sibling, cohort, kind, data, opt)
			if !reflect.DeepEqual(data, spread()) {
				t.Errorf("kind %d: Intervals modified its input", kind)
			}
			if again := rank.Intervals(durable, sibling, cohort, kind, spread(), opt); !reflect.DeepEqual(again, first) {
				t.Errorf("kind %d: a second call got\n%s\nthe first\n%s", kind, format(again), format(first))
			}

			reordered := spread()
			for _, cs := range reordered {
				slices.Reverse(cs)
			}
			if got := rank.Intervals(durable, sibling, cohort, kind, reordered, opt); !reflect.DeepEqual(got, first) {
				t.Errorf("kind %d: reversed clusters got\n%s\nin order\n%s", kind, format(got), format(first))
			}
		}

		// A ratio subject's value interval comes from its own clusters and
		// its own random stream, so a newcomer changes ranks, not values,
		// even one who sorts, and so would draw, first.
		opt := rank.Options{MinN: 10}
		first := rank.Intervals(durable, sibling, cohort, rank.Ratio, spread(), opt)
		joined := spread()
		joined[person("aaron@example.com")] = repeat("a", 10, 1, 4)
		got := rank.Intervals(durable, sibling, cohort, rank.Ratio, joined, opt)
		for i, was := range first {
			if now := got[i+1]; now.Subject != was.Subject || now.Lo != was.Lo || now.Hi != was.Hi {
				t.Errorf("a value interval moved when Aaron joined: got\n%s\nwas\n%s", format(got), format(first))
				break
			}
		}
	})

	t.Run("the seed, the random stream and the draw are pinned", func(t *testing.T) {
		// These values come from this implementation, not from a hand
		// derivation: a golden master (Feathers, Working Effectively with
		// Legacy Code, 2004, ch. 13). Each Lo and Hi is one order statistic
		// of finely spread replicates, so a change to the seed's encoding,
		// the PCG stream or the bounded draw, in this package or in a Go
		// release, fails here instead of silently moving published
		// intervals.
		const seed = 0x2690983a37e8243d
		if got := rank.Seed(durable, sibling, cohort); got != seed {
			t.Errorf("Seed(durable@1, sibling, cohort) = %#x, want %#x", got, seed)
		}
		east := time.FixedZone("UTC+5", 5*60*60)
		if got := rank.Seed(durable, sibling, domain.Window{Since: cohort.Since.In(east), Until: cohort.Until.In(east)}); got != seed {
			t.Errorf("the window's location changed the seed: %#x", got)
		}
		for name, other := range map[string]uint64{
			"version": rank.Seed(domain.MetricID{Name: "durable", Version: 2}, sibling, cohort),
			"metric":  rank.Seed(simplifying, sibling, cohort),
			"scope":   rank.Seed(durable, domain.Scope{Kind: domain.ScopeGroup, Key: "sibling"}, cohort),
			"window":  rank.Seed(durable, sibling, window),
		} {
			if other == seed {
				t.Errorf("another %s gave the same seed", name)
			}
		}

		data := spread()
		check(t, rank.Intervals(durable, sibling, cohort, rank.Ratio, data, rank.Options{MinN: 10}), durable, sibling, cohort,
			row{"ada@example.com", 55.0 / 156, 0.21428571428571427, 0.527027027027027, 1, 3, 12, true},
			row{"bob@example.com", 85.0 / 209, 0.28703703703703703, 0.5252525252525253, 1, 3, 16, true},
			row{"cara@example.com", 63.0 / 168, 0.25748502994011974, 0.5, 1, 3, 13, true},
		)
		check(t, rank.Intervals(simplifying, sibling, window, rank.Sum, data, rank.Options{MinN: 10, Blocks: 13}), simplifying, sibling, window,
			row{"ada@example.com", 55, 30, 79, 1, 3, 12, true},
			row{"bob@example.com", 85, 58, 116, 1, 3, 16, true},
			row{"cara@example.com", 63, 42, 86, 1, 3, 13, true},
		)
	})
}

// siblingDurable is durable@1's clusters on the sibling fixture, from the
// hand-derived tables of testdata/fixture/SPEC.md#durable1-sibling: Num is a
// landing's surviving lines and Den its added lines.
func siblingDurable() map[domain.Subject][]rank.Cluster {
	var lena []rank.Cluster
	for i := 1; i <= 10; i++ {
		c := rank.Cluster{Change: fmt.Sprint("L", i), Num: 3, Den: 3}
		switch i {
		case 2:
			c.Num = 2 // W2 replaced lena_2_2
		case 7:
			c.Num = 0 // reverted by R1
		}
		lena = append(lena, c)
	}
	var omar []rank.Cluster // O10 and D1 add nothing, so they are not clusters
	for i := 1; i <= 9; i++ {
		omar = append(omar, rank.Cluster{Change: fmt.Sprint("O", i), Num: 3, Den: 3})
	}
	return map[domain.Subject][]rank.Cluster{
		person("lena@example.com"):  lena,
		person("omar@example.com"):  omar,
		person("pia@example.com"):   {{Change: "C1", Num: 4, Den: 4}, {Change: "M", Num: 5, Den: 5}},
		person("quinn@example.com"): {{Change: "Q1", Num: 6, Den: 6}},
	}
}

// siblingSimplifying is simplifying@1's clusters on the sibling fixture,
// from testdata/fixture/SPEC.md#simplifying1-sibling: Num is a landing's
// removed lines and Block its week, counted back from fixture.Now.
func siblingSimplifying() map[domain.Subject][]rank.Cluster {
	removed := func(change, day string, lines float64) rank.Cluster {
		landed, err := time.Parse(time.DateOnly, day)
		if err != nil {
			panic(err)
		}
		landed = landed.Add(12 * time.Hour) // every sibling commit is at 12:00:00Z
		return rank.Cluster{Change: change, Num: lines, Block: int(fixture.Now.Sub(landed) / (7 * 24 * time.Hour))}
	}
	return map[domain.Subject][]rank.Cluster{
		person("lena@example.com"): {
			removed("W1", "2026-07-20", 0), removed("W2", "2026-07-27", 1), removed("W3", "2026-08-03", 0),
			removed("W4", "2026-08-10", 0), removed("W5", "2026-08-17", 0), removed("W6", "2026-08-24", 0),
			removed("W7", "2026-08-31", 0), removed("S1", "2026-09-14", 22), removed("W8", "2026-09-16", 0),
			removed("W9", "2026-09-21", 0),
		},
		person("omar@example.com"): {removed("IR", "2026-07-24", 0), removed("S2", "2026-08-12", 21)},
		person("pia@example.com"):  {removed("T1", "2026-07-29", 0)},
	}
}

// spread is three subjects whose clusters vary in both Num and Den, so their
// replicates take many distinct values. Bob's 16 clusters exercise the draw
// for a power of two, where rand.Rand.IntN would mask low bits instead.
func spread() map[domain.Subject][]rank.Cluster {
	data := map[domain.Subject][]rank.Cluster{}
	for s, name := range []string{"ada", "bob", "cara"} {
		var cs []rank.Cluster
		for i := range []int{12, 16, 13}[s] {
			cs = append(cs, rank.Cluster{
				Change: fmt.Sprintf("%s-%02d", name, i),
				Num:    float64((i*7 + s*3) % 11),
				Den:    float64(10 + (i*5+s)%7),
				Block:  (i*5 + s) % 13,
			})
		}
		data[person(name+"@example.com")] = cs
	}
	return data
}

func person(email string) domain.Subject {
	return domain.Subject{Kind: domain.SubjectPerson, Key: email}
}

// repeat returns n clusters with the same Num and Den.
func repeat(prefix string, n int, num, den float64) []rank.Cluster {
	cs := make([]rank.Cluster, n)
	for i := range cs {
		cs[i] = rank.Cluster{Change: fmt.Sprint(prefix, i), Num: num, Den: den}
	}
	return cs
}

// row is one expected interval; a subject that is not eligible has only a
// key, an estimate and N.
type row struct {
	key              string
	estimate, lo, hi float64
	rankLo, rankHi   int
	n                int
	eligible         bool
}

func check(t *testing.T, got []domain.RankInterval, id domain.MetricID, scope domain.Scope, w domain.Window, rows ...row) {
	t.Helper()
	want := make([]domain.RankInterval, len(rows))
	for i, r := range rows {
		want[i] = domain.RankInterval{
			Metric: id, Scope: scope, Subject: person(r.key), Window: w,
			Estimate: r.estimate, Lo: r.lo, Hi: r.hi, RankLo: r.rankLo, RankHi: r.rankHi,
			N: r.n, Eligible: r.eligible,
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%s\nwant\n%s", format(got), format(want))
	}
}

func format(rs []domain.RankInterval) string {
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "  %s %s %v in %v %s: %v [%v, %v] rank %d-%d n %d eligible %v\n",
			r.Metric, r.Scope, r.Window.Since.Format(time.DateOnly), r.Subject.Kind, r.Subject.Key,
			r.Estimate, r.Lo, r.Hi, r.RankLo, r.RankHi, r.N, r.Eligible)
	}
	return b.String()
}
