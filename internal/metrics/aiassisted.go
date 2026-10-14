package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// aiAssistedV1 is ai_assisted@1: a tag on changes, and cohort comparisons
// of the tagged changes against the rest (ADR 0007). A landing is
// AI-assisted when any commit in its map is (identity.AIAssisted), so a
// merge landing is tagged through a side-branch commit.
type aiAssistedV1 struct{}

var aiAssistedID = domain.MetricID{Name: "ai_assisted", Version: 1}

// Definition returns ai_assisted@1's catalog entry.
func (aiAssistedV1) Definition() Definition {
	return Definition{
		ID:       aiAssistedID,
		Question: "How much of what landed was AI-assisted, and how is it holding up against the rest?",
		Unit: "Landings in [now - 365d, now). Per AI-assisted landing (subject change, scope repo): Value 1. Per repository and " +
			"group (subject repo or group): the AI-assisted share of the landings, with the cohort comparisons durable_ai and " +
			"durable_other (durable@1's Σ surviving / Σ added over the clusters of tagged and untagged landings), their cluster " +
			"counts durable_ai_n and durable_other_n, and reverted_ai and reverted_other (reverted landings among each). Per " +
			"person (repository, group and all): the share of their clusters, over non-merge commits, in which one of their own " +
			"commits is AI-assisted; it is shown only to that person.",
		Ranked:      false,
		Eligibility: "Not ranked: AI-assisted work is a tag and a cohort comparison, never a ranked row (ADR 0007).",
		BlindSpots: "A lower bound: editor completions leave no trace, and only the policy's co-author emails and agent authors " +
			"are recognised, so a tool with an unknown trailer (Cursor's is unverified) is missed. GitHub squash merges list human " +
			"co-authors, who are never tagged.",
		Gaming:   "Removing the trailer hides the tag; adding one to a commit no agent touched fakes it.",
		Oracle:   "testdata/fixture/SPEC.md#ai-assisted-tags-and-credit (sibling) and the main fixture's 0 of 16",
		Validity: "A tag, not a measurement of quality: exact for the trailers and authors the policy names, blind to the rest.",
	}
}

// Compute returns ai_assisted@1's values.
func (m aiAssistedV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (aiAssistedV1) compute(v *view) ([]domain.MetricValue, error) {
	year := v.window(yearDays, 0)
	out := aiTags(v, year)
	out = append(out, aiCohortValues(v, year)...)
	out = append(out, aiPersonShares(v, year)...)
	return sorted(out), nil
}

// aiTags returns the tag itself: one value per AI-assisted landing in w.
func aiTags(v *view, w domain.Window) []domain.MetricValue {
	var out []domain.MetricValue
	for _, r := range v.repos {
		if !r.covers(w) {
			continue
		}
		for _, l := range r.landings {
			if l.ai && w.Contains(l.time) {
				out = append(out, count(aiAssistedID, domain.Scope{Kind: domain.ScopeRepo, Key: r.key},
					domain.Subject{Kind: domain.SubjectChange, Key: l.sha}, w, 1, 0, nil))
			}
		}
	}
	return out
}

// aiCohortValues returns the cohort comparisons per repository and group.
func aiCohortValues(v *view, w domain.Window) []domain.MetricValue {
	repos := make(map[string]*aiCohorts)
	groups := make(map[string]*aiCohorts)
	for _, r := range v.repos {
		if !r.covers(w) {
			continue
		}
		for _, l := range r.landings {
			if !w.Contains(l.time) {
				continue
			}
			lookup(repos, r.key).addLanding(l)
			if r.group != "" {
				lookup(groups, r.group).addLanding(l)
			}
		}
	}
	for _, k := range durableClusters(v) {
		lookup(repos, k.repo.key).addDurable(k)
		if k.repo.group != "" {
			lookup(groups, k.repo.group).addDurable(k)
		}
	}
	var out []domain.MetricValue
	for key, c := range repos {
		if c.landings > 0 {
			out = append(out, c.value(domain.Scope{Kind: domain.ScopeRepo, Key: key}, repoSubject(key), w))
		}
	}
	for name, c := range groups {
		if c.landings > 0 {
			out = append(out, c.value(domain.Scope{Kind: domain.ScopeGroup, Key: name}, groupSubject(name), w))
		}
	}
	return out
}

// aiPersonShares returns each human's own share: of their clusters of
// non-merge commits in w, those in which one of their own commits is
// AI-assisted. Only that person sees it (ADR 0007).
func aiPersonShares(v *view, w domain.Window) []domain.MetricValue {
	type acc struct{ clusters, ai int }
	accs := make(map[personKey]*acc)
	for _, r := range v.repos {
		if !r.covers(w) {
			continue
		}
		for _, l := range r.landings {
			if !w.Contains(l.time) {
				continue
			}
			mine := make(map[string]bool) // person → one of their commits here is AI-assisted
			for _, c := range l.commits {
				if !c.merge {
					mine[c.credit] = mine[c.credit] || c.ai
				}
			}
			for email, ai := range mine {
				if v.who.Bot(email) {
					continue
				}
				for _, s := range r.scopes {
					a := lookup(accs, personKey{s, email})
					a.clusters++
					if ai {
						a.ai++
					}
				}
			}
		}
	}
	out := make([]domain.MetricValue, 0, len(accs))
	for k, a := range accs {
		out = append(out, ratio(aiAssistedID, k.scope, personSubject(k.email), w, a.ai, a.clusters, a.clusters, nil))
	}
	return out
}

// aiCohorts compares AI-assisted landings with the rest in one population.
type aiCohorts struct {
	landings, ai                       int
	revertedAI, revertedOther          int
	aiSurviving, aiAdded, aiN          int
	otherSurviving, otherAdded, otherN int
}

func (c *aiCohorts) addLanding(l *landingView) {
	c.landings++
	switch {
	case l.ai:
		c.ai++
		if l.reverted {
			c.revertedAI++
		}
	case l.reverted:
		c.revertedOther++
	}
}

// addDurable adds one durable@1 cluster to its cohort, with durable@1's own
// denominator: the added lines on files blame read.
func (c *aiCohorts) addDurable(k *cluster) {
	if k.landing.ai {
		c.aiSurviving += k.surviving
		c.aiAdded += k.traced
		c.aiN++
		return
	}
	c.otherSurviving += k.surviving
	c.otherAdded += k.traced
	c.otherN++
}

// value renders the comparison; a durable ratio is left out when its cohort
// has no cluster.
func (c *aiCohorts) value(s domain.Scope, sub domain.Subject, w domain.Window) domain.MetricValue {
	detail := map[string]float64{
		"reverted_ai":     float64(c.revertedAI),
		"reverted_other":  float64(c.revertedOther),
		"durable_ai_n":    float64(c.aiN),
		"durable_other_n": float64(c.otherN),
	}
	if c.aiAdded > 0 {
		detail["durable_ai"] = float64(c.aiSurviving) / float64(c.aiAdded)
	}
	if c.otherAdded > 0 {
		detail["durable_other"] = float64(c.otherSurviving) / float64(c.otherAdded)
	}
	return ratio(aiAssistedID, s, sub, w, c.ai, c.landings, c.landings, detail)
}
