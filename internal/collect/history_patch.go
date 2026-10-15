package collect

import (
	"bufio"
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
	var want []string
	for _, landing := range h.landings {
		for _, sha := range h.maps[landing] {
			c := h.commits[sha].commit
			switch {
			case len(c.Parents) > 1: // a merge: no patch of its own in v1
			case h.repo.Shallow && len(c.Parents) == 0: // the clone's boundary
			default:
				want = append(want, sha)
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	slices.Sort(want)

	args := []string{attrSource(h.repo.Head), "log", "--no-walk", "--stdin", "--diff-merges=off", "-p", "-U0", "-w"}
	args = append(args, historyDiffArgs()...)
	args = append(args, "--no-color", "--no-ext-diff", "--default-prefix", "--pretty=format:%x00%H", "--")

	cmd := domain.GitCmd{Dir: h.repo.Path, Args: args, Stdin: historyStdin(want)}
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

// historyParsePatch reads the output of the own-patch query and calls fn
// once for each file of each commit that has hunks, with the destination
// path (the source path for a deletion) and the non-blank lines the hunks
// add and remove. A file with no hunks, such as a pure rename, a mode
// change or a binary file, adds and removes nothing, so fn is not called
// for it. Git prints a typechange as two files with one path, so fn can be
// called twice for one path.
func historyParsePatch(r io.Reader, fn func(commit, path string, added, removed int) error) error {
	s := newScanner(r, bufio.ScanLines)
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

	for s.Scan() {
		if oldLeft > 0 || newLeft > 0 {
			// A hunk line, read as bytes: hunks are most of a patch, and
			// counting them needs no string.
			line := s.Bytes()
			mark := byte(0) // an empty line has none
			if len(line) > 0 {
				mark = line[0]
			}
			switch {
			case mark == '+' && newLeft > 0:
				newLeft--
				if nonBlank(line[1:]) {
					added++
				}
			case mark == '-' && oldLeft > 0:
				oldLeft--
				if nonBlank(line[1:]) {
					removed++
				}
			case (mark == ' ' || len(line) == 0) && oldLeft > 0 && newLeft > 0:
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

		line := s.Text()
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
	if err := s.Err(); err != nil {
		return err
	}
	if oldLeft > 0 || newLeft > 0 {
		return fmt.Errorf("commit %s: the output ended inside a hunk", commit)
	}
	return flush()
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
		if label, err = historyUnquote(label); err != nil {
			return "", err
		}
	}
	path, ok := strings.CutPrefix(label, prefix)
	if !ok || path == "" {
		return "", errors.New("a patch path lacks its a/ or b/ prefix")
	}
	return path, nil
}

// historyUnquote undoes git's quote_c_style: the surrounding quotes, the
// escapes \a \b \t \n \v \f \r \" \\, and three-digit octal escapes, which
// stand for single bytes, so a path that is not valid UTF-8 survives byte
// for byte. Raw bytes pass through, which is how git prints non-ASCII when
// core.quotePath is false.
func historyUnquote(s string) (string, error) {
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
			if i+2 >= len(s) || !historyOctal(s[i+1]) || !historyOctal(s[i+2]) {
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

func historyOctal(c byte) bool { return c >= '0' && c <= '7' }

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
