package github_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/github"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// plantedToken is the token the test hands the adapter. It must reach the
// API (in an Authorization header) and the git server (in a Basic header),
// and nothing else: no fact, mirror file, log line or error.
const plantedToken = "ghp_PLANTEDstillhereTOKEN0000000000000000"

// TestGitHubSync drives the GitHub adapter through the recorded responses
// in testdata/github, with no live call: the API is replayed in order, and
// the mirrors clone from a local git server that, like GitHub, accepts only
// the token. The expected facts, waits and reviews_given@1 values are
// derived by hand in testdata/github/README.md.
func TestGitHubSync(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", t.TempDir()) // go-gh must not read the developer's gh config
	ctx := t.Context()

	server := newGitServer(t, plantedToken)
	api := loadCassette(t, filepath.Join("..", "..", "testdata", "github", "exchanges.json"))
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	git, err := gitcli.New(ctx, gitcli.Config{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	// The local clock moves only when the adapter waits, so every time it
	// derives from GitHub's Date headers, a pass's start among them, is the
	// recorded one.
	clock := &fakeClock{}
	var waits []time.Duration
	src, err := github.New(ctx, github.Config{
		Token:     plantedToken,
		Git:       git,
		Logger:    logger,
		UserAgent: "stillhere/test",
		GitURL:    server.url,
		Transport: api,
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			clock.advance(d)
			return nil
		},
		Clock:        clock.now,
		ChangedPaths: git.AtLeast(2, 46),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := server.url
	widgetKey := base + "/acme/widget.git"

	// The listing: two pages, every repository with its flags, sorted by Key.
	repos, err := src.Repos(ctx, "acme")
	if err != nil {
		t.Fatalf("Repos: %v", err)
	}
	wantRepos := []domain.ForgeRepo{
		{ID: "R_empty", Key: base + "/acme/empty-repo.git", Owner: "acme", Name: "empty-repo"},
		{ID: "R_gadget", Key: base + "/acme/gadget.git", Owner: "acme", Name: "gadget", DefaultBranch: "trunk"},
		{ID: "R_legacy", Key: base + "/acme/legacy.git", Owner: "acme", Name: "legacy", DefaultBranch: "main", Archived: true},
		{ID: "R_fork", Key: base + "/acme/upstream-fork.git", Owner: "acme", Name: "upstream-fork", DefaultBranch: "main", Fork: true},
		{ID: "R_widget", Key: widgetKey, Owner: "acme", Name: "widget", DefaultBranch: "main"},
	}
	equal(t, "Repos", repos, wantRepos)
	widget := repos[4]

	// The mirror: a bare clone with the refs/heads/* refspec only, so the
	// pull request ref the server advertises stays out, and HEAD on the
	// default branch the API reported.
	root := filepath.Join(t.TempDir(), "mirrors")
	dir, err := src.Mirror(ctx, widget, root)
	if err != nil {
		t.Fatalf("Mirror (clone): %v", err)
	}
	if want := filepath.Join(root, "acme", "widget.git"); dir != want {
		t.Fatalf("Mirror path = %s, want %s", dir, want)
	}
	inspect := func(args ...string) string {
		t.Helper()
		out, err := git.Run(ctx, domain.GitCmd{Dir: dir, Args: args})
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	equal(t, "mirror refs after the clone", inspect("for-each-ref", "--format=%(refname)"),
		"refs/heads/feature\nrefs/heads/main")
	equal(t, "mirror HEAD", inspect("symbolic-ref", "HEAD"), "refs/heads/main")
	equal(t, "mirror bare", inspect("rev-parse", "--is-bare-repository"), "true")
	equal(t, "mirror remote.origin.url", inspect("config", "--get", "remote.origin.url"), widgetKey)
	equal(t, "mirror remote.origin.fetch", inspect("config", "--get-all", "remote.origin.fetch"), "+refs/heads/*:refs/heads/*")
	// #56: the mirror has a commit-graph, with version 2 changed-path
	// filters where git writes them (2.46 and later), which scan's check
	// finds; a scan of a user's clone without one prints a hint instead.
	if _, err := os.Stat(filepath.Join(dir, "objects", "info", "commit-graph")); err != nil {
		t.Fatalf("the mirror has no commit-graph: %v", err)
	}
	graph, filters, err := collect.CommitGraph(ctx, git, domain.Repo{Path: dir, Bare: true})
	if err != nil || !graph || filters != git.AtLeast(2, 46) {
		t.Fatalf("CommitGraph(mirror) = %v, %v, %v; want a graph, filters %v on git %s",
			graph, filters, err, git.AtLeast(2, 46), git.Version())
	}

	// A later sync fetches with --prune and re-points HEAD: upstream drops
	// feature, adds topic, and (as the API now says) makes topic the
	// default branch.
	server.git(t, "update-ref", "-d", "refs/heads/feature")
	server.git(t, "update-ref", "refs/heads/topic", "refs/heads/main")
	renamed := widget
	renamed.DefaultBranch = "topic"
	if _, err := src.Mirror(ctx, renamed, root); err != nil {
		t.Fatalf("Mirror (fetch): %v", err)
	}
	equal(t, "mirror refs after the fetch", inspect("for-each-ref", "--format=%(refname)"),
		"refs/heads/main\nrefs/heads/topic")
	equal(t, "mirror HEAD after the default branch moved", inspect("symbolic-ref", "HEAD"), "refs/heads/topic")
	equal(t, "mirror node id", inspect("config", "--get", "stillhere.forgeId"), "R_widget")

	// Upstream renames widget to gizmo; the node id stays. ReconcileMirrors
	// moves the mirror to the new name before any fetch, and Mirror fetches
	// from the new URL: one mirror, where a clone under the new name would
	// leave two that scan counts twice.
	server.rename(t, "gizmo")
	gizmo := renamed
	gizmo.Name, gizmo.Key = "gizmo", base+"/acme/gizmo.git"
	listing := []domain.ForgeRepo{repos[0], repos[1], repos[2], repos[3], gizmo}
	widgetDir, gizmoDir := dir, filepath.Join(root, "acme", "gizmo.git")
	moves, err := src.ReconcileMirrors(ctx, "acme", listing, root)
	if err != nil {
		t.Fatalf("ReconcileMirrors (rename): %v", err)
	}
	equal(t, "moves after the rename", moves, []domain.MirrorMove{
		{Repo: "R_widget", From: widgetDir, To: gizmoDir, Reason: domain.MirrorRenamed},
	})
	if dir, err = src.Mirror(ctx, gizmo, root); err != nil {
		t.Fatalf("Mirror (after the rename): %v", err)
	}
	equal(t, "Mirror path after the rename", dir, gizmoDir)
	equal(t, "mirrors after the rename", mirrorsIn(t, filepath.Join(root, "acme")), []string{"gizmo.git"})
	equal(t, "mirror remote.origin.url after the rename", inspect("config", "--get", "remote.origin.url"), gizmo.Key)
	equal(t, "mirror refs after the rename", inspect("for-each-ref", "--format=%(refname)"),
		"refs/heads/main\nrefs/heads/topic")

	// The repository leaves the listing (deleted, transferred to another
	// owner, or hidden from the token): its mirror moves out of scan's way,
	// to root/.stale, and comes back when the repository is listed again.
	staleDir := filepath.Join(root, ".stale", "acme", "gizmo.git")
	if moves, err = src.ReconcileMirrors(ctx, "acme", repos[:4], root); err != nil {
		t.Fatalf("ReconcileMirrors (unlisted): %v", err)
	}
	equal(t, "moves once the repository is no longer listed", moves, []domain.MirrorMove{
		{Repo: "R_widget", From: gizmoDir, To: staleDir, Reason: domain.MirrorStale},
	})
	equal(t, "mirrors while the repository is not listed", mirrorsIn(t, filepath.Join(root, "acme")), []string(nil))
	if moves, err = src.ReconcileMirrors(ctx, "acme", listing, root); err != nil {
		t.Fatalf("ReconcileMirrors (listed again): %v", err)
	}
	equal(t, "moves once the repository is listed again", moves, []domain.MirrorMove{
		{Repo: "R_widget", From: staleDir, To: gizmoDir, Reason: domain.MirrorRestored},
	})
	if moves, err = src.ReconcileMirrors(ctx, "acme", listing, root); err != nil || moves != nil {
		t.Fatalf("ReconcileMirrors with every mirror in place = %v, %v; want no move", moves, err)
	}
	if server.authorized.Load() == 0 || server.refused.Load() != 0 {
		t.Fatalf("git server: %d authorized, %d refused requests; want every request authorized by the token",
			server.authorized.Load(), server.refused.Load())
	}

	// A git server that redirects to another host gets no further: git
	// follows no redirect, so the header that carries the token never
	// reaches the other host, which would get it on the requests after the
	// first.
	redirect := newRedirectServer(t)
	via, err := github.New(ctx, github.Config{Token: plantedToken, Git: git, Logger: logger, GitURL: redirect.url, Transport: api})
	if err != nil {
		t.Fatal(err)
	}
	_, redirectErr := via.Mirror(ctx, repos[1], filepath.Join(t.TempDir(), "redirected"))
	if redirectErr == nil || redirect.redirected.Load() == 0 || redirect.elsewhere.Load() != 0 {
		t.Fatalf("Mirror behind a redirect: err %v, %d redirects, %d requests to the other host; want an error, a redirect and none",
			redirectErr, redirect.redirected.Load(), redirect.elsewhere.Load())
	}

	// The first pass: newest update first, down to a year before the sync,
	// through a secondary rate limit, a nearly spent budget, a 502 and a
	// field GitHub could not resolve (#99). Only the last page carries the
	// watermark: the time the pass began on GitHub's clock (E3's Date), not
	// a time read from a pull request (#101).
	var pages []domain.ForgePage
	keep := func(p *domain.ForgePage) error {
		pages = append(pages, *p)
		return nil
	}
	if err := src.PullRequests(ctx, widget, ts("2025-10-01T12:00:01Z"), keep); err != nil {
		t.Fatalf("PullRequests (first pass): %v", err)
	}
	first := firstPass()
	equal(t, "first pass", pages, first)
	if !strings.Contains(logs.String(), `msg="GitHub could not resolve some accounts (deleted since); read as no account" repo=acme/widget fields=1`) {
		t.Errorf("the first pass did not log the field GitHub could not resolve:\n%s", logs.String())
	}

	// The later pass, a week later, from the first pass's start: newest
	// update first, down to it less five minutes. #9, older, sits before #10
	// on the page, which the stop rule skips rather than takes for the end
	// of the range, so #10, inside the overlap, is read again; the next page
	// holds nothing that recent, which ends the pass.
	clock.advance(7 * 24 * time.Hour)
	since := pages[len(pages)-1].Updated
	pages = nil
	if err := src.PullRequests(ctx, widget, since, keep); err != nil {
		t.Fatalf("PullRequests (later pass): %v", err)
	}
	second := secondPass()
	equal(t, "later pass", pages, second)

	// GitHub's count of widget's pull requests settled before the later pass
	// began, five minutes before its start: the completeness check's side.
	count, err := src.CountPullRequests(ctx, widget, time.Time{}, second[len(second)-1].Updated)
	if err != nil || count != 10 {
		t.Fatalf("CountPullRequests = %d, %v; want 10", count, err)
	}
	api.done(t)

	// The waits, all measured on GitHub's clock (the Date header): the reset
	// a 200 announced in its headers, Retry-After, the reset the budget
	// reported, at least a minute after a secondary limit in a 200, and a
	// backoff after the 502. A rate limit holds every request until it is
	// over, and the request that met it waits once.
	if len(waits) != 5 ||
		!within(waits[0], 10*time.Minute, 10*time.Minute+2*time.Second) ||
		!within(waits[1], 60*time.Second, 61*time.Second) ||
		!within(waits[2], 10*time.Minute, 10*time.Minute+2*time.Second) ||
		!within(waits[3], time.Minute, 2*time.Minute) ||
		!within(waits[4], time.Second, 2*time.Second) {
		t.Fatalf("waits = %v; want [10m, 10m2s], [60s, 61s), [10m, 10m2s], [1m, 2m), [1s, 2s]", waits)
	}

	// A rate limit any request meets holds every other one until it is
	// over, a shorter limit that one meets during a longer hold included: B
	// is in flight when A meets a ten-minute limit, then meets a 30-second
	// one, and waits out the rest of A's hold before it sends again, where
	// it once sent after its own 30 seconds. The clock stands still, so the
	// waits are what each request asked for.
	holds := newHoldTransport()
	held, err := github.New(ctx, github.Config{
		Token: plantedToken, Git: git, Logger: logger, Transport: holds,
		Sleep: func(ctx context.Context, d time.Duration) error {
			holds.slept(ctx.Value(holdCaller{}).(string), d)
			return nil
		},
		Clock: func() time.Time { return ts("2026-10-09T12:00:00Z") },
	})
	if err != nil {
		t.Fatal(err)
	}
	var both sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		both.Go(func() {
			repo := domain.ForgeRepo{ID: "R_" + name, Owner: "acme", Name: name}
			if _, err := held.CountPullRequests(context.WithValue(ctx, holdCaller{}, name), repo, time.Time{},
				ts("2026-10-09T12:00:00Z")); err != nil {
				t.Errorf("CountPullRequests of %s through the hold: %v", name, err)
			}
		})
	}
	both.Wait()
	if a, b := holds.waits("a"), holds.waits("b"); len(a) != 1 || !within(a[0], 600*time.Second, 601*time.Second) ||
		len(b) != 2 || !within(b[0], 30*time.Second, 31*time.Second) || !within(b[1], 600*time.Second, 601*time.Second) {
		t.Fatalf("waits through a longer hold: a %v, b %v; want a [600s, 601s], b [30s, 31s] then [600s, 601s]", a, b)
	}

	// A token git's server refuses fails at once: no prompt, no mirror,
	// no temporary directory left, and the error does not carry the token.
	const wrongToken = "ghp_WRONGstillhereTOKEN00000000000000000000"
	bad, err := github.New(ctx, github.Config{Token: wrongToken, Git: git, Logger: logger, GitURL: server.url, Transport: api})
	if err != nil {
		t.Fatal(err)
	}
	_, mirrorErr := bad.Mirror(ctx, repos[1], root)
	if mirrorErr == nil {
		t.Fatal("Mirror with a refused token: want an error")
	}
	for _, pattern := range []string{"*gadget*", filepath.Join(".stillhere-clones", "*gadget*")} {
		if left, _ := filepath.Glob(filepath.Join(root, "acme", pattern)); len(left) != 0 {
			t.Fatalf("a refused clone left %v", left)
		}
	}

	// The token appears nowhere: not in the facts, not in any file of the
	// mirrors or their config, not in the logs, not in an error, and not
	// when a Source itself is printed.
	secrets := []string{
		plantedToken, base64.StdEncoding.EncodeToString([]byte("x-access-token:" + plantedToken)),
		wrongToken, base64.StdEncoding.EncodeToString([]byte("x-access-token:" + wrongToken)),
	}
	noSecret(t, "facts", fmt.Sprintf("%#v %#v %#v", repos, first, second), secrets)
	noSecret(t, "logs", logs.String(), append(secrets, "x-access-token", "AUTHORIZATION"))
	noSecret(t, "the refused clone's error", mirrorErr.Error(), secrets)
	noSecret(t, "the redirected clone's error", redirectErr.Error(), secrets)
	noSecret(t, "a printed Source", fmt.Sprintf("%v %+v %#v", src, src, src), secrets)
	noSecret(t, "the mirror's local config", inspect("config", "--list", "--local"), append(secrets, "extraheader"))
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		noSecret(t, path, string(b), append(secrets, "extraheader"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("reviews_given@1", func(t *testing.T) {
		reviewsGivenOracle(t, repos, append(first, second...), base)
	})
	t.Run("sync", syncScenario)
}

// syncScenario drives app.Sync through the adapter, the store and git, with
// testdata/github/sync-exchanges.json as GitHub (README.md, "The sync
// scenario"): a first sync that stops halfway moves no watermark, and its
// rerun leaves no hole (#101); a check that finds GitHub counting more than
// the replica holds is recorded, and a scan of the mirror says so; a
// replica with a hole an earlier stillhere left is repaired by one sync; an
// archived repository's mirror moves out of scan's way (#110); a shortfall
// is recorded before the full pass that is to fill it in, which may fail; a
// first sync whose check fails still records how far it read; and every
// sync with a mirror asks GitHub about each author email once (#103). Every
// sync but the fourth names its repository with --repo (#111).
func syncScenario(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	ctx := t.Context()
	server := newGitServer(t, plantedToken)
	api := loadCassette(t, filepath.Join("..", "..", "testdata", "github", "sync-exchanges.json"))
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	git, err := gitcli.New(ctx, gitcli.Config{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{}
	src, err := github.New(ctx, github.Config{
		Token: plantedToken, Git: git, Logger: logger, GitURL: server.url, Transport: api,
		Sleep:        func(_ context.Context, d time.Duration) error { clock.advance(d); return nil },
		Clock:        clock.now,
		ChangedPaths: git.AtLeast(2, 46),
	})
	if err != nil {
		t.Fatal(err)
	}
	open := func() *sqlite.Store {
		s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "facts.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	root := filepath.Join(t.TempDir(), "mirrors")
	widget := domain.ForgeRepo{ID: "R_widget", Key: server.url + "/acme/widget.git", Owner: "acme", Name: "widget", DefaultBranch: "main"}
	run := func(store *sqlite.Store, now string, org string, repos ...string) (*app.SyncSummary, error) {
		return app.Sync(ctx, app.Deps{Forge: src, ForgeStore: store, Store: store, Log: logger},
			app.SyncRequest{Org: org, Repos: repos, Mirrors: root, Now: ts(now)})
	}
	mark := func(store *sqlite.Store) domain.ForgeWatermark {
		t.Helper()
		wm, err := store.ForgeWatermark(ctx, "R_widget")
		if err != nil {
			t.Fatal(err)
		}
		return wm
	}
	stored := func(store *sqlite.Store) []int {
		t.Helper()
		f, err := store.LoadForge(ctx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		var numbers []int
		for _, p := range f.PullRequests {
			numbers = append(numbers, p.Number)
		}
		return numbers
	}
	links := func(store *sqlite.Store) map[string]string {
		t.Helper()
		l, err := store.ForgeLinks(ctx, "R_widget")
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	// GitHub's answer about each email of the main fixture's history
	// (sync-links.json): ada's two addresses are one account, and gina's
	// account, deleted since, is a NOT_FOUND beside a null.
	wantLinks := map[string]string{
		"ada@example.com": "ada", "ada@other.example": "ada", "bob@example.com": "bob", "cara@example.com": "cara",
		"copilot@bot.example": "", "dana@example.com": "", "erin@example.com": "erin", "frank@example.com": "frank",
		"gina@example.com": "", "hank@example.com": "hank", "ivy@example.com": "", "jules@example.com": "",
	}
	// holed returns a facts.db as an earlier stillhere left it: a watermark
	// that is a time read from a pull request, #8's, which it holds alone,
	// with a hole below.
	var holedFrom *sqlite.Store
	holed := func() *sqlite.Store {
		t.Helper()
		h := open()
		if err := h.PutForgeRepos(ctx, []domain.ForgeRepo{widget}); err != nil {
			t.Fatal(err)
		}
		f, err := holedFrom.LoadForge(ctx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		pr08 := f.PullRequests[slices.IndexFunc(f.PullRequests, func(p domain.PullRequest) bool { return p.Number == 8 })]
		if err := h.PutForgePage(ctx, &domain.ForgePage{Repo: "R_widget", PullRequests: []domain.PullRequest{pr08},
			Updated: pr08.Updated}); err != nil {
			t.Fatal(err)
		}
		if err := h.MarkForgeSynced(ctx, "R_widget", ts("2026-10-08T10:00:00Z")); err != nil {
			t.Fatal(err)
		}
		return h
	}

	// Run 1: a first sync whose second page fails with an error that no
	// retry fixes and no null explains (S1 to S3). The page it stored stays,
	// and no watermark moves: the first sync did not finish.
	a := open()
	sum, err := run(a, "2026-10-09T00:00:00Z", "", "acme/widget")
	if err == nil || !errors.Is(err, domain.ErrNoFacts) {
		t.Fatalf("run 1: %v; want no repository synced", err)
	}
	equal(t, "run 1's summary", *sum, app.SyncSummary{Repos: 1, Failed: 1, Pulls: 2, Took: sum.Took})
	equal(t, "run 1's watermark", mark(a), domain.ForgeWatermark{Repo: "R_widget", Pending: true})
	equal(t, "run 1's pull requests", stored(a), []int{5, 8})
	// The mirror is up to date, so the links pass ran (L1), whatever the
	// pull requests did: every email once, through its newest commit.
	equal(t, "run 1's author links", links(a), wantLinks)
	if !strings.Contains(logs.String(), `msg="GitHub could not resolve some accounts (deleted since); read as no account" repo=acme/widget fields=1`) {
		t.Errorf("the links pass did not log the account GitHub could not resolve:\n%s", logs.String())
	}

	// Run 2, its rerun (S4 to S7): the whole window again, a watermark at
	// the pass's start on GitHub's clock (S4's Date), complete from a year
	// before the sync. GitHub counts one more pull request than the list
	// held, which the replica records as missing.
	sum, err = run(a, "2026-10-09T01:00:00Z", "", "acme/widget")
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	equal(t, "run 2's summary", *sum, app.SyncSummary{Repos: 1, Synced: 1, Incomplete: 1, Pulls: 4, Took: sum.Took})
	equal(t, "run 2's watermark", mark(a), domain.ForgeWatermark{Repo: "R_widget",
		Updated: ts("2026-10-09T01:00:00Z"), From: ts("2025-10-09T01:00:00Z"), Synced: ts("2026-10-09T01:00:00Z"), Missing: 1})
	equal(t, "run 2's pull requests", stored(a), []int{5, 8, 9, 10})
	// Run 2 asked GitHub about no email: run 1 had asked about them all.
	equal(t, "run 2's author links", links(a), wantLinks)

	// A scan of the mirror reads that replica, and the pages that show its
	// GitHub numbers say the data is incomplete (ADR 0013).
	pol, err := policy.Default()
	if err != nil {
		t.Fatal(err)
	}
	model, err := app.Scan(ctx, app.Deps{Git: git, Store: a, ForgeStore: a, Policy: pol, Log: logger},
		app.ScanRequest{Paths: []string{filepath.Join(root, "acme")}, Now: ts("2026-10-09T01:30:00Z")})
	if err != nil {
		t.Fatalf("scan of the mirror: %v", err)
	}
	var doc bytes.Buffer
	if err := report.JSON(&doc, model); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.String(), `"code": "forge-incomplete"`) &&
		!strings.Contains(doc.String(), `"code":"forge-incomplete"`) {
		t.Errorf("the scan of an incomplete replica carries no forge-incomplete caveat:\n%s", doc.String())
	}

	// Run 3, over a replica an earlier stillhere left with a hole (S8 to
	// S14): its watermark is a time read from a pull request, #8's, and it
	// holds #8 alone. The pass down to that watermark completes, the check
	// counts 4 in the window against 1, and the same sync reads the whole
	// window again: no hole, nothing missing, the watermark at the second
	// pass's start (S11's Date).
	holedFrom = a
	b := holed()
	sum, err = run(b, "2026-10-09T02:00:00Z", "", "acme/widget")
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	equal(t, "run 3's summary", *sum, app.SyncSummary{Repos: 1, Synced: 1, Pulls: 5, Took: sum.Took})
	equal(t, "run 3's watermark", mark(b), domain.ForgeWatermark{Repo: "R_widget",
		Updated: ts("2026-10-09T02:00:03Z"), From: ts("2025-10-09T02:00:00Z"), Synced: ts("2026-10-09T02:00:00Z")})
	equal(t, "run 3's pull requests", stored(b), []int{5, 8, 9, 10})
	equal(t, "run 3's author links", links(b), wantLinks) // L2

	// Run 4: the organization now lists widget archived (S15). The sync
	// leaves it out, and its mirror moves to .stale, where scan never
	// looks; nothing is left to sync.
	sum, err = run(a, "2026-10-09T03:00:00Z", "acme")
	if err == nil || !errors.Is(err, domain.ErrNoFacts) {
		t.Fatalf("run 4: %v; want nothing to sync", err)
	}
	equal(t, "run 4's summary", *sum, app.SyncSummary{Took: sum.Took})
	equal(t, "mirrors of acme after run 4", mirrorsIn(t, filepath.Join(root, "acme")), []string(nil))
	equal(t, "stale mirrors after run 4", mirrorsIn(t, filepath.Join(root, ".stale", "acme")), []string{"widget.git"})

	// Run 5, over the hole run 3 started from (S16 to S20): the pass down to
	// the watermark completes, the check counts 4 against 1, and the full
	// pass that is to fill the hole fails on its first page. The shortfall
	// was recorded before it began, so the replica still says 3 are missing
	// and the next sync reads the window again; the "as of" stays the last
	// complete sync's.
	c := holed()
	sum, err = run(c, "2026-10-09T04:00:00Z", "", "acme/widget")
	if err == nil || !errors.Is(err, domain.ErrNoFacts) {
		t.Fatalf("run 5: %v; want no repository synced", err)
	}
	equal(t, "run 5's summary", *sum, app.SyncSummary{Repos: 1, Failed: 1, Pulls: 1, Took: sum.Took})
	equal(t, "run 5's watermark", mark(c), domain.ForgeWatermark{Repo: "R_widget",
		Updated: ts("2026-10-09T04:00:00Z"), Synced: ts("2026-10-08T10:00:00Z"), Missing: 3})
	equal(t, "run 5's author links", links(c), wantLinks) // L3

	// Run 6, a first sync into a fresh facts.db (S21 to S24): the pass reads
	// the whole window and completes, and the check fails. The replica is
	// synced and complete from the window's start, which a later --since
	// compares against; nothing is recorded missing.
	d := open()
	sum, err = run(d, "2026-10-09T05:00:00Z", "", "acme/widget")
	if err != nil {
		t.Fatalf("run 6: %v", err)
	}
	equal(t, "run 6's summary", *sum, app.SyncSummary{Repos: 1, Synced: 1, Unchecked: 1, Pulls: 4, Took: sum.Took})
	equal(t, "run 6's watermark", mark(d), domain.ForgeWatermark{Repo: "R_widget",
		Updated: ts("2026-10-09T05:00:00Z"), From: ts("2025-10-09T05:00:00Z"), Synced: ts("2026-10-09T05:00:00Z")})
	equal(t, "run 6's author links", links(d), wantLinks) // L4
	api.done(t)
	noSecret(t, "the sync scenario's logs", logs.String(), []string{plantedToken, "AUTHORIZATION"})
}

// fakeClock is a local clock that moves only when the test moves it, as
// the adapter's Sleep does.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// reviewsGivenOracle stores the pages the two passes returned in a real
// facts.db, as sync does (sqlite.Open, PutForgeRepos, PutForgePage), reads
// the replica back as scan does (LoadForge, from a year before the
// fixture's Now) and from a time between the passes, and checks
// reviews_given@1 over what LoadForge returns against the values derived by
// hand in testdata/github/README.md#reviews_given1.
func reviewsGivenOracle(t *testing.T, repos []domain.ForgeRepo, pages []domain.ForgePage, gitURL string) {
	ctx := t.Context()
	// The replica outlives runs: an earlier sync of another organization
	// left other-org/secret in it, with a merged pull request bob approved
	// in the window. This run did not scan that repository, so it counts in
	// no scope. cara's comment, submitted first, has the larger review ID,
	// so the round trip tells submission order from ID order.
	secret := domain.ForgeRepo{ID: "R_secret", Key: gitURL + "/other-org/secret.git", Owner: "other-org", Name: "secret", DefaultBranch: "main"}
	secretPull := domain.PullRequest{
		ID: "PR_s1", Repo: "R_secret", Number: 1, Title: "feat: a secret", Author: user("dana"),
		State: domain.PullMerged, BaseBranch: "main", Created: ts("2026-09-28T09:00:00Z"),
		Updated: ts("2026-09-30T12:00:00Z"), Closed: ts("2026-09-30T12:00:00Z"), Merged: ts("2026-09-30T12:00:00Z"),
		MergeCommit: commit("5e00000000000000000000000000000000000001", "dana@example.com", "dana"),
		Reviews: []domain.Review{
			review("PRR_s1b", user("cara"), domain.ReviewCommented, "2026-09-30T10:00:00Z", 12, 0),
			review("PRR_s1a", user("bob"), domain.ReviewApproved, "2026-09-30T11:00:00Z", 0, 0),
		},
		Commits: []domain.ForgeCommit{commit("5e00000000000000000000000000000000000002", "dana@example.com", "dana")},
	}
	// The run also scanned acme/sprocket, whose collection failed: a failed
	// repository adds nothing to any number, so its pull request, which bob
	// approved in the window, counts in no scope either, its group's
	// included.
	sprocket := domain.ForgeRepo{ID: "R_sprocket", Key: gitURL + "/acme/sprocket.git", Owner: "acme", Name: "sprocket", DefaultBranch: "main"}
	sprocketPull := domain.PullRequest{
		ID: "PR_p1", Repo: "R_sprocket", Number: 1, Title: "fix: a sprocket", Author: user("dana"),
		State: domain.PullMerged, BaseBranch: "main", Created: ts("2026-09-28T09:00:00Z"),
		Updated: ts("2026-09-29T12:00:00Z"), Closed: ts("2026-09-29T12:00:00Z"), Merged: ts("2026-09-29T12:00:00Z"),
		MergeCommit: commit("6e00000000000000000000000000000000000001", "dana@example.com", "dana"),
		Reviews:     []domain.Review{review("PRR_p1a", user("bob"), domain.ReviewApproved, "2026-09-29T10:00:00Z", 0, 0)},
		Commits:     []domain.ForgeCommit{commit("6e00000000000000000000000000000000000002", "dana@example.com", "dana")},
	}
	pages = append(pages,
		domain.ForgePage{Repo: "R_secret", PullRequests: []domain.PullRequest{secretPull}, Updated: secretPull.Updated},
		domain.ForgePage{Repo: "R_sprocket", PullRequests: []domain.PullRequest{sprocketPull}, Updated: sprocketPull.Updated})
	repos = append(slices.Clone(repos), secret, sprocket)

	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "facts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.PutForgeRepos(ctx, repos); err != nil {
		t.Fatal(err)
	}
	for i := range pages {
		if err := store.PutForgePage(ctx, &pages[i]); err != nil {
			t.Fatalf("PutForgePage, page %d: %v", i+1, err)
		}
	}
	replica, err := store.LoadForge(ctx, fixture.Now.AddDate(0, 0, -365))
	if err != nil {
		t.Fatal(err)
	}

	// The round trip: LoadForge returns each pull request as the last page
	// that carried it said, every label, review and commit included, with
	// each repository's watermark at the start of its last completed pass,
	// which only a pass's last page carries, and nothing pending.
	latest := map[string]domain.PullRequest{}
	marks := map[string]time.Time{}
	for _, p := range pages {
		for _, pr := range p.PullRequests {
			latest[pr.ID] = pr
		}
		if !p.Updated.IsZero() {
			marks[p.Repo] = p.Updated
		}
	}
	var wantPulls []domain.PullRequest
	for _, pr := range latest {
		wantPulls = append(wantPulls, pr)
	}
	slices.SortFunc(wantPulls, func(a, b domain.PullRequest) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Number, b.Number))
	})
	wantRepos := slices.Clone(repos)
	slices.SortFunc(wantRepos, func(a, b domain.ForgeRepo) int { return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.ID, b.ID)) })
	wantMarks := []domain.ForgeWatermark{
		{Repo: "R_secret", Updated: marks["R_secret"]},
		{Repo: "R_sprocket", Updated: marks["R_sprocket"]},
		{Repo: "R_widget", Updated: marks["R_widget"]},
	}
	equal(t, "LoadForge after PutForgePage", replica, &domain.ForgeFacts{
		Repos:        wantRepos,
		PullRequests: wantPulls,
		Watermarks:   wantMarks,
	})
	// LoadForge's since, which a scan sets to a year before Now: from #10's
	// update, which the later pass read again, it returns the pull requests
	// updated at or after it, #10 itself included, each with its children,
	// and every repository and watermark.
	since := ts("2026-10-01T11:58:00Z")
	var wantRecent []domain.PullRequest
	var recentIDs []string
	for _, pr := range wantPulls {
		if !pr.Updated.Before(since) {
			wantRecent = append(wantRecent, pr)
			recentIDs = append(recentIDs, pr.ID)
		}
	}
	equal(t, "the pull requests updated at or after #10's update", recentIDs, []string{"PR_05", "PR_08", "PR_10"})
	recent, err := store.LoadForge(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "LoadForge from #10's update", recent, &domain.ForgeFacts{
		Repos:        wantRepos,
		PullRequests: wantRecent,
		Watermarks:   wantMarks,
	})
	// The run scanned widget through a user's own SSH clone, not the managed
	// mirror: the replica keys widget by its HTTPS clone URL, and the two
	// join by host, owner and name. sprocket's collection failed.
	u, err := url.Parse(gitURL)
	if err != nil {
		t.Fatal(err)
	}
	clone := "git@" + u.Hostname() + ":acme/widget.git"
	sprocketClone := "git@" + u.Hostname() + ":acme/sprocket.git"
	person := func(name, email string) domain.Person { return domain.Person{Name: name, Email: email} }
	landed := func(sha string, who domain.Person) domain.Commit { return domain.Commit{SHA: sha, Author: who} }
	ada, bob, erin := person("Ada Example", "ada@example.com"), person("Bob", "bob@example.com"), person("Erin", "erin@example.com")
	facts := &domain.FactSet{
		Now:    fixture.Now,
		Groups: []domain.Group{{Name: "acme", Path: "/src/acme", Repos: []string{sprocketClone, clone}}},
		Repos: []domain.RepoFacts{{
			Repo: domain.Repo{Key: sprocketClone, Name: "sprocket", Group: "acme", Path: "/src/acme/sprocket",
				Remote: sprocketClone, Branch: "main"},
			Status: domain.RepoStatusError,
			Error:  "history: collect.History: sprocket: reading landings: git log: exit 128",
		}, {
			Repo:   domain.Repo{Key: clone, Name: "widget", Group: "acme", Path: "/src/acme/widget", Remote: clone, Branch: "main"},
			Status: domain.RepoStatusOK,
			// The landings the scan saw (mailmapped by git), as of Now: the
			// squash commits of #1 to #7 and #9. Erin's commits are mailmapped
			// from erin@old.example; #8 landed after Now.
			Commits: []domain.Commit{
				landed("f010000000000000000000000000000000000000", ada),
				landed("f020000000000000000000000000000000000000", bob),
				landed("f030000000000000000000000000000000000000", erin),
				landed("f040000000000000000000000000000000000000",
					person("dependabot[bot]", "49699333+dependabot[bot]@users.noreply.github.com")),
				landed("f050000000000000000000000000000000000000", erin),
				landed("f070000000000000000000000000000000000000", person("Former", "former@example.com")),
				landed("f090000000000000000000000000000000000000", ada),
			},
		}},
		Forge: replica,
	}
	var m metrics.Metric
	for _, x := range metrics.All() {
		if x.Definition().ID == (domain.MetricID{Name: "reviews_given", Version: 1}) {
			m = x
		}
	}
	if m == nil {
		t.Fatal("reviews_given@1 is not registered")
	}
	// The packaged policy's [bots] fragments, and no Who: metrics.Compute
	// passes its Input on as it got it, and Who is optional there, so the
	// metric must build the directory itself.
	bots := &policy.Policy{Bots: policy.Bots{Fragments: []string{"copilot", "gpt-engineer", "dependabot", "renovate", "[bot]"}}}
	in := &metrics.Input{Facts: facts, Policy: bots}
	values, err := m.Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	window := domain.Window{
		Since: time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC),
	}
	type row struct {
		key                                            string
		pulls, reviews, comments, approvals, noComment int
		share                                          float64 // -1: no approvals, so no share
	}
	rows := []row{
		{"@cara", 2, 2, 2, 1, 1, 1},
		{"@copilot-pull-request-reviewer[bot]", 1, 1, 1, 0, 0, -1},
		{"@renovate-approver", 1, 1, 0, 1, 1, 1},
		{"ada@example.com", 3, 4, 1, 2, 1, 0.5},
		{"bob@example.com", 4, 5, 4, 3, 3, 1},
		{"erin@example.com", 1, 1, 0, 1, 0, 0},
	}
	// The same values in every scope that holds widget: all, its group, and
	// its own repository scope, in which a scan ranks a repository named
	// directly; none in sprocket's.
	var want []domain.MetricValue
	for _, scope := range []domain.Scope{{Kind: domain.ScopeAll}, {Kind: domain.ScopeGroup, Key: "acme"}, {Kind: domain.ScopeRepo, Key: clone}} {
		for _, r := range rows {
			detail := map[string]float64{
				"reviews":              float64(r.reviews),
				"review_comments":      float64(r.comments),
				"approvals":            float64(r.approvals),
				"approvals_no_comment": float64(r.noComment),
			}
			if r.share >= 0 {
				detail["no_comment_share"] = r.share
			}
			want = append(want, domain.MetricValue{
				Metric:  domain.MetricID{Name: "reviews_given", Version: 1},
				Scope:   scope,
				Subject: domain.Subject{Kind: domain.SubjectPerson, Key: r.key},
				Window:  window,
				Value:   float64(r.pulls), Num: float64(r.pulls), Den: 1, N: r.pulls,
				Detail: detail,
			})
		}
	}
	equal(t, "reviews_given@1 values", values, want)

	ranked, ok := m.(metrics.Ranked)
	if !ok {
		t.Fatal("reviews_given@1 is not ranked")
	}
	ri, err := ranked.Clusters(in, domain.Scope{Kind: domain.ScopeAll})
	if err != nil {
		t.Fatal(err)
	}
	subject := func(key string) domain.Subject { return domain.Subject{Kind: domain.SubjectPerson, Key: key} }
	// @renovate-approver is a User account that the [bots] fragment
	// "renovate" makes a bot (identity.Directory.Bot), so it has values and
	// no cluster. Each block is the week, counted back from Now, of the
	// person's first counted review (domain.Window.Week): no review is at a
	// week's boundary, where ⌊(Now − t) / 7 days⌋ would give one block more.
	cluster := func(pr string, block int) rank.Cluster { return rank.Cluster{Change: pr, Num: 1, Block: block} }
	if kind := m.Definition().RankKind(); kind != rank.Sum {
		t.Errorf("reviews_given@1 ranks by kind %d, want rank.Sum: a count is a total, not a share", kind)
	}
	equal(t, "reviews_given@1 clusters", ri, metrics.RankInput{
		Window:  window,
		Options: rank.Options{MinN: 10, Blocks: 13},
		Data: map[domain.Subject][]rank.Cluster{
			subject("@cara"):            {cluster("PR_01", 8), cluster("PR_09", 1)},
			subject("ada@example.com"):  {cluster("PR_02", 3), cluster("PR_03", 11), cluster("PR_05", 0)},
			subject("bob@example.com"):  {cluster("PR_01", 8), cluster("PR_04", 2), cluster("PR_05", 0), cluster("PR_09", 2)},
			subject("erin@example.com"): {cluster("PR_07", 9)},
		},
	})
}

// The SHAs of the recorded commits: cNN are pull request NN's own commits,
// fNN the commit its merge put on main.
const (
	c01a = "0100000000000000000000000000000000000001"
	c02a = "0200000000000000000000000000000000000001"
	c02b = "0200000000000000000000000000000000000002"
	c03a = "0300000000000000000000000000000000000001"
	c04a = "0400000000000000000000000000000000000001"
	c05a = "0500000000000000000000000000000000000001"
	c06a = "0600000000000000000000000000000000000001"
	c07a = "0700000000000000000000000000000000000001"
	c08a = "0800000000000000000000000000000000000001"
	c09a = "0900000000000000000000000000000000000001"
	c09b = "0900000000000000000000000000000000000002"
	c09c = "0900000000000000000000000000000000000003"
	c10a = "1000000000000000000000000000000000000001"
	f01  = "f010000000000000000000000000000000000000"
	f02  = "f020000000000000000000000000000000000000"
	f03  = "f030000000000000000000000000000000000000"
	f04  = "f040000000000000000000000000000000000000"
	f05  = "f050000000000000000000000000000000000000"
	f07  = "f070000000000000000000000000000000000000"
	f08  = "f080000000000000000000000000000000000000"
	f09  = "f090000000000000000000000000000000000000"

	dependabotEmail = "49699333+dependabot[bot]@users.noreply.github.com"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func user(login string) domain.ForgeUser { return domain.ForgeUser{Login: login} }

func appAccount(login string) domain.ForgeUser { return domain.ForgeUser{Login: login, Bot: true} }

func commit(sha, email, login string) domain.ForgeCommit {
	return domain.ForgeCommit{SHA: sha, AuthorEmail: email, AuthorLogin: login}
}

func review(id string, author domain.ForgeUser, state, submitted string, bodyLength, comments int) domain.Review {
	return domain.Review{ID: id, Author: author, State: state, Submitted: ts(submitted), BodyLength: bodyLength, Comments: comments}
}

// firstPass is what the first pass hands put: pages 1 to 3 of
// testdata/github/pulls-asc-*.json, with PR 9 completed by E8 to E10, and
// the watermark of each page (README.md, "First pass").
func firstPass() []domain.ForgePage {
	merged := func(p domain.PullRequest, at string) domain.PullRequest {
		p.State, p.Closed, p.Merged = domain.PullMerged, ts(at), ts(at)
		return p
	}
	pr03 := merged(domain.PullRequest{
		ID: "PR_03", Repo: "R_widget", Number: 3, Title: "docs: explain the widget limits", Author: user("erin"),
		BaseBranch: "main", Created: ts("2026-05-28T10:00:00Z"), Updated: ts("2026-07-20T12:00:00Z"),
		MergeCommit: commit(f03, "erin@old.example", "erin"),
		Reviews: []domain.Review{
			review("PRR_03a", user("bob"), domain.ReviewApproved, "2026-06-01T08:00:00Z", 0, 0),
			review("PRR_03b", user("ada"), domain.ReviewCommented, "2026-07-20T12:00:00Z", 34, 0),
		},
		Commits: []domain.ForgeCommit{commit(c03a, "erin@old.example", "erin")},
	}, "2026-06-01T09:00:00Z")
	pr07 := merged(domain.PullRequest{
		ID: "PR_07", Repo: "R_widget", Number: 7, Title: "docs: fix a typo", Author: domain.ForgeUser{},
		BaseBranch: "main", Created: ts("2026-07-25T10:00:00Z"), Updated: ts("2026-08-01T09:00:00Z"),
		MergeCommit: commit(f07, "former@example.com", ""),
		Labels:      []string{"docs"},
		Reviews:     []domain.Review{review("PRR_07a", user("erin"), domain.ReviewApproved, "2026-07-30T17:00:00Z", 4, 0)},
		Commits:     []domain.ForgeCommit{commit(c07a, "former@example.com", "")},
	}, "2026-08-01T09:00:00Z")
	pr01 := merged(domain.PullRequest{
		ID: "PR_01", Repo: "R_widget", Number: 1, Title: "feat: add the widget API", Author: user("ada"),
		BaseBranch: "main", Created: ts("2026-08-03T09:00:00Z"), Updated: ts("2026-08-10T12:00:00Z"),
		MergeCommit: commit(f01, "ada@example.com", "ada"),
		Labels:      []string{"feature"},
		Reviews: []domain.Review{
			review("PRR_01a", user("cara"), domain.ReviewCommented, "2026-08-09T15:00:00Z", 38, 2),
			review("PRR_01b", user("bob"), domain.ReviewApproved, "2026-08-10T10:00:00Z", 0, 0),
		},
		Commits: []domain.ForgeCommit{commit(c01a, "ada@example.com", "ada")},
	}, "2026-08-10T12:00:00Z")
	pr06 := domain.PullRequest{
		ID: "PR_06", Repo: "R_widget", Number: 6, Title: "feat: rewrite everything", Author: user("ada"),
		State: domain.PullClosed, BaseBranch: "main", Created: ts("2026-08-20T10:00:00Z"),
		Updated: ts("2026-09-02T08:00:00Z"), Closed: ts("2026-09-02T08:00:00Z"),
		Labels:  []string{"wontfix"},
		Reviews: []domain.Review{review("PRR_06a", user("bob"), domain.ReviewChangesRequested, "2026-09-01T16:00:00Z", 33, 3)},
		Commits: []domain.ForgeCommit{commit(c06a, "ada@example.com", "ada")},
	}
	pr02 := merged(domain.PullRequest{
		ID: "PR_02", Repo: "R_widget", Number: 2, Title: "fix: handle empty input", Author: user("bob"),
		BaseBranch: "main", Created: ts("2026-09-10T08:00:00Z"), Updated: ts("2026-09-15T16:00:00Z"),
		MergeCommit: commit(f02, "bob@example.com", "bob"),
		Labels:      []string{"bug"},
		Reviews: []domain.Review{
			review("PRR_02a", user("ada"), domain.ReviewChangesRequested, "2026-09-14T11:00:00Z", 34, 1),
			review("PRR_02b", user("bob"), domain.ReviewCommented, "2026-09-14T13:00:00Z", 15, 1),
			review("PRR_02c", user("ada"), domain.ReviewApproved, "2026-09-15T14:00:00Z", 0, 0),
			review("PRR_02d", user("renovate-approver"), domain.ReviewApproved, "2026-09-15T15:00:00Z", 0, 0),
		},
		Commits: []domain.ForgeCommit{commit(c02a, "bob@example.com", "bob"), commit(c02b, "bob@example.com", "bob")},
	}, "2026-09-15T16:00:00Z")
	pr04 := merged(domain.PullRequest{
		ID: "PR_04", Repo: "R_widget", Number: 4, Title: "chore(deps): bump golang.org/x/text from 0.40.0 to 0.41.0",
		Author: appAccount("dependabot"), BaseBranch: "main",
		Created: ts("2026-09-19T06:00:00Z"), Updated: ts("2026-09-20T11:00:00Z"),
		MergeCommit: commit(f04, dependabotEmail, "dependabot[bot]"),
		Labels:      []string{"dependencies"},
		Reviews: []domain.Review{
			review("PRR_04a", appAccount("copilot-pull-request-reviewer"), domain.ReviewCommented, "2026-09-19T06:05:00Z", 87, 1),
			review("PRR_04b", user("bob"), domain.ReviewApproved, "2026-09-20T10:30:00Z", 0, 0),
		},
		Commits: []domain.ForgeCommit{commit(c04a, dependabotEmail, "dependabot[bot]")},
	}, "2026-09-20T11:00:00Z")
	pr09 := merged(domain.PullRequest{
		ID: "PR_09", Repo: "R_widget", Number: 9, Title: "feat: widget import", Author: user("ada"),
		BaseBranch: "main", Created: ts("2026-09-21T10:00:00Z"), Updated: ts("2026-09-25T18:00:00Z"),
		MergeCommit: commit(f09, "ada@example.com", "ada"),
		Labels:      []string{"enhancement", "needs-review"},
		Reviews: []domain.Review{
			review("PRR_09a", user("bob"), domain.ReviewCommented, "2026-09-22T09:00:00Z", 16, 3),
			review("PRR_09b", user("erin"), domain.ReviewDismissed, "2026-09-23T10:00:00Z", 0, 0),
			review("PRR_09c", user("bob"), domain.ReviewApproved, "2026-09-24T15:00:00Z", 0, 0),
			review("PRR_09d", user("cara"), domain.ReviewApproved, "2026-09-25T17:00:00Z", 0, 0),
		},
		Commits: []domain.ForgeCommit{
			commit(c09a, "ada@example.com", "ada"),
			commit(c09b, "ada@other.example", "ada"),
			commit(c09c, "ada@example.com", "ada"),
		},
	}, "2026-09-25T18:00:00Z")
	pr05 := domain.PullRequest{
		ID: "PR_05", Repo: "R_widget", Number: 5, Title: "refactor: split the widget module", Author: user("erin"),
		State: domain.PullOpen, BaseBranch: "main",
		Created: ts("2026-09-29T09:00:00Z"), Updated: ts("2026-10-01T10:00:00Z"),
		Labels:  []string{"refactor"},
		Reviews: []domain.Review{review("PRR_05a", user("ada"), domain.ReviewApproved, "2026-10-01T10:00:00Z", 11, 0)},
		Commits: []domain.ForgeCommit{commit(c05a, "erin@old.example", "erin")},
	}
	pr10 := domain.PullRequest{
		ID: "PR_10", Repo: "R_widget", Number: 10, Title: "wip: widget themes", Author: user("bob"),
		State: domain.PullOpen, Draft: true, BaseBranch: "main",
		Created: ts("2026-10-01T11:30:00Z"), Updated: ts("2026-10-01T11:58:00Z"),
		Commits: []domain.ForgeCommit{commit(c10a, "bob@example.com", "bob")},
	}
	pr08 := domain.PullRequest{
		ID: "PR_08", Repo: "R_widget", Number: 8, Title: "feat: widget export", Author: user("bob"),
		State: domain.PullOpen, BaseBranch: "main",
		Created: ts("2026-10-01T11:00:00Z"), Updated: ts("2026-10-01T12:00:00Z"),
		Labels:  []string{"feature"},
		Reviews: []domain.Review{review("PRR_08a", user("ada"), domain.ReviewApproved, "2026-10-01T12:00:00Z", 0, 0)},
		Commits: []domain.ForgeCommit{commit(c08a, "bob@example.com", "bob")},
	}
	return []domain.ForgePage{
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr08, pr10, pr05}},
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr09, pr04, pr02}},
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr06, pr01, pr07, pr03}, Updated: ts("2026-10-01T12:00:01Z")},
	}
}

// secondPass is what the later pass hands put: #8 and #5 merged since the
// first pass and #10 read again inside the overlap, on a page whose #9 is
// older than the stop point; then a page with nothing that recent, which
// ends the pass and carries its start, E12's Date a week on (README.md,
// "Later pass").
func secondPass() []domain.ForgePage {
	first := firstPass()
	pr08 := first[0].PullRequests[0]
	pr08.State, pr08.Updated, pr08.Closed, pr08.Merged = domain.PullMerged,
		ts("2026-10-08T09:00:00Z"), ts("2026-10-08T09:00:00Z"), ts("2026-10-08T09:00:00Z")
	pr08.MergeCommit = commit(f08, "bob@example.com", "bob")
	pr08.Reviews = append(pr08.Reviews, review("PRR_08b", user("cara"), domain.ReviewCommented, "2026-10-07T16:00:00Z", 18, 1))
	pr05 := first[0].PullRequests[2]
	pr05.State, pr05.Updated, pr05.Closed, pr05.Merged = domain.PullMerged,
		ts("2026-10-04T15:00:00Z"), ts("2026-10-04T15:00:00Z"), ts("2026-10-04T15:00:00Z")
	pr05.MergeCommit = commit(f05, "erin@old.example", "erin")
	pr05.Reviews = append(pr05.Reviews, review("PRR_05b", user("bob"), domain.ReviewCommented, "2026-10-03T09:00:00Z", 24, 1))
	pr10 := first[0].PullRequests[1]
	return []domain.ForgePage{
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr08, pr05, pr10}},
		{Repo: "R_widget", Updated: ts("2026-10-08T13:02:10Z")},
	}
}

// holdCaller names the request, in its context, that a holdTransport and
// the Sleep beside it serve.
type holdCaller struct{}

// holdTransport is GitHub for two count requests in flight at once, a and
// b: a's first try gets a 403 with Retry-After 600 once b's is in flight
// too, b's a 403 with Retry-After 30 once a is waiting out its own, and
// every later try a count. Sleep records each caller's waits.
type holdTransport struct {
	bSent, aWaits chan struct{}
	onceB, onceA  sync.Once
	mu            sync.Mutex
	tries         map[string]int
	slept         func(caller string, d time.Duration)
	waitsBy       map[string][]time.Duration
}

func newHoldTransport() *holdTransport {
	h := &holdTransport{bSent: make(chan struct{}), aWaits: make(chan struct{}), tries: map[string]int{},
		waitsBy: map[string][]time.Duration{}}
	h.slept = func(caller string, d time.Duration) {
		h.mu.Lock()
		h.waitsBy[caller] = append(h.waitsBy[caller], d)
		h.mu.Unlock()
		if caller == "a" {
			h.onceA.Do(func() { close(h.aWaits) })
		}
	}
	return h
}

func (h *holdTransport) waits(caller string) []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.waitsBy[caller])
}

func (h *holdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	caller, _ := req.Context().Value(holdCaller{}).(string)
	h.mu.Lock()
	h.tries[caller]++
	try := h.tries[caller]
	h.mu.Unlock()
	status, body, header := http.StatusOK,
		`{"data": {"rateLimit": {"cost": 1, "remaining": 4999, "resetAt": "2026-10-09T13:00:00Z"}, "search": {"issueCount": 1}}}`,
		http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	if try == 1 {
		status, body = http.StatusForbidden, `{"message": "You have exceeded a secondary rate limit."}`
		switch caller {
		case "a":
			<-h.bSent
			header.Set("Retry-After", "600")
		case "b":
			h.onceB.Do(func() { close(h.bSent) })
			<-h.aWaits
			header.Set("Retry-After", "30")
		}
	}
	return &http.Response{
		Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), StatusCode: status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: header,
		Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: req,
	}, nil
}

// gitServer serves bare repositories over git's smart HTTP protocol (git
// http-backend behind net/http/cgi) and, like GitHub, refuses any request
// that does not carry the token as a Basic password.
type gitServer struct {
	url        string
	widget     string // the upstream bare repository acme/widget.git
	authorized atomic.Int64
	refused    atomic.Int64
}

func newGitServer(t *testing.T, token string) *gitServer {
	t.Helper()
	root := t.TempDir()
	s := &gitServer{widget: filepath.Join(root, "acme", "widget.git")}
	gitTest(t, "", "clone", "--quiet", "--bare", fixture.Main(t), s.widget)
	// GitHub advertises a ref per pull request; a mirror must not fetch it.
	s.git(t, "update-ref", "refs/pull/1/head", "refs/heads/main")

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"HOME=" + t.TempDir(),
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != token {
			s.refused.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		s.authorized.Add(1)
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// git runs git in the upstream widget repository.
func (s *gitServer) git(t *testing.T, args ...string) {
	t.Helper()
	gitTest(t, s.widget, args...)
}

// rename renames the upstream widget repository, as GitHub does when its
// owner renames it: the server serves it under the new name only.
func (s *gitServer) rename(t *testing.T, name string) {
	t.Helper()
	to := filepath.Join(filepath.Dir(s.widget), name+".git")
	if err := os.Rename(s.widget, to); err != nil {
		t.Fatal(err)
	}
	s.widget = to
}

// redirectServer is a git URL that answers every request with a redirect to
// another host (localhost, where the git URL is 127.0.0.1), and counts the
// requests that reach that host. Before git was told to follow no redirect,
// the POST after a redirected info/refs reached the other host with the
// token's header.
type redirectServer struct {
	url        string
	redirected atomic.Int64 // requests the git URL redirected
	elsewhere  atomic.Int64 // requests that reached the other host
}

func newRedirectServer(t *testing.T) *redirectServer {
	t.Helper()
	s := &redirectServer{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.elsewhere.Add(1)
		http.Error(w, "not here", http.StatusNotFound)
	}))
	t.Cleanup(other.Close)
	elsewhere := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.redirected.Add(1)
		http.Redirect(w, r, elsewhere+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// mirrorsIn lists the mirrors in an owner's directory: the entries whose
// name ends in .git, sorted, or nil.
func mirrorsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".git") {
			out = append(out, e.Name())
		}
	}
	return out
}

// gitTest runs git for the test's own setup, without the machine's global
// and system config (whose url.insteadOf rules could rewrite a URL) and
// without any inherited GIT_ variable.
func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(t.Context(), "git", args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// cassette replays testdata/github/exchanges.json: each API request must
// be the next recorded one (operation and variables), and gets its recorded
// status, headers and body. It also checks that the token reaches the API
// in the Authorization header, the only place it may go.
type cassette struct {
	t     *testing.T
	dir   string
	mu    sync.Mutex
	next  int
	steps []exchange
}

type exchange struct {
	Name      string            `json:"name"`
	Operation string            `json:"operation"`
	Variables map[string]any    `json:"variables"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"` // a file beside exchanges.json
	Text      string            `json:"text"` // or the body itself
}

var operationName = regexp.MustCompile(`^\s*query\s+(\w+)`)

func loadCassette(t *testing.T, path string) *cassette {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &cassette{t: t, dir: filepath.Dir(path)}
	if err := json.Unmarshal(b, &c.steps); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

func (c *cassette) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if got, want := req.URL.String(), "https://api.github.com/graphql"; req.Method != http.MethodPost || got != want {
		return nil, fmt.Errorf("cassette: %s %s, want POST %s", req.Method, got, want)
	}
	if got := req.Header.Get("Authorization"); got != "token "+plantedToken {
		return nil, fmt.Errorf("cassette: the request does not carry the planted token")
	}
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("cassette: request body: %w", err)
	}
	m := operationName.FindStringSubmatch(body.Query)
	if m == nil {
		return nil, fmt.Errorf("cassette: no operation name in %q", body.Query)
	}
	if c.next >= len(c.steps) {
		c.t.Errorf("cassette: unexpected request %s %v after the last recorded exchange", m[1], body.Variables)
		return nil, fmt.Errorf("cassette: no exchange left")
	}
	step := c.steps[c.next]
	c.next++
	if m[1] != step.Operation || !reflect.DeepEqual(body.Variables, step.Variables) {
		c.t.Errorf("cassette: %s: got %s %v, want %s %v", step.Name, m[1], body.Variables, step.Operation, step.Variables)
		return nil, fmt.Errorf("cassette: request does not match %q", step.Name)
	}
	payload := []byte(step.Text)
	if step.Body != "" {
		var err error
		if payload, err = os.ReadFile(filepath.Join(c.dir, step.Body)); err != nil {
			return nil, err
		}
	}
	header := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	for k, v := range step.Headers {
		header.Set(k, v)
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", step.Status, http.StatusText(step.Status)),
		StatusCode:    step.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
		Request:       req,
	}, nil
}

// done fails the test unless every recorded exchange was requested.
func (c *cassette) done(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next != len(c.steps) {
		t.Fatalf("cassette: %d of %d exchanges requested; next unrequested: %s", c.next, len(c.steps), c.steps[c.next].Name)
	}
}

// syncBuffer is a bytes.Buffer that git's and the adapter's loggers may
// write to concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func equal[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

func within(d, lo, hi time.Duration) bool { return d >= lo && d <= hi }

func noSecret(t *testing.T, where, text string, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s contains %q", where, s)
		}
	}
}
