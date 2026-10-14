package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// TestDiscover is the contract test of discovery (docs/spec/discovery.md)
// on the discovery fixture (testdata/fixture/SPEC.md#discovery-fixture-testdiscover):
// which paths are repositories or groups, duplicates by scrubbed origin URL,
// the default branch and Head, and a planted token that reaches no output.
func TestDiscover(t *testing.T) {
	t.Parallel()
	fx := fixture.Discovery(t)
	git := newRunner(t, "")
	main := discoverRevParse(t, git, fx.Upstream, "refs/heads/main")
	trunk := discoverRevParse(t, git, fx.Upstream, "refs/heads/trunk")
	const widget = "https://github.com/example/widget.git"

	t.Run("a duplicate remote is skipped with a warning naming both paths and the scrubbed URL", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.WidgetB, fx.Other, fx.WidgetA)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: "https://github.com/example/other.git", Name: "other", Path: fx.Other, Remote: "https://github.com/example/other.git", Branch: "main", Head: main},
				{Key: widget, Name: "widget-a", Path: fx.WidgetA, Remote: widget, Branch: "main", Head: main},
			},
			Warnings: []domain.Warning{{
				Code:    "duplicate-repo",
				Message: "skipped " + fx.WidgetB + "; same remote as " + fx.WidgetA + " (" + widget + ")",
				Repo:    widget,
			}},
		})
	})

	t.Run("a work-tree clone and a bare mirror of one HTTPS remote are duplicates", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.WidgetA, fx.Mirror)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: widget, Name: "mirror", Path: fx.Mirror, Bare: true, Remote: widget, Branch: "main", Head: main},
			},
			Warnings: []domain.Warning{{
				Code:    "duplicate-repo",
				Message: "skipped " + fx.WidgetA + "; same remote as " + fx.Mirror + " (" + widget + ")",
				Repo:    widget,
			}},
		})
	})

	t.Run("the scp and HTTPS forms of one path stay two repositories", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.SCP, fx.HTTPS)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: "git@github.com:o/r.git", Name: "scp", Path: fx.SCP, Remote: "git@github.com:o/r.git", Branch: "main", Head: main},
				{Key: "https://github.com/o/r.git", Name: "https", Path: fx.HTTPS, Remote: "https://github.com/o/r.git", Branch: "main", Head: main},
			},
		})
	})

	t.Run("a remote that does not parse is never a dedupe key", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.UnparsedA, fx.UnparsedB)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: "local:unparsed-a", Name: "unparsed-a", Path: fx.UnparsedA, Remote: redact.Unparsed, Branch: "main", Head: main},
				{Key: "local:unparsed-b", Name: "unparsed-b", Path: fx.UnparsedB, Remote: redact.Unparsed, Branch: "main", Head: main},
			},
		})
	})

	t.Run("bare repositories are scanned, named directly or as a group's member", func(t *testing.T) {
		t.Parallel()
		team := fx.Group
		got := discoverRun(t, git, fx.Bare, team)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: "https://github.com/example/api.git", Name: "api", Group: "team", Path: filepath.Join(team, "api"), Remote: "https://github.com/example/api.git", Branch: "main", Head: main},
				// A bare repository's branch is HEAD's, though main exists.
				{Key: "https://github.com/example/web.git", Name: "web", Group: "team", Path: filepath.Join(team, "web.git"), Bare: true, Remote: "https://github.com/example/web.git", Branch: "trunk", Head: trunk},
				{Key: "local:bare", Name: "bare", Path: fx.Bare, Bare: true, Branch: "trunk", Head: trunk},
				// A worktree, whose .git is a file, is a repository. notes/,
				// though it holds a clone, and README.md are not members.
				{Key: "local:linked", Name: "linked", Group: "team", Path: filepath.Join(team, "linked"), Branch: "main", Head: main},
			},
			Groups: []domain.Group{{
				Name:  "team",
				Path:  team,
				Repos: []string{"https://github.com/example/api.git", "https://github.com/example/web.git", "local:linked"},
			}},
		})
	})

	t.Run("a checkout's nested clone is not added", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.Outer)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: "https://github.com/example/outer.git", Name: "outer", Path: fx.Outer, Remote: "https://github.com/example/outer.git", Branch: "main", Head: main},
			},
		})
	})

	t.Run("groups that share a basename get suffixes in path order, and no two share a name", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.SrcB, fx.SrcC, fx.SrcA)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				{Key: "local:one", Name: "one", Group: "src", Path: filepath.Join(fx.SrcA, "one"), Branch: "main", Head: main},
				{Key: "local:two", Name: "two", Group: "src-3", Path: filepath.Join(fx.SrcB, "two"), Branch: "main", Head: main},
			},
			Groups: []domain.Group{
				{Name: "src", Path: fx.SrcA, Repos: []string{"local:one"}},
				{Name: "src-2", Path: fx.SrcC}, // its own basename, which b/src cannot take
				{Name: "src-3", Path: fx.SrcB, Repos: []string{"local:two"}},
			},
		})
	})

	t.Run("a repository named twice is one repository and keeps its group", func(t *testing.T) {
		t.Parallel()
		api := filepath.Join(fx.Group, "api")
		got := discoverRun(t, git, api, fx.WidgetA, fx.WidgetA+string(filepath.Separator), fx.Group)
		if len(got.Warnings) != 0 {
			t.Errorf("warnings = %v, want none: one path named twice is not a duplicate clone", got.Warnings)
		}
		var paths []string
		for _, r := range got.Repos {
			paths = append(paths, r.Path)
			if r.Path == api && r.Group != "team" {
				t.Errorf("api has group %q, want team: the group keeps its member", r.Group)
			}
		}
		want := []string{api, filepath.Join(fx.Group, "web.git"), filepath.Join(fx.Group, "linked"), fx.WidgetA}
		if !discoverSameSet(paths, want) {
			t.Errorf("repos at %q, want each of %q once", paths, want)
		}
	})

	t.Run("the default branch is origin/HEAD's, else main, else master, else HEAD", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.WidgetA, fx.Tracking, fx.MainFirst, fx.MasterOnly, fx.Current, fx.Detached)
		discoverCheck(t, got, &Discovery{
			Repos: []domain.Repo{
				// origin/HEAD names trunk, and Head is origin/trunk: the
				// local commit on trunk never landed upstream.
				{Key: "https://github.com/example/tracking.git", Name: "tracking", Path: fx.Tracking, Remote: "https://github.com/example/tracking.git", Branch: "trunk", Head: trunk},
				{Key: widget, Name: "widget-a", Path: fx.WidgetA, Remote: widget, Branch: "main", Head: main},
				{Key: "local:current", Name: "current", Path: fx.Current, Branch: "dev", Head: trunk},
				{Key: "local:detached", Name: "detached", Path: fx.Detached, Branch: "HEAD", Head: trunk},
				{Key: "local:main-first", Name: "main-first", Path: fx.MainFirst, Branch: "main", Head: main},
				{Key: "local:master-only", Name: "master-only", Path: fx.MasterOnly, Branch: "master", Head: main},
			},
		})
		if local := discoverRevParse(t, git, fx.Tracking, "refs/heads/trunk"); local == trunk {
			t.Errorf("the fixture's local trunk is origin/trunk (%s), so the case above proves nothing", local)
		}
	})

	t.Run("a path that is neither a repository nor a group fails alone", func(t *testing.T) {
		t.Parallel()
		got := discoverRun(t, git, fx.File, fx.Missing, fx.Empty, fx.Broken, fx.Stray, fx.StrayBare, fx.WidgetA)
		if len(got.Repos) != 1 || got.Repos[0].Path != fx.WidgetA {
			t.Errorf("repos = %+v, want only %s: a failed path must not take the others with it", got.Repos, fx.WidgetA)
		}
		if got.Groups != nil {
			t.Errorf("groups = %+v, want none: a directory with a .git git cannot use is a broken repository, not a group", got.Groups)
		}
		want := map[string]string{
			fx.Broken:    "", // git's own message, in the user's language
			fx.Empty:     "no commits",
			fx.Stray:     "enclosing work tree", // not Outer, found by walking up
			fx.StrayBare: "inside a bare repository",
			fx.File:      "not a directory",
			fx.Missing:   "does not exist",
		}
		if len(got.Failed) != len(want) {
			t.Fatalf("failed = %+v, want one entry for each of %d paths", got.Failed, len(want))
		}
		for i, f := range got.Failed {
			part, ok := want[f.Path]
			if !ok || f.Message == "" || !strings.Contains(f.Message, part) {
				t.Errorf("failed[%d] = %+v, want one of the failing paths and a message containing %q", i, f, part)
			}
		}
	})

	t.Run("a cancelled run is an error, not a list of failed paths", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		got, err := Discover(ctx, discoverInterrupt{GitRunner: git, cancel: cancel}, []string{fx.WidgetA, fx.Other, fx.Bare})
		if !errors.Is(err, context.Canceled) || got != nil {
			t.Errorf("Discover = %+v, %v; want no discovery and an error that wraps context.Canceled", got, err)
		}
	})

	t.Run("the planted token appears in no struct, warning, error or log line", func(t *testing.T) {
		t.Parallel()
		for _, dir := range []string{filepath.Join(fx.WidgetA, ".git"), filepath.Join(fx.UnparsedA, ".git")} {
			config, err := os.ReadFile(filepath.Join(dir, "config"))
			if err != nil || !bytes.Contains(config, []byte(fixture.DiscoveryToken)) {
				t.Fatalf("%s/config does not hold the token (%v), so this case proves nothing", dir, err)
			}
		}
		var log bytes.Buffer
		logged, err := gitcli.New(t.Context(), gitcli.Config{
			Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		})
		if err != nil {
			t.Fatalf("gitcli.New: %v", err)
		}
		got, err := Discover(t.Context(), logged, []string{
			fx.WidgetA, fx.WidgetB, fx.Other, fx.Mirror, fx.SCP, fx.HTTPS, fx.UnparsedA, fx.UnparsedB,
			fx.Bare, fx.Group, fx.Outer, fx.SrcA, fx.SrcB, fx.SrcC,
			fx.Tracking, fx.MainFirst, fx.MasterOnly, fx.Current, fx.Detached,
			fx.Empty, fx.Broken, fx.Stray, fx.StrayBare, fx.File, fx.Missing,
		})
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("encode the discovery: %v", err)
		}
		for name, out := range map[string]string{
			"the discovery as JSON": string(encoded),
			"the discovery as Go":   fmt.Sprintf("%#v", got),
			"the git log":           log.String(),
		} {
			if strings.Contains(out, fixture.DiscoveryToken) {
				t.Errorf("%s holds the token:\n%s", name, out)
			}
		}
		if !strings.Contains(string(encoded), widget) || !strings.Contains(log.String(), "rev-parse") {
			t.Errorf("the output lacks the scrubbed widget remote or the log lacks git's commands, so the check above proves nothing")
		}
	})
}

// TestCloneHealth is the contract test of clone health
// (docs/spec/discovery.md#clone-health, testdata/fixture/SPEC.md): shallow
// and partial clones are kept, flagged and warned about; a partial clone
// still gives commit facts but no line facts; and nothing connects to its
// promisor remote for the blobs it lacks, because the Analysis profile sets
// GIT_NO_LAZY_FETCH=1. gitcli.New refuses git older than 2.44, the first to
// honour that variable, so the assertion never needs SPEC's skip below 2.44.
func TestCloneHealth(t *testing.T) {
	t.Parallel()
	promisor, connections := discoverPromisor(t)
	fx := fixture.CloneHealth(t, promisor)
	git := newRunner(t, "")
	main := discoverRevParse(t, git, fx.Upstream, "refs/heads/main")

	got := discoverRun(t, git, fx.Full, fx.Shallow, fx.Partial, fx.Extension, fx.OtherPromisor, fx.NotPromisor)
	clone := func(path, remote string, shallow, partial bool) domain.Repo {
		return domain.Repo{
			Key: remote, Name: filepath.Base(path), Path: path, Remote: remote,
			Branch: "main", Head: main, Shallow: shallow, Partial: partial,
		}
	}
	wantRepos := []domain.Repo{
		clone(fx.Partial, promisor, false, true),
		clone(fx.Extension, "https://github.com/example/extension.git", false, true),
		clone(fx.Full, "https://github.com/example/full.git", false, false),
		clone(fx.NotPromisor, "https://github.com/example/not-promisor.git", false, false),
		clone(fx.OtherPromisor, "https://github.com/example/other-promisor.git", false, true),
		clone(fx.Shallow, "https://github.com/example/shallow.git", true, false),
	}
	if !reflect.DeepEqual(got.Repos, wantRepos) {
		t.Errorf("repos:\n%s\nwant:\n%s", discoverJSON(got.Repos), discoverJSON(wantRepos))
	}
	wantWarnings := []struct{ code, repo, path string }{
		{"partial-clone", promisor, fx.Partial},
		{"partial-clone", "https://github.com/example/extension.git", fx.Extension},
		{"partial-clone", "https://github.com/example/other-promisor.git", fx.OtherPromisor},
		{"shallow-clone", "https://github.com/example/shallow.git", fx.Shallow},
	}
	if len(got.Warnings) != len(wantWarnings) {
		t.Fatalf("warnings = %+v, want %d", got.Warnings, len(wantWarnings))
	}
	for i, w := range wantWarnings {
		if g := got.Warnings[i]; g.Code != w.code || g.Repo != w.repo || !strings.Contains(g.Message, w.path) {
			t.Errorf("warnings[%d] = %+v, want code %s for %s, naming %s", i, g, w.code, w.repo, w.path)
		}
	}
	if got.Groups != nil || got.Failed != nil {
		t.Errorf("groups = %+v, failed = %+v, want neither", got.Groups, got.Failed)
	}

	t.Run("a partial clone gives commit facts, fails on line facts, and fetches nothing", func(t *testing.T) {
		out, err := git.Run(t.Context(), domain.GitCmd{Dir: fx.Partial, Args: []string{"log", "--format=%H", main}})
		if n := len(strings.Fields(string(out))); err != nil || n != 2 {
			t.Errorf("git log on the partial clone: %d commits, %v; want both commits of main", n, err)
		}
		// numstat diffs app.py against its first version, and blame reads
		// every version: the clone has neither blob.
		for _, args := range [][]string{
			{"log", "--numstat", "-1", "--format=%H", main},
			{"blame", "--line-porcelain", main, "--", "app.py"},
		} {
			if _, err := git.Run(t.Context(), domain.GitCmd{Dir: fx.Partial, Args: args}); err == nil {
				t.Errorf("git %s succeeded on the partial clone, so it fetched the missing blob", strings.Join(args, " "))
			}
		}
		if n := connections(); n != 0 {
			t.Fatalf("%d connections reached the promisor remote under the Analysis profile, want none", n)
		}
		// The control: without GIT_NO_LAZY_FETCH, the same command does
		// connect, so the promisor would have seen a lazy fetch above.
		_, _ = git.Run(t.Context(), domain.GitCmd{Dir: fx.Partial, Profile: domain.Sync, Args: []string{"log", "--numstat", "-1", "--format=%H", main}})
		if connections() == 0 {
			t.Errorf("a lazy fetch under the Sync profile never reached the fake promisor, so the assertion above proves nothing")
		}
	})
}

// discoverInterrupt is the real git runner with a user who presses Ctrl-C as
// git is first asked anything: it cancels the run, then runs git as usual,
// so git sees the cancellation exactly as it would.
type discoverInterrupt struct {
	domain.GitRunner
	cancel context.CancelFunc
}

func (r discoverInterrupt) Run(ctx context.Context, cmd domain.GitCmd) ([]byte, error) {
	r.cancel()
	return r.GitRunner.Run(ctx, cmd)
}

// discoverRun runs Discover on paths and stops the test if it fails.
func discoverRun(t *testing.T, git domain.GitRunner, paths ...string) *Discovery {
	t.Helper()
	d, err := Discover(t.Context(), git, paths)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return d
}

// discoverCheck compares a discovery with the one wanted, nil slices
// included: an empty slice must be nil (docs/spec/codebase.md#determinism).
func discoverCheck(t *testing.T, got, want *Discovery) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discovery:\n%s\nwant:\n%s", discoverJSON(got), discoverJSON(want))
	}
}

// discoverJSON renders v for a failure message; JSON shows a nil slice as
// null and an empty one as [], so the two never look alike.
func discoverJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(b)
}

// discoverRevParse returns the full SHA of the commit rev names in dir.
func discoverRevParse(t *testing.T, git domain.GitRunner, dir, rev string) string {
	t.Helper()
	out, err := git.Run(t.Context(), domain.GitCmd{Dir: dir, Args: []string{"rev-parse", "--verify", rev + "^{commit}"}})
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", rev, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// discoverSameSet reports whether got holds each element of want exactly
// once and nothing else.
func discoverSameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	count := make(map[string]int, len(want))
	for _, w := range want {
		count[w]++
	}
	for _, g := range got {
		if count[g]--; count[g] < 0 {
			return false
		}
	}
	return true
}

// discoverPromisor serves a promisor remote that answers nothing: it accepts
// each connection on a local port, counts it and closes it at once, so a
// lazy fetch shows up as a connection and then fails. It returns the
// remote's URL and a function that reports the connections so far.
func discoverPromisor(t *testing.T) (url string, connections func() int64) {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the fake promisor: %v", err)
	}
	var n atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
	})
	return "http://" + l.Addr().String() + "/widget.git", n.Load
}
