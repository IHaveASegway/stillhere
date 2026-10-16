package mcp

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/redact"
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
//     patch.
//
// Finding an address is internal/redact's rule (redact.Addresses,
// redact.Place), which the command line's pages apply too: one rule for
// every output, so the pages and the egress cannot drift apart. What
// replaces an address is this server's: the stable id below, where a page
// prints a fixed label.

// pseudonym is the id that stands in for an email (identity.Pseudonym):
// stable, so an agent can join two answers on it and pass it back to
// person, and the command line takes it too.
func pseudonym(email string) string { return identity.Pseudonym(email) }

// isPseudonym reports whether s has the form of a pseudonym.
func isPseudonym(s string) bool { return identity.IsPseudonym(s) }

// withholder replaces emails by their ids in what leaves the server. Its
// vocabulary is the run's people; without one (an answer that reads no run)
// only the structure and the shape of an address are known.
type withholder struct {
	addrs *redact.Addresses
}

// newWithholder returns the withholder of a run's vocabulary; names may be
// nil.
func newWithholder(names *app.Names) *withholder {
	var emails []string
	if names != nil {
		for _, p := range names.People {
			// A login is no address; an empty local part (@example.com) is
			// one, and gets an id like any other.
			if !identity.LoginKey(p.Key) {
				emails = append(emails, p.Key)
			}
		}
	}
	return &withholder{addrs: redact.NewAddresses(emails)}
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
	return w.addrs.Replace(s, path, pseudonym)
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

// json rewrites every string of a JSON document, object names included,
// token by token, so the document keeps its order and shape
// (redact.RewriteJSON): a person's email by the structural layer, a path by
// the vocabulary, and any other string by the vocabulary and the shape of
// an address.
func (w *withholder) json(doc []byte) ([]byte, error) {
	return redact.RewriteJSON(doc, func(p redact.Place, s string) string {
		if p.Email() {
			return w.key(s)
		}
		return w.text(s, p.Path())
	})
}
