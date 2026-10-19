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
	// reverted is true when one of the commits holds a change a revert in
	// a later landing took off the branch (commitView.reverted).
	reverted bool
}

// counts reports whether a commit counts in the change metrics: neither a
// merge commit itself (v1 has no combined diff), nor a commit the
// repository's ignore-revs file lists, nor a copy of a change an earlier
// landing brought (markCopies), which its original already counts: blame
// gives the copy's lines to the original, so durable@1 would score it 0,
// and simplifying@1 would count its removal twice. Nor does a revert
// commit, nor a commit a revert in its own landing took back (markReverts,
// #43): an undo is not new work, and a change its branch withdrew before
// landing never reached the default branch, so the pair cancels and the
// rest of the landing counts. A reverted commit, one a later landing's
// revert names, counts unless the metric drops it (changeClusters). So a
// landing made only of copies, or of reverts, forms no cluster.
func (c *commitView) counts() bool {
	return !c.merge && !c.ignored && !c.copy && !c.revert && !c.withdrawn
}

// leftOut reports whether the revert rule alone keeps c out of a change
// metric (markReverts, #43): a commit that is no merge, ignore-revs commit
// or copy, but is a revert commit, or one a revert in its own landing took
// back, or, with dropReverted (simplifying@1's rule), one a later landing's
// revert names.
func (c *commitView) leftOut(dropReverted bool) bool {
	return !c.merge && !c.ignored && !c.copy && (c.revert || c.withdrawn || (dropReverted && c.reverted))
}

// detailRevertsLeftOut is the Detail key of every value of the change
// metrics, durable@1's and simplifying@1's per person and ai_assisted@1's
// per repository and group: the commits the revert rule left out
// (revertsLeftOut, aiCohorts). Every value carries it, 0 included, so a run
// stored before reverts moved to commit grain, whose values lack it, is
// told by its data (corrections).
const detailRevertsLeftOut = "reverts_left_out"

// changeRepo reports whether the change metrics read repository r's
// commits in w: its history holds their patches and covers w, and, with
// needLines, blame ran in it, so its surviving lines are known.
func (r *repoView) changeRepo(w domain.Window, needLines bool) bool {
	return r.patches && (!needLines || r.lines) && r.covers(w)
}

// revertsLeftOut counts, per scope and person, their commits in the
// landings of w that the revert rule leaves out (commitView.leftOut), in
// the repositories changeClusters reads: durable@1's and simplifying@1's
// Detail reverts_left_out. A person whose commits are all left out has no
// value to carry it.
func revertsLeftOut(v *view, w domain.Window, needLines, dropReverted bool) map[personKey]int {
	out := make(map[personKey]int)
	for _, r := range v.repos {
		if !r.changeRepo(w, needLines) {
			continue
		}
		for _, l := range r.landings {
			if !w.Contains(l.time) {
				continue
			}
			for _, c := range l.commits {
				if !c.leftOut(dropReverted) {
					continue
				}
				for _, s := range r.scopes {
					out[personKey{s, c.credit}]++
				}
			}
		}
	}
	return out
}

// changeClusters returns the clusters of the landings in w, in repository,
// landing and person order, of the commits that count (commitView.counts).
// A reverted commit counts unless dropReverted, which is how simplifying@1
// leaves out every commit a later landing's revert names while durable@1
// keeps it (its lines are gone, so it scores what blame still gives it, 0
// for a plain revert, and its cluster is flagged reverted). needLines also
// skips repositories without blame, whose surviving lines are unknown;
// within a repository with blame, the lines whose survival blame cannot
// tell (on a file whose blame failed, or of a change git took for binary)
// are left out of traced and surviving alike.
func changeClusters(v *view, w domain.Window, needLines, dropReverted bool) []*cluster {
	var out []*cluster
	for _, r := range v.repos {
		if !r.changeRepo(w, needLines) {
			continue
		}
		for _, l := range r.landings {
			if !w.Contains(l.time) {
				continue
			}
			var mine []*cluster // this landing's clusters, one per person
			for _, c := range l.commits {
				if !c.counts() || (dropReverted && c.reverted) {
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
				k.reverted = k.reverted || c.reverted
			}
			slices.SortFunc(mine, func(a, b *cluster) int { return cmp.Compare(a.person, b.person) })
			out = append(out, mine...)
		}
	}
	return out
}

// rankData turns clusters into rank's input: each subject's clusters,
// sorted by change (then repository, for a SHA two scanned forks share),
// bots' included, which RankInput.withData sets apart; an empty result is
// nil.
func rankData(clusters []*cluster, toRank func(*cluster) rank.Cluster) map[domain.Subject][]rank.Cluster {
	byPerson := make(map[string][]*cluster)
	for _, k := range clusters {
		byPerson[k.person] = append(byPerson[k.person], k)
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
