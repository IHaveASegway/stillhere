package metrics

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// This file is reviews_given@1 (docs/metrics.md#reviews_given1-m2). It
// belongs to the GitHub sync (#14), which brings the forge facts it reads;
// the rest of the package is #9's.

// The window and eligibility of reviews_given@1: part of its versioned
// definition.
const (
	reviewsGivenDays = 90
	reviewsGivenMinN = 10
	reviewsWeek      = 7 * 24 * time.Hour
)

// reviewsGiven is reviews_given@1: per person, the distinct merged pull
// requests authored by someone else on which they submitted a review in the
// last 90 days.
type reviewsGiven struct{}

var _ Ranked = reviewsGiven{}

// Definition returns reviews_given@1's catalog entry.
func (reviewsGiven) Definition() Definition {
	return Definition{
		ID:       domain.MetricID{Name: "reviews_given", Version: 1},
		Question: "Who carries the review load?",
		Unit:     "merged pull requests reviewed, per person",
		Ranked:   true,
		Eligibility: "at least 10 reviewed pull requests in the window; " +
			"bots (an app account, or a [bots] match) are listed and never ranked",
		BlindSpots: "reviews of pull requests that were closed unmerged or are still open; dismissed reviews; " +
			"conversation comments outside a review; pairing and reviews outside GitHub; " +
			"a reviewer with no commit in the run shows as @login, apart from their email",
		Gaming: "rubber-stamp approvals, which the share of approvals with no comment shows next to the count; " +
			"reviewing many trivial pull requests, such as dependency bumps",
		Oracle:   "testdata/github/README.md#reviews_given1 (TestGitHubSync)",
		Validity: "unvalidated until the M2 labelled-sample check",
	}
}

// Compute returns one value per person for scope all and for each group:
// Value and N are the pull requests reviewed, and Detail holds the context
// numbers shown next to them (reviews, review_comments, approvals,
// approvals_no_comment, and no_comment_share when there are approvals).
func (m reviewsGiven) Compute(in *Input) ([]domain.MetricValue, error) {
	rv := newReviewsView(in)
	if rv == nil {
		return nil, nil
	}
	id := m.Definition().ID
	scopes := []domain.Scope{{Kind: domain.ScopeAll}}
	for _, g := range in.Facts.Groups {
		scopes = append(scopes, domain.Scope{Kind: domain.ScopeGroup, Key: g.Name})
	}
	var out []domain.MetricValue
	for _, scope := range scopes {
		totals := map[domain.Subject]*reviewsTally{}
		for _, r := range rv.inScope(scope) {
			t := totals[r.who]
			if t == nil {
				t = &reviewsTally{}
				totals[r.who] = t
			}
			t.pulls++
			t.reviews += r.reviews
			t.comments += r.comments
			t.approvals += r.approvals
			t.approvalsNoComment += r.approvalsNoComment
		}
		for who, t := range totals {
			detail := map[string]float64{
				"reviews":              float64(t.reviews),
				"review_comments":      float64(t.comments),
				"approvals":            float64(t.approvals),
				"approvals_no_comment": float64(t.approvalsNoComment),
			}
			if t.approvals > 0 {
				detail["no_comment_share"] = float64(t.approvalsNoComment) / float64(t.approvals)
			}
			out = append(out, domain.MetricValue{
				Metric:  id,
				Scope:   scope,
				Subject: who,
				Window:  rv.window,
				Value:   float64(t.pulls),
				Num:     float64(t.pulls),
				Den:     1,
				N:       t.pulls,
				Detail:  detail,
			})
		}
	}
	slices.SortFunc(out, func(a, b domain.MetricValue) int {
		return cmp.Or(
			cmp.Compare(a.Scope.Kind, b.Scope.Kind), cmp.Compare(a.Scope.Key, b.Scope.Key),
			cmp.Compare(a.Subject.Kind, b.Subject.Kind), cmp.Compare(a.Subject.Key, b.Subject.Key))
	})
	return out, nil
}

// Clusters returns one cluster per person and pull request reviewed, with
// Num 1, in the week (counted back from Now) of the person's first counted
// review of it, for rank's week-block bootstrap. Bots get no clusters: they
// are never ranked.
func (m reviewsGiven) Clusters(in *Input, scope domain.Scope) (RankInput, error) {
	if in == nil || in.Facts == nil {
		return RankInput{}, errors.New("reviews_given@1: no facts to rank")
	}
	ri := RankInput{
		Kind:    rank.Sum,
		Window:  reviewsGivenWindow(in.Facts.Now),
		Options: rank.Options{MinN: reviewsGivenMinN, Blocks: (reviewsGivenDays + 6) / 7},
	}
	rv := newReviewsView(in)
	if rv == nil {
		return ri, nil
	}
	for _, r := range rv.inScope(scope) {
		if r.bot {
			continue
		}
		if ri.Data == nil {
			ri.Data = map[domain.Subject][]rank.Cluster{}
		}
		ri.Data[r.who] = append(ri.Data[r.who], rank.Cluster{
			Change: r.pull,
			Num:    1,
			Block:  int(in.Facts.Now.Sub(r.first) / reviewsWeek),
		})
	}
	for _, cs := range ri.Data {
		slices.SortFunc(cs, func(a, b rank.Cluster) int { return cmp.Compare(a.Change, b.Change) })
	}
	return ri, nil
}

// reviewsGivenWindow is [now − 90 days, now).
func reviewsGivenWindow(now time.Time) domain.Window {
	return domain.Window{Since: now.Add(-reviewsGivenDays * 24 * time.Hour), Until: now}
}

// reviewsTally sums one subject's reviewed pull requests within a scope.
type reviewsTally struct {
	pulls, reviews, comments, approvals, approvalsNoComment int
}

// reviewsPull is one person's counted reviews of one pull request: the unit
// reviews_given@1 counts and rank resamples.
type reviewsPull struct {
	pull  string // PullRequest.ID
	repo  string // ForgeRepo.Key of its repository
	who   domain.Subject
	bot   bool
	first time.Time // the person's earliest counted review of it

	reviews, comments, approvals, approvalsNoComment int
}

// reviewsView is every counted review of a fact set, grouped by person and
// pull request, sorted by pull request, then subject.
type reviewsView struct {
	window     domain.Window
	all        []reviewsPull
	groupRepos map[string][]string // group name → Repo.Key of its repositories
}

// newReviewsView applies the definition to the forge facts. It returns nil
// when the fact set has none.
//
// A review counts when it was submitted in the window as an approval, a
// change request or a comment, by an account with a login, on a pull
// request merged before Now by someone else. "Someone else" compares
// people, not only logins, so a second account of the author does not
// count either.
func newReviewsView(in *Input) *reviewsView {
	if in == nil || in.Facts == nil || in.Facts.Forge == nil {
		return nil
	}
	fs := in.Facts
	rv := &reviewsView{window: reviewsGivenWindow(fs.Now), groupRepos: map[string][]string{}}
	for _, g := range fs.Groups {
		rv.groupRepos[g.Name] = g.Repos
	}
	who := newReviewsLogins(in)
	repoKey := make(map[string]string, len(fs.Forge.Repos))
	for _, r := range fs.Forge.Repos {
		repoKey[r.ID] = r.Key
	}
	for i := range fs.Forge.PullRequests {
		p := &fs.Forge.PullRequests[i]
		if p.State != domain.PullMerged || p.Merged.IsZero() || !p.Merged.Before(fs.Now) {
			continue
		}
		author := who.subject(p.Author)
		byPerson := map[domain.Subject]*reviewsPull{}
		for _, r := range p.Reviews {
			switch r.State {
			case domain.ReviewApproved, domain.ReviewChangesRequested, domain.ReviewCommented:
			default:
				continue
			}
			if r.Author.Login == "" || r.Author.Login == p.Author.Login || !rv.window.Contains(r.Submitted) {
				continue
			}
			s := who.subject(r.Author)
			if p.Author.Login != "" && s == author {
				continue
			}
			c := byPerson[s]
			if c == nil {
				c = &reviewsPull{pull: p.ID, repo: repoKey[p.Repo], who: s, bot: who.bot(r.Author, s), first: r.Submitted}
				byPerson[s] = c
			}
			if r.Submitted.Before(c.first) {
				c.first = r.Submitted
			}
			c.reviews++
			c.comments += r.Comments
			if r.State == domain.ReviewApproved {
				c.approvals++
				if r.BodyLength == 0 && r.Comments == 0 {
					c.approvalsNoComment++
				}
			}
		}
		for _, c := range byPerson {
			rv.all = append(rv.all, *c)
		}
	}
	slices.SortFunc(rv.all, func(a, b reviewsPull) int {
		return cmp.Or(cmp.Compare(a.pull, b.pull), cmp.Compare(a.who.Key, b.who.Key))
	})
	return rv
}

// inScope returns the counted reviews of pull requests in scope's
// repositories: every one for scope all, a group's repositories by
// Repo.Key, or one repository.
func (rv *reviewsView) inScope(scope domain.Scope) []reviewsPull {
	if scope.Kind == domain.ScopeAll {
		return rv.all
	}
	var out []reviewsPull
	for _, r := range rv.all {
		if scope.Kind == domain.ScopeRepo && r.repo == scope.Key ||
			scope.Kind == domain.ScopeGroup && rv.inGroup(r.repo, scope.Key) {
			out = append(out, r)
		}
	}
	return out
}

// inGroup reports whether the repository with repoKey is in the group.
func (rv *reviewsView) inGroup(repoKey, group string) bool {
	return slices.Contains(rv.groupRepos[group], repoKey)
}

// reviewsLogins resolves forge accounts to people. A login is a person when a
// commit the forge attributes to it is in the run: the person is the run's
// own mailmapped author of that SHA, the most such commits winning, then
// the smallest email. That way git's mailmap stays the only identity store
// (docs/spec/git-invocation.md#identity-mailmap-and-bots). Any other login
// is the subject "@login" (ADR 0004), and an app's "@login[bot]".
type reviewsLogins struct {
	email map[string]string // login → canonical email
	in    *Input
}

func newReviewsLogins(in *Input) reviewsLogins {
	author := map[string]string{} // SHA → canonical author email in the run
	for _, r := range in.Facts.Repos {
		for _, c := range r.Commits {
			if c.Author.Email != "" {
				author[c.SHA] = c.Author.Email
			}
		}
	}
	type vote struct{ login, sha string }
	seen := map[vote]bool{}
	votes := map[string]map[string]int{} // login → email → distinct commits
	count := func(c domain.ForgeCommit) {
		email, ok := author[c.SHA]
		if c.AuthorLogin == "" || !ok || seen[vote{c.AuthorLogin, c.SHA}] {
			return
		}
		seen[vote{c.AuthorLogin, c.SHA}] = true
		if votes[c.AuthorLogin] == nil {
			votes[c.AuthorLogin] = map[string]int{}
		}
		votes[c.AuthorLogin][email]++
	}
	for i := range in.Facts.Forge.PullRequests {
		p := &in.Facts.Forge.PullRequests[i]
		for _, c := range p.Commits {
			count(c)
		}
		count(p.MergeCommit)
	}
	links := reviewsLogins{email: map[string]string{}, in: in}
	for login, emails := range votes {
		best, n := "", 0
		for email, k := range emails {
			if k > n || k == n && email < best {
				best, n = email, k
			}
		}
		links.email[login] = best
	}
	return links
}

// subject is the person behind an account.
func (l reviewsLogins) subject(u domain.ForgeUser) domain.Subject {
	switch {
	case u.Bot:
		return domain.Subject{Kind: domain.SubjectPerson, Key: "@" + strings.TrimSuffix(u.Login, "[bot]") + "[bot]"}
	case l.email[u.Login] != "":
		return domain.Subject{Kind: domain.SubjectPerson, Key: l.email[u.Login]}
	}
	return domain.Subject{Kind: domain.SubjectPerson, Key: "@" + u.Login}
}

// bot reports whether an account is a bot: an app account, or a subject the
// policy's [bots] fragments match.
func (l reviewsLogins) bot(u domain.ForgeUser, s domain.Subject) bool {
	return u.Bot || l.in.Who != nil && l.in.Who.Bot(s.Key)
}
