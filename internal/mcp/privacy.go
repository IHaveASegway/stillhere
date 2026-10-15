package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/identity"
)

// Every answer goes on to the agent's model provider, so what leaves the
// server is minimised by default (ADR 0006): emails only with
// --allow-emails, commit subjects and diffs only with --allow-source, and a
// person's AI-assisted share only for the user the server runs for (ADR
// 0007). The last two are decided where the answers are built; emails are
// withheld here, on the encoded answer, as the last step before it leaves.
//
// The filter is defence in depth, three layers from the most certain to the
// least (as data-loss-prevention tools combine record transformations by
// field with detectors over free text: Google Cloud's Sensitive Data
// Protection, Microsoft's Presidio):
//
//  1. Structure. The published shapes hold a person's canonical email in a
//     member named email, or in the key of an object whose kind is person
//     (docs/spec/report-json.md, docs/spec/mcp.md). Such a value becomes the
//     person's id whatever it looks like: a dotless domain, an IP literal,
//     a Unicode or quoted local part.
//  2. Vocabulary. Every email the run knows, a person of app.Names, is
//     replaced wherever it appears in a string, as a whole address: a
//     warning that quotes an author, a commit subject that names one, an
//     error. Paths get this layer only.
//  3. Shape. What still looks like an email in text that is not a path is
//     replaced too: an address the run does not know, such as one in a
//     patch. Its domain must end in a label of letters, so a version pin
//     (lodash@4.17.21, actions/setup-go@v5.0.1) or a metric id (durable@1)
//     is left alone, and an address followed by a colon and a path is an
//     scp-like remote (git@github.com:o/r.git), not an email.

// pseudonymPrefix starts every id that stands in for an email.
const pseudonymPrefix = "p-"

// pseudonym is the id that stands in for an email: "p-" and the first ten
// hex digits of the SHA-256 of the canonical (lowercased) email. It is
// stable, across answers, sessions and runs, so an agent can join two
// answers on it and pass it back to person; it is not anonymous: anyone who
// already holds an email can compute its id and confirm it. 40 bits keep
// two people of one organisation apart.
func pseudonym(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return pseudonymPrefix + hex.EncodeToString(sum[:5])
}

// isPseudonym reports whether s has the form of a pseudonym.
func isPseudonym(s string) bool {
	if len(s) != len(pseudonymPrefix)+10 || !strings.HasPrefix(s, pseudonymPrefix) {
		return false
	}
	return strings.Trim(s[len(pseudonymPrefix):], "0123456789abcdef") == ""
}

// emailGuard is the third layer: what looks like an email in free text. The
// local part takes letters and digits in any script and RFC 5322's atext,
// less the characters that delimit an address in text (/ = ? ` { | }); the
// domain is dotted labels ending in one of letters only (a top-level
// domain, or its punycode form), or an IP literal in brackets. A dotless
// domain (root@localhost) is left to the first two layers: in source text
// pkg@latest is far more common than a person's address on a host name.
var emailGuard = regexp.MustCompile(`[\p{L}\p{M}\p{N}._%+'!#$&*^~-]+@(?:(?:[\p{L}\p{M}\p{N}](?:[\p{L}\p{M}\p{N}-]*[\p{L}\p{M}\p{N}])?\.)+(?:\p{L}{2,}|[Xx][Nn]--[A-Za-z0-9-]+)|\[[0-9A-Fa-f:.]+\])`)

// localRune reports whether r can be part of an email's local part, as
// emailGuard reads one.
func localRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || strings.ContainsRune("._%+'!#$&*^~-", r)
}

// domainRune reports whether r can be part of a domain name.
func domainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '-' || r == '.'
}

// withholder replaces emails by their ids in what leaves the server. Its
// vocabulary is the run's people; without one (an answer that reads no run)
// only the structure and the shape of an address are known.
type withholder struct {
	ids    map[string]string   // canonical email → id
	exotic map[string][]string // domain → the run's local parts that emailGuard's class does not cover, such as a/b
}

// newWithholder returns the withholder of a run's vocabulary; names may be
// nil.
func newWithholder(names *app.Names) *withholder {
	w := &withholder{ids: map[string]string{}, exotic: map[string][]string{}}
	if names == nil {
		return w
	}
	for _, p := range names.People {
		at := strings.LastIndexByte(p.Key, '@')
		if identity.LoginKey(p.Key) || at < 0 || at == len(p.Key)-1 {
			continue // a login, or not an address; an empty local part (@example.com) is an address
		}
		email := strings.ToLower(p.Key)
		w.ids[email] = pseudonym(email)
		local, domain := email[:at], email[at+1:]
		if strings.IndexFunc(local, func(r rune) bool { return !localRune(r) }) >= 0 {
			w.exotic[domain] = append(w.exotic[domain], local)
		}
	}
	return w
}

// key is the structural layer: the id of a value that holds a person's
// canonical email. A login (identity.LoginKey: @name, @name[bot]) and an id
// stay as they are; an address with an empty local part (@example.com) is
// an address, and becomes its id.
func (w *withholder) key(v string) string {
	if v == "" || identity.LoginKey(v) || isPseudonym(v) {
		return v
	}
	return pseudonym(v)
}

// text withholds the emails in free text: every address the run knows, and
// unless the text is a path, whatever else looks like an email.
func (w *withholder) text(s string, path bool) string {
	if !strings.Contains(s, "@") {
		return s
	}
	s = w.known(s)
	if path {
		return s
	}
	return guard(s)
}

// known replaces each address of the run's vocabulary in s, as a whole
// address: not the end of a longer local part (x+lena@...) nor the start of
// a longer domain (...@example.com.au).
func (w *withholder) known(s string) string {
	if len(w.ids) == 0 {
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
		start, end, id, ok := w.knownAt(s, at)
		if !ok || start < last {
			i = at + 1
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(id)
		last, i = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// knownAt finds a known address whose @ is at s[at], and returns where it
// starts and ends and its id.
func (w *withholder) knownAt(s string, at int) (start, end int, id string, ok bool) {
	// The domain: the run of domain characters after the @, less a full
	// stop that ends a sentence; or an IP literal.
	end = at + 1
	if end < len(s) && s[end] == '[' {
		bracket := strings.IndexByte(s[end:], ']')
		if bracket < 0 {
			return 0, 0, "", false
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
		return 0, 0, "", false
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
		if id, ok := w.ids[strings.ToLower(s[start:at])+"@"+domain]; ok {
			return start, end, id, true
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
	for _, local := range w.exotic[domain] {
		start := at - len(local)
		if start < 0 || !strings.EqualFold(s[start:at], local) {
			continue
		}
		if r, _ := utf8.DecodeLastRuneInString(s[:start]); start == 0 || !localRune(r) {
			return start, end, w.ids[local+"@"+domain], true
		}
	}
	// An address with an empty local part (@example.com): only where no
	// local-part character comes before the @, so it is never the end of
	// another address.
	if id, ok := w.ids["@"+domain]; ok && run == at {
		return at, end, id, true
	}
	return 0, 0, "", false
}

// guard is the third layer: it replaces what looks like an email in s by
// its id, unless the address begins an scp-like remote (user@host:path).
func guard(s string) string {
	locs := emailGuard.FindAllStringIndex(s, -1)
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
		if scpRemote(s, loc[1]) {
			continue
		}
		// A local part never starts with a full stop, and a quote before
		// an address quotes it: 'bob@corp.example' is bob's.
		start := loc[0]
		for start < loc[1] && (s[start] == '\'' || s[start] == '.') {
			start++
		}
		b.WriteString(s[last:start])
		b.WriteString(pseudonym(s[start:loc[1]]))
		last = loc[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// scpRemote reports whether the address that ends at s[end] is the user and
// host of an scp-like remote: a colon follows, and then a path, not a space
// or the end of the text ("by colon@corp.example: fixed" names a person).
func scpRemote(s string, end int) bool {
	if end+1 >= len(s) || s[end] != ':' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[end+1:])
	return !unicode.IsSpace(r)
}

// encode renders an answer as compact JSON in the rules of
// docs/spec/report-json.md: fields in declaration order, map keys sorted,
// empty lists [] and maps {}, absent sections null, invalid UTF-8 as U+FFFD
// and a number that is not finite as null. Compact, because indentation
// costs an agent tokens and tells it nothing. With a withholder (emails not
// allowed), every string then passes it.
func encode(v any, w *withholder) ([]byte, error) {
	b, err := json.Marshal(v,
		json.Deterministic(true),
		jsontext.AllowInvalidUTF8(true),
		json.WithMarshalers(json.MarshalToFunc(finiteOrNull)),
	)
	if err != nil || w == nil {
		return b, err
	}
	return w.json(b)
}

// finiteOrNull encodes a NaN or an infinity as null, as report.JSON does.
func finiteOrNull(enc *jsontext.Encoder, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return enc.WriteToken(jsontext.Null)
	}
	return errors.ErrUnsupported
}

// frame is one open object or array of the document json rewrites.
type frame struct {
	object   bool
	wantName bool   // an object's next string is a member name
	name     string // the member whose value is being read
	person   bool   // the object's kind is person: its key is a person's
}

// json rewrites every string of a JSON document, object names included,
// token by token, so the document keeps its order and shape: a person's
// key by the structural layer, a path by the vocabulary, and any other
// string by the vocabulary and the shape of an address.
func (w *withholder) json(doc []byte) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(doc), jsontext.AllowInvalidUTF8(true))
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out, jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))
	var stack []frame
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
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
			top.name, top.wantName = tok.String(), false
			tok = jsontext.String(w.text(top.name, false))
		case kind == '{' || kind == '[':
			stack = append(stack, frame{object: kind == '{', wantName: kind == '{'})
		default:
			if kind == '"' {
				tok = jsontext.String(w.value(top, tok.String()))
			}
			if top != nil && top.object {
				top.wantName = true
			}
		}
		if err := enc.WriteToken(tok); err != nil {
			return nil, err
		}
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// value withholds the emails in one string value of a document.
func (w *withholder) value(f *frame, s string) string {
	if f == nil || !f.object {
		return w.text(s, false)
	}
	switch f.name {
	case "kind":
		f.person = s == "person"
		return s
	case "email":
		return w.key(s)
	case "key":
		if f.person {
			return w.key(s)
		}
	case "path", "old_path":
		return w.text(s, true)
	}
	return w.text(s, false)
}
