package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/identity"
)

// This file is what the run's identity candidates could change in the two
// metrics that count people holding lines (#104): knowledge_loss@1 and
// knowledge_concentration@1. Both count each canonical email as a person,
// so one person whose addresses no .mailmap merges is several people, and
// an older address of someone still active holds lines that look gone. A
// caveat that says only "identities may be unmerged" leaves the reader
// with a point and no idea how far it could move, so each value says, in
// its Detail, how many candidates hold lines in its area, and what it would
// be with them merged: their high-confidence lines (identity.Candidate.High:
// a high candidate's every address, and in another the addresses high
// evidence links), which `stillhere identities --format mailmap` writes as
// lines, and every candidate whole. That is a sensitivity analysis
// over the one uncertain input (Saltelli et al., Global Sensitivity
// Analysis: The Primer, Wiley 2008, ch. 1): an agent can state a range,
// not a point.
//
// The values with merges are computed by the metrics' own functions over
// the same files, with each address replaced by its candidate's canonical
// address, so the definition is applied once, never restated. Merging only
// joins people: it moves lines between no two areas, and a candidate holds
// no bot (identity.Candidates leaves bots out), so knowledge_loss@1's
// denominator, the lines held by people, is the same with merges, and only
// its numerator moves. Merging a quiet address into an active one makes its
// lines active, and merging two quiet ones leaves them gone, so merging
// makes no active line quiet, and if_all_merged_num <= if_merged_num <= Num.

// The Detail keys this file adds.
const (
	detailCandidates     = "identity_candidates"      // candidates with an address among the people holding lines in the area
	detailCandidatesHigh = "identity_candidates_high" // of those, the ones with high-confidence lines (identity.Candidate.High)
	detailIfMerged       = "if_merged"                // knowledge_concentration@1: authors_in_prefix with the high-confidence lines merged
	detailIfAllMerged    = "if_all_merged"            // knowledge_concentration@1: with every candidate merged
	detailIfMergedNum    = "if_merged_num"            // knowledge_loss@1: Num with the high-confidence lines merged
	detailIfAllMergedNum = "if_all_merged_num"        // knowledge_loss@1: Num with every candidate merged
)

// identityMerges is the run's identity candidates as the two metrics read
// them: which candidate each address is in, and the canonical address each
// one merges into.
type identityMerges struct {
	of   map[string]int    // address → index of its candidate in Input.Candidates
	high []bool            // candidate index → it has high-confidence lines
	toH  map[string]string // address high evidence links to its candidate's canonical one → that canonical address
	toA  map[string]string // address of any candidate → its canonical address
}

// newIdentityMerges indexes the candidates; nil when there are none.
func newIdentityMerges(cs []identity.Candidate) *identityMerges {
	if len(cs) == 0 {
		return nil
	}
	m := &identityMerges{of: map[string]int{}, high: make([]bool, len(cs)), toH: map[string]string{}, toA: map[string]string{}}
	for i := range cs {
		c := &cs[i]
		m.high[i] = len(c.High) >= 2
		for _, a := range c.Addresses {
			m.of[a.Email] = i
			m.toA[a.Email] = c.Canonical
		}
		for _, j := range c.High {
			m.toH[c.Addresses[j].Email] = c.Canonical
		}
	}
	return m
}

// holding counts the candidates with an address among the people holding
// lines in files, and those among them with high-confidence lines.
func (m *identityMerges) holding(v *view, files []*fileView) (all, high int) {
	seen := map[int]bool{}
	for _, f := range files {
		for email := range f.byPerson {
			if i, ok := m.of[email]; ok && !seen[i] && !v.who.Bot(email) {
				seen[i] = true
				all++
				if m.high[i] {
					high++
				}
			}
		}
	}
	return all, high
}

// merged returns files with each address replaced by its canonical address
// under to, and each file's majority owners found again by majorityOwners,
// the rule indexLines applies: what the metrics would read had a .mailmap
// merged them.
func merged(files []*fileView, to map[string]string) []*fileView {
	out := make([]*fileView, len(files))
	for i, f := range files {
		g := &fileView{path: f.path, total: f.total, byPerson: make(map[string]int, len(f.byPerson))}
		for email, n := range f.byPerson {
			if c, ok := to[email]; ok {
				email = c
			}
			g.byPerson[email] += n
		}
		g.owners = majorityOwners(g.byPerson, g.total)
		out[i] = g
	}
	return out
}

// mergedActive is active with each address replaced by its canonical
// address under to: a candidate is active when any of its addresses is.
func mergedActive(active map[string]bool, to map[string]string) map[string]bool {
	out := make(map[string]bool, len(active))
	for email := range active {
		if c, ok := to[email]; ok {
			email = c
		}
		out[email] = true
	}
	return out
}

// lossIfMerged adds to a knowledge_loss@1 value's detail, when candidates
// hold lines in files, how many, and its numerator with them merged.
func (v *view) lossIfMerged(detail map[string]float64, active map[string]bool, files []*fileView) {
	m := v.merges
	if m == nil {
		return
	}
	all, high := m.holding(v, files)
	if all == 0 {
		return
	}
	detail[detailCandidates], detail[detailCandidatesHigh] = float64(all), float64(high)
	for key, to := range map[string]map[string]string{detailIfMergedNum: m.toH, detailIfAllMergedNum: m.toA} {
		_, gone, _, _ := lossCounts(v, mergedActive(active, to), merged(files, to))
		detail[key] = float64(gone)
	}
}

// concentrationIfMerged adds to a knowledge_concentration@1 value's detail,
// when candidates hold lines in files, how many, and its value with them
// merged.
func (v *view) concentrationIfMerged(detail map[string]float64, files []*fileView) {
	m := v.merges
	if m == nil {
		return
	}
	all, high := m.holding(v, files)
	if all == 0 {
		return
	}
	detail[detailCandidates], detail[detailCandidatesHigh] = float64(all), float64(high)
	for key, to := range map[string]map[string]string{detailIfMerged: m.toH, detailIfAllMerged: m.toA} {
		authors, _, _ := concentrationWalk(v, merged(files, to))
		detail[key] = float64(authors)
	}
}
