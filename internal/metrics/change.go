package metrics

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// This file holds the change facts that durable@1 and simplifying@1 share
// (docs/metrics.md#change-facts-shared-by-durable1-and-simplifying1): which
// commits count, and the clusters they form. Both metrics read the same
// clusters, so a rule such as "a merge commit itself never counts" lives in
// one place.

// cluster is one person's counted commits in one landing: the unit
// eligibility counts and the bootstrap resamples.
type cluster struct {
	repo    *repoView
	landing *landingView
	person  string
	// added and removed are non-blank prod lines in the commits' own
	// patches. traced is the part of added whose fate blame can tell, the
	// denominator durable@1 divides surviving by, and surviving is what
	// blame attributes to those commits at HEAD, the untraced lines left
	// out of both alike (untracedLines).
	added, removed, traced, surviving int
}

// counts reports whether a commit counts in the change metrics: neither a
// merge commit itself (v1 has no combined diff) nor a commit the
// repository's ignore-revs file lists. Revert landings are excluded a level
// up, landing by landing.
func (c *commitView) counts() bool {
	return !c.merge && !c.ignored
}

// changeClusters returns the clusters of the landings in w, in repository,
// landing and person order. Revert landings never form clusters; reverted
// landings form them unless dropReverted, which is how simplifying@1 leaves
// out every commit of a reverted landing while durable@1 keeps it (its lines
// are gone, so it already scores nothing). needLines also skips repositories
// without blame, whose surviving lines are unknown; within a repository with
// blame, the lines whose survival blame cannot tell (on a file whose blame
// failed, or of a change git took for binary) are left out of traced and
// surviving alike.
func changeClusters(v *view, w domain.Window, needLines, dropReverted bool) []*cluster {
	var out []*cluster
	for _, r := range v.repos {
		if !r.patches || (needLines && !r.lines) || !r.covers(w) {
			continue
		}
		for _, l := range r.landings {
			if !w.Contains(l.time) || l.revert || (dropReverted && l.reverted) {
				continue
			}
			var mine []*cluster // this landing's clusters, one per person
			for _, c := range l.commits {
				if !c.counts() {
					continue
				}
				i := slices.IndexFunc(mine, func(k *cluster) bool { return k.person == c.credit })
				if i < 0 {
					mine = append(mine, &cluster{repo: r, landing: l, person: c.credit})
					i = len(mine) - 1
				}
				k := mine[i]
				k.added += c.added
				k.removed += c.removed
				k.traced += c.traced
				k.surviving += c.surviving
			}
			slices.SortFunc(mine, func(a, b *cluster) int { return cmp.Compare(a.person, b.person) })
			out = append(out, mine...)
		}
	}
	return out
}

// rankData turns clusters into rank's input: each person's clusters, sorted
// by change (then repository, for a SHA two scanned forks share). Bots are
// never ranked, so they are left out; an empty result is nil.
func rankData(v *view, clusters []*cluster, toRank func(*cluster) rank.Cluster) map[domain.Subject][]rank.Cluster {
	byPerson := make(map[string][]*cluster)
	for _, k := range clusters {
		if !v.who.Bot(k.person) {
			byPerson[k.person] = append(byPerson[k.person], k)
		}
	}
	if len(byPerson) == 0 {
		return nil
	}
	data := make(map[domain.Subject][]rank.Cluster, len(byPerson))
	for person, ks := range byPerson {
		slices.SortFunc(ks, func(a, b *cluster) int {
			return cmp.Or(cmp.Compare(a.landing.sha, b.landing.sha), cmp.Compare(a.repo.key, b.repo.key))
		})
		cs := make([]rank.Cluster, len(ks))
		for i, k := range ks {
			cs[i] = toRank(k)
		}
		data[personSubject(person)] = cs
	}
	return data
}

// scopeView projects in for the repositories of scope s, so ranking one
// repository does not index every other.
func scopeView(in *Input, s domain.Scope) (*view, error) {
	switch s.Kind {
	case domain.ScopeRepo, domain.ScopeGroup, domain.ScopeAll:
	default:
		return nil, fmt.Errorf("metrics: unknown scope kind %q", s.Kind)
	}
	return newView(in, func(f *domain.RepoFacts) bool { return inScope(s, f.Repo.Key, f.Repo.Group) })
}
