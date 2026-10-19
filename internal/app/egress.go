package app

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The egress record's rule (#85, ADR 0015). aishare.go decides one run's
// AI-assisted shares so that no two answers about the run can be subtracted
// to give a share of one or two people. Answers about two runs can be: a
// scan of group ai shows the group at 4 of 16 with team and solo withheld, a
// rescan of team alone at the same as-of time shows team at 1 of 12, and
// (4 - 1) of (16 - 12) is solo's one person's own 3 of 4. So can answers
// about one run from two versions whose rules decide it differently. Both
// are closed by deciding against what already left: an answer that left the
// machine is a fact (Helland, "Immutability Changes Everything", CIDR 2015),
// and it is the record of those facts, not a rule recomputed by whichever
// binary runs, that a new decision must agree with.
//
//  1. Write before send, as a transactional outbox records a message before
//     it is sent. The first time an answer about a run would hold a share,
//     the adapter calls ShareDecision, which returns the decision the record
//     holds for the run, by any version, or decides one, records it durably
//     and only then returns it. A record whose decision was made over other
//     numbers than the run holds now (Inputs) is ErrEgressInputs: the run
//     was rewritten, and its new numbers could be subtracted from the old.
//  2. Against what was sent. The shares recorded for the run's window (the
//     same as-of and length) and the run's own form one table over
//     repositories, whatever run each came from. The run's shares may leave
//     only when that table keeps the property aishare.go proves for one run:
//     its sets of repositories nest (a laminar family), and every residual,
//     a set less the sets below it, holds no one or at least
//     AIShareMinPeople people. A repository must carry the same counts in
//     every run of the window that sends a share of it, or the difference
//     of two shares would be its change between the scans (a policy or a
//     .mailmap changed). When it does not hold, the run's offending shares
//     are withheld, the run's own
//     rules decide again around them (aiShareInputs.decide with forced), and
//     the check is made again, until it holds: each round withholds one
//     more of the run's shares, so it ends.
//  3. Fail safe (Saltzer and Schroeder, "The Protection of Information in
//     Computer Systems", 1975: fail-safe defaults). An adapter that cannot
//     read or write the record withholds every share; ShareDecision returns
//     the error, and decides nothing it has not recorded.
//
// Why a table and not a check of pairs, as the lane's first design had it:
// a pair (a share S, a recorded share R whose repositories hold S's) counted
// the people of S less the people of R. Those are not the residual's
// people: the residual is the share of R's repositories that S lacks, made
// of the people who landed there, some of whom may land in S too. One person
// who lands in team and alone in solo leaves no one in people(group) less
// people(team), so a pair check lets team out beside the group, and the
// difference is that person's own share in solo. Pairs also miss a residual
// three shares make: a group of r1, r2 and r3 from one run, r2 shown by
// another and r3 by a third, give r1's share; and two sets that overlap
// without nesting, a group of r1, r2 and r3 and another of r2, r3 and r4,
// give r1's beside r4's.

// aiShareMetric is the metric whose shares the record decides.
var aiShareMetric = domain.MetricID{Name: "ai_assisted", Version: 1}

// ErrEgressInputs is ShareDecision's refusal of a run whose recorded
// decision was made over other AI-assisted numbers than the run holds now.
var ErrEgressInputs = errors.New("the egress record holds a decision about this run made over other AI-assisted numbers " +
	"than it holds now: its shares stay local, since the new numbers could be subtracted from those sent")

// ShareDecision returns the decision about n's run that may leave the
// machine (ADR 0015): the one d.Egress holds for the run, else one decided
// from n's inputs against every decision recorded for the run's window, and
// recorded before it is returned. It returns every error of the record as
// it is, and ErrEgressInputs; either way the caller withholds every share.
// A run with no AI-assisted value has nothing to decide: the zero decision,
// recorded nowhere.
func ShareDecision(ctx context.Context, d Deps, n *Names) (AIShares, error) {
	if d.Egress == nil {
		return AIShares{}, errors.New("app.ShareDecision: no egress record")
	}
	in := n.aiInputs
	if in == nil || len(in.values) == 0 {
		return AIShares{}, nil
	}
	window := in.window()
	inputs := in.digest(window)
	run := domain.EgressRun{ID: n.Run.ID, Started: n.Run.Started, Window: window}
	dec, made, err := d.Egress.Decide(ctx, run, func(salt []byte, sent []domain.EgressDecision) (domain.EgressDecision, error) {
		return in.against(salt, sent, inputs, d.Version), nil
	})
	if err != nil {
		return AIShares{}, err
	}
	if dec.Inputs != inputs || dec.Metric != aiShareMetric {
		return AIShares{}, ErrEgressInputs
	}
	shown := make([]domain.Scope, 0, len(dec.Shares))
	for _, sh := range dec.Shares {
		shown = append(shown, sh.Scope)
	}
	slices.SortFunc(shown, aiShareCompare)
	if made && d.Log != nil {
		d.Log.Info("recorded the AI-assisted shares that may leave", "run", n.Run.ID, "shares", len(shown))
	}
	return AIShares{Shown: shown}, nil
}

// window is the window of the run's AI-assisted shares: every repository's
// value has the same, 365 days before --now; the earliest key's otherwise.
func (in *aiShareInputs) window() domain.Window {
	keys := slices.Sorted(maps.Keys(in.values))
	if len(keys) == 0 {
		return domain.Window{}
	}
	return in.values[keys[0]].Window
}

// digest is the hex SHA-256 of what the decision is made over that is the
// run's own and no version's rule: the metric and window, whether the whole
// run's share reaches an answer, and each repository with its group and its
// value's counts, from which every larger scope's share is added up. The
// people are left out: they are counted by the version's rule
// (identity.Blocks), and a later version whose rule counts otherwise must
// read the recorded decision, not refuse it.
func (in *aiShareInputs) digest(w domain.Window) string {
	group := map[string]string{}
	for _, r := range in.repos {
		group[r.Key] = r.Group
	}
	var b strings.Builder
	fmt.Fprintf(&b, "stillhere egress inputs 1\nmetric %s\nwindow %s %s\nwhole %t\n", aiShareMetric,
		w.Since.UTC().Format("2006-01-02T15:04:05.000000000Z"), w.Until.UTC().Format("2006-01-02T15:04:05.000000000Z"), in.whole)
	for _, k := range slices.Sorted(maps.Keys(in.values)) {
		v := in.values[k]
		fmt.Fprintf(&b, "repo %s %s %s %s %s %s\n", strconv.Quote(k), strconv.Quote(group[k]),
			strconv.FormatFloat(v.Num, 'g', -1, 64), strconv.FormatFloat(v.Den, 'g', -1, 64),
			v.Window.Since.UTC().Format("2006-01-02T15:04:05.000000000Z"), v.Window.Until.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// egressPerson is the key the record holds for one block of addresses: the
// first 128 bits of HMAC-SHA-256 under the record's salt, so the record
// does not reverse to addresses, and two homes' records do not join on a
// person. The empty address stays "", which aiShareCount reads.
func egressPerson(salt []byte, key string) string {
	if key == "" {
		return ""
	}
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(key))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// against decides the run's shares against the decisions sent for its
// window (point 2 above) and returns the decision to record.
func (in *aiShareInputs) against(salt []byte, sent []domain.EgressDecision, inputs, version string) domain.EgressDecision {
	people := make(map[string]map[string]bool, len(in.people))
	for repo, ps := range in.people {
		hashed := make(map[string]bool, len(ps))
		for p := range ps {
			hashed[egressPerson(salt, p)] = true
		}
		people[repo] = hashed
	}
	run := &aiShareInputs{repos: in.repos, people: people, values: in.values, whole: in.whole}
	forced := map[domain.Scope]bool{}
	for {
		shares := run.shares(run.decide(forced))
		add := egressConflicts(shares, people, in.values, sent)
		if len(add) == 0 {
			return domain.EgressDecision{Inputs: inputs, Metric: aiShareMetric, Shares: shares,
				Repos: egressRepos(shares, people, in.values), Version: version}
		}
		for _, sc := range add {
			forced[sc] = true
		}
	}
}

// shares is what a decision lets leave: each shown scope with the
// repositories its share adds up and its counts, by kind and key.
func (in *aiShareInputs) shares(a AIShares) []domain.EgressShare {
	var out []domain.EgressShare
	for _, sc := range a.Shown {
		var repos []string
		for _, r := range in.repos {
			if _, ok := in.values[r.Key]; ok && (sc.Kind == domain.ScopeAll ||
				sc.Kind == domain.ScopeGroup && r.Group == sc.Key || sc.Kind == domain.ScopeRepo && r.Key == sc.Key) {
				repos = append(repos, r.Key)
			}
		}
		if len(repos) == 0 {
			continue
		}
		slices.Sort(repos)
		sh := domain.EgressShare{Scope: sc, Repos: slices.Compact(repos)}
		for _, k := range sh.Repos {
			sh.Num += in.values[k].Num
			sh.Den += in.values[k].Den
		}
		out = append(out, sh)
	}
	return out
}

// egressRepos is every repository a share adds up, as the record keeps it:
// by key, with its counts and its people, sorted, nil for a repository of
// no person's landing.
func egressRepos(shares []domain.EgressShare, people map[string]map[string]bool, values map[string]domain.MetricValue) []domain.EgressRepo {
	keys := map[string]bool{}
	for _, sh := range shares {
		for _, k := range sh.Repos {
			keys[k] = true
		}
	}
	var out []domain.EgressRepo
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		r := domain.EgressRepo{Key: k, Num: values[k].Num, Den: values[k].Den}
		if len(people[k]) > 0 {
			r.People = slices.Sorted(maps.Keys(people[k]))
		}
		out = append(out, r)
	}
	return out
}

// egressNode is one set of repositories in the table of shares the record
// and the run make together: a recorded share's, the run's, or both, when
// the run would show a set again.
type egressNode struct {
	repos    []string // Repo.Key, sorted
	in       map[string]bool
	num, den float64
	recorded bool
	scopes   []domain.Scope             // the run's scopes with this set: what withholding it withholds
	people   map[string]map[string]bool // by repository: the people of the run the node is read from
	children []*egressNode              // the largest sets below it
}

func newEgressNode(repos []string, num, den float64, people map[string]map[string]bool) *egressNode {
	n := &egressNode{repos: repos, in: make(map[string]bool, len(repos)), num: num, den: den, people: people}
	for _, k := range repos {
		n.in[k] = true
	}
	return n
}

// count is the fewest people the repositories can be made of, by the
// node's run's people (aiShareCount).
func (n *egressNode) count(repos []string) int {
	u := map[string]bool{}
	for _, k := range repos {
		maps.Copy(u, n.people[k])
	}
	return aiShareCount(u)
}

// uncovered is the node's residual: its repositories no set below it holds.
func (n *egressNode) uncovered() []string {
	covered := map[string]bool{}
	for _, c := range n.children {
		maps.Copy(covered, c.in)
	}
	var out []string
	for _, k := range n.repos {
		if !covered[k] {
			out = append(out, k)
		}
	}
	return out
}

// within lists every node at and below n.
func (n *egressNode) within() []*egressNode {
	out := []*egressNode{n}
	for _, c := range n.children {
		out = append(out, c.within()...)
	}
	return out
}

// egressConflicts returns the run's shares to withhold before the check of
// point 2 can hold, none when it holds: first every share whose set
// overlaps a recorded one without nesting, or that holds a repository a
// recorded decision holds with other counts (values are the run's); else,
// at the first residual of one or two people, bottom-up, the run's own
// shares directly below that set, smallest first, while each adds someone
// to it, and the set itself when it is the run's alone and that was not
// enough. A residual those cannot mend withholds every share of the run
// within the set, and one no share of the run is within, which only a
// damaged record holds, every share of the run (fail closed).
func egressConflicts(shares []domain.EgressShare, people map[string]map[string]bool, values map[string]domain.MetricValue,
	sent []domain.EgressDecision) []domain.Scope {
	// The recorded sets, each once, read by the first decision that sent it:
	// every later one was checked against that one.
	var nodes []*egressNode
	bySet := map[string]*egressNode{}
	counted := map[string][][2]float64{} // the counts each recorded decision holds a repository with
	for _, d := range sent {
		dp := map[string]map[string]bool{}
		for _, r := range d.Repos {
			set := make(map[string]bool, len(r.People))
			for _, p := range r.People {
				set[p] = true
			}
			dp[r.Key] = set
			counted[r.Key] = append(counted[r.Key], [2]float64{r.Num, r.Den})
		}
		for _, sh := range d.Shares {
			k := strings.Join(sh.Repos, "\x00")
			if len(sh.Repos) == 0 || bySet[k] != nil {
				continue
			}
			n := newEgressNode(sh.Repos, sh.Num, sh.Den, dp)
			n.recorded = true
			bySet[k] = n
			nodes = append(nodes, n)
		}
	}
	var bad []domain.Scope
	for _, sh := range shares {
		for _, k := range sh.Repos {
			for _, c := range counted[k] {
				if c != [2]float64{values[k].Num, values[k].Den} {
					bad = append(bad, sh.Scope) // the repository changed between the runs: the difference is the change's
				}
			}
		}
		for _, r := range nodes {
			common := 0
			for _, k := range sh.Repos {
				if r.in[k] {
					common++
				}
			}
			if common > 0 && common < len(sh.Repos) && common < len(r.repos) {
				bad = append(bad, sh.Scope) // overlapping sets that do not nest
			}
		}
	}
	if len(bad) > 0 {
		slices.SortFunc(bad, aiShareCompare)
		return slices.Compact(bad)
	}
	for _, sh := range shares {
		k := strings.Join(sh.Repos, "\x00")
		n := bySet[k]
		if n == nil {
			n = newEgressNode(sh.Repos, sh.Num, sh.Den, people)
			bySet[k] = n
			nodes = append(nodes, n)
		}
		n.scopes = append(n.scopes, sh.Scope)
	}
	// The table is laminar now, so each set hangs from the smallest set that
	// holds it, and the residuals are read bottom-up, as aishare.go reads
	// one run's.
	slices.SortStableFunc(nodes, func(a, b *egressNode) int {
		return cmp.Or(cmp.Compare(len(a.repos), len(b.repos)), cmp.Compare(strings.Join(a.repos, "\x00"), strings.Join(b.repos, "\x00")))
	})
	for i, n := range nodes {
		for _, p := range nodes[i+1:] {
			if len(p.repos) > len(n.repos) && p.in[n.repos[0]] {
				p.children = append(p.children, n)
				break
			}
		}
	}
	for _, p := range nodes {
		u := p.uncovered()
		c := p.count(u)
		if c == 0 || c >= AIShareMinPeople {
			continue
		}
		var kids []*egressNode
		for _, ch := range p.children {
			if !ch.recorded {
				kids = append(kids, ch)
			}
		}
		slices.SortFunc(kids, func(a, b *egressNode) int {
			return cmp.Or(cmp.Compare(p.count(a.repos), p.count(b.repos)), aiShareCompare(a.scopes[0], b.scopes[0]))
		})
		var add []domain.Scope
		for _, ch := range kids {
			if c >= AIShareMinPeople {
				break
			}
			with := append(slices.Clone(u), ch.uncovered()...)
			if p.count(with) == c {
				continue // withholding it would add no one
			}
			u, c = with, p.count(with)
			add = append(add, ch.scopes...)
		}
		if c > 0 && c < AIShareMinPeople && !p.recorded {
			add = append(add, p.scopes...)
		}
		if len(add) == 0 {
			for _, n := range p.within() {
				add = append(add, n.scopes...)
			}
		}
		if len(add) == 0 {
			for _, sh := range shares {
				add = append(add, sh.Scope)
			}
		}
		slices.SortFunc(add, aiShareCompare)
		return slices.Compact(add)
	}
	return nil
}

// EgressRequest asks for the egress record, as stillhere egress prints it.
type EgressRequest struct {
	Record string // the record's path, which the answer names
}

// Egress returns every decision the egress record holds, oldest first, as
// report.Egress: what the MCP server let leave the machine, for the owner to
// audit (ADR 0015). It reads d.Egress only.
func Egress(ctx context.Context, d Deps, r EgressRequest) (*report.Egress, error) {
	if d.Egress == nil {
		return nil, errors.New("app.Egress: no egress record")
	}
	ds, err := d.Egress.Decisions(ctx)
	if err != nil {
		return nil, err
	}
	out := &report.Egress{Record: r.Record, Decisions: []report.EgressDecision{}}
	for _, dec := range ds {
		people := map[string]map[string]bool{}
		for _, rp := range dec.Repos {
			set := map[string]bool{}
			for _, p := range rp.People {
				set[p] = true
			}
			people[rp.Key] = set
		}
		e := report.EgressDecision{Run: dec.Run.ID, RunStarted: dec.Run.Started, AsOf: dec.Run.Window.Until,
			Window: report.Interval{Since: dec.Run.Window.Since, Until: dec.Run.Window.Until}, Metric: dec.Metric.String(),
			Inputs: dec.Inputs, Version: dec.Version, Decided: dec.Decided, Shares: []report.EgressShare{}}
		for _, sh := range dec.Shares {
			n := newEgressNode(sh.Repos, sh.Num, sh.Den, people)
			es := report.EgressShare{Scope: report.Scope{Kind: sh.Scope.Kind, Key: sh.Scope.Key}, Repos: slices.Clone(sh.Repos),
				Num: sh.Num, Den: sh.Den, People: n.count(sh.Repos)}
			if es.Repos == nil {
				es.Repos = []string{}
			}
			if sh.Den > 0 {
				es.Value = sh.Num / sh.Den
			}
			e.Shares = append(e.Shares, es)
		}
		out.Decisions = append(out.Decisions, e)
	}
	return out, nil
}
