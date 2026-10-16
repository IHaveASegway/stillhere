// Package gitcli is the outbound adapter that implements domain.GitRunner
// over the git binary.
//
// Every git process stillhere starts goes through Runner, so the hardening
// lives in one place (docs/spec/git-invocation.md): the common prefix that
// disables hooks, fsmonitor, external diff, signature verification and the
// per-user attributes file, and pins the repository settings no subcommand
// option can (which files git takes for binary, how trailers are parsed,
// how attribute patterns match case, and, on macOS, how path arguments are
// read); an environment stripped of inherited GIT_* and askpass variables
// and of system and global config and attributes; a profile that keeps
// analysis offline and prompt-free; a timeout that kills git and everything
// it started; and errors whose stderr has been scrubbed of credentials.
// Call sites still spell out their git arguments, options included, so the
// exact command stays visible where it is used.
package gitcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// DefaultTimeout bounds a git command whose GitCmd.Timeout is zero: the
// budget for one repository's log. Callers pass shorter limits where the
// spec sets them, such as 120 s for one blame.
const DefaultTimeout = 300 * time.Second

const (
	// waitDelay bounds how long Stream waits, after git exits or is killed,
	// for helpers it started to close the pipes they inherited.
	waitDelay = 5 * time.Second

	// versionTimeout bounds git version, which answers at once unless
	// something is wrong with the binary.
	versionTimeout = 10 * time.Second

	// stderrLimit caps the stderr kept per command. Only 180 runes survive
	// redact.Message, so this is generous, and it stops a runaway or
	// hostile git from growing memory without bound.
	stderrLimit = 8 << 10
)

// errAborted is the cancellation cause when the Stream callback returns an
// error and git must be stopped before it finishes.
var errAborted = errors.New("gitcli: output reader returned an error")

// Config configures a Runner.
type Config struct {
	Git         string       // binary; default "git", resolved on PATH once by New
	MailmapFile string       // org mailmap, passed as -c mailmap.file=; made absolute by New
	Logger      *slog.Logger // debug lines for each command; nil discards them
}

// Runner runs git with stillhere's hardening. It implements
// domain.GitRunner and is safe for concurrent use: every field is read-only
// after New.
type Runner struct {
	git          string   // absolute path of the git binary
	mailmap      string   // absolute path of the org mailmap, or ""
	version      string   // version token printed by git version, such as "2.50.1"
	major, minor int      // its first two numbers
	environ      []string // the process environment without inherited GIT_* and askpass variables
	log          *slog.Logger
}

var _ domain.GitRunner = (*Runner)(nil)

// Version returns the version git reported, such as "2.50.1" or
// "2.44.0.windows.1".
func (r *Runner) Version() string { return r.version }

// AtLeast reports whether the git binary is major.minor or newer: what a
// feature newer than the floor needs, such as version 2 changed-path
// filters (2.46) in a mirror's commit-graph (#56).
func (r *Runner) AtLeast(major, minor int) bool {
	return r.major > major || r.major == major && r.minor >= minor
}

// Run runs one git command and returns its stdout. It buffers the whole
// output, so it is for small outputs; use Stream for log -p and blame.
// On error it returns no output.
func (r *Runner) Run(ctx context.Context, cmd domain.GitCmd) ([]byte, error) {
	var out []byte
	err := r.Stream(ctx, cmd, func(stdout io.Reader) error {
		var err error
		out, err = io.ReadAll(stdout)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Stream runs one git command and hands its stdout to fn while git is still
// writing it. fn should read until EOF; anything it leaves unread is
// discarded so git can exit.
//
// Stream returns nil when fn returned nil and git exited with 0 or a code in
// cmd.OKExit. It returns a *domain.GitError when git failed, timed out, was
// cancelled through ctx, or could not start; errors.Is(err,
// context.Canceled) is true for a cancellation and errors.Is(err,
// context.DeadlineExceeded) for a timeout, even when the kill also made fn
// fail on a truncated stream. When fn returns an error first, Stream stops
// git, waits for it, and returns fn's error wrapped with git's scrubbed
// stderr. In every case git has exited, and been reaped, before Stream
// returns, and on Unix every process left in git's process group has been
// killed.
//
// A helper that leaves git's process group, such as a daemonising filter,
// cannot hold Stream past the timeout: once the command's context ends,
// Stream closes its end of stdout after waitDelay, so fn sees an error
// instead of blocking.
func (r *Runner) Stream(ctx context.Context, cmd domain.GitCmd, fn func(io.Reader) error) error {
	if fn == nil {
		return errors.New("gitcli: Stream needs a function to read stdout")
	}
	inv, err := r.invocation(cmd)
	if err != nil {
		return err
	}
	return r.exec(ctx, inv, fn)
}

// invocation is one fully resolved git command line.
type invocation struct {
	argv     []string // everything after the git binary
	env      []string
	stdin    io.Reader
	timeout  time.Duration
	okExit   []int
	graceful bool     // stop with SIGTERM, so git can remove its lock files
	dir      string   // for debug logs
	args     []string // the caller's arguments, scrubbed, for errors and debug logs
}

// invocation validates cmd and builds its command line and environment:
//
//	git -C <dir> --no-optional-locks -c core.hooksPath=/dev/null
//	    -c core.fsmonitor= -c core.attributesFile=/dev/null -c diff.external=
//	    -c log.showSignature=false -c core.bigFileThreshold=512m
//	    -c diff.default.binary=auto -c core.commentChar=#
//	    -c trailer.separators=: -c core.ignoreCase=false
//	    -c core.precomposeUnicode=false [-c mailmap.file=<org-mailmap>] <args>
//
// Command-line -c beats repo-local config. core.attributesFile=/dev/null
// drops only the per-user attributes file: the repository's .gitattributes
// and info/attributes still apply, so the linguist checks keep working.
// diff.external= does not switch external diff off: git would try to run an
// empty command and fail, so a git diff that forgets --no-ext-diff fails
// closed instead of running a program the repository names.
// log.showSignature=false keeps log and show from running gpg.program.
//
// The next six are git's defaults, pinned here because no subcommand
// option overrides them and a repository's own value changes facts (each
// verified on git 2.50.1 with the value in the repository's config). Above
// core.bigFileThreshold every blob is binary, so numstat counts no lines and
// a patch has no hunks; diff.default.binary=true does the same to every
// file no diff attribute names, while blame still counts every line of it.
// git's trailer parser skips comment lines, so core.commentChar=C (or
// core.commentString, the same setting) hides every Co-authored-by line,
// and without ":" in trailer.separators no line is a trailer.
// core.ignoreCase=true, which git on macOS and Windows writes into every
// repository it creates or clones, matches attribute patterns in any case:
// "*.PY linguist-generated" marked a.py, which a Linux clone, like GitHub,
// does not. core.precomposeUnicode=true, which git on macOS writes too,
// rewrites a decomposed path argument in composed form, so blame found no
// such path for a file whose name the tree stores decomposed, which git on
// Linux blames. Every command gets them (classification, the history and
// co-author logs, blame, and any command added later), and none changes
// the output of a command that does not read it.
// Settings that a subcommand option pins, such as rename detection
// or submodule diffs, are pinned by that option where the command is built,
// because an option also beats the more specific config a -c cannot reach
// (submodule.<name>.ignore beats -c diff.ignoreSubmodules=none, while
// --ignore-submodules=none beats both).
//
// Malformed commands are programmer errors, returned as plain errors.
func (r *Runner) invocation(cmd domain.GitCmd) (invocation, error) {
	switch {
	case cmd.Dir == "":
		return invocation{}, errors.New("gitcli: GitCmd.Dir is required")
	case len(cmd.Args) == 0:
		return invocation{}, errors.New("gitcli: GitCmd.Args is empty")
	case cmd.Profile != domain.Analysis && cmd.Profile != domain.Sync:
		return invocation{}, fmt.Errorf("gitcli: unknown profile %v", cmd.Profile)
	case cmd.Profile == domain.Analysis && len(cmd.Env) > 0:
		// Analysis must never authenticate to anything.
		return invocation{}, errors.New("gitcli: GitCmd.Env is allowed on the Sync profile only")
	}

	argv := []string{
		"-C", cmd.Dir,
		"--no-optional-locks",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "diff.external=",
		"-c", "log.showSignature=false",
		"-c", "core.bigFileThreshold=512m",
		"-c", "diff.default.binary=auto",
		"-c", "core.commentChar=#",
		"-c", "trailer.separators=:",
		"-c", "core.ignoreCase=false",
		"-c", "core.precomposeUnicode=false",
	}
	if r.mailmap != "" {
		argv = append(argv, "-c", "mailmap.file="+r.mailmap)
	}
	argv = append(argv, cmd.Args...)

	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return invocation{
		argv:     argv,
		env:      r.env(cmd.Profile, cmd.Env),
		stdin:    cmd.Stdin,
		timeout:  timeout,
		okExit:   cmd.OKExit,
		graceful: cmd.Profile == domain.Sync,
		dir:      cmd.Dir,
		args:     scrubArgs(cmd.Args),
	}, nil
}

// env returns the environment for one command: the process environment
// without inherited GIT_* and askpass variables, then the Sync profile's
// credential variables, then the hardening. os/exec keeps the last value of
// a duplicated key, so credential variables can never undo the hardening.
func (r *Runner) env(p domain.Profile, credentials []string) []string {
	hardening := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		// The system attributes file is not config, so GIT_CONFIG_NOSYSTEM
		// leaves it in force; it would change numstat and the linguist
		// checks from one machine to the next.
		"GIT_ATTR_NOSYSTEM=1",
	}
	if p == domain.Analysis {
		hardening = append(hardening,
			// Analysis never touches the network. GIT_NO_LAZY_FETCH (git
			// 2.45+) makes a partial clone's missing blobs read as missing
			// instead of fetched from its promisor remote.
			"GIT_NO_LAZY_FETCH=1",
			// Defence in depth: an allow list that names no real protocol
			// refuses every transport (file, git, ssh, http, https, ext and
			// remote helpers), whatever the repository's own config says
			// (protocol.<name>.allow), on any git since 2.12. A command that
			// would fetch, through a lazy fetch or anything git adds later,
			// fails instead of connecting.
			"GIT_ALLOW_PROTOCOL=none",
			// GIT_TERMINAL_PROMPT=0 covers only the terminal. git asks
			// GIT_ASKPASS, then core.askPass, then SSH_ASKPASS first; an
			// empty GIT_ASKPASS ends that search, so no program prompts.
			// Sync keeps the variable free for the credential mechanism
			// M2 chooses.
			"GIT_ASKPASS=",
		)
	}
	return slices.Concat(r.environ, credentials, hardening)
}

// exec starts git, streams its stdout to fn, and classifies the outcome.
func (r *Runner) exec(ctx context.Context, inv invocation, fn func(io.Reader) error) error {
	started := time.Now()

	// Two cancellation sources besides ctx: abort, when fn fails, and the
	// per-command timeout. Context causes tell them apart afterwards.
	actx, abort := context.WithCancelCause(ctx)
	defer abort(nil)
	tctx, stop := context.WithTimeoutCause(actx, inv.timeout,
		fmt.Errorf("timed out after %s: %w", inv.timeout, context.DeadlineExceeded))
	defer stop()

	cmd := exec.CommandContext(tctx, r.git, inv.argv...)
	cmd.Env = inv.env
	cmd.Stdin = inv.stdin
	stderr := &stderrBuffer{}
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	stopTree(cmd, inv.graceful)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &domain.GitError{Args: inv.args, Exit: -1, Err: err}
	}
	if err := cmd.Start(); err != nil {
		r.debug(ctx, inv, started, -1)
		return &domain.GitError{Args: inv.args, Exit: -1, Err: err}
	}

	// os/exec's WaitDelay closes stdout only while one of its own copy
	// goroutines is still running. A helper that left git's process group
	// (so the group kill missed it) and closed stderr but kept stdout open
	// would block fn, and the drain below, forever (go.dev/issue/23019).
	// release stops that watch once git has been reaped.
	release := closeAfter(tctx, waitDelay, stdout)
	reap := func() error {
		err := cmd.Wait()
		killGroup(cmd) // helpers git left behind in its process group
		release()
		return err
	}
	reaped := false
	defer func() {
		if !reaped {
			// fn panicked: stop git and reap it before the panic continues.
			abort(errAborted)
			_, _ = io.Copy(io.Discard, stdout)
			_ = reap()
		}
	}()
	fnErr := fn(stdout)
	if fnErr != nil {
		abort(errAborted)
	}
	// Drain what fn left, so git is never blocked on a full pipe and Wait
	// never races a reader. After a kill this ends at EOF at once.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := reap()
	reaped = true

	r.debug(ctx, inv, started, exitCode(waitErr))
	return inv.outcome(ctx, tctx, fnErr, waitErr, redact.Message(stderr.String()))
}

// closeAfter closes c once ctx has been done for delay. The returned release
// function cancels that and returns only after the watching goroutine has
// exited.
func closeAfter(ctx context.Context, delay time.Duration, c io.Closer) (release func()) {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = c.Close() // fn's next Read fails; Wait's own Close then fails harmlessly
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// outcome decides what Stream returns once git has been reaped. The order
// matters: a cancellation or a timeout explains a truncated output, so it
// wins over fn's error, and fn's error wins over git's own exit status,
// because a killed git always exits with a failure.
//
// The timeout is read from the timer itself: tctx.Err() is DeadlineExceeded
// only when the timer fired before an abort, while an abort after the kill,
// because fn failed on the truncated stream, leaves it unchanged.
func (inv invocation) outcome(ctx, tctx context.Context, fnErr, waitErr error, stderr string) error {
	if fnErr == nil && waitErr == nil {
		return nil
	}
	fail := func(exit int, cause error) error {
		return &domain.GitError{Args: inv.args, Exit: exit, Stderr: stderr, Err: cause}
	}
	switch {
	case ctx.Err() != nil:
		return fail(-1, ctx.Err())
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		return fail(-1, context.Cause(tctx))
	case fnErr != nil:
		if stderr != "" {
			return fmt.Errorf("%s: reading output: %w (stderr: %s)", gitName(inv.args), fnErr, stderr)
		}
		return fmt.Errorf("%s: reading output: %w", gitName(inv.args), fnErr)
	}

	var exitErr *exec.ExitError
	switch {
	case errors.As(waitErr, &exitErr) && exitErr.Exited():
		if slices.Contains(inv.okExit, exitErr.ExitCode()) {
			return nil
		}
		return fail(exitErr.ExitCode(), waitErr)
	case errors.Is(waitErr, exec.ErrWaitDelay):
		// git exited 0 and its stdout reached EOF; only a helper it started
		// kept stderr open past waitDelay. The output is complete.
		return nil
	default:
		// Killed by a signal stillhere did not send, or an I/O failure.
		return fail(-1, waitErr)
	}
}

func (r *Runner) debug(ctx context.Context, inv invocation, started time.Time, exit int) {
	if !r.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	// Arguments and timing only: the environment may hold credentials and
	// stdin may hold paths in bulk, so neither is ever logged. The directory
	// is a PATH the user typed, or a mirror under one, scrubbed like the
	// arguments (#71).
	r.log.LogAttrs(ctx, slog.LevelDebug, "git",
		slog.String("dir", redact.Scrub(inv.dir)),
		slog.Any("args", inv.args),
		slog.Duration("took", time.Since(started)),
		slog.Int("exit", exit),
	)
}

// exitCode returns git's exit status for logging: 0 on success, -1 when it
// did not exit normally.
func exitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// gitName names a command in messages the way domain.GitError does: "git
// log", or "git" when the arguments hold no subcommand.
func gitName(args []string) string {
	if sub := domain.GitSubcommand(args); sub != "" {
		return "git " + sub
	}
	return "git"
}

// scrubArgs returns a copy of args that is safe to log and to store in a
// GitError. Credentials never belong in arguments (they travel in
// GitCmd.Env), so this is defence in depth for any URL or scp-like remote an
// argument carries. redact.Scrub changes nothing but the credential, so the
// copy still shows the command that ran: no truncation, no whitespace
// folding, and arguments without a URL or an @ are left byte for byte.
func scrubArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, "://") || strings.Contains(a, "@") {
			a = redact.Scrub(a)
		}
		out[i] = a
	}
	return out
}

// inheritable returns environ without the variables git reads (GIT_*) and
// without the askpass programs ssh and git fall back to (SSH_ASKPASS,
// SSH_ASKPASS_REQUIRE). stillhere may itself run inside a git hook, where
// an inherited GIT_DIR or GIT_INDEX_FILE would override -C and point every
// command at the wrong repository; and a desktop session or an IDE terminal
// often exports an askpass program that would pop up a prompt.
func inheritable(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if dropped(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func dropped(name string) bool {
	if runtime.GOOS == "windows" {
		// Environment variable names are case-insensitive on Windows.
		name = strings.ToUpper(name)
	}
	return strings.HasPrefix(name, "GIT_") || name == "SSH_ASKPASS" || name == "SSH_ASKPASS_REQUIRE"
}

// stderrBuffer keeps the first stderrLimit bytes git writes to stderr and
// counts the rest as dropped. os/exec writes to it from one goroutine and
// Wait returns only after that goroutine is done, so it needs no lock.
type stderrBuffer struct {
	buf       []byte
	truncated bool
}

func (b *stderrBuffer) Write(p []byte) (int, error) {
	room := stderrLimit - len(b.buf)
	if len(p) > room {
		b.buf = append(b.buf, p[:max(room, 0)]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// String returns the kept bytes. When some were dropped it also drops the
// last, possibly cut, word: a credential cut in half no longer looks like a
// URL to the scrubber, so it must not be returned at all.
func (b *stderrBuffer) String() string {
	if !b.truncated {
		return string(b.buf)
	}
	i := strings.LastIndexAny(string(b.buf), " \t\r\n")
	if i < 0 {
		return ""
	}
	return string(b.buf[:i])
}

// absMailmap makes the org mailmap path absolute: git runs with -C in each
// repository, which would otherwise resolve a relative path there.
func absMailmap(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("gitcli: mailmap %s: %w", path, err)
	}
	return abs, nil
}
