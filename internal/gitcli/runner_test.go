//go:build unix

package gitcli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
)

// helperEnv makes the test binary act as a helper that git starts, instead
// of running the tests.
const helperEnv = "STILLHERE_GITCLI_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "escape" {
		escapedHelper(os.Args[1])
		return
	}
	os.Exit(m.Run())
}

// escapedHelper does what a daemonising filter does: it leaves git's process
// group and session, closes stderr and keeps stdout open. It records its pid
// so the test can kill it.
func escapedHelper(pidfile string) {
	_, _ = syscall.Setsid()
	_ = os.Stderr.Close()
	_ = os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	time.Sleep(time.Minute)
	os.Exit(0)
}

// TestRunner is the contract test of the git adapter
// (docs/spec/codebase.md#tests, docs/spec/git-invocation.md). It runs real
// git. Process control is driven through repo-local aliases, which git runs
// with sh inside its own process group, as it runs any helper.
func TestRunner(t *testing.T) {
	t.Run("a timeout wins over a reader that fails on the truncated output", func(t *testing.T) {
		t.Parallel()
		dir := newRepo(t)
		alias(t, dir, "partial", "printf partial-record; sleep 30")
		err := newRunner(t).Stream(t.Context(), domain.GitCmd{
			Dir: dir, Args: []string{"partial"}, Timeout: 300 * time.Millisecond,
		}, func(r io.Reader) error {
			b, err := io.ReadAll(r)
			if err == nil && !bytes.HasSuffix(b, []byte("\n")) {
				err = errors.New("truncated record")
			}
			return err
		})
		wantTimeout(t, err)
	})

	t.Run("a timeout kills git's whole process group", func(t *testing.T) {
		t.Parallel()
		dir := newRepo(t)
		pidfile := filepath.Join(t.TempDir(), "pid")
		alias(t, dir, "child", "sleep 60 & echo $! >"+shQuote(pidfile)+"; wait")
		_, err := newRunner(t).Run(t.Context(), domain.GitCmd{
			Dir: dir, Args: []string{"child"}, Timeout: 300 * time.Millisecond,
		})
		wantTimeout(t, err)
		wantGone(t, readPid(t, pidfile))
	})

	t.Run("cancellation stops git and its process group", func(t *testing.T) {
		t.Parallel()
		dir := newRepo(t)
		pidfile := filepath.Join(t.TempDir(), "pid")
		alias(t, dir, "child", "sleep 60 & echo $! >"+shQuote(pidfile)+"; wait")
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		_, err := newRunner(t).Run(ctx, domain.GitCmd{Dir: dir, Args: []string{"child"}})
		var gitErr *domain.GitError
		if !errors.As(err, &gitErr) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want a *domain.GitError carrying the caller's context error", err)
		}
		wantGone(t, readPid(t, pidfile))
	})

	t.Run("a helper outside the process group cannot hold Stream past the timeout", func(t *testing.T) {
		t.Parallel()
		dir := newRepo(t)
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		pidfile := filepath.Join(t.TempDir(), "pid")
		alias(t, dir, "escape", helperEnv+"=escape "+shQuote(self)+" "+shQuote(pidfile)+" & sleep 30")
		t.Cleanup(func() {
			if pid, err := os.ReadFile(pidfile); err == nil {
				if n, err := strconv.Atoi(string(pid)); err == nil {
					_ = syscall.Kill(n, syscall.SIGKILL)
				}
			}
		})
		start := time.Now()
		_, err = newRunner(t).Run(t.Context(), domain.GitCmd{
			Dir: dir, Args: []string{"escape"}, Timeout: 300 * time.Millisecond,
		})
		wantTimeout(t, err)
		// The timeout, then waitDelay (5 s) for the helper to let go of stdout.
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("Run returned after %v; want the timeout plus about 5 s", took)
		}
	})

	t.Run("sync helpers that ignore SIGTERM are killed once git is reaped", func(t *testing.T) {
		t.Parallel()
		dir := newRepo(t)
		pidfile := filepath.Join(t.TempDir(), "pid")
		alias(t, dir, "ignoreterm",
			"exec >/dev/null 2>&1; trap '' TERM; sleep 60 & echo $! >"+shQuote(pidfile)+"; wait")
		_, err := newRunner(t).Run(t.Context(), domain.GitCmd{
			Dir: dir, Args: []string{"ignoreterm"}, Timeout: 300 * time.Millisecond, Profile: domain.Sync,
		})
		wantTimeout(t, err)
		wantGone(t, readPid(t, pidfile))
	})

	t.Run("the user's and the system's attributes files do not apply", func(t *testing.T) {
		dir := newRepo(t)
		if err := os.Mkdir(filepath.Join(dir, "gen"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "gen", "x.go"), "package x\n")
		writeFile(t, filepath.Join(dir, ".gitattributes"), "gen/** linguist-generated\n")
		setup(t, dir, "add", "gen/x.go", ".gitattributes")
		setup(t, dir, "commit", "--quiet", "--message=three")
		bare := filepath.Join(t.TempDir(), "bare.git")
		setup(t, "", "clone", "--quiet", "--bare", dir, bare)

		// A per-user attributes file that would make a.txt binary and
		// generated on this machine only.
		xdg := t.TempDir()
		if err := os.Mkdir(filepath.Join(xdg, "git"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(xdg, "git", "attributes"), "*.txt -diff linguist-generated\n")
		t.Setenv("XDG_CONFIG_HOME", xdg)
		r := newRunner(t)

		numstat := run(t, r, dir, nil, "log", "-1", "--numstat", "--format=", "HEAD~1")
		if numstat != "1\t0\ta.txt\n" {
			t.Errorf("numstat = %q, want a.txt counted as text", numstat)
		}
		// The repository's own .gitattributes still applies, at the scanned
		// revision, in a work tree and in a bare clone.
		const want = "a.txt: linguist-generated: unspecified\ngen/x.go: linguist-generated: set\n"
		for _, repo := range []string{dir, bare} {
			if got := run(t, r, repo, nil, "check-attr", "--source=HEAD", "linguist-generated", "--", "a.txt", "gen/x.go"); got != want {
				t.Errorf("check-attr in %s = %q, want %q", filepath.Base(repo), got, want)
			}
		}
		if got := run(t, r, dir, []int{1}, "var", "GIT_ATTR_SYSTEM"); got != "" {
			t.Errorf("git var GIT_ATTR_SYSTEM = %q, want no system attributes file", got)
		}
	})

	t.Run("no askpass program runs, so nothing can prompt", func(t *testing.T) {
		dir := newRepo(t)
		marker := filepath.Join(t.TempDir(), "asked")
		askpass := writeProgram(t, marker)
		t.Setenv("SSH_ASKPASS", askpass)
		setup(t, dir, "config", "core.askPass", askpass)
		r := newRunner(t)
		fill := func(p domain.Profile) error {
			_, err := r.Run(t.Context(), domain.GitCmd{
				Dir: dir, Args: []string{"credential", "fill"}, Profile: p,
				Stdin: strings.NewReader("protocol=https\nhost=example.com\n\n"),
			})
			return err
		}
		if err := fill(domain.Analysis); err == nil || !strings.Contains(err.Error(), "terminal prompts disabled") {
			t.Errorf("Analysis credential fill: err = %v, want git to give up without prompting", err)
		}
		// Sync leaves GIT_ASKPASS to its credential mechanism; core.askPass
		// would come from a mirror's config, which only stillhere writes.
		setup(t, dir, "config", "--unset", "core.askPass")
		if err := fill(domain.Sync); err == nil || !strings.Contains(err.Error(), "terminal prompts disabled") {
			t.Errorf("Sync credential fill: err = %v, want git to give up without prompting", err)
		}
		wantNotRun(t, marker)
	})

	t.Run("repo-local signature and external diff programs never run", func(t *testing.T) {
		dir := newRepo(t)
		marker := filepath.Join(t.TempDir(), "ran")
		program := writeProgram(t, marker)
		setup(t, dir, "config", "log.showSignature", "true")
		setup(t, dir, "config", "gpg.program", program)
		setup(t, dir, "config", "diff.external", program)
		signed := signedCommit(t, dir)
		r := newRunner(t)
		run(t, r, dir, nil, "log", "-1", "--format=%H", signed)
		run(t, r, dir, nil, "show", "--format=%H", signed)
		run(t, r, dir, nil, "log", "-1", "-p", "--format=%H")
		run(t, r, dir, nil, "diff", "--no-ext-diff", "HEAD~1", "HEAD")
		// A git diff that forgets --no-ext-diff fails closed.
		if _, err := r.Run(t.Context(), domain.GitCmd{Dir: dir, Args: []string{"diff", "HEAD~1", "HEAD"}}); err == nil {
			t.Error("git diff without --no-ext-diff succeeded; want it to fail rather than run diff.external")
		}
		wantNotRun(t, marker)
	})

	t.Run("an inherited GIT_DIR cannot redirect a command", func(t *testing.T) {
		dir := newRepo(t)
		t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere.git"))
		out, err := newRunner(t).Run(t.Context(), domain.GitCmd{
			Dir: dir, Args: []string{"rev-parse", "--absolute-git-dir"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("git dir = %q, want %q", got, want)
		}
	})
}

// newRunner builds the runner after the test has set its environment: New
// reads the process environment once.
func newRunner(t *testing.T) *gitcli.Runner {
	t.Helper()
	r, err := gitcli.New(t.Context(), gitcli.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// newRepo creates a work tree with two commits that change a.txt.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	setup(t, "", "init", "--quiet", "--template=", "--initial-branch=main", dir)
	writeFile(t, filepath.Join(dir, "a.txt"), "a\nb\nc\n")
	setup(t, dir, "add", "a.txt")
	setup(t, dir, "commit", "--quiet", "--message=one")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\nb\nc\nd\n")
	setup(t, dir, "commit", "--quiet", "--all", "--message=two")
	return dir
}

// setup runs git directly, outside the runner under test, with neither the
// machine's configuration nor an inherited GIT_ variable.
func setup(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return setupStdin(t, dir, "", args...)
}

func setupStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Stdin = strings.NewReader(stdin)
	env := []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// run runs one command through the runner under test and returns its
// output, failing the test on an error.
func run(t *testing.T, r *gitcli.Runner, dir string, okExit []int, args ...string) string {
	t.Helper()
	out, err := r.Run(t.Context(), domain.GitCmd{Dir: dir, Args: args, OKExit: okExit})
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// signedCommit writes a copy of HEAD that carries a (bogus) signature, which
// is enough to make git run gpg.program when it shows signatures.
func signedCommit(t *testing.T, dir string) string {
	t.Helper()
	header, message, _ := strings.Cut(setup(t, dir, "cat-file", "commit", "HEAD"), "\n\n")
	signed := header + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n c2lnbmF0dXJl\n -----END PGP SIGNATURE-----\n\n" + message
	return strings.TrimSpace(setupStdin(t, dir, signed, "hash-object", "--literally", "-t", "commit", "-w", "--stdin"))
}

// writeProgram writes a program that records each run in marker and fails.
func writeProgram(t *testing.T, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program")
	script := "#!/bin/sh\necho \"$0 $*\" >>" + shQuote(marker) + "\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantNotRun(t *testing.T, marker string) {
	t.Helper()
	if runs, err := os.ReadFile(marker); err == nil {
		t.Errorf("a program named by the environment or the repository ran:\n%s", runs)
	}
}

// alias defines a repo-local shell alias: git runs command with sh, as a
// child in git's process group.
func alias(t *testing.T, dir, name, command string) {
	t.Helper()
	setup(t, dir, "config", "alias."+name, "!"+command)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// shQuote quotes s for sh.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// wantTimeout checks that err reports the per-command timeout as a
// *domain.GitError, whatever the reader made of the truncated output.
func wantTimeout(t *testing.T, err error) {
	t.Helper()
	var gitErr *domain.GitError
	if !errors.As(err, &gitErr) || gitErr.Exit != -1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a timeout *domain.GitError with Exit -1", err)
	}
}

// wantGone waits briefly for pid to be gone: killed, and reaped by init once
// its parent died with it.
func wantGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d outlived the git command that started it", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
