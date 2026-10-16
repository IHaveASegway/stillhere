package sqlite_test

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// TestStoreRoundTrip is the contract test of the facts store
// (docs/spec/codebase.md#run-model, docs/spec/codebase.md#tests): every
// field of a run's facts and metrics loads back exactly as it was put, so
// LoadFacts(PutRepoFacts(x)) is reflect.DeepEqual to x, with lists in the
// order they were put, nil for empty, times in UTC whatever zone they were
// put in, and a zero as +0; Run(id) returns the run as begun and finished,
// policy source included; RunWatermarks returns the forge watermarks the
// run read as they were last put, each with its name and the run's
// repositories it joined (domain.RunWatermark); RunForge returns the GitHub
// data the run kept as it was last put, with those watermarks and that join
// (ADR 0011); and readers never see a running or failed run.
func TestStoreRoundTrip(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "home", "facts.db") // Open creates the directory
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	run := sampleRun()
	zone := time.FixedZone("IST", 5*3600+30*60)
	begun := run
	begun.Started = run.Started.In(zone)                // the store keeps the instant, in UTC
	begun.ID, begun.Status = 99, domain.RunStatusFailed // the store's to set: ignored
	id, err := s.BeginRun(ctx, begun)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a running run is invisible", func(t *testing.T) {
		wantNotFound(t, s, id)
		if _, err := s.LatestRun(ctx, domain.RunKindScan); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("LatestRun(scan) = %v, want ErrNotFound", err)
		}
	})

	want := sampleFacts(run)
	// Put the repositories out of key order, with every time in another
	// zone: LoadFacts sorts them by key and returns the times in UTC.
	for i := len(want.Repos) - 1; i >= 0; i-- {
		f := inZone(want.Repos[i], zone)
		if err := s.PutRepoFacts(ctx, id, &f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRunFacts(ctx, id, want.Groups, want.Warnings); err != nil {
		t.Fatal(err)
	}
	values, ranks := sampleMetrics(want.Repos[0].Repo.Key, run.Now)
	// Two calls append: LoadMetrics returns one list in the order put.
	if err := s.PutMetrics(ctx, id, values[:2], ranks); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMetrics(ctx, id, values[2:], nil); err != nil {
		t.Fatal(err)
	}
	// The forge watermarks the run read, each with its name and the run's
	// repositories it joined. A second call replaces the first, join rows
	// included, and a watermark that joins nothing is refused.
	if err := s.PutRunWatermarks(ctx, id, []domain.RunWatermark{
		{ForgeWatermark: domain.ForgeWatermark{Repo: "R_api"}, Name: "acme/old-api", Keys: []string{"local:api"}},
		{ForgeWatermark: domain.ForgeWatermark{Repo: "R_gone"}, Name: "acme/gone", Keys: []string{"local:gone"}},
	}); err != nil {
		t.Fatal(err)
	}
	marks := sampleRunWatermarks(zone)
	if err := s.PutRunWatermarks(ctx, id, marks); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunWatermarks(ctx, id, []domain.RunWatermark{{ForgeWatermark: domain.ForgeWatermark{Repo: "R_api"}}}); err == nil {
		t.Error("PutRunWatermarks took a watermark that joins none of the run's repositories")
	}
	// The GitHub data the run read, whole. A second call replaces the
	// first, and a pull request of a repository that is not the run's is
	// refused.
	repos, pulls, links := sampleRunForge(zone)
	if err := s.PutRunForge(ctx, id, repos[:1], pulls[:1], links[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunForge(ctx, id, repos, pulls, links); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunForge(ctx, id, repos[:1], pulls, nil); err == nil {
		t.Error("PutRunForge took a pull request of a repository that is not among the run's")
	}
	if err := s.PutRunForge(ctx, id, repos[:1], pulls[:1], links); err == nil {
		t.Error("PutRunForge took a link of a repository that is not among the run's")
	}
	before := time.Now().UTC()
	if err := s.FinishRun(ctx, id, domain.RunStatusComplete); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	t.Run("Run returns the run as begun and finished", func(t *testing.T) {
		got, err := s.Run(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Finished.Before(before) || got.Finished.After(after) || got.Finished.Location() != time.UTC {
			t.Errorf("Finished = %v, want a UTC time between %v and %v", got.Finished, before, after)
		}
		wantRun := run
		wantRun.ID, wantRun.Status, wantRun.Finished = id, domain.RunStatusComplete, got.Finished
		wantEqual(t, "Run", wantRun, got)
		latest, err := s.LatestRun(ctx, domain.RunKindScan)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "LatestRun", wantRun, latest)
		if _, err := s.LatestRun(ctx, domain.RunKindSync); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("LatestRun(sync) = %v, want ErrNotFound", err)
		}
		// Runs (MC-17): the run with its groups and repositories as
		// LoadFacts gives them, and none of another kind.
		runs, err := s.Runs(ctx, domain.RunKindScan)
		if err != nil {
			t.Fatal(err)
		}
		wantScopes := domain.RunScopes{Run: wantRun, Groups: want.Groups}
		for _, f := range want.Repos {
			wantScopes.Repos = append(wantScopes.Repos, f.Repo)
		}
		wantEqual(t, "Runs", []domain.RunScopes{wantScopes}, runs)
		if runs, err := s.Runs(ctx, domain.RunKindSync); err != nil || len(runs) != 0 {
			t.Errorf("Runs(sync) = %v, %v; want none", runs, err)
		}
	})

	t.Run("LoadFacts returns the facts as put", func(t *testing.T) {
		got, err := s.LoadFacts(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "LoadFacts", want, got)
	})

	t.Run("LoadMetrics returns the metrics as put", func(t *testing.T) {
		gotValues, gotRanks, err := s.LoadMetrics(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		// NaN equals nothing, itself included, so check the NaNs apart and
		// compare the rest.
		if n := len(gotValues); n > 0 {
			last := &gotValues[n-1]
			if !math.IsNaN(last.Value) || !math.IsNaN(last.Detail["ratio"]) {
				t.Errorf("NaN loaded as Value %v, Detail[ratio] %v", last.Value, last.Detail["ratio"])
			}
			last.Value, last.Detail["ratio"] = 0, 0
		}
		// -0 == +0, so reflect.DeepEqual cannot see the sign either: the
		// store writes a -0 as +0 (SQLite keeps a whole REAL as an integer,
		// which has none), and the who_knows value's Den, put as -0, must
		// load as +0.
		if len(gotValues) > 2 && math.Signbit(gotValues[2].Den) {
			t.Errorf("Den put as -0 loaded as %v, want +0", gotValues[2].Den)
		}
		wantValues := slices.Clone(values)
		last := &wantValues[len(wantValues)-1]
		last.Value, last.Detail = 0, map[string]float64{"ratio": 0}
		wantEqual(t, "LoadMetrics values", wantValues, gotValues)
		wantEqual(t, "LoadMetrics ranks", ranks, gotRanks)
	})

	t.Run("RunWatermarks returns the watermarks as put", func(t *testing.T) {
		got, err := s.RunWatermarks(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "RunWatermarks", sampleRunWatermarks(time.UTC), got)
		if err := s.PutRunWatermarks(ctx, id, marks); err == nil {
			t.Error("PutRunWatermarks changed a published run")
		}
	})

	t.Run("RunForge returns the GitHub data as put, with its watermarks and join", func(t *testing.T) {
		got, err := s.RunForge(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		repos, pulls, links := sampleRunForge(time.UTC)
		want := &domain.ForgeFacts{Repos: repos, PullRequests: pulls, Links: links}
		for _, wm := range sampleRunWatermarks(time.UTC) {
			want.Watermarks = append(want.Watermarks, wm.ForgeWatermark)
			if want.Join == nil {
				want.Join = map[string][]string{}
			}
			want.Join[wm.Repo] = wm.Keys
		}
		wantEqual(t, "RunForge", want, got)
		if err := s.PutRunForge(ctx, id, repos, pulls, links); err == nil {
			t.Error("PutRunForge changed a published run")
		}
	})

	t.Run("a failed run is invisible, and a partial one is published", func(t *testing.T) {
		later := run
		later.Now = run.Now.Add(24 * time.Hour)
		failed, err := s.BeginRun(ctx, later)
		if err != nil {
			t.Fatal(err)
		}
		f := domain.RepoFacts{Repo: domain.Repo{Key: "local:half"}, Status: domain.RepoStatusOK}
		if err := s.PutRepoFacts(ctx, failed, &f); err != nil {
			t.Fatal(err)
		}
		wantNotFound(t, s, failed)
		if err := s.FinishRun(ctx, failed, domain.RunStatusFailed); err != nil {
			t.Fatal(err)
		}
		wantNotFound(t, s, failed)
		if err := s.PutRepoFacts(ctx, failed, &f); err == nil {
			t.Error("PutRepoFacts on a failed run succeeded")
		}
		if latest, err := s.LatestRun(ctx, domain.RunKindScan); err != nil || latest.ID != id {
			t.Errorf("LatestRun(scan) = run %d, %v; want run %d, the newest published", latest.ID, err, id)
		}

		partial, err := s.BeginRun(ctx, later)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, partial, domain.RunStatusPartial); err != nil {
			t.Fatal(err)
		}
		latest, err := s.LatestRun(ctx, domain.RunKindScan)
		if err != nil || latest.ID != partial || latest.Status != domain.RunStatusPartial {
			t.Errorf("LatestRun(scan) = run %d (%s), %v; want the partial run %d", latest.ID, latest.Status, err, partial)
		}
		got, err := s.LoadFacts(ctx, partial)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "LoadFacts of a run without facts", &domain.FactSet{Now: later.Now, Policy: later.Policy}, got)
		if marks, err := s.RunWatermarks(ctx, partial); err != nil || marks != nil {
			t.Errorf("RunWatermarks of a run that read no GitHub data = %v, %v; want nil", marks, err)
		}
		if github, err := s.RunForge(ctx, partial); err != nil || github != nil {
			t.Errorf("RunForge of a run that read no GitHub data = %+v, %v; want nil", github, err)
		}
		wantNotFound(t, s, partial+1000)
	})

	t.Run("the facts survive closing and reopening", func(t *testing.T) {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s = reopened
		got, err := s.LoadFacts(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, "LoadFacts after reopening", want, got)
	})
}

// wantNotFound checks that every read of run id is domain.ErrNotFound.
func wantNotFound(t *testing.T, s *sqlite.Store, id int64) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.Run(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Run(%d) = %v, want ErrNotFound", id, err)
	}
	if _, err := s.LoadFacts(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("LoadFacts(%d) = %v, want ErrNotFound", id, err)
	}
	if _, _, err := s.LoadMetrics(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("LoadMetrics(%d) = %v, want ErrNotFound", id, err)
	}
	if _, err := s.RunWatermarks(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("RunWatermarks(%d) = %v, want ErrNotFound", id, err)
	}
	if _, err := s.RunForge(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("RunForge(%d) = %v, want ErrNotFound", id, err)
	}
}

// sampleRunWatermarks are the forge watermarks of the sample run, with
// times in zone: one forge repository that two clones of the run name,
// complete from a year back but for the pull requests its last check found
// missing (ADR 0013), and one whose first sync stopped halfway, whose times
// are zero.
func sampleRunWatermarks(zone *time.Location) []domain.RunWatermark {
	return []domain.RunWatermark{{
		ForgeWatermark: domain.ForgeWatermark{
			Repo:    "R_api",
			Updated: time.Date(2026, time.October, 6, 18, 0, 0, 123456789, time.UTC).In(zone),
			From:    time.Date(2025, time.October, 6, 17, 59, 0, 0, time.UTC).In(zone),
			Synced:  time.Date(2026, time.October, 6, 22, 0, 0, 0, time.UTC).In(zone),
			Missing: 3,
		},
		Name: "acme/api",
		Keys: []string{"git@github.com:acme/api.git", "https://github.com/acme/api"},
	}, {
		ForgeWatermark: domain.ForgeWatermark{Repo: "R_docs", Pending: true},
		Name:           "acme/docs",
		Keys:           []string{"https://github.com/acme/docs"},
	}}
}

// sampleRunForge is the GitHub data the sample run kept, with times in
// zone: the forge repositories of its watermarks, their pull requests with
// every field set somewhere and every list empty somewhere (nil), as
// LoadForge sorts them, and the links of older pull requests (#103).
func sampleRunForge(zone *time.Location) ([]domain.ForgeRepo, []domain.PullRequest, []domain.ForgeLink) {
	at := func(day, hour int) time.Time {
		return time.Date(2026, time.September, day, hour, 0, 0, 0, time.UTC).In(zone)
	}
	repos := []domain.ForgeRepo{
		{ID: "R_api", Key: "https://github.com/acme/api.git", Owner: "acme", Name: "api", DefaultBranch: "main"},
		{ID: "R_docs", Key: "https://github.com/acme/docs.git", Owner: "acme", Name: "docs", Archived: true, Fork: true},
	}
	pulls := []domain.PullRequest{{
		ID: "PR_1", Repo: "R_api", Number: 1, Title: "Fix the Grüße parser", Author: domain.ForgeUser{Login: "ada"},
		State: domain.PullMerged, BaseBranch: "main", Created: at(1, 9), Updated: at(3, 12), Closed: at(3, 12), Merged: at(3, 12),
		MergeCommit: domain.ForgeCommit{SHA: sha('f'), AuthorEmail: "ada@example.com", AuthorLogin: "ada"},
		Labels:      []string{"bug", "parser"},
		Reviews: []domain.Review{
			{ID: "PRR_1", Author: domain.ForgeUser{Login: "renovate", Bot: true}, State: domain.ReviewCommented, Submitted: at(2, 9),
				BodyLength: 12, Comments: 3},
			{ID: "PRR_2", Author: domain.ForgeUser{Login: "bob"}, State: domain.ReviewApproved, Submitted: at(2, 10)},
		},
		Commits: []domain.ForgeCommit{
			{SHA: sha('c'), AuthorEmail: "ada@example.com", AuthorLogin: "ada"},
			{SHA: sha('d'), AuthorEmail: "ada@other.example"},
		},
	}, {
		ID: "PR_2", Repo: "R_api", Number: 2, Title: "Draft", Author: domain.ForgeUser{Login: "bob"}, State: domain.PullOpen,
		Draft: true, BaseBranch: "main", Created: at(4, 9), Updated: at(4, 9),
		Commits: []domain.ForgeCommit{{SHA: sha('e'), AuthorEmail: "bob@example.com", AuthorLogin: "bob"}},
	}, {
		ID: "PR_3", Repo: "R_docs", Number: 7, Title: "Typo", State: domain.PullClosed, BaseBranch: "gh-pages",
		Created: at(5, 9), Updated: at(6, 9), Closed: at(6, 9), // a deleted author: no login
		Reviews: []domain.Review{{ID: "PRR_3", Author: domain.ForgeUser{Login: "ada"}, State: domain.ReviewDismissed, Submitted: at(5, 10)}},
	}}
	links := []domain.ForgeLink{
		{Repo: "R_api", ForgeCommit: domain.ForgeCommit{SHA: sha('a'), AuthorEmail: "ada@old.example", AuthorLogin: "ada"}},
		{Repo: "R_docs", ForgeCommit: domain.ForgeCommit{SHA: sha('b'), AuthorEmail: "bob@example.com", AuthorLogin: "bob"}},
	}
	return repos, pulls, links
}

// wantEqual fails the test unless got is reflect.DeepEqual to want, naming
// the first field that differs.
func wantEqual(t *testing.T, what string, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s differs at %s", what, diff("", reflect.ValueOf(want), reflect.ValueOf(got)))
	}
}

// diff describes the first difference between want and got.
func diff(path string, want, got reflect.Value) string {
	switch want.Kind() {
	case reflect.Pointer:
		if want.IsNil() != got.IsNil() {
			return fmt.Sprintf("%s: want nil %v, got nil %v", path, want.IsNil(), got.IsNil())
		}
		if want.IsNil() {
			return ""
		}
		return diff(path, want.Elem(), got.Elem())
	case reflect.Struct:
		if want.Type() == reflect.TypeFor[time.Time]() {
			break
		}
		for i := range want.NumField() {
			if d := diff(path+"."+want.Type().Field(i).Name, want.Field(i), got.Field(i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.Slice:
		if want.IsNil() != got.IsNil() || want.Len() != got.Len() {
			return fmt.Sprintf("%s: want %d items (nil %v), got %d (nil %v)",
				path, want.Len(), want.IsNil(), got.Len(), got.IsNil())
		}
		for i := range want.Len() {
			if d := diff(fmt.Sprintf("%s[%d]", path, i), want.Index(i), got.Index(i)); d != "" {
				return d
			}
		}
		return ""
	}
	if reflect.DeepEqual(want.Interface(), got.Interface()) {
		return ""
	}
	return fmt.Sprintf("%s: want %#v, got %#v", path, want.Interface(), got.Interface())
}

// inZone returns a copy of f with every time moved to loc: the same
// instants, which the store returns in UTC.
func inZone(f domain.RepoFacts, loc *time.Location) domain.RepoFacts {
	f.FirstCommit = f.FirstCommit.In(loc)
	f.History = domain.Window{Since: f.History.Since.In(loc), Until: f.History.Until.In(loc)}
	f.Commits = slices.Clone(f.Commits)
	for i := range f.Commits {
		c := &f.Commits[i]
		c.AuthorTime, c.CommitTime = c.AuthorTime.In(loc), c.CommitTime.In(loc)
	}
	f.Landings = slices.Clone(f.Landings)
	for i := range f.Landings {
		f.Landings[i].Time = f.Landings[i].Time.In(loc)
	}
	f.Owners = slices.Clone(f.Owners)
	for i := range f.Owners {
		f.Owners[i].AuthorTime = f.Owners[i].AuthorTime.In(loc)
	}
	return f
}

// sha returns a full SHA made of one repeated hex digit.
func sha(c byte) string { return strings.Repeat(string(c), 40) }

func sampleRun() domain.Run {
	return domain.Run{
		Kind:        domain.RunKindScan,
		Now:         time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC),
		Started:     time.Date(2026, time.October, 14, 9, 30, 15, 123456789, time.UTC), // nanoseconds survive
		ToolVersion: "v0.1.0-test",
		Policy:      domain.PolicyRef{Version: 1, SHA256: strings.Repeat("ab", 32)},
		// The exact bytes the hash covers, binary length prefix included.
		PolicySource: []byte("[headline]\nmetric = \"prod_nonblank_lines\"\n\x00\x00\x00\x07\x00vendor/"),
		Options:      map[string]string{"jobs": "4", "rank": "durable@1", "scope": "group"},
	}
}

// sampleFacts sets every field of a FactSet, with the shapes the core
// produces: a root commit without parents, a merge, co-authors in trailer
// order rather than sorted, a path that is not valid UTF-8, a failed
// repository whose lists are all nil, and a shallow one without a first
// commit time.
func sampleFacts(run domain.Run) *domain.FactSet {
	const api = "https://github.com/acme/api"
	ada := domain.Person{Name: "Ada Example", Email: "ada@example.com"}
	bob := domain.Person{Name: "Bob Ünicode", Email: "bob@example.com"}
	claude := domain.Person{Name: "Claude", Email: "noreply@anthropic.com"}
	pia := domain.Person{Name: "Pia Pair", Email: "pia@example.com"}
	day := func(d int) time.Time { return run.Now.AddDate(0, 0, -d) }
	return &domain.FactSet{
		Now:    run.Now,
		Policy: run.Policy,
		Groups: []domain.Group{
			{Name: "acme", Path: "/src/acme", Repos: []string{api, "local:tools"}},
			{Name: "acme-2", Path: "/elsewhere/acme"}, // no repositories: nil
		},
		Warnings: []domain.Warning{
			{Code: "excluded-repo", Message: "legacy excluded by --exclude-repo leg*", Repo: "local:legacy"},
			{Code: "duplicate-repo", Message: "/src/acme/api-copy is a clone of /src/acme/api"},
		},
		Repos: []domain.RepoFacts{
			{
				Repo: domain.Repo{
					Key: api, Name: "api", Group: "acme", Path: "/src/acme/api", Remote: api,
					Branch: "main", Head: sha('a'),
				},
				Status:      domain.RepoStatusOK,
				Warnings:    []domain.Warning{{Code: "numstat-rename", Message: "a => b => c", Repo: api}},
				FirstCommit: time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC),
				History:     domain.Window{Since: day(365), Until: run.Now},
				Paths: []domain.PathClass{
					{Path: "cmd/main.go", Role: domain.RoleProd, Size: 1234, AtHead: true},
					{Path: "docs/caf\xe9.md", Role: domain.RoleDoc, Size: 10, AtHead: true},
					{Path: "gone/old.py", Role: domain.RoleProd, Size: 77},
					{Path: "vendor/x.go", Skip: domain.SkipDeniedPath, Size: -1, AtHead: true},
				},
				Commits: []domain.Commit{
					{SHA: sha('1'), Author: ada, AuthorTime: day(300), CommitTime: day(299), Subject: "init"},
					{
						SHA: sha('2'), Parents: []string{sha('1')}, Author: pia, AuthorTime: day(200),
						CommitTime: day(200), CoAuthors: []domain.Person{pia, claude, ada},
						Subject: "feat: Frank & Sons <script>",
					},
					{
						SHA: sha('3'), Parents: []string{sha('2'), sha('4')}, Author: bob, AuthorTime: day(100),
						CommitTime: day(100), Subject: "Merge branch 'feature'",
					},
					{SHA: sha('4'), Parents: []string{sha('1')}, Author: bob, AuthorTime: day(150), CommitTime: day(101), Subject: "side"},
				},
				Landings: []domain.Landing{
					{SHA: sha('1'), Time: day(299), Commits: []string{sha('1')}},
					{SHA: sha('2'), Time: day(200), Commits: []string{sha('2')}},
					{SHA: sha('3'), Time: day(100), Merge: true, Commits: []string{sha('3'), sha('4')}},
				},
				Changes: []domain.FileChange{
					{Commit: sha('1'), Path: "main.go", Added: 10, AddedNonBlank: 8},
					{Commit: sha('2'), Path: "cmd/main.go", OldPath: "main.go", Added: 3, Removed: 2, AddedNonBlank: 3, RemovedNonBlank: 1},
					{Commit: sha('4'), Path: "logo.png", Binary: true, AddedNonBlank: -1, RemovedNonBlank: -1},
				},
				Reverts:   []domain.Revert{{SHA: sha('5'), Reverted: sha('2')}},
				LineFacts: true,
				Owners: []domain.LineOwner{
					// sha('1') wrote main.go, which sha('2') moved: blame names the path the lines had.
					{Path: "cmd/main.go", Commit: sha('1'), OrigPath: "main.go", Author: ada, AuthorTime: day(300), Lines: 7},
					{Path: "cmd/main.go", Commit: sha('2'), Author: claude, AuthorTime: day(200), CoAuthors: []domain.Person{pia, ada}, Lines: 3},
				},
				IgnoreRevs: domain.IgnoreRevs{
					Source: domain.IgnoreRevsRoot, SHA256: strings.Repeat("cd", 32), Revs: []string{sha('6'), sha('7')},
				},
				BlameErrors: []domain.FileError{{Path: "big.go", Message: "git blame: timed out after 2m0s"}},
				Aliases: []domain.Alias{
					{Email: "ada@old.example", Name: "Ada Old", Canonical: "ada@example.com"},
					{Email: "pia@laptop.local", Canonical: "pia@example.com"},
				},
			},
			{
				Repo:   domain.Repo{Key: "local:empty", Name: "empty", Path: "/src/empty", Bare: true, Partial: true},
				Status: domain.RepoStatusError,
				Error:  "git log: exit 128: fatal: bad object " + sha('e'),
			},
			{
				Repo: domain.Repo{
					Key: "local:tools", Name: "tools", Group: "acme", Path: "/src/acme/tools", Branch: "trunk",
					Head: sha('b'), Shallow: true,
				},
				Status:   domain.RepoStatusOK,
				Warnings: []domain.Warning{{Code: "shallow-clone", Message: "no blame facts", Repo: "local:tools"}},
				History:  domain.Window{Since: day(365), Until: run.Now}, // FirstCommit unknown: zero
				Commits:  []domain.Commit{{SHA: sha('8'), Parents: []string{sha('9')}, Author: ada, AuthorTime: day(3), CommitTime: day(2), Subject: "fix"}},
				Landings: []domain.Landing{{SHA: sha('8'), Time: day(2), Commits: []string{sha('8')}}},
			},
		},
	}
}

// sampleMetrics returns values and rank intervals that set every field. The
// last value carries NaNs, which the test compares apart.
func sampleMetrics(repo string, now time.Time) ([]domain.MetricValue, []domain.RankInterval) {
	durable := domain.MetricID{Name: "durable", Version: 1}
	cohort := domain.Window{Since: now.AddDate(0, 0, -365), Until: now.AddDate(0, 0, -90)}
	group := domain.Scope{Kind: domain.ScopeGroup, Key: "acme"}
	ada := domain.Subject{Kind: domain.SubjectPerson, Key: "ada@example.com"}
	bob := domain.Subject{Kind: domain.SubjectPerson, Key: "bob@example.com"}
	values := []domain.MetricValue{
		{
			Metric: durable, Scope: group, Subject: ada, Window: cohort, Value: 0.75, Num: 30, Den: 40, N: 12,
			Detail: map[string]float64{"surviving": 30, "added": 40, "reverted": 0},
		},
		{
			Metric: domain.MetricID{Name: "landed", Version: 1}, Scope: domain.Scope{Kind: domain.ScopeRepo, Key: repo},
			Subject: bob, Window: domain.Window{Since: now.AddDate(0, 0, -7), Until: now}, Value: 3, Num: 3, Den: 1, N: 3,
		},
		{
			Metric: domain.MetricID{Name: "who_knows", Version: 1}, Scope: domain.Scope{Kind: domain.ScopeAll},
			Subject: domain.Subject{Kind: domain.SubjectArea, Key: repo + ":cmd/"}, Value: math.Inf(1),
			Num: math.Inf(-1), Den: math.Copysign(0, -1), N: 0, // -0 loads as +0; no window: zero times
		},
		{
			Metric: durable, Scope: group, Subject: bob, Window: cohort, Value: math.NaN(), Num: 0, Den: 0,
			Detail: map[string]float64{"ratio": math.NaN()},
		},
	}
	ranks := []domain.RankInterval{
		{Metric: durable, Scope: group, Subject: ada, Window: cohort, Estimate: 0.75, Lo: 0.6, Hi: 0.9, RankLo: 1, RankHi: 3, N: 12, Eligible: true},
		{Metric: durable, Scope: group, Subject: bob, Window: cohort, Estimate: 0.5, Lo: 0.1, Hi: 0.95, N: 4},
	}
	return values, ranks
}
