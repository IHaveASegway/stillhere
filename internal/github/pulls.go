package github

import (
	"cmp"
	"context"
	"fmt"
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

	// overlap is how far below since a pass keeps reading. It re-reads the
	// pull requests updated in the second the last pass began, any whose
	// new updatedAt GitHub's list showed late, and covers a few minutes of
	// difference between GitHub's clocks. A re-read pull request is stored
	// again unchanged, so the cost is a few pull requests per pass. GitHub
	// documents no consistency bound; this is a judgment, cheap to change.
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
// after since, a page at a time, newest update first.
//
// GitHub's pull request list has no "updated since" filter, only an order by
// updatedAt, which is GitHub's and can disagree with the updatedAt values it
// returns: on cli/cli one pull request updated in July was listed among
// those updated in March (#101). Two rules make a pass safe anyway
// (ADR 0013):
//
//   - The watermark is a position GitHub owns, never a value read from the
//     pull requests: the time the pass began on GitHub's clock (passStart),
//     carried by the last page only, and only when the pass read down to
//     since. A pass that stops halfway, for any reason, moves no watermark,
//     and the next one reads the same range again.
//   - The stop is tolerant: a pull request updated before the stop point
//     (since less the overlap) is skipped, not taken for the end of the
//     range, and the pass ends at the first page that holds none at or
//     after the stop point, or at the end of the list. So one pull request
//     out of order near the top cannot end a pass early.
//
// A pull request updated during the pass moves to the front, behind the
// reader; its new update is after the pass began, so the next pass, which
// reads down to this one's start less the overlap, reads it. A zero since
// reads the whole list.
//
// This is how Kafka's consumers and Postgres's replication slots keep their
// place, by an offset or a log sequence number the source owns, not by a
// field of the records (Kleppmann, Designing Data-Intensive Applications,
// ch. 11, "Logs and offsets"). Each page's pull requests are complete: a
// list longer than the page asked for is finished with follow-up queries
// before the page is handed to put.
func (s *Source) PullRequests(ctx context.Context, repo domain.ForgeRepo, since time.Time, put func(*domain.ForgePage) error) error {
	start := s.passStart()
	var stop time.Time
	if !since.IsZero() {
		stop = since.Add(-overlap)
	}
	tolerated := 0 // fields GitHub could not resolve, read as null (#99)
	first := pageSize
	after := ""
	for {
		vars := map[string]any{"repo": repo.ID, "first": first, "after": cursor(after), "direction": "DESC"}
		data, err := query[pullsData](ctx, s, "PullRequests", pullsQuery, vars, true, &tolerated)
		if err != nil {
			return err
		}
		first = vars["first"].(int)
		if data.Node == nil || data.Node.PullRequests == nil {
			return errNoNode("repository", repo.Owner+"/"+repo.Name)
		}
		conn := data.Node.PullRequests
		var pulls []domain.PullRequest
		for i := range conn.Nodes {
			n := &conn.Nodes[i]
			if !stop.IsZero() && n.UpdatedAt.Before(stop) {
				continue // older than the range, wherever GitHub's order put it
			}
			p, err := s.pull(ctx, repo, n, &tolerated)
			if err != nil {
				return err
			}
			pulls = append(pulls, p)
		}
		// The end of the list, or a whole page below the stop point.
		last := !conn.PageInfo.HasNextPage || (!stop.IsZero() && len(pulls) == 0)
		page := &domain.ForgePage{Repo: repo.ID, PullRequests: pulls}
		if last {
			page.Updated = start
		}
		if err := put(page); err != nil {
			return err
		}
		if last {
			if tolerated > 0 {
				s.log.Info("GitHub could not resolve some accounts (deleted since); read as no account",
					"repo", repo.Owner+"/"+repo.Name, "fields", tolerated)
			}
			return nil
		}
		if after, err = nextCursor("PullRequests", conn.PageInfo, after); err != nil {
			return err
		}
	}
}

// countQuery counts a repository's pull requests updated in a range with
// GitHub's search, which, unlike the pull request list, filters by update
// time (1 point). Asking for no node keeps it at the count.
const countQuery = `query PullRequestCount($q: String!) {
  rateLimit { cost remaining resetAt }
  search(query: $q, type: ISSUE, first: 0) { issueCount }
}`

type countData struct {
	RateLimit rateLimitJSON `json:"rateLimit"`
	Search    struct {
		IssueCount int `json:"issueCount"`
	} `json:"search"`
}

func (d *countData) rate() rateLimitJSON { return d.RateLimit }

// searchTime is how a time is written in a search qualifier.
const searchTime = "2006-01-02T15:04:05Z"

// CountPullRequests counts the pull requests of repo last updated at or
// after from (from the first for a zero from) and before start less the
// overlap: one updated in the overlap may have been read before that update,
// and GitHub's clocks may disagree by a little, so it is left out rather than
// counted as missing. GitHub's search leaves out pull requests it hides,
// such as a spam account's (on cli/cli, 2026-10-15: 4,698 by search of
// 4,730 listed), so the count is a lower bound.
func (s *Source) CountPullRequests(ctx context.Context, repo domain.ForgeRepo, from, start time.Time) (int, error) {
	if !ValidName(repo.Owner) || !ValidName(repo.Name) {
		return 0, fmt.Errorf("github: cannot count the pull requests of %q/%q: not a valid repository name", repo.Owner, repo.Name)
	}
	to := start.Add(-overlap).UTC().Format(searchTime)
	updated := "updated:<=" + to
	if !from.IsZero() {
		updated = "updated:" + from.UTC().Format(searchTime) + ".." + to
	}
	q := "repo:" + repo.Owner + "/" + repo.Name + " is:pr " + updated
	data, err := query[countData](ctx, s, "PullRequestCount", countQuery, map[string]any{"q": q}, false, nil)
	if err != nil {
		return 0, err
	}
	return data.Search.IssueCount, nil
}

// pull translates one pull request, finishing its labels, reviews and
// commits with follow-up queries when the page did not hold them all.
// tolerated counts the fields GitHub could not resolve (query).
func (s *Source) pull(ctx context.Context, repo domain.ForgeRepo, n *pullJSON, tolerated *int) (domain.PullRequest, error) {
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

	// Each nested list's first page came with the pull request, read from
	// the start of the list (after ""); the follow-ups read on from there.
	labels, after := n.Labels, ""
	for {
		for _, l := range labels.Nodes {
			p.Labels = append(p.Labels, l.Name)
		}
		if !labels.PageInfo.HasNextPage {
			break
		}
		next, err := nextCursor("PullRequestLabels", labels.PageInfo, after)
		if err != nil {
			return p, err
		}
		data, err := query[labelsData](ctx, s, "PullRequestLabels", labelsQuery,
			map[string]any{"pull": n.ID, "after": next}, false, tolerated)
		if err != nil {
			return p, err
		}
		if data.Node == nil || data.Node.Labels == nil {
			return p, errNoNode("pull request", n.ID)
		}
		labels, after = *data.Node.Labels, next
	}
	slices.Sort(p.Labels)
	p.Labels = slices.Compact(p.Labels) // a set: the replica keys labels by name

	reviews, after := n.Reviews, ""
	for {
		for i := range reviews.Nodes {
			if r, ok := review(&reviews.Nodes[i]); ok {
				p.Reviews = append(p.Reviews, r)
			}
		}
		if !reviews.PageInfo.HasNextPage {
			break
		}
		next, err := nextCursor("PullRequestReviews", reviews.PageInfo, after)
		if err != nil {
			return p, err
		}
		data, err := query[reviewsData](ctx, s, "PullRequestReviews", reviewsQuery,
			map[string]any{"pull": n.ID, "after": next}, false, tolerated)
		if err != nil {
			return p, err
		}
		if data.Node == nil || data.Node.Reviews == nil {
			return p, errNoNode("pull request", n.ID)
		}
		reviews, after = *data.Node.Reviews, next
	}
	slices.SortFunc(p.Reviews, func(a, b domain.Review) int {
		return cmp.Or(a.Submitted.Compare(b.Submitted), cmp.Compare(a.ID, b.ID))
	})

	commits, after := n.Commits, ""
	for {
		for i := range commits.Nodes {
			p.Commits = append(p.Commits, commit(&commits.Nodes[i].Commit))
		}
		if !commits.PageInfo.HasNextPage {
			break
		}
		next, err := nextCursor("PullRequestCommits", commits.PageInfo, after)
		if err != nil {
			return p, err
		}
		data, err := query[commitsData](ctx, s, "PullRequestCommits", commitsQuery,
			map[string]any{"pull": n.ID, "after": next}, false, tolerated)
		if err != nil {
			return p, err
		}
		if data.Node == nil || data.Node.Commits == nil {
			return p, errNoNode("pull request", n.ID)
		}
		commits, after = *data.Node.Commits, next
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
