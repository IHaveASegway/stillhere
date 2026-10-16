package policy

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file makes the deny list fast to match. The packaged list has about
// 240 patterns, most of them unanchored, and Go's regexp spends about a
// microsecond on each one for a typical path, so a path would cost about
// 200 µs: 10 s of CPU for a 50,000-file repository. Most patterns cannot
// match a path that lacks a literal of theirs ("node_modules/", "jquery",
// ".min."), so each pattern carries the literals at least one of which any
// match must contain, and Denies tries a pattern only when the path holds
// one of them: the prefilter idea of RE2's FilteredRE2 and of Hyperscan.
// That brings a path to a few microseconds, measured on the packaged list.

// Denies reports whether any Deny pattern matches path.
//
// A policy built by Default, Parse or FromSource carries a prefilter for
// each pattern, which skips the pattern when path cannot match it. A
// pattern added to Deny by hand has none and is always tried, so the answer
// never depends on the prefilter.
func (ps *Paths) Denies(path string) bool { return ps.denying(path) >= 0 }

// Denying returns the first Deny pattern that matches path, the one
// Denies stops at, with where the policy holds it; false when none does.
// classify.Explain reads it to say which rule denied a path, so the reason
// a reader is given and the decision are one computation.
func (ps *Paths) Denying(path string) (DenyRule, bool) {
	i := ps.denying(path)
	if i < 0 {
		return DenyRule{}, false
	}
	d := DenyRule{Pattern: ps.Deny[i].String(), Index: i + 1}
	if i < len(ps.origins) {
		d.Line, d.Note = ps.origins[i].line, ps.origins[i].note
	}
	return d, true
}

// denying is the index of the first Deny pattern that matches path, or -1.
func (ps *Paths) denying(path string) int {
	var folded string // path in canonical case, computed on first use
	for i, re := range ps.Deny {
		if i < len(ps.filter) && ps.filter[i].re == re && !ps.filter[i].admits(path, &folded) {
			continue
		}
		if re.MatchString(path) {
			return i
		}
	}
	return -1
}

// denyOrigin is where a package file holds one deny pattern: its line,
// from 1, and the label of its group, the first words of the comment block
// above it (denyNote).
type denyOrigin struct {
	line int
	note string
}

// denyNote is the label a comment line gives the patterns below it: the
// line without its # marks, up to the first ": ", ", " or ". ", so
// "xcode_file?: the extension is .nib" labels "xcode_file?" and "Test
// fixtures" labels itself. A line of # marks alone, a banner, labels
// nothing.
func denyNote(line string) string {
	s := strings.TrimSpace(strings.TrimLeft(line, "#"))
	s = strings.TrimSpace(strings.TrimRight(s, "#"))
	for _, sep := range []string{": ", ", ", ". "} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSuffix(s, ".")
}

// denyFilter is the prefilter of one deny pattern.
type denyFilter struct {
	re    *regexp.Regexp
	atoms []atom // one of these is in every match of re; nil when no literal is required
}

// atom is a literal that a match contains: exactly, or, when fold is set,
// up to Unicode simple case folding, which is how (?i) matches.
type atom struct {
	text string // for fold, in canonical case (see foldString)
	fold bool
}

// newDenyFilter finds re's atoms in its syntax tree.
func newDenyFilter(re *regexp.Regexp) denyFilter {
	// regexp.Compile parses with syntax.Perl, so this parse succeeds for any
	// compiled pattern; on the impossible failure the pattern is always
	// tried.
	tree, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return denyFilter{re: re}
	}
	return denyFilter{re: re, atoms: atoms(tree.Simplify())}
}

// admits reports whether path holds one of the atoms, so that the pattern
// may match it. folded caches path in canonical case across patterns.
func (f denyFilter) admits(path string, folded *string) bool {
	if f.atoms == nil {
		return true
	}
	for _, a := range f.atoms {
		haystack := path
		if a.fold {
			if *folded == "" {
				*folded = foldString(path)
			}
			haystack = *folded
		}
		if strings.Contains(haystack, a.text) {
			return true
		}
	}
	return false
}

// atoms returns literals at least one of which every match of n contains,
// or nil when there is no such set. It is sound rather than complete: nil
// only costs a regexp run.
//
//   - A literal requires itself.
//   - A concatenation requires what any one of its parts requires; the part
//     whose shortest literal is longest filters best.
//   - An alternation requires one of its branches' literals, if every
//     branch requires some.
//   - A group, x+ and x{n,m} with n >= 1 require what x requires.
//   - Anything that can match without a literal (x?, x*, classes, anchors)
//     requires nothing.
func atoms(n *syntax.Regexp) []atom {
	switch n.Op {
	case syntax.OpLiteral:
		if slices.Contains(n.Rune, utf8.RuneError) {
			// The pattern also matches an invalid byte of the path there,
			// which a byte search for U+FFFD would not find.
			return nil
		}
		if n.Flags&syntax.FoldCase != 0 {
			return []atom{{text: foldString(string(n.Rune)), fold: true}}
		}
		return []atom{{text: string(n.Rune)}}
	case syntax.OpCapture, syntax.OpPlus:
		return atoms(n.Sub[0])
	case syntax.OpRepeat:
		if n.Min >= 1 {
			return atoms(n.Sub[0])
		}
	case syntax.OpConcat:
		var best []atom
		for _, sub := range n.Sub {
			if a := atoms(sub); a != nil && (best == nil || shortest(a) > shortest(best)) {
				best = a
			}
		}
		return best
	case syntax.OpAlternate:
		var all []atom
		for _, sub := range n.Sub {
			a := atoms(sub)
			if a == nil {
				return nil
			}
			all = append(all, a...)
		}
		return all
	}
	return nil
}

// shortest is the byte length of the shortest atom in a.
func shortest(a []atom) int {
	n := len(a[0].text)
	for _, x := range a[1:] {
		n = min(n, len(x.text))
	}
	return n
}

// foldString maps each rune of s to the smallest rune that Unicode simple
// case folding makes equal to it, the equivalence (?i) matches under. Two
// strings match case-insensitively exactly when their foldStrings are
// equal, so containment survives the mapping. Lowercasing would not do: (?i)s
// matches the long s (U+017F), which is already lowercase.
func foldString(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return least
	}, s)
}
