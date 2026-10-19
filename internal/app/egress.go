package app

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
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
// rescan of team alone minutes later shows team at 1 of 12, and
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
//  2. Against what was sent: every decision recorded for a window that
//     overlaps the run's, whatever its as-of time. A share is a sum of its
//     repositories' counts, so a sum the record holds and one the run would
//     send are terms of one table wherever a repository carries the same
//     counts in both: a rescan seconds or days later, in which no landing
//     entered or left the window, subtracts exactly. So a repository is one
//     term of the table across runs when its counts are equal and it is the
//     same repository, by its key or by its history, the landings its
//     share counts, which a copy or a clone whose origin is spelt otherwise
//     shares under another key. One whose counts differ is another term:
//     the difference of its shares is the change of its landings between
//     the windows, which is what remains (ADR 0015). The run's shares may
//     leave only when the table keeps the property aishare.go proves for
//     one run: its sets of terms nest (a laminar family), and every
//     residual, a set less the sets below it, holds no one or at least
//     AIShareMinPeople people, counted by the people of each decision that
//     holds the set. Over the same landings, or in the same window, a
//     repository must carry the same counts in every run that sends a share
//     of it, or the difference of two shares would be its change between
//     the scans (a policy or a .mailmap changed). When the table does not
//     hold, the run's offending shares are withheld, the run's own rules
//     decide again around them (aiShareInputs.decide with forced), and the
//     check is made again, until it holds: each round withholds one more of
//     the run's shares, so it ends.
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
// from n's inputs against every decision recorded for a window that
// overlaps the run's, and recorded before it is returned. It returns every
// error of the record as it is, and ErrEgressInputs; either way the caller
// withholds every share. A run with no AI-assisted value has nothing to
// decide: the zero decision, recorded nowhere.
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
	checked := 0
	dec, made, err := d.Egress.Decide(ctx, run, func(sent []domain.EgressDecision) (domain.EgressDecision, error) {
		checked = len(sent)
		return in.against(window, sent, inputs, d.Version)
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
		d.Log.Info("recorded the AI-assisted shares that may leave", "run", n.Run.ID, "shares", len(shown),
			"checked_against", checked)
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

// egressInstant is how the digest writes an instant: UTC to the nanosecond.
const egressInstant = "2006-01-02T15:04:05.000000000Z"

// digest is the hex SHA-256 of what the decision is made over that is the
// run's own and no version's rule: the line "stillhere egress inputs 1";
// the metric (ai_assisted@1); the shares' window, since and until; whether
// the whole run's share reaches an answer (report.WholeRun); and for each
// repository with a value, by key, its key, its group, its value's num and
// den and its value's own window, from which every larger scope's share is
// added up. The people are left out: they are counted by the version's
// rule (identity.Blocks), and a later version whose rule counts otherwise
// must read the recorded decision, not refuse it. So are the repositories'
// names and histories, which the record keeps to match repositories, not
// numbers that leave.
func (in *aiShareInputs) digest(w domain.Window) string {
	group := map[string]string{}
	for _, r := range in.repos {
		group[r.Key] = r.Group
	}
	var b strings.Builder
	fmt.Fprintf(&b, "stillhere egress inputs 1\nmetric %s\nwindow %s %s\nwhole %t\n", aiShareMetric,
		w.Since.UTC().Format(egressInstant), w.Until.UTC().Format(egressInstant), in.whole)
	for _, k := range slices.Sorted(maps.Keys(in.values)) {
		v := in.values[k]
		fmt.Fprintf(&b, "repo %s %s %s %s %s %s\n", strconv.Quote(k), strconv.Quote(group[k]),
			strconv.FormatFloat(v.Num, 'g', -1, 64), strconv.FormatFloat(v.Den, 'g', -1, 64),
			v.Window.Since.UTC().Format(egressInstant), v.Window.Until.UTC().Format(egressInstant))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// egressHistory is the history the record matches a repository by: the hex
// SHA-256 of the line "stillhere egress history 1" and the SHAs of its
// landings in w, one per line, sorted. A copy of the repository, or a
// clone whose origin is spelt otherwise, has the same under another key.
// It is "" when no landing fell in w: every such repository would share it.
func egressHistory(landings []domain.Landing, w domain.Window) string {
	var shas []string
	for _, l := range landings {
		if w.Contains(l.Time) {
			shas = append(shas, l.SHA)
		}
	}
	if len(shas) == 0 {
		return ""
	}
	slices.Sort(shas)
	sum := sha256.Sum256([]byte("stillhere egress history 1\n" + strings.Join(slices.Compact(shas), "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

// egressPeople numbers the blocks of addresses of one decision ("1", "2",
// ...), the people the record keeps for it, in an order no one can repeat:
// by HMAC-SHA-256 under a random key made for the decision and never
// stored. Numbers are distinct within the decision, which is all its
// residuals need, since every residual is counted by the people of one
// decision; outside it they name no one, so no list of candidate addresses
// can be tested against the record, nor sorted to match the numbers, and
// two decisions' people do not join. The empty address stays "", which
// aiShareCount reads.
func egressPeople(people map[string]map[string]bool) (map[string]string, error) {
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	order := map[string]string{}
	for _, ps := range people {
		for p := range ps {
			if p != "" {
				m := hmac.New(sha256.New, key)
				m.Write([]byte(p))
				order[p] = string(m.Sum(nil))
			}
		}
	}
	blocks := slices.Collect(maps.Keys(order))
	slices.SortFunc(blocks, func(a, b string) int { return cmp.Compare(order[a], order[b]) })
	out := map[string]string{"": ""}
	for i, p := range blocks {
		out[p] = strconv.Itoa(i + 1)
	}
	return out, nil
}

// against decides the run's shares against the decisions sent for windows
// that overlap w (point 2 above) and returns the decision to record.
func (in *aiShareInputs) against(w domain.Window, sent []domain.EgressDecision, inputs, version string) (domain.EgressDecision, error) {
	numbered, err := egressPeople(in.people)
	if err != nil {
		return domain.EgressDecision{}, err
	}
	people := make(map[string]map[string]bool, len(in.people))
	for repo, ps := range in.people {
		set := make(map[string]bool, len(ps))
		for p := range ps {
			set[numbered[p]] = true
		}
		people[repo] = set
	}
	names := map[string]string{}
	for _, r := range in.repos {
		names[r.Key] = r.Name
	}
	repos := make(map[string]domain.EgressRepo, len(in.values))
	for k, v := range in.values {
		r := domain.EgressRepo{Key: k, Name: names[k], History: in.history[k], Num: v.Num, Den: v.Den}
		if len(people[k]) > 0 {
			r.People = slices.Sorted(maps.Keys(people[k]))
		}
		repos[k] = r
	}
	run := &aiShareInputs{repos: in.repos, people: people, values: in.values, whole: in.whole}
	forced := map[domain.Scope]bool{}
	for {
		shares := run.shares(run.decide(forced))
		add := egressConflicts(shares, repos, w, sent)
		if len(add) == 0 {
			d := domain.EgressDecision{Inputs: inputs, Metric: aiShareMetric, Shares: shares, Version: version}
			for _, k := range egressHeld(shares) {
				d.Repos = append(d.Repos, repos[k])
			}
			return d, nil
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

// egressHeld is every repository a share adds up, sorted: the ones the
// record keeps with the decision.
func egressHeld(shares []domain.EgressShare) []string {
	keys := map[string]bool{}
	for _, sh := range shares {
		for _, k := range sh.Repos {
			keys[k] = true
		}
	}
	return slices.Sorted(maps.Keys(keys))
}

// egressTerms numbers the terms of the table the record and the run make
// together: one per repository of the run, and one per repository of a
// recorded decision that is no earlier term. A recorded repository is an
// earlier term when it carries the same counts and the same key or the same
// history, so a rescan's repository and the one a group's share added up
// before are one term when nothing changed, and two when anything did. A
// repository of no landing (0 of 0) adds nothing to any sum and is no term.
type egressTerms struct {
	n                int
	byKey, byHistory map[string]int // "key or history\x00num\x00den" -> term
	// ambiguous terms are those one decision holds twice, or that two terms
	// could be: a run, or a decision, that holds a repository and a copy of
	// it. The table cannot say how often they enter a sum, so the run's
	// shares that hold them stay local.
	ambiguous map[int]bool
}

func newEgressTerms() *egressTerms {
	return &egressTerms{byKey: map[string]int{}, byHistory: map[string]int{}, ambiguous: map[int]bool{}}
}

func egressCounted(id string, num, den float64) string {
	return id + "\x00" + strconv.FormatFloat(num, 'g', -1, 64) + "\x00" + strconv.FormatFloat(den, 'g', -1, 64)
}

// add returns r's term, -1 for a repository of no landing; own says r is
// the run's own, which is always a term of its own.
func (t *egressTerms) add(r domain.EgressRepo, own bool) int {
	if r.Num == 0 && r.Den == 0 {
		return -1
	}
	byKey := egressCounted(r.Key, r.Num, r.Den)
	byHistory := ""
	if r.History != "" {
		byHistory = egressCounted(r.History, r.Num, r.Den)
	}
	var found []int
	if id, ok := t.byKey[byKey]; ok {
		found = append(found, id)
	}
	if id, ok := t.byHistory[byHistory]; ok && byHistory != "" && !slices.Contains(found, id) {
		found = append(found, id)
	}
	id := t.n
	switch {
	case own || len(found) == 0:
		t.n++
	default:
		id = slices.Min(found)
	}
	if (own && len(found) > 0) || len(found) > 1 {
		t.ambiguous[id] = true
		for _, f := range found {
			t.ambiguous[f] = true
		}
	}
	if _, ok := t.byKey[byKey]; !ok {
		t.byKey[byKey] = id
	}
	if _, ok := t.byHistory[byHistory]; !ok && byHistory != "" {
		t.byHistory[byHistory] = id
	}
	return id
}

// egressNode is one set of terms in the table: a recorded share's, the
// run's, or both, when the run would show a set again.
type egressNode struct {
	terms    []int // sorted
	in       map[int]bool
	recorded bool
	scopes   []domain.Scope // the run's scopes with this set: what withholding it withholds
	// people holds, for each decision that holds the set (the run's
	// included), the people of each of its terms in that decision. A
	// residual counts as few as the fewest any of them makes it.
	people []map[int]map[string]bool
}

func (n *egressNode) sig() string {
	var b strings.Builder
	for _, t := range n.terms {
		b.WriteString(strconv.Itoa(t))
		b.WriteByte(',')
	}
	return b.String()
}

// count is the fewest people the terms can be made of, by the people of
// each decision that holds the node (aiShareCount).
func (n *egressNode) count(terms []int) int {
	fewest := -1
	for _, pm := range n.people {
		u := map[string]bool{}
		for _, t := range terms {
			maps.Copy(u, pm[t])
		}
		if c := aiShareCount(u); fewest < 0 || c < fewest {
			fewest = c
		}
	}
	return max(fewest, 0)
}

// within reports whether every term of n is in p and p holds more.
func (n *egressNode) within(p *egressNode) bool {
	if len(n.terms) >= len(p.terms) {
		return false
	}
	for _, t := range n.terms {
		if !p.in[t] {
			return false
		}
	}
	return true
}

// common is how many terms n and p share.
func (n *egressNode) common(p *egressNode) int {
	c := 0
	for _, t := range n.terms {
		if p.in[t] {
			c++
		}
	}
	return c
}

// tangled reports whether n and p overlap without either holding the
// other.
func (n *egressNode) tangled(p *egressNode) bool {
	c := n.common(p)
	return c > 0 && c < len(n.terms) && c < len(p.terms)
}

// egressConflicts returns the run's shares to withhold before the check of
// point 2 can hold, none when it holds. repos are the run's repositories
// with a value, w its window, and sent the decisions recorded for windows
// that overlap it.
//
// First, every share of the run that holds a repository whose counts a
// recorded decision holds otherwise over the same landings or in the same
// window, or that holds an ambiguous term; every share whose set overlaps
// a recorded set without nesting; and every share that overlaps a recorded
// set which itself overlaps another recorded set without nesting, which
// the residuals below cannot read. Otherwise, at the first residual of one
// or two people, smallest sets first, the run's own shares inside that set,
// smallest first, while each adds someone to it, and the set itself when it
// is the run's alone and that was not enough; a residual those cannot mend
// withholds every share of the run within the set. A residual of recorded
// sets alone, which no share of the run is within, is left: the run neither
// made it nor can mend it.
func egressConflicts(shares []domain.EgressShare, repos map[string]domain.EgressRepo, w domain.Window,
	sent []domain.EgressDecision) []domain.Scope {
	terms := newEgressTerms()
	own := map[string]int{}
	for _, k := range slices.Sorted(maps.Keys(repos)) {
		own[k] = terms.add(repos[k], true)
	}
	runPeople := map[int]map[string]bool{}
	for k, id := range own {
		runPeople[id] = egressSet(repos[k].People)
	}
	// The recorded sets, each once, with the people of every decision that
	// holds it.
	var nodes []*egressNode
	bySig := map[string]*egressNode{}
	node := func(ts []int, people map[int]map[string]bool) *egressNode {
		slices.Sort(ts)
		ts = slices.Compact(ts)
		n := &egressNode{terms: ts, in: map[int]bool{}}
		for _, t := range ts {
			n.in[t] = true
		}
		if found := bySig[n.sig()]; found != nil {
			found.people = append(found.people, people)
			return found
		}
		n.people = []map[int]map[string]bool{people}
		bySig[n.sig()] = n
		nodes = append(nodes, n)
		return n
	}
	for _, d := range sent {
		of := map[string]int{}
		people := map[int]map[string]bool{}
		held := map[int]bool{}
		for _, r := range d.Repos {
			id := terms.add(r, false)
			of[r.Key] = id
			if id < 0 {
				continue
			}
			if held[id] {
				terms.ambiguous[id] = true // the decision holds one term twice
			}
			held[id] = true
			if people[id] == nil {
				people[id] = map[string]bool{}
			}
			maps.Copy(people[id], egressSet(r.People))
		}
		for _, sh := range d.Shares {
			var ts []int
			for _, k := range sh.Repos {
				if id, ok := of[k]; ok && id >= 0 {
					ts = append(ts, id)
				}
			}
			if len(ts) > 0 {
				node(ts, people).recorded = true
			}
		}
	}
	recorded := slices.Clone(nodes)
	// tangled holds, for a recorded set the run touches, whether it overlaps
	// another recorded set without nesting.
	tangled := map[*egressNode]bool{}
	tangledWith := func(r *egressNode) bool {
		if t, ok := tangled[r]; ok {
			return t
		}
		tangled[r] = slices.ContainsFunc(recorded, r.tangled)
		return tangled[r]
	}
	// changed holds the run's repositories that a recorded decision holds
	// with other counts over the same landings, or in the same window: the
	// difference of two shares of one would be its change between the scans.
	changed := map[string]bool{}
	byHistory := map[string][]string{}
	for k, r := range repos {
		if r.History != "" {
			byHistory[r.History] = append(byHistory[r.History], k)
		}
	}
	for _, d := range sent {
		same := d.Run.Window.Since.Equal(w.Since) && d.Run.Window.Until.Equal(w.Until)
		for _, q := range d.Repos {
			var keys []string
			if r, ok := repos[q.Key]; ok && same && (q.Num != r.Num || q.Den != r.Den) {
				keys = append(keys, q.Key)
			}
			if q.History != "" {
				for _, k := range byHistory[q.History] {
					if r := repos[k]; q.Num != r.Num || q.Den != r.Den {
						keys = append(keys, k)
					}
				}
			}
			for _, k := range keys {
				changed[k] = true
			}
		}
	}
	runNodes := make([]*egressNode, len(shares))
	var bad []domain.Scope
	for i, sh := range shares {
		var ts []int
		for _, k := range sh.Repos {
			if changed[k] {
				bad = append(bad, sh.Scope) // the same landings, or the same window, counted otherwise: the difference is the change's
			}
			if id := own[k]; id >= 0 {
				ts = append(ts, id)
				if terms.ambiguous[id] {
					bad = append(bad, sh.Scope) // a repository the table holds a copy of: how often it enters a sum is unknown
				}
			}
		}
		if len(ts) == 0 {
			continue // a share of no landing adds nothing to the table
		}
		n := &egressNode{terms: slices.Compact(slices.Sorted(slices.Values(ts))), in: map[int]bool{}}
		for _, t := range n.terms {
			n.in[t] = true
		}
		runNodes[i] = n
		for _, r := range recorded {
			switch {
			case n.tangled(r):
				bad = append(bad, sh.Scope) // overlapping sets that do not nest
			case n.common(r) > 0 && tangledWith(r):
				bad = append(bad, sh.Scope) // beside recorded sets that overlap without nesting, a residual means nothing
			}
		}
	}
	if len(bad) > 0 {
		slices.SortFunc(bad, aiShareCompare)
		return slices.Compact(bad)
	}
	for i, sh := range shares {
		if runNodes[i] == nil {
			continue
		}
		n := node(runNodes[i].terms, runPeople)
		n.scopes = append(n.scopes, sh.Scope)
	}
	// The run's sets nest with every recorded set they touch now, so a
	// set's residual is its terms that no other set inside it holds, and
	// the residuals are read smallest set first, as aishare.go reads one
	// run's bottom-up. Only a set that is the run's or holds one of the
	// run's can have a residual the run makes.
	slices.SortStableFunc(nodes, func(a, b *egressNode) int {
		return cmp.Or(cmp.Compare(len(a.terms), len(b.terms)), cmp.Compare(a.sig(), b.sig()))
	})
	for _, p := range nodes {
		if len(p.scopes) == 0 && !slices.ContainsFunc(runNodes, func(n *egressNode) bool { return n != nil && n.within(p) }) {
			continue
		}
		var inside []*egressNode
		for _, n := range nodes {
			if n.within(p) {
				inside = append(inside, n)
			}
		}
		residual := func(without map[*egressNode]bool) []int {
			covered := map[int]bool{}
			for _, n := range inside {
				if !without[n] {
					maps.Copy(covered, n.in)
				}
			}
			var out []int
			for _, t := range p.terms {
				if !covered[t] {
					out = append(out, t)
				}
			}
			return out
		}
		c := p.count(residual(nil))
		if c == 0 || c >= AIShareMinPeople {
			continue
		}
		var kids []*egressNode
		for _, n := range inside {
			if len(n.scopes) > 0 && !n.recorded {
				kids = append(kids, n)
			}
		}
		slices.SortFunc(kids, func(a, b *egressNode) int {
			return cmp.Or(cmp.Compare(p.count(a.terms), p.count(b.terms)), aiShareCompare(a.scopes[0], b.scopes[0]))
		})
		without := map[*egressNode]bool{}
		var add []domain.Scope
		for _, ch := range kids {
			if c >= AIShareMinPeople {
				break
			}
			without[ch] = true
			if with := p.count(residual(without)); with != c {
				c = with
				add = append(add, ch.scopes...)
				continue
			}
			delete(without, ch) // withholding it would add no one
		}
		if c > 0 && c < AIShareMinPeople && !p.recorded {
			add = append(add, p.scopes...)
		}
		if c > 0 && c < AIShareMinPeople && len(add) == 0 {
			add = append(add, p.scopes...)
			for _, n := range inside {
				add = append(add, n.scopes...)
			}
		}
		if len(add) == 0 {
			continue
		}
		slices.SortFunc(add, aiShareCompare)
		return slices.Compact(add)
	}
	return nil
}

// egressSet is a list of people as a set.
func egressSet(people []string) map[string]bool {
	set := make(map[string]bool, len(people))
	for _, p := range people {
		set[p] = true
	}
	return set
}

// egressOverlap reports whether two windows overlap: what Decide hands a
// new decision.
func egressOverlap(a, b domain.Window) bool {
	return a.Until.After(b.Since) && a.Since.Before(b.Until)
}

// EgressRequest asks for the egress record, as stillhere egress prints it.
type EgressRequest struct {
	Record string // the record's path, which the answer names
}

// Egress returns every decision the egress record holds, oldest first, as
// report.Egress: the AI-assisted shares the MCP server let leave the
// machine, or may still let leave, of each run it answered about with a
// share, for the owner to audit (ADR 0015). Each decision names the earlier
// ones it was checked against: every one recorded before it for a window
// that overlaps its own. It reads d.Egress only.
func Egress(ctx context.Context, d Deps, r EgressRequest) (*report.Egress, error) {
	if d.Egress == nil {
		return nil, errors.New("app.Egress: no egress record")
	}
	ds, err := d.Egress.Decisions(ctx)
	if err != nil {
		return nil, err
	}
	out := &report.Egress{Record: r.Record, Decisions: []report.EgressDecision{}}
	for i, dec := range ds {
		people := map[string]map[string]bool{}
		names := map[string]string{}
		for _, rp := range dec.Repos {
			people[rp.Key] = egressSet(rp.People)
			names[rp.Key] = rp.Name
		}
		e := report.EgressDecision{Run: dec.Run.ID, RunStarted: dec.Run.Started, AsOf: dec.Run.Window.Until,
			Window: report.Interval{Since: dec.Run.Window.Since, Until: dec.Run.Window.Until}, Metric: dec.Metric.String(),
			Inputs: dec.Inputs, Version: dec.Version, Decided: dec.Decided, CheckedAgainst: []report.EgressRun{},
			Shares: []report.EgressShare{}}
		for _, earlier := range ds[:i] {
			if egressOverlap(earlier.Run.Window, dec.Run.Window) {
				e.CheckedAgainst = append(e.CheckedAgainst, report.EgressRun{Run: earlier.Run.ID, RunStarted: earlier.Run.Started})
			}
		}
		for _, sh := range dec.Shares {
			u := map[string]bool{}
			for _, k := range sh.Repos {
				maps.Copy(u, people[k])
			}
			es := report.EgressShare{Scope: report.Scope{Kind: sh.Scope.Kind, Key: sh.Scope.Key}, Name: sh.Scope.Key,
				Repos: slices.Clone(sh.Repos), Num: sh.Num, Den: sh.Den, People: aiShareCount(u)}
			switch sh.Scope.Kind {
			case domain.ScopeRepo:
				es.Name = names[sh.Scope.Key]
			case domain.ScopeAll:
				es.Name = ""
			}
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
