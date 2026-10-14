package fixture

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// builder writes one fixture repository with the git binary.
//
// It keeps the first error and turns every later step into a no-op, so a
// history reads as the table it implements rather than as a column of error
// checks: the sticky-error pattern of bufio.Writer, described in Rob Pike's
// "Errors are values" (The Go Blog, 2015). The caller checks err once, at
// the end.
type builder struct {
	ctx context.Context
	dir string
	env []string // the hermetic environment every git process starts from
	who []string // GIT_AUTHOR_* and GIT_COMMITTER_* of the current row
	err error    // the first failure; later steps do nothing
}

// person is a git identity exactly as a History row writes it.
type person struct{ name, email string }

// newBuilder prepares dir for a new repository. dir is created when missing
// and must otherwise be empty, so a mistaken path can never add commits to
// an existing repository.
func newBuilder(ctx context.Context, dir string) (*builder, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("%s is not empty", dir)
	}
	return &builder{ctx: ctx, dir: dir, env: hermeticEnv(os.Environ())}, nil
}

// hermeticEnv returns environ without any GIT_ variable, so nothing the
// caller exported (GIT_DIR, GIT_AUTHOR_NAME, GIT_CONFIG_PARAMETERS,
// GIT_TEMPLATE_DIR, ...) reaches git. It then adds the variables that stop
// git reading the machine's global and system configuration and the
// system-wide attributes file.
func hermeticEnv(environ []string) []string {
	env := make([]string, 0, len(environ)+3)
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
	)
}

// initRepo creates the repository on branch main. Without a template, no
// hook or info/exclude file from the git installation reaches the history.
// SHA-1 is the object format SPEC was measured with; pinning it keeps commit
// IDs stable when a newer git changes the default.
func (b *builder) initRepo() {
	b.git("init", "--quiet", "--template=", "--initial-branch=main", "--object-format=sha1")
}

// row starts one row of a History table. Until the next row, every git
// process runs as who, both author and committer, at when (recorded in UTC).
func (b *builder) row(when time.Time, who person) {
	date := strconv.FormatInt(when.Unix(), 10) + " +0000" // git's internal date format
	b.who = []string{
		"GIT_AUTHOR_NAME=" + who.name,
		"GIT_AUTHOR_EMAIL=" + who.email,
		"GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + who.name,
		"GIT_COMMITTER_EMAIL=" + who.email,
		"GIT_COMMITTER_DATE=" + date,
	}
}

// write creates or replaces the file at path, which is slash-separated and
// relative to the work tree, with content, and stages it.
func (b *builder) write(path, content string) {
	if b.err != nil {
		return
	}
	full := filepath.Join(b.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		b.err = fmt.Errorf("write %s: %w", path, err)
		return
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		b.err = fmt.Errorf("write %s: %w", path, err)
		return
	}
	b.git("add", "--", path)
}

// extend appends content to the file at path and stages it.
func (b *builder) extend(path, content string) {
	if b.err != nil {
		return
	}
	old, err := os.ReadFile(filepath.Join(b.dir, filepath.FromSlash(path)))
	if err != nil {
		b.err = fmt.Errorf("extend %s: %w", path, err)
		return
	}
	b.write(path, string(old)+content)
}

// commit records the staged changes with the one-line message msg.
func (b *builder) commit(msg string) {
	b.git("commit", "--quiet", "--message", msg)
}

// git runs one git command in the work tree, as the current row's identity.
func (b *builder) git(args ...string) {
	if b.err != nil {
		return
	}
	argv := slices.Concat([]string{
		// GIT_CONFIG_GLOBAL does not cover the per-user ignore and
		// attributes files under $XDG_CONFIG_HOME/git, which git reads by
		// default. An ignore rule would make "git add" refuse a fixture
		// path; an attribute could rewrite a blob as it is staged.
		"-c", "core.excludesFile=/dev/null",
		"-c", "core.attributesFile=/dev/null",
	}, args)
	cmd := exec.CommandContext(b.ctx, "git", argv...)
	cmd.Dir = b.dir
	cmd.Env = slices.Concat(b.env, b.who)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		b.err = fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			b.err = fmt.Errorf("%w: %s", b.err, msg)
		}
	}
}

// at is 2026-month-day at hour:00:00 UTC, the only year the fixture uses.
func at(month time.Month, day, hour int) time.Time {
	return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
}

// lines joins ls into file content: one line each, with a final newline.
func lines(ls ...string) string {
	return strings.Join(ls, "\n") + "\n"
}

// numbered returns n distinct non-blank lines, "<name>_1 = 1" to
// "<name>_<n> = <n>", for files whose History row states only a line count.
func numbered(name string, n int) string {
	var s strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&s, "%s_%d = %d\n", name, i, i)
	}
	return s.String()
}
