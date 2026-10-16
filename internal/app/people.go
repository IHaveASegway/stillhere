package app

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// A reader names people as they see them: a display name from a page, a
// GitHub login, an id (p-…) from an MCP answer, or an email from JSON. The
// run keys people by canonical email. FindPerson translates between the
// two for every inbound adapter, the MCP server and the command line alike
// (PF-7), so a person named one way on one reaches the same person on the
// other, and every miss says what to pass instead.

// maxCandidates is how many candidates a refusal lists.
const maxCandidates = 10

// PersonQuery is how one reader names people back: how a candidate is
// named so that it can be passed again, and in which words.
type PersonQuery struct {
	// ID names a candidate in a refusal so the reader can pass it back:
	// the email for a reader that may see emails, else its id
	// (identity.Pseudonym); nil: the id. A GitHub account keyed by its
	// login is named by the login either way.
	ID func(NamedPerson) string
	// Again is how the reader names one of several candidates on its next
	// try, the start of a sentence: "Call again with name set to" (MCP),
	// "Pass --person" (the command line).
	Again string
	// Commands is the reader's vocabulary for the commands a refusal sends
	// it to; nil: the command line's.
	Commands report.Commands
}

// personMiss is a person the run does not know: its message says what to
// pass instead, and errors.Is finds domain.ErrNotFound in it without the
// sentinel's text at the end of the sentence.
type personMiss struct{ msg string }

func (e *personMiss) Error() string { return e.msg }
func (e *personMiss) Unwrap() error { return domain.ErrNotFound }

// FindPerson resolves query, how a reader names a person, against a run's
// people (n): an id from an earlier answer (identity.Pseudonym), an email,
// a GitHub login, without case, as GitHub compares logins (a reviewer the
// run keys by login, or the person the run links the login to,
// NamedPerson.Logins, the links every metric that reads reviews counts by),
// or a display name as the reports print it, compared without case. A name
// that matches no one exactly may match one person by the start of each of
// its words ("lena" for Lena Lead), which the caveat it returns says. Two
// people with one name are a *domain.UsageError that lists both with their
// ids and logins; a name no one has is an error that wraps
// domain.ErrNotFound and suggests near names.
func FindPerson(n *Names, query string, q PersonQuery) (NamedPerson, *report.Caveat, error) {
	cmds := report.CommandsOr(q.Commands)
	query = strings.TrimSpace(query)
	run := n.Run.ID
	if query == "" {
		return NamedPerson{}, nil, &domain.UsageError{Msg: "name a person: a display name as the reports print it, " +
			"a GitHub login (@name), or an id (p-…) from an earlier answer"}
	}
	lower := strings.ToLower(query)
	switch {
	case identity.IsPseudonym(lower):
		for _, p := range n.People {
			if !identity.LoginKey(p.Key) && identity.Pseudonym(p.Key) == lower {
				return p, nil, nil
			}
		}
		return NamedPerson{}, nil, &personMiss{fmt.Sprintf("id %s names no one in run %d: ids are stable across runs, so "+
			"this person is not in this one.", lower, run)}
	case strings.Contains(lower, "@") && !strings.ContainsAny(lower, " \t"):
		// Every key is lowercased where it is made (an email by the
		// collectors, a login by identity.Reviewer), so a lowercased query
		// compares without case.
		for _, p := range n.People {
			if p.Key == lower {
				return p, nil, nil
			}
		}
		if login, ok := strings.CutPrefix(lower, "@"); ok && identity.LoginKey(lower) {
			for _, p := range n.People {
				if slices.ContainsFunc(p.Logins, func(l string) bool { return strings.EqualFold(l, login) }) {
					return p, nil, nil
				}
			}
		}
		return NamedPerson{}, nil, &personMiss{fmt.Sprintf("%q is no one's email or GitHub login in run %d: people are "+
			"keyed by their canonical email, after .mailmap, and a login names whoever the run's GitHub data links it to. "+
			"Pass a display name instead.", lower, run)}
	}
	want := personNorm(query)
	var exact, partial []NamedPerson
	for _, p := range n.People {
		name := personNorm(p.Name)
		switch {
		case name == want:
			exact = append(exact, p)
		case wordPrefixes(want, name):
			partial = append(partial, p)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil, nil
	case len(exact) > 1:
		return NamedPerson{}, nil, &domain.UsageError{Msg: fmt.Sprintf(
			"%d people are named %q in run %d: %s. %s one of the ids. If they are one person, a .mailmap line merges "+
				"them (%s suggests it).", len(exact), query, run, listPeople(exact, q), q.Again, cmds.Identities())}
	case len(partial) == 1:
		p := partial[0]
		return p, &report.Caveat{Code: "name-match", Message: fmt.Sprintf(
			"%q matched %q by the start of each word: pass the full name to be exact.", query, p.Name)}, nil
	case len(partial) > 1:
		return NamedPerson{}, nil, &domain.UsageError{Msg: fmt.Sprintf(
			"%q could be %d people in run %d: %s. %s a full name or an id.",
			query, len(partial), run, listPeople(partial, q), q.Again)}
	}
	msg := fmt.Sprintf("No one in run %d is named %q.", run, query)
	if near := nearPeople(n, want); len(near) > 0 {
		msg += " Did you mean " + listPeople(near, q) + "?"
	}
	// Said on every miss, so it confirms nothing about this one: a name
	// the reports withhold is no name to match (report.SubjectOf).
	return NamedPerson{}, nil, &personMiss{fmt.Sprintf("%s Names are display names as the reports print them, after "+
		".mailmap; %s lists the people with the most surviving lines. A display name that gives an address away (an "+
		"address, or its local part) is withheld from every answer and matches no one: pass that person's id or email (%s).",
		msg, cmds.Report(), cmds.PersonKeys())}
}

// personNorm folds a name for comparison: lowercased, with runs of white
// space as one space.
func personNorm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// wordPrefixes reports whether every word of query starts a different word
// of name: "lena" and "le lead" for "lena lead", not "ena".
func wordPrefixes(query, name string) bool {
	words := strings.Fields(name)
	for _, q := range strings.Fields(query) {
		i := slices.IndexFunc(words, func(w string) bool { return strings.HasPrefix(w, q) })
		if i < 0 {
			return false
		}
		words = slices.Delete(words, i, i+1)
	}
	return true
}

// nearPeople returns the people whose name, or a word of it, is within a
// few edits of query: typos, not guesses.
func nearPeople(n *Names, query string) []NamedPerson {
	type near struct {
		p    NamedPerson
		dist int
	}
	limit := max(1, len([]rune(query))/4)
	var out []near
	for _, p := range n.People {
		if p.Name == "" {
			continue // no name a report shows, so none to be near: such a person is passed by id
		}
		name := personNorm(p.Name)
		best := levenshtein(query, name)
		for _, w := range strings.Fields(name) {
			best = min(best, levenshtein(query, w))
		}
		if best <= limit {
			out = append(out, near{p, best})
		}
	}
	slices.SortFunc(out, func(a, b near) int { return cmp.Or(cmp.Compare(a.dist, b.dist), cmp.Compare(a.p.Name, b.p.Name)) })
	var people []NamedPerson
	for i := 0; i < len(out) && i < 5; i++ {
		people = append(people, out[i].p)
	}
	return people
}

// levenshtein is the edit distance between two strings, by rune.
func levenshtein(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur := make([]int, len(y)+1)
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(y)]
}

// listPeople describes candidates so a reader can tell them apart and pick
// one: the name, the id to pass (PersonQuery.ID), the GitHub logins the run
// links to them, which a reader may know them by, unless one gives an
// address away (NamedPerson.ShownLogins), and what they hold. Names come
// from the repositories, so each is quoted: in stillhere's own sentence,
// repository text stays data.
func listPeople(people []NamedPerson, q PersonQuery) string {
	id := q.ID
	if id == nil {
		id = func(p NamedPerson) string {
			if identity.LoginKey(p.Key) {
				return p.Key
			}
			return identity.Pseudonym(p.Key)
		}
	}
	var parts []string
	for i, p := range people {
		if i == maxCandidates {
			parts = append(parts, fmt.Sprintf("and %d more", len(people)-maxCandidates))
			break
		}
		about := []string{"id " + id(p)}
		for _, l := range p.ShownLogins {
			about = append(about, "login @"+l)
		}
		if p.Bot {
			about = append(about, "a bot")
		}
		if p.Lines > 0 {
			about = append(about, fmt.Sprintf("%d surviving prod lines", p.Lines))
		}
		if len(p.Repos) > 0 {
			repos := p.Repos
			more := ""
			if len(repos) > 3 {
				repos, more = repos[:3], fmt.Sprintf(" and %d more", len(p.Repos)-3)
			}
			quoted := make([]string, len(repos))
			for i, r := range repos {
				quoted[i] = strconv.Quote(r)
			}
			about = append(about, "in "+strings.Join(quoted, ", ")+more)
		}
		parts = append(parts, fmt.Sprintf("%q (%s)", p.Name, strings.Join(about, ", ")))
	}
	return strings.Join(parts, "; ")
}
