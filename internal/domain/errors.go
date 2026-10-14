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
