package collect

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file counts Rust's inline tests: the #[cfg(test)] modules a Rust
// crate keeps inside its source files, which classification, a verdict per
// path, counts as prod (docs/spec/classification.md#rust-inline-tests).
// Telling them apart line by line would change the input of every metric
// (debt, named there); the count lets a repository's caveat say how much
// of its prod is tests.

// inlineTestsAttribute is the attribute, as git grep -E reads it: #[cfg(test)]
// or the inner #![cfg(test)], with any spacing between its tokens.
const inlineTestsAttribute = `#!?[[:space:]]*\[[[:space:]]*cfg[[:space:]]*\([[:space:]]*test[[:space:]]*\)[[:space:]]*\]`

// InlineTestCount is what InlineTests found.
type InlineTestCount struct {
	Files int // files with at least one non-blank line under #[cfg(test)]
	Lines int // the non-blank lines from each #[cfg(test)] to the end of the item it gates
}

// InlineTests counts, among paths, the Rust files at repo.Head (".rs",
// ignoring case) whose items #[cfg(test)] gates, and those items'
// non-blank lines, with two git commands:
//
//	git grep --no-color -l -z -a -E -e <attribute> <head> -- ':(icase)*.rs'
//	git cat-file --batch
//
// The first finds the files that hold the attribute at all; the second
// reads those of them that paths names, which a scan passes as its prod
// paths at Head, so a file the classifier skipped or gave another role is
// never read. Each is counted by inlineTestLines. A repository without a
// Rust file among paths runs no command.
func InlineTests(ctx context.Context, git domain.GitRunner, repo domain.Repo, paths []string) (InlineTestCount, error) {
	var count InlineTestCount
	want := map[string]bool{}
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".rs") {
			want[p] = true
		}
	}
	if len(want) == 0 {
		return count, nil
	}
	if !classifyObjectID(repo.Head) {
		return count, fmt.Errorf("inline tests: head %q is not a full commit id", repo.Head)
	}
	prefix := repo.Head + ":"
	var hits []string
	cmd := domain.GitCmd{Dir: repo.Path, OKExit: []int{1}, Args: []string{
		"grep", "--no-color", "-l", "-z", "-a", "-E", "-e", inlineTestsAttribute, repo.Head, "--", ":(icase)*.rs"}}
	err := git.Stream(ctx, cmd, func(r io.Reader) error {
		s := newScanner(r, scanNUL)
		for s.Scan() {
			path, ok := strings.CutPrefix(s.Text(), prefix)
			if !ok {
				return fmt.Errorf("unreadable grep record %q", s.Text())
			}
			if want[path] {
				hits = append(hits, path)
			}
		}
		return s.Err()
	})
	if err != nil {
		return count, fmt.Errorf("inline tests: %w", err)
	}
	if len(hits) == 0 {
		return count, nil
	}
	slices.Sort(hits)
	var stdin bytes.Buffer
	for _, p := range hits {
		if strings.ContainsAny(p, "\n\x00") {
			continue // a batch line cannot name it; it counts for nothing
		}
		stdin.WriteString(prefix + p + "\n")
	}
	cmd = domain.GitCmd{Dir: repo.Path, Args: []string{"cat-file", "--batch"}, Stdin: bytes.NewReader(stdin.Bytes())}
	err = git.Stream(ctx, cmd, func(r io.Reader) error {
		br := bufio.NewReader(r)
		for {
			header, err := br.ReadString('\n')
			if errors.Is(err, io.EOF) && header == "" {
				return nil
			}
			if err != nil {
				return fmt.Errorf("reading cat-file: %w", err)
			}
			fields := strings.Fields(header)
			if len(fields) != 3 || fields[1] != "blob" {
				continue // "<name> missing": nothing to count
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil || size < 0 {
				return fmt.Errorf("unreadable cat-file header %q", header)
			}
			blob := make([]byte, size+1) // the content and the LF after it
			if _, err := io.ReadFull(br, blob); err != nil {
				return fmt.Errorf("reading cat-file: %w", err)
			}
			if n := inlineTestLines(blob[:size]); n > 0 {
				count.Files++
				count.Lines += n
			}
		}
	})
	if err != nil {
		return InlineTestCount{}, fmt.Errorf("inline tests: %w", err)
	}
	return count, nil
}

// inlineTestLines counts the non-blank lines of the items #[cfg(test)]
// gates in one Rust source file: from the line of each attribute to the end
// of the item it is on, the brace that closes the item's block or the
// semicolon that ends it, and for an inner #![cfg(test)], to the end of the
// block it is in (the whole file, at the top). Comments, strings and
// character literals are skipped, so a brace or an attribute inside one
// counts for nothing; an attribute inside an item an earlier one gates is
// that item's. It is a count, not a parser: an attribute on a field or an
// argument runs to the end of the block around it, which items written as
// Rust is written seldom need, and a macro that writes a test module is not
// seen.
func inlineTestLines(src []byte) int {
	lines := 0
	for i := 0; i < len(src); {
		if j, ok := rustSkip(src, i); ok {
			i = j
			continue
		}
		if src[i] == '#' {
			if j, inner, ok := rustCfgTest(src, i); ok {
				end := rustItemEnd(src, j)
				if inner {
					end = rustBlockEnd(src, j)
				}
				lines += nonBlankLines(src[lineStart(src, i):end])
				i = end
				continue
			}
		}
		i++
	}
	return lines
}

// rustCfgTest reports whether an attribute #[cfg(test)], or the inner
// #![cfg(test)], starts at src[i], with any white space between its
// tokens, and returns the offset after its closing bracket.
func rustCfgTest(src []byte, i int) (end int, inner, ok bool) {
	j := i + 1
	if j < len(src) && src[j] == '!' {
		inner = true
		j++
	}
	for _, tok := range []string{"[", "cfg", "(", "test", ")", "]"} {
		for j < len(src) && isSpace(src[j]) {
			j++
		}
		if !bytes.HasPrefix(src[j:], []byte(tok)) {
			return 0, false, false
		}
		j += len(tok)
		if (tok == "cfg" || tok == "test") && j < len(src) && isIdent(src[j]) {
			return 0, false, false // cfg_attr, testing
		}
	}
	return j, inner, true
}

// rustItemEnd returns the offset after the item that starts at src[i]: its
// block's closing brace, the semicolon that ends it outside any bracket, or
// the closing bracket of the block around it, whichever comes first.
func rustItemEnd(src []byte, i int) int {
	braces, others := 0, 0 // brace depth; parenthesis and square bracket depth
	for i < len(src) {
		if j, ok := rustSkip(src, i); ok {
			i = j
			continue
		}
		switch src[i] {
		case '{':
			braces++
		case '}':
			if braces == 0 {
				return i // the block around the item ends
			}
			braces--
			if braces == 0 && others == 0 {
				return i + 1
			}
		case '(', '[':
			others++
		case ')', ']':
			if others == 0 && braces == 0 {
				return i
			}
			if others > 0 {
				others--
			}
		case ';':
			if braces == 0 && others == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(src)
}

// rustBlockEnd returns the offset after the brace that closes the block
// src[i] is in, or the end of src at the top level.
func rustBlockEnd(src []byte, i int) int {
	braces := 0
	for i < len(src) {
		if j, ok := rustSkip(src, i); ok {
			i = j
			continue
		}
		switch src[i] {
		case '{':
			braces++
		case '}':
			if braces == 0 {
				return i + 1
			}
			braces--
		}
		i++
	}
	return len(src)
}

// rustSkip reports whether a comment, a string or a character literal
// starts at src[i], and returns the offset after it: // and nested /* */
// comments, "..." strings with their escapes and b"..." and c"...", raw
// strings r"...", r#"..."# (and br, cr), and character literals 'x', '\n',
// '\u{1F600}', told from a lifetime ('a) by their closing quote.
func rustSkip(src []byte, i int) (int, bool) {
	c := src[i]
	next := byte(0)
	if i+1 < len(src) {
		next = src[i+1]
	}
	switch {
	case c == '/' && next == '/':
		if end := bytes.IndexByte(src[i:], '\n'); end >= 0 {
			return i + end, true
		}
		return len(src), true
	case c == '/' && next == '*':
		depth := 0
		for j := i; j+1 < len(src); j++ {
			switch {
			case src[j] == '/' && src[j+1] == '*':
				depth++
				j++
			case src[j] == '*' && src[j+1] == '/':
				depth--
				j++
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return len(src), true
	case c == '"':
		return rustString(src, i+1), true
	case c == '\'':
		if next == '\\' {
			if end := bytes.IndexByte(src[i+2:], '\''); end >= 0 {
				// '\'' and '\\' close one byte later than the first quote
				// after the backslash would say.
				if end == 0 {
					return i + 4, true
				}
				return i + 2 + end + 1, true
			}
			return len(src), true
		}
		if i+1 < len(src) {
			_, n := utf8.DecodeRune(src[i+1:])
			if i+1+n < len(src) && src[i+1+n] == '\'' {
				return i + 2 + n, true
			}
		}
		return 0, false // a lifetime or a label
	case (c == 'r' || c == 'b' || c == 'c') && (i == 0 || !isIdent(src[i-1])):
		j := i + 1
		if c != 'r' && j < len(src) && src[j] == 'r' {
			j++ // br"..." and cr"..."
		} else if c != 'r' {
			if j < len(src) && src[j] == '"' {
				return rustString(src, j+1), true // b"..." and c"..."
			}
			return 0, false
		}
		hashes := 0
		for j < len(src) && src[j] == '#' {
			hashes++
			j++
		}
		if j >= len(src) || src[j] != '"' {
			return 0, false // an identifier, or r#ident
		}
		closing := append([]byte{'"'}, bytes.Repeat([]byte{'#'}, hashes)...)
		if end := bytes.Index(src[j+1:], closing); end >= 0 {
			return j + 1 + end + len(closing), true
		}
		return len(src), true
	}
	return 0, false
}

// rustString returns the offset after the string whose content starts at
// src[i], skipping its escapes.
func rustString(src []byte, i int) int {
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1
		}
		i++
	}
	return len(src)
}

// lineStart is the offset of the start of the line src[i] is on.
func lineStart(src []byte, i int) int {
	return bytes.LastIndexByte(src[:i], '\n') + 1
}

// nonBlankLines counts the non-blank lines of b, by nonBlank, the
// predicate blame counts surviving lines with.
func nonBlankLines(b []byte) int {
	n := 0
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		if nonBlank(line) {
			n++
		}
	}
	return n
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}
