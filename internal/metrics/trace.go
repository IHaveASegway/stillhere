package metrics

import (
	"cmp"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Traced is a metric that is not ranked whose value about a person can name
// the facts it counted (#109): the landings landed@1 and landed@2 count, the
// blamed commits surviving@1 sums, the files majority_owner@1 counts. So
// explain reaches the commits behind every number, as product.md's second
// principle promises, without a second implementation of what a metric
// counts: each metric traces with the projection and the rule it computes
// with. A ranked metric's are its clusters (Ranked).
type Traced interface {
	Metric
	// Trace returns what the person's values are made of, over every
	// repository of the run, each item with its repository's key. A person
	// with no value gets an empty trace, with the window the metric reads.
	Trace(in *Input, person string) (domain.Trace, error)
}

// Trace lists the landings landed@1 counts for person: those whose landing
// commit is credited to them, with that commit's lines.
func (landedV1) Trace(in *Input, person string) (domain.Trace, error) {
	v, err := newView(in, nil)
	if err != nil {
		return domain.Trace{}, err
	}
	if v.daysErr != nil {
		return domain.Trace{}, v.daysErr
	}
	w := v.window(v.days, 0)
	t := domain.Trace{Window: w}
	for _, r := range v.repos {
		landings, _ := landedIn(r, w)
		for _, l := range landings {
			if l.commit.credit != person {
				continue
			}
			lines, prod := landedLines(r, l.commit)
			t.Landings = append(t.Landings, tracedLanding(r, l, 1, lines, prod))
		}
	}
	sortTracedLandings(t.Landings)
	return t, nil
}

// Trace lists the landings landed@2 counts for person: those that hold a
// non-merge commit credited to them, with those commits' lines. The merge
// landings that are theirs only by the merge commit, its Detail merged,
// hold none of their commits and are not listed.
func (landedV2) Trace(in *Input, person string) (domain.Trace, error) {
	v, err := newView(in, nil)
	if err != nil {
		return domain.Trace{}, err
	}
	if v.daysErr != nil {
		return domain.Trace{}, v.daysErr
	}
	w := v.window(v.days, 0)
	t := domain.Trace{Window: w}
	for _, r := range v.repos {
		landings, _ := landedIn(r, w)
		for _, l := range landings {
			for _, p := range landedV2Parts(r, l).people {
				if p.email == person {
					t.Landings = append(t.Landings, tracedLanding(r, l, p.commits, p.lines, p.prod))
				}
			}
		}
	}
	sortTracedLandings(t.Landings)
	return t, nil
}

// tracedLanding is one landing of r counted for a person, with lines its
// counted commits added, or -1 when r's history counted no numstat (#74).
func tracedLanding(r *repoView, l *landingView, commits, lines, prod int) domain.TracedLanding {
	if !r.numstat {
		lines, prod = -1, -1
	}
	return domain.TracedLanding{Repo: r.key, SHA: l.sha, Time: l.time, Merge: l.commit.merge, Commits: commits,
		Lines: lines, ProdLines: prod}
}

func sortTracedLandings(ls []domain.TracedLanding) {
	slices.SortFunc(ls, func(a, b domain.TracedLanding) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), a.Time.Compare(b.Time), cmp.Compare(a.SHA, b.SHA))
	})
}

// Trace lists the blamed commits whose prod lines at HEAD surviving@1
// credits to person, in the repositories blame ran in: the rows its value
// sums, by commit.
func (survivingV1) Trace(in *Input, person string) (domain.Trace, error) {
	v, err := newView(in, nil)
	if err != nil {
		return domain.Trace{}, err
	}
	var t domain.Trace
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		byCommit := map[string]*domain.TracedCommit{}
		for _, o := range r.owners {
			if o.credit != person || o.role != domain.RoleProd {
				continue
			}
			c := byCommit[o.commit]
			if c == nil {
				c = &domain.TracedCommit{Repo: r.key, SHA: o.commit, Time: o.at}
				byCommit[o.commit] = c
			}
			c.Lines += o.lines
		}
		for _, c := range byCommit {
			t.Commits = append(t.Commits, *c)
		}
	}
	slices.SortFunc(t.Commits, func(a, b domain.TracedCommit) int {
		return cmp.Or(cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.SHA, b.SHA))
	})
	return t, nil
}

// Trace lists the prod files majority_owner@1 counts for person: those at
// HEAD where they hold at least half the non-blank lines.
func (majorityOwnerV1) Trace(in *Input, person string) (domain.Trace, error) {
	v, err := newView(in, nil)
	if err != nil {
		return domain.Trace{}, err
	}
	var t domain.Trace
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		for _, f := range r.sortedFiles() {
			if slices.Contains(f.owners, person) {
				t.Files = append(t.Files, domain.TracedFile{Repo: r.key, Path: f.path, Lines: f.byPerson[person], Of: f.total})
			}
		}
	}
	slices.SortFunc(t.Files, func(a, b domain.TracedFile) int {
		return cmp.Or(cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Path, b.Path))
	})
	return t, nil
}
