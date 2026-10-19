package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/cli"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// TestDeterminism checks docs/spec/codebase.md#determinism end to end on
// both fixtures, scanned as one group with their mailmaps in one file: two
// fresh scans, each into its own facts.db, render byte-identical JSON once
// timings are removed, and byte-identical text and Markdown; report on the
// stored run reproduces its scan; a person's own page prints a share of
// den 1 as a percent, as explain does (#66); and a token planted in a URL
// given as a PATH, in its userinfo, query or fragment, reaches no byte of
// facts.db, no rendering and no log line.
//
// The scans read GitHub data too (docs/spec/codebase.md#run-model): the
// fixture's origin names a forge repository in the replica, beside another
// organization's. The run keeps only its own repository's watermark,
// reviews_given@1 counts that repository's reviews, in its week blocks at
// both ends of its window, and the report dates them exactly as the scan
// did, after a later sync renamed the repository too.
//
// A repository of display names the reports may not print as git recorded
// them is scanned on its own, twice, and checked the same way, with how
// each page names those people (displayNames, names_test.go, #52).
func TestDeterminism(t *testing.T) {
	t.Parallel()
	group := filepath.Join(t.TempDir(), "acme")
	if err := fixture.BuildContext(t.Context(), filepath.Join(group, "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.BuildSiblingContext(t.Context(), filepath.Join(group, "sibling")); err != nil {
		t.Fatal(err)
	}
	setOrigin(t, filepath.Join(group, "fixture"), forgeFixture.Key)
	mailmap := mailmapFile(t, fixture.MailmapText+fixture.SiblingMailmapText)
	req := scanRanked(group)
	req.Rank = append(req.Rank, reviewsGiven)
	seed := func(s *sqlite.Store) { seedReplica(t, s) }

	first := scan(t, mailmap, nil, req, seed)
	checkGroupRun(t, first.model)
	checkForgeRun(t, first)
	checkSubjects(t, first)
	second := scan(t, mailmap, nil, req, seed)
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

	// A person's own page prints a share by its metric's one declaration,
	// as explain does, whatever its den (#66): Gina's own AI-assisted share
	// is 0 of 1, which me printed as a bare 0.
	gina := render(t, first.me(t, "gina@example.com"))
	if !bytes.Contains(gina.text, []byte("0.0% (0 of 1)")) || !bytes.Contains(gina.markdown, []byte("0.0% (0 of 1)")) {
		t.Errorf("Gina's own page does not print her AI-assisted share of 0 of 1 as 0.0%% (0 of 1):\n%s", gina.text)
	}

	// A later sync follows a rename on GitHub by node id and moves the
	// watermark on. The stored run keeps what it read, so its report does
	// not move (docs/spec/codebase.md#run-model).
	renamed := forgeFixture
	renamed.Key, renamed.Name = "https://git.example.test/acme/fixture-renamed.git", "fixture-renamed"
	if err := first.store.PutForgeRepos(t.Context(), []domain.ForgeRepo{renamed}); err != nil {
		t.Fatal(err)
	}
	later := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)
	if err := first.store.MarkForgeSynced(t.Context(), forgeFixture.ID, later); err != nil {
		t.Fatal(err)
	}
	if m, err = app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run}); err != nil {
		t.Fatalf("app.Report after a later sync: %v", err)
	}
	sameBytes(t, "report's JSON after a rename and a later sync, and the scan's", render(t, withoutTimings(m)).json, a.json)

	// A run a stillhere before wave 10's corrections in place stored holds
	// review_coverage@1 counted by landing and surviving@1's landings_365d
	// counted with merges. Its readers tell them by the Detail keys the
	// corrected definitions always write, via_other_branch and merged_365d,
	// and compute both again from the run's facts: its report is the
	// scan's, so explain cannot print a stored share above a list of
	// changes counted by another rule.
	forgetDetail(t, first.db, first.run, domain.MetricID{Name: "review_coverage", Version: 1}, "via_other_branch")
	forgetDetail(t, first.db, first.run, domain.MetricID{Name: "surviving", Version: 1}, "merged_365d")
	if m, err = app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run}); err != nil {
		t.Fatalf("app.Report of a run stored before wave 10's corrections: %v", err)
	}
	sameBytes(t, "report's JSON of a run whose review_coverage@1 and surviving@1 values lack via_other_branch and "+
		"merged_365d, and the scan's", render(t, withoutTimings(m)).json, a.json)

	// A run a stillhere before reverts at commit grain (#43) stored holds
	// the three change metrics' values without reverts_left_out, scored at
	// landing grain, and rank intervals drawn from those clusters. Its
	// readers compute the three again from its facts, with landed@2 and
	// review_coverage@1, whose copy rule reads reverts, and rank both
	// ranked metrics again: its report is the scan's, so MCP's changes
	// cannot credit a cluster to someone its person answer says holds no
	// value. explain prints each value's detail, which the report does not.
	aiAssisted := domain.MetricID{Name: "ai_assisted", Version: 1}
	explained := func() []byte {
		var out []byte
		for _, q := range []struct {
			id  domain.MetricID
			sub domain.Subject
		}{
			{durable, person("ada@example.com")}, {simplifying, person("ada@example.com")},
			{aiAssisted, domain.Subject{Kind: domain.SubjectRepo, Key: first.model.Repos[0].Key}},
		} {
			out = append(out, render(t, first.explain(t, q.id, q.sub)).json...)
		}
		return out
	}
	asScanned := explained()
	for _, id := range []domain.MetricID{durable, simplifying, aiAssisted} {
		forgetDetail(t, first.db, first.run, id, "reverts_left_out")
	}
	if m, err = app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run}); err != nil {
		t.Fatalf("app.Report of a run stored before reverts at commit grain: %v", err)
	}
	old := render(t, withoutTimings(m))
	sameBytes(t, "report's JSON of a run whose change metrics' values lack reverts_left_out, and the scan's", old.json, a.json)
	sameBytes(t, "report's text of a run whose change metrics' values lack reverts_left_out, and the scan's", old.text, a.text)
	sameBytes(t, "explain's JSON of durable@1, simplifying@1 and ai_assisted@1 in a run whose values lack "+
		"reverts_left_out, and in the scan's", explained(), asScanned)

	// A run a stillhere older than landed@2 and review_coverage@1 stored
	// holds no value of either. Its readers compute them from its facts,
	// so its report is the scan's, what landed and review coverage
	// included, rather than "nothing landed" over a run of landings
	// (#108, #107).
	forgetMetric(t, first.db, first.run, domain.MetricID{Name: "landed", Version: 2})
	forgetMetric(t, first.db, first.run, domain.MetricID{Name: "review_coverage", Version: 1})
	if m, err = app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run}); err != nil {
		t.Fatalf("app.Report of a run without landed@2: %v", err)
	}
	old = render(t, withoutTimings(m))
	sameBytes(t, "report's JSON of a run that holds no landed@2 or review_coverage@1 value, and the scan's", old.json, a.json)
	sameBytes(t, "report's text of a run that holds no landed@2 or review_coverage@1 value, and the scan's", old.text, a.text)

	// A run a stillhere before wave 10 stored holds knowledge_loss@1 values
	// without going_quiet, computed under the activity rule since corrected,
	// beside which a page would show each holder's last activity by the
	// corrected rule. Its readers compute both knowledge metrics again from
	// its facts (metrics.Corrected), so its report is the scan's, not a card
	// that contradicts itself (wave 10's review).
	staleKnowledge(t, first.db, first.run)
	if m, err = app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run}); err != nil {
		t.Fatalf("app.Report of a run stored before wave 10: %v", err)
	}
	old = render(t, withoutTimings(m))
	sameBytes(t, "report's JSON of a run whose knowledge values predate wave 10, and the scan's", old.json, a.json)
	sameBytes(t, "report's text of a run whose knowledge values predate wave 10, and the scan's", old.text, a.text)

	t.Run("names that give an address away", displayNames)
	t.Run("a token planted in a PATH", func(t *testing.T) {
		// The review of #42 found a token typed in a PATH in facts.db and in
		// every renderer: discovery made the URL a path under the working
		// directory, "<cwd>/https:/x-access-token:TOKEN@host/...", which the
		// scrubber did not match. The review of #13 found the same for a
		// token in the query or the fragment. The token sits in each of the
		// three parts of a URL that redact.URL removes: the userinfo, the
		// query and the fragment. Discovery refuses a PATH that holds a
		// credential where it enters (#93), before it looks for a
		// directory, so the run records a path-failed warning that quotes
		// it scrubbed; the three scrub to one path, so one warning names
		// them.
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		planted := []string{
			"https://x-access-token:" + fixture.DiscoveryToken + "@example.com/acme/pay.git",
			"https://example.com/acme/pay.git?access_token=" + fixture.DiscoveryToken,
			"https://example.com/acme/pay.git#" + fixture.DiscoveryToken,
		}
		s := scan(t, mailmap, log, scanRanked(append([]string{group}, planted...)...))
		var failed []string
		for _, w := range s.model.Warnings {
			if w.Code == "path-failed" {
				failed = append(failed, w.Message)
			}
		}
		if s.model.Run.Status != string(domain.RunStatusPartial) || len(failed) != 1 ||
			!strings.Contains(failed[0], "https:/example.com/acme/pay.git: holds what looks like a credential") {
			t.Fatalf("the planted PATHs gave run status %q and path-failed warnings %q, want a partial run with one, "+
				"naming the scrubbed path as holding a credential", s.model.Run.Status, failed)
		}
		m, err := app.Report(t.Context(), s.read, app.ReportRequest{RunID: s.run})
		if err != nil {
			t.Fatalf("app.Report: %v", err)
		}
		type named struct {
			name string
			doc  report.Document
		}
		docs := []named{{"scan", s.model}, {"report", m}}
		// who-knows quotes its PATH in its answer, header and caveats: one
		// pasted with a token is answered, and printed, scrubbed (#71).
		for _, p := range planted {
			w, err := app.WhoKnows(t.Context(), s.read, app.WhoKnowsRequest{RunID: s.run, Path: p})
			if err != nil {
				t.Fatalf("app.WhoKnows(a planted PATH): %v", err)
			}
			docs = append(docs, named{"who-knows of a planted PATH", w})
		}
		for _, d := range docs {
			r := render(t, d.doc)
			noToken(t, d.name+" as JSON", r.json)
			noToken(t, d.name+" as text", r.text)
			noToken(t, d.name+" as Markdown", r.markdown)
		}

		// The review of the MVP found a token pasted into sync github's
		// --org or --mirrors in facts.db (#71). The command line refuses
		// each before a run begins, and --db, a URL where a local path
		// belongs, with an error line that holds no token and says why. A
		// sync run records its options scrubbed, as a scan does, here one
		// whose organization GitHub's stand-in lists no repository of: the
		// run begins, records them and fails. One GitHub refuses, an
		// organization it does not know, is a usage error that begins no
		// run (#111's review). --db names a directory in the sync rows,
		// which no open accepts: a check gone missing ends there, before
		// GitHub could be asked anything, and only the reason tells it
		// apart.
		org := "https://x-access-token:" + fixture.DiscoveryToken + "@github.com/acme"
		mirrors := filepath.Join(t.TempDir(), "https:", "x-access-token:"+fixture.DiscoveryToken+"@github.com", "mirrors")
		noDB := t.TempDir()
		for _, c := range []struct {
			args []string
			why  string
		}{
			{[]string{"--db", noDB, "sync", "github", "--org", org, "--mirrors", t.TempDir()}, "invalid --org"},
			{[]string{"--db", noDB, "sync", "github", "--org", "acme", "--mirrors", mirrors},
				fmt.Sprintf("--mirrors %q: want a local path", redact.Scrub(mirrors))},
			{[]string{"--db", org + "/facts.db", "report"}, fmt.Sprintf("--db %q: want a local path", redact.Scrub(org+"/facts.db"))},
		} {
			var stderr bytes.Buffer
			code := cli.Execute(t.Context(), c.args, io.Discard, &stderr, cli.Build{})
			if code != 2 || !bytes.Contains(stderr.Bytes(), []byte(c.why)) {
				t.Errorf("stillhere %s exits %d: %s; want 2 and %q", c.args[2], code, stderr.Bytes(), c.why)
			}
			noToken(t, "the error line of stillhere "+c.args[2], stderr.Bytes())
		}
		var usage *domain.UsageError
		sum, syncErr := app.Sync(t.Context(), app.Deps{Forge: refusingForge{}, ForgeStore: s.store, Store: s.store, Log: log},
			app.SyncRequest{Org: org, Mirrors: mirrors, Now: fixture.Now})
		if sum != nil || !errors.As(syncErr, &usage) {
			t.Fatalf("a sync GitHub refused: summary %v, error %v; want no run and a usage error", sum, syncErr)
		}
		noToken(t, "the refused sync's error", []byte(syncErr.Error()))
		_, syncErr = app.Sync(t.Context(), app.Deps{Forge: emptyForge{}, ForgeStore: s.store, Store: s.store, Log: log},
			app.SyncRequest{Org: org, Mirrors: mirrors, Now: fixture.Now})
		if !errors.Is(syncErr, domain.ErrNoFacts) {
			t.Fatalf("a sync of an organization with no repository: %v; want no repository synced", syncErr)
		}
		noToken(t, "the empty sync's error", []byte(syncErr.Error()))
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
		if !bytes.Contains(readDB(t, s.db), []byte(redact.Scrub(mirrors))) {
			t.Error("facts.db holds no sync run options with the scrubbed --mirrors, so the check above proves nothing for sync")
		}
		if logs.Len() == 0 {
			t.Fatal("the scan and the report logged nothing at debug level, so the log check is empty")
		}
		noToken(t, "the debug log of the scan and the report", logs.Bytes())
	})
}

// refusingForge stands in for GitHub answering that it has no such
// organization: a sync through it fails before it begins its run, and
// nothing else of it is called.
type refusingForge struct{ domain.ForgeSource }

func (refusingForge) Repos(context.Context, string) ([]domain.ForgeRepo, error) {
	return nil, &domain.UsageError{Msg: "GitHub has no such organization"}
}

// emptyForge stands in for GitHub listing no repository of an
// organization: a sync through it begins its run, records its options,
// moves no mirror and fails, and nothing else of it is called.
type emptyForge struct{ domain.ForgeSource }

func (emptyForge) Repos(context.Context, string) ([]domain.ForgeRepo, error) { return nil, nil }

func (emptyForge) ReconcileMirrors(context.Context, string, []domain.ForgeRepo, string) ([]domain.MirrorMove, error) {
	return nil, nil
}

// readDB reads a facts database file whole.
func readDB(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
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

// checkSubjects checks that every value a scan stored is about a kind of
// subject its metric declares (metrics.Definition.Subjects): explain
// refuses every other kind (#69), so a kind left out of the declaration
// would hide stored values from it.
func checkSubjects(t *testing.T, s *scanned) {
	t.Helper()
	values, _, err := s.store.LoadMetrics(t.Context(), s.run)
	if err != nil {
		t.Fatal(err)
	}
	var undeclared []string
	for _, v := range values {
		m, err := metrics.Lookup(v.Metric.String())
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(m.Definition().Subjects, v.Subject.Kind) {
			undeclared = append(undeclared, v.Metric.String()+": "+v.Subject.Kind)
		}
	}
	slices.Sort(undeclared)
	if undeclared = slices.Compact(undeclared); len(undeclared) > 0 {
		t.Errorf("the scan stored values about kinds of subject their metrics do not declare (metric: kind): %v", undeclared)
	}
}

// reviewsGiven is the forge metric TestDeterminism ranks beside the change
// metrics.
var reviewsGiven = domain.MetricID{Name: "reviews_given", Version: 1}

// The forge repositories of the replica TestDeterminism's scans read: the
// fixture's, which its origin names, and another organization's, which
// none of the run's repositories is.
var (
	forgeFixture = domain.ForgeRepo{ID: "R_fixture", Key: "https://git.example.test/acme/fixture.git",
		Owner: "acme", Name: "fixture", DefaultBranch: "main"}
	forgeOther = domain.ForgeRepo{ID: "R_other", Key: "https://git.example.test/other-org/secret.git",
		Owner: "other-org", Name: "secret", DefaultBranch: "main"}
	forgeUpdated = time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC) // the fixture's watermark: its newest pull request
	forgeSynced  = time.Date(2026, time.October, 6, 22, 0, 0, 0, time.UTC)
)

// setOrigin gives a fixture repository an origin by writing its local
// config file: no git process runs and nothing touches the network.
func setOrigin(t *testing.T, repo, url string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, "[remote \"origin\"]\n\turl = %s\n", url); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// seedReplica fills the forge replica as a sync would. In the fixture's
// repository, zed reviewed two pull requests, one exactly 7 days before Now
// and one exactly at the start of reviews_given@1's 90-day window, and yan
// one. In other-org/secret, synced in March, zed approved one in the window
// too, which no scope of the run counts.
func seedReplica(t *testing.T, s *sqlite.Store) {
	t.Helper()
	ctx := t.Context()
	at := func(month time.Month, day, hour int) time.Time {
		return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
	}
	pull := func(id, repo string, n int, author string, merged time.Time, reviewer, state string, submitted time.Time) domain.PullRequest {
		return domain.PullRequest{
			ID: id, Repo: repo, Number: n, Title: "t", Author: domain.ForgeUser{Login: author}, State: domain.PullMerged,
			BaseBranch: "main", Created: merged.Add(-24 * time.Hour), Updated: merged, Closed: merged, Merged: merged,
			Reviews: []domain.Review{{ID: "R" + id, Author: domain.ForgeUser{Login: reviewer}, State: state, Submitted: submitted}},
		}
	}
	pages := []domain.ForgePage{{Repo: forgeFixture.ID, Updated: forgeUpdated, PullRequests: []domain.PullRequest{
		pull("PR_1", forgeFixture.ID, 1, "lena", forgeUpdated, "zed", domain.ReviewApproved, fixture.Now.Add(-7*24*time.Hour)),
		pull("PR_2", forgeFixture.ID, 2, "lena", at(time.July, 10, 12), "zed", domain.ReviewCommented, fixture.Now.Add(-90*24*time.Hour)),
		pull("PR_3", forgeFixture.ID, 3, "zed", at(time.September, 15, 12), "yan", domain.ReviewApproved, at(time.September, 14, 10)),
	}}, {Repo: forgeOther.ID, Updated: at(time.September, 30, 12), PullRequests: []domain.PullRequest{
		pull("PR_o1", forgeOther.ID, 1, "dana", at(time.September, 30, 12), "zed", domain.ReviewApproved, at(time.September, 29, 10)),
	}}}
	if err := s.PutForgeRepos(ctx, []domain.ForgeRepo{forgeFixture, forgeOther}); err != nil {
		t.Fatal(err)
	}
	for i := range pages {
		if err := s.PutForgePage(ctx, &pages[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkForgeSynced(ctx, forgeFixture.ID, forgeSynced); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkForgeSynced(ctx, forgeOther.ID, at(time.March, 1, 0)); err != nil {
		t.Fatal(err)
	}
}

// checkForgeRun checks what a scan of the seeded replica read and counted:
// the run's own repository's watermark only, reviews_given@1's values in
// every scope that holds the fixture, their week blocks, and the dates the
// scan's report gives them.
func checkForgeRun(t *testing.T, s *scanned) {
	t.Helper()
	ctx := t.Context()
	marks, err := s.store.RunWatermarks(ctx, s.run)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.RunWatermark{{
		ForgeWatermark: domain.ForgeWatermark{Repo: forgeFixture.ID, Updated: forgeUpdated, Synced: forgeSynced},
		Name:           "acme/fixture",
		Keys:           []string{forgeFixture.Key},
	}}
	if fmt.Sprint(marks) != fmt.Sprint(want) {
		t.Errorf("RunWatermarks = %+v, want only the fixture's: %+v", marks, want)
	}
	values, _, err := s.store.LoadMetrics(ctx, s.run)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, v := range values {
		if v.Metric == reviewsGiven {
			got[v.Scope.Kind+" "+v.Scope.Key+" "+v.Subject.Key] = v.Value
		}
	}
	wantValues := map[string]float64{}
	for _, scope := range []string{"all ", "group acme", "repo " + forgeFixture.Key} {
		wantValues[scope+" @zed"], wantValues[scope+" @yan"] = 2, 1
	}
	sameRows(t, "reviews_given@1 values", got, wantValues)

	// The week blocks of zed's reviews, over the stored facts and the
	// replica the scan read: 7 days before Now is block 0, and the window's
	// first instant is in its oldest block, 12 (domain.Window.Week).
	fs := s.facts(t)
	if fs.Forge, err = s.store.LoadForge(ctx, fixture.Now.AddDate(0, 0, -365)); err != nil {
		t.Fatal(err)
	}
	p, err := policy.Default()
	if err != nil {
		t.Fatal(err)
	}
	m, err := metrics.Lookup(reviewsGiven.String())
	if err != nil {
		t.Fatal(err)
	}
	ri, err := m.(metrics.Ranked).Clusters(&metrics.Input{Facts: fs, Policy: p}, domain.Scope{Kind: domain.ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	wantBlocks := []rank.Cluster{{Change: "PR_1", Num: 1, Block: 0}, {Change: "PR_2", Num: 1, Block: 12}}
	if gotBlocks := ri.Data[domain.Subject{Kind: domain.SubjectPerson, Key: "@zed"}]; fmt.Sprint(gotBlocks) != fmt.Sprint(wantBlocks) {
		t.Errorf("zed's reviews_given@1 clusters = %v, want %v", gotBlocks, wantBlocks)
	}

	// The scan's report dates the GitHub numbers by the fixture's sync
	// alone: the sibling has no origin, so no GitHub data, which the rank
	// table's caveat says.
	var table *report.RankTable
	for i := range s.model.Ranks {
		if s.model.Ranks[i].Metric == reviewsGiven.String() {
			table = &s.model.Ranks[i]
		}
	}
	switch {
	case table == nil:
		t.Fatal("the scan's report has no reviews_given@1 rank table")
	case table.Provenance.Forge == nil || table.Provenance.Forge.AsOf == nil || !table.Provenance.Forge.AsOf.Equal(forgeSynced):
		t.Errorf("the reviews_given@1 table's provenance.forge = %+v, want as_of %v", table.Provenance.Forge, forgeSynced)
	case !slices.ContainsFunc(table.Caveats, func(c report.Caveat) bool {
		return c.Code == "forge-not-synced" && slices.Equal(c.Repos, []string{"sibling"})
	}):
		t.Errorf("the reviews_given@1 table's caveats %+v do not name the sibling as without GitHub data", table.Caveats)
	}
	// Beside each count, the table shows the share of approvals with no
	// comment, the counterweight reviews_given@1's definition promises
	// (PF-5): zed's one approval, of PR_1, has none.
	if table != nil {
		var zed *report.RankRow
		for _, rows := range [][]report.RankRow{table.Ranked, table.BelowMin} {
			for i := range rows {
				if rows[i].Email == "@zed" {
					zed = &rows[i]
				}
			}
		}
		if table.Beside != "no_comment_share" || zed == nil || zed.Beside == nil || *zed.Beside != 1 {
			t.Errorf("the reviews_given@1 table shows %q beside the count and zed's row %+v, want no_comment_share 1", table.Beside, zed)
		}
	}
}

// forgetDetail deletes the Detail key from every value of id in run in the
// facts.db at db, as a stillhere before the key was written stored them.
// It fails the test when no value held it, which would leave the case
// checking nothing.
func forgetDetail(t *testing.T, db string, run int64, id domain.MetricID, key string) {
	t.Helper()
	raw, err := sql.Open("sqlite", db+"?_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	res, err := raw.ExecContext(t.Context(), `DELETE FROM metric_details WHERE run_id = ? AND name = ? AND value_seq IN
		(SELECT seq FROM metric_values WHERE run_id = ? AND metric = ? AND version = ?)`, run, key, run, id.Name, id.Version)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		t.Fatalf("run %d holds no %s detail %s to forget (%v), so this case checks nothing", run, id, key, err)
	}
}

// staleKnowledge makes run's knowledge values in the facts.db at db what a
// stillhere before wave 10 stored: knowledge_loss@1 without its going_quiet
// detail, every line counted as gone, and knowledge_concentration@1 one
// author more, values the old activity and identity rules could give. It
// fails the test when the run holds no knowledge_loss@1 value.
func staleKnowledge(t *testing.T, db string, run int64) {
	t.Helper()
	raw, err := sql.Open("sqlite", db+"?_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	for _, q := range []string{
		`DELETE FROM metric_details WHERE run_id = ?1 AND name IN ('going_quiet_num', 'going_quiet_people') AND value_seq IN
			(SELECT seq FROM metric_values WHERE run_id = ?1 AND metric = 'knowledge_loss')`,
		`UPDATE metric_values SET num = den, value = 1 WHERE run_id = ?1 AND metric = 'knowledge_loss'`,
		`UPDATE metric_values SET num = num + 1, value = value + 1 WHERE run_id = ?1 AND metric = 'knowledge_concentration'`,
	} {
		res, err := raw.ExecContext(t.Context(), q, run)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			t.Fatalf("%s changed %d rows (%v): the run holds no knowledge value to make stale, so this case checks nothing", q, n, err)
		}
	}
}

// forgetMetric deletes every value of id from run in the facts.db at db, as
// if a stillhere older than the metric had stored the run, and numbers the
// values left as the store numbers them. It fails the test when the run
// held none, which would leave the case checking nothing.
func forgetMetric(t *testing.T, db string, run int64, id domain.MetricID) {
	t.Helper()
	ctx := t.Context()
	raw, err := sql.Open("sqlite", db+"?_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec(`DELETE FROM metric_details WHERE run_id = ? AND value_seq IN
		(SELECT seq FROM metric_values WHERE run_id = ? AND metric = ? AND version = ?)`, run, run, id.Name, id.Version)
	if exec(`DELETE FROM metric_values WHERE run_id = ? AND metric = ? AND version = ?`, run, id.Name, id.Version) == 0 {
		t.Fatalf("run %d holds no value of %s to forget, so this case checks nothing", run, id)
	}
	// The values left, numbered from 0 again, through negative numbers so
	// that no two rows share a key on the way.
	exec(`CREATE TEMP TABLE remap AS SELECT seq AS old, ROW_NUMBER() OVER (ORDER BY seq) - 1 AS new
		FROM metric_values WHERE run_id = ?`, run)
	for _, q := range []string{
		`UPDATE metric_details SET value_seq = -1 - (SELECT new FROM remap WHERE old = value_seq) WHERE run_id = ?`,
		`UPDATE metric_details SET value_seq = -1 - value_seq WHERE run_id = ?`,
		`UPDATE metric_values SET seq = -1 - (SELECT new FROM remap WHERE old = seq) WHERE run_id = ?`,
		`UPDATE metric_values SET seq = -1 - seq WHERE run_id = ?`,
	} {
		exec(q, run)
	}
	exec(`DROP TABLE remap`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// landingGrainReverts makes run's revert links in the facts.db at db what a
// stillhere before #43 stored: each names, as its revert end, the landing
// whose map holds the revert commit, the merge for one a merge brought in,
// and two links of one landing to one commit are one. It fails the test
// when no link names a commit a merge brought in, which would leave the
// case checking nothing.
func landingGrainReverts(t *testing.T, s *scanned) {
	t.Helper()
	ctx := t.Context()
	fs := s.facts(t)
	raw, err := sql.Open("sqlite", s.db+"?_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	moved := 0
	for _, rf := range fs.Repos {
		landingOf := map[string]string{}
		for _, l := range rf.Landings {
			for _, sha := range l.Commits {
				landingOf[sha] = l.SHA
			}
		}
		seen := map[domain.Revert]bool{}
		var links []domain.Revert
		for _, rv := range rf.Reverts {
			if l, ok := landingOf[rv.SHA]; ok && l != rv.SHA {
				rv.SHA = l
				moved++
			}
			if !seen[rv] {
				seen[rv] = true
				links = append(links, rv)
			}
		}
		var repo int64
		if err := raw.QueryRowContext(ctx, `SELECT id FROM run_repos WHERE run_id = ? AND repo_key = ?`,
			s.run, rf.Repo.Key).Scan(&repo); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, `DELETE FROM reverts WHERE repo_id = ?`, repo); err != nil {
			t.Fatal(err)
		}
		for i, rv := range links {
			if _, err := raw.ExecContext(ctx, `INSERT INTO reverts (repo_id, seq, sha, reverted) VALUES (?, ?, ?, ?)`,
				repo, i, rv.SHA, rv.Reverted); err != nil {
				t.Fatal(err)
			}
		}
	}
	if moved == 0 {
		t.Fatalf("run %d holds no revert link by a commit a merge brought in, so this case checks nothing", s.run)
	}
}
