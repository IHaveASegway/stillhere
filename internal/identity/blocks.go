package identity

import "github.com/IHaveASegway/stillhere/internal/domain"

// Blocks returns, for every human address in fs, the address that stands
// for its block: every address it may share a person with. Two addresses
// are in one block when Candidates links them, when one hand-off chain
// holds them, however long (Candidates leaves out a chain longer than a
// candidate may hold), or when they share a key a rule compares, without
// the vetoes the rules apply: the words of any name seen with each, as
// author or co-author, a default one such as John Doe included
// (candWords); a local part without its +tag or a GitHub noreply address's
// login (candLocalPart), whatever its length or kind; a GitHub account id;
// or a GitHub account the run's GitHub data attributes commits of both to
// (forgeAccounts, the same-forge-account rule's links, #103), an address
// linked to two accounts joining both. Blocks join through any number of links, however many addresses that
// makes, and each is named by its smallest address, so the result does not
// depend on the order of the facts.
//
// It is Candidates turned the other way, for a count of people that must
// not err high. Candidates suggests merges, where a wrong link invents a
// person, so it leans to precision and leaves a person's addresses apart
// whenever the evidence is mixed: a name spelt two ways, a local part that
// many addresses share. Blocks counts people for a floor (ADR 0007's
// amendment: an AI-assisted share leaves the machine only when three
// people landed in it), where a missed link lets one person's own share
// out and a wrong one only lowers the count, so it leans to recall. Record
// linkage draws the same line between blocking, which must miss no true
// match, and classification, which must make no false one (Christen, Data
// Matching, Springer 2012, ch. 4). Blocks merges no one on any page.
func Blocks(fs *domain.FactSet, who *Directory) map[string]string {
	if who == nil {
		who = New(fs, nil)
	}
	survey, addrs, leftOut := candSurvey(fs, who)
	parent := make(map[string]string, len(addrs))
	find := func(a string) string {
		if _, ok := parent[a]; !ok {
			parent[a] = a
		}
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	union := func(a, b string) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[max(ra, rb)] = min(ra, rb)
		}
	}
	type key struct{ rule, value string }
	first := map[key]string{} // the first address with a key
	for _, a := range addrs {
		find(a.Email)
		local, account, _, _, _ := candLocalPart(a.Email)
		keys := []key{{"local", local}, {"account", account}}
		for _, name := range a.nameKeys {
			keys = append(keys, key{"name", name})
		}
		for _, login := range who.accounts[a.Email] {
			keys = append(keys, key{"forge", login})
		}
		if a.forge != "" { // a noreply address's own login, when the run links that login (candForge)
			keys = append(keys, key{"forge", a.forge})
		}
		for _, k := range keys {
			if k.value == "" {
				continue
			}
			if f, ok := first[k]; ok {
				union(f, a.Email)
			} else {
				first[k] = a.Email
			}
		}
	}
	for _, c := range survey.Candidates {
		for _, x := range c.Addresses[1:] {
			union(c.Addresses[0].Email, x.Email)
		}
	}
	for _, chain := range leftOut {
		for _, x := range chain[1:] {
			union(chain[0].Email, x.Email)
		}
	}
	out := make(map[string]string, len(parent))
	for a := range parent {
		out[a] = find(a)
	}
	return out
}
