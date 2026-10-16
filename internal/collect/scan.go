package collect

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// This file holds what every collector (#4, #5, #6, #7) needs to read git's
// output and count its lines, so the four do not each write their own, and
// what the history and blame collectors share: the path unquoting and the
// attribute source. A collector's other unexported identifiers carry its
// prefix (discover, classify, history, blame), so the four can be written
// in parallel without colliding (docs/spec/codebase.md#working-in-parallel).

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
// the lines blame keeps by the lines a commit's own patch added, so both
// collectors count with this one function: historyParsePatch for the
// patch's added and removed lines, blameParse for the surviving ones. Both
// hand it the line's bytes after git's one-byte marker (the patch's "+" or
// "-", the porcelain's tab).
func nonBlank(line []byte) bool {
	return len(bytes.TrimSpace(line)) > 0
}

// nonBlankPieces tells nonBlank's answer for a line read in pieces, which
// the own-patch reader uses for a hunk line it does not hold whole (#73).
// A line is non-blank when some rune of it is outside White_Space, decoding
// it as one stream (an invalid byte is U+FFFD, which is not white space),
// so the answer for the line is the answer for any of its pieces as long
// as each piece ends where a rune starts: a byte that starts a rune
// (utf8.RuneStart) is never inside another rune, valid or not, so decoding
// the whole line puts a boundary there too. write holds back the bytes of
// a rune a piece cuts short and judges them with the next piece.
type nonBlankPieces struct {
	carry []byte // the start of a rune the last piece cut short
	found bool   // a rune outside White_Space was seen
}

// write judges the next piece of the line.
func (n *nonBlankPieces) write(p []byte) {
	if n.found {
		return
	}
	if len(n.carry) > 0 {
		p = append(n.carry, p...)
		n.carry = n.carry[:0]
	}
	end := len(p)
	for i := len(p) - 1; i >= 0 && i >= len(p)-utf8.UTFMax; i-- {
		if utf8.RuneStart(p[i]) {
			if !utf8.FullRune(p[i:]) {
				end = i
			}
			break
		}
	}
	if nonBlank(p[:end]) {
		n.found = true
		return
	}
	n.carry = append(n.carry, p[end:]...)
}

// done reports whether the line was non-blank, once every piece is
// written: a rune still cut short is invalid, U+FFFD, and so non-blank.
func (n *nonBlankPieces) done() bool { return n.found || len(n.carry) > 0 }

// unquotePath undoes git's quote_c_style, with which a path that holds a
// quote, a backslash, a control character or (unless core.quotePath is
// false) a non-ASCII byte is printed where git writes paths one per line:
// a patch's "---" and "+++" labels (historyLabel) and blame's porcelain
// "filename" (blameParse). It removes the surrounding quotes and undoes
// the escapes \a \b \t \n \v \f \r \" \\ and three-digit octal escapes,
// which stand for single bytes, so a path that is not valid UTF-8 survives
// byte for byte, as the -z output the collectors read elsewhere gives it.
// Raw bytes pass through, which is how git prints non-ASCII when
// core.quotePath is false.
func unquotePath(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", errors.New("malformed quoted path")
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			return "", errors.New("malformed quoted path")
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", errors.New("malformed quoted path")
		}
		switch e := s[i]; e {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'v':
			b.WriteByte('\v')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\':
			b.WriteByte(e)
		case '0', '1', '2', '3':
			if i+2 >= len(s) || !isOctal(s[i+1]) || !isOctal(s[i+2]) {
				return "", errors.New("malformed octal escape in quoted path")
			}
			b.WriteByte((e-'0')<<6 | (s[i+1]-'0')<<3 | (s[i+2] - '0'))
			i += 2
		default:
			return "", errors.New("unknown escape in quoted path")
		}
	}
	return b.String(), nil
}

// isOctal reports whether c is an octal digit.
func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// The empty tree of each object format. git holds it as an object of every
// repository, whether or not the repository stores it (verified on git
// 2.50.1 in SHA-1 and SHA-256 repositories that do not).
const (
	emptyTreeSHA1   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	emptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

// attrSource is the global option, ahead of the subcommand, with which every
// history log that can print a diff, and blame, read no .gitattributes file:
// the attribute source is the empty tree of head's object format (a 64-hex
// head is SHA-256). In these commands attributes decide one thing, through
// the diff attribute: whether git counts a file's lines at all. Blame
// attributes every line of a file whatever that attribute says, so a log
// that honoured a committed -diff would leave out of the added lines what
// blame counts as surviving: durable@1 gave Ann 22 surviving lines over 2
// added, where a clone that reads no attributes gives 22 over 22
// (verified). Which files count is classification's decision: it reads
// linguist-generated and linguist-vendored at head (check-attr --source),
// and its header sniff reads no attribute either (git grep -a).
//
// Without a source, git reads attributes from wherever it finds them. A
// work tree reads its checked-out files, uncommitted edits included; a bare
// clone reads HEAD's tree on git 2.43.0 to 2.45.2, CI's floor among them,
// and none from 2.45.3 and 2.46.0 on; and a repository's attr.tree config
// names any tree, in a bare clone too. So a file marked -diff gave numstat
// "-\t-" in a work tree and "1\t0" in a bare clone of the same commit, and
// blame stopped following a rename that also turned CRLF into LF (verified
// on git 2.50.1). What still applies is $GIT_DIR/info/attributes, which
// belongs to the repository rather than to a checkout and which no option
// turns off, its patterns matched case-sensitively on every machine
// because the runner pins core.ignoreCase=false; and git's own content
// check, which takes a file with a NUL byte in its first 8,000 bytes for
// binary.
//
// It costs almost nothing. Over a year of Homebrew's brew (2,459 landings,
// 3,696 non-merge commits; a bare clone, git 2.50.1, best of three), the
// landing log took 1.34 s with no source, 1.39 s with the empty tree and
// 1.87 s with head's tree, and the own-patch log 1.56 s, 1.62 s and 2.25 s
// (docs/spec/git-invocation.md). --attr-source arrived in git 2.41, below
// the 2.45 floor, and domain.GitSubcommand skips it.
func attrSource(head string) string {
	if len(head) == 64 {
		return "--attr-source=" + emptyTreeSHA256
	}
	return "--attr-source=" + emptyTreeSHA1
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
