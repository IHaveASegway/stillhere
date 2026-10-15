package mcp

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// An agent works with names, as people do: "Lena Lead", "payments",
// "sibling". The run keys people by canonical email and repositories by
// remote, which an agent does not have and, for emails, should not get. The
// resolver translates between the two vocabularies at the boundary, the
// adapter's half of an anti-corruption layer (Evans, Domain-Driven Design,
// ch. 14), and makes every miss an error that says what to pass instead
// (Anthropic, "Writing effective tools for agents", 2025).

// maxCandidates is how many candidates an error lists.
const maxCandidates = 10

// notFound is a miss the agent can act on: its message says what to pass
// instead, and errors.Is finds domain.ErrNotFound in it without the
// sentinel's text at the end of the sentence.
type notFound struct{ msg string }

func (e *notFound) Error() string { return e.msg }
func (e *notFound) Unwrap() error { return domain.ErrNotFound }

func notFoundf(format string, args ...any) error { return &notFound{msg: fmt.Sprintf(format, args...)} }

// resolver resolves an agent's names against one run's vocabulary.
type resolver struct {
	names  *app.Names
	emails bool // --allow-emails: a candidate is named by email rather than by id
}

// norm folds a name for comparison: lowercased, with runs of white space
// as one space.
func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// id is how an answer names a person when two share a name: the email with
// --allow-emails, else its pseudonym; a login stays a login.
func (r *resolver) id(p app.NamedPerson) string {
	if r.emails || strings.HasPrefix(p.Key, "@") {
		return p.Key
	}
	return pseudonym(p.Key)
}

// person resolves a person: a display name as the reports print it
// (compared without case), an id from an earlier answer, an email, or a
// login. A name that matches no one exactly may match one person by the
// start of each of its words ("lena" for Lena Lead), which the caveat
// says. Two people with one name are an error that lists both with their
// ids.
func (r *resolver) person(query string) (app.NamedPerson, *report.Caveat, error) {
	q := strings.TrimSpace(query)
	run := r.names.Run.ID
	if q == "" {
		return app.NamedPerson{}, nil, &domain.UsageError{Msg: "name a person: a display name as the reports print it, " +
			"or an id (p-…) from an earlier answer"}
	}
	lower := strings.ToLower(q)
	switch {
	case isPseudonym(lower):
		for _, p := range r.names.People {
			if !strings.HasPrefix(p.Key, "@") && pseudonym(p.Key) == lower {
				return p, nil, nil
			}
		}
		return app.NamedPerson{}, nil, notFoundf("id %s names no one in run %d: ids are stable across runs, so this "+
			"person is not in this one.", lower, run)
	case strings.Contains(lower, "@") && !strings.ContainsAny(lower, " \t"):
		for _, p := range r.names.People {
			if p.Key == lower {
				return p, nil, nil
			}
		}
		return app.NamedPerson{}, nil, notFoundf("%s is no one's email or login in run %d: people are keyed by their "+
			"canonical email, after .mailmap. Pass a display name instead.", lower, run)
	}
	want := norm(q)
	var exact, partial []app.NamedPerson
	for _, p := range r.names.People {
		name := norm(p.Name)
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
		return app.NamedPerson{}, nil, &domain.UsageError{Msg: fmt.Sprintf(
			"%d people are named %q in run %d: %s. Call again with name set to one of the ids. If they are one person, "+
				"a .mailmap line merges them (identity_candidates suggests it).",
			len(exact), q, run, r.listPeople(exact))}
	case len(partial) == 1:
		p := partial[0]
		return p, &report.Caveat{Code: "name-match", Message: fmt.Sprintf(
			"%q matched %s by the start of each word: pass the full name to be exact.", q, p.Name)}, nil
	case len(partial) > 1:
		return app.NamedPerson{}, nil, &domain.UsageError{Msg: fmt.Sprintf(
			"%q could be %d people in run %d: %s. Call again with name set to a full name or an id.",
			q, len(partial), run, r.listPeople(partial))}
	}
	msg := fmt.Sprintf("No one in run %d is named %q.", run, q)
	if near := r.nearPeople(want); len(near) > 0 {
		msg += " Did you mean " + r.listPeople(near) + "?"
	}
	return app.NamedPerson{}, nil, notFoundf("%s Names are display names as the reports print them, after .mailmap; "+
		"overview lists the people with the most surviving lines.", msg)
}

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
func (r *resolver) nearPeople(query string) []app.NamedPerson {
	type near struct {
		p    app.NamedPerson
		dist int
	}
	limit := max(1, len([]rune(query))/4)
	var out []near
	for _, p := range r.names.People {
		name := norm(p.Name)
		best := levenshtein(query, name)
		for _, w := range strings.Fields(name) {
			best = min(best, levenshtein(query, w))
		}
		if best <= limit {
			out = append(out, near{p, best})
		}
	}
	slices.SortFunc(out, func(a, b near) int { return cmp.Or(cmp.Compare(a.dist, b.dist), cmp.Compare(a.p.Name, b.p.Name)) })
	var people []app.NamedPerson
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

// listPeople describes candidates so an agent can tell them apart and
// pick one: the name, the id to pass, and what they hold.
func (r *resolver) listPeople(people []app.NamedPerson) string {
	var parts []string
	for i, p := range people {
		if i == maxCandidates {
			parts = append(parts, fmt.Sprintf("and %d more", len(people)-maxCandidates))
			break
		}
		about := []string{"id " + r.id(p)}
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
			about = append(about, "in "+strings.Join(repos, ", ")+more)
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", p.Name, strings.Join(about, ", ")))
	}
	return strings.Join(parts, "; ")
}

// scope resolves a scope: "" for the whole run; a group name; a repository
// name or key; or, to say which, "group:NAME", "repo:NAME" or
// "GROUP/NAME" for the repository NAME in group GROUP. Names compare
// without case.
func (r *resolver) scope(query string) (domain.Scope, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return domain.Scope{}, nil
	}
	kind := ""
	if k, rest, ok := strings.Cut(q, ":"); ok && (strings.EqualFold(k, "group") || strings.EqualFold(k, "repo")) {
		kind, q = strings.ToLower(k), strings.TrimSpace(rest)
	}
	var found []domain.Scope
	if kind != domain.ScopeRepo {
		for _, g := range r.names.Groups {
			if strings.EqualFold(g.Name, q) {
				found = append(found, domain.Scope{Kind: domain.ScopeGroup, Key: g.Name})
			}
		}
	}
	if kind != domain.ScopeGroup {
		for _, rp := range r.repos(q) {
			found = append(found, domain.Scope{Kind: domain.ScopeRepo, Key: rp.Key})
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return domain.Scope{}, &domain.UsageError{Msg: fmt.Sprintf("%q is no group or repository of run %d. %s",
			query, r.names.Run.ID, r.listScopes())}
	}
	var parts []string
	for _, sc := range found {
		if sc.Kind == domain.ScopeGroup {
			parts = append(parts, "group "+sc.Key)
			continue
		}
		parts = append(parts, r.describeRepo(sc.Key))
	}
	return domain.Scope{}, &domain.UsageError{Msg: fmt.Sprintf(
		"%q names %d scopes of run %d: %s. Pass group:NAME, repo:NAME, GROUP/NAME or a repository key.",
		query, len(found), r.names.Run.ID, strings.Join(parts, "; "))}
}

// repo resolves one repository by name or key, as scope does.
func (r *resolver) repo(query string) (app.NamedRepo, error) {
	q := strings.TrimSpace(query)
	if k, rest, ok := strings.Cut(q, ":"); ok && strings.EqualFold(k, "repo") {
		q = strings.TrimSpace(rest)
	}
	found := r.repos(q)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return app.NamedRepo{}, &domain.UsageError{Msg: fmt.Sprintf("%q is no repository of run %d. %s",
			query, r.names.Run.ID, r.listScopes())}
	}
	var parts []string
	for _, rp := range found {
		parts = append(parts, r.describeRepo(rp.Key))
	}
	return app.NamedRepo{}, &domain.UsageError{Msg: fmt.Sprintf(
		"%q names %d repositories of run %d: %s. Pass GROUP/NAME or the key.",
		query, len(found), r.names.Run.ID, strings.Join(parts, "; "))}
}

// repos finds the repositories a name or key names: a key, the scrubbed
// form of a remote pasted with its credentials, a name, or GROUP/NAME.
func (r *resolver) repos(q string) []app.NamedRepo {
	var found []app.NamedRepo
	scrubbed := redact.URL(q)
	for _, rp := range r.names.Repos {
		if rp.Key == q || rp.Key == scrubbed {
			return []app.NamedRepo{rp}
		}
	}
	for _, rp := range r.names.Repos {
		if strings.EqualFold(rp.Name, q) {
			found = append(found, rp)
		}
	}
	if len(found) == 0 {
		if group, name, ok := strings.Cut(q, "/"); ok {
			for _, rp := range r.names.Repos {
				if rp.Group != "" && strings.EqualFold(rp.Group, group) && strings.EqualFold(rp.Name, name) {
					found = append(found, rp)
				}
			}
		}
	}
	return found
}

// within finds the repository an absolute path is inside, at the path the
// run read it, and the path relative to it: an agent in Claude Code works
// with absolute paths. The innermost repository wins.
func (r *resolver) within(p string) (app.NamedRepo, string, bool) {
	p = filepath.Clean(p)
	sep := string(filepath.Separator)
	var best app.NamedRepo
	var rel string
	found := false
	for _, rp := range r.names.Repos {
		if rp.Path == "" {
			continue
		}
		root := filepath.Clean(rp.Path)
		if p != root && !strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep) {
			continue
		}
		if !found || len(root) > len(filepath.Clean(best.Path)) {
			best, rel, found = rp, strings.TrimPrefix(strings.TrimPrefix(p, root), sep), true
		}
	}
	return best, filepath.ToSlash(rel), found
}

// describeRepo names a repository so two of one name can be told apart.
func (r *resolver) describeRepo(key string) string {
	for _, rp := range r.names.Repos {
		if rp.Key == key {
			if rp.Group != "" {
				return fmt.Sprintf("repository %s in group %s (key %s)", rp.Name, rp.Group, rp.Key)
			}
			return fmt.Sprintf("repository %s (key %s)", rp.Name, rp.Key)
		}
	}
	return "repository " + key
}

// listScopes says which scopes the run has, for an error about one it has
// not.
func (r *resolver) listScopes() string {
	var groups, repos []string
	for _, g := range r.names.Groups {
		groups = append(groups, g.Name)
	}
	for _, rp := range r.names.Repos {
		repos = append(repos, rp.Name)
	}
	slices.Sort(repos)
	repos = slices.Compact(repos)
	cut := func(names []string) string {
		if len(names) > 20 {
			return strings.Join(names[:20], ", ") + fmt.Sprintf(" and %d more", len(names)-20)
		}
		return strings.Join(names, ", ")
	}
	switch {
	case len(groups) == 0:
		return "Its repositories: " + cut(repos) + "."
	default:
		return "Its groups: " + cut(groups) + ". Its repositories: " + cut(repos) + "."
	}
}

// scopeName is the name an answer gives a scope: the group's, the
// repository's, or "" for the whole run.
func (r *resolver) scopeName(sc domain.Scope) string {
	switch sc.Kind {
	case domain.ScopeGroup:
		return sc.Key
	case domain.ScopeRepo:
		for _, rp := range r.names.Repos {
			if rp.Key == sc.Key {
				return rp.Name
			}
		}
	}
	return ""
}
