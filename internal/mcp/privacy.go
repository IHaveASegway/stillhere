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
)

// Every answer goes on to the agent's model provider, so what leaves the
// server is minimised by default (ADR 0006): emails only with
// --allow-emails, commit subjects and diffs only with --allow-source, and a
// person's AI-assisted share only for the user the server runs for (ADR
// 0007). The last two are decided where the answers are built; emails are
// withheld here, on the encoded bytes, as the last step before an answer
// leaves: an egress filter that sees every string of every answer and of
// every error, so a field added later, a warning that quotes an author or
// an error that names a person cannot carry an email out.

// emailPattern finds what looks like an email address in free text.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)

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

// withholdEmails replaces every email in s by its pseudonym. The user and
// host that begin an scp-like remote (git@github.com:o/r.git, a repository
// key) are not an email and stay.
func withholdEmails(s string) string {
	locs := emailPattern.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if loc[1] < len(s) && s[loc[1]] == ':' {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(pseudonym(s[loc[0]:loc[1]]))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// encode renders an answer as compact JSON in the rules of
// docs/spec/report-json.md: fields in declaration order, map keys sorted,
// empty lists [] and maps {}, absent sections null, invalid UTF-8 as U+FFFD
// and a number that is not finite as null. Compact, because indentation
// costs an agent tokens and tells it nothing. Unless emails are allowed,
// every string is then passed through withholdEmails.
func encode(v any, allowEmails bool) ([]byte, error) {
	b, err := json.Marshal(v,
		json.Deterministic(true),
		jsontext.AllowInvalidUTF8(true),
		json.WithMarshalers(json.MarshalToFunc(finiteOrNull)),
	)
	if err != nil || allowEmails {
		return b, err
	}
	return withholdJSON(b)
}

// finiteOrNull encodes a NaN or an infinity as null, as report.JSON does.
func finiteOrNull(enc *jsontext.Encoder, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return enc.WriteToken(jsontext.Null)
	}
	return errors.ErrUnsupported
}

// withholdJSON rewrites every string of a JSON document, object names
// included, through withholdEmails, token by token, so the document keeps
// its order and shape.
func withholdJSON(doc []byte) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(doc))
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out)
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if tok.Kind() == '"' {
			if s := tok.String(); emailPattern.MatchString(s) {
				tok = jsontext.String(withholdEmails(s))
			}
		}
		if err := enc.WriteToken(tok); err != nil {
			return nil, err
		}
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}
