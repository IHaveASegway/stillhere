package collect

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// blameCommit is what one file's porcelain says about one commit: its
// author, and how many non-blank lines of the file blame gives it from each
// path the file had in it.
type blameCommit struct {
	name, email string
	time        int64 // author-time, in Unix seconds
	seen        blameSeen
	from        *blameFrom   // the path the commit's last "filename" gave, which its records' lines come from; nil before one
	froms       []*blameFrom // every path the commit's records gave, in order of first appearance; almost always one
}

// blameFrom counts the non-blank lines blame gives one commit from one path
// the file had in that commit: blame's origin, the (commit, path) pair it
// passes lines along.
type blameFrom struct {
	path  string // unquoted
	lines int
}

// blameSeen records which author keys a commit's porcelain has given.
type blameSeen uint8

const (
	blameSeenName blameSeen = 1 << iota
	blameSeenMail
	blameSeenTime
	blameSeenAuthor = blameSeenName | blameSeenMail | blameSeenTime
)

// blameParse reads the porcelain git blame printed for path and returns one
// LineOwner per commit and path in it (OrigPath) that blame gives at least
// one non-blank line, sorted by Commit, then OrigPath; Blame fills
// CoAuthors. It reads --line-porcelain, which Blame runs, and --porcelain
// alike (docs/spec/blame-collector.md#porcelain-parsing).
//
// Both forms are a sequence of records, one per line of the file: a header,
// key lines, then the line's content.
//
//   - The header is "<sha> <orig-line> <final-line>", with a fourth field,
//     the group size, only on the first line of a group. The group size is
//     ignored: a group can hold blank lines, so multiplying by it
//     overcounts.
//   - --line-porcelain repeats a commit's author keys in every record;
//     --porcelain prints them only the first time the commit appears. They
//     are kept per commit, never carried over from the previous record: in
//     --porcelain, a commit that comes back after another commit's lines has
//     no author keys, and the previous record's would be the other commit's.
//   - "filename" names the path the record's line had in its commit, quoted
//     as git quotes paths (unquotePath). --line-porcelain prints it in every
//     record; --porcelain prints it the first time the commit appears, and
//     at the start of each of its groups when blame reached the commit
//     through more than one path (git's MORE_THAN_ONE_PATH, which a merge of
//     two renames of the file gives). So the commit's last filename is the
//     path of its record's line, in both forms.
//   - Other keys are skipped: committer, summary, previous, boundary, and
//     the "ignored" and "unblamable" marks that a repository's own
//     blame.markIgnoredLines and blame.markUnblamableLines add.
//   - The content is the record's line after a tab. It adds one line to the
//     record's commit, from the record's path, when it is non-blank
//     (nonBlank, scan.go: a rune outside Unicode's White_Space,
//     docs/metrics.md), the predicate the history collector counts added
//     lines with.
//
// author-mail is already mailmapped by git; its angle brackets are removed
// and it is lowercased, the canonical key. Output that breaks this grammar
// is an error rather than lines credited to the wrong record, so a file's
// facts are never half-read. No message quotes the file's content, which
// may be anything.
func blameParse(r io.Reader, path string) ([]domain.LineOwner, error) {
	commits := make(map[string]*blameCommit)
	var cur *blameCommit // the open record's commit: after its header, before its content
	sc := newScanner(r, bufio.ScanLines)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Bytes()
		switch {
		case len(line) > 0 && line[0] == '\t':
			if cur == nil {
				return nil, fmt.Errorf("porcelain line %d: content without a record header", n)
			}
			if cur.seen != blameSeenAuthor {
				return nil, fmt.Errorf("porcelain line %d: content before its commit's author keys", n)
			}
			if cur.from == nil {
				return nil, fmt.Errorf("porcelain line %d: content before its commit's filename", n)
			}
			if nonBlank(line[1:]) {
				cur.from.lines++
			}
			cur = nil
		case cur == nil:
			sha := blameHeader(line)
			if sha == nil {
				return nil, fmt.Errorf("porcelain line %d: not a record header", n)
			}
			// Indexing with string(sha) does not allocate; only a new
			// commit's key does.
			if cur = commits[string(sha)]; cur == nil {
				cur = &blameCommit{}
				commits[string(sha)] = cur
			}
		default:
			if err := cur.key(line); err != nil {
				return nil, fmt.Errorf("porcelain line %d: %w", n, err)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("porcelain: %w", err)
	}
	if cur != nil {
		return nil, fmt.Errorf("porcelain line %d: the last record has no content", n)
	}

	var owners []domain.LineOwner
	for sha, c := range commits {
		for _, f := range c.froms {
			if f.lines == 0 {
				continue // only blank lines survive from this commit and path
			}
			o := domain.LineOwner{
				Path:       path,
				Commit:     sha,
				Author:     domain.Person{Name: c.name, Email: c.email},
				AuthorTime: time.Unix(c.time, 0).UTC(),
				Lines:      f.lines,
			}
			if f.path != path {
				o.OrigPath = f.path
			}
			owners = append(owners, o)
		}
	}
	slices.SortFunc(owners, func(a, b domain.LineOwner) int {
		return cmp.Or(strings.Compare(a.Commit, b.Commit), strings.Compare(a.OrigPath, b.OrigPath))
	})
	return owners, nil
}

// key applies one key line of a record to its commit. git prints the same
// values each time it repeats a commit's author keys, so only the first of
// each is read: --line-porcelain repeats them for every line of the file.
// A filename is read every time, because it names the path of the lines
// that follow it, which can change between a commit's groups.
func (c *blameCommit) key(line []byte) error {
	key, value, _ := bytes.Cut(line, []byte{' '})
	switch string(key) {
	case "filename":
		if c.from != nil && string(value) == c.from.path {
			return nil // the common case: the commit's path again, unquoted
		}
		p := string(value)
		if len(p) > 0 && p[0] == '"' {
			var err error
			if p, err = unquotePath(p); err != nil {
				return fmt.Errorf("filename: %w", err)
			}
		}
		i := slices.IndexFunc(c.froms, func(f *blameFrom) bool { return f.path == p })
		if i < 0 {
			c.froms = append(c.froms, &blameFrom{path: p})
			i = len(c.froms) - 1
		}
		c.from = c.froms[i]
	case "author":
		if c.seen&blameSeenName == 0 {
			c.name = string(value)
			c.seen |= blameSeenName
		}
	case "author-mail":
		if c.seen&blameSeenMail == 0 {
			email := bytes.TrimSuffix(bytes.TrimPrefix(value, []byte("<")), []byte(">"))
			c.email = strings.ToLower(string(email))
			c.seen |= blameSeenMail
		}
	case "author-time":
		if c.seen&blameSeenTime == 0 {
			t, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil {
				return errors.New("author-time is not a number of seconds")
			}
			c.time = t
			c.seen |= blameSeenTime
		}
	}
	return nil
}

// blameHeader returns the commit ID that starts a porcelain record header,
// one that matches ^([0-9a-f]{7,64}) (\d+) (\d+)( (\d+))?$, or nil when
// line is not one. It is written out rather than as a regular expression
// because it runs once for every line of every blamed file.
func blameHeader(line []byte) []byte {
	n := 0
	for n < len(line) && blameIsHex(line[n]) {
		n++
	}
	if n < 7 || n > 64 {
		return nil
	}
	fields := 0
	for rest := line[n:]; len(rest) > 0; fields++ {
		if rest[0] != ' ' {
			return nil
		}
		d := 1
		for d < len(rest) && '0' <= rest[d] && rest[d] <= '9' {
			d++
		}
		if d == 1 {
			return nil
		}
		rest = rest[d:]
	}
	if fields != 2 && fields != 3 {
		return nil
	}
	return line[:n]
}

// blameIsHex reports whether c is a lowercase hexadecimal digit, as git
// prints object IDs.
func blameIsHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f'
}
