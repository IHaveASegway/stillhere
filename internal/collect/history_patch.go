package collect

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file reads each commit's own patch, the source of
// FileChange.AddedNonBlank and RemovedNonBlank, which durable@1 and
// simplifying@1 count (docs/metrics.md#change-facts-shared-by-durable1-and-simplifying1).
//
// The patch is read line by line. Inside a hunk, lines are consumed by the
// counts in its "@@ -a,b +c,d @@" header, never by their look: with -U0 a
// hunk is exactly b removed and d added lines, so an added line whose text
// is "++ b/x" (printed "+++ b/x") or a line that begins with NUL can never
// be mistaken for a header or for the next commit. A hunk's lines are
// counted with nonBlank (scan.go), the predicate blame counts surviving
// lines with, so durable@1 divides like by like. Outside hunks, a line that
// starts with NUL starts the next commit, "diff --git" starts a file, and
// the "---" and "+++" lines name its paths. The "diff --git" line itself is
// never parsed for paths: "diff --git a/a b/b => c" has no unambiguous
// split.
//
// A hunk line is never held whole (#73). The patch covers every changed
// path, denied ones included, since the classification that would leave
// them out comes later, and a minified bundle, a source map or one-line
// JSON can hold a line of any length: read whole, one over the scanner's
// 64 MiB failed the repository's whole scan. What a hunk line counts for
// is its first byte and whether the rest is blank (patchLines.hunk), so it
// is read in pieces of the reader's buffer and judged piece by piece. Only
// the lines outside hunks, which name commits and paths, are read whole,
// up to maxToken, as every other git output is.

// readPatches runs the own-patch query over every non-merge commit the
// window's landings brought in, the landings included, and adds each
// file's non-blank added and removed lines to its numstat row.
//
// It uses git log --no-walk, so each commit is diffed against its first
// parent (a root commit against the empty tree) and a merge prints no
// diff. It never uses --first-parent: under it git prints a merge's
// first-parent diff, which would credit a merged branch's lines to the
// person who merged it. -w drops whitespace-only changes, so a re-indent
// adds and removes nothing, as blame -w already gives those lines to their
// earlier author. The diff is pinned like numstat's (attrSource,
// historyDiffArgs), so the two queries read every file the same way.
func (h *historyRun) readPatches(ctx context.Context) error {
	want := h.ownPatchCommits()
	if len(want) == 0 {
		return nil
	}
	cmd := domain.GitCmd{Dir: h.repo.Path, Args: h.ownPatchArgs("%x00%H"), Stdin: historyStdin(want)}
	err := h.git.Stream(ctx, cmd, func(r io.Reader) error {
		return historyParsePatch(r, func(commit, path string, added, removed int) error {
			fc, ok := h.changes[historyChangeKey{commit: commit, path: path}]
			if !ok {
				// Both queries pin the same rename detection, so every
				// file with hunks has a numstat row; a miss is a bug in
				// how a path was read, which must not pass silently.
				return fmt.Errorf("commit %s: the patch names a path its numstat did not", commit)
			}
			fc.AddedNonBlank += added
			fc.RemovedNonBlank += removed
			return nil
		})
	})
	if err != nil {
		return fmt.Errorf("reading own patches: %w", err)
	}
	return nil
}

// ownPatchCommits returns, sorted, the commits that have a patch of their
// own: every commit of the window's landings' maps, the landings
// included, but merges, which have none in v1, and a shallow clone's
// boundary, whose diff against the empty tree is its whole tree.
func (h *historyRun) ownPatchCommits() []string {
	var want []string
	for _, landing := range h.landings {
		for _, sha := range h.maps[landing] {
			c := h.commits[sha].commit
			switch {
			case len(c.Parents) > 1: // a merge: no patch of its own in v1
			case h.repo.Boundary(&c): // the clone's boundary
			default:
				want = append(want, sha)
			}
		}
	}
	slices.Sort(want)
	return want
}

// ownPatchArgs is the own-patch query, its commits read from stdin, with
// format as each commit's header: the query readPatches counts lines in
// and readPatchIDs fingerprints, so both read one diff of each commit.
func (h *historyRun) ownPatchArgs(format string) []string {
	args := []string{attrSource(h.repo.Head), "log", "--no-walk", "--stdin", "--diff-merges=off", "-p", "-U0", "-w"}
	args = append(args, historyDiffArgs()...)
	return append(args, "--no-color", "--no-ext-diff", "--default-prefix", "--pretty=format:"+format, "--")
}

// historyParsePatch reads the output of the own-patch query and calls fn
// once for each file of each commit that has hunks, with the destination
// path (the source path for a deletion) and the non-blank lines the hunks
// add and remove. A file with no hunks, such as a pure rename, a mode
// change or a binary file, adds and removes nothing, so fn is not called
// for it. Git prints a typechange as two files with one path, so fn can be
// called twice for one path.
func historyParsePatch(r io.Reader, fn func(commit, path string, added, removed int) error) error {
	s := newPatchLines(r)
	var (
		commit           string
		oldPath, newPath string
		oldSeen, newSeen bool // the file's "---" and "+++" lines
		hunks            bool // the file has a hunk
		added, removed   int  // non-blank lines in the file's hunks
		oldLeft, newLeft int  // lines of the current hunk still to read
	)
	flush := func() error {
		if !hunks {
			return nil
		}
		path := newPath
		if path == "" { // "+++ /dev/null": a deletion
			path = oldPath
		}
		a, r := added, removed
		hunks, added, removed = false, 0, 0
		return fn(commit, path, a, r)
	}
	newFile := func() {
		oldPath, newPath, oldSeen, newSeen = "", "", false, false
	}

	for {
		if oldLeft > 0 || newLeft > 0 {
			// A hunk line, judged by its first byte and whether the rest
			// is blank, never held whole: hunks are most of a patch, and a
			// line of a minified file can be longer than any buffer.
			mark, empty, rest, ok, err := s.hunk()
			if err != nil {
				return err
			}
			if !ok {
				break // the output ended inside a hunk, which the check below reports
			}
			switch {
			case mark == '+' && !empty && newLeft > 0:
				newLeft--
				if rest {
					added++
				}
			case mark == '-' && !empty && oldLeft > 0:
				oldLeft--
				if rest {
					removed++
				}
			case (mark == ' ' || empty) && oldLeft > 0 && newLeft > 0:
				// A context line, which -U0 prints only when
				// diff.interHunkContext joins two hunks; an empty one
				// under diff.suppressBlankEmpty.
				oldLeft--
				newLeft--
			case mark == '\\':
				// "\ No newline at end of file" after a removed line.
			default:
				return fmt.Errorf("commit %s: a hunk does not match its header", commit)
			}
			continue
		}

		line, ok, err := s.text()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		switch {
		case strings.HasPrefix(line, "\x00"):
			if err := flush(); err != nil {
				return err
			}
			if commit = line[1:]; !historySHA(commit) {
				return errors.New("malformed commit line in patch output")
			}
			newFile()
		case strings.HasPrefix(line, "diff --git "):
			if err := flush(); err != nil {
				return err
			}
			newFile()
		case strings.HasPrefix(line, "--- "):
			var err error
			if oldPath, err = historyLabel(line[len("--- "):], "a/"); err != nil {
				return fmt.Errorf("commit %s: %w", commit, err)
			}
			oldSeen = true
		case strings.HasPrefix(line, "+++ "):
			var err error
			if newPath, err = historyLabel(line[len("+++ "):], "b/"); err != nil {
				return fmt.Errorf("commit %s: %w", commit, err)
			}
			newSeen = true
		case strings.HasPrefix(line, "@@ "):
			if commit == "" || !oldSeen || !newSeen || (oldPath == "" && newPath == "") {
				return fmt.Errorf("commit %s: a hunk came before its file's paths", commit)
			}
			var err error
			if oldLeft, newLeft, err = historyHunk(line); err != nil {
				return fmt.Errorf("commit %s: %w", commit, err)
			}
			hunks = true
		}
		// Anything else is a file's extended header ("index", "new file
		// mode", "rename from", "similarity index", "Binary files ...
		// differ"), the "\ No newline" marker after a hunk's last line, or
		// the blank line before a commit's first file: none changes counts.
	}
	if oldLeft > 0 || newLeft > 0 {
		return fmt.Errorf("commit %s: the output ended inside a hunk", commit)
	}
	return flush()
}

// historyPatchLines reads the own-patch output one line at a time, as
// bufio.ScanLines splits it (a final line without a newline included, a
// carriage return before the newline dropped), in two ways: text returns
// a line whole, up to maxToken bytes, and hunk returns only what a hunk
// line counts for, reading it in pieces of the buffer, so its length is
// unbounded (#73).
type historyPatchLines struct {
	r    *bufio.Reader
	line []byte // text's line, reused
}

func newPatchLines(r io.Reader) *historyPatchLines {
	return &historyPatchLines{r: bufio.NewReaderSize(r, 64<<10)}
}

// hunk reads one line of a hunk: its first byte, whether the line is
// empty, and whether what follows its first byte holds a rune outside
// White_Space (nonBlank). ok is false at the end of the output.
func (p *historyPatchLines) hunk() (mark byte, empty, rest, ok bool, err error) {
	var judge nonBlankPieces
	first := true
	for {
		piece, rerr := p.r.ReadSlice('\n')
		last := !errors.Is(rerr, bufio.ErrBufferFull)
		switch {
		case rerr == nil, errors.Is(rerr, bufio.ErrBufferFull):
		case errors.Is(rerr, io.EOF):
			if first && len(piece) == 0 {
				return 0, false, false, false, nil
			}
		default:
			return 0, false, false, false, rerr
		}
		if last {
			piece = historyDropEOL(piece)
		}
		if first {
			first = false
			if len(piece) == 0 && last {
				return 0, true, false, true, nil
			}
			mark, piece = piece[0], piece[1:]
		}
		judge.write(piece)
		if last {
			return mark, false, judge.done(), true, nil
		}
	}
}

// text reads one line whole, without its line ending; ok is false at the
// end of the output. A line longer than maxToken is an error, as it is for
// every other git output the collectors read: outside hunks, the patch
// prints only commit names, paths and short headers.
func (p *historyPatchLines) text() (string, bool, error) {
	p.line = p.line[:0]
	for {
		piece, err := p.r.ReadSlice('\n')
		switch {
		case err == nil:
			p.line = append(p.line, piece...)
			return string(historyDropEOL(p.line)), true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(p.line)+len(piece) > maxToken {
				return "", false, bufio.ErrTooLong
			}
			p.line = append(p.line, piece...)
		case errors.Is(err, io.EOF):
			p.line = append(p.line, piece...)
			if len(p.line) == 0 {
				return "", false, nil
			}
			return string(historyDropEOL(p.line)), true, nil
		default:
			return "", false, err
		}
	}
}

// historyDropEOL drops a line's newline and a carriage return before it,
// as bufio.ScanLines does.
func historyDropEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	return bytes.TrimSuffix(line, []byte{'\r'})
}

// historyLabel returns the path a "---" or "+++" line names, without its
// prefix ("a/" or "b/", pinned by --default-prefix), or "" for /dev/null.
// Git quotes a path that holds a quote, a backslash, a control character
// or (unless core.quotePath is false) a non-ASCII byte, and ends the line
// with a tab when the path holds a space. An unquoted path never holds a
// raw tab, so that tab is never part of it.
func historyLabel(label, prefix string) (string, error) {
	label = strings.TrimSuffix(label, "\t")
	if label == "/dev/null" {
		return "", nil
	}
	if strings.HasPrefix(label, `"`) {
		var err error
		if label, err = unquotePath(label); err != nil {
			return "", err
		}
	}
	path, ok := strings.CutPrefix(label, prefix)
	if !ok || path == "" {
		return "", errors.New("a patch path lacks its a/ or b/ prefix")
	}
	return path, nil
}

// historyHunk returns the line counts of a hunk header, "@@ -a[,b] +c[,d]
// @@ heading": b removed and d added lines, where a missing count is 1.
func historyHunk(line string) (removed, added int, err error) {
	rest, ok := strings.CutPrefix(line, "@@ -")
	oldRange, rest, ok2 := strings.Cut(rest, " +")
	newRange, _, ok3 := strings.Cut(rest, " @@")
	if !ok || !ok2 || !ok3 {
		return 0, 0, errors.New("malformed hunk header")
	}
	removed, okR := historyRangeCount(oldRange)
	added, okA := historyRangeCount(newRange)
	if !okR || !okA {
		return 0, 0, errors.New("malformed hunk header")
	}
	return removed, added, nil
}

// historyRangeCount returns the line count of one side of a hunk header,
// "start,count" or "start" (one line).
func historyRangeCount(r string) (int, bool) {
	start, count, ok := strings.Cut(r, ",")
	if _, okStart := historyCount(start); !okStart {
		return 0, false
	}
	if !ok {
		return 1, true
	}
	return historyCount(count)
}
