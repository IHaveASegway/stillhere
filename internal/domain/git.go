package domain

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"
)

// Profile selects how a git command is hardened
// (docs/spec/git-invocation.md).
type Profile int

// The git profiles. The zero value is Analysis, the offline one, so a
// command that forgets to choose cannot reach the network.
const (
	// Analysis is offline and hardened: no hooks, no fsmonitor, no external
	// diff or signature program, no system or global config or attributes,
	// no prompt or askpass program, no lazy fetch.
	Analysis Profile = iota
	// Sync is the only network profile: the same hardening, lazy fetch
	// allowed, credentials from GitCmd.Env. Only stillhere sync uses it, and
	// only on the mirrors it owns.
	Sync
)

// String returns "analysis" or "sync".
func (p Profile) String() string {
	switch p {
	case Analysis:
		return "analysis"
	case Sync:
		return "sync"
	default:
		return "Profile(" + strconv.Itoa(int(p)) + ")"
	}
}

// GitCmd is one git invocation. Call sites spell out the git arguments, so a
// reader and a test can see the exact command; the runner adds only the
// profile's common prefix and environment.
type GitCmd struct {
	Dir     string        // passed as -C
	Args    []string      // git arguments after the common prefix
	Stdin   io.Reader     // nil: an empty stdin; must not block, such as a bytes.Reader of NUL-separated paths
	Timeout time.Duration // 0: the profile's default
	OKExit  []int         // non-zero exits that are not errors (git grep exits 1 on no match)
	Env     []string      // Sync only: credential variables; never logged
	Profile Profile
}

// GitRunner is the port through which everything outside the gitcli adapter
// runs git.
type GitRunner interface {
	// Run returns git's stdout, buffered: for small outputs only.
	Run(ctx context.Context, cmd GitCmd) ([]byte, error)
	// Stream hands git's stdout to fn as it is produced, for large outputs
	// such as log -p and blame. When fn returns an error the runner stops
	// git; in every case git has exited before Stream returns, so no process
	// outlives the call.
	Stream(ctx context.Context, cmd GitCmd, fn func(io.Reader) error) error
}

// GitError is returned for an exit outside OKExit, a timeout, or a failed
// start.
type GitError struct {
	Args   []string // never contains credentials
	Exit   int      // -1 for a timeout or a failed start
	Stderr string   // redact.Message applied
	Err    error
}

// Error leads with the git subcommand and the cause, and leaves the full
// argument list to Args, so a message truncated for storage still says what
// failed: "git log: exit 128: fatal: bad object" or
// "git blame: timed out after 2m0s: context deadline exceeded".
func (e *GitError) Error() string {
	var b strings.Builder
	b.WriteString("git")
	if sub := GitSubcommand(e.Args); sub != "" {
		b.WriteString(" ")
		b.WriteString(sub)
	}
	switch {
	case e.Exit >= 0:
		b.WriteString(": exit ")
		b.WriteString(strconv.Itoa(e.Exit))
	case e.Err != nil:
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	if e.Stderr != "" {
		b.WriteString(": ")
		b.WriteString(e.Stderr)
	}
	return b.String()
}

// Unwrap returns the underlying cause, so errors.Is(err, context.Canceled)
// and errors.Is(err, context.DeadlineExceeded) see through a GitError.
func (e *GitError) Unwrap() error { return e.Err }

// GitSubcommand returns the git subcommand in a GitCmd's arguments: the
// first argument that is not an option, skipping the values of git's -c and
// -C options, so ["-c", "core.quotePath=false", "log"] gives "log". It
// returns "" when there is none. Every message about a git command names it
// this way.
func GitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}
