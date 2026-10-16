package metrics

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// reviewCoverageV1 is review_coverage@1 (#107): how much of what landed was
// reviewed by someone other than its author. It describes repositories and
// groups, never a person: a per-person review rate would make review a
// target for individuals, so no value of it is about one, and it is never
// ranked. It counts changes (reviewsIndex.changes, the one grouping
// validate and label read too): a pull request with the landings that put
// it on the default branch, a pull request merged into another branch
// whose merge a landing brought, or a landing no pull request landed. It
// reads the GitHub data the run read (FactSet.Forge) through the reviews
// index who_knows@1 reads, so a landing belongs to the pull request
// who_knows@1 and reviews_given@1 say it does, and a reviewer is the person
// identity.Directory.Reviewer says.
type reviewCoverageV1 struct{}

var reviewCoverageID = domain.MetricID{Name: "review_coverage", Version: 1}

// reviewCoverageDays is review_coverage@1's window, reviews_given@1's.
const reviewCoverageDays = reviewsGivenDays

// Definition returns review_coverage@1's catalog entry.
func (reviewCoverageV1) Definition() Definition {
	return Definition{
		ID:       reviewCoverageID,
		Question: "How much of what landed was reviewed by someone other than its author?",
		Unit: "Num / Den per repository, group and run (subject repo, group or all), over the changes that reached the " +
			"default branch in [now - 90d, now) in the repositories whose GitHub data the run read. A change is a merged " +
			"pull request with the landings that put it on the default branch (as who_knows@1 maps them), a pull request " +
			"merged into another branch whose merge commit a merge landing in the window brought, or a landing no pull " +
			"request landed. Den: the human-authored changes, of their own work, whose pull request, or its absence, the " +
			"GitHub data knows. The author is the pull request's author, or, when a coding agent opened it, the person its " +
			"commits are credited to (identity.Credit); with no pull request, the person its landing commit is credited " +
			"to. Num: those whose pull request has a review submitted before its merge (approved, changes requested or " +
			"commented) by a human other than that author or the pull request's. Detail: reviewed (Num), via_pull_request " +
			"(pull requests merged into the default branch), via_other_branch (pull requests merged into another branch " +
			"that a merge brought), direct (landings with no pull request in the GitHub data the run read, which no " +
			"review covers), approved_no_comment (reviewed only by approvals with no text and no inline comment), " +
			"bot_landings (a bot's changes), branch_merges (merges whose every commit is a pull request merged into the " +
			"other branch, counted on its own, or a copy: no work of their own), copies (changes made only of copies of " +
			"what an earlier landing brought, which were counted when they first landed), after_sync (landings with no " +
			"pull request at or after the GitHub data's as-of, the last sync's start: theirs may not be synced yet), " +
			"unmatched (landings with no pull request whose subject names one as GitHub's merges do, \"Merge pull request " +
			"#N from\" or \"(#N)\" at its end, which the GitHub data lacks: sync again), named_elsewhere (landings whose " +
			"subject names a pull request the data holds that did not land by its merge commit: merged into another " +
			"branch, or the branch rewritten after the merge), all left out of Den, and repos (repositories counted).",
		Ratio:       true,
		Ranked:      false,
		Forge:       true,
		Subjects:    []string{domain.SubjectRepo, domain.SubjectGroup, domain.SubjectAll},
		Eligibility: "Not ranked, and never about a person: it describes how a codebase's changes land, not who reviews.",
		BlindSpots: "Review outside GitHub, pairing and conversation comments outside a review are invisible; a landing with no " +
			"pull request is unreviewed whatever happened before it. A rebase merge whose commits GitHub committed in different " +
			"seconds maps only its last commit to its pull request, so the others count as direct (#67), and of a pull " +
			"request rebased into another branch only the last commit is its own. A pull request the GitHub data lacks " +
			"makes its landing direct unless its subject names it: a merge or squash GitHub made does, a rebase merge " +
			"does not; a subject that ends in \"(#N)\" for an issue makes a direct push unmatched. A pull request merged " +
			"into another branch counts only once that branch is merged into the default branch, and a cherry-pick of it " +
			"onto the default branch is left out as named_elsewhere. A repository without GitHub data has no value, and " +
			"a group or run counts only the repositories that have it.",
		Gaming: "A rubber-stamp approval counts as a review; approved_no_comment is shown beside the share for that reason. " +
			"Approving from a second account the run does not link to the author counts too.",
		Oracle: "testdata/fixture/SPEC.md#review_coverage1-with-reviews, the merge-methods guard and the merge-back guard " +
			"(testdata/fixture/SPEC.md#merge-back-fixturemergeback)",
		Validity: "Context, unvalidated: whether a change had an independent review, which says nothing about how good the review was.",
	}
}

// Compute returns review_coverage@1's values.
func (m reviewCoverageV1) Compute(in *Input) ([]domain.MetricValue, error) {
	return computeAlone(m, in)
}

// The outcomes of a change under review_coverage@1: in Den (reviewed,
// unreviewed and direct) or left out of it, and why. They are the words
// explain lists a change with (domain.TracedChange.Outcome).
const (
	coverageReviewed   = "reviewed"
	coverageUnreviewed = "unreviewed"
	coverageDirect     = "direct"
	coverageBot        = "bot"
	coverageBranch     = "branch_merge"
	coverageCopies     = "copies"
	coverageAfterSync  = "after_sync"
	coverageUnmatched  = "unmatched"
	coverageElsewhere  = "named_elsewhere"
)

// reviewCoverageTally sums one scope's changes.
type reviewCoverageTally struct {
	den, reviewed, viaPull, viaOther, direct, approvedOnly, bots, branches, copies, afterSync, unmatched, elsewhere, repos int
}

// reviewCoverageItem is one change of a repository in the window, with its
// outcome: the rule both the values and the trace read, so explain cannot
// disagree with the share it explains.
type reviewCoverageItem struct {
	change       *changeView
	outcome      string
	author       string // the change's author, as Den reads it; "" for one left out before it is known
	reviewers    int    // the people other than its author who reviewed it before it merged
	approvedOnly bool   // reviewed, and only by approvals with no text and no inline comment
	named        int    // the pull request a landing's subject names (unmatched, named_elsewhere)
}

// reviewCoverageItems returns, per repository of v with GitHub data whose
// history covers the window, its changes in the window w with their
// outcomes; a repository without either has no entry: its coverage is
// unknown, not zero.
func reviewCoverageItems(v *view, x *reviewsIndex, w domain.Window) map[*repoView][]reviewCoverageItem {
	out := map[*repoView][]reviewCoverageItem{}
	for _, r := range v.repos {
		if _, known := x.landings(r); !known || !r.covers(w) {
			continue
		}
		subjects := make(map[string]string) // landing SHA → its subject
		if rf := x.facts[r.key]; rf != nil {
			for j := range rf.Commits {
				subjects[rf.Commits[j].SHA] = rf.Commits[j].Subject
			}
		}
		asOf := x.asOf[r.key]
		landed := make(map[string]bool, len(r.landings)) // the default branch's landings, by SHA
		for _, l := range r.landings {
			landed[l.sha] = true
		}
		items := []reviewCoverageItem{}
		for _, c := range x.changes(r) {
			l := c.landing()
			if l.commit == nil || !w.Contains(c.time()) {
				continue
			}
			it := reviewCoverageItem{change: c}
			own := c.own()
			switch {
			case len(own) == 0 && len(c.brought) == 0 && len(c.commits) > 0:
				it.outcome = coverageCopies // counted when its change first landed
			case len(own) == 0:
				it.outcome = coverageBranch // its pull requests are changes of their own
			case v.botCommits(own):
				it.outcome = coverageBot // a bot's change, by the rule label and validate read too
			case c.pull == nil:
				it.author = l.commit.credit
				it.named = namedPull(subjects[l.sha])
				named := x.numbered[r.key][it.named]
				switch {
				case !l.time.Before(asOf):
					it.outcome = coverageAfterSync // its pull request, if it has one, may not be synced yet
				case it.named > 0 && named == nil:
					it.outcome = coverageUnmatched // a pull request's merge the GitHub data lacks: a replica's hole (#101)
				case it.named > 0 && named.State == domain.PullMerged && !landed[named.MergeCommit.SHA]:
					// The data holds it, merged into another branch or under a
					// merge commit the branch no longer holds: no sync finds
					// this landing's pull request.
					it.outcome = coverageElsewhere
				default:
					// No pull request, or a subject naming one that landed by
					// its own merge commit, or never merged: pushed directly.
					it.named = 0
					it.outcome = coverageDirect
				}
			default:
				it.author = reviewCoverageAuthor(v, c)
				it.outcome = coverageUnreviewed
				if it.reviewers, it.approvedOnly = reviewCoverageOf(v, c.pull, it.author); it.reviewers > 0 {
					it.outcome = coverageReviewed
				}
			}
			items = append(items, it)
		}
		out[r] = items
	}
	return out
}

func (reviewCoverageV1) compute(v *view) ([]domain.MetricValue, error) {
	x := v.reviews()
	if x == nil {
		return nil, nil
	}
	w := v.window(reviewCoverageDays, 0)
	tallies := make(map[domain.Scope]*reviewCoverageTally)
	for r, items := range reviewCoverageItems(v, x, w) {
		var t reviewCoverageTally
		for _, it := range items {
			switch it.outcome {
			case coverageReviewed, coverageUnreviewed:
				t.den++
				if it.change.via != nil {
					t.viaOther++
				} else {
					t.viaPull++
				}
				if it.outcome == coverageReviewed {
					t.reviewed++
					if it.approvedOnly {
						t.approvedOnly++
					}
				}
			case coverageDirect:
				t.den++
				t.direct++
			case coverageBot:
				t.bots++
			case coverageBranch:
				t.branches++
			case coverageCopies:
				t.copies++
			case coverageAfterSync:
				t.afterSync++
			case coverageUnmatched:
				t.unmatched++
			case coverageElsewhere:
				t.elsewhere++
			}
		}
		for _, s := range r.scopes {
			a := lookup(tallies, s)
			a.den += t.den
			a.reviewed += t.reviewed
			a.viaPull += t.viaPull
			a.viaOther += t.viaOther
			a.direct += t.direct
			a.approvedOnly += t.approvedOnly
			a.bots += t.bots
			a.branches += t.branches
			a.copies += t.copies
			a.afterSync += t.afterSync
			a.unmatched += t.unmatched
			a.elsewhere += t.elsewhere
			a.repos++
		}
	}
	var out []domain.MetricValue
	for s, t := range tallies {
		if t.den == 0 {
			continue // a share of nothing
		}
		out = append(out, ratio(reviewCoverageID, s, scopeSubject(s), w, t.reviewed, t.den, t.den, map[string]float64{
			"reviewed":            float64(t.reviewed),
			"via_pull_request":    float64(t.viaPull),
			"via_other_branch":    float64(t.viaOther),
			"direct":              float64(t.direct),
			"approved_no_comment": float64(t.approvedOnly),
			"bot_landings":        float64(t.bots),
			"branch_merges":       float64(t.branches),
			"copies":              float64(t.copies),
			"after_sync":          float64(t.afterSync),
			"unmatched":           float64(t.unmatched),
			"named_elsewhere":     float64(t.elsewhere),
			"repos":               float64(t.repos),
		}))
	}
	return sorted(out), nil
}

// Trace lists the changes behind review_coverage@1's value about a
// repository, a group or the whole run (#109's promise, for a value about
// code): every change of the window in the scope's repositories with GitHub
// data, with its outcome, those in Den first, the unreviewed ones before
// the reviewed, across repositories (then by repository and time), so the
// first page of a group's list is its unreviewed changes, whichever
// repository holds them. A person has no value of it, so their trace is
// empty.
func (reviewCoverageV1) Trace(in *Input, subject domain.Subject) (domain.Trace, error) {
	v, err := newView(in, nil)
	if err != nil {
		return domain.Trace{}, err
	}
	w := v.window(reviewCoverageDays, 0)
	t := domain.Trace{Window: w}
	scope, ok := subjectScope(subject)
	x := v.reviews()
	if !ok || x == nil {
		return t, nil
	}
	for r, items := range reviewCoverageItems(v, x, w) {
		if !inScope(scope, r.key, r.group) {
			continue
		}
		for _, it := range items {
			c := it.change
			tc := domain.TracedChange{Repo: r.key, Landing: c.landing().sha, Time: c.time(), Via: c.via != nil,
				Outcome: it.outcome, Author: it.author, Reviewers: it.reviewers, ApprovedOnly: it.approvedOnly, Named: it.named}
			if p := c.pull; p != nil {
				tc.Pull, tc.Base = p.Number, p.BaseBranch
			}
			t.Changes = append(t.Changes, tc)
		}
	}
	slices.SortFunc(t.Changes, func(a, b domain.TracedChange) int {
		return cmp.Or(cmp.Compare(coverageOrder(a.Outcome), coverageOrder(b.Outcome)), cmp.Compare(a.Repo, b.Repo),
			a.Time.Compare(b.Time), cmp.Compare(a.Landing, b.Landing), cmp.Compare(a.Pull, b.Pull))
	})
	return t, nil
}

// coverageOrder is the order explain lists outcomes in: what a lead acts on
// first, the changes in Den that no one else reviewed.
func coverageOrder(outcome string) int {
	return slices.Index([]string{coverageUnreviewed, coverageDirect, coverageReviewed, coverageBot, coverageBranch,
		coverageCopies, coverageAfterSync, coverageUnmatched, coverageElsewhere}, outcome)
}

// subjectScope is the scope whose own numbers a subject about code names:
// a repository's, a group's or the whole run's; false for any other kind.
func subjectScope(s domain.Subject) (domain.Scope, bool) {
	switch s.Kind {
	case domain.SubjectRepo:
		return domain.Scope{Kind: domain.ScopeRepo, Key: s.Key}, true
	case domain.SubjectGroup:
		return domain.Scope{Kind: domain.ScopeGroup, Key: s.Key}, true
	case domain.SubjectAll:
		return domain.Scope{Kind: domain.ScopeAll}, true
	}
	return domain.Scope{}, false
}

// reviewCoverageAuthor is who wrote change c: its pull request's author, as
// identity.Directory.Reviewer keys an account, unless a coding agent opened
// it (identity.Directory.Agent), whose work is the person's its commits are
// credited to (identity.Credit, as landed@2 credits them): the first of the
// change's commits credited to a human. Without an author's login, it is
// the person its first commit, else its landing commit, is credited to. A
// bot's, such as a dependency bot's pull request, stays a bot's.
func reviewCoverageAuthor(v *view, c *changeView) string {
	p := c.pull
	if p.Author.Login == "" {
		if len(c.commits) > 0 {
			return c.commits[0].credit
		}
		return c.landing().commit.credit
	}
	author := v.who.Reviewer(p.Author).Key
	if !v.who.Agent(author) {
		return author
	}
	for _, k := range c.commits {
		if !v.who.Bot(k.credit) {
			return k.credit
		}
	}
	return author
}

// pullSubject matches the subjects GitHub writes when it lands a pull
// request: a merge commit's "Merge pull request #N from owner/branch", and
// a squash's title with " (#N)" at its end.
var pullSubject = regexp.MustCompile(`^Merge pull request #([0-9]+) from |\(#([0-9]+)\)$`)

// namedPull is the pull request a landing's subject names the way GitHub's
// merges do (pullSubject), 0 for none.
func namedPull(subject string) int {
	m := pullSubject.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1] + m[2])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// reviewCoverageOf counts the people other than its author
// (reviewCoverageAuthor) and p's who reviewed p before it merged: a review
// reviewsByOthers counts (submitted work, by an account with a login, not
// the pull request's author, compared as people), submitted before the
// merge, by someone who is neither a bot nor author. approvedOnly is true
// when every such review was an approval with no text and no inline
// comment.
func reviewCoverageOf(v *view, p *domain.PullRequest, author string) (reviewers int, approvedOnly bool) {
	approvedOnly = true
	seen := map[string]bool{}
	reviewsByOthers(p, domain.Window{Until: p.Merged}, v.who, func(r *domain.Review, s domain.Subject) {
		if v.who.Bot(s.Key) || s.Key == author {
			return
		}
		seen[s.Key] = true
		if r.State != domain.ReviewApproved || r.BodyLength > 0 || r.Comments > 0 {
			approvedOnly = false
		}
	})
	return len(seen), len(seen) > 0 && approvedOnly
}
