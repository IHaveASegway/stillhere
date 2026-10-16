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
			parts = append(parts, fmt.Sprintf("group %q", sc.Key))
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
