package metrics

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// reviewCoverageV1 is review_coverage@1 (#107): how much of what landed was
// reviewed by someone other than its author. It describes repositories and
// groups, never a person: a per-person review rate would make review a
// target for individuals, so no value of it is about one, and it is never
// ranked. It reads the GitHub data the run read (FactSet.Forge) through the
// reviews index who_knows@1 reads, so a landing belongs to the pull request
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
		Unit: "Num / Den per repository, group and run (subject repo, group or all), over the landings in [now - 90d, now) " +
			"of the repositories whose GitHub data the run read. Den: the landings whose author is human and whose pull " +
			"request, or its absence, the GitHub data knows. The author is the author of the pull request that landed it (as " +
			"who_knows@1 maps landings to pull requests), or, when a coding agent opened it, the person its commits are credited " +
			"to (identity.Credit); with no pull request, the person its landing commit is credited to. Num: those whose pull " +
			"request has a review submitted before its merge (approved, changes requested or commented) by a human other than " +
			"that author or the pull request's. Detail: reviewed (Num), via_pull_request (landings with a pull request), " +
			"direct (landings with no pull request in the GitHub data the run read, which no review covers), " +
			"approved_no_comment (reviewed only by approvals with no text and no inline comment), bot_landings (a bot's, left " +
			"out of Den), after_sync (landings with no pull request at or after the GitHub data's as-of, the last sync's start: " +
			"theirs may not be synced yet, so they are left out of Den), unmatched (landings with no pull request whose subject " +
			"names one as GitHub's merges do, \"Merge pull request #N from\" or \"(#N)\" at its end: the GitHub data lacks it, " +
			"so they are left out of Den), repos (repositories counted).",
		Ratio:       true,
		Ranked:      false,
		Forge:       true,
		Subjects:    []string{domain.SubjectRepo, domain.SubjectGroup, domain.SubjectAll},
		Eligibility: "Not ranked, and never about a person: it describes how a codebase's changes land, not who reviews.",
		BlindSpots: "Review outside GitHub, pairing and conversation comments outside a review are invisible; a landing with no " +
			"pull request is unreviewed whatever happened before it. A rebase merge whose commits GitHub committed in different " +
			"seconds maps only its last commit to its pull request, so the others count as direct (#67). A pull request the " +
			"GitHub data lacks makes its landing direct unless its subject names it: a merge or squash GitHub made does, a " +
			"rebase merge does not; a subject that ends in \"(#N)\" for an issue makes a direct push unmatched. A repository " +
			"without GitHub data has no value, and a group or run counts only the repositories that have it.",
		Gaming: "A rubber-stamp approval counts as a review; approved_no_comment is shown beside the share for that reason. " +
			"Approving from a second account the run does not link to the author counts too.",
		Oracle:   "testdata/fixture/SPEC.md#review_coverage1-with-reviews",
		Validity: "Context, unvalidated: whether a change had an independent review, which says nothing about how good the review was.",
	}
}

// Compute returns review_coverage@1's values.
func (m reviewCoverageV1) Compute(in *Input) ([]domain.MetricValue, error) {
	return computeAlone(m, in)
}

// reviewCoverageTally sums one scope's landings.
type reviewCoverageTally struct {
	den, reviewed, viaPull, direct, approvedOnly, bots, afterSync, unmatched, repos int
}

func (reviewCoverageV1) compute(v *view) ([]domain.MetricValue, error) {
	x := v.reviews()
	if x == nil {
		return nil, nil
	}
	w := v.window(reviewCoverageDays, 0)
	tallies := make(map[domain.Scope]*reviewCoverageTally)
	for _, r := range v.repos {
		pulls, known := x.landings(r)
		if !known || !r.covers(w) {
			continue // no GitHub data: its coverage is unknown, not zero
		}
		pullOf := make(map[*landingView]*domain.PullRequest)
		for _, pl := range pulls {
			for _, l := range pl.landings {
				pullOf[l] = pl.pull
			}
		}
		subjects := make(map[string]string) // landing SHA → its subject
		if rf := x.facts[r.key]; rf != nil {
			for j := range rf.Commits {
				subjects[rf.Commits[j].SHA] = rf.Commits[j].Subject
			}
		}
		asOf := x.asOf[r.key]
		var t reviewCoverageTally
		for _, l := range r.landings {
			if l.commit == nil || !w.Contains(l.time) {
				continue
			}
			if v.botLandings(l) {
				t.bots++ // a bot's change, by the one rule label and validate read too
				continue
			}
			p := pullOf[l]
			author := reviewCoverageAuthor(v, l, p)
			if p == nil {
				switch {
				case !l.time.Before(asOf):
					t.afterSync++ // its pull request, if it has one, may not be synced yet
					continue
				case namesPull(subjects[l.sha]):
					t.unmatched++ // a pull request's merge the GitHub data lacks: a replica's hole (#101)
					continue
				}
				t.den++
				t.direct++
				continue
			}
			t.den++
			t.viaPull++
			if reviewed, approvedOnly := reviewCoverageOf(v, p, author); reviewed {
				t.reviewed++
				if approvedOnly {
					t.approvedOnly++
				}
			}
		}
		for _, s := range r.scopes {
			a := lookup(tallies, s)
			a.den += t.den
			a.reviewed += t.reviewed
			a.viaPull += t.viaPull
			a.direct += t.direct
			a.approvedOnly += t.approvedOnly
			a.bots += t.bots
			a.afterSync += t.afterSync
			a.unmatched += t.unmatched
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
			"direct":              float64(t.direct),
			"approved_no_comment": float64(t.approvedOnly),
			"bot_landings":        float64(t.bots),
			"after_sync":          float64(t.afterSync),
			"unmatched":           float64(t.unmatched),
			"repos":               float64(t.repos),
		}))
	}
	return sorted(out), nil
}

// reviewCoverageAuthor is who wrote landing l, which pull request p landed
// (nil for none): p's author, as identity.Directory.Reviewer keys an
// account, unless a coding agent opened it (identity.Directory.Agent),
// whose work is the person's its commits are credited to (identity.Credit,
// as landed@2 credits them): the first non-merge commit of the landing
// credited to a human. Without a pull request, or an author's login, it is
// the person the landing commit is credited to. A bot's, such as a
// dependency bot's pull request, stays a bot's.
func reviewCoverageAuthor(v *view, l *landingView, p *domain.PullRequest) string {
	if p == nil || p.Author.Login == "" {
		return l.commit.credit
	}
	author := v.who.Reviewer(p.Author).Key
	if !v.who.Agent(author) {
		return author
	}
	for _, c := range l.commits {
		if !c.merge && !v.who.Bot(c.credit) {
			return c.credit
		}
	}
	return author
}

// pullSubject matches the subjects GitHub writes when it lands a pull
// request: a merge commit's "Merge pull request #N from owner/branch", and
// a squash's title with " (#N)" at its end.
var pullSubject = regexp.MustCompile(`^Merge pull request #([0-9]+) from |\(#([0-9]+)\)$`)

// namesPull reports whether a landing's subject names a pull request the
// way GitHub's merges do (pullSubject).
func namesPull(subject string) bool {
	m := pullSubject.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1] + m[2])
	return err == nil && n > 0
}

// reviewCoverageOf reports whether p was reviewed before it merged by a
// human other than its author (reviewCoverageAuthor) and p's: a review
// reviewsByOthers counts (submitted work, by an account with a login, not
// the pull request's author, compared as people), submitted before the
// merge, by someone who is neither a bot nor author. approvedOnly is true
// when every such review was an approval with no text and no inline
// comment.
func reviewCoverageOf(v *view, p *domain.PullRequest, author string) (reviewed, approvedOnly bool) {
	approvedOnly = true
	reviewsByOthers(p, domain.Window{Until: p.Merged}, v.who, func(r *domain.Review, s domain.Subject) {
		if v.who.Bot(s.Key) || s.Key == author {
			return
		}
		reviewed = true
		if r.State != domain.ReviewApproved || r.BodyLength > 0 || r.Comments > 0 {
			approvedOnly = false
		}
	})
	return reviewed, reviewed && approvedOnly
}
