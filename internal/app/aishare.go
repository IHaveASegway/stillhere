package app

import (
	"cmp"
	"maps"
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/report"
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
// answer about the run under one version of these rules reads the same
// one: no two calls disagree, and no pair of answers about the run can be
// subtracted to give what one of them withholds. Two decisions made here
// alone can be: those of two runs, since each run is decided on its own (a
// rescan of fewer repositories minutes later, in which no landing entered
// or left the window, gives a share the first run withheld exactly), and
// those one run gets from two versions whose count of people, threshold or
// order of suppression differ. What leaves the machine is therefore
// decided once more, against the egress record (egress.go, ADR 0015): a
// run's decision is recorded before its first share leaves, every later
// call by any version reads that record, and a new run's decision is
// checked against the shares recorded for every window that overlaps its
// own.
//
// The rules, bottom-up over the tree of scopes: the whole run, its groups,
// their repositories, and the repositories named on their own, which hang
// from the run.
//
//   - Primary: a scope's share is withheld when fewer than
//     AIShareMinPeople people landed in its window (aiShareCount counts
//     them over aiSharePeople's sets, the one rule for who a share is
//     made of, and errs low: it counts as one person the addresses
//     identity.Blocks puts in one block, and adds no one for an empty
//     address beside others).
//   - Secondary: the children of a scope whose shares are withheld leave a
//     part of it that no shown share covers. When the people of that part
//     number 1 or 2, more children are withheld, the smallest first (fewest
//     people, then kind and key), until they number AIShareMinPeople or
//     more; a child whose withholding would add no one is passed over. When
//     no child can bring them there, the scope's own share is withheld.
//     Only the run can come to that: a shown group whose withheld
//     repositories credit no one (it has none, or they hold only bots'
//     landings) adds no one when it is withheld, while withholding a
//     shown repository brings the part to AIShareMinPeople or more.
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
//  4. People are what aiShareCount counts over aiSharePeople's sets. The
//     argument asks two things of that count: it is never more than the
//     humans behind a set's landings, so three counted are three humans
//     or more, and it is 0 only for a set with no person's landing (a
//     bot's landings credit no one), so a U that holds no one holds no
//     person's landings. Blocks give the first wherever they
//     join every address of each human, whether or not they also join two
//     humans wrongly; a missed link breaks it (#86). The empty address
//     keeps both: beside others it adds no one, and alone it is one.
//     Counting it as no one broke the second: a repository whose only
//     author had no email looked empty, so its share could be subtracted
//     out of its group's.
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

// aiShareInputs is what a run's decision about its AI-assisted shares is
// made from, read once from the stored run: its repositories with their
// groups, the people behind each repository's share (aiSharePeople), each
// repository's ai_assisted@1 value, whose counts add up to a group's and
// the whole run's share as report.Build adds them, and whether the whole
// run's share reaches an answer (report.WholeRun). The run's own decision
// (decide) and the one the egress record holds (egress.go) are made from
// the same inputs; the record also matches each repository by the landings
// its value counts (history), which a copy shares under another key.
type aiShareInputs struct {
	repos   []domain.Repo
	people  map[string]map[string]bool    // by Repo.Key, for a repository with a share
	values  map[string]domain.MetricValue // by Repo.Key: its ai_assisted@1 value
	history map[string]string             // by Repo.Key: egressHistory of its landings in its value's window
	whole   bool
}

// aiShareInputsOf reads a stored run's inputs.
func aiShareInputsOf(s *stored) *aiShareInputs {
	in := &aiShareInputs{people: aiSharePeople(s), values: map[string]domain.MetricValue{}, history: map[string]string{},
		whole: report.WholeRun(s.scope(), len(s.facts.Groups))}
	for _, rf := range s.facts.Repos {
		in.repos = append(in.repos, rf.Repo)
	}
	for _, v := range s.values {
		if v.Metric.String() == report.MetricAIAssisted && v.Subject.Kind == domain.SubjectRepo {
			in.values[v.Subject.Key] = v
		}
	}
	for i := range s.facts.Repos {
		rf := &s.facts.Repos[i]
		if v, ok := in.values[rf.Repo.Key]; ok {
			in.history[rf.Repo.Key] = egressHistory(rf.Landings, v.Window)
		}
	}
	return in
}

// decide is the run's own decision (aiShareDecide), with the scopes forced
// holds withheld whatever their people: what the egress record's check
// (egress.go) asks of the run after the shares already sent.
func (in *aiShareInputs) decide(forced map[domain.Scope]bool) AIShares {
	return aiShareDecide(in.repos, in.people, in.whole, forced)
}

// aiSharePeople returns, per repository with an AI-assisted share, the
// people the share is made of as the threshold counts them: the humans
// credited with a non-merge commit of a landing in its window
// (mcpAIPeople), with the addresses of one block counted as one person
// (identity.Blocks over the whole run: what the identities rules link,
// every hand-off chain, and every two addresses that share a name, any
// name seen with either and a default one included, a local part or a
// GitHub account, without the vetoes those rules apply).
//
// A .mailmap merges people on every page; this merges no one on any page,
// and only makes the count err low, never high. One person who committed
// from a work address, a laptop's made-up one and GitHub's noreply one,
// which no .mailmap merges, is one person here whenever the three share a
// name or a local part, even spelt as Sam Solo, S. Solo and Samuel Solo,
// which identities, leaning to precision, leaves apart: a share of theirs
// alone is their own share. Two people a block joins wrongly count as one,
// which lowers a count and never raises one, so a wrong link never lets
// out a share of fewer than three people. Only a missed link can: what no
// key joins still counts apart, addresses with nothing in common (#86).
// Nor does a lower count make the decision monotone: it can move secondary
// suppression to another sibling, so a version that counts otherwise
// decides a stored run otherwise (ADR 0007's amendment, known limits).
//
// The one address no block holds is the empty one: git records a commit
// with no author email, and so do the facts. It is a missing fact, not a
// person, so a set keeps it as "" for aiShareCount, which counts it only
// when no one else is there: beside others it may be any of them, and
// alone it is still someone. Counted as a person of its own, it made one
// person who once committed without an email two (#74). That holds while
// the run takes "" for a person's: identity decides it once per run, by
// the name most often seen with it, and a bot's "" is in no set, its
// landings tagged by no one (#74).
func aiSharePeople(s *stored) map[string]map[string]bool {
	block := identity.Blocks(s.facts, s.who)
	out := map[string]map[string]bool{}
	for key, people := range mcpAIPeople(s) {
		counted := make(map[string]bool, len(people))
		for p := range people {
			if b, ok := block[p]; ok {
				p = b
			} else {
				p = "" // no block holds it: the empty address, someone unknown
			}
			counted[p] = true
		}
		out[key] = counted
	}
	return out
}

// aiShareCount is the fewest people a set from aiSharePeople can be made
// of: its blocks, where the empty address ("") may be any of the others,
// so it adds one only to a set that holds no one else. It is 0 only for a
// set with no person's landing, which is what a part that covers no one
// must mean to the residual rule.
func aiShareCount(people map[string]bool) int {
	n := len(people)
	if people[""] && n > 1 {
		n--
	}
	return n
}

// aiShareDecide decides a run's AI-assisted shares: repos are the run's
// repositories with their groups (Repo.Group), people those behind each
// repository's share as the threshold counts them (aiSharePeople), and
// whole says whether the whole run's share reaches any answer
// (report.WholeRun); a scope in forced is withheld whatever its people,
// and the rules decide the rest around it. It is a pure function of its
// arguments, so every call about one run gets the same decision.
func aiShareDecide(repos []domain.Repo, people map[string]map[string]bool, whole bool, forced map[domain.Scope]bool) AIShares {
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
	run.decide(whole, forced)
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
// n's share can reach an answer at all, and forced holds the scopes
// withheld whatever their people.
func (n *aiShareNode) decide(reaches bool, forced map[domain.Scope]bool) {
	if n.scope.Kind != domain.ScopeRepo {
		n.people = map[string]bool{}
		for _, c := range n.children {
			c.decide(true, forced)
			maps.Copy(n.people, c.people)
		}
	}
	if forced[n.scope] {
		return // withheld for the shares already sent (egress.go): the scope above counts its people as uncovered
	}
	if !reaches || aiShareCount(n.people) < AIShareMinPeople {
		return // the primary rule; such a scope's children are all withheld too
	}
	u := map[string]bool{}
	n.uncovered(u)
	if c := aiShareCount(u); c > 0 && c < AIShareMinPeople {
		var children []*aiShareNode
		for _, c := range n.children {
			if c.shown {
				children = append(children, c)
			}
		}
		slices.SortFunc(children, func(a, b *aiShareNode) int {
			return cmp.Or(cmp.Compare(aiShareCount(a.people), aiShareCount(b.people)), aiShareCompare(a.scope, b.scope))
		})
		for _, c := range children {
			if aiShareCount(u) >= AIShareMinPeople {
				break
			}
			with := maps.Clone(u)
			c.uncovered(with)
			if aiShareCount(with) == aiShareCount(u) {
				continue // withholding it would add no one
			}
			u, c.shown = with, false
		}
		if aiShareCount(u) < AIShareMinPeople {
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
