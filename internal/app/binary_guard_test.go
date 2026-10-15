package app_test

import (
	"testing"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
)

// binaryGuard checks SPEC's binary-file guard (#54): git takes some changes
// for binary, so their added lines are unknown while blame still attributes
// the files' lines. durable@1 leaves such a change's lines out of both of
// its sums, matching the lines blame gives its commit by the path they had
// in that commit, which blame names whatever renames lie between: R1's,
// which the history never saw, and V2's, which took a file with no role
// onto a prod path. It keeps every other change's: B1's core.py beside its
// binary bin.py, and T1's and T4's lines on flip.py, which T2 made binary
// and T3 text again. ai_assisted@1's cohort comparison reads the same sums.
func binaryGuard(t *testing.T) {
	s := scan(t, "", nil, scanRanked(fixture.Binary(t)))
	r := onlyRepo(t, s.model)

	// The guard's premise, as History saw it: the changes git took for
	// binary, by landing time and path. R1 is no landing, so its rename is
	// not among them.
	rf := onlyFacts(t, s)
	landed := map[string]string{}
	for _, l := range rf.Landings {
		landed[l.SHA] = rfc3339(l.Time)
	}
	binary := map[string]bool{}
	for _, fc := range rf.Changes {
		if fc.Binary {
			binary[landed[fc.Commit]+" "+fc.Path] = true
		}
	}
	sameRows(t, "SPEC regression guards, binary files, the binary changes", binary, map[string]bool{
		"2026-02-02T12:00:00Z bin.py":        true, // B1
		"2026-04-06T12:00:00Z flip.py":       true, // T2: its new side holds a NUL byte
		"2026-05-04T12:00:00Z flip.py":       true, // T3: its old side does
		"2026-05-11T12:00:00Z vendor/lib.py": true, // V1, on a path with no role
		"2026-05-18T12:00:00Z lib.py":        true, // V2, the move onto a prod path
	})

	checkDurable(t, s, r.Key, "SPEC regression guards, binary files, durable@1", map[string]durableRow{
		"ann@example.com": {"Ann Binary", 2, 2, 1, 0, false},
		"flo@example.com": {"Flo Flip", 4, 6, 3, 0, false},
		"val@example.com": {"Val Vendor", 2, 2, 1, 0, false},
	}, map[string]map[string]cluster{
		// B1: core.py; bin.py's 3 lines, blamed to B1 in moved.py from
		// bin.py through R1's rename, are in neither sum.
		"ann@example.com": {"2026-02-02T12:00:00Z": {2, 2}},
		"flo@example.com": {
			"2026-03-02T12:00:00Z": {2, 4}, // T1: flip_1 and flip_3 survive, flip.py was text
			"2026-04-06T12:00:00Z": {1, 1}, // T2: flo.py; flip_4 = 40, blamed to T2, is in neither sum
			"2026-06-08T12:00:00Z": {1, 1}, // T4: flip_5, added to flip.py as text after its binary changes
		},
		// V1: val.py; vendor/lib.py's 3 lines, blamed to V1 in lib.py, are
		// in neither sum, although their path had no role in V1.
		"val@example.com": {"2026-05-11T12:00:00Z": {2, 2}},
	})

	// ai_assisted@1: no landing is tagged, so every cluster above is in the
	// untagged cohort, with durable@1's own sums.
	const table = "SPEC regression guards, binary files, ai_assisted@1"
	e := s.explain(t, domain.MetricID{Name: "ai_assisted", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
	if v, ok := repoValue(t, table, e, r.Key); ok {
		_, ai := v.Detail["durable_ai"]
		if v.Num != 0 || v.Den != 7 || v.Detail["durable_other"] != 8.0/10 || v.Detail["durable_other_n"] != 5 || ai {
			t.Errorf("%s: %s, want 0 of 7 landings tagged, durable_other 8/10 over 5 clusters and no durable_ai", table, describe(v))
		}
	}
}
