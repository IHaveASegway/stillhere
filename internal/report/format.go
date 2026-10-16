package report

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// This file holds what the text and Markdown renderers share: number and
// date formats, the defences against untrusted text, and the line printer.

// maxWidth is the width the text renderer fits its tables and prose into.
const maxWidth = 100

// printer writes lines and keeps the first write error, so a renderer can
// print section after section and check once at the end. It is one page:
// names labels the people the page cannot name (printer.person in
// names.go).
type printer struct {
	w     io.Writer
	err   error
	names pageNames
	all   bool // print every row of every table (AllRows), not the first pageRows
}

// pageRows is how many rows of a list of people, a rank table's ranked
// rows, or an explanation's changes, commits or files a page prints before
// it counts the rest (SR-14): the first page a lead reads is the top of
// each table, and JSON, or AllRows, has every row.
const pageRows = 10

// areaRows is how many of a repository's areas its card prints before it
// counts the rest.
const areaRows = 5

// Option changes how Text and Markdown print a document. JSON always holds
// every row.
type Option func(*printer)

// AllRows prints every row of every table, where a page otherwise prints
// the first pageRows and counts the rest (the command line's --all).
func AllRows() Option { return func(p *printer) { p.all = true } }

// newPrinter is a page on w with opts.
func newPrinter(w io.Writer, opts []Option) *printer {
	p := &printer{w: w}
	for _, o := range opts {
		o(p)
	}
	return p
}

// shown returns the first rows of a table a page prints, max of them
// unless it prints every row, and how many it leaves out.
func shown[R any](p *printer, rows []R, limit int) ([]R, int) {
	if p.all || len(rows) <= limit {
		return rows, 0
	}
	return rows[:limit], len(rows) - limit
}

// moreText is "12 more people (--all lists them)", or "" for none.
func moreText(n int, one, many string) string {
	if n == 0 {
		return ""
	}
	return plural(n, "more "+one, "more "+many) + " (--all lists them)"
}

func (p *printer) line(s string) {
	if p.err != nil {
		return
	}
	_, p.err = io.WriteString(p.w, s+"\n")
}

func (p *printer) linef(format string, args ...any) {
	p.line(fmt.Sprintf(format, args...))
}

func (p *printer) blank() { p.line("") }

// wrapped prints prose wrapped at maxWidth: the first line starts with
// first, and every later one with rest, so a list item keeps its hanging
// indent. A word longer than a line gets a line of its own.
func (p *printer) wrapped(first, rest, text string) {
	prefix := first
	line := prefix
	empty := true
	for _, word := range strings.Fields(text) {
		if !empty && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > maxWidth {
			p.line(line)
			prefix = rest
			line, empty = prefix, true
		}
		if !empty {
			line += " "
		}
		line += word
		empty = false
	}
	if !empty {
		p.line(line)
	}
}

// column describes one column of a table.
type column struct {
	head  string
	right bool // numbers: right-aligned, so magnitudes line up
}

// minCell is the narrowest a text column is cut to.
const minCell = 8

// textTable prints rows under the column heads, padded with spaces: names
// left-aligned and numbers right-aligned, two spaces apart. text/tabwriter
// aligns every column one way, which is why this does not use it. While
// the table is wider than maxWidth, the widest left-aligned column (names,
// titles, details) loses one character of width, never going below
// minCell: the cut falls on the text with most to spare and evens out
// across several wide columns, and cut cells end in "…". Numbers are never
// cut. Only a table whose text columns are all at minCell can still be too
// wide. Cells must already be clean.
func (p *printer) textTable(indent string, cols []column, rows [][]string) {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = utf8.RuneCountInString(c.head)
	}
	for _, r := range rows {
		for i := range cols {
			widths[i] = max(widths[i], utf8.RuneCountInString(r[i]))
		}
	}
	total := utf8.RuneCountInString(indent) + 2*(len(cols)-1)
	for _, w := range widths {
		total += w
	}
	for over := total - maxWidth; over > 0; over-- {
		cut := -1
		for i, c := range cols {
			if !c.right && widths[i] > minCell && (cut < 0 || widths[i] > widths[cut]) {
				cut = i
			}
		}
		if cut < 0 {
			break
		}
		widths[cut]--
	}
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = c.head
	}
	p.line(indent + alignRow(head, cols, widths))
	for _, r := range rows {
		p.line(indent + alignRow(r, cols, widths))
	}
}

func alignRow(cells []string, cols []column, widths []int) string {
	var b strings.Builder
	for i, c := range cols {
		cell := truncate(cells[i], widths[i])
		pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
		if i > 0 {
			b.WriteString("  ")
		}
		if c.right {
			b.WriteString(pad + cell)
		} else {
			b.WriteString(cell + pad)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// truncate cuts s to at most width runes, ending in "…" when it cut.
func truncate(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

// clean makes untrusted text (an author name, a path, a commit subject, a
// git message) safe to print on a terminal: control and format characters,
// which include escape sequences and bidirectional overrides, are shown
// escaped the way Go quotes them (\x1b, ‮), and runs of whitespace
// become one space. Bytes that are not UTF-8 print as U+FFFD, as JSON
// writes them. Everything else is printed as it is.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// wordJoiner (U+2060) renders as nothing and joins nothing: put after an @
// or a #, it breaks GitHub's mention and reference patterns.
const wordJoiner = "\u2060"

// mdText escapes untrusted text for Markdown prose and table cells. Every
// ASCII punctuation character that Markdown or GitHub's inline HTML could
// read as syntax is backslash-escaped, and & becomes &amp;, so a name such
// as "<img src=x>" or "a | b" renders as the literal text. Parentheses and
// "!" need no escape: they are syntax only next to a bracket, and brackets
// are escaped. Control characters are escaped first, as for the terminal.
//
// A page is pasted into issues and pull requests, where GitHub links
// "@name" as a mention, which notifies that account, and "#12" as a
// reference, which posts a back-link on issue 12. A commit subject or a
// display name does either at random ("thanks @alice", "fixes #12"), so @
// and # are followed by a word joiner: the text reads the same and links
// nobody.
func mdText(s string) string {
	var b strings.Builder
	for _, r := range clean(s) {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '@':
			b.WriteString("@" + wordJoiner)
		case '#':
			b.WriteString(`\#` + wordJoiner)
		case '\\', '`', '*', '_', '[', ']', '<', '>', '|', '~':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mdCode renders untrusted text, such as a path or a SHA, as a code span. The
// fence is one backtick longer than the longest run inside, as CommonMark
// requires, and | is escaped because GitHub splits table cells on it even
// inside a code span.
func mdCode(s string) string {
	s = strings.ReplaceAll(clean(s), "|", `\|`)
	if s == "" {
		return ""
	}
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

// count turns a metric number that counts things (lines, files, landings)
// into an int. Metric values are float64 so that ratios fit the same type.
func count(f float64) int {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int(math.Round(f))
}

// grouped formats n with thousands separators: 2730000 is "2,730,000".
func grouped(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// number formats a metric number: whole numbers with separators, others
// with up to two decimals.
func number(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return grouped(int(f))
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}

// percent formats a share in [0, 1] with one decimal: 0.1176 is "11.8%".
func percent(f float64) string {
	return strconv.FormatFloat(100*f, 'f', 1, 64) + "%"
}

// percent0 formats a share as a whole percentage, for rank estimates whose
// intervals make decimals meaningless: 0.81 is "81%".
func percent0(f float64) string {
	return strconv.FormatFloat(100*f, 'f', 0, 64) + "%"
}

// share prints a Share as "11.8% (4 of 34 lines)", with what it is a share
// of.
func share(s *Share, of string) string {
	return percent(s.Value) + " (" + number(s.Num) + " of " + number(s.Den) + " " + of + ")"
}

// day formats an instant as a UTC date, with the time when it is not
// midnight: "2026-10-07", or "2026-10-06 12:00". It is for a table cell
// whose column head says UTC; prose prints dayUTC (#79).
func day(t time.Time) string {
	t = t.UTC()
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format(time.DateOnly)
	}
	return t.Format("2006-01-02 15:04")
}

// dayUTC is day saying it is UTC, "2026-10-07 UTC": a reader outside UTC
// would otherwise read an as-of time or a window's edge in their own zone
// (#79).
func dayUTC(t time.Time) string { return day(t) + " UTC" }

// date formats an instant as its UTC date.
func date(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// interval formats a window as "2026-09-30 → 2026-10-07 UTC" (the end is
// exclusive, as everywhere in stillhere).
func interval(w Interval) string {
	return day(w.Since) + " → " + dayUTC(w.Until)
}

// windowDays is a window's length in whole days: 90 for review_coverage@1's.
func windowDays(w Interval) int { return int(w.Until.Sub(w.Since) / (24 * time.Hour)) }

// knownCount prints a count that may be unknown (nil), of lines, people or
// authors, as "—" when it is, so that "unknown" never reads as 0 (#74).
func knownCount(n *int) string {
	if n == nil {
		return "—"
	}
	return grouped(*n)
}

// short abbreviates a full SHA for reading; JSON keeps the full one.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// plural returns "1 repository" or "3 repositories".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return grouped(n) + " " + many
}

// joinNames lists names as "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
