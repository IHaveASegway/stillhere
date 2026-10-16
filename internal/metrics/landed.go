package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file holds the two versions of "what landed": landed@1, the week
// table as M0 defined it, which credits a landing to its landing commit's
// author, and landed@2 (#108), which credits a landing to the people who
// wrote its commits. Both count a commit's lines by one rule
// (landedLines), and both leave lines unknown, never zero, where the
// history counted no numstat (#74).

// The Detail keys of both versions.
const (
	landedLinesAdded     = "lines_added"
	landedProdLinesAdded = "prod_lines_added"
	// landedLinesUnknown counts the landings whose lines are unknown, a
	// partial clone's, which lines_added and prod_lines_added leave out.
	// It is present only when there are any, and when no landing's lines
	// are known the two lines keys are absent, not 0 (#74).
	landedLinesUnknown = "landings_lines_unknown"
	landedCommits      = "commits"
	landedMerged       = "merged"
	// landedCopies counts, in a scope's totals, the copies its landings
	// brought (markCopies), which landed@2 credits to no one: their change
	// was credited when it first landed.
	landedCopies = "copies"
)

// landedV1 is landed@1: what landed in the window, per person. It is the
// week table (docs/spec/week-collector.md): a landing counts for the person
// its landing commit is credited to, and its lines are the landing commit's
// numstat insertions, which for a merge are its first-parent diff, so a
// merged branch's lines count for whoever landed it. That is activity, not
// authorship; landed@2 and the change metrics credit side-branch commits
// to their own authors instead.
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
			"(blank lines included) on paths that received a role, and prod_lines_added, the prod subset; " +
			"landings_lines_unknown counts the landings whose lines are unknown (a partial clone's), which both leave out, " +
			"and both are absent when no landing's lines are known.",
		Ranked:      false,
		Subjects:    []string{domain.SubjectPerson},
		Eligibility: "Not ranked: volume rewards volume. In the proof run a dependency bot led every activity table.",
		BlindSpots: "A merged branch counts for the person who landed it, with the branch's lines; landed@2 credits its " +
			"authors. Numstat counts blank lines and moves. Work that never lands on the default branch is invisible.",
		Gaming:   "Splitting work into many small landings raises the count; generated or vendored code is denied, but a role-assigned bulk change still adds lines.",
		Oracle:   "testdata/fixture/SPEC.md#landed1-main-fixture",
		Validity: "Context, never ranked: a count of what landed, which is exact by construction and says nothing about value.",
	}
}

// Compute returns landed@1's values.
func (m landedV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (landedV1) compute(v *view) ([]domain.MetricValue, error) {
	if v.daysErr != nil {
		return nil, v.daysErr
	}
	w := v.window(v.days, 0)
	accs := make(map[personKey]*landedTally)
	for _, r := range v.repos {
		landings, _ := landedIn(r, w)
		for _, l := range landings {
			lines, prod := landedLines(r, l.commit)
			for _, s := range r.scopes {
				lookup(accs, personKey{s, l.commit.credit}).add(r, 1, lines, prod)
			}
		}
	}
	out := make([]domain.MetricValue, 0, len(accs))
	for k, a := range accs {
		out = append(out, count(landedID, k.scope, personSubject(k.email), w, a.landings, a.landings, a.lineDetail(nil)))
	}
	return sorted(out), nil
}

// landedV2 is landed@2 (#108): who wrote what landed in the window. A
// landing counts for each person credited (identity.Credit) with one of the
// non-merge commits it brought in, its own included, so a merged branch
// counts for the people who wrote it, as durable@1 and surviving@1 credit
// its lines, and a bot's commits count for the bot even when a person
// merged them. Lines are each commit's own numstat insertions. A merge
// commit's own diff is credited to no one; the person its landing commit is
// credited to gets merged instead. Nor is a copy (markCopies): a
// maintenance branch merged back brings cherry-picks of changes the branch
// already holds, which were credited when they first landed, and the
// scope's totals count them as copies. It also gives each scope its totals,
// about the repository, the group and the whole run, since a landing that
// holds several people's commits is a row of each.
type landedV2 struct{}

var landedV2ID = domain.MetricID{Name: "landed", Version: 2}

// Definition returns landed@2's catalog entry.
func (landedV2) Definition() Definition {
	return Definition{
		ID:       landedV2ID,
		Question: "Who wrote what landed in the window?",
		Unit: "Landings per person, per repository, group and all, in [now - days, now): the landings that hold a non-merge " +
			"commit credited (identity.Credit) to the person, a landing's own commit included, so a merged branch counts for " +
			"its authors. A copy, a commit whose patch id (git patch-id --stable) a commit of an earlier landing in the " +
			"history has, such as a cherry-pick a maintenance branch brings back, is credited to no one: its change was " +
			"credited when it first landed. Detail: commits (their credited non-merge commits), lines_added and " +
			"prod_lines_added (those commits' own numstat insertions on paths with a role, and the prod subset), merged " +
			"(merge landings whose landing commit is credited to them; a merge's own diff is credited to no one), and " +
			"landings_lines_unknown (landings whose lines are unknown, a partial clone's, which the lines leave out; the " +
			"lines keys are absent when none is known). Per repository, group and run (subject repo, group or all): the " +
			"same numbers over every landing in the scope, over its repositories whose history covers the window, and " +
			"copies (the copies its landings brought): 0 when nothing landed there, and no value when none covers it.",
		Ranked:      false,
		Subjects:    []string{domain.SubjectPerson, domain.SubjectRepo, domain.SubjectGroup, domain.SubjectAll},
		Eligibility: "Not ranked: volume rewards volume. In the proof run a dependency bot led every activity table.",
		BlindSpots: "Lines are per commit, so a branch that adds lines and later removes them counts both adds (landed@1 keeps " +
			"the landing's own diff). A squash merge credits the commit's author, whom GitHub sets to the pull request's " +
			"author; a rebase merge is already one landing per commit. Numstat counts blank lines and moves. Work that never " +
			"lands on the default branch is invisible. A backport whose diff was adapted (a conflict resolved) has a patch " +
			"id of its own and is credited again, and a copy whose original landed before the history the run read is " +
			"credited as an original. A change landed again after a revert took it out is credited again, but in a week " +
			"run, which reads no reverts, as a copy.",
		Gaming:   "Splitting work into many small commits or landings raises the counts; generated or vendored code is denied, but a role-assigned bulk change still adds lines.",
		Oracle:   "testdata/fixture/SPEC.md#landed2-main-fixture",
		Validity: "Context, never ranked: a count of who wrote what landed, exact by construction, which says nothing about value.",
	}
}

// Compute returns landed@2's values.
func (m landedV2) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (landedV2) compute(v *view) ([]domain.MetricValue, error) {
	if v.daysErr != nil {
		return nil, v.daysErr
	}
	w := v.window(v.days, 0)
	people := make(map[personKey]*landedTally)
	totals := make(map[domain.Scope]*landedTally)
	for _, r := range v.repos {
		landings, covered := landedIn(r, w)
		if !covered {
			continue // its landings in the window are unknown, not none (#74)
		}
		for _, s := range r.scopes {
			lookup(totals, s) // a window the history covers in which nothing landed: 0, not unknown
		}
		for _, l := range landings {
			parts := landedV2Parts(r, l)
			for _, s := range r.scopes {
				for _, p := range parts.people {
					a := lookup(people, personKey{s, p.email})
					a.commits += p.commits
					a.add(r, 1, p.lines, p.prod)
				}
				if l.commit.merge {
					lookup(people, personKey{s, l.commit.credit}).merged++
				}
				t := lookup(totals, s)
				t.commits += parts.all.commits
				t.copies += parts.copies
				t.add(r, 1, parts.all.lines, parts.all.prod)
				if l.commit.merge {
					t.merged++
				}
			}
		}
	}
	out := make([]domain.MetricValue, 0, len(people)+len(totals))
	for k, a := range people {
		out = append(out, count(landedV2ID, k.scope, personSubject(k.email), w, a.landings, a.landings, a.v2Detail()))
	}
	for s, t := range totals {
		d := t.v2Detail()
		d[landedCopies] = float64(t.copies)
		out = append(out, count(landedV2ID, s, scopeSubject(s), w, t.landings, t.landings, d))
	}
	return sorted(out), nil
}

// landedIn returns the landings of r that landed@1 and landed@2 count in
// w: those in the window whose landing commit the history holds, oldest
// first, and whether r's history covers w at all. When it does not, a
// shallow clone's boundary inside the window, the landings there are
// unknown, and none is returned (#74).
func landedIn(r *repoView, w domain.Window) ([]*landingView, bool) {
	if !r.covers(w) {
		return nil, false
	}
	var out []*landingView
	for _, l := range r.landings {
		if l.commit != nil && w.Contains(l.time) {
			out = append(out, l)
		}
	}
	return out, true
}

// landedPart is one person's part of a landing under landed@2: their
// credited non-merge commits in it, and the lines those commits added.
type landedPart struct {
	email                string
	commits, lines, prod int
}

// landedParts is a landing under landed@2: each person's part, in the order
// their first commit appears in the landing's map, the landing's own
// numbers, each commit once, and the copies it brought, which are in
// neither.
type landedParts struct {
	people []landedPart
	all    landedPart // email ""
	copies int
}

// landedV2Parts is landed@2's one rule for what a landing holds of whom,
// which its values, its trace (#109) and surviving@1's landings_365d read,
// so explain cannot disagree with the number it explains: each non-merge
// commit of the landing's map, the landing commit included, counts for the
// person it is credited to (identity.Credit), with its own numstat lines,
// unless it is a copy of a change an earlier landing brought (markCopies),
// which counts for no one; a merge commit's own diff counts for no one.
func landedV2Parts(r *repoView, l *landingView) landedParts {
	var out landedParts
	at := make(map[string]int)
	for _, c := range l.commits {
		if c.merge {
			continue
		}
		if c.copy {
			out.copies++
			continue
		}
		lines, prod := landedLines(r, c)
		i, ok := at[c.credit]
		if !ok {
			i = len(out.people)
			at[c.credit] = i
			out.people = append(out.people, landedPart{email: c.credit})
		}
		for _, p := range []*landedPart{&out.people[i], &out.all} {
			p.commits++
			p.lines += lines
			p.prod += prod
		}
	}
	return out
}

// landedLines returns a commit's numstat insertions, blank lines included,
// on the paths that received a role, and the prod subset: a non-merge
// commit's own diff, a merge landing's diff against its first parent.
func landedLines(r *repoView, c *commitView) (lines, prod int) {
	for _, fc := range c.changes {
		switch r.role[fc.Path] {
		case "":
		case domain.RoleProd:
			lines += fc.Added
			prod += fc.Added
		default:
			lines += fc.Added
		}
	}
	return lines, prod
}

// landedTally sums one subject's landings in a scope, with their lines
// where the history counted them.
type landedTally struct {
	landings, commits, merged int
	copies                    int // a scope's: the copies its landings brought, credited to no one
	lines, prod               int // over the landings whose lines are counted
	counted, unknown          int // landings whose lines are counted, and those whose lines are unknown
}

// add counts n landings of repository r with the lines they added, or with
// their lines unknown when r's history counted no numstat.
func (t *landedTally) add(r *repoView, n, lines, prod int) {
	t.landings += n
	if !r.numstat {
		t.unknown += n
		return
	}
	t.counted += n
	t.lines += lines
	t.prod += prod
}

// lineDetail adds the lines to detail (a new map when nil): lines_added and
// prod_lines_added, unless no landing's lines are known, and
// landings_lines_unknown when some are not.
func (t *landedTally) lineDetail(detail map[string]float64) map[string]float64 {
	if detail == nil {
		detail = map[string]float64{}
	}
	if t.unknown == 0 || t.counted > 0 {
		detail[landedLinesAdded] = float64(t.lines)
		detail[landedProdLinesAdded] = float64(t.prod)
	}
	if t.unknown > 0 {
		detail[landedLinesUnknown] = float64(t.unknown)
	}
	return detail
}

// v2Detail is landed@2's Detail: commits and merged, and the lines.
func (t *landedTally) v2Detail() map[string]float64 {
	return t.lineDetail(map[string]float64{
		landedCommits: float64(t.commits),
		landedMerged:  float64(t.merged),
	})
}
