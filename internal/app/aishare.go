package app

import (
	"cmp"
	"maps"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Which AI-assisted shares an answer that leaves the machine may hold (ADR
// 0007 and its amendment of 2026-10-15, docs/spec/mcp.md). A share over the
// landings of one or two people is close to one person's own share, which
// ADR 0007 shows only to that person.
//
// Why a decision of its own. A share is a ratio of counts, and the counts
// add up over the scopes: a group's landings are its repositories', the
// whole run's are every repository's. Shares sent side by side can be
// subtracted: a group's share less the shares of its repositories that are
// shown is the share of those that are not. Withholding a small scope's
// share on its own does not hide it; a scope around it must be withheld
// too wherever the rest would give it away. Statistical agencies publish
// tables this way, and call it secondary, or complementary, suppression
// (Hundepool et al., Statistical Disclosure Control, Wiley 2012, ch. 4;
// tau-ARGUS's modular method for hierarchical tables; R's sdcTable). The
// decision is made once per run, from the run's facts alone, so every
// answer about the run reads the same one: no two calls disagree, and no
// pair of answers can be subtracted to give what one of them withholds.
//
// The rules, bottom-up over the tree of scopes: the whole run, its groups,
// their repositories, and the repositories named on their own, which hang
// from the run.
//
//   - Primary: a scope's share is withheld when fewer than
//     AIShareMinPeople people landed in its window (mcpAIPeople counts them,
//     the one rule for who a share is made of).
//   - Secondary: the children of a scope whose shares are withheld leave a
//     part of it that no shown share covers. When the people of that part
//     number 1 or 2, more children are withheld, the smallest first (fewest
//     people, then kind and key), until they number AIShareMinPeople or
//     more; a child whose withholding would add no one is passed over. When
//     no child can bring them there, the scope's own share is withheld.
//     Only the run can come to that: a group whose repositories are all
//     shown adds no one when it is withheld, and a group's children are
//     repositories, each of which adds its own people.
//
// Why that is enough. For a shown scope S, call U(S) the repositories under
// S that no shown scope below S covers, so that S's share less the shares
// shown below it is U(S)'s.
//
//  1. Shown scopes nest: a repository is in one group at most, the
//     Repo.Group the metric itself adds the group's share up by, which is
//     the group the tree hangs it from, so a repository in two groups
//     cannot occur in the shares. Every shown share is then the sum of the
//     U of itself and of the shown scopes below it, and the U of two shown
//     scopes are disjoint (a repository is in the U of its lowest shown
//     scope only). Whatever answers are combined, at one level or across
//     levels, what they show is sums of whole U(S): a laminar family's
//     residuals span what its sums span, and nothing finer.
//  2. Every U(S) of a shown S holds no one, or at least AIShareMinPeople
//     people. Bottom-up: a repository's U is itself, shown only with that
//     many people. A scope's U is the union of the U of its withheld
//     children. A shown child's U holds no one or that many, by induction,
//     and a shown repository at least that many, so withholding one more
//     child adds no one or brings the scope's U to that many; failing both,
//     the scope is withheld. Withholding a child changes the U of no scope
//     below it, and a scope is decided after its children, so each U is
//     final when it is checked.
//  3. Three, not two: one of a U's people who takes their own landings away
//     still sees two people's together, which is the reason the minimum
//     frequency rule of disclosure control asks for three.
//
// The whole run's share reaches an answer only in an overview of every
// repository, which a run has when it was scanned with --scope all or has
// no groups (report.WholeRun). Otherwise its share is withheld here and
// asks nothing of its children: a run with one group and a repository of
// one person named on its own withholds neither the group's share nor its
// repositories' on the run's account.

// AIShareMinPeople is the fewest people whose landings an AI-assisted share
// that leaves the machine may be made of, and the fewest the part of a
// shown share that no other shown share covers may be made of, unless it is
// made of no one.
const AIShareMinPeople = 3

// AIShares is a run's one decision about which AI-assisted shares may
// leave the machine. Every answer about the run reads it.
type AIShares struct {
	// Shown lists the scopes whose share may leave, sorted by kind (all,
	// group, repo) and key: the whole run is the scope of kind all. A
	// scope it does not list is withheld, so an AIShares never decided
	// withholds every share.
	Shown []domain.Scope
}

// Show reports whether the AI-assisted share of a scope may leave the
// machine; the zero Scope is the whole run.
func (a AIShares) Show(sc domain.Scope) bool {
	if sc.Kind == "" {
		sc.Kind = domain.ScopeAll
	}
	_, ok := slices.BinarySearchFunc(a.Shown, sc, aiShareCompare)
	return ok
}

func aiShareCompare(a, b domain.Scope) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Key, b.Key))
}

// aiShareNode is one scope of the tree the shares add up over.
type aiShareNode struct {
	scope    domain.Scope
	people   map[string]bool // who landed in its window: a repository's own, or its children's together
	children []*aiShareNode  // none for a repository
	shown    bool
}

// aiShareDecide decides a run's AI-assisted shares: repos are the run's
// repositories with their groups (Repo.Group), people the humans behind
// each repository's share (mcpAIPeople), and whole says whether the whole
// run's share reaches any answer (report.WholeRun). It is a pure function
// of the run's facts, so every call about one run gets the same decision.
func aiShareDecide(repos []NamedRepo, people map[string]map[string]bool, whole bool) AIShares {
	run := &aiShareNode{scope: domain.Scope{Kind: domain.ScopeAll}}
	groups := map[string]*aiShareNode{}
	for _, r := range repos {
		parent := run
		if r.Group != "" {
			if parent = groups[r.Group]; parent == nil {
				parent = &aiShareNode{scope: domain.Scope{Kind: domain.ScopeGroup, Key: r.Group}}
				groups[r.Group] = parent
				run.children = append(run.children, parent)
			}
		}
		parent.children = append(parent.children, &aiShareNode{
			scope: domain.Scope{Kind: domain.ScopeRepo, Key: r.Key}, people: people[r.Key]})
	}
	run.decide(whole)
	var shown []domain.Scope
	var collect func(n *aiShareNode)
	collect = func(n *aiShareNode) {
		if n.shown {
			shown = append(shown, n.scope)
		}
		for _, c := range n.children {
			collect(c)
		}
	}
	collect(run)
	slices.SortFunc(shown, aiShareCompare)
	return AIShares{Shown: shown}
}

// decide decides n's share and every share below it; reaches says whether
// n's share can reach an answer at all.
func (n *aiShareNode) decide(reaches bool) {
	if n.scope.Kind != domain.ScopeRepo {
		n.people = map[string]bool{}
		for _, c := range n.children {
			c.decide(true)
			maps.Copy(n.people, c.people)
		}
	}
	if !reaches || len(n.people) < AIShareMinPeople {
		return // the primary rule; such a scope's children are all withheld too
	}
	u := map[string]bool{}
	n.uncovered(u)
	if len(u) > 0 && len(u) < AIShareMinPeople {
		var children []*aiShareNode
		for _, c := range n.children {
			if c.shown {
				children = append(children, c)
			}
		}
		slices.SortFunc(children, func(a, b *aiShareNode) int {
			return cmp.Or(cmp.Compare(len(a.people), len(b.people)), aiShareCompare(a.scope, b.scope))
		})
		for _, c := range children {
			if len(u) >= AIShareMinPeople {
				break
			}
			add := map[string]bool{}
			c.uncovered(add)
			before := len(u)
			maps.Copy(u, add)
			if len(u) == before {
				continue // withholding it would add no one
			}
			c.shown = false
		}
		if len(u) < AIShareMinPeople {
			return // no child can cover the part: the scope itself is withheld
		}
	}
	n.shown = true
}

// uncovered adds to u the people of U(n): of n's repositories that no shown
// scope below n covers. A repository's is its own people, and n's own
// status does not count, so a shown child's U is what withholding it adds.
func (n *aiShareNode) uncovered(u map[string]bool) {
	if n.scope.Kind == domain.ScopeRepo {
		maps.Copy(u, n.people)
		return
	}
	for _, c := range n.children {
		if !c.shown {
			c.uncovered(u)
		}
	}
}
