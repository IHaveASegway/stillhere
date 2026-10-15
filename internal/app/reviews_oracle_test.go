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

// The forge repository the fixture's origin names, and when its sync
// started: the as-of time the run reads.
var (
	reviewRepo = domain.ForgeRepo{ID: "R_fixture", Key: "https://git.example.test/acme/fixture.git",
		Owner: "acme", Name: "fixture", DefaultBranch: "main"}
	reviewSynced = reviewAt(time.October, 6, 22)
)

// reviewAt is a SPEC time: 2026, the month, the day and the hour, UTC.
func reviewAt(month time.Month, day, hour int) time.Time {
	return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
}

// reviewOracles checks SPEC's review oracles on the main fixture: reviews
// as knowledge_loss@1's activity, who_knows@1's recent reviewers, the logins
// both resolve as reviews_given@1 does, and a stored run that a later sync
// leaves as it was.
func reviewOracles(t *testing.T) {
	repo := fixture.Main(t)
	setOrigin(t, repo, reviewRepo.Key)
	commits := reviewCommits(t, repo)
	s := scan(t, fixture.Mailmap(t), nil, scanRanked(repo), func(st *sqlite.Store) { reviewSeed(t, st, commits) })
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
	git, err := gitcli.New(t.Context(), gitcli.Config{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := git.Run(t.Context(), domain.GitCmd{Dir: repo, Args: []string{"log", "--all", "--format=%H %ct"}})
	if err != nil {
		t.Fatal(err)
	}
	shas := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sha, unix, ok := strings.Cut(line, " ")
		sec, err := strconv.ParseInt(unix, 10, 64)
		if !ok || err != nil {
			t.Fatalf("git log printed %q", line)
		}
		shas[rfc3339(time.Unix(sec, 0))] = sha
	}
	return shas
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
		{ID: "PR_5", Repo: reviewRepo.ID, Number: 5, Title: "t5", Author: user("bob"), State: domain.PullOpen,
			BaseBranch: "main", Created: reviewAt(time.May, 1, 9), Updated: reviewAt(time.May, 20, 10),
			Commits: []domain.ForgeCommit{unlanded("5", "bob")},
			Reviews: []domain.Review{review("PRR_5a", "gina", domain.ReviewCommented, reviewAt(time.May, 20, 10))}},
		merged(6, "erin", reviewAt(time.October, 4, 9), reviewAt(time.October, 6, 16), landed(reviewAt(time.October, 6, 16), "erin"),
			[]domain.ForgeCommit{unlanded("6", "erin"), unlanded("7", "erin")},
			review("PRR_6a", "zoe", domain.ReviewApproved, reviewAt(time.October, 5, 8))),
	}
}

// reviewSeed stores SPEC's replica as a sync would: the repository, one page
// whose watermark is #6's update, and the sync that started at
// reviewSynced.
func reviewSeed(t *testing.T, s *sqlite.Store, commits map[string]string) {
	t.Helper()
	ctx := t.Context()
	page := domain.ForgePage{Repo: reviewRepo.ID, PullRequests: reviewPulls(commits), Updated: reviewAt(time.October, 6, 16)}
	if err := s.PutForgeRepos(ctx, []domain.ForgeRepo{reviewRepo}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutForgePage(ctx, &page); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkForgeSynced(ctx, reviewRepo.ID, reviewSynced); err != nil {
		t.Fatal(err)
	}
}
