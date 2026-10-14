package gitcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The version floor (docs/spec/git-invocation.md#version-floors). Git 2.44
// brings GIT_NO_LAZY_FETCH, the no-network guarantee, and covers
// check-attr --source (2.40) and --diff-merges (2.31).
const (
	floorMajor = 2
	floorMinor = 44
	floor      = "2.44"
)

// New resolves the git binary once, runs git version, and returns a Runner.
// A missing git, an unreadable version, or a git older than 2.44 is a
// *domain.UsageError that names the floor, because the user fixes it by
// installing a newer git.
func New(ctx context.Context, cfg Config) (*Runner, error) {
	name := cfg.Git
	if name == "" {
		name = "git"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"git not found (%v): install git %s or newer", err, floor)}
	}
	// LookPath returns a name that contains a separator unchanged, so a
	// relative Config.Git would otherwise resolve against whatever directory
	// a later process starts in.
	if path, err = filepath.Abs(path); err != nil {
		return nil, fmt.Errorf("gitcli: git binary %s: %w", name, err)
	}
	mailmap, err := absMailmap(cfg.MailmapFile)
	if err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Runner{
		git:     path,
		mailmap: mailmap,
		environ: inheritable(os.Environ()),
		log:     log,
	}

	// git version runs outside any repository, so it skips the -C prefix
	// but keeps the hardened environment.
	var out []byte
	err = r.exec(ctx, invocation{
		argv:    []string{"version"},
		env:     r.env(domain.Analysis, nil),
		timeout: versionTimeout,
		dir:     ".",
		args:    []string{"version"},
	}, func(stdout io.Reader) error {
		var err error
		out, err = io.ReadAll(stdout)
		return err
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return nil, &domain.UsageError{Msg: fmt.Sprintf(
				"cannot run %s (%v): install git %s or newer", path, err, floor)}
		}
		return nil, fmt.Errorf("gitcli: %w", err)
	}

	version, major, minor, ok := parseVersion(string(out))
	if !ok {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"cannot read the version of %s from %q: install git %s or newer",
			path, firstLine(string(out)), floor)}
	}
	if major < floorMajor || major == floorMajor && minor < floorMinor {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"git %s is older than %s, the oldest version stillhere supports: install git %s or newer",
			version, floor, floor)}
	}
	r.version = version
	return r, nil
}

// parseVersion reads the output of git version, such as
// "git version 2.50.1 (Apple Git-155)" or "git version 2.44.0.windows.1",
// and returns the version token ("2.50.1", "2.44.0.windows.1") with its
// major and minor numbers.
func parseVersion(out string) (version string, major, minor int, ok bool) {
	rest, found := strings.CutPrefix(firstLine(out), "git version ")
	if !found {
		return "", 0, 0, false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, 0, false
	}
	version = fields[0]
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return "", 0, 0, false
	}
	major, okMajor := leadingInt(parts[0])
	minor, okMinor := leadingInt(parts[1])
	if !okMajor || !okMinor {
		return "", 0, 0, false
	}
	return version, major, minor, true
}

// leadingInt parses the decimal digits at the start of s, so "0-rc1" is 0.
func leadingInt(s string) (int, bool) {
	n, digits := 0, 0
	for _, c := range s {
		if c < '0' || c > '9' || digits == 9 {
			break
		}
		n = n*10 + int(c-'0')
		digits++
	}
	return n, digits > 0
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
