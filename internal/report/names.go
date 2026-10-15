package report

import (
	"fmt"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/identity"
)

// How a page names a person. Emails are the join key of the JSON and are
// printed in no text or Markdown page (docs/spec/report-json.md, "Where
// emails go"), and no page prints a part of one either: a display name that
// gives the address away is withheld in every format, JSON included, so a
// reader that drops the email fields, such as the MCP server without
// --allow-emails, sends no address under another field's name. Whether a
// name gives an address away is identity's rule (identity.RevealsAddress),
// which the identities answer applies too: this file decides only how a
// page shows the person instead.

// displayName is the name every page shows a person by, the JSON's name
// fields included:
//
//   - an unlinked GitHub account's key, "@login" or "@login[bot]", is shown
//     as that login (ADR 0004): it has no display name, and a login is what
//     GitHub itself shows of an account;
//   - a bot's or an agent's display name is shown whatever it is
//     (dependabot[bot], Copilot): its address is no person's;
//   - a person's display name is shown unless it gives their address away
//     (identity.RevealsAddress: it holds an @, or it is the address's own
//     local part, as GitHub names a commit by the login of an account
//     without a profile name);
//   - otherwise, and when git recorded no name, it is "".
//
// A person shown with "" is told apart from others on a page by a label of
// the page's own (printer.person), never by anything derived from the
// email: an unkeyed hash of an address lets anyone who guesses the address
// confirm it with one hash (ENISA, "Pseudonymisation techniques and best
// practices", 2019, §7.2). nameWithheld says which of the two reasons
// applies.
func displayName(who People, key string) string {
	name, _ := shownName(who, key)
	return name
}

// nameWithheld reports whether displayName is "" because the person's
// display name gives their address away.
func nameWithheld(who People, key string) bool {
	_, withheld := shownName(who, key)
	return withheld
}

// shownName is displayName and nameWithheld at once.
func shownName(who People, key string) (name string, withheld bool) {
	if loginKey(key) {
		return key, false
	}
	n, ok := realName(who, key)
	switch {
	case !ok:
		return "", false
	case !isBot(who, key) && identity.RevealsAddress(n, key):
		return "", true
	}
	return n, false
}

// loginKey reports whether a person's key is a GitHub account that no
// commit of the run links to an email: "@" and the login, as the forge
// metrics key a reviewer they cannot link, and "@login[bot]" for an app
// (ADR 0004). A login is letters, digits, hyphens and, for an enterprise's
// managed users, underscores, so it holds no dot and no second @: an email
// with an empty local part ("@example.com") is not one.
func loginKey(key string) bool {
	login, ok := strings.CutPrefix(key, "@")
	login = strings.TrimSuffix(login, "[bot]")
	if !ok || login == "" {
		return false
	}
	for _, r := range login {
		if !loginRune(r) {
			return false
		}
	}
	return true
}

// loginRune reports whether r can be part of a GitHub login.
func loginRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

// realName is the identity directory's display name for email, and whether
// it has one. It is the name as git recorded it, whether or not a page may
// show it: the identity caveats compare it, and print only what
// displayName would.
func realName(who People, email string) (string, bool) {
	if who == nil {
		return "", false
	}
	n := who.Name(email)
	return n, n != ""
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
// deterministic as the rest of the output.
type pageNames struct {
	labels   map[string]string // a person's key → their label on the page
	unnamed  int               // labels given to people git records without a name
	withheld int               // labels given to people whose name gives their address away
}

// person is how a page names a person: their display name, escaped by
// esc, or, when the page may show none (displayName is ""), their label on
// the page: "(no name 1)" for a person git records without a name, "(name
// withheld 1)" for one whose name gives their address away, numbered in
// the order the page first names them. A label is plain text that needs no
// escaping.
func (p *printer) person(key, name string, withheld bool, esc func(string) string) string {
	if name != "" {
		return esc(name)
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
