package metrics

import (
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
			"of the repositories whose GitHub data the run read. Den: the landings whose author is human: the author of the pull " +
			"request that landed it (as who_knows@1 maps landings to pull requests), else the person its landing commit is " +
			"credited to. Num: those whose pull request has a review submitted before its merge (approved, changes requested or " +
			"commented) by a human other than the pull request's author. Detail: reviewed (Num), via_pull_request (landings with " +
			"a pull request), direct (landings with none, never reviewed), approved_no_comment (reviewed only by approvals with " +
			"no text and no inline comment), bot_landings (a bot's, left out of Den), repos (repositories counted).",
		Ratio:       true,
		Ranked:      false,
		Forge:       true,
		Subjects:    []string{domain.SubjectRepo, domain.SubjectGroup, domain.SubjectAll},
		Eligibility: "Not ranked, and never about a person: it describes how a codebase's changes land, not who reviews.",
		BlindSpots: "Review outside GitHub, pairing and conversation comments outside a review are invisible; a direct push is " +
			"unreviewed whatever happened before it. A rebase merge whose commits GitHub committed in different seconds maps only " +
			"its last commit to its pull request, so the others count as direct (#67). A repository without GitHub data has no " +
			"value, and a group or run counts only the repositories that have it.",
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
	den, reviewed, viaPull, direct, approvedOnly, bots, repos int
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
		var t reviewCoverageTally
		for _, l := range r.landings {
			if l.commit == nil || !w.Contains(l.time) {
				continue
			}
			p := pullOf[l]
			author := l.commit.credit
			if p != nil && p.Author.Login != "" {
				author = v.who.Reviewer(p.Author).Key
			}
			if v.who.Bot(author) {
				t.bots++
				continue
			}
			t.den++
			if p == nil {
				t.direct++
				continue
			}
			t.viaPull++
			if reviewed, approvedOnly := reviewCoverageOf(v, p); reviewed {
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
			"repos":               float64(t.repos),
		}))
	}
	return sorted(out), nil
}

// reviewCoverageOf reports whether p was reviewed before it merged by a
// human other than its author: a review reviewsByOthers counts (submitted
// work, by an account with a login, not the author, compared as people),
// submitted before the merge, by someone who is not a bot. approvedOnly is
// true when every such review was an approval with no text and no inline
// comment.
func reviewCoverageOf(v *view, p *domain.PullRequest) (reviewed, approvedOnly bool) {
	approvedOnly = true
	reviewsByOthers(p, domain.Window{Until: p.Merged}, v.who, func(r *domain.Review, s domain.Subject) {
		if v.who.Bot(s.Key) {
			return
		}
		reviewed = true
		if r.State != domain.ReviewApproved || r.BodyLength > 0 || r.Comments > 0 {
			approvedOnly = false
		}
	})
	return reviewed, reviewed && approvedOnly
}
