package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// knowledgeLossV1 is knowledge_loss@1: the share of an area's surviving prod
// lines held by people who have had neither a landing nor a submitted review
// anywhere in the scanned organization for 180 days. Knowledge is held by
// people, so lines credited to bots are in neither part of the share.
type knowledgeLossV1 struct{}

var knowledgeLossID = domain.MetricID{Name: "knowledge_loss", Version: 1}

// Definition returns knowledge_loss@1's catalog entry.
func (knowledgeLossV1) Definition() Definition {
	return Definition{
		ID:       knowledgeLossID,
		Question: "How much of the code is held by people who have gone quiet?",
		Unit: "Num / Den per repository (subject repo), per top-level directory (subject area, \"<repo key>:<dir>/\") and per " +
			"group (subject group): Den is the surviving prod lines credited to people (bots excluded), Num the part credited to " +
			"people with no activity in [now - 180d, now) in any scanned repository. A person has had activity when a commit " +
			"credited to them, merge commits included, is in a landing in the window, so a side-branch author is active when " +
			"the branch lands, or when they submitted a review (approved, changes requested or commented) in the window on any " +
			"pull request of the scanned repositories, open, closed or merged, in the GitHub data the run read. Detail: people " +
			"(humans holding lines), people_inactive.",
		Ranked:      false,
		Forge:       true,
		Eligibility: "Not ranked: it describes code, not people.",
		BlindSpots: "People who moved to repositories outside the scan look gone. Mailmap gaps split one person in two and can make " +
			"either half look gone. Reviews count only on the pull requests of repositories whose GitHub data the run read, and " +
			"only for a reviewer the run links to an email (a login whose commits are in the run): a reviewer shown as @login " +
			"holds no line. Blame's blind spots apply.",
		Gaming:   "Not a target for people; a quiet expert who lands one trivial change, or submits one review, resets their clock.",
		Oracle:   "testdata/fixture/SPEC.md#knowledge_loss1-main-fixture and #reviews-in-knowledge_loss1-and-who_knows1-main-fixture",
		Validity: knowledgeLossValidity,
	}
}

// knowledgeLossValidity is the prior art for blame-based knowledge loss.
const knowledgeLossValidity = "Context, unvalidated. Prior art measures the same thing: Rigby, Zhu, Donadelli and Mockus, \"Quantifying " +
	"and Mitigating Turnover-Induced Knowledge Loss\" (ICSE 2016), used files abandoned by departed developers as the proxy for " +
	"knowledge loss at Chrome and Avaya, and CodeScene's knowledge-loss view (Tornhill, Software Design X-Rays, 2018) does it " +
	"with history. Here inactivity stands in for departure, which it is not."

// Compute returns knowledge_loss@1's values.
func (m knowledgeLossV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (knowledgeLossV1) compute(v *view) ([]domain.MetricValue, error) {
	w := v.window(activityDays, 0)
	active := v.active(w)
	// A review is activity as a landing is (docs/metrics.md#knowledge_loss1-per-area):
	// someone who reviewed has not gone quiet. Only a reviewer linked to an
	// email can hold lines, so only they change a value.
	for email := range v.reviews().active(w) {
		active[email] = true
	}
	var out []domain.MetricValue
	add := func(s domain.Scope, sub domain.Subject, files []*fileView) {
		if val, ok := knowledgeLoss(v, active, s, sub, w, files); ok {
			out = append(out, val)
		}
	}
	groups := make(map[string][]*fileView)
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		files := r.sortedFiles()
		repoScope := domain.Scope{Kind: domain.ScopeRepo, Key: r.key}
		add(repoScope, repoSubject(r.key), files)
		for _, a := range byTopDir(files) {
			add(repoScope, areaSubject(r.key, a.dir), a.files)
		}
		if r.group != "" {
			groups[r.group] = append(groups[r.group], files...)
		}
	}
	for name, files := range groups {
		add(domain.Scope{Kind: domain.ScopeGroup, Key: name}, groupSubject(name), files)
	}
	return sorted(out), nil
}

// knowledgeLoss computes the share over files. ok is false when no person
// holds a line there, which leaves a share of nothing.
func knowledgeLoss(v *view, active map[string]bool, s domain.Scope, sub domain.Subject, w domain.Window, files []*fileView) (domain.MetricValue, bool) {
	held, gone := 0, 0
	people := make(map[string]bool)
	for _, f := range files {
		for email, n := range f.byPerson {
			if v.who.Bot(email) {
				continue
			}
			held += n
			people[email] = !active[email]
			if !active[email] {
				gone += n
			}
		}
	}
	if held == 0 {
		return domain.MetricValue{}, false
	}
	inactive := 0
	for _, isGone := range people {
		if isGone {
			inactive++
		}
	}
	return ratio(knowledgeLossID, s, sub, w, gone, held, 0, map[string]float64{
		"people":          float64(len(people)),
		"people_inactive": float64(inactive),
	}), true
}
