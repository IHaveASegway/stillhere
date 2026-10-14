package domain

import "errors"

// UsageError is an error the user can fix by changing how stillhere is
// invoked or configured: a bad flag, a missing path, an unreadable or
// incomplete policy, or a git older than the supported floor. The command
// line maps it to exit code 2.
type UsageError struct{ Msg string }

// Error returns the message, which names what to change.
func (e *UsageError) Error() string { return e.Msg }

// ErrNoFacts means no repository produced facts: every path failed, or none
// was a repository. The command line maps it to exit code 1.
var ErrNoFacts = errors.New("no repository produced facts")

// ErrNotFound means a requested record, such as a run id, does not exist.
var ErrNotFound = errors.New("not found")

// ErrNotImplemented marks code whose implementation has not landed yet: each
// stub of the walking skeleton returns it wrapped with the issue that builds
// it, and the command line maps it, and only it, to exit code 2. It is
// stillhere's own sentinel rather than errors.ErrUnsupported, which syscall
// errors such as ENOTSUP and ENOSYS also match. Delete it with the last stub
// (#13).
var ErrNotImplemented = errors.New("not implemented yet")
