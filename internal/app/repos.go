package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// MatchRepos returns the repositories of repos that a reader's name for
// one names: the one rule both inbound adapters match a repository
// argument by (the command line's --repo, the MCP server's repo and scope
// arguments), so a name that works on one works on the other. In order,
// the first that matches any wins:
//
//   - its key, or the key a remote pasted with its credentials scrubs to
//     (redact.URL), which names one repository;
//   - its name as every page heads its card ("werkzeug"), without case;
//   - GROUP/NAME, the group as the run named it;
//   - its origin, as a remote URL in any form (https, ssh, scp-like, with
//     or without ".git"), as HOST/OWNER/NAME, the URL without its scheme,
//     or as OWNER/NAME, compared as domain.RemoteRepo reads a remote: what
//     a user copies from GitHub's address bar or clone box.
//
// A trailing slash is ignored, as a shell's completion of a directory
// leaves one. None is an empty list; two or more are for the caller to
// refuse, listing them.
func MatchRepos(repos []NamedRepo, query string) []NamedRepo {
	q := strings.TrimRight(strings.TrimSpace(query), "/")
	if q == "" {
		return nil
	}
	scrubbed := redact.URL(q)
	for _, rp := range repos {
		if rp.Key == q || rp.Key == scrubbed {
			return []NamedRepo{rp}
		}
	}
	var found []NamedRepo
	for _, rp := range repos {
		if strings.EqualFold(rp.Name, q) {
			found = append(found, rp)
		}
	}
	if len(found) > 0 {
		return found
	}
	group, name, slash := strings.Cut(q, "/")
	if slash {
		for _, rp := range repos {
			if rp.Group != "" && strings.EqualFold(rp.Group, group) && strings.EqualFold(rp.Name, name) {
				found = append(found, rp)
			}
		}
		if len(found) > 0 {
			return found
		}
	}
	want, remote := domain.RemoteRepo(scrubbed)
	ownerName, hostForm := "", ""
	if !remote && slash {
		bare := strings.ToLower(strings.TrimSuffix(q, ".git"))
		switch strings.Count(bare, "/") {
		case 1:
			ownerName = "/" + bare
		case 2:
			hostForm = bare // host/owner/name, as RemoteRepo gives a remote
		}
	}
	names := func(have string) bool {
		return remote && have == want || ownerName != "" && strings.HasSuffix(have, ownerName) ||
			hostForm != "" && have == hostForm
	}
	for _, rp := range repos {
		if have, ok := domain.RemoteRepo(rp.Key); ok && names(have) {
			found = append(found, rp)
		}
	}
	if len(found) > 0 {
		return found
	}
	// Last, the names the run's GitHub data gives a repository (wave 11,
	// round 3's KT-2): its forge repository's, which a clone of an old name
	// does not have, and the old names GitHub resolves to it, which a
	// mirror under the new name does not.
	for _, rp := range repos {
		if slices.ContainsFunc(rp.Forge, names) {
			found = append(found, rp)
		}
	}
	return found
}

// namedRepos is a fact set's repositories as an adapter names them.
func namedRepos(fs *domain.FactSet) []NamedRepo {
	repos := make([]domain.Repo, 0, len(fs.Repos))
	for _, rf := range fs.Repos {
		repos = append(repos, rf.Repo)
	}
	_, named := mcpNamedScopes(nil, repos)
	withForgeNames(named, fs)
	return named
}

// repoArg resolves the command line's --repo of a run to its key
// (MatchRepos): "" stays "", every repository; a name that matches none or
// several is a *domain.UsageError that lists what to pass. The error
// quotes the argument scrubbed: it is printed and may be logged, and a
// remote pasted with its token is a common argument
// (docs/spec/git-invocation.md#url-scrubbing).
func repoArg(fs *domain.FactSet, runID int64, arg string) (string, error) {
	if arg == "" {
		return "", nil
	}
	repos := namedRepos(fs)
	found := MatchRepos(repos, arg)
	shown := redact.Scrub(arg)
	switch len(found) {
	case 1:
		return found[0].Key, nil
	case 0:
		var names []string
		for _, rp := range repos {
			names = append(names, rp.Name)
		}
		return "", &domain.UsageError{Msg: fmt.Sprintf("--repo %q is no repository of run %d: pass its name as a card "+
			"heads it, OWNER/NAME or its remote URL; the run holds %s", shown, runID, repoList(names))}
	}
	var keys []string
	for _, rp := range found {
		keys = append(keys, rp.Key)
	}
	return "", &domain.UsageError{Msg: fmt.Sprintf("--repo %q names %d repositories of run %d: pass one key of %s",
		shown, len(found), runID, repoList(keys))}
}

// repoList names up to five repositories, and counts the rest.
func repoList(names []string) string {
	const most = 5
	if len(names) <= most {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:most], ", "), len(names)-most)
}
