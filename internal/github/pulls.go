package github

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// How a repository's pull requests are paged.
const (
	// pageSize is the pull requests per page. With the nested reviews,
	// commits and labels a page costs 2 points (measured with rateLimit
	// dryRun on 2026-10-14); a timeout halves it for the rest of the pass.
	pageSize = 50

	// overlap is how far before the watermark a later pass keeps reading.
	// It re-reads the pull requests updated in the same second as the
	// watermark, and any whose new updatedAt GitHub's list showed late. A
	// re-read pull request is stored again unchanged, so the cost is a few
	// pull requests per pass. GitHub documents no consistency bound; this
	// is a judgment, cheap to change.
	overlap = 5 * time.Minute
)

// The GraphQL documents of the pull request pass. A page asks for each pull
// request's first 50 labels and reviews and first 100 commits; the rest of a
// longer list comes from a follow-up query per pull request (1 point each).
// A review's inline comments are counted, not listed: a connection asked
// only for its totalCount costs nothing, while listing every comment would
// multiply a page's cost by 13 (26 points instead of 2).
const (
	pullFields = `
fragment pull on PullRequest {
  id number title state isDraft baseRefName createdAt updatedAt closedAt mergedAt
  author { __typename login }
  mergeCommit { ...commit }
  labels(first: 50) { pageInfo { hasNextPage endCursor } nodes { name } }
  reviews(first: 50, states: [APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED]) {
    pageInfo { hasNextPage endCursor } nodes { ...review }
  }
  commits(first: 100) { pageInfo { hasNextPage endCursor } nodes { commit { ...commit } } }
}`
	reviewFields = `
fragment review on PullRequestReview {
  id state submittedAt body author { __typename login } comments { totalCount }
}`
	commitFields = `
fragment commit on Commit {
  oid author { email user { login } }
}`

	pullsQuery = `query PullRequests($repo: ID!, $first: Int!, $after: String, $direction: OrderDirection!) {
  rateLimit { cost remaining resetAt }
  node(id: $repo) {
    ... on Repository {
      pullRequests(first: $first, after: $after, orderBy: {field: UPDATED_AT, direction: $direction}) {
        pageInfo { hasNextPage endCursor }
        nodes { ...pull }
      }
    }
  }
}` + pullFields + reviewFields + commitFields

	reviewsQuery = `query PullRequestReviews($pull: ID!, $after: String) {
  rateLimit { cost remaining resetAt }
  node(id: $pull) {
    ... on PullRequest {
      reviews(first: 100, after: $after, states: [APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED]) {
        pageInfo { hasNextPage endCursor } nodes { ...review }
      }
    }
  }
}` + reviewFields

	commitsQuery = `query PullRequestCommits($pull: ID!, $after: String) {
  rateLimit { cost remaining resetAt }
  node(id: $pull) {
    ... on PullRequest {
      commits(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes { commit { ...commit } } }
    }
  }
}` + commitFields

	labelsQuery = `query PullRequestLabels($pull: ID!, $after: String) {
  rateLimit { cost remaining resetAt }
  node(id: $pull) {
    ... on PullRequest {
      labels(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes { name } }
    }
  }
}`
)

// The JSON shapes of those documents. They stay in this package.
type (
	actorJSON struct {
		Typename string `json:"__typename"`
		Login    string `json:"login"`
	}
	commitJSON struct {
		OID    string `json:"oid"`
		Author *struct {
			Email string `json:"email"`
			User  *struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"author"`
	}
	reviewJSON struct {
		ID          string     `json:"id"`
		State       string     `json:"state"`
		SubmittedAt time.Time  `json:"submittedAt"`
		Body        string     `json:"body"`
		Author      *actorJSON `json:"author"`
		Comments    struct {
			TotalCount int `json:"totalCount"`
		} `json:"comments"`
	}
	labelsJSON struct {
		PageInfo pageInfo `json:"pageInfo"`
		Nodes    []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	reviewsJSON struct {
		PageInfo pageInfo     `json:"pageInfo"`
		Nodes    []reviewJSON `json:"nodes"`
	}
	commitsJSON struct {
		PageInfo pageInfo `json:"pageInfo"`
		Nodes    []struct {
			Commit commitJSON `json:"commit"`
		} `json:"nodes"`
	}
	pullJSON struct {
		ID          string      `json:"id"`
		Number      int         `json:"number"`
		Title       string      `json:"title"`
		State       string      `json:"state"`
		IsDraft     bool        `json:"isDraft"`
		BaseRefName string      `json:"baseRefName"`
		CreatedAt   time.Time   `json:"createdAt"`
		UpdatedAt   time.Time   `json:"updatedAt"`
		ClosedAt    time.Time   `json:"closedAt"` // JSON null leaves it zero
		MergedAt    time.Time   `json:"mergedAt"`
		Author      *actorJSON  `json:"author"`
		MergeCommit *commitJSON `json:"mergeCommit"`
		Labels      labelsJSON  `json:"labels"`
		Reviews     reviewsJSON `json:"reviews"`
		Commits     commitsJSON `json:"commits"`
	}
	pullsData struct {
		RateLimit rateLimitJSON `json:"rateLimit"`
		Node      *struct {
			PullRequests *struct {
				PageInfo pageInfo   `json:"pageInfo"`
				Nodes    []pullJSON `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"node"`
	}
	reviewsData struct {
		RateLimit rateLimitJSON `json:"rateLimit"`
		Node      *struct {
			Reviews *reviewsJSON `json:"reviews"`
		} `json:"node"`
	}
	commitsData struct {
		RateLimit rateLimitJSON `json:"rateLimit"`
		Node      *struct {
			Commits *commitsJSON `json:"commits"`
		} `json:"node"`
	}
	labelsData struct {
		RateLimit rateLimitJSON `json:"rateLimit"`
		Node      *struct {
			Labels *labelsJSON `json:"labels"`
		} `json:"node"`
	}
)

func (d *pullsData) rate() rateLimitJSON   { return d.RateLimit }
func (d *reviewsData) rate() rateLimitJSON { return d.RateLimit }
func (d *commitsData) rate() rateLimitJSON { return d.RateLimit }
func (d *labelsData) rate() rateLimitJSON  { return d.RateLimit }

// PullRequests calls put with every pull request of repo last updated at or
// after since, a page at a time.
//
// GitHub's pull request list has no "updated since" filter, only an order by
// updatedAt, so the pass depends on the watermark:
//
//   - The first pass (since is zero) reads oldest update first. Every page
//     is then a checkpoint: all pull requests updated before its last one
//     have been read, so each page carries the new watermark, and a pass
//     that stops halfway resumes after the last page stored. A pull request
//     updated during the pass moves to the end of the order and is read
//     again there.
//   - A later pass reads newest update first and stops at the first pull
//     request older than since less the overlap. Only the last page raises
//     the watermark, to the newest update read: one that happens during the
//     pass moves to the front, behind the reader, and its new updatedAt is
//     after that watermark, so the next pass reads it.
//
// This is Airbyte's GitHub connector's approach (ascending until the first
// state is saved, descending after). Each page's pull requests are complete:
// a list longer than the page asked for is finished with follow-up queries
// before the page is handed to put.
func (s *Source) PullRequests(ctx context.Context, repo domain.ForgeRepo, since time.Time, put func(*domain.ForgePage) error) error {
	if since.IsZero() {
		return s.pass(ctx, repo, "ASC", time.Time{}, put)
	}
	return s.pass(ctx, repo, "DESC", since, put)
}

// pass runs one pass in direction, ASC from the beginning or DESC down to
// since less the overlap.
func (s *Source) pass(ctx context.Context, repo domain.ForgeRepo, direction string, since time.Time, put func(*domain.ForgePage) error) error {
	stop := since.Add(-overlap)
	watermark := since // what the replica can claim once a page is stored
	newest := since    // DESC: the newest update read, the watermark after the last page
	first := pageSize
	after := ""
	for {
		vars := map[string]any{"repo": repo.ID, "first": first, "after": cursor(after), "direction": direction}
		data, err := query[pullsData](ctx, s, "PullRequests", pullsQuery, vars, true)
		if err != nil {
			return err
		}
		first = vars["first"].(int)
		if data.Node == nil || data.Node.PullRequests == nil {
			return errNoNode("repository", repo.Owner+"/"+repo.Name)
		}
		conn := data.Node.PullRequests
		last := !conn.PageInfo.HasNextPage
		var pulls []domain.PullRequest
		for i := range conn.Nodes {
			n := &conn.Nodes[i]
			updated := n.UpdatedAt.UTC()
			if direction == "DESC" && updated.Before(stop) {
				last = true
				break
			}
			p, err := s.pull(ctx, repo, n)
			if err != nil {
				return err
			}
			pulls = append(pulls, p)
			if updated.After(newest) {
				newest = updated
			}
		}
		if direction == "ASC" || last {
			watermark = newest
		}
		if err := put(&domain.ForgePage{Repo: repo.ID, PullRequests: pulls, Updated: watermark}); err != nil {
			return err
		}
		if last {
			return nil
		}
		after = conn.PageInfo.EndCursor
	}
}

// pull translates one pull request, finishing its labels, reviews and
// commits with follow-up queries when the page did not hold them all.
func (s *Source) pull(ctx context.Context, repo domain.ForgeRepo, n *pullJSON) (domain.PullRequest, error) {
	p := domain.PullRequest{
		ID:         n.ID,
		Repo:       repo.ID,
		Number:     n.Number,
		Title:      n.Title,
		Author:     user(n.Author),
		State:      pullState(n.State),
		Draft:      n.IsDraft,
		BaseBranch: n.BaseRefName,
		Created:    utc(n.CreatedAt),
		Updated:    utc(n.UpdatedAt),
		Closed:     utc(n.ClosedAt),
		Merged:     utc(n.MergedAt),
	}
	if p.State == domain.PullMerged && n.MergeCommit != nil {
		p.MergeCommit = commit(n.MergeCommit)
	}

	labels := n.Labels
	for {
		for _, l := range labels.Nodes {
			p.Labels = append(p.Labels, l.Name)
		}
		if !labels.PageInfo.HasNextPage {
			break
		}
		data, err := query[labelsData](ctx, s, "PullRequestLabels", labelsQuery,
			map[string]any{"pull": n.ID, "after": labels.PageInfo.EndCursor}, false)
		if err != nil {
			return p, err
		}
		if data.Node == nil || data.Node.Labels == nil {
			return p, errNoNode("pull request", n.ID)
		}
		labels = *data.Node.Labels
	}
	slices.Sort(p.Labels)
	p.Labels = slices.Compact(p.Labels) // a set: the replica keys labels by name

	reviews := n.Reviews
	for {
		for i := range reviews.Nodes {
			if r, ok := review(&reviews.Nodes[i]); ok {
				p.Reviews = append(p.Reviews, r)
			}
		}
		if !reviews.PageInfo.HasNextPage {
			break
		}
		data, err := query[reviewsData](ctx, s, "PullRequestReviews", reviewsQuery,
			map[string]any{"pull": n.ID, "after": reviews.PageInfo.EndCursor}, false)
		if err != nil {
			return p, err
		}
		if data.Node == nil || data.Node.Reviews == nil {
			return p, errNoNode("pull request", n.ID)
		}
		reviews = *data.Node.Reviews
	}
	slices.SortFunc(p.Reviews, func(a, b domain.Review) int {
		return cmp.Or(a.Submitted.Compare(b.Submitted), cmp.Compare(a.ID, b.ID))
	})

	commits := n.Commits
	for {
		for i := range commits.Nodes {
			p.Commits = append(p.Commits, commit(&commits.Nodes[i].Commit))
		}
		if !commits.PageInfo.HasNextPage {
			break
		}
		data, err := query[commitsData](ctx, s, "PullRequestCommits", commitsQuery,
			map[string]any{"pull": n.ID, "after": commits.PageInfo.EndCursor}, false)
		if err != nil {
			return p, err
		}
		if data.Node == nil || data.Node.Commits == nil {
			return p, errNoNode("pull request", n.ID)
		}
		commits = *data.Node.Commits
	}
	return p, nil
}

// review translates one review. ok is false for a review that was never
// submitted, which the query's states filter already leaves out.
func review(n *reviewJSON) (domain.Review, bool) {
	if n.SubmittedAt.IsZero() || n.State == "PENDING" {
		return domain.Review{}, false
	}
	return domain.Review{
		ID:         n.ID,
		Author:     user(n.Author),
		State:      reviewState(n.State),
		Submitted:  utc(n.SubmittedAt),
		BodyLength: utf8.RuneCountInString(strings.TrimSpace(n.Body)),
		Comments:   n.Comments.TotalCount,
	}, true
}

// user translates an author. GitHub returns null for a deleted account and
// types an app's account as a Bot, whose login it gives without "[bot]".
func user(a *actorJSON) domain.ForgeUser {
	if a == nil {
		return domain.ForgeUser{}
	}
	return domain.ForgeUser{Login: a.Login, Bot: a.Typename == "Bot"}
}

// commit translates a commit and the account GitHub resolved its author
// email to.
func commit(c *commitJSON) domain.ForgeCommit {
	fc := domain.ForgeCommit{SHA: strings.ToLower(c.OID)}
	if c.Author != nil {
		fc.AuthorEmail = strings.ToLower(strings.TrimSpace(c.Author.Email))
		if c.Author.User != nil {
			fc.AuthorLogin = c.Author.User.Login
		}
	}
	return fc
}

// pullState translates GitHub's PullRequestState. A state GitHub adds later
// is kept lowercased, so it is visible rather than mistaken for a known one.
func pullState(s string) string {
	switch s {
	case "OPEN":
		return domain.PullOpen
	case "CLOSED":
		return domain.PullClosed
	case "MERGED":
		return domain.PullMerged
	}
	return strings.ToLower(s)
}

// reviewState translates GitHub's PullRequestReviewState, keeping an unknown
// state lowercased like pullState.
func reviewState(s string) string {
	switch s {
	case "APPROVED":
		return domain.ReviewApproved
	case "CHANGES_REQUESTED":
		return domain.ReviewChangesRequested
	case "COMMENTED":
		return domain.ReviewCommented
	case "DISMISSED":
		return domain.ReviewDismissed
	}
	return strings.ToLower(s)
}

// utc returns t in UTC, and the zero time unchanged.
func utc(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}
