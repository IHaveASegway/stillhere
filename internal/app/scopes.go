package app

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// A reader names repositories and groups as the reports print them: a
// repository by its name ("click"), its key (its remote, as JSON gives it),
// or GROUP/NAME, and a group by its name; a prefix, repo: or group:, says
// which kind of scope a name is, where a group and a repository share one
// (go-git/go-git). FindScopes is the one finder of those names, which the
// MCP server's scope arguments and the command line's explain both resolve
// through, so a name an answer prints resolves the same way on either.

// FindScopes returns the scopes a non-empty argument names in a run's
// vocabulary (n): its groups by name, and its repositories by key, by the
// scrubbed form of a remote pasted with credentials, by name, or by
// GROUP/NAME, each name compared without case. A "repo:" or "group:"
// prefix, in any case, keeps one kind. Of several found, one spelt exactly
// as asked wins, so "API" and "api" can each be named. Groups come first,
// then repositories, in the run's order.
func FindScopes(n *Names, query string) []domain.Scope {
	q := strings.TrimSpace(query)
	kind := ""
	if k, rest, ok := strings.Cut(q, ":"); ok && (strings.EqualFold(k, domain.ScopeGroup) || strings.EqualFold(k, domain.ScopeRepo)) {
		kind, q = strings.ToLower(k), strings.TrimSpace(rest)
	}
	var found []domain.Scope
	if kind != domain.ScopeRepo {
		for _, g := range n.Groups {
			if strings.EqualFold(g.Name, q) {
				found = append(found, domain.Scope{Kind: domain.ScopeGroup, Key: g.Name})
			}
		}
	}
	if kind != domain.ScopeGroup {
		for _, rp := range reposNamed(n, q) {
			found = append(found, domain.Scope{Kind: domain.ScopeRepo, Key: rp.Key})
		}
	}
	if len(found) > 1 {
		exact := slices.DeleteFunc(slices.Clone(found), func(sc domain.Scope) bool {
			return sc.Key != q && ScopeName(n, sc) != q
		})
		if len(exact) == 1 {
			found = exact
		}
	}
	return found
}

// reposNamed finds the repositories a name or key names: a key, the
// scrubbed form of a remote pasted with its credentials, a name, or
// GROUP/NAME.
func reposNamed(n *Names, q string) []NamedRepo {
	scrubbed := redact.URL(q)
	for _, rp := range n.Repos {
		if rp.Key == q || rp.Key == scrubbed {
			return []NamedRepo{rp}
		}
	}
	var found []NamedRepo
	for _, rp := range n.Repos {
		if strings.EqualFold(rp.Name, q) {
			found = append(found, rp)
		}
	}
	if len(found) == 0 {
		if group, name, ok := strings.Cut(q, "/"); ok {
			for _, rp := range n.Repos {
				if rp.Group != "" && strings.EqualFold(rp.Group, group) && strings.EqualFold(rp.Name, name) {
					found = append(found, rp)
				}
			}
		}
	}
	return found
}

// ScopeName is the name an answer gives a scope: the group's, the
// repository's, or "" for the whole run.
func ScopeName(n *Names, sc domain.Scope) string {
	switch sc.Kind {
	case domain.ScopeGroup:
		return sc.Key
	case domain.ScopeRepo:
		for _, rp := range n.Repos {
			if rp.Key == sc.Key {
				return rp.Name
			}
		}
	}
	return ""
}

// findScope resolves a command line's --repo or --group (kind) against the
// stored run's vocabulary: exactly one scope of that kind, or a
// *domain.UsageError that says what the run holds, or which of several
// the name could be, by key.
func findScope(n *Names, kind, query, flag string) (domain.Scope, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return domain.Scope{}, &domain.UsageError{Msg: fmt.Sprintf("%s needs a name, as the report prints it", flag)}
	}
	if k, rest, ok := strings.Cut(q, ":"); ok && strings.EqualFold(k, kind) {
		q = strings.TrimSpace(rest)
	}
	found := FindScopes(n, kind+":"+q)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return domain.Scope{}, &domain.UsageError{Msg: fmt.Sprintf("%s %q names no %s of run %d. %s", flag, query,
			scopeKindName(kind), n.Run.ID, listScopes(n, kind))}
	}
	keys := make([]string, len(found))
	for i, sc := range found {
		keys[i] = strconv.Quote(sc.Key)
	}
	return domain.Scope{}, &domain.UsageError{Msg: fmt.Sprintf("%s %q names %d repositories of run %d; pass one's key: %s",
		flag, query, len(found), n.Run.ID, strings.Join(keys, ", "))}
}

// scopeKindName is a kind of scope in a sentence.
func scopeKindName(kind string) string {
	if kind == domain.ScopeGroup {
		return "group"
	}
	return "repository"
}

// listScopes says which scopes of a kind the run holds, at most 20 names,
// for an error about one it does not.
func listScopes(n *Names, kind string) string {
	var names []string
	if kind == domain.ScopeGroup {
		for _, g := range n.Groups {
			names = append(names, g.Name)
		}
	} else {
		for _, rp := range n.Repos {
			names = append(names, rp.Name)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		return fmt.Sprintf("The run has no %s.", scopeKindName(kind))
	}
	more := ""
	if len(names) > 20 {
		more, names = fmt.Sprintf(" and %d more", len(names)-20), names[:20]
	}
	for i, s := range names {
		names[i] = strconv.Quote(s)
	}
	plural := "repositories"
	if kind == domain.ScopeGroup {
		plural = "groups"
	}
	return "Its " + plural + ": " + strings.Join(names, ", ") + more + "."
}
