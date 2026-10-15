package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// majorityOwnerV1 is majority_owner@1: the prod files in which a person is
// credited with at least half of the non-blank lines. A file split 50/50
// has two majority owners. Never max() alone, which would hide the split.
type majorityOwnerV1 struct{}

var majorityOwnerID = domain.MetricID{Name: "majority_owner", Version: 1}

// Definition returns majority_owner@1's catalog entry.
func (majorityOwnerV1) Definition() Definition {
	return Definition{
		ID:       majorityOwnerID,
		Question: "Which files does a person hold most of?",
		Unit: "Prod files at HEAD in which the person is credited with at least half of the non-blank lines, per person, " +
			"per repository, group and all. A 50/50 file has two majority owners. Bots can be majority owners and stay in the bot table.",
		Ranked:      false,
		Subjects:    []string{domain.SubjectPerson},
		Eligibility: "Not ranked: ownership rewards tenure.",
		BlindSpots: "Blame-based, so it shares surviving@1's blind spots; a file nobody holds half of has no majority owner, " +
			"so highly collaborative files are invisible here.",
		Gaming:   "Rewriting a file's lines takes its ownership.",
		Oracle:   "testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle",
		Validity: majorityValidity,
	}
}

// majorityValidity is the evidence behind file ownership as a signal.
const majorityValidity = "Context, never ranked. Bird, Nagappan, Murphy, Gall and Devanbu, \"Don't Touch My Code!\" (ESEC/FSE 2011) " +
	"found that ownership measures relate to pre-release faults and post-release failures in Windows Vista and Windows 7: " +
	"components with more minor contributors (under 5% of the commits) failed more, and a higher top-owner share failed less. " +
	"Their ownership counts commits per component, not surviving lines per file. Greiler, Herzig and Czerwonka (MSR 2015) " +
	"replicated the link at Microsoft; Foucault, Falleri and Blanc (EASE 2014) found it weak in open source, where module size " +
	"predicted faults better. For blame as the authorship signal, see surviving@1 (Avelino et al., IEEE Software 2019)."

// Compute returns majority_owner@1's values.
func (m majorityOwnerV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (majorityOwnerV1) compute(v *view) ([]domain.MetricValue, error) {
	files := make(map[personKey]int)
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		for _, f := range r.files {
			for _, email := range f.owners {
				for _, s := range r.scopes {
					files[personKey{s, email}]++
				}
			}
		}
	}
	out := make([]domain.MetricValue, 0, len(files))
	for k, n := range files {
		out = append(out, count(majorityOwnerID, k.scope, personSubject(k.email), domain.Window{}, n, 0, nil))
	}
	return sorted(out), nil
}
