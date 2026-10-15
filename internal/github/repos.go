package github

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
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

func validName(s string) bool { return safeName.MatchString(s) && s != "." && s != ".." }

// Repos lists the repositories owner owns, sorted by Key. Archived
// repositories and forks are listed with their flags; the use case decides
// which to sync. A login GitHub does not know is a *domain.UsageError.
func (s *Source) Repos(ctx context.Context, owner string) ([]domain.ForgeRepo, error) {
	if !validName(owner) {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("invalid GitHub organization %q", owner)}
	}
	var repos []domain.ForgeRepo
	after := ""
	for {
		vars := map[string]any{"owner": owner, "after": cursor(after)}
		data, err := query[reposData](ctx, s, "Repos", reposQuery, vars, false)
		if err != nil {
			return nil, err
		}
		if data.Owner == nil {
			return nil, &domain.UsageError{Msg: fmt.Sprintf(
				"GitHub has no organization or user %q visible to this token", owner)}
		}
		conn := data.Owner.Repositories
		for _, n := range conn.Nodes {
			if !validName(n.Owner.Login) || !validName(n.Name) {
				s.log.Warn("GitHub listed a repository whose name cannot be a directory; skipped",
					"owner", n.Owner.Login, "name", n.Name)
				continue
			}
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
			repos = append(repos, repo)
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

// cloneURL is the HTTPS clone URL of owner/name: the URL a mirror clones and
// fetches from, and so its Repo.Key once discovery reads its origin.
func (s *Source) cloneURL(owner, name string) string {
	return s.gitURL + "/" + owner + "/" + name + ".git"
}
