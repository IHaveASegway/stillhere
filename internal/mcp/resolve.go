package mcp

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
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

// miss is a scope or a repository the run does not hold: a usage error
// whose message lists what the run holds, to which the call adds the
// stored run that holds it, if one does (call.elsewhere, MC-17).
type miss struct {
	query string // as the agent passed it
	repo  bool   // a repository was asked for, not any scope
	msg   string
}

func (e *miss) Error() string { return e.msg }
func (e *miss) Unwrap() error { return &domain.UsageError{Msg: e.msg} }

// resolver resolves an agent's names against one run's vocabulary.
type resolver struct {
	names  *app.Names
	emails bool // --allow-emails: a candidate is named by email rather than by id
}

// norm folds a name for comparison: lowercased, with runs of white space
// as one space.
func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// id is how an answer names a person when two share a name: the email with
// --allow-emails, else its pseudonym; a login (identity.LoginKey) stays a
// login, and an address with an empty local part ("@example.com") is an
// address like any other.
func (r *resolver) id(p app.NamedPerson) string {
	if r.emails || identity.LoginKey(p.Key) {
		return p.Key
	}
	return pseudonym(p.Key)
}

// named is how the server's own sentences, and the fields that say whom an
// answer is about, name a person: by display name as the reports print it,
// or by id (r.id) when the reports show none, because git recorded none or
// because it gives an address away (report.SubjectOf).
func (r *resolver) named(p app.NamedPerson) string {
	if p.Name != "" {
		return p.Name
	}
	return r.id(p)
}

// person resolves a person: a display name as the reports print it
// (compared without case), an id from an earlier answer, an email, or a
// GitHub login, without case, as GitHub compares logins: a reviewer the
// run keys by login, or the person the run links the login to
// (app.NamedPerson.Logins, the links every metric that reads reviews
// counts by), so an agent that knows people by their GitHub handles can
// name them (#69). A name that matches no one exactly may match one person
// by the start of each of its words ("lena" for Lena Lead), which the
// caveat says. Two people with one name are an error that lists both with
// their ids and logins.
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
			if !identity.LoginKey(p.Key) && pseudonym(p.Key) == lower {
				return p, nil, nil
			}
		}
		return app.NamedPerson{}, nil, notFoundf("id %s names no one in run %d: ids are stable across runs, so this "+
			"person is not in this one.", lower, run)
	case strings.Contains(lower, "@") && !strings.ContainsAny(lower, " \t"):
		// Every key is lowercased where it is made (an email by the
		// collectors, a login by identity.Reviewer), so a lowercased query
		// compares without case.
		for _, p := range r.names.People {
			if p.Key == lower {
				return p, nil, nil
			}
		}
		if login, ok := strings.CutPrefix(lower, "@"); ok && identity.LoginKey(lower) {
			for _, p := range r.names.People {
				if slices.ContainsFunc(p.Logins, func(l string) bool { return strings.EqualFold(l, login) }) {
					return p, nil, nil
				}
			}
		}
		return app.NamedPerson{}, nil, notFoundf("%q is no one's email or GitHub login in run %d: people are keyed by "+
			"their canonical email, after .mailmap, and a login names whoever the run's GitHub data links it to. Pass a "+
			"display name instead.", lower, run)
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
			"%q matched %q by the start of each word: pass the full name to be exact.", q, p.Name)}, nil
	case len(partial) > 1:
		return app.NamedPerson{}, nil, &domain.UsageError{Msg: fmt.Sprintf(
			"%q could be %d people in run %d: %s. Call again with name set to a full name or an id.",
			q, len(partial), run, r.listPeople(partial))}
	}
	msg := fmt.Sprintf("No one in run %d is named %q.", run, q)
	if near := r.nearPeople(want); len(near) > 0 {
		msg += " Did you mean " + r.listPeople(near) + "?"
	}
	// Said on every miss, so it confirms nothing about this one: a name
	// the reports withhold is no name to match (report.SubjectOf).
	return app.NamedPerson{}, nil, notFoundf("%s Names are display names as the reports print them, after .mailmap; "+
		"overview lists the people with the most surviving lines. A display name that gives an address away (an "+
		"address, or its local part) is withheld from every answer and matches no one: pass that person's id or email.", msg)
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
		if p.Name == "" {
			continue // no name a report shows, so none to be near: such a person is passed by id
		}
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
// pick one: the name, the id to pass, the GitHub logins the run links to
// them, which an agent may know them by, unless one gives an address away
// (app.NamedPerson.ShownLogins), and what they hold. Names come from the
// repositories, so each is quoted: in the server's own sentence,
// repository text stays data (docs/spec/mcp.md#every-answer, "Repository
// text is untrusted").
func (r *resolver) listPeople(people []app.NamedPerson) string {
	var parts []string
	for i, p := range people {
		if i == maxCandidates {
			parts = append(parts, fmt.Sprintf("and %d more", len(people)-maxCandidates))
			break
		}
		about := []string{"id " + r.id(p)}
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
			about = append(about, "in "+quoteAll(repos)+more)
		}
		parts = append(parts, fmt.Sprintf("%q (%s)", p.Name, strings.Join(about, ", ")))
	}
	return strings.Join(parts, "; ")
}

// scope resolves a scope: "" for the whole run; a group name; a repository
// name or key; or, to say which, "group:NAME", "repo:NAME" or
// "GROUP/NAME" for the repository NAME in group GROUP. Names compare
// without case.
func (r *resolver) scope(query string) (domain.Scope, error) {
	if strings.TrimSpace(query) == "" {
		return domain.Scope{}, nil
	}
	found := r.scopes(query)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return domain.Scope{}, &miss{query: query, msg: fmt.Sprintf("%q is no group or repository of run %d. %s",
			query, r.names.Run.ID, r.listScopes())}
	}
	return domain.Scope{}, &domain.UsageError{Msg: fmt.Sprintf(
		"%q names %d scopes of run %d: %s. Pass one of those; overview's index gives each scope's scope_arg.",
		query, len(found), r.names.Run.ID, r.choices(found))}
}

// choices lists the scopes one argument found, so that an agent can pick
// one: each described, with the scope_arg that names it alone.
func (r *resolver) choices(found []domain.Scope) string {
	parts := make([]string, len(found))
	for i, sc := range found {
		parts[i] = fmt.Sprintf("%s, passed as %q", r.describeScope(sc), r.scopeArg(sc))
	}
	return strings.Join(parts, "; ")
}

// repoQuery is a repository argument (who_knows.repo, diff.repo, explain's
// subject for repo and area) as scopes reads it: a name, a key, GROUP/NAME,
// or a repository's scope_arg, repo:NAME, never a group.
func repoQuery(query string) string { return kindQuery(domain.ScopeRepo, query) }

// groupQuery is a group argument (explain's subject for subject_kind group)
// as scopes reads it: a name, or a group's scope_arg, group:NAME, as
// overview gives it.
func groupQuery(query string) string { return kindQuery(domain.ScopeGroup, query) }

// kindQuery prefixes an argument with the one kind of scope it may name,
// once: a prefix the agent passed, in any case, is not doubled.
func kindQuery(kind, query string) string {
	q := strings.TrimSpace(query)
	if k, rest, ok := strings.Cut(q, ":"); ok && strings.EqualFold(k, kind) {
		q = strings.TrimSpace(rest)
	}
	return kind + ":" + q
}

// scopes finds the scopes a non-empty scope argument names, as scope reads
// it: the one finder, which repo, a group subject (groupQuery), scopeArg
// and the run hint (call.elsewhere) all go through, so an answer's
// scope_arg resolves wherever an argument names a scope.
func (r *resolver) scopes(query string) []domain.Scope {
	q := strings.TrimSpace(query)
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
	if len(found) > 1 {
		// Names compare without case, but one spelt exactly as asked wins,
		// so "API" and "api" can each be named.
		exact := slices.DeleteFunc(slices.Clone(found), func(sc domain.Scope) bool {
			return sc.Key != q && r.scopeName(sc) != q
		})
		if len(exact) == 1 {
			found = exact
		}
	}
	return found
}

// repo resolves one repository by name or key, with the finder scope
// resolves by (scopes), limited to repositories, so the two cannot match
// differently.
func (r *resolver) repo(query string) (app.NamedRepo, error) {
	found := r.scopes(repoQuery(query))
	switch len(found) {
	case 1:
		for _, rp := range r.names.Repos {
			if rp.Key == found[0].Key {
				return rp, nil
			}
		}
	case 0:
		return app.NamedRepo{}, &miss{query: query, repo: true, msg: fmt.Sprintf("%q is no repository of run %d. %s",
			query, r.names.Run.ID, r.listScopes())}
	}
	return app.NamedRepo{}, &domain.UsageError{Msg: fmt.Sprintf(
		"%q names %d repositories of run %d: %s. Pass one of those.",
		query, len(found), r.names.Run.ID, r.choices(found))}
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

// scopeArg is the string an agent passes as scope to name sc and nothing
// else (MC-4): a group as group:NAME, and a repository by its name when
// that names it alone, else repo:NAME, GROUP/NAME or its key, the first
// that does. An organization whose main repository shares its name
// (go-git/go-git) holds a group and a repository of one name, which a bare
// name cannot tell apart. Each try goes through scopes, the finder scope
// resolves by, so a string an answer gives always resolves; "" for the
// whole run.
func (r *resolver) scopeArg(sc domain.Scope) string {
	var tries []string
	switch sc.Kind {
	case domain.ScopeGroup:
		tries = []string{"group:" + sc.Key}
	case domain.ScopeRepo:
		name := r.scopeName(sc)
		tries = []string{name, "repo:" + name}
		for _, rp := range r.names.Repos {
			if rp.Key == sc.Key && rp.Group != "" {
				tries = append(tries, rp.Group+"/"+name)
			}
		}
		tries = append(tries, sc.Key)
	default:
		return ""
	}
	for _, q := range tries {
		if got := r.scopes(q); len(got) == 1 && got[0] == sc {
			return q
		}
	}
	return tries[len(tries)-1]
}

// describeScope names a scope in a sentence, a repository so that two of
// one name can be told apart.
func (r *resolver) describeScope(sc domain.Scope) string {
	if sc.Kind == domain.ScopeGroup {
		return fmt.Sprintf("group %q", sc.Key)
	}
	return r.describeRepo(sc.Key)
}

// describeRepo names a repository so two of one name can be told apart.
func (r *resolver) describeRepo(key string) string {
	for _, rp := range r.names.Repos {
		if rp.Key == key {
			if rp.Group != "" {
				return fmt.Sprintf("repository %q in group %q (key %q)", rp.Name, rp.Group, rp.Key)
			}
			return fmt.Sprintf("repository %q (key %q)", rp.Name, rp.Key)
		}
	}
	return fmt.Sprintf("repository %q", key)
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
			return quoteAll(names[:20]) + fmt.Sprintf(" and %d more", len(names)-20)
		}
		return quoteAll(names)
	}
	switch {
	case len(groups) == 0:
		return "Its repositories: " + cut(repos) + "."
	default:
		return "Its groups: " + cut(groups) + ". Its repositories: " + cut(repos) + "."
	}
}

// quoteAll quotes each name of a list, for a sentence.
func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return strings.Join(quoted, ", ")
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
