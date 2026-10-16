package report

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// Email addresses on a page (docs/spec/report-json.md, "Where emails go"). A
// published document holds a person's email only where the shape says
// (redact.Place.Email: a member named email, a person subject's key), and
// text and Markdown print none of those. Repository text can hold one
// anywhere else: a commit subject that thanks a teammate or quotes a
// customer, a git message, a directory's name. Every renderer withholds
// such an address, each found by the one rule the MCP server's egress
// applies too (redact.Addresses: the page's people's addresses anywhere,
// and in text that is not a path whatever looks like one), and prints
// addressLabel in its place.
//
// It is done here, at the render boundary, and not in the model: the MCP
// server reads the same model and applies the rule with its own operator,
// a stable id, or none with --allow-emails. And it is done over the
// document's published shape, every string of it, rather than at each
// place a renderer prints repository text: a field added later, or printed
// in a new place, passes the rule without anyone remembering to call it.
// Text and Markdown render a copy in which the rule has replaced what it
// found; a page with no address renders from the document itself.

// addressLabel is what every format prints in place of an email address
// found outside the fields that hold a person's email: a fixed label, so
// the page tells nothing about the address, not even which of two it was.
const addressLabel = "(address withheld)"

// encodeDocument encodes d as JSON in the rules of JSON: fields in
// declaration order, map keys sorted, invalid UTF-8 as U+FFFD, a number
// that is not finite as null.
func encodeDocument(d Document, opts ...jsontext.Options) ([]byte, error) {
	return json.Marshal(d, append([]jsontext.Options{
		json.Deterministic(true),
		jsontext.AllowInvalidUTF8(true),
		json.WithMarshalers(json.MarshalToFunc(finiteOrNull)),
	}, opts...)...)
}

// knower is a document that carries its run's addresses (#90): every
// address the run shows someone by, which its builder read from People, so
// that a page withholds a person's address wherever repository text holds
// it, whether or not the person is on the page, as the MCP server's egress
// withholds over the run's people. A document that does not carry them is
// withheld by the vocabulary of its own people alone.
type knower interface{ runAddresses() []string }

// knownOf returns d's run's addresses, nil when d carries none.
func knownOf(d Document) []string {
	if k, ok := d.(knower); ok {
		return k.runAddresses()
	}
	return nil
}

// withholdAddresses returns doc, a document encoded as JSON under opts, with
// every email address outside the fields that hold a person's email
// replaced by addressLabel, and whether it replaced any. The vocabulary is
// the run's addresses, known, and the page's own people: the addresses at
// those fields, logins left out.
func withholdAddresses(doc []byte, known []string, opts ...jsontext.Options) ([]byte, bool, error) {
	emails := slices.Clone(known)
	err := redact.JSONStrings(doc, func(p redact.Place, s string) {
		if p.Email() && !identity.LoginKey(s) {
			emails = append(emails, s)
		}
	})
	if err != nil {
		return nil, false, err
	}
	addrs := redact.NewAddresses(emails)
	withheld := false
	label := func(string) string {
		withheld = true
		return addressLabel
	}
	out, err := redact.RewriteJSON(doc, func(p redact.Place, s string) string {
		if p.Email() {
			return s
		}
		return addrs.Replace(s, p.Path(), label)
	}, opts...)
	if err != nil || !withheld {
		return doc, false, err
	}
	return out, true, nil
}

// page returns d as text and Markdown print it: d itself when it holds no
// address outside the fields for one, and otherwise a copy, decoded from
// its JSON once the rule has replaced each such address with addressLabel.
// The copy is the document as JSON gives it, which every renderer reads
// alike: a nil list is empty, and a number that was not finite is 0.
func page(d Document) (Document, error) {
	doc, err := encodeDocument(d)
	if err != nil {
		return nil, fmt.Errorf("report: encode: %w", err)
	}
	doc, withheld, err := withholdAddresses(doc, knownOf(d))
	if err != nil {
		return nil, fmt.Errorf("report: withhold addresses: %w", err)
	}
	if !withheld {
		return d, nil
	}
	t := reflect.TypeOf(d)
	if t.Kind() != reflect.Pointer {
		return nil, fmt.Errorf("report: withhold addresses: %T is not a pointer", d)
	}
	copied := reflect.New(t.Elem()).Interface()
	if err := json.Unmarshal(doc, copied, jsontext.AllowDuplicateNames(true)); err != nil {
		return nil, fmt.Errorf("report: withhold addresses: %w", err)
	}
	return copied.(Document), nil
}
