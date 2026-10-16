package github_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
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

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/github"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/rank"
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
			return nil
		},
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

	// The first pass: oldest update first, a watermark on every page,
	// through a secondary rate limit, a nearly spent budget and a 502.
	var pages []domain.ForgePage
	collect := func(p *domain.ForgePage) error {
		pages = append(pages, *p)
		return nil
	}
	if err := src.PullRequests(ctx, widget, time.Time{}, collect); err != nil {
		t.Fatalf("PullRequests (first pass): %v", err)
	}
	first := firstPass()
	equal(t, "first pass", pages, first)

	// The second pass, from the last watermark: newest update first, down to
	// the watermark less five minutes, so #10 (two minutes older) is read
	// again and #9 stops the pass.
	since := pages[len(pages)-1].Updated
	pages = nil
	if err := src.PullRequests(ctx, widget, since, collect); err != nil {
		t.Fatalf("PullRequests (later pass): %v", err)
	}
	second := secondPass()
	equal(t, "later pass", pages, second)
	api.done(t)

	// The waits, all measured on GitHub's clock (the Date header): the reset
	// a 200 announced in its headers, Retry-After, the reset the budget
	// reported, a backoff after the 502, and at least a minute after a
	// secondary limit in a 200.
	if len(waits) != 5 ||
		!within(waits[0], 10*time.Minute, 10*time.Minute+2*time.Second) ||
		!within(waits[1], 60*time.Second, 61*time.Second) ||
		!within(waits[2], 10*time.Minute, 10*time.Minute+2*time.Second) ||
		!within(waits[3], time.Second, 2*time.Second) ||
		!within(waits[4], time.Minute, 2*time.Minute) {
		t.Fatalf("waits = %v; want [10m, 10m2s], [60s, 61s), [10m, 10m2s], [1s, 2s], [1m, 2m)", waits)
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
	// each repository's watermark at its last page's.
	latest := map[string]domain.PullRequest{}
	marks := map[string]time.Time{}
	for _, p := range pages {
		for _, pr := range p.PullRequests {
			latest[pr.ID] = pr
		}
		marks[p.Repo] = p.Updated
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
	// "renovate" makes a bot (identity.Directory.Bot): it has values and,
	// as every bot since #75, the Copilot app included, its clusters, which
	// the ranking use case leaves out (app.rankAll) and explain lists. Each block is the week, counted back from Now, of the
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
			// The bots' (#75): PRR_02d, on 2026-09-15, in week block 3, and
			// the Copilot app's review of PR_04, in Bob's block.
			subject("@renovate-approver"):                  {cluster("PR_02", 3)},
			subject("@copilot-pull-request-reviewer[bot]"): {cluster("PR_04", 2)},
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
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr03, pr07, pr01}, Updated: ts("2026-08-10T12:00:00Z")},
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr06, pr02, pr04}, Updated: ts("2026-09-20T11:00:00Z")},
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr09, pr05, pr10, pr08}, Updated: ts("2026-10-01T12:00:00Z")},
	}
}

// secondPass is what the later pass hands put: #8 and #5 merged since the
// first pass, #10 read again inside the overlap, and the watermark raised
// to #8's update (README.md, "Later pass").
func secondPass() []domain.ForgePage {
	first := firstPass()
	pr08 := first[2].PullRequests[3]
	pr08.State, pr08.Updated, pr08.Closed, pr08.Merged = domain.PullMerged,
		ts("2026-10-08T09:00:00Z"), ts("2026-10-08T09:00:00Z"), ts("2026-10-08T09:00:00Z")
	pr08.MergeCommit = commit(f08, "bob@example.com", "bob")
	pr08.Reviews = append(pr08.Reviews, review("PRR_08b", user("cara"), domain.ReviewCommented, "2026-10-07T16:00:00Z", 18, 1))
	pr05 := first[2].PullRequests[1]
	pr05.State, pr05.Updated, pr05.Closed, pr05.Merged = domain.PullMerged,
		ts("2026-10-04T15:00:00Z"), ts("2026-10-04T15:00:00Z"), ts("2026-10-04T15:00:00Z")
	pr05.MergeCommit = commit(f05, "erin@old.example", "erin")
	pr05.Reviews = append(pr05.Reviews, review("PRR_05b", user("bob"), domain.ReviewCommented, "2026-10-03T09:00:00Z", 24, 1))
	pr10 := first[2].PullRequests[2]
	return []domain.ForgePage{
		{Repo: "R_widget", PullRequests: []domain.PullRequest{pr08, pr05, pr10}, Updated: ts("2026-10-08T09:00:00Z")},
	}
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
