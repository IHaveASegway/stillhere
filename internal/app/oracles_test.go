package app_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// This file holds what the end-to-end oracle tests share
// (docs/spec/codebase.md#tests): TestWeekOracle, TestStillHereOracle,
// TestChangeOracles and TestDeterminism. They drive the use cases with the
// adapters internal/cli's composition root builds (env.with in
// internal/cli/root.go): a gitcli.Runner whose organisation mailmap is the
// fixture's mailmap file, a sqlite.Store on a new facts.db, and the packaged
// policy, which a run gets in a directory without stillhere.toml. git runs
// for real and nothing is mocked.
//
// Every expected number is a literal copied from testdata/fixture/SPEC.md,
// never a value the code under test computed, and every message names the
// SPEC table it checks, so a failure says which oracle moved. A landing is
// found by its time, the row's date and hour, never by a SHA written here:
// SPEC's commit IDs are for debugging.

// toolVersion is the version every test run records (app.Deps.Version).
const toolVersion = "oracle-test"

// The ranked metrics the change oracles cover, as --rank names them.
var (
	durable     = domain.MetricID{Name: "durable", Version: 1}
	simplifying = domain.MetricID{Name: "simplifying", Version: 1}
)

// writeDeps returns the Deps internal/cli builds for week: git with mailmap
// as the organisation mailmap file ("" for none), the packaged policy, the
// logger (nil discards) and the tool version. A scan adds a store.
func writeDeps(t *testing.T, mailmap string, log *slog.Logger) app.Deps {
	t.Helper()
	git, err := gitcli.New(t.Context(), gitcli.Config{MailmapFile: mailmap, Logger: log})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	p, err := policy.Default()
	if err != nil {
		t.Fatalf("policy.Default: %v", err)
	}
	return app.Deps{Git: git, Policy: p, Log: log, Version: toolVersion}
}

// openStore opens facts.db in a new temporary directory, as internal/cli
// opens --db, and closes it when the test ends.
func openStore(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "facts.db")
	s, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing %s: %v", path, err)
		}
	})
	return s, path
}

// readDeps returns the Deps internal/cli builds for report, explain,
// who-knows and me: the store, as both its ports, since facts.db holds the
// forge replica too. A read use case gets no git runner and no policy,
// because it rebuilds the run's own policy from facts.db.
func readDeps(s *sqlite.Store, log *slog.Logger) app.Deps {
	return app.Deps{Store: s, ForgeStore: s, Log: log, Version: toolVersion}
}

// mailmapFile writes text to a new 0600 file and returns its path: the
// organisation mailmap of a run over several fixtures, whose texts
// concatenate (fixture.SiblingMailmapText).
func mailmapFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailmap")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("writing the mailmap: %v", err)
	}
	return path
}

// scanned is one scan stored in its own facts.db.
type scanned struct {
	model *report.Model // app.Scan's report
	run   int64         // the stored run's id
	store *sqlite.Store
	db    string   // facts.db's path
	read  app.Deps // the read commands' Deps over the store
}

// scan runs app.Scan with the Deps internal/cli builds for scan, into a new
// facts.db whose forge replica each seed fills first, as a sync would, and
// fails the test unless it stored a run.
func scan(t *testing.T, mailmap string, log *slog.Logger, req app.ScanRequest, seed ...func(*sqlite.Store)) *scanned {
	t.Helper()
	d := writeDeps(t, mailmap, log)
	s, db := openStore(t)
	for _, fill := range seed {
		fill(s)
	}
	d.Store, d.ForgeStore = s, s
	m, err := app.Scan(t.Context(), d, req)
	if err != nil {
		t.Fatalf("app.Scan: %v", err)
	}
	if m.Run == nil {
		t.Fatal("app.Scan stored no run")
	}
	return &scanned{model: m, run: m.Run.ID, store: s, db: db, read: readDeps(s, log)}
}

// scanRanked is the scan request of the change oracles: paths at --now
// 2026-10-07, ranking both change metrics.
func scanRanked(paths ...string) app.ScanRequest {
	return app.ScanRequest{Paths: paths, Now: fixture.Now, Rank: []domain.MetricID{durable, simplifying}}
}

// onlyRepo returns the one repository of a report about one repository.
func onlyRepo(t *testing.T, m *report.Model) *report.Repo {
	t.Helper()
	if len(m.Repos) != 1 {
		t.Fatalf("the report has %d repositories, want 1", len(m.Repos))
	}
	r := &m.Repos[0]
	if r.Status != domain.RepoStatusOK {
		t.Fatalf("repository %s failed: %s", r.Name, r.Error)
	}
	return r
}

// facts loads the run's facts back from the store, which is how the tests
// see the facts no report prints, such as revert links.
func (s *scanned) facts(t *testing.T) *domain.FactSet {
	t.Helper()
	fs, err := s.store.LoadFacts(t.Context(), s.run)
	if err != nil {
		t.Fatalf("LoadFacts(%d): %v", s.run, err)
	}
	return fs
}

// explain runs app.Explain for subject, as the explain command does for a
// person and the MCP server for any subject.
func (s *scanned) explain(t *testing.T, metric domain.MetricID, subject domain.Subject) *report.Explain {
	t.Helper()
	e, err := app.Explain(t.Context(), s.read, app.ExplainRequest{RunID: s.run, Metric: metric, Subject: subject})
	if err != nil {
		t.Fatalf("app.Explain(%s, %s %s): %v", metric, subject.Kind, subject.Key, err)
	}
	return e
}

// whoKnows runs app.WhoKnows for path in every repository of the run.
func (s *scanned) whoKnows(t *testing.T, path string) *report.WhoKnows {
	t.Helper()
	w, err := app.WhoKnows(t.Context(), s.read, app.WhoKnowsRequest{RunID: s.run, Path: path})
	if err != nil {
		t.Fatalf("app.WhoKnows(%s): %v", path, err)
	}
	return w
}

// me runs app.Me for email.
func (s *scanned) me(t *testing.T, email string) *report.Me {
	t.Helper()
	m, err := app.Me(t.Context(), s.read, app.MeRequest{RunID: s.run, Email: email})
	if err != nil {
		t.Fatalf("app.Me(%s): %v", email, err)
	}
	return m
}

// person is a person subject.
func person(email string) domain.Subject {
	return domain.Subject{Kind: domain.SubjectPerson, Key: email}
}

// rfc3339 is how the tests write an instant: the SPEC row's date and time,
// in UTC.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// window is a SPEC window, [since, until), as the report prints it.
func window(since, until string) report.Interval {
	parse := func(s string) time.Time {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err) // a typo in a literal of this file
		}
		return t
	}
	return report.Interval{Since: parse(since), Until: parse(until)}
}

// sameInterval compares two windows by instant.
func sameInterval(a, b report.Interval) bool {
	return a.Since.Equal(b.Since) && a.Until.Equal(b.Until)
}

// sameRows compares rows keyed by email (or another key) with want, and
// names table, the SPEC table they come from, in every message: a missing
// row, a row SPEC does not have, and a row with other numbers.
func sameRows[R comparable](t *testing.T, table string, got, want map[string]R) {
	t.Helper()
	for _, k := range slices.Sorted(maps.Keys(want)) {
		g, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%s: no row for %s, want %+v", table, k, want[k])
		case g != want[k]:
			t.Errorf("%s: %s is %+v, want %+v", table, k, g, want[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: %s has a row, %+v, which SPEC does not list", table, k, got[k])
		}
	}
}

// noToken fails the test when data holds the planted token, quoting the 80
// bytes around it with the token itself replaced, so a failure never
// prints the token.
func noToken(t *testing.T, where string, data []byte) {
	t.Helper()
	i := bytes.Index(data, []byte(fixture.DiscoveryToken))
	if i < 0 {
		return
	}
	from, to := max(i-80, 0), min(i+len(fixture.DiscoveryToken)+80, len(data))
	around := strings.ReplaceAll(string(data[from:to]), fixture.DiscoveryToken, "<token>")
	t.Errorf("%s holds the planted token at byte %d: %q", where, i, around)
}

// rendered is a document in the three formats a command prints.
type rendered struct{ json, text, markdown []byte }

// withoutTimings returns a copy of m without its timings, the only part of
// a report that two runs on the same inputs do not share.
func withoutTimings(m *report.Model) *report.Model {
	c := *m
	c.Timings = nil
	return &c
}

// render renders d in the three formats.
func render(t *testing.T, d report.Document) rendered {
	t.Helper()
	var out rendered
	for _, f := range []struct {
		name string
		fn   func(w *bytes.Buffer) error
		dst  *[]byte
	}{
		{"JSON", func(w *bytes.Buffer) error { return report.JSON(w, d) }, &out.json},
		{"text", func(w *bytes.Buffer) error { return report.Text(w, d) }, &out.text},
		{"Markdown", func(w *bytes.Buffer) error { return report.Markdown(w, d) }, &out.markdown},
	} {
		var buf bytes.Buffer
		if err := f.fn(&buf); err != nil {
			t.Fatalf("rendering %s: %v", f.name, err)
		}
		*f.dst = buf.Bytes()
	}
	return out
}

// sameBytes fails the test when a and b differ, showing the first line
// that differs.
func sameBytes(t *testing.T, what string, a, b []byte) {
	t.Helper()
	if bytes.Equal(a, b) {
		return
	}
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y || i >= len(al) || i >= len(bl) {
			t.Errorf("%s differ (%d and %d bytes), first at line %d:\n  %q\n  %q", what, len(a), len(b), i+1, x, y)
			return
		}
	}
	t.Errorf("%s differ (%d and %d bytes)", what, len(a), len(b))
}

// errNotFound reports whether err says the run holds nothing about what was
// asked.
func errNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }

// describe is a short form of a value for messages.
func describe(v report.Value) string {
	return fmt.Sprintf("%s %s/%s value %v num %v den %v n %d detail %v",
		v.Metric, v.Scope.Kind, v.Scope.Key, v.Value, v.Num, v.Den, v.N, v.Detail)
}
