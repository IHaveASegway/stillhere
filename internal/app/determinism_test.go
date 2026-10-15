package app_test

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// TestDeterminism checks docs/spec/codebase.md#determinism end to end on
// both fixtures, scanned as one group with their mailmaps in one file: two
// fresh scans, each into its own facts.db, render byte-identical JSON once
// timings are removed, and byte-identical text and Markdown; report on the
// stored run reproduces its scan; and a token planted in a PATH reaches no
// byte of facts.db, no rendering and no log line.
func TestDeterminism(t *testing.T) {
	t.Parallel()
	group := filepath.Join(t.TempDir(), "acme")
	if err := fixture.BuildContext(t.Context(), filepath.Join(group, "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.BuildSiblingContext(t.Context(), filepath.Join(group, "sibling")); err != nil {
		t.Fatal(err)
	}
	mailmap := mailmapFile(t, fixture.MailmapText+fixture.SiblingMailmapText)

	first := scan(t, mailmap, nil, scanRanked(group))
	checkGroupRun(t, first.model)
	second := scan(t, mailmap, nil, scanRanked(group))
	a, b := render(t, withoutTimings(first.model)), render(t, withoutTimings(second.model))
	sameBytes(t, "the two scans' JSON without timings", a.json, b.json)
	sameBytes(t, "the two scans' text", a.text, b.text)
	sameBytes(t, "the two scans' Markdown", a.markdown, b.markdown)

	m, err := app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run})
	if err != nil {
		t.Fatalf("app.Report: %v", err)
	}
	rep := render(t, withoutTimings(m))
	sameBytes(t, "report's JSON without timings and the scan's", rep.json, a.json)
	sameBytes(t, "report's text and the scan's", rep.text, a.text)
	sameBytes(t, "report's Markdown and the scan's", rep.markdown, a.markdown)

	t.Run("a token planted in a PATH", func(t *testing.T) {
		// The review of #42 found a token typed in a PATH in facts.db and in
		// every renderer: discovery made the URL a path under the working
		// directory, "<cwd>/https:/x-access-token:TOKEN@host/...", which the
		// scrubber did not match. The PATH is no repository, so the run
		// records it as a path-failed warning, scrubbed.
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		planted := "https://x-access-token:" + fixture.DiscoveryToken + "@example.com/acme/pay.git"
		s := scan(t, mailmap, log, scanRanked(group, planted))
		if s.model.Run.Status != string(domain.RunStatusPartial) ||
			!slices.ContainsFunc(s.model.Warnings, func(w report.Warning) bool { return w.Code == "path-failed" }) {
			t.Fatalf("the planted PATH gave run status %q and warnings %v, want a partial run with a path-failed warning",
				s.model.Run.Status, s.model.Warnings)
		}
		m, err := app.Report(t.Context(), s.read, app.ReportRequest{RunID: s.run})
		if err != nil {
			t.Fatalf("app.Report: %v", err)
		}
		for _, d := range []struct {
			name string
			doc  report.Document
		}{{"scan", s.model}, {"report", m}} {
			r := render(t, d.doc)
			noToken(t, d.name+" as JSON", r.json)
			noToken(t, d.name+" as text", r.text)
			noToken(t, d.name+" as Markdown", r.markdown)
		}
		// While the store is open its writes sit in the write-ahead log,
		// which must exist then; once it is closed they are checkpointed
		// into facts.db.
		files := func(when string, required ...string) {
			for _, path := range []string{s.db, s.db + "-wal", s.db + "-shm"} {
				data, err := os.ReadFile(path)
				if errors.Is(err, fs.ErrNotExist) && !slices.Contains(required, path) {
					continue
				}
				if err != nil {
					t.Fatalf("reading %s %s: %v", filepath.Base(path), when, err)
				}
				noToken(t, filepath.Base(path)+" "+when, data)
			}
		}
		files("with the store open", s.db, s.db+"-wal")
		if err := s.store.Close(); err != nil {
			t.Fatalf("closing the store: %v", err)
		}
		files("after the store closed", s.db)
		if logs.Len() == 0 {
			t.Fatal("the scan and the report logged nothing at debug level, so the log check is empty")
		}
		noToken(t, "the debug log of the scan and the report", logs.Bytes())
	})
}

// checkGroupRun checks that a determinism scan covers what it is meant to
// compare: both fixtures in one group, collected, and rank tables with
// someone ranked, so the seeded bootstrap is in the output.
func checkGroupRun(t *testing.T, m *report.Model) {
	t.Helper()
	ranked := slices.ContainsFunc(m.Ranks, func(rt report.RankTable) bool { return len(rt.Ranked) > 0 })
	switch {
	case m.Run.Status != string(domain.RunStatusComplete):
		t.Fatalf("the group scan is %s, want complete", m.Run.Status)
	case len(m.Groups) != 1 || len(m.Repos) != 2:
		t.Fatalf("the group scan has %d groups and %d repositories, want 1 and 2", len(m.Groups), len(m.Repos))
	case !ranked:
		t.Fatal("the group scan ranks nobody, so no bootstrap interval is compared")
	}
}
