package metrics

import (
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// landedV1 is landed@1: what landed in the window, per person. It is the
// week table (docs/spec/week-collector.md): a landing counts for the person
// its landing commit is credited to, and its lines are the landing commit's
// numstat insertions, which for a merge are its first-parent diff, so a
// merged branch's lines count for whoever landed it. That is activity, not
// authorship; the change metrics credit side-branch commits to their own
// authors instead.
type landedV1 struct{}

var landedID = domain.MetricID{Name: "landed", Version: 1}

// Definition returns landed@1's catalog entry.
func (landedV1) Definition() Definition {
	return Definition{
		ID:       landedID,
		Question: "What landed in the window?",
		Unit: "Landings per person, per repository, group and all: first-parent commits of the default branch " +
			"with committer time in [now - days, now), each credited (identity.Credit) to its landing commit's author, " +
			"or that commit's first human co-author when an agent wrote it. Detail: lines_added, the numstat insertions " +
			"(blank lines included) on paths that received a role, and prod_lines_added, the prod subset.",
		Ranked:      false,
		Eligibility: "Not ranked: volume rewards volume. In the proof run a dependency bot led every activity table.",
		BlindSpots: "A merged branch counts for the person who landed it, with the branch's lines. " +
			"Numstat counts blank lines and moves. Work that never lands on the default branch is invisible.",
		Gaming:   "Splitting work into many small landings raises the count; generated or vendored code is denied, but a role-assigned bulk change still adds lines.",
		Oracle:   "testdata/fixture/SPEC.md#week-oracle-m0-testweekoracle",
		Validity: "Context, never ranked: a count of what landed, which is exact by construction and says nothing about value.",
	}
}

// Compute returns landed@1's values.
func (m landedV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (landedV1) compute(v *view) ([]domain.MetricValue, error) {
	if v.days <= 0 {
		return nil, fmt.Errorf("landed@1 needs a window of at least one day, not %d", v.days)
	}
	w := v.window(v.days, 0)
	type acc struct{ landings, lines, prod int }
	accs := make(map[personKey]*acc)
	for _, r := range v.repos {
		if !r.covers(w) {
			continue
		}
		for _, l := range r.landings {
			if l.commit == nil || !w.Contains(l.time) {
				continue
			}
			lines, prod := 0, 0
			for _, fc := range l.commit.changes {
				switch r.role[fc.Path] {
				case "":
				case domain.RoleProd:
					lines += fc.Added
					prod += fc.Added
				default:
					lines += fc.Added
				}
			}
			for _, s := range r.scopes {
				a := lookup(accs, personKey{s, l.commit.credit})
				a.landings++
				a.lines += lines
				a.prod += prod
			}
		}
	}
	out := make([]domain.MetricValue, 0, len(accs))
	for k, a := range accs {
		out = append(out, count(landedID, k.scope, personSubject(k.email), w, a.landings, a.landings, map[string]float64{
			"lines_added":      float64(a.lines),
			"prod_lines_added": float64(a.prod),
		}))
	}
	return sorted(out), nil
}
