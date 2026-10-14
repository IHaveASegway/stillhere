package collect

import (
	"bufio"
	"bytes"
	"io"
)

// This file holds what every collector (#4, #5, #6, #7) needs to read git's
// output and count its lines, so the four do not each write their own. A
// collector's other unexported identifiers carry its prefix (discover,
// classify, history, blame), so the four can be written in parallel without
// colliding (docs/spec/codebase.md#working-in-parallel).

// maxToken bounds one line, or one NUL-terminated token, of git output.
// Generated files have megabyte lines (docs/spec/codebase.md#concurrency);
// the buffer grows only as far as the output needs.
const maxToken = 64 << 20

// newScanner returns a scanner over git output that splits with split
// (bufio.ScanLines, or scanNUL for NUL-separated output) and allows tokens
// of up to maxToken bytes.
func newScanner(r io.Reader, split bufio.SplitFunc) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64<<10), maxToken)
	s.Split(split)
	return s
}

// nonBlank reports whether a line of content counts in a non-blank line
// count: whether it holds a rune outside Unicode's White_Space property
// (unicode.IsSpace), so that something is left after bytes.TrimSpace
// (docs/metrics.md#noise-applied-before-every-metric). durable@1 divides
// the lines blame keeps by the lines a commit's own patch added, so the
// blame and history collectors must count with this one predicate.
func nonBlank(line []byte) bool {
	return len(bytes.TrimSpace(line)) > 0
}

// scanNUL is a bufio.SplitFunc for NUL-separated output: check-attr -z,
// ls-tree -z, and the %x00 that starts each record of the history log. Like
// bufio.ScanLines, it also returns a final token that has no terminator,
// which is how the last record of that log ends.
func scanNUL(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
