package app_test

import (
	"testing"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
)

// binaryGuard checks SPEC's binary-file guard (#54): git takes some changes
// to prod files for binary, so their added lines are unknown while blame
// still attributes the files' lines. durable@1 leaves such a change's lines
// out of both of its sums, following the file's renames to its path at
// HEAD, and keeps every other change's: B1's core.py beside its binary
// bin.py, and T1's lines on flip.py, which T2 made binary and T3 text
// again. ai_assisted@1's cohort comparison reads the same sums.
func binaryGuard(t *testing.T) {
	t.Parallel()
	s := scan(t, "", nil, scanRanked(fixture.Binary(t)))
	r := onlyRepo(t, s.model)

	// The guard's premise, as History saw it: the changes git took for
	// binary, by landing time and path.
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
		"2026-02-02T12:00:00Z bin.py":   true, // B1
		"2026-04-06T12:00:00Z flip.py":  true, // T2: its new side holds a NUL byte
		"2026-05-04T12:00:00Z flip.py":  true, // T3: its old side does
		"2026-06-01T12:00:00Z moved.py": true, // R1, the rename
	})

	checkDurable(t, s, r.Key, "SPEC regression guards, binary files, durable@1", map[string]durableRow{
		"ann@example.com": {"Ann Binary", 2, 2, 1, 0, false},
		"flo@example.com": {"Flo Flip", 3, 5, 2, 0, false},
	}, map[string]map[string]cluster{
		// B1: core.py; bin.py's 3 lines, blamed to B1 in moved.py through
		// R1's rename, are in neither sum.
		"ann@example.com": {"2026-02-02T12:00:00Z": {2, 2}},
		"flo@example.com": {
			"2026-03-02T12:00:00Z": {2, 4}, // T1: flip_1 and flip_3 survive, flip.py was text
			"2026-04-06T12:00:00Z": {1, 1}, // T2: flo.py; flip_4 = 40, blamed to T2, is in neither sum
		},
	})

	// ai_assisted@1: no landing is tagged, so every cluster above is in the
	// untagged cohort, with durable@1's own sums.
	const table = "SPEC regression guards, binary files, ai_assisted@1"
	e := s.explain(t, domain.MetricID{Name: "ai_assisted", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
	if v, ok := repoValue(t, table, e, r.Key); ok {
		_, ai := v.Detail["durable_ai"]
		if v.Num != 0 || v.Den != 5 || v.Detail["durable_other"] != 5.0/7 || v.Detail["durable_other_n"] != 3 || ai {
			t.Errorf("%s: %s, want 0 of 5 landings tagged, durable_other 5/7 over 3 clusters and no durable_ai", table, describe(v))
		}
	}
}
