package collect

import (
	"bufio"
	"bytes"
	"io"
)

// This file holds what every collector (#4, #5, #6, #7) needs to read git's
// output and count its lines, so the four do not each write their own, and
// the option with which the history and blame collectors read attributes
// at the scanned commit. A collector's other unexported identifiers carry
// its prefix (discover, classify, history, blame), so the four can be
// written in parallel without colliding
// (docs/spec/codebase.md#working-in-parallel).

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

// attrSource is the global option, ahead of the subcommand, with which a
// history log or a blame reads .gitattributes from head's tree: the
// scanned commit's attributes, which classification reads too (check-attr
// --source). Without it git reads them from wherever it finds them. A work
// tree reads its checked-out files, uncommitted edits included; a bare
// clone reads HEAD's tree on git 2.43.0 to 2.45.2, CI's floor among them,
// and none from 2.45.3 and 2.46.0 on; and a repository's attr.tree config
// can name any tree. The diff
// attribute decides whether a file is binary, so a file marked -diff gave
// numstat "-\t-" in a work tree and "1\t0" in a bare clone of the same
// commit, an uncommitted "-diff" line changed the work tree's count, and
// blame stopped following a rename there (verified on git 2.50.1).
// $GIT_DIR/info/attributes, which belongs to the repository rather than to
// a checkout, still applies. git reads the source only when a command first
// asks for an attribute, so a log that prints no diff, as in a partial
// clone, reads nothing from it.
//
// The source is head's tree, not head: git looks each .gitattributes up in
// the source anew, and from a commit it first reads the commit. Over a year
// of Homebrew's brew (2,459 landings, 3,696 non-merge commits; git 2.50.1,
// best of three), the landing log took 1.49 s with no source, 2.28 s with
// head and 2.01 s with head^{tree}, and the own-patch log 1.83 s, 2.79 s
// and 2.48 s (docs/spec/git-invocation.md). --attr-source arrived in git
// 2.41, below the 2.45 floor, and domain.GitSubcommand skips it.
func attrSource(head string) string {
	return "--attr-source=" + head + "^{tree}"
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
