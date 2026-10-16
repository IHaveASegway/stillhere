package app_test

import (
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/rank"
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
	reviewWholeChange(t, s, shas)

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

// reviewWholeChange checks that label shows the whole of #1, a rebase merge
// of two landings, R2 and R1 (the panel's evidence showed a rebase merge's
// newest landing alone): both commits on its card, oldest first, and both
// files in its diff, taken from R1's parent; and that Diff refuses an
// earlier landing that is not the first parent of the one before it.
func reviewWholeChange(t *testing.T, s *scanned, shas map[string]string) {
	t.Helper()
	const oracle = "SPEC merge methods, label of a rebase merge"
	ls, err := app.Label(t.Context(), s.read, app.LabelRequest{RunID: s.run, N: 10, Seed: 1})
	if err != nil {
		t.Fatalf("%s: app.Label: %v", oracle, err)
	}
	i := slices.IndexFunc(ls.Changes, func(c report.LabelChange) bool { return c.Pull != nil && c.Pull.Number == 1 })
	if i < 0 {
		t.Fatalf("%s: the sample holds no #1: %+v", oracle, ls.Changes)
	}
	c := ls.Changes[i]
	r1, r2 := shas["feat: add one.py"], shas["feat: add two.py"]
	if !slices.Equal(c.Landings, []string{r2, r1}) || !slices.Equal(c.Commits, []string{"feat: add one.py", "feat: add two.py"}) {
		t.Errorf("%s: #1 is landings %v with commits %q, want R2 and R1, and both subjects, R1's first", oracle, c.Landings, c.Commits)
	}
	d := s.read
	d.Git = writeDeps(t, "", nil).Git
	files := func(a *app.DiffAnswer) []string {
		var out []string
		for _, f := range a.Files {
			out = append(out, f.Path)
		}
		return out
	}
	whole, err := app.Diff(t.Context(), d, app.DiffRequest{RunID: s.run, Repo: c.RepoKey, Change: c.Change, Earlier: c.Landings[1:]})
	if err != nil || !slices.Equal(files(whole), []string{"one.py", "two.py"}) || whole.Base != shas["feat: add base.py"] ||
		!strings.Contains(whole.Patch, "+one_1 = 1") || !strings.Contains(whole.Patch, "+two_1 = 1") {
		t.Errorf("%s: the whole change's diff is %+v, %v; want one.py and two.py from B0", oracle, whole, err)
	}
	alone, err := app.Diff(t.Context(), d, app.DiffRequest{RunID: s.run, Repo: c.RepoKey, Change: c.Change})
	if err != nil || !slices.Equal(files(alone), []string{"two.py"}) {
		t.Errorf("%s: R2's own diff is %+v, %v; want two.py alone", oracle, alone, err)
	}
	var usage *domain.UsageError
	_, err = app.Diff(t.Context(), d, app.DiffRequest{RunID: s.run, Repo: c.RepoKey, Change: c.Change,
		Earlier: []string{shas["feat: add a.py (#2)"]}})
	if !errors.As(err, &usage) || !strings.Contains(usage.Msg, "is not the first parent of landing") {
		t.Errorf("%s: an earlier landing that is not R2's parent gives %v, want a usage error that says so", oracle, err)
	}
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
			Labeller: labeller, Scores: labeller == ""})
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
	reviewScores(t, table, v, commits)
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

// reviewScores checks validate --scores over the lead's labels: one row per
// labelled change, noise included, with each metric's measure of it or
// null, which are the very numbers each AUC and its "size alone" column are
// computed from; and that docs/metrics.md's Validity line of each ranked
// metric holds the metric's model-panel reference word for word.
func reviewScores(t *testing.T, table string, v *report.Validation, commits map[string]string) {
	t.Helper()
	row := func(month time.Month, day, hour int) string { return commits[rfc3339(reviewAt(month, day, hour))] }
	if len(v.Scores) != v.Changes {
		t.Fatalf("%s: --scores gives %d rows for %d labelled changes", table, len(v.Scores), v.Changes)
	}
	byChange := map[string]report.ChangeScore{}
	for _, c := range v.Scores {
		byChange[c.Change] = c
	}
	score := func(sha string, m domain.MetricID) *float64 { return byChange[sha].Scores[m.String()] }
	for _, c := range []struct {
		sha    string
		metric domain.MetricID
		want   *float64 // nil: not scored
	}{
		{row(time.October, 6, 13), reviewsGiven, ptr(3.0)}, // #4: frank, cara and @zoe
		{row(time.October, 6, 17), reviewsGiven, nil},      // row 13: no pull request
		{row(time.January, 1, 12), durable, ptr(1.0)},      // row 1: 7 / 7
		{row(time.January, 4, 12), durable, ptr(0.0)},      // row 3: 0 / 2
		{row(time.October, 6, 15), simplifying, ptr(0.0)},  // row 11, noise: in no AUC, scored all the same
		{row(time.January, 1, 12), reviewsGiven, nil},      // before reviews_given@1's window
	} {
		got := score(c.sha, c.metric)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: --scores gives %s of %s as %v, want %v", table, c.metric, shortSHA(c.sha), deref(got), deref(c.want))
		}
	}
	if c := byChange[row(time.October, 6, 15)]; c.Label != "noise" || c.Pull != nil || c.Added+c.Removed == 0 {
		t.Errorf("%s: --scores row 11 is %+v, want noise, no pull request, its lines", table, c)
	}
	// Size alone, derived by hand from the numstat of each labelled
	// change's landings on paths with a role, blank lines included (SPEC's
	// validate section): durable@1's meaningful rows 1 (10) and 5 (2)
	// against routine rows 3 (4) and 7 (5) win 2 of 4 pairs;
	// simplifying@1's #3 (9) and #4 (2) against #6 (1), row 13 (3) and row
	// 14 (1) win 5 of 6; reviews_given@1's #3 and #4 both beat #6 (1). Each
	// metric's n is under 50, so its verdict cannot tell yet.
	sizeAlone := map[string]float64{durable.String(): 0.5, simplifying.String(): 5.0 / 6, reviewsGiven.String(): 1}
	for _, a := range v.Metrics {
		var pos, neg, sizePos, sizeNeg []float64
		for _, c := range v.Scores {
			x := c.Scores[a.Metric]
			if x == nil || c.Label == "noise" {
				continue
			}
			if c.Label == "meaningful" {
				pos, sizePos = append(pos, *x), append(sizePos, float64(c.Added+c.Removed))
			} else {
				neg, sizeNeg = append(neg, *x), append(sizeNeg, float64(c.Added+c.Removed))
			}
		}
		if len(pos) != a.Meaningful || len(neg) != a.Routine || a.AUC == nil || *a.AUC != rank.AUC(pos, neg) ||
			a.SizeAUC == nil || *a.SizeAUC != rank.AUC(sizePos, sizeNeg) || *a.SizeLo > *a.SizeAUC || *a.SizeHi < *a.SizeAUC {
			t.Errorf("%s: %s's AUC %v and size alone %v (%v–%v) are not those of its --scores rows (%d / %d: %v and %v)", table,
				a.Metric, deref(a.AUC), deref(a.SizeAUC), deref(a.SizeLo), deref(a.SizeHi), len(pos), len(neg),
				rank.AUC(pos, neg), rank.AUC(sizePos, sizeNeg))
		}
		if want, ok := sizeAlone[a.Metric]; !ok || a.SizeAUC == nil || math.Abs(*a.SizeAUC-want) > 1e-12 {
			t.Errorf("%s: %s's size alone is %v, want %v, derived by hand", table, a.Metric, deref(a.SizeAUC), want)
		}
		if a.SizeDiffLo == nil || a.SizeDiffHi == nil || *a.SizeDiffLo > *a.AUC-*a.SizeAUC || *a.SizeDiffHi < *a.AUC-*a.SizeAUC ||
			a.Verdict != rank.VerdictTooFew {
			t.Errorf("%s: %s's paired interval of AUC less size alone is %v–%v around %v, verdict %q; want one that holds the difference, and %q",
				table, a.Metric, deref(a.SizeDiffLo), deref(a.SizeDiffHi), *a.AUC-*a.SizeAUC, a.Verdict, rank.VerdictTooFew)
		}
	}

	// One reference, the places that print it: each ranked metric's
	// validity note (report.ValidityNote of its definition), which every
	// validity caveat says, its Validity line in docs/metrics.md, and
	// report-json.md's example of a rank table's caveat, durable@1's.
	readDoc := func(path ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, path...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	doc, jsonDoc := readDoc("docs", "metrics.md"), readDoc("docs", "spec", "report-json.md")
	for _, mi := range app.Metrics().Metrics {
		if !mi.Ranked {
			continue
		}
		if mi.Reference == nil {
			t.Errorf("%s: %s has no reference result", table, mi.ID)
			continue
		}
		ref := "Reference (" + mi.Reference.Source + "): " + mi.Reference.Result + ". " + mi.Reference.About
		section, _, _ := strings.Cut(sectionAfter(doc, "\n### "+mi.ID), "\n### ")
		_, line, _ := strings.Cut(section, "- **Validity**:")
		line, _, _ = strings.Cut(line, "\n")
		if !strings.Contains(line, ref) {
			t.Errorf("%s: docs/metrics.md's Validity line of %s does not hold its definition's reference %q", table, mi.ID, ref)
		}
		if note := report.ValidityNote(mi); mi.ID == durable.String() && !strings.Contains(jsonDoc, `"message": "Validity: `+note+`"`) {
			t.Errorf("%s: report-json.md's example rank table does not carry durable@1's validity note %q", table, note)
		}
	}
}

// sectionAfter is s from the first heading, after its marker, or "".
func sectionAfter(s, marker string) string {
	if _, after, ok := strings.Cut(s, marker); ok {
		return after
	}
	return ""
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

// deref is a pointer's value for a message, or NaN for nil.
func deref(f *float64) float64 {
	if f == nil {
		return math.NaN()
	}
	return *f
}

// shortSHA is a SHA's first 7 digits, for messages.
func shortSHA(sha string) string { return sha[:min(7, len(sha))] }
