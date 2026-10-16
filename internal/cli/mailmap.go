package cli

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"
)

// The organisation mailmap is git's to read: stillhere passes the file to
// git (-c mailmap.file=) and never parses it for merges. But git skips a
// line it cannot read without a word, and applies a line it can read only
// in part, so a typo silently renames someone or merges no one (RB-17).
// The command line reads the file once more, by git's own rules, only to
// say which lines git will skip or cut, with their numbers.

// maxMailmapCheck is how much of a mailmap the check reads: far more than
// any organisation's (the Linux kernel's is about 70 KB). git reads the
// rest, unchecked, which the warning says.
const maxMailmapCheck = 16 << 20

// maxMailmapWarnings is how many lines the check names before it counts
// the rest.
const maxMailmapWarnings = 10

// checkMailmap logs, at warn, each line of the mailmap at path that git
// will ignore or apply only in part, by number, as label names the file.
// It never fails a command: git reads the file as it is either way.
func checkMailmap(log *slog.Logger, label, path string) {
	f, err := os.Open(path)
	if err != nil {
		return // readable already said why, when it matters
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxMailmapCheck+1))
	if err != nil {
		return
	}
	if len(data) > maxMailmapCheck {
		log.Warn("the mailmap is larger than stillhere checks; git reads it all", "file", label,
			"checked_bytes", maxMailmapCheck)
		data = data[:maxMailmapCheck]
	}
	if i := bytes.IndexByte(data, 0); i >= 0 || !utf8.Valid(data) {
		log.Warn("the mailmap is not a text file (it holds a NUL byte or bytes that are not UTF-8): git reads what it "+
			"can of it, and no line of it may mean what it seems", "file", label)
		return
	}
	var problems []string
	for n, line := range strings.Split(string(data), "\n") {
		if why := mailmapLineProblem(line); why != "" {
			problems = append(problems, fmt.Sprintf("line %d %s", n+1, why))
		}
	}
	if len(problems) == 0 {
		return
	}
	shown := problems
	if len(shown) > maxMailmapWarnings {
		shown = append(shown[:maxMailmapWarnings:maxMailmapWarnings], fmt.Sprintf("and %d more", len(problems)-maxMailmapWarnings))
	}
	log.Warn("git ignores or cuts some lines of the mailmap: "+strings.Join(shown, "; "), "file", label)
}

// mailmapLineProblem says how git misreads one line of a mailmap, "" for a
// line it reads whole: a comment (# first), a blank line, or "Name
// <email>", "<email> <email>", "Name <email> <email>" or "Name <email>
// Name <email>". It follows git's read_mailmap_line (mailmap.c): a line
// without a closed, non-empty first <email> is skipped, and after it, text
// that holds no closed second <email> is dropped, so the line maps the
// first email alone, renaming that address's commits to the first name.
func mailmapLineProblem(line string) string {
	line = strings.TrimRight(line, "\r")
	if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
		return "" // git skips an indented comment too, and harmlessly
	}
	left := strings.IndexByte(line, '<')
	if left < 0 {
		return "has no <email>, so git skips it"
	}
	right := strings.IndexByte(line[left+1:], '>')
	if right < 0 {
		return "has a < with no >, so git skips it"
	}
	right += left + 1
	if right == left+1 {
		return "starts with an empty <>, so git skips it"
	}
	name := strings.TrimSpace(line[:left])
	rest := line[right+1:]
	trimmed := strings.TrimSpace(rest)
	switch l := strings.IndexByte(rest, '<'); {
	case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		if name == "" {
			return "names one <email> and nothing to map it to, so it changes nothing"
		}
		return ""
	case l < 0:
		return "has text after its first <email> but no second <email>, so git drops the text and maps only the first email to the first name"
	case !strings.Contains(rest[l+1:], ">"):
		return "has a second < with no >, so git drops it and maps only the first email to the first name"
	}
	return ""
}
