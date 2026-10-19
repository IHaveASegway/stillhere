package metrics

import (
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// aiAssistedV1 is ai_assisted@1: a tag on changes, and cohort comparisons
// of the tagged changes against the rest (ADR 0007). A landing is
// AI-assisted when any commit in its map is (identity.AIAssisted), so a
// merge landing is tagged through a side-branch commit. A person's own
// share is not among its Compute values: PersonalAIShare answers it per
// query, for that person only.
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
			"counts durable_ai_n and durable_other_n, and reverted_ai and reverted_other (the landings among each holding a " +
			"commit a later landing's revert names). Per " +
			"person (repository, group and all), on request only (metrics.PersonalAIShare, for that person's own page; never " +
			"computed by a scan): the share of their clusters, over non-merge commits, in which one of their own commits is " +
			"AI-assisted.",
		Ratio:       true,
		Ranked:      false,
		Subjects:    []string{domain.SubjectRepo, domain.SubjectGroup, domain.SubjectChange},
		Eligibility: "Not ranked: AI-assisted work is a tag and a cohort comparison, never a ranked row (ADR 0007).",
		BlindSpots: "A lower bound: editor completions leave no trace, and only the policy's co-author emails and agent authors " +
			"are recognised, so a tool with an unknown trailer (Cursor's is unverified) is missed. GitHub squash merges list human " +
			"co-authors, who are never tagged.",
		Gaming: "Removing the trailer hides the tag; adding one to a commit no agent touched fakes it.",
		Oracle: "testdata/fixture/SPEC.md#ai-assisted-tags-and-credit (sibling), the main fixture's 0 of 16, and " +
			"testdata/fixture/SPEC.md#binary-files-fixturebinary (durable_other 15/21)",
		Validity: "A tag, not a measurement of quality: exact for the trailers and authors the policy names, blind to the rest.",
	}
}

// Compute returns ai_assisted@1's values.
func (m aiAssistedV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (aiAssistedV1) compute(v *view) ([]domain.MetricValue, error) {
	year := v.window(yearDays, 0)
	out := aiTags(v, year)
	out = append(out, aiCohortValues(v, year)...)
	return sorted(out), nil
}

// PersonalAIShare returns ai_assisted@1's share for one person: of their
// clusters of non-merge commits in [now - 365d, now), the share in which
// one of their own commits is AI-assisted, with one value for each
// repository, group and the whole run where they have a cluster.
//
// ADR 0007 shows this share only to that person. It is therefore computed
// per query, as WhoKnows is, and never by Compute: a scan does not store
// it, so no report, explain answer or MCP tool that reads a run's values
// can show it by accident. Its only caller is the person's own page: the
// me use case, and from M3 the web server's, where sign-in tells who is
// asking (ADR 0005). email is trimmed and lowercased; a bot, an agent, or
// an email with no cluster gets no values.
func PersonalAIShare(in *Input, email string) ([]domain.MetricValue, error) {
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || v.who.Bot(email) {
		return nil, nil
	}
	return sorted(aiPersonShare(v, v.window(yearDays, 0), email)), nil
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

// aiPersonShare returns one person's own share in each scope: of their
// clusters of non-merge commits in w, those in which one of their own
// commits is AI-assisted.
func aiPersonShare(v *view, w domain.Window, email string) []domain.MetricValue {
	type acc struct{ clusters, ai int }
	accs := make(map[domain.Scope]*acc)
	for _, r := range v.repos {
		if !r.covers(w) {
			continue
		}
		for _, l := range r.landings {
			if !w.Contains(l.time) {
				continue
			}
			mine, ai := false, false // a commit of theirs is in the landing, and one of those is AI-assisted
			for _, c := range l.commits {
				if !c.merge && c.credit == email {
					mine, ai = true, ai || c.ai
				}
			}
			if !mine {
				continue
			}
			for _, s := range r.scopes {
				a := lookup(accs, s)
				a.clusters++
				if ai {
					a.ai++
				}
			}
		}
	}
	var out []domain.MetricValue
	for s, a := range accs {
		out = append(out, ratio(aiAssistedID, s, personSubject(email), w, a.ai, a.clusters, a.clusters, nil))
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
// sums: the added and the surviving lines whose survival blame can tell, so
// a cohort ratio leaves out what durable@1 leaves out (untracedLines).
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
