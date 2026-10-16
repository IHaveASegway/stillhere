package app_test

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// This file is TestChangeOracles' review cases (#38): testdata/fixture/SPEC.md,
// "Reviews in knowledge_loss@1 and who_knows@1 (main fixture)". The main
// fixture gets an origin and a replica written by hand, as a sync stores
// one, and every expected value is a literal from that section. Its
// top-level names carry the prefix review, apart from the harness's.

// The forge repositories the origins of the main fixture and of the
// merge-methods guard name, and when their sync started: the as-of time the
// runs read.
var (
	reviewRepo = domain.ForgeRepo{ID: "R_fixture", Key: "https://git.example.test/acme/fixture.git",
		Owner: "acme", Name: "fixture", DefaultBranch: "main"}
	reviewMergesRepo = domain.ForgeRepo{ID: "R_merges", Key: "https://git.example.test/acme/merge-methods.git",
		Owner: "acme", Name: "merge-methods", DefaultBranch: "main"}
	reviewSynced = reviewAt(time.October, 6, 22)
)

// reviewAt is a SPEC time: 2026, the month, the day and the hour, UTC.
func reviewAt(month time.Month, day, hour int) time.Time {
	return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
}

// reviewOracles checks SPEC's review oracles on the main fixture: reviews
// as knowledge_loss@1's activity, who_knows@1's recent reviewers, the logins
// both resolve as reviews_given@1 does, and a stored run that a later sync
// leaves as it was; and on the merge-methods guard, how pull requests land.
func reviewOracles(t *testing.T) {
	t.Run("merge methods", func(t *testing.T) { t.Parallel(); reviewMergeMethods(t) })
	t.Run("activity off the default branch", func(t *testing.T) { t.Parallel(); reviewActivity(t) })
	repo := fixture.Main(t)
	setOrigin(t, repo, reviewRepo.Key)
	commits := reviewCommits(t, repo)
	s := scan(t, fixture.Mailmap(t), nil, scanRanked(repo), func(st *sqlite.Store) {
		reviewSeed(t, st, reviewRepo, reviewPulls(commits), reviewAt(time.October, 6, 16))
	})
	r := onlyRepo(t, s.model)
	if r.Key != reviewRepo.Key {
		t.Fatalf("the fixture's key is %s, want its origin %s", r.Key, reviewRepo.Key)
	}
	const table = "SPEC reviews in knowledge_loss@1 and who_knows@1"

	t.Run("knowledge_loss@1", func(t *testing.T) {
		// Gina's comment on the open #5 makes her active; Hank's only review
		// is before the window.
		checkShare(t, table+", knowledge_loss@1", r.Card.KnowledgeLoss, 2, 34)
		e := s.explain(t, domain.MetricID{Name: "knowledge_loss", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
		if v, ok := repoValue(t, table+", knowledge_loss@1", e, r.Key); ok {
			if v.Num != 2 || v.Den != 34 || v.Detail["people"] != 8 || v.Detail["people_inactive"] != 1 || !sameInterval(v.Window, activity) {
				t.Errorf("%s: knowledge_loss@1 is %s, want 2 / 34 over [2026-04-10, 2026-10-07), people 8, people_inactive 1",
					table, describe(v))
			}
		}
		for _, c := range r.Card.Caveats {
			if c.Code == "forge-not-synced" {
				t.Errorf("%s: the card says %q, although its one repository has GitHub data", table, c.Message)
			}
		}
	})

	t.Run("who_knows@1", func(t *testing.T) {
		// The owners, top authors and landers are those without reviews.
		for _, c := range []struct {
			whoKnowsCase
			reviewers map[string]int
		}{
			{whoKnowsCase{path: "service.py", lines: 8, owners: []string{"ada@example.com"},
				top: []knows{{"ada@example.com", 6}, {"bob@example.com", 2}}, landers: []string{"bob@example.com"}},
				map[string]int{"cara@example.com": 1, "@zoe": 1}},
			{whoKnowsCase{path: "side.py", lines: 2, owners: []string{"cara@example.com"},
				top: []knows{{"cara@example.com", 2}}, landers: []string{"cara@example.com"}},
				map[string]int{"bob@example.com": 1}},
			{whoKnowsCase{path: "split.py", lines: 4, owners: []string{"gina@example.com", "hank@example.com"},
				top: []knows{{"gina@example.com", 2}, {"hank@example.com", 2}}},
				map[string]int{}},
		} {
			checkWhoKnows(t, table+", who_knows@1", s, r.Key, c.whoKnowsCase)
			reviewCheckReviewers(t, table+", who_knows@1, "+c.path, s.whoKnows(t, c.path), r.Key, c.reviewers)
		}
		// #6, a squash of two commits, lands as row 12 alone: row 11 before
		// it was committed an hour earlier.
		reviewCheckReviewers(t, table+", who_knows@1, new_name.py", s.whoKnows(t, "new_name.py"), r.Key, map[string]int{"@zoe": 1})
		reviewCheckReviewers(t, table+", who_knows@1, kept.py", s.whoKnows(t, "kept.py"), r.Key, map[string]int{})
	})

	t.Run("reviews_given@1", func(t *testing.T) {
		// The same logins name the same subjects: the three metrics read
		// one rule (identity.Directory.Reviewer).
		values, _, err := s.store.LoadMetrics(t.Context(), s.run)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]float64{}
		for _, v := range values {
			if v.Metric == reviewsGiven && v.Scope.Kind == domain.ScopeAll {
				got[v.Subject.Key] = v.Value
			}
		}
		sameRows(t, table+", reviews_given@1 in scope all", got, map[string]float64{
			"bob@example.com": 2, "cara@example.com": 1, "@zoe": 2, "@copilot-pull-request-reviewer[bot]": 2,
		})
		// The page names her as GitHub last showed her login: #6, updated
		// last, says Zoe.
		if w := s.whoKnows(t, "new_name.py"); len(w.Repos) != 1 || len(w.Repos[0].RecentReviewers) != 1 ||
			w.Repos[0].RecentReviewers[0].Name != "@Zoe" {
			t.Errorf("%s: new_name.py's reviewers %+v, want @zoe named @Zoe", table, w.Repos)
		}
	})

	t.Run("review_coverage@1", func(t *testing.T) {
		// Of rows 8 to 16, Copilot's row 10 is a bot's; #3, #4 and #6 had a
		// review by someone else before they merged, #7 none, and rows 11,
		// 13, 14 and 16 landed with no pull request.
		want := reviewCoverageRow{Num: 3, Den: 8, Via: 4, Direct: 4, ApprovedOnly: 2, Bots: 1, Repos: 1}
		reviewCheckCoverage(t, table+", review_coverage@1", s, map[string]reviewCoverageRow{
			"repo:" + r.Key: want,
		}, map[string]reviewCoverageRow{"all:": want})
	})

	t.Run("validate scores each metric at its own grain", func(t *testing.T) { reviewValidate(t, s, commits) })
	t.Run("label samples the changes the metrics score", func(t *testing.T) { reviewLabel(t, s, commits) })

	t.Run("a later sync changes nothing in the stored run", func(t *testing.T) {
		ctx := t.Context()
		later := reviewPulls(commits)[2] // #3, again, with hank's review of 10-03
		later.Updated = reviewAt(time.October, 12, 9)
		later.Reviews = append(later.Reviews, domain.Review{ID: "PRR_3b", Author: domain.ForgeUser{Login: "hank"},
			State: domain.ReviewCommented, Submitted: reviewAt(time.October, 3, 10)})
		page := domain.ForgePage{Repo: reviewRepo.ID, PullRequests: []domain.PullRequest{later}, Updated: later.Updated}
		if err := s.store.PutForgePage(ctx, &page); err != nil {
			t.Fatal(err)
		}
		if err := s.store.MarkForgeSynced(ctx, reviewRepo.ID, reviewAt(time.October, 12, 10)); err != nil {
			t.Fatal(err)
		}
		// The replica holds the new review, so a reader of it would move.
		replica, err := s.store.LoadForge(ctx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.IndexFunc(replica.PullRequests, func(p domain.PullRequest) bool { return p.ID == later.ID }); i < 0 ||
			len(replica.PullRequests[i].Reviews) != 2 {
			t.Fatalf("%s: the later sync did not store #3's second review, so this case checks nothing", table)
		}

		w := s.whoKnows(t, "side.py")
		reviewCheckReviewers(t, table+", who-knows side.py after a later sync", w, r.Key, map[string]int{"bob@example.com": 1})
		if f := w.Provenance.Forge; f == nil || f.AsOf == nil || !f.AsOf.Equal(reviewSynced) ||
			!slices.Contains(f.Metrics, "who_knows@1") {
			t.Errorf("%s: who-knows side.py's provenance.forge is %+v after a later sync, want as_of %s dating who_knows@1",
				table, f, rfc3339(reviewSynced))
		}
		m, err := app.Report(ctx, s.read, app.ReportRequest{RunID: s.run})
		if err != nil {
			t.Fatalf("app.Report: %v", err)
		}
		checkShare(t, table+", knowledge_loss@1 after a later sync", onlyRepo(t, m).Card.KnowledgeLoss, 2, 34)
	})

	t.Run("a run stored before logins were folded", func(t *testing.T) {
		// A stillhere before #69 kept a login's case in the keys it stored,
		// so had both pull requests said Zoe, the run would hold @Zoe. Its
		// readers fold the keys where they read the run, so @zoe finds her
		// 2 reviews in it, as in a run stored since.
		ctx := t.Context()
		raw, err := sql.Open("sqlite", s.db+"?_busy_timeout=10000")
		if err != nil {
			t.Fatal(err)
		}
		changed := int64(0)
		for _, rows := range []string{"metric_values", "rank_intervals"} {
			res, err := raw.ExecContext(ctx, `UPDATE `+rows+` SET subject_key = '@Zoe' WHERE subject_key = '@zoe'`)
			if err != nil {
				t.Fatal(err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				t.Fatal(err)
			}
			changed += n
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if changed == 0 {
			t.Fatalf("%s: the run holds no @zoe to store as @Zoe, so this case checks nothing", table)
		}
		e := s.explain(t, reviewsGiven, person("@zoe"))
		if i := slices.IndexFunc(e.Values, func(v report.Value) bool { return v.Scope.Kind == domain.ScopeAll }); i < 0 ||
			e.Values[i].Value != 2 {
			t.Errorf("%s: explain(reviews_given@1, @zoe) of a run that stored @Zoe gives %+v, want 2 in scope all", table, e.Values)
		}
	})
}

// reviewMergeMethods checks SPEC's merge-methods guard: a rebase merge lands
// as the first-parent commits that share its committer second, and a squash
// of several commits does not take, in the same second, the landing of
// another pull request.
func reviewMergeMethods(t *testing.T) {
	repo := fixture.MergeMethods(t)
	setOrigin(t, repo, reviewMergesRepo.Key)
	shas := reviewSubjects(t, repo)
	s := scan(t, "", nil, scanRanked(repo), func(st *sqlite.Store) {
		reviewSeed(t, st, reviewMergesRepo, reviewMergesPulls(shas), reviewAt(time.October, 3, 12))
	})
	r := onlyRepo(t, s.model)
	for _, c := range []struct {
		path string
		want map[string]int
	}{
		{"base.py", map[string]int{}},
		{"one.py", map[string]int{"@sam": 1}},
		{"two.py", map[string]int{"@sam": 1}},
		{"a.py", map[string]int{"@rita": 1}},
		{"b.py", map[string]int{"@tess": 1}},
		{"c.py", map[string]int{"rob@example.com": 1}},
	} {
		reviewCheckReviewers(t, "SPEC merge methods, who_knows@1, "+c.path, s.whoKnows(t, c.path), r.Key, c.want)
	}
	// review_coverage@1: B0 landed with no pull request; R1 and R2 are #1's,
	// S1 #2's and S2 #3's, each approved before it merged; A1 is #4's,
	// which the Copilot coding agent opened and wrote for Rob, so it is
	// Rob's landing, and his approval is his own.
	want := reviewCoverageRow{Num: 4, Den: 6, Via: 5, Direct: 1, ApprovedOnly: 4, Repos: 1}
	reviewCheckCoverage(t, "SPEC merge methods, review_coverage@1", s,
		map[string]reviewCoverageRow{"repo:" + r.Key: want}, map[string]reviewCoverageRow{"all:": want})

	// A replica with a hole (#101): it lacks #3, whose squash, S2, names it.
	// S2 is left out, unmatched, rather than counted as pushed directly.
	pulls := reviewMergesPulls(shas)
	holed := scan(t, "", nil, scanRanked(repo), func(st *sqlite.Store) {
		reviewSeed(t, st, reviewMergesRepo, slices.Delete(slices.Clone(pulls), 2, 3), reviewAt(time.October, 3, 12))
	})
	want = reviewCoverageRow{Num: 3, Den: 5, Via: 4, Direct: 1, ApprovedOnly: 3, Unmatched: 1, Repos: 1}
	reviewCheckCoverage(t, "SPEC merge methods, review_coverage@1 with #3 missing", holed,
		map[string]reviewCoverageRow{"repo:" + r.Key: want}, map[string]reviewCoverageRow{"all:": want})

	// A clone fresher than the last sync: synced 10-01 18:00, before S1,
	// S2 and A1 landed, with #1 alone. Their pull requests may not be
	// synced yet, so they are left out, after the sync.
	stale := scan(t, "", nil, scanRanked(repo), func(st *sqlite.Store) {
		reviewSeedAt(t, st, reviewMergesRepo, pulls[:1], reviewAt(time.October, 1, 12), reviewAt(time.October, 1, 18))
	})
	want = reviewCoverageRow{Num: 2, Den: 3, Via: 2, Direct: 1, ApprovedOnly: 2, AfterSync: 3, Repos: 1}
	reviewCheckCoverage(t, "SPEC merge methods, review_coverage@1 synced before S1", stale,
		map[string]reviewCoverageRow{"repo:" + r.Key: want}, map[string]reviewCoverageRow{"all:": want})
}

// reviewCoverageRow is one review_coverage@1 value: Num and Den, and Detail
// via_pull_request, direct, approved_no_comment, bot_landings, after_sync,
// unmatched and repos.
type reviewCoverageRow struct{ Num, Den, Via, Direct, ApprovedOnly, Bots, AfterSync, Unmatched, Repos int }

// reviewCheckCoverage compares the run's review_coverage@1 values in its
// one repository's scope and in scope all with SPEC's.
func reviewCheckCoverage(t *testing.T, oracle string, s *scanned, inRepo, inAll map[string]reviewCoverageRow) {
	t.Helper()
	id := domain.MetricID{Name: "review_coverage", Version: 1}
	rows := func(scope domain.Scope) map[string]reviewCoverageRow {
		got := map[string]reviewCoverageRow{}
		for key, v := range s.stored(t, id, scope) {
			if v.Detail["reviewed"] != v.Num {
				t.Errorf("%s: %s's Detail reviewed is %v, Num %v: want them equal", oracle, key, v.Detail["reviewed"], v.Num)
			}
			got[key] = reviewCoverageRow{int(v.Num), int(v.Den), int(v.Detail["via_pull_request"]), int(v.Detail["direct"]),
				int(v.Detail["approved_no_comment"]), int(v.Detail["bot_landings"]), int(v.Detail["after_sync"]),
				int(v.Detail["unmatched"]), int(v.Detail["repos"])}
		}
		return got
	}
	r := onlyRepo(t, s.model)
	sameRows(t, oracle+" in the repository", rows(domain.Scope{Kind: domain.ScopeRepo, Key: r.Key}), inRepo)
	sameRows(t, oracle+" in the run", rows(domain.Scope{Kind: domain.ScopeAll}), inAll)
	// The report reads the same values onto the repository's card and the
	// run's overview (#107).
	shown := func(c *report.ReviewCoverage) reviewCoverageRow {
		if c == nil {
			return reviewCoverageRow{}
		}
		return reviewCoverageRow{int(c.Num), int(c.Den), c.ViaPullRequest, c.Direct, c.ApprovedNoComment, c.BotLandings,
			c.AfterSync, c.Unmatched, c.Repos}
	}
	if got, want := shown(r.Card.ReviewCoverage), inRepo["repo:"+r.Key]; got != want {
		t.Errorf("%s: the card's review coverage %+v, want %+v", oracle, got, want)
	}
	if len(s.model.Overviews) != 1 || shown(s.model.Overviews[0].ReviewCoverage) != inAll["all:"] {
		t.Errorf("%s: the overview of every repository does not show the run's review coverage %+v", oracle, inAll["all:"])
	}
}

// reviewCheckReviewers compares an answer's recent reviewers in repository
// key, and each one's pull requests, with SPEC's.
func reviewCheckReviewers(t *testing.T, oracle string, w *report.WhoKnows, key string, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, r := range w.Repos {
		if r.Key != key {
			continue
		}
		for _, k := range r.RecentReviewers {
			got[k.Email] = k.RecentReviews
		}
	}
	sameRows(t, oracle+": recent reviewers and their pull requests", got, want)
}

// reviewCommits maps the committer time of each of the fixture's commits,
// as SPEC's rows give it, to its SHA, read with git through the runner the
// command line uses: a replica names commits by SHA, and SPEC's short IDs
// are for debugging.
func reviewCommits(t *testing.T, repo string) map[string]string {
	t.Helper()
	shas := map[string]string{}
	for sha, unix := range reviewLog(t, repo, "%ct") {
		sec, err := strconv.ParseInt(unix, 10, 64)
		if err != nil {
			t.Fatalf("git log printed %q for %s", unix, sha)
		}
		shas[rfc3339(time.Unix(sec, 0))] = sha
	}
	return shas
}

// reviewSubjects maps the subject of each of the merge-methods guard's
// commits, as SPEC's rows give it, to its SHA: its commits share committer
// times, so its rows are told apart by what they add.
func reviewSubjects(t *testing.T, repo string) map[string]string {
	t.Helper()
	shas := map[string]string{}
	for sha, subject := range reviewLog(t, repo, "%s") {
		shas[subject] = sha
	}
	return shas
}

// reviewLog returns, for every commit of repo, its SHA and what format (a
// git log placeholder) prints for it, read with git through the runner the
// command line uses.
func reviewLog(t *testing.T, repo, format string) map[string]string {
	t.Helper()
	git, err := gitcli.New(t.Context(), gitcli.Config{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := git.Run(t.Context(), domain.GitCmd{Dir: repo, Args: []string{"log", "--all", "--format=%H " + format}})
	if err != nil {
		t.Fatal(err)
	}
	log := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sha, value, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("git log printed %q", line)
		}
		log[sha] = value
	}
	return log
}

// reviewPulls is SPEC's replica: the seven pull requests of acme/fixture,
// with their merge commits found by the rows' committer times in commits.
func reviewPulls(commits map[string]string) []domain.PullRequest {
	user := func(login string) domain.ForgeUser { return domain.ForgeUser{Login: login} }
	landed := func(at time.Time, login string) domain.ForgeCommit {
		return domain.ForgeCommit{SHA: commits[rfc3339(at)], AuthorEmail: login + "@example.com", AuthorLogin: login}
	}
	unlanded := func(sha, login string) domain.ForgeCommit { // a squashed pull request's own commit: no run holds it
		return domain.ForgeCommit{SHA: strings.Repeat(sha, 40), AuthorEmail: login + "@example.com", AuthorLogin: login}
	}
	review := func(id, login, state string, at time.Time) domain.Review {
		return domain.Review{ID: id, Author: user(login), State: state, Submitted: at}
	}
	merged := func(n int, author string, created, at time.Time, merge domain.ForgeCommit, commits []domain.ForgeCommit,
		reviews ...domain.Review) domain.PullRequest {
		return domain.PullRequest{ID: "PR_" + strconv.Itoa(n), Repo: reviewRepo.ID, Number: n, Title: "t" + strconv.Itoa(n),
			Author: user(author), State: domain.PullMerged, BaseBranch: "main", Created: created, Updated: at, Closed: at,
			Merged: at, MergeCommit: merge, Commits: commits, Reviews: reviews}
	}
	bot := domain.Review{ID: "PRR_4c", Author: domain.ForgeUser{Login: "copilot-pull-request-reviewer", Bot: true},
		State: domain.ReviewCommented, Submitted: reviewAt(time.October, 5, 9)}
	return []domain.PullRequest{
		merged(1, "gina", reviewAt(time.January, 6, 8), reviewAt(time.January, 6, 12), landed(reviewAt(time.January, 6, 12), "gina"),
			[]domain.ForgeCommit{unlanded("1", "gina")},
			review("PRR_1a", "hank", domain.ReviewApproved, reviewAt(time.January, 6, 10))),
		merged(2, "hank", reviewAt(time.January, 7, 8), reviewAt(time.January, 7, 12), landed(reviewAt(time.January, 7, 12), "hank"),
			[]domain.ForgeCommit{unlanded("2", "hank")}),
		merged(3, "cara", reviewAt(time.September, 25, 9), reviewAt(time.October, 6, 12), landed(reviewAt(time.October, 6, 12), "bob"),
			[]domain.ForgeCommit{landed(reviewAt(time.February, 1, 12), "cara"), landed(reviewAt(time.October, 5, 12), "cara")},
			review("PRR_3a", "bob", domain.ReviewApproved, reviewAt(time.October, 2, 10))),
		merged(4, "bob", reviewAt(time.June, 1, 9), reviewAt(time.October, 6, 13), landed(reviewAt(time.October, 6, 13), "bob"),
			[]domain.ForgeCommit{unlanded("3", "bob"), unlanded("4", "bob")},
			review("PRR_4a", "frank", domain.ReviewCommented, reviewAt(time.June, 15, 10)),
			review("PRR_4b", "zoe", domain.ReviewCommented, reviewAt(time.September, 30, 10)),
			bot,
			review("PRR_4d", "bob", domain.ReviewCommented, reviewAt(time.October, 5, 10)),
			review("PRR_4e", "cara", domain.ReviewApproved, reviewAt(time.October, 5, 11))),
		{ID: "PR_5", Repo: reviewRepo.ID, Number: 5, Title: "t5", Author: user("gina"), State: domain.PullOpen,
			BaseBranch: "main", Created: reviewAt(time.May, 1, 9), Updated: reviewAt(time.May, 20, 10),
			Commits: []domain.ForgeCommit{unlanded("5", "gina")},
			Reviews: []domain.Review{review("PRR_5a", "gina", domain.ReviewCommented, reviewAt(time.May, 20, 10))}},
		// zoe's login in another case, as a sync after she changed its case
		// would copy it: one account to GitHub, so one subject, @zoe (#69).
		merged(6, "erin", reviewAt(time.October, 4, 9), reviewAt(time.October, 6, 16), landed(reviewAt(time.October, 6, 16), "erin"),
			[]domain.ForgeCommit{unlanded("6", "erin"), unlanded("7", "erin")},
			review("PRR_6a", "Zoe", domain.ReviewApproved, reviewAt(time.October, 5, 8))),
		// Wave 10: hank's own pull request, opened before the activity
		// window and updated in it by someone else's comment: an update is
		// no activity of its author's, so Hank stays quiet.
		{ID: "PR_8", Repo: reviewRepo.ID, Number: 8, Title: "t8", Author: user("hank"), State: domain.PullOpen,
			BaseBranch: "main", Created: reviewAt(time.March, 1, 9), Updated: reviewAt(time.September, 15, 10)},
		// #107: reviewed only by its author, a bot and, after its merge,
		// bob, so review_coverage@1 counts it unreviewed.
		merged(7, "ivy", reviewAt(time.October, 5, 9), reviewAt(time.October, 6, 19), landed(reviewAt(time.October, 6, 19), "ivy"),
			[]domain.ForgeCommit{unlanded("8", "ivy")},
			domain.Review{ID: "PRR_7a", Author: domain.ForgeUser{Login: "copilot-pull-request-reviewer", Bot: true},
				State: domain.ReviewApproved, Submitted: reviewAt(time.October, 6, 17)},
			review("PRR_7b", "ivy", domain.ReviewCommented, reviewAt(time.October, 6, 18)),
			review("PRR_7c", "bob", domain.ReviewApproved, reviewAt(time.October, 6, 20))),
	}
}

// reviewMergesPulls is SPEC's replica of the merge-methods guard: the three
// pull requests of acme/merge-methods, with their merge commits found by
// subject in shas.
func reviewMergesPulls(shas map[string]string) []domain.PullRequest {
	user := func(login string) domain.ForgeUser { return domain.ForgeUser{Login: login} }
	commit := func(sha, login string) domain.ForgeCommit {
		return domain.ForgeCommit{SHA: sha, AuthorEmail: login + "@example.com", AuthorLogin: login}
	}
	unlanded := func(c byte, login string) domain.ForgeCommit { // written before the merge: no run holds it
		return commit(strings.Repeat(string(c), 40), login)
	}
	merged := func(n int, author string, at time.Time, merge string, commits []domain.ForgeCommit, reviewer string,
		submitted time.Time) domain.PullRequest {
		id := "PR_m" + strconv.Itoa(n)
		return domain.PullRequest{ID: id, Repo: reviewMergesRepo.ID, Number: n, Title: "m" + strconv.Itoa(n), Author: user(author),
			State: domain.PullMerged, BaseBranch: "main", Created: at.Add(-72 * time.Hour), Updated: at, Closed: at, Merged: at,
			MergeCommit: commit(shas[merge], author), Commits: commits,
			Reviews: []domain.Review{{ID: id + "a", Author: user(reviewer), State: domain.ReviewApproved, Submitted: submitted}}}
	}
	// #4: the Copilot coding agent's, an app account, approved only by
	// rob, the person it worked for, whose commit its co-author trailer
	// credits.
	agent := merged(4, "copilot-swe-agent", reviewAt(time.October, 3, 12), "feat: add c.py (#4)",
		[]domain.ForgeCommit{unlanded('d', "Copilot")}, "rob", reviewAt(time.October, 3, 9))
	agent.Author.Bot = true
	agent.MergeCommit.AuthorLogin = "Copilot"
	return []domain.PullRequest{
		merged(1, "rob", reviewAt(time.October, 1, 12), "feat: add two.py", // R2, after R1 in the same second
			[]domain.ForgeCommit{unlanded('8', "rob"), unlanded('9', "rob")}, "sam", reviewAt(time.October, 1, 9)),
		merged(2, "ali", reviewAt(time.October, 2, 12), "feat: add a.py (#2)", // S1
			[]domain.ForgeCommit{unlanded('a', "ali")}, "rita", reviewAt(time.October, 2, 9)),
		merged(3, "bo", reviewAt(time.October, 2, 12), "feat: add b.py (#3)", // S2, after S1 in the same second
			[]domain.ForgeCommit{unlanded('b', "bo"), unlanded('c', "bo")}, "tess", reviewAt(time.October, 2, 10)),
		agent,
	}
}

// reviewSeed stores a SPEC replica as a sync would: the forge repository,
// one page of its pull requests with watermark updated, and the sync that
// started at reviewSynced.
func reviewSeed(t *testing.T, s *sqlite.Store, repo domain.ForgeRepo, pulls []domain.PullRequest, updated time.Time) {
	t.Helper()
	reviewSeedAt(t, s, repo, pulls, updated, reviewSynced)
}

// reviewSeedAt is reviewSeed with the sync that started at synced.
func reviewSeedAt(t *testing.T, s *sqlite.Store, repo domain.ForgeRepo, pulls []domain.PullRequest, updated, synced time.Time) {
	t.Helper()
	ctx := t.Context()
	page := domain.ForgePage{Repo: repo.ID, PullRequests: pulls, Updated: updated}
	if err := s.PutForgeRepos(ctx, []domain.ForgeRepo{repo}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutForgePage(ctx, &page); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkForgeSynced(ctx, repo.ID, synced); err != nil {
		t.Fatal(err)
	}
}

// reviewValidate checks SPEC's "validate over the same replica" (#77): a
// labelled change is a landing or a pull request, named by SHA or by
// number, and each ranked metric scores it at its own grain, durable@1 and
// simplifying@1 by its landings and reviews_given@1 by its pull request's
// reviewers, a landing no pull request landed unscored; a change counts
// once, whatever names it, and over two labellers by its majority label.
func reviewValidate(t *testing.T, s *scanned, commits map[string]string) {
	const table = "SPEC validate over the same replica"
	row := func(month time.Month, day, hour int) string { return commits[rfc3339(reviewAt(month, day, hour))] }
	lead := strings.Join([]string{
		"# the lead's labels; a comment",
		"change,label,labeller",
		"#3,meaningful,lead",                           // row 8, Bob's merge: reviewed by bob
		"pr:4,meaningful,lead",                         // row 9: frank, cara and @zoe
		"acme/fixture#6,routine,lead",                  // row 12: @zoe
		row(time.October, 6, 17) + ",routine,lead",     // row 13, Frank's, no pull request
		row(time.October, 6, 18)[:7] + ",routine,lead", // row 14, Ada's, by a prefix, no pull request
		row(time.October, 6, 15) + ",noise,lead",       // row 11, Dana's: noise, in no AUC
		row(time.January, 1, 12) + ",meaningful,lead",  // row 1, Ada's init: durable 7 / 7
		row(time.January, 4, 12) + ",routine,lead",     // row 3, Bob's move: durable 0 / 2
		"#1,meaningful,lead",                           // row 5 (#1, Gina's): durable 2 / 2; hank's review is before the window
		row(time.March, 1, 12) + ",routine,lead",       // row 7, Bob's: durable 5 / 5
		"#9,meaningful,lead",                           // no such pull request: unmatched
		row(time.October, 6, 20) + ",skip,lead",        // row 16: passed over, no label
		row(time.October, 6, 12) + ",meaningful,lead",  // row 8 again, by its SHA: one change, counted once
	}, "\n")
	validate := func(labels, labeller string) *report.Validation {
		t.Helper()
		v, err := app.Validate(t.Context(), s.read, app.ValidateRequest{RunID: s.run, Labels: []byte(labels), LabelsName: "labels.csv",
			Labeller: labeller})
		if err != nil {
			t.Fatalf("app.Validate: %v", err)
		}
		return v
	}
	hasCaveat := func(v *report.Validation, code, text string) bool {
		return slices.ContainsFunc(v.Caveats, func(c report.Caveat) bool { return c.Code == code && strings.Contains(c.Message, text) })
	}
	v := validate(lead, "")
	if v.Labels != 12 || v.Changes != 10 || v.Meaningful != 4 || v.Routine != 5 || v.Noise != 1 || v.Unmatched != 1 || v.Labellers != 1 {
		t.Errorf("%s: %d rows labelling %d changes (%d meaningful, %d routine, %d noise), %d unmatched, %d labellers; "+
			"want 12 rows, 10 changes (4, 5, 1), 1 and 1", table, v.Labels, v.Changes, v.Meaningful, v.Routine, v.Noise, v.Unmatched, v.Labellers)
	}
	want := map[string]struct {
		auc                 float64
		meaningful, routine int
	}{
		reviewsGiven.String(): {0.75, 2, 1}, // 1 and 3 against 1
		durable.String():      {0.75, 2, 2}, // 1, 1 against 0, 1
		simplifying.String():  {0.5, 2, 3},  // every one removes nothing
	}
	for _, a := range v.Metrics {
		w, ok := want[a.Metric]
		if !ok {
			continue
		}
		if a.AUC == nil || *a.AUC != w.auc || a.Meaningful != w.meaningful || a.Routine != w.routine || !a.Beta ||
			a.Lo == nil || a.Hi == nil || *a.Lo > *a.AUC || *a.Hi < *a.AUC {
			t.Errorf("%s: %s scores %+v (AUC %v in [%v, %v]), want AUC %v over %d meaningful and %d routine, beta, inside its interval",
				table, a.Metric, a, deref(a.AUC), deref(a.Lo), deref(a.Hi), w.auc, w.meaningful, w.routine)
		}
		delete(want, a.Metric)
	}
	for m := range want {
		t.Errorf("%s: validate scores no %s", table, m)
	}
	for _, c := range []struct{ code, text string }{
		{"unmatched-labels", "#950, pr:950 or owner/name#950"},
		{"unscored-labels", reviewsGiven.String() + " scored 3 of the 5 changes"}, // rows 13 and 14: no pull request
		{"repeated-labels", "1 row"},
	} {
		if !hasCaveat(v, c.code, c.text) {
			t.Errorf("%s: no %s caveat that says %q: %+v", table, c.code, c.text, v.Caveats)
		}
	}

	// A second labeller agrees on #3 and not on #6: one label per change, a
	// tie left out, unless --labeller asks for one labeller's labels.
	both := lead + "\n#3,meaningful,second\nacme/fixture#6,meaningful,second\n"
	v = validate(both, "")
	if v.Changes != 9 || v.Labellers != 2 || !hasCaveat(v, "several-labellers", "agreed on 1 of them; 1 change with no majority") {
		t.Errorf("%s: two labellers give %d changes from %d labellers, caveats %+v; want 9 from 2, #6's tie left out",
			table, v.Changes, v.Labellers, v.Caveats)
	}
	v = validate(both, "lead")
	if i := slices.IndexFunc(v.Metrics, func(a report.MetricAUC) bool { return a.Metric == reviewsGiven.String() }); i < 0 ||
		v.Metrics[i].N != 3 || v.Changes != 10 || !hasCaveat(v, "several-labellers", "the 2 rows") {
		t.Errorf("%s: --labeller lead gives %+v, caveats %+v; want the lead's 10 changes, reviews_given@1 n 3, the second's 2 rows left out",
			table, v.Metrics, v.Caveats)
	}

	// A "#" and digits name a pull request; every other "#" line is a
	// comment, and a file of nothing else says so (KB-19).
	_, err := app.Validate(t.Context(), s.read, app.ValidateRequest{RunID: s.run, Labels: []byte("# 3,meaningful\n#pr 4,routine\n"),
		LabelsName: "labels.csv"})
	var usage *domain.UsageError
	if !errors.As(err, &usage) || !strings.Contains(usage.Msg, "read as comments: 2") {
		t.Errorf("%s: a labels file of comments gives %v, want a usage error that counts its 2 comment lines", table, err)
	}
}

// reviewLabel checks SPEC's "label over the same replica": the sample is
// drawn from the 14 changes a ranked metric scores, people's only (row 10,
// the bot's, is none), the windows take turns, a larger sample extends a
// smaller one, and a change the labels file holds is marked labelled.
func reviewLabel(t *testing.T, s *scanned, commits map[string]string) {
	const table = "SPEC label over the same replica"
	sample := func(n int, labels string) *report.LabelSample {
		t.Helper()
		ls, err := app.Label(t.Context(), s.read, app.LabelRequest{RunID: s.run, N: n, Seed: 1, Labels: []byte(labels)})
		if err != nil {
			t.Fatalf("app.Label(%d): %v", n, err)
		}
		return ls
	}
	bot := commits[rfc3339(reviewAt(time.October, 6, 14))]
	merge := commits[rfc3339(reviewAt(time.October, 6, 12))]
	all := sample(20, "#3,meaningful\n")
	if all.Pool != 14 || len(all.Changes) != 14 || all.Labelled != 1 {
		t.Errorf("%s: a sample of 20 holds %d of a pool of %d, %d labelled; want all 14, 1 labelled (#3)",
			table, len(all.Changes), all.Pool, all.Labelled)
	}
	if !slices.ContainsFunc(all.Caveats, func(c report.Caveat) bool { return c.Code == "small-sample" }) {
		t.Errorf("%s: a sample of 20 from 14 has no small-sample caveat: %+v", table, all.Caveats)
	}
	older, recent := 0, 0
	for i, c := range all.Changes {
		switch {
		case c.Change == bot:
			t.Errorf("%s: the sample holds the bot's landing %s", table, shortSHA(bot))
		case c.Change == merge && (c.Pull == nil || c.Pull.Number != 3 || c.Label != "meaningful"):
			t.Errorf("%s: row 8 is %+v, want pull request #3, labelled meaningful", table, c)
		}
		if c.Landed.Before(reviewAt(time.July, 9, 0)) {
			older++
		} else {
			recent++
		}
		if i == 3 && (older != 2 || recent != 2) {
			t.Errorf("%s: the first 4 changes are %d from 90–365 days ago and %d from the last 90, want 2 and 2", table, older, recent)
		}
	}
	if older != 6 || recent != 8 {
		t.Errorf("%s: the sample holds %d changes from 90–365 days ago and %d from the last 90, want 6 and 8", table, older, recent)
	}
	// Another labeller's rows are theirs: #3 is not marked for "second".
	second, err := app.Label(t.Context(), s.read, app.LabelRequest{RunID: s.run, N: 20, Seed: 1, Labels: []byte("#3,meaningful\n"),
		Labeller: "second"})
	if err != nil {
		t.Fatalf("app.Label: %v", err)
	}
	if second.Labelled != 0 || second.OtherLabellers != 1 {
		t.Errorf("%s: for labeller second, %d sampled changes are labelled and %d other labellers named; want 0 and 1",
			table, second.Labelled, second.OtherLabellers)
	}
	four := sample(4, "")
	for i, c := range four.Changes {
		if c.Change != all.Changes[i].Change {
			t.Errorf("%s: a sample of 4 is not the start of a sample of 20: change %d is %s, not %s", table, i+1,
				shortSHA(c.Change), shortSHA(all.Changes[i].Change))
		}
	}
}

// reviewActivity checks SPEC's "Activity off the default branch (main
// fixture, wave 10)": knowledge_loss@1 counts a commit on another ref of
// the clone and a pull request its author opened or pushed their own
// commit to, through GitHub's links of the history's emails (#117), and a
// card says who goes quiet within 30 days and when each holder was last
// active.
func reviewActivity(t *testing.T) {
	const table = "SPEC activity off the default branch"
	repo := fixture.Main(t)
	setOrigin(t, repo, reviewRepo.Key)
	commits := reviewCommits(t, repo)
	row5, row6 := commits[rfc3339(reviewAt(time.January, 6, 12))], commits[rfc3339(reviewAt(time.January, 7, 12))]
	refs := reviewRefs(t, repo, []reviewRefCommit{
		{"refs/heads/release-1", "Hank Split <hank@laptop.example>", reviewAt(time.September, 20, 12), row6, "release.py"},
		{"refs/heads/old-work", "Gina Split <gina@example.com>", reviewAt(time.April, 20, 12), row5, "old_work.py"},
		{"refs/tags/later", "Gina Split <gina@example.com>", time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC), row5, "later.py"},
	})
	release := refs["refs/heads/release-1"]
	pr := domain.PullRequest{ID: "PR_8", Repo: reviewRepo.ID, Number: 8, Title: "release", Author: domain.ForgeUser{Login: "hank"},
		State: domain.PullOpen, BaseBranch: "main", Created: reviewAt(time.March, 20, 9), Updated: reviewAt(time.September, 20, 13),
		Commits: []domain.ForgeCommit{{SHA: release, AuthorEmail: "hank@laptop.example", AuthorLogin: "hank"}}}
	s := scan(t, fixture.Mailmap(t), nil, app.ScanRequest{Paths: []string{repo}, Now: fixture.Now}, func(st *sqlite.Store) {
		reviewSeed(t, st, reviewRepo, []domain.PullRequest{pr}, reviewAt(time.September, 20, 13))
		// The links pass's answer for Hank's history address (forge_links):
		// no pull request of the replica holds a commit of the run's that
		// ties the login hank to him.
		if err := st.PutForgeLinks(t.Context(), reviewRepo.ID, []domain.ForgeCommit{
			{SHA: row6, AuthorEmail: "hank@example.com", AuthorLogin: "hank"}}); err != nil {
			t.Fatal(err)
		}
	})
	r := onlyRepo(t, s.model)
	// Gina is active through her commit on old-work, Hank through his
	// pull request: nobody holding a line has gone quiet. Gina's activity is
	// 170 days old, so her 2 lines go quiet within 30 days.
	checkShare(t, table+", knowledge_loss@1", r.Card.KnowledgeLoss, 0, 34)
	if q := r.Card.KnowledgeLoss.GoingQuiet; q == nil || *q != (report.ShareGoingQuiet{People: 1, Lines: 2, Num: 2, Value: 2.0 / 34}) {
		t.Errorf("%s: going_quiet %+v, want 1 person, 2 lines, 2 / 34", table, q)
	}
	if !slices.ContainsFunc(r.Card.Caveats, func(c report.Caveat) bool {
		return c.Code == "going-quiet" && strings.Contains(c.Message, "1 person who holds 2 lines here goes quiet within 30 days")
	}) {
		t.Errorf("%s: the card's caveats %+v, want a going-quiet caveat", table, r.Card.Caveats)
	}
	type active struct {
		at    string
		kind  string
		quiet int
		going bool
	}
	got := map[string]active{}
	for _, row := range r.StillHere.Humans {
		if row.LastActive != nil && row.QuietInDays != nil {
			got[row.Email] = active{rfc3339(*row.LastActive), row.LastActivity, *row.QuietInDays, row.GoingQuiet}
		}
	}
	for email, want := range map[string]active{
		"gina@example.com": {"2026-04-20T12:00:00Z", domain.ActivityCommit, 10, true},
		"hank@example.com": {"2026-09-20T12:00:00Z", domain.ActivityPullRequest, 163, false},
	} {
		if got[email] != want {
			t.Errorf("%s: %s last active %+v, want %+v", table, email, got[email], want)
		}
	}
	// The run keeps the other refs' commits made before --now: not the tag's,
	// dated after it.
	fs, err := s.store.LoadFacts(t.Context(), s.run)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, c := range fs.Repos[0].RefCommits {
		kept = append(kept, c.SHA)
	}
	if want := []string{refs["refs/heads/old-work"], release}; !slices.Equal(kept, slices.Sorted(slices.Values(want))) {
		t.Errorf("%s: the run keeps the other refs' commits %v, want %v", table, kept, want)
	}
}

// reviewRefCommit is one commit fast-import writes on a ref of its own:
// the ref, the author and committer as git writes them, when, the commit
// it follows, and the one-line file it adds.
type reviewRefCommit struct {
	ref, author string
	at          time.Time
	from, file  string
}

// reviewRefs writes each commit on its ref with git fast-import, through
// the runner the scans use, and returns each ref's commit.
func reviewRefs(t *testing.T, repo string, commits []reviewRefCommit) map[string]string {
	t.Helper()
	git, err := gitcli.New(t.Context(), gitcli.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var stream strings.Builder
	for _, c := range commits {
		when := fmt.Sprintf("%d +0000", c.at.Unix())
		msg := "add " + c.file + "\n"
		body := "x = 1\n"
		fmt.Fprintf(&stream, "commit %s\nauthor %s %s\ncommitter %s %s\ndata %d\n%sfrom %s\nM 100644 inline %s\ndata %d\n%s\n",
			c.ref, c.author, when, c.author, when, len(msg), msg, c.from, c.file, len(body), body)
	}
	if _, err := git.Run(t.Context(), domain.GitCmd{Dir: repo, Args: []string{"fast-import", "--quiet"},
		Stdin: strings.NewReader(stream.String())}); err != nil {
		t.Fatalf("git fast-import: %v", err)
	}
	out := map[string]string{}
	for _, c := range commits {
		sha, err := git.Run(t.Context(), domain.GitCmd{Dir: repo, Args: []string{"rev-parse", c.ref}})
		if err != nil {
			t.Fatal(err)
		}
		out[c.ref] = strings.TrimSpace(string(sha))
	}
	return out
}

// deref is a pointer's value for a message, or NaN for nil.
func deref(f *float64) float64 {
	if f == nil {
		return math.NaN()
	}
	return *f
}

// shortSHA is a SHA's first 7 digits, for messages.
func shortSHA(sha string) string { return sha[:min(7, len(sha))] }
