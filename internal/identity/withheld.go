package identity

import (
	"strings"
	"unicode"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file decides which display names a page may show (#52,
// docs/spec/report-json.md, "Where emails go"). Text and Markdown print no
// address, and no format prints a part of one outside the fields that hold
// emails, so a display name that gives an address away is withheld in
// every format: one that holds an @, as an address typed as a name does,
// or that is the local part of a person's address, as GitHub names a
// commit by the account's login when the account has no profile name.
//
// The decision is the name's, made over every person's address in the
// run, and not the address's alone: when jdoe@corp.example and
// john.doe@example.org both carry the name jdoe, the name is withheld on
// both rows, or the page would print on one row the local part it
// withholds on the other. The directory holds every address the facts
// show, so the rule is its own (Information Expert: Larman, "Applying UML
// and Patterns", ch. 17), and the report's rows, its caveats and the
// identities answer all ask it instead of restating it.

// LoginKey reports whether a person key is a GitHub account that no commit
// of the run links to an email: "@" and the login, as reviews_given@1 keys
// a reviewer it cannot link, or "@login[bot]" for an app (ADR 0004). A
// login is letters, digits, hyphens and, for an enterprise's managed
// users, underscores, so it holds no dot and no second @: an email with an
// empty local part ("@example.com") is no login.
func LoginKey(key string) bool {
	login, ok := strings.CutPrefix(key, "@")
	login = strings.TrimSuffix(login, appSuffix)
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

// Withheld reports whether the display name of email gives away an address
// of the run, so that no page shows it, in any format: the name holds an
// @, as an address typed as a name does, or it is, ignoring case, a local
// part of a person's address the fact set shows (the whole of it, the part
// before a +tag, or a GitHub noreply address's login), as GitHub names a
// commit by the account's login when it has no profile name. "John Doe" on
// john.doe@ is a name, not a local part, and is shown.
//
// Every address that carries a withheld name has it withheld. A name that
// holds an @ is withheld whoever carries it, a bot included, while a bot's
// or an agent's own address withholds no name, since it is no person's:
// dependabot[bot] is its own noreply login and stays its name. An email
// without a display name has nothing to withhold, and a GitHub login
// (LoginKey) is shown as it is: it is the account's public name, which no
// commit of the run links to an address (ADR 0004).
func (d *Directory) Withheld(email string) bool {
	if LoginKey(email) {
		return false
	}
	name := strings.TrimSpace(d.Name(email))
	return name != "" && (strings.ContainsRune(name, '@') || d.locals[fold(name)])
}

// personLocals returns the local-part forms (localForms) of every address
// the fact set shows a person by, folded (fold): commit authors and
// co-authors, and the authors and co-authors of blamed commits, as
// displayNames reads them, with a name or without one. A bot's or an
// agent's address is left out: it is no person's. nil when there is none.
func personLocals(fs *domain.FactSet, d *Directory) map[string]bool {
	if fs == nil {
		return nil
	}
	seen := map[string]bool{}
	var out map[string]bool
	add := func(p domain.Person) {
		email := canonical(p.Email)
		if email == "" || seen[email] {
			return
		}
		seen[email] = true
		if d.Bot(email) {
			return
		}
		for _, f := range localForms(email) {
			if f == "" {
				continue
			}
			if out == nil {
				out = map[string]bool{}
			}
			out[fold(f)] = true
		}
	}
	for i := range fs.Repos {
		r := &fs.Repos[i]
		for j := range r.Commits {
			add(r.Commits[j].Author)
			for _, co := range r.Commits[j].CoAuthors {
				add(co)
			}
		}
		for j := range r.Owners {
			add(r.Owners[j].Author)
			for _, co := range r.Owners[j].CoAuthors {
				add(co)
			}
		}
	}
	return out
}

// localForms returns the forms of an address's local part that a display
// name gives the address away by being: the whole local part, the part
// before a +tag, or a GitHub noreply address's login (12345+login@ is
// login). An address without an @ is all local part. RevealsAddress and
// Withheld both read these, so the address's own rule and the run's agree.
func localForms(email string) []string {
	local, domain := email, ""
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		local, domain = email[:i], email[i+1:]
	}
	forms := []string{local}
	if domain == candNoreply {
		if id, login, ok := strings.Cut(local, "+"); ok && candDigits(id) {
			forms = append(forms, login)
		}
	} else if i := strings.IndexByte(local, '+'); i > 0 {
		forms = append(forms, local[:i])
	}
	return forms
}

// fold maps every rune of s to the smallest rune of its case-folding
// orbit, so two strings are equal after fold exactly when strings.EqualFold
// says they are equal, and a map keyed by fold matches without case as
// RevealsAddress compares.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		lowest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			lowest = min(lowest, f)
		}
		return lowest
	}, s)
}
