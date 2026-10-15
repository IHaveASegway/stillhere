package app_test

import (
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
			"bob@example.com": 1, "cara@example.com": 1, "@zoe": 2, "@copilot-pull-request-reviewer[bot]": 1,
		})
		// The page names her as GitHub last showed her login: #6, updated
		// last, says Zoe.
		if w := s.whoKnows(t, "new_name.py"); len(w.Repos) != 1 || len(w.Repos[0].RecentReviewers) != 1 ||
			w.Repos[0].RecentReviewers[0].Name != "@Zoe" {
			t.Errorf("%s: new_name.py's reviewers %+v, want @zoe named @Zoe", table, w.Repos)
		}
	})

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
		reviewSeed(t, st, reviewMergesRepo, reviewMergesPulls(shas), reviewAt(time.October, 2, 12))
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
	} {
		reviewCheckReviewers(t, "SPEC merge methods, who_knows@1, "+c.path, s.whoKnows(t, c.path), r.Key, c.want)
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

// reviewPulls is SPEC's replica: the six pull requests of acme/fixture,
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
	return []domain.PullRequest{
		merged(1, "rob", reviewAt(time.October, 1, 12), "feat: add two.py", // R2, after R1 in the same second
			[]domain.ForgeCommit{unlanded('8', "rob"), unlanded('9', "rob")}, "sam", reviewAt(time.October, 1, 9)),
		merged(2, "ali", reviewAt(time.October, 2, 12), "feat: add a.py", // S1
			[]domain.ForgeCommit{unlanded('a', "ali")}, "rita", reviewAt(time.October, 2, 9)),
		merged(3, "bo", reviewAt(time.October, 2, 12), "feat: add b.py", // S2, after S1 in the same second
			[]domain.ForgeCommit{unlanded('b', "bo"), unlanded('c', "bo")}, "tess", reviewAt(time.October, 2, 10)),
	}
}

// reviewSeed stores a SPEC replica as a sync would: the forge repository,
// one page of its pull requests with watermark updated, and the sync that
// started at reviewSynced.
func reviewSeed(t *testing.T, s *sqlite.Store, repo domain.ForgeRepo, pulls []domain.PullRequest, updated time.Time) {
	t.Helper()
	ctx := t.Context()
	page := domain.ForgePage{Repo: repo.ID, PullRequests: pulls, Updated: updated}
	if err := s.PutForgeRepos(ctx, []domain.ForgeRepo{repo}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutForgePage(ctx, &page); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkForgeSynced(ctx, repo.ID, reviewSynced); err != nil {
		t.Fatal(err)
	}
}
