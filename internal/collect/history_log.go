package collect

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file reads the logs printed with -z and historyLogFormat: the
// landing log, the brought-in log and the co-author log of coAuthors.
//
// The format keeps every field where no byte inside another field can move
// it. A record starts at NUL with a header of the fields whose shape is
// fixed, separated by U+001F: the SHA, the parents, and the committer and
// author times in Unix seconds. Five NUL-terminated fields of free text
// follow, read by position whatever bytes they hold: the mailmapped author
// name and email, the email as written, the subject, and the co-author
// trailer values separated by U+001E. None of them can hold a NUL: git reads a commit message only
// up to its first NUL, and git fsck rejects a NUL in a commit header. So a
// U+001F inside a name, an email or a trailer value, which git accepts,
// cannot shift a field, and History and coAuthors, which read the same
// format with this same parser, cannot disagree about a commit.
//
// With -z, git ends every numstat row with NUL instead of a newline and
// prints paths exactly as stored, never quoted. A rename row has an empty
// path field followed by two more NUL-terminated fields, the source and
// the destination: "3\t1\t\0old\0new\0". So no path is ever parsed out of
// git's human-readable "dir/{old => new}/file.py" form, which is ambiguous
// for a path that contains " => " or braces and is quoted for one that
// contains a quote, a control character or (by default) a non-ASCII byte.
//
// After the fields, git writes a newline before the first numstat row and
// a NUL between commits. Where exactly they land depends on the format
// kind (format: separates commits, tformat: terminates them), so the reader
// does not count them. It classifies every token that is not a positional
// field instead: an empty token is a separator; a token that starts with
// the newline, or that reads as a numstat row, is a row; and any other
// token is the next commit's header.

// historyLogFormat is the pretty format of every log that reads commits
// (the landing log, the brought-in log and coAuthors), laid out as this
// file's comment describes. %aN and %aE apply the mailmap even where a
// repository sets log.mailmap=false; %an and %ae would not
// (week-collector.md). %ct and %at print any time git stored, while %cI
// prints an offset such as +0099, which git accepts, as "+00:99", which is
// not RFC 3339 (verified on 2.50.1). %ae, the author email as the commit
// wrote it, follows %aE so the facts can say what the mailmap rewrote
// (#53, domain.Alias): stillhere never parses a mailmap, but it can record
// what git made of one.
const historyLogFormat = "%x00%H%x1f%P%x1f%ct%x1f%at%x00%aN%x00%aE%x00%ae%x00%s%x00" + coAuthorFormat + "%x00"

// historyTextFields is the number of free-text fields after a header: the
// author's name and email, the email as written, the subject and the
// co-author trailers.
const historyTextFields = 5

// historyRecord is one commit of a history log, as git printed it.
type historyRecord struct {
	commit    domain.Commit   // CoAuthors stays empty until historyRun.canonicalise
	written   string          // the author email as the commit wrote it (%ae), lowercased: before the mailmap
	coAuthors []domain.Person // as the trailers wrote them, before the mailmap
	numstat   []historyNumstat
}

// historyNumstat is one numstat row.
type historyNumstat struct {
	path    string // the destination of a rename
	oldPath string // the source of a rename, else ""
	added   int
	removed int
	binary  bool // numstat printed "-" for both counts
}

// historyParseLog reads a history log and calls fn once per commit, in the
// order git printed them, with the commit's numstat rows.
func historyParseLog(r io.Reader, fn func(*historyRecord) error) error {
	s := newScanner(r, scanNUL)
	var (
		rec   *historyRecord
		text  []string // rec's free-text fields read so far
		row   historyNumstat
		paths int // rename paths still to read: 2 (source, destination), then 1
	)
	flush := func() error {
		if rec == nil {
			return nil
		}
		err := fn(rec)
		rec = nil
		return err
	}
	for s.Scan() {
		tok := s.Text()
		switch {
		case rec != nil && len(text) < historyTextFields:
			if text = append(text, tok); len(text) == historyTextFields {
				historySetText(rec, text)
			}
			continue
		case paths == 2:
			row.oldPath, paths = tok, 1
			continue
		case paths == 1:
			row.path, paths = tok, 0
			rec.numstat = append(rec.numstat, row)
			continue
		case tok == "":
			continue
		}

		entry, isRow := tok, false
		if rest, ok := strings.CutPrefix(tok, "\n"); ok {
			entry, isRow = rest, true
		} else if _, err := historyParseNumstat(tok); err == nil {
			isRow = true
		}
		if !isRow {
			if err := flush(); err != nil {
				return err
			}
			var err error
			if rec, err = historyParseHeader(tok); err != nil {
				return err
			}
			text = text[:0]
			continue
		}
		if rec == nil {
			return errors.New("a numstat row came before any commit")
		}
		var err error
		if row, err = historyParseNumstat(entry); err != nil {
			return fmt.Errorf("commit %s: %w", rec.commit.SHA, err)
		}
		if row.path == "" {
			paths = 2 // a rename: its source and destination follow
			continue
		}
		rec.numstat = append(rec.numstat, row)
	}
	if err := s.Err(); err != nil {
		return err
	}
	switch {
	case rec != nil && len(text) < historyTextFields:
		return fmt.Errorf("commit %s: the output ended inside the commit's fields", rec.commit.SHA)
	case paths != 0:
		return errors.New("the output ended inside a rename row")
	}
	return flush()
}

// historyParseHeader parses the header of one commit, the fields whose
// shape is fixed: SHA, parents, committer time and author time.
func historyParseHeader(header string) (*historyRecord, error) {
	f := strings.Split(header, "\x1f")
	if len(f) != 4 || !historySHA(f[0]) {
		return nil, errors.New("malformed commit header in git log output")
	}
	sha := f[0]
	var parents []string // nil for a root commit
	for _, p := range strings.Fields(f[1]) {
		if !historySHA(p) {
			return nil, fmt.Errorf("commit %s: malformed parent list", sha)
		}
		parents = append(parents, p)
	}
	committed, ok1 := historyUnix(f[2])
	authored, ok2 := historyUnix(f[3])
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("commit %s: malformed commit or author time", sha)
	}
	return &historyRecord{commit: domain.Commit{
		SHA:        sha,
		Parents:    parents,
		AuthorTime: authored,
		CommitTime: committed,
	}}, nil
}

// historySetText fills in the free-text fields of a record, in format
// order: the mailmapped author name and email, the email as written, the
// subject and the co-author trailers. Error messages never quote them:
// they hold names, emails and the subject.
func historySetText(rec *historyRecord, text []string) {
	rec.commit.Author = domain.Person{Name: text[0], Email: strings.ToLower(text[1])}
	rec.written = strings.ToLower(text[2])
	rec.commit.Subject = text[3]
	rec.coAuthors = coAuthorParse(text[4])
}

// historyUnix parses a time git printed with %ct or %at, in Unix seconds,
// as UTC, which every time fact is.
func historyUnix(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}

// historyParseNumstat parses one numstat row, "<added>\t<removed>\t<path>",
// where both counts are "-" for a binary file and the path is empty for a
// rename, whose paths follow as their own fields.
func historyParseNumstat(entry string) (historyNumstat, error) {
	added, rest, ok1 := strings.Cut(entry, "\t")
	removed, path, ok2 := strings.Cut(rest, "\t")
	if !ok1 || !ok2 {
		return historyNumstat{}, errors.New("malformed numstat row")
	}
	if added == "-" && removed == "-" {
		return historyNumstat{path: path, binary: true}, nil
	}
	a, okA := historyCount(added)
	r, okR := historyCount(removed)
	if !okA || !okR {
		return historyNumstat{}, errors.New("malformed numstat counts")
	}
	return historyNumstat{path: path, added: a, removed: r}, nil
}

// historyCount parses a count git printed: decimal digits only.
func historyCount(s string) (int, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
