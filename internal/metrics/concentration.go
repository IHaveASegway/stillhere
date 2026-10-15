package metrics

import (
	"cmp"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// knowledgeConcentrationV1 is knowledge_concentration@1: how few people hold
// half of an area's majority-owned files. It is labelled "not a truck
// factor": it is not Avelino's degree-of-authorship algorithm.
type knowledgeConcentrationV1 struct{}

var knowledgeConcentrationID = domain.MetricID{Name: "knowledge_concentration", Version: 1}

// Definition returns knowledge_concentration@1's catalog entry.
func (knowledgeConcentrationV1) Definition() Definition {
	return Definition{
		ID:       knowledgeConcentrationID,
		Question: "How few people hold half of the code's majority-owned files? (Not a truck factor.)",
		Unit: "Authors in the prefix, per repository (subject repo), per top-level directory (subject area, \"<repo key>:<dir>/\") " +
			"and per group (subject group, the walk re-run on the summed columns). Sort people by majority_files descending, prod " +
			"lines descending, email ascending, and walk until the running sum of majority_files is at least files_with_majority / 2. " +
			"Detail: files_with_majority (each file once, even with two owners), bots_in_prefix, prod_files.",
		Ranked:      false,
		Eligibility: "Not ranked: it describes code, not people.",
		BlindSpots: "Only majority-owned files count, so a codebase where nobody holds half of any file looks unconcentrated. " +
			"It is not a truck factor: it ignores who could take over a file, and blame's blind spots apply.",
		Gaming:   "Not a target for people; it moves when ownership moves.",
		Oracle:   "testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle (the concentration walk)",
		Validity: "Context, never ranked. Built on majority_owner@1; see its evidence. Avelino, Passos, Hora and Valente's truck factor (ICPC 2016) is the validated algorithm this deliberately is not.",
	}
}

// Compute returns knowledge_concentration@1's values.
func (m knowledgeConcentrationV1) Compute(in *Input) ([]domain.MetricValue, error) {
	return computeAlone(m, in)
}

func (knowledgeConcentrationV1) compute(v *view) ([]domain.MetricValue, error) {
	var out []domain.MetricValue
	groups := make(map[string][]*fileView)
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		files := r.sortedFiles()
		repoScope := domain.Scope{Kind: domain.ScopeRepo, Key: r.key}
		if val, ok := concentration(v, repoScope, repoSubject(r.key), files); ok {
			out = append(out, val)
		}
		for _, area := range byTopDir(files) {
			if val, ok := concentration(v, repoScope, areaSubject(r.key, area.dir), area.files); ok {
				out = append(out, val)
			}
		}
		if r.group != "" {
			groups[r.group] = append(groups[r.group], files...)
		}
	}
	for name, files := range groups {
		if val, ok := concentration(v, domain.Scope{Kind: domain.ScopeGroup, Key: name}, groupSubject(name), files); ok {
			out = append(out, val)
		}
	}
	return sorted(out), nil
}

// area is the files under one top-level directory.
type area struct {
	dir   string // "src/"
	files []*fileView
}

// byTopDir groups files, sorted by path, under their top-level directories,
// in directory order. Files at the root belong to no directory; the
// repository's own value covers them.
func byTopDir(files []*fileView) []area {
	var out []area
	for _, f := range files {
		dir := topDir(f.path)
		if dir == "" {
			continue
		}
		if len(out) == 0 || out[len(out)-1].dir != dir {
			out = append(out, area{dir: dir})
		}
		out[len(out)-1].files = append(out[len(out)-1].files, f)
	}
	return out
}

// concentration runs the walk over files. ok is false when no file has a
// majority owner, which leaves nothing to walk.
func concentration(v *view, s domain.Scope, sub domain.Subject, files []*fileView) (domain.MetricValue, bool) {
	type person struct {
		email           string
		majority, lines int
	}
	people := make(map[string]*person)
	withMajority := 0
	for _, f := range files {
		if len(f.owners) > 0 {
			withMajority++
		}
		for email, n := range f.byPerson {
			lookup(people, email).lines += n
		}
		for _, email := range f.owners {
			people[email].majority++
		}
	}
	if withMajority == 0 {
		return domain.MetricValue{}, false
	}
	var walk []*person
	for email, p := range people {
		if p.majority > 0 {
			p.email = email
			walk = append(walk, p)
		}
	}
	slices.SortFunc(walk, func(a, b *person) int {
		return cmp.Or(cmp.Compare(b.majority, a.majority), cmp.Compare(b.lines, a.lines), cmp.Compare(a.email, b.email))
	})
	authors, bots, running := 0, 0, 0
	for _, p := range walk {
		authors++
		if v.who.Bot(p.email) {
			bots++
		}
		running += p.majority
		if 2*running >= withMajority { // running >= files_with_majority / 2, in true division
			break
		}
	}
	return count(knowledgeConcentrationID, s, sub, domain.Window{}, authors, 0, map[string]float64{
		"files_with_majority": float64(withMajority),
		"bots_in_prefix":      float64(bots),
		"prod_files":          float64(len(files)),
	}), true
}
