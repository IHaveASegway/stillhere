package mcp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
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

// person resolves a person as the reports, an earlier answer or GitHub
// name them: a display name, an id, an email or a login
// (app.FindPerson, the one rule the command line resolves by too, PF-7).
// A name that matches one person by the start of each word comes with a
// caveat that says so; two people with one name are an error that lists
// both with their ids and logins.
func (r *resolver) person(query string) (app.NamedPerson, *report.Caveat, error) {
	return app.FindPerson(r.names, query, app.PersonQuery{
		ID: r.id, Again: "Call again with name set to", Commands: toolCommands{},
	})
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
// it: the one finder (app.FindScopes, which the command line's explain
// resolves --repo and --group by too), which repo, a group subject
// (groupQuery), scopeArg and the run hint (call.elsewhere) all go through,
// so an answer's scope_arg resolves wherever an argument names a scope.
func (r *resolver) scopes(query string) []domain.Scope { return app.FindScopes(r.names, query) }

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
// repository's, or "" for the whole run (app.ScopeName).
func (r *resolver) scopeName(sc domain.Scope) string { return app.ScopeName(r.names, sc) }
