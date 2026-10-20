package report

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/identity"
)

// How a page names a person. Emails are the join key of the JSON and are
// printed in no text or Markdown page (docs/spec/report-json.md, "Where
// emails go"), and no format prints a part of one outside the fields that
// hold them: a display name that gives an address of the run away is
// withheld in every format, JSON included, so a reader that drops the email
// fields, such as the MCP server without --allow-emails, sends no address
// under another field's name. Whether a name gives an address away is
// identity's decision (identity.Directory.Withheld), made per name over
// every person's address in the run, which the identities answer asks too:
// this file decides only how a page shows the person instead.

// personName is how every page names the person keyed key, as the two
// fields every person row and subject carries, Name and NameWithheld. A
// row sets both from this one call: a row that took its name some other
// way would leave NameWithheld false and label a withheld person as one
// git records without a name. The rule:
//
//   - an unlinked GitHub account's key (identity.LoginKey), "@login" or
//     "@login[bot]", is shown as that login (ADR 0004): it has no display
//     name, and a login is what GitHub itself shows of an account. The key
//     is the login lowercased, as GitHub compares logins; the name is the
//     login in the account's own case when the run's GitHub data carries it
//     (People.Name: "@Zoe" for the key "@zoe"), else the key;
//   - a display name that gives an address of the run away
//     (identity.Directory.Withheld: it holds an @, or it is the local part
//     of a person's address in the run, as GitHub names a commit by the
//     login of an account without a profile name) is "", and withheld is
//     true;
//   - otherwise the display name is shown, and it is "" when git recorded
//     none.
//
// A person shown with "" is told apart from others on a page by a label of
// the page's own (printer.person), never by anything derived from the
// email: an unkeyed hash of an address lets anyone who guesses the address
// confirm it with one hash (ENISA, "Pseudonymisation techniques and best
// practices", 2019, §7.2). who may be nil: then no person has a name.
func personName(who People, key string) (name string, withheld bool) {
	switch {
	case identity.LoginKey(key):
		if who != nil {
			if n := who.Name(key); n != "" {
				return n, false
			}
		}
		return key, false
	case who == nil:
		return "", false
	case who.Withheld(key):
		return "", true
	}
	return who.Name(key), false
}

// nameWithheld is personName's withheld: whether a page names the person
// with "" because their display name gives an address away.
func nameWithheld(who People, key string) bool {
	_, withheld := personName(who, key)
	return withheld
}

// pageNames labels, on one page, the people it cannot name: a counter per
// kind of label, and the label each person got, so the same person keeps
// one label everywhere on the page and two such people stay apart. A label
// is only the person's place among them on the page, in the order the page
// first names them: it says nothing about the address, matches nothing
// outside the page, and is gone once the page is written, so there is no
// table to keep (ENISA 2019, §5.1.1 and §7.1, the counter technique;
// Microsoft Presidio's instance-counter pseudonymisation, "<PERSON_0>", is
// the same idea). Every renderer of one document holds one, through its
// printer, and pages render in a fixed order, so the labels are as
// deterministic as the rest of the output. No name a page prints can be
// read as a label (nameText).
type pageNames struct {
	labels   map[string]string // a person's key → their label on the page
	unnamed  int               // labels given to people git records without a name
	withheld int               // labels given to people whose name gives an address away
}

// person is how a page names a person: their display name, printed by
// nameText, or, when the page may show none (personName's name is ""),
// their label on the page: "(no name 1)" for a person git records without
// a name, "(name withheld 1)" for one whose name gives an address away,
// numbered in the order the page first names them. A label is plain text
// that needs no escaping.
//
// A Markdown page that links (Links) links the name, or the label, to the
// person's page.
func (p *printer) person(key, name string, withheld bool, esc func(string) string) string {
	return p.mdLink(p.personLabel(key, name, withheld, esc), Target{Kind: TargetPerson, Key: key})
}

// personLabel is person without the link.
func (p *printer) personLabel(key, name string, withheld bool, esc func(string) string) string {
	if name != "" {
		return nameText(name, esc)
	}
	if label, ok := p.names.labels[key]; ok {
		return label
	}
	var label string
	if withheld {
		p.names.withheld++
		label = fmt.Sprintf("(name withheld %d)", p.names.withheld)
	} else {
		p.names.unnamed++
		label = fmt.Sprintf("(no name %d)", p.names.unnamed)
	}
	if p.names.labels == nil {
		p.names.labels = map[string]string{}
	}
	p.names.labels[key] = label
	return label
}

// nameText prints a name, escaped by esc. git records any name, so a
// committer can be called "(no name 1)": a name that could be read as a
// label a page gives a person it cannot name, or as a name quoted for that
// reason (labelLike), is printed quoted, as Go quotes a string. A label
// and a name then never print alike, and neither do two names unless the
// names are alike, so two people stay apart on every page; JSON keeps the
// name as it is.
func nameText(name string, esc func(string) string) string {
	if c := clean(name); labelLike(c) {
		return esc(strconv.Quote(c))
	}
	return esc(name)
}

// labelLike reports whether a name, as clean prints it, starts with a
// double quote, or has a label's shape in any case: "(no name)" or "(name
// withheld)", alone or with a number, as the person rows and the
// identities answer label people.
func labelLike(s string) bool {
	if strings.HasPrefix(s, `"`) {
		return true
	}
	inner, ok := strings.CutPrefix(s, "(")
	if !ok {
		return false
	}
	if inner, ok = strings.CutSuffix(inner, ")"); !ok {
		return false
	}
	for _, kind := range []string{"no name", "name withheld"} {
		if len(inner) < len(kind) || !strings.EqualFold(inner[:len(kind)], kind) {
			continue
		}
		rest := inner[len(kind):]
		if rest == "" {
			return true
		}
		n, ok := strings.CutPrefix(rest, " ")
		return ok && n != "" && strings.Trim(n, "0123456789") == ""
	}
	return false
}
