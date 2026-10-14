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

// This file reads the history logs printed with -z and historyLogFormat:
// the landing log and the brought-in log.
//
// With -z, git ends every numstat row with NUL instead of a newline and
// prints paths exactly as stored, never quoted. A rename row has an empty
// path field followed by two more NUL-terminated fields, the source and
// the destination: "3\t1\t\0old\0new\0". So no path is ever parsed out of
// git's human-readable "dir/{old => new}/file.py" form, which is ambiguous
// for a path that contains " => " or braces and is quoted for one that
// contains a quote, a control character or (by default) a non-ASCII byte.
//
// Around the rows, git writes the header line that historyLogFormat
// produces, a newline before the first row, and NUL separators between
// commits. Where exactly those land depends on the format kind (format:
// separates commits, tformat: terminates them) and on whether the commit
// printed a diff, so the reader does not count them. It classifies each
// NUL-separated token instead: an empty token is a separator; a token that
// starts with the newline, or that reads as a numstat row, is a row; any
// other token is a header, possibly followed by a newline and the first
// row; and the two tokens after a rename row are its paths, whatever bytes
// they hold.

// historyRecord is one commit of a history log, as git printed it.
type historyRecord struct {
	commit    domain.Commit   // CoAuthors stays empty until historyRun.canonicalise
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
			header, rest, _ := strings.Cut(tok, "\n")
			var err error
			if rec, err = historyParseHeader(header); err != nil {
				return err
			}
			if rest == "" {
				continue
			}
			entry = rest
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
	if paths != 0 {
		return errors.New("the output ended inside a rename row")
	}
	return flush()
}

// historyParseHeader parses the header line of one commit. Error messages
// never quote the line: it holds names, emails and the subject.
func historyParseHeader(header string) (*historyRecord, error) {
	f := strings.SplitN(header, "\x1f", 8)
	if len(f) != 8 || !historySHA(f[0]) {
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
	committed, err1 := time.Parse(time.RFC3339, f[2])
	authored, err2 := time.Parse(time.RFC3339, f[3])
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("commit %s: %w", sha, err)
	}
	return &historyRecord{
		commit: domain.Commit{
			SHA:        sha,
			Parents:    parents,
			Author:     domain.Person{Name: f[4], Email: strings.ToLower(f[5])},
			AuthorTime: authored.UTC(),
			CommitTime: committed.UTC(),
			Subject:    f[7],
		},
		coAuthors: coAuthorParse(f[6]),
	}, nil
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
