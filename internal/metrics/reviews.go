package metrics

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// This file is reviews_given@1 (docs/metrics.md#reviews_given1-m2), which
// the GitHub sync (#14) brought, and the reading of reviews that
// knowledge_loss@1 and who_knows@1 share with it (#38): which reviews count,
// and the pull requests a repository's landings landed. Who a login is
// comes from identity.Directory.Reviewer, so the three metrics agree on it.
// The rest of the package is #9's.

// The window and eligibility of reviews_given@1: part of its versioned
// definition.
const (
	reviewsGivenDays = 90
	reviewsGivenMinN = 10
)

// reviewsGiven is reviews_given@1: per person, the distinct merged pull
// requests authored by someone else on which they submitted a review in the
// last 90 days.
type reviewsGiven struct{}

var (
	_ Ranked         = reviewsGiven{}
	_ changeMeasured = reviewsGiven{}
	_ scoring        = reviewsGiven{}
)

// Definition returns reviews_given@1's catalog entry.
func (reviewsGiven) Definition() Definition {
	return Definition{
		ID:       domain.MetricID{Name: "reviews_given", Version: 1},
		Question: "Who carries the review load?",
		Unit:     "merged pull requests reviewed, per person",
		Ranked:   true,
		Forge:    true,
		Subjects: []string{domain.SubjectPerson},
		Eligibility: "at least 10 reviewed pull requests in the window; " +
			"bots (an app account, or a [bots] match) are listed and never ranked",
		BlindSpots: "reviews of pull requests that were closed unmerged or are still open; dismissed reviews; " +
			"conversation comments outside a review; pairing and reviews outside GitHub; " +
			"a reviewer with no commit in the run shows as @login, apart from their email; " +
			"pull requests of a repository the run did not scan, scanned through a host alias, or failed to collect",
		Gaming: "rubber-stamp approvals, which the share of approvals with no comment shows next to the count; " +
			"reviewing many trivial pull requests, such as dependency bumps",
		Oracle:   "testdata/github/README.md#reviews_given1 (TestGitHubSync)",
		Validity: betaValidity,
	}
}

// Compute returns one value per person for scope all, each group and each
// repository, as the git metrics do, so that every scope a scan ranks in
// has the values its table lists bots from: Value and N are the pull
// requests reviewed, and Detail holds the context numbers shown next to
// them (reviews, review_comments, approvals, approvals_no_comment, and
// no_comment_share when there are approvals).
func (m reviewsGiven) Compute(in *Input) ([]domain.MetricValue, error) {
	rv := newReviewsView(in, domain.Scope{Kind: domain.ScopeAll})
	if rv == nil {
		return nil, nil
	}
	id := m.Definition().ID
	scopes := []domain.Scope{{Kind: domain.ScopeAll}}
	for _, g := range in.Facts.Groups {
		scopes = append(scopes, domain.Scope{Kind: domain.ScopeGroup, Key: g.Name})
	}
	for _, key := range rv.repoKeys() {
		scopes = append(scopes, domain.Scope{Kind: domain.ScopeRepo, Key: key})
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
// Num 1, in the window's week block (counted back from Now,
// domain.Window.Week) of the person's first counted review of it, for
// rank's week-block bootstrap. Bots get no clusters: they are never ranked.
func (m reviewsGiven) Clusters(in *Input, scope domain.Scope) (RankInput, error) {
	if in == nil || in.Facts == nil {
		return RankInput{}, errors.New("reviews_given@1: no facts to rank")
	}
	window := reviewsGivenWindow(in.Facts.Now)
	ri := RankInput{
		Window:  window,
		Options: rank.Options{MinN: reviewsGivenMinN, Blocks: window.Weeks()},
	}
	rv := newReviewsView(in, scope)
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
			Block:  window.Week(r.first),
		})
	}
	for _, cs := range ri.Data {
		slices.SortFunc(cs, func(a, b rank.Cluster) int { return cmp.Compare(a.Change, b.Change) })
	}
	return ri, nil
}

// measureChanges is reviews_given@1's measure of a change (#77,
// docs/metrics.md#validity-checks): the people other than its author who
// submitted a review of its pull request that reviewsByOthers counts, bots
// left out as its clusters leave them out, at any time before Now. The
// window selects the change, by when it landed (Scorer.Score), not its
// reviews, so a pull request merged early in the window keeps the reviews
// it got before the window began. A pull request that no one else
// reviewed is measured 0. A change that is no pull request of the run's
// GitHub data is not measured: a direct push, a merge GitHub's API does
// not list (a security advisory's "Merge commit from fork") and a pull
// request a sync missed look alike in the replica, so a 0 there would read
// a gap in the data as no review.
func (reviewsGiven) measureChanges(in *Input) func(*Change) (float64, bool) {
	if in == nil || in.Facts == nil || in.Facts.Forge == nil {
		return func(*Change) (float64, bool) { return 0, false }
	}
	who, now := in.who(), in.Facts.Now
	pulls := map[string]*domain.PullRequest{}
	for i := range in.Facts.Forge.PullRequests {
		p := &in.Facts.Forge.PullRequests[i]
		pulls[p.ID] = p
	}
	return func(c *Change) (float64, bool) {
		if c.Pull == nil || pulls[c.Pull.ID] == nil {
			return 0, false
		}
		reviewers := map[domain.Subject]bool{}
		reviewsByOthers(pulls[c.Pull.ID], domain.Window{Until: now}, who, func(_ *domain.Review, s domain.Subject) {
			if !who.Bot(s.Key) {
				reviewers[s] = true
			}
		})
		return float64(len(reviewers)), true
	}
}

// scores names the changes measureChanges measures.
func (reviewsGiven) scores() string {
	return "a pull request of the run's GitHub data, which a direct push, a merge GitHub's API does not list and a " +
		"pull request the sync did not copy are not"
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
	pull  string   // PullRequest.ID
	repos []string // Repo.Key of the run's repositories that are its repository: one, unless the run scanned two clones of it
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
	join       map[string][]string // ForgeRepo.ID → Repo.Key of the run's repositories (reviewsJoin)
}

// newReviewsView applies the definition to the forge facts. It returns nil
// when the fact set has none.
//
// Only the pull requests of the run's own repositories count, in every
// scope: the replica outlives runs and holds whatever any sync copied into
// facts.db, such as another organization's repositories, while every
// scope's numbers describe the repositories the run scanned. A forge
// repository is the run's when its clone URL and the origin of one of the
// run's repositories that it collected name the same host, owner and name
// (domain.ForgeJoin, which the scan made once and the run keeps, as its
// watermarks record it: reviewsJoin), so a user's SSH clone counts like a
// managed mirror, and a repository whose collection failed adds nothing
// here, as it adds nothing to any number.
//
// A review counts when reviewsByOthers counts it, on a pull request merged
// before Now. Only the pull requests of scope's repositories are read past
// the join (inScope still picks a scope's own): a rank in each repository
// of a run is one call per repository, and resolving every review of every
// repository's pull requests in each would cost the repositories times the
// pull requests, seconds on a large organization.
func newReviewsView(in *Input, scope domain.Scope) *reviewsView {
	if in == nil || in.Facts == nil || in.Facts.Forge == nil {
		return nil
	}
	fs := in.Facts
	rv := &reviewsView{
		window:     reviewsGivenWindow(fs.Now),
		groupRepos: map[string][]string{},
		join:       reviewsJoin(fs),
	}
	for _, g := range fs.Groups {
		rv.groupRepos[g.Name] = g.Repos
	}
	who := in.who()
	for i := range fs.Forge.PullRequests {
		p := &fs.Forge.PullRequests[i]
		keys := rv.join[p.Repo]
		if len(keys) == 0 || !rv.covers(keys, scope) || p.State != domain.PullMerged || p.Merged.IsZero() ||
			!p.Merged.Before(fs.Now) {
			continue
		}
		byPerson := map[domain.Subject]*reviewsPull{}
		reviewsByOthers(p, rv.window, who, func(r *domain.Review, s domain.Subject) {
			c := byPerson[s]
			if c == nil {
				c = &reviewsPull{pull: p.ID, repos: keys, who: s, bot: who.Bot(s.Key), first: r.Submitted}
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
		})
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
// repositories: every one for scope all, which holds the run's repositories
// only (newReviewsView), a group's repositories by Repo.Key, or one
// repository.
func (rv *reviewsView) inScope(scope domain.Scope) []reviewsPull {
	if scope.Kind == domain.ScopeAll {
		return rv.all
	}
	var out []reviewsPull
	for _, r := range rv.all {
		if rv.covers(r.repos, scope) {
			out = append(out, r)
		}
	}
	return out
}

// covers reports whether scope holds one of the repositories with keys:
// every one of the run's for scope all, one of a group's, or the one.
func (rv *reviewsView) covers(keys []string, scope domain.Scope) bool {
	if scope.Kind == domain.ScopeAll {
		return true
	}
	return slices.ContainsFunc(keys, func(key string) bool {
		return scope.Kind == domain.ScopeRepo && key == scope.Key ||
			scope.Kind == domain.ScopeGroup && rv.inGroup(key, scope.Key)
	})
}

// inGroup reports whether the repository with repoKey is in the group.
func (rv *reviewsView) inGroup(repoKey, group string) bool {
	return slices.Contains(rv.groupRepos[group], repoKey)
}

// repoKeys returns, sorted, the Repo.Key of every repository of the run
// that a forge repository joins: the repository scopes reviews_given@1 can
// have values in.
func (rv *reviewsView) repoKeys() []string {
	var out []string
	for _, keys := range rv.join {
		out = append(out, keys...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// who returns the directory the metrics resolve people with: Input.Who, or
// one built from the facts and the policy as Input documents. Without it no
// [bots] fragment would match, and a User-typed bot account (renovate, a
// [bot] machine user) would be ranked; nor would any login be linked.
func (in *Input) who() *identity.Directory {
	if in.Who != nil {
		return in.Who
	}
	return identity.New(in.Facts, in.Policy)
}

// reviewsSubmitted reports whether a review is submitted work: an approval,
// a change request or a comment. A dismissed review is not, nor a state
// the forge adds later.
func reviewsSubmitted(r *domain.Review) bool {
	switch r.State {
	case domain.ReviewApproved, domain.ReviewChangesRequested, domain.ReviewCommented:
		return true
	}
	return false
}

// reviewsByOthers calls fn with every review of p that reviews_given@1 and
// who_knows@1 count, in the pull request's order, and the subject who
// submitted it: submitted work (reviewsSubmitted) in w, by an account with
// a login, who is someone other than the pull request's author. "Someone
// other" compares people, not only logins (identity.Directory.Reviewer), so
// a second account of the author does not count either.
func reviewsByOthers(p *domain.PullRequest, w domain.Window, who *identity.Directory, fn func(*domain.Review, domain.Subject)) {
	author := who.Reviewer(p.Author)
	for i := range p.Reviews {
		r := &p.Reviews[i]
		// Logins compare without case, as GitHub compares them
		// (identity.Directory.Reviewer).
		if !reviewsSubmitted(r) || r.Author.Login == "" || strings.EqualFold(r.Author.Login, p.Author.Login) ||
			!w.Contains(r.Submitted) {
			continue
		}
		s := who.Reviewer(r.Author)
		if p.Author.Login != "" && s == author {
			continue
		}
		fn(r, s)
	}
}

// reviewsJoin returns the join every metric that reads GitHub data counts
// by: ForgeRepo.ID → the Repo.Key of the repositories of the fact set it
// is. A run's GitHub data carries the join its scan made and recorded
// (ForgeFacts.Join), and that one is used, so a stored run's numbers are
// counted by the join its provenance dates, whatever a later stillhere's
// domain.ForgeJoin would make of the same origins. Only GitHub data that no
// run joined, the replica itself, is joined here.
func reviewsJoin(fs *domain.FactSet) map[string][]string {
	if fs.Forge.Join != nil {
		return fs.Forge.Join
	}
	return domain.ForgeJoin(fs.Repos, fs.Forge.Repos)
}

// reviewsIndex is the GitHub data of a view's repositories that
// knowledge_loss@1 and who_knows@1 read, indexed once per computation: a
// question about a path in every repository of a large organization joins
// the repositories and reads the pull requests once, not once per
// repository.
type reviewsIndex struct {
	who     *identity.Directory
	pulls   []domain.PullRequest             // every pull request of the GitHub data, FactSet.Forge's
	join    map[string][]string              // ForgeRepo.ID → Repo.Key of the view's repositories it is (reviewsJoin)
	known   map[string]bool                  // Repo.Key → the run had GitHub data for it: a page or a finished sync
	merged  map[string][]*domain.PullRequest // Repo.Key → the merged pull requests of its forge repositories, in FactSet order
	claimed map[string]bool                  // the merge commit of every merged pull request: the landing that pull request lands
	facts   map[string]*domain.RepoFacts     // Repo.Key → its facts, whose commits name each landing's first parent
}

// reviews indexes the GitHub data of the view's repositories: those the
// run's join names, less the ones the view left out, such as a repository
// a who-knows query did not ask about. It is nil without GitHub data for
// any of them.
func (v *view) reviews() *reviewsIndex {
	if v.facts == nil || v.facts.Forge == nil {
		return nil
	}
	in := make(map[string]bool, len(v.repos))
	for _, r := range v.repos {
		in[r.key] = true
	}
	x := &reviewsIndex{who: v.who, pulls: v.facts.Forge.PullRequests, join: map[string][]string{}, known: map[string]bool{},
		merged: map[string][]*domain.PullRequest{}, claimed: map[string]bool{}, facts: map[string]*domain.RepoFacts{}}
	for id, keys := range reviewsJoin(v.facts) {
		if keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return !in[k] }); len(keys) > 0 {
			x.join[id] = keys
		}
	}
	if len(x.join) == 0 {
		return nil
	}
	for _, wm := range v.facts.Forge.Watermarks {
		if !wm.Updated.IsZero() || !wm.Synced.IsZero() {
			for _, key := range x.join[wm.Repo] {
				x.known[key] = true
			}
		}
	}
	for i := range x.pulls {
		p := &x.pulls[i]
		if p.State != domain.PullMerged || p.MergeCommit.SHA == "" {
			continue
		}
		x.claimed[p.MergeCommit.SHA] = true
		for _, key := range x.join[p.Repo] {
			x.merged[key] = append(x.merged[key], p)
		}
	}
	for i := range v.facts.Repos {
		if rf := &v.facts.Repos[i]; in[rf.Repo.Key] {
			x.facts[rf.Repo.Key] = rf
		}
	}
	return x
}

// active returns the people, by canonical email, who submitted a review
// (reviewsSubmitted) in w on any pull request of the view's forge
// repositories, whatever its state, their own pull requests included:
// knowledge_loss@1's review activity, which asks only whether someone is
// still around, and a reply in a review thread of one's own pull request,
// which GitHub records as a review, says they are. A reviewer the run
// cannot link to an email ("@login") holds no line, so it is left out, and
// so is an app account: identity.LoginKey tells them apart, the one rule
// for what a login is, so a person whose canonical email has an empty
// local part ("@example.com") is a person here too. It is nil without
// GitHub data.
func (x *reviewsIndex) active(w domain.Window) map[string]bool {
	if x == nil {
		return nil
	}
	var out map[string]bool
	for i := range x.pulls {
		p := &x.pulls[i]
		if len(x.join[p.Repo]) == 0 {
			continue
		}
		for j := range p.Reviews {
			r := &p.Reviews[j]
			if !reviewsSubmitted(r) || r.Author.Login == "" || !w.Contains(r.Submitted) {
				continue
			}
			if s := x.who.Reviewer(r.Author); !identity.LoginKey(s.Key) {
				if out == nil {
					out = map[string]bool{}
				}
				out[s.Key] = true
			}
		}
	}
	return out
}

// reviewsLanded is one merged pull request of a repository, with the
// landings that put it on the default branch.
type reviewsLanded struct {
	pull     *domain.PullRequest
	landings []*landingView
}

// landings returns the merged pull requests of the view's repository r that
// landed in its history, each with its landings, and whether the run has
// GitHub data for r at all: a forge repository that joins it, whose replica
// held a page or a finished sync when the run read it. Without that,
// who_knows@1's reviewers are unknown, not none.
//
// A pull request lands as its merge commit (PullRequest.MergeCommit), which
// GitHub reports for each merge method as the commit the base branch was
// updated to: a merge commit, whose landing's map holds the pull request's
// commits; a squash commit, the one landing; and for a rebase merge, the
// last of the commits it put on the branch one by one. Those carry new
// SHAs, which the pull request's commit list does not hold, and they share
// the merge's committer second, so the first-parent landings before the
// merge commit's that share its committer time are the pull request's too,
// up to its commit count, unless one is a merge, which a rebase never puts
// on the branch, or another pull request's merge commit, which that pull
// request lands: a merge queue or a bot can land two squashes in one
// second, and the second, of several commits, must not take the first's
// landing. A commit pushed straight to the branch in the same second, just
// before a squash of several commits, is still taken: nothing in the
// history or the GitHub data tells the two apart (the definition's blind
// spots). A pull request merged into another branch has no landing of its
// own.
func (x *reviewsIndex) landings(r *repoView) ([]reviewsLanded, bool) {
	if x == nil || !x.known[r.key] {
		return nil, false
	}
	bySHA := make(map[string]*landingView, len(r.landings))
	for _, l := range r.landings {
		bySHA[l.sha] = l
	}
	firstParent := map[string]string{} // landing commit → its first parent, a landing too when the history reaches it
	if rf := x.facts[r.key]; rf != nil {
		for j := range rf.Commits {
			if c := &rf.Commits[j]; bySHA[c.SHA] != nil && len(c.Parents) > 0 {
				firstParent[c.SHA] = c.Parents[0]
			}
		}
	}
	var out []reviewsLanded
	for _, p := range x.merged[r.key] {
		l := bySHA[p.MergeCommit.SHA]
		if l == nil {
			continue
		}
		pl := reviewsLanded{pull: p, landings: []*landingView{l}}
		if l.commit != nil && !l.commit.merge {
			for sha := l.sha; len(pl.landings) < len(p.Commits); {
				prev := bySHA[firstParent[sha]]
				if prev == nil || prev.commit == nil || prev.commit.merge || !prev.time.Equal(l.time) || x.claimed[prev.sha] {
					break
				}
				pl.landings = append(pl.landings, prev)
				sha = prev.sha
			}
		}
		out = append(out, pl)
	}
	return out, true
}
