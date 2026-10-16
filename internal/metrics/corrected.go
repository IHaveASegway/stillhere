package metrics

import "github.com/IHaveASegway/stillhere/internal/domain"

// Correction is one correction made to a metric's definition in place,
// before the first release (docs/metrics.md: "before the first release, so
// the version stays N"), as a stored value shows it by its data: the Detail
// key every value of the metric about a subject of Kind ("" for any) has
// carried since. A value without it was stored by a stillhere before the
// correction, by the old definition.
type Correction struct {
	Metric domain.MetricID
	Kind   string            // the subject kind whose values carry Key; "" for every kind
	Key    string            // the Detail key
	With   []domain.MetricID // other metrics the same change moved, which are computed again with it
	Lacks  string            // what a run stored before it never kept, so its values computed again miss it; "" for nothing
}

// corrections is the one list of the corrections in place a stored run's
// readers tell by its data, in the order they were made.
var corrections = []Correction{
	// wave 10: changes, not landings, which explain lists (reviewCoverageItems)
	{Metric: reviewCoverageID, Key: "via_other_branch"},
	// wave 10: landings_365d by landed@2's credit, the merges a person made apart
	{Metric: survivingID, Kind: domain.SubjectPerson, Key: "merged_365d"},
	// wave 10: activity off the default branch (a pull request opened or
	// pushed to, a commit on another ref) and GitHub's link high for an
	// address whose names differ, which moves both knowledge metrics'
	// identity detail. A run stored before kept no commit of another ref.
	{Metric: knowledgeLossID, Key: detailGoingQuietNum, With: []domain.MetricID{knowledgeConcentrationID},
		Lacks: "the commits of other refs"},
}

// Corrected returns the corrections that values, a stored run's, predate:
// those of which some value of the metric about a subject of its kind lacks
// the key, in the order they were made. A reader computes those metrics
// (Correction.Metric and With) again from the run's facts rather than show
// the old values beside what it computes per query by the corrected rules,
// such as explain's list of review_coverage@1's changes or each holder's
// last activity (app's load, stored.computeUnstored). A run is told by its
// data, never by a version (docs/spec/codebase.md#run-model). None for a
// run that holds no value of a corrected metric, whose readers show none.
func Corrected(values []domain.MetricValue) []Correction {
	stale := make([]bool, len(corrections))
	for _, v := range values {
		for i, c := range corrections {
			if stale[i] || v.Metric != c.Metric || (c.Kind != "" && v.Subject.Kind != c.Kind) {
				continue
			}
			if _, ok := v.Detail[c.Key]; !ok {
				stale[i] = true
			}
		}
	}
	var out []Correction
	for i, c := range corrections {
		if stale[i] {
			out = append(out, c)
		}
	}
	return out
}
