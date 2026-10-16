package github

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// reposQuery lists the repositories an organization or a user owns, a
// hundred at a time (1 point a page). repositoryOwner answers for both kinds
// of account; ownerAffiliations OWNER leaves out the repositories a user
// only collaborates on.
const reposQuery = `query Repos($owner: String!, $after: String) {
  rateLimit { cost remaining resetAt }
  repositoryOwner(login: $owner) {
    repositories(first: 100, after: $after, ownerAffiliations: [OWNER], orderBy: {field: NAME, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes { id name owner { login } isArchived isFork defaultBranchRef { name } }
    }
  }
}`

type reposData struct {
	RateLimit rateLimitJSON `json:"rateLimit"`
	Owner     *struct {
		Repositories struct {
			PageInfo pageInfo   `json:"pageInfo"`
			Nodes    []repoJSON `json:"nodes"`
		} `json:"repositories"`
	} `json:"repositoryOwner"`
}

func (d *reposData) rate() rateLimitJSON { return d.RateLimit }

type repoJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	IsArchived       bool `json:"isArchived"`
	IsFork           bool `json:"isFork"`
	DefaultBranchRef *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

// safeName matches the owner and repository names GitHub allows. A name
// becomes a directory under the mirrors root, so anything else, including
// "." and "..", is refused rather than joined into a path.
var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidName reports whether s can be a GitHub owner (an organization or a
// user login) or repository name, as the adapter takes them: ASCII letters,
// digits, ".", "_" and "-", and neither "." nor "..". It is the one rule:
// Repos, Mirror and ReconcileMirrors apply it, and the command line checks
// --org with it before a sync begins its run, so a URL pasted as an
// organization, token and all, is refused before anything records it.
func ValidName(s string) bool { return safeName.MatchString(s) && s != "." && s != ".." }

// Repos lists the repositories owner owns, sorted by Key. Archived
// repositories and forks are listed with their flags; the use case decides
// which to sync. A login GitHub does not know is a *domain.UsageError.
func (s *Source) Repos(ctx context.Context, owner string) ([]domain.ForgeRepo, error) {
	if !ValidName(owner) {
		// The message may be printed or logged: a URL pasted as an
		// organization is quoted without its credentials.
		return nil, &domain.UsageError{Msg: fmt.Sprintf("invalid GitHub organization %q", redact.Scrub(owner))}
	}
	var repos []domain.ForgeRepo
	after := ""
	for {
		vars := map[string]any{"owner": owner, "after": cursor(after)}
		data, err := query[reposData](ctx, s, "Repos", reposQuery, vars, false, nil)
		if err != nil {
			return nil, err
		}
		if data.Owner == nil {
			return nil, &domain.UsageError{Msg: fmt.Sprintf(
				"GitHub has no organization or user %q visible to this token", owner)}
		}
		conn := data.Owner.Repositories
		for i := range conn.Nodes {
			n := &conn.Nodes[i]
			if !ValidName(n.Owner.Login) || !ValidName(n.Name) {
				s.log.Warn("GitHub listed a repository whose name cannot be a directory; skipped",
					"owner", n.Owner.Login, "name", n.Name)
				continue
			}
			repos = append(repos, s.forgeRepo(n))
		}
		if !conn.PageInfo.HasNextPage {
			break
		}
		if after, err = nextCursor("Repos", conn.PageInfo, after); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(repos, func(a, b domain.ForgeRepo) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.ID, b.ID))
	})
	return repos, nil
}

// repoQuery looks up one repository by owner and name (1 point). GitHub
// answers for a repository renamed or transferred since with the repository
// under its new name, which the mirror then takes.
const repoQuery = `query Repo($owner: String!, $name: String!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) { id name owner { login } isArchived isFork defaultBranchRef { name } }
}`

type repoData struct {
	RateLimit  rateLimitJSON `json:"rateLimit"`
	Repository *repoJSON     `json:"repository"`
}

func (d *repoData) rate() rateLimitJSON { return d.RateLimit }

// Repo looks up the repository owner/name (#111), as Repos lists it. A name
// that cannot be a GitHub name, and one GitHub has no repository for that
// the token can see, are a *domain.UsageError: GitHub answers a missing
// repository with a NOT_FOUND error on a null repository, which the query
// reads as that null.
func (s *Source) Repo(ctx context.Context, owner, name string) (domain.ForgeRepo, error) {
	if !ValidName(owner) || !ValidName(name) {
		return domain.ForgeRepo{}, &domain.UsageError{Msg: fmt.Sprintf("invalid GitHub repository %q",
			redact.Scrub(owner+"/"+name))}
	}
	data, err := query[repoData](ctx, s, "Repo", repoQuery, map[string]any{"owner": owner, "name": name}, false, nil)
	if err != nil {
		return domain.ForgeRepo{}, err
	}
	n := data.Repository
	if n == nil {
		return domain.ForgeRepo{}, &domain.UsageError{Msg: fmt.Sprintf(
			"GitHub has no repository %s/%s visible to this token", owner, name)}
	}
	if !ValidName(n.Owner.Login) || !ValidName(n.Name) {
		return domain.ForgeRepo{}, fmt.Errorf("GitHub named repository %s/%s %q/%q, which cannot be a directory",
			owner, name, n.Owner.Login, n.Name)
	}
	return s.forgeRepo(n), nil
}

// forgeRepo translates a repository GitHub listed or looked up, whose
// owner and name passed ValidName.
func (s *Source) forgeRepo(n *repoJSON) domain.ForgeRepo {
	repo := domain.ForgeRepo{
		ID:       n.ID,
		Key:      s.cloneURL(n.Owner.Login, n.Name),
		Owner:    n.Owner.Login,
		Name:     n.Name,
		Archived: n.IsArchived,
		Fork:     n.IsFork,
	}
	if n.DefaultBranchRef != nil {
		repo.DefaultBranch = n.DefaultBranchRef.Name
	}
	return repo
}

// cloneURL is the HTTPS clone URL of owner/name: the URL a mirror clones and
// fetches from, and so its Repo.Key once discovery reads its origin.
func (s *Source) cloneURL(owner, name string) string {
	return s.gitURL + "/" + owner + "/" + name + ".git"
}
