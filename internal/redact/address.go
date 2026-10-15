package redact

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Email addresses in what stillhere prints and sends: the one rule for
// finding them, which the command line's pages and the MCP server's egress
// both apply (docs/spec/report-json.md, "Where emails go";
// docs/spec/mcp.md, "Emails are withheld at the egress"). What replaces an
// address is the caller's: the pages print a fixed label, the MCP server a
// stable id. That is how data-loss-prevention tools split the work, a
// detector that finds and an operator that transforms (Microsoft's
// Presidio: its analyzer and its anonymizer; Google Cloud's Sensitive Data
// Protection: infoType detectors and transformations).
//
// The rule lives here, not in a package of its own, because this package
// is already where stillhere keeps what must not reach an output, free text
// included: credentials in URLs and git messages. Its readers, the report's
// renderers and the MCP server, already import it, and it is pure string
// work with no dependency, so the address rule needs no new place in the
// dependency rule (docs/spec/codebase.md#the-dependency-rule). A package of
// its own would hold the same kind of code under the same import rules,
// and one more name to keep in step with the lint config. The two rules
// stay apart in their functions: Scrub keeps every address (an address is
// how a person is keyed, and facts.db stores it), and nothing here touches
// a credential.
//
// Addresses are found in three layers, from the most certain to the least:
//
//  1. Structure. A published document holds a person's canonical email in
//     a member named email, or in the key of an object whose kind is
//     person, and nowhere else (Place.Email). The caller decides what such
//     a value becomes.
//  2. Vocabulary. Every address the caller knows, a run's people, is found
//     wherever it appears in a string, as a whole address: a warning that
//     quotes an author, a commit subject that names one, an error. Paths
//     get this layer only.
//  3. Shape. What still looks like an address in text that is not a path is
//     found too: an address no one in the run has, such as a customer's in
//     a commit subject. Its domain must end in a label of letters, so a
//     version pin (lodash@4.17.21, actions/setup-go@v5.0.1) or a metric id
//     (durable@1) is left alone, and an address followed by a colon and a
//     path is an scp-like remote (git@github.com:o/r.git), not an email.

// emailShape is the third layer: what looks like an email in free text. The
// local part takes letters and digits in any script and RFC 5322's atext,
// less the characters that delimit an address in text (/ = ? ` { | }); the
// domain is dotted labels ending in one of letters only (a top-level
// domain, or its punycode form), or an IP literal in brackets. A dotless
// domain (root@localhost) is left to the first two layers: in source text
// pkg@latest is far more common than a person's address on a host name.
var emailShape = regexp.MustCompile(`[\p{L}\p{M}\p{N}._%+'!#$&*^~-]+@(?:(?:[\p{L}\p{M}\p{N}](?:[\p{L}\p{M}\p{N}-]*[\p{L}\p{M}\p{N}])?\.)+(?:\p{L}{2,}|[Xx][Nn]--[A-Za-z0-9-]+)|\[[0-9A-Fa-f:.]+\])`)

// localRune reports whether r can be part of an email's local part, as
// emailShape reads one.
func localRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || strings.ContainsRune("._%+'!#$&*^~-", r)
}

// domainRune reports whether r can be part of a domain name.
func domainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '-' || r == '.'
}

// Addresses finds email addresses in text: those of its vocabulary, and
// whatever looks like one. A nil *Addresses has no vocabulary and finds by
// shape alone.
type Addresses struct {
	known  map[string]bool     // every address of the vocabulary, lowercased
	exotic map[string][]string // domain → the vocabulary's local parts that emailShape's class does not cover, such as a/b
}

// NewAddresses returns the finder of a vocabulary: addresses as a run keys
// people, in any case. An entry without an @ or with nothing after it is
// no address and is left out; an empty local part (@example.com) is one. A
// GitHub login ("@name", identity.LoginKey) is no address either: callers
// leave logins out, since this package cannot tell one from a domain.
func NewAddresses(emails []string) *Addresses {
	a := &Addresses{known: map[string]bool{}, exotic: map[string][]string{}}
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		at := strings.LastIndexByte(e, '@')
		if at < 0 || at == len(e)-1 || a.known[e] {
			continue
		}
		a.known[e] = true
		local, domain := e[:at], e[at+1:]
		if strings.IndexFunc(local, func(r rune) bool { return !localRune(r) }) >= 0 {
			a.exotic[domain] = append(a.exotic[domain], local)
		}
	}
	for _, locals := range a.exotic {
		// The longest first, so that x/a/b@ is found whole and not as a/b@
		// after an x/ that would be left printed.
		slices.SortFunc(locals, func(x, y string) int { return cmp.Or(cmp.Compare(len(y), len(x)), cmp.Compare(x, y)) })
	}
	return a
}

// Replace returns s with each address it finds replaced by repl, which gets
// the address as s spells it: every address of the vocabulary, and unless
// s is a path, whatever else looks like one. A path keeps an address no one
// in the vocabulary has, so that it stays the repository's path, which a
// reader can pass back. The shape layer reads what the vocabulary's
// replacements leave, so repl returns no @.
func (a *Addresses) Replace(s string, path bool, repl func(address string) string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	s = a.byVocabulary(s, repl)
	if path {
		return s
	}
	return byShape(s, repl)
}

// byVocabulary replaces each address of the vocabulary in s, as a whole
// address: not the end of a longer local part (x+lena@...) nor the start of
// a longer domain (...@example.com.au).
func (a *Addresses) byVocabulary(s string, repl func(string) string) string {
	if a == nil || len(a.known) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		at := strings.IndexByte(s[i:], '@')
		if at < 0 {
			break
		}
		at += i
		start, end, ok := a.knownAt(s, at)
		if !ok || start < last {
			i = at + 1
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(repl(s[start:end]))
		last, i = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// knownAt finds an address of the vocabulary whose @ is at s[at], and
// returns where it starts and ends.
func (a *Addresses) knownAt(s string, at int) (start, end int, ok bool) {
	// The domain: the run of domain characters after the @, less a full
	// stop that ends a sentence; or an IP literal.
	end = at + 1
	if end < len(s) && s[end] == '[' {
		bracket := strings.IndexByte(s[end:], ']')
		if bracket < 0 {
			return 0, 0, false
		}
		end += bracket + 1
	} else {
		for end < len(s) {
			r, n := utf8.DecodeRuneInString(s[end:])
			if !domainRune(r) {
				break
			}
			end += n
		}
		for end > at+1 && s[end-1] == '.' {
			end--
		}
	}
	if end == at+1 {
		return 0, 0, false
	}
	domain := strings.ToLower(s[at+1 : end])
	// The local part: the run of local-part characters before the @, or
	// the part of it after a character that rarely continues one, such as
	// the quote that opens 'lena@example.com'. Not after . _ % + or -:
	// x+lena@example.com is another address.
	run := at
	for run > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:run])
		if !localRune(r) {
			break
		}
		run -= n
	}
	for start = run; start < at; {
		if a.known[strings.ToLower(s[start:at])+"@"+domain] {
			return start, end, true
		}
		next := strings.IndexFunc(s[start:at], func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) && !strings.ContainsRune("._%+-", r)
		})
		if next < 0 {
			break
		}
		_, n := utf8.DecodeRuneInString(s[start+next:])
		start += next + n
	}
	// A local part with characters outside that run, such as a/b: as a
	// whole, after a character that cannot continue it.
	for _, local := range a.exotic[domain] {
		start := at - len(local)
		if start < 0 || !strings.EqualFold(s[start:at], local) {
			continue
		}
		if r, _ := utf8.DecodeLastRuneInString(s[:start]); start == 0 || !localRune(r) {
			return start, end, true
		}
	}
	// An address with an empty local part (@example.com): only where no
	// local-part character comes before the @, so it is never the end of
	// another address.
	if a.known["@"+domain] && run == at {
		return at, end, true
	}
	return 0, 0, false
}

// byShape is the third layer: it replaces what looks like an email in s,
// unless the address begins an scp-like remote (user@host:path).
func byShape(s string, repl func(string) string) string {
	locs := emailShape.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if next, _ := utf8.DecodeRuneInString(s[loc[1]:]); loc[1] < len(s) &&
			(unicode.IsLetter(next) || unicode.IsDigit(next) || next == '-' || next == '_') {
			continue // the last label goes on, as in name@host.v2: no top-level domain, so no address
		}
		if scpAt(s, loc[1]) {
			continue
		}
		// A local part never starts with a full stop, and a quote before
		// an address quotes it: 'bob@corp.example' is bob's.
		start := loc[0]
		for start < loc[1] && (s[start] == '\'' || s[start] == '.') {
			start++
		}
		b.WriteString(s[last:start])
		b.WriteString(repl(s[start:loc[1]]))
		last = loc[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// scpAt reports whether the address that ends at s[end] is the user and
// host of an scp-like remote: a colon follows, and then a path, not a space
// or the end of the text ("by colon@corp.example: fixed" names a person).
func scpAt(s string, end int) bool {
	if end+1 >= len(s) || s[end] != ':' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[end+1:])
	return !unicode.IsSpace(r)
}

// Place is where a string sits in a JSON document, as RewriteJSON and
// JSONStrings hand it over.
type Place struct {
	Member string // the object member whose value the string is; "" in an array or at the top
	Name   bool   // the string is an object member's name, not a value
	Person bool   // the string's object has a member kind, before it, whose value is "person"
}

// Email reports whether a published document holds a person's canonical
// email at p, the first layer: the value of a member named email, or of
// the key of an object whose kind is person (docs/spec/report-json.md,
// "Where emails go"). No other place may hold an address.
func (p Place) Email() bool { return !p.Name && (p.Member == "email" || p.Member == "key" && p.Person) }

// Path reports whether p holds a repository's path (path, old_path), which
// gets the vocabulary layer only: see Addresses.Replace.
func (p Place) Path() bool { return !p.Name && (p.Member == "path" || p.Member == "old_path") }

// frame is one open object or array of the document walkJSON reads.
type frame struct {
	object   bool
	wantName bool   // an object's next string is a member name
	member   string // the member whose value is being read
	person   bool   // the object's kind is person
}

// RewriteJSON returns doc with every string, member names included, passed
// through fn, token by token, so the document keeps its order and shape.
// opts go to the encoder, such as an indent; invalid UTF-8 reads and writes
// as U+FFFD, and a member name a rewrite makes twice in one object is kept
// twice. The result has no newline after the value, so a document whose
// strings fn leaves alone comes back byte for byte under the options it was
// written with.
func RewriteJSON(doc []byte, fn func(p Place, s string) string, opts ...jsontext.Options) ([]byte, error) {
	var out bytes.Buffer
	opts = append([]jsontext.Options{jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true)}, opts...)
	if err := walkJSON(doc, fn, jsontext.NewEncoder(&out, opts...)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// JSONStrings calls fn with every string of a JSON document, member names
// included, and its place, in document order.
func JSONStrings(doc []byte, fn func(p Place, s string)) error {
	return walkJSON(doc, func(p Place, s string) string { fn(p, s); return s }, nil)
}

// walkJSON reads doc token by token, passes every string through fn with its
// place, and writes each token to enc, if not nil.
func walkJSON(doc []byte, fn func(p Place, s string) string, enc *jsontext.Encoder) error {
	dec := jsontext.NewDecoder(bytes.NewReader(doc), jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))
	var stack []frame
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		switch kind := tok.Kind(); {
		case kind == '}' || kind == ']':
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				stack[len(stack)-1].wantName = stack[len(stack)-1].object
			}
		case top != nil && top.object && top.wantName:
			top.member, top.wantName = tok.String(), false
			tok = jsontext.String(fn(Place{Name: true, Person: top.person}, top.member))
		case kind == '{' || kind == '[':
			stack = append(stack, frame{object: kind == '{', wantName: kind == '{'})
		default:
			if kind == '"' {
				s := tok.String()
				p := Place{}
				if top != nil && top.object {
					p.Member, p.Person = top.member, top.person
					if top.member == "kind" {
						top.person = s == "person"
					}
				}
				tok = jsontext.String(fn(p, s))
			}
			if top != nil && top.object {
				top.wantName = true
			}
		}
		if enc != nil {
			if err := enc.WriteToken(tok); err != nil {
				return err
			}
		}
	}
}
