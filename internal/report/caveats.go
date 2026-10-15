package report

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Caveats turn the run's warnings and facts into what a reader needs to
// know before trusting a section: "what could make this wrong". A
// repository's caveats are specific (how many files blame could not read);
// a group's and the run's summarise their repositories' by code and name
// the repositories.

// The caveat codes. Codes that a collector's warning already has keep it.
const (
	codeNotFetched    = "not-fetched"
	codeFailed        = "failed"
	codeShallow       = "shallow-clone"
	codePartial       = "partial-clone"
	codeShallowWindow = "shallow-window"
	codeBlameErrors   = "blame-errors"
	codeNumstat       = "numstat-rename"
	codeNoIgnoreRevs  = "no-ignore-revs"
	codeExcluded      = "excluded-repo"
	codeDuplicate     = "duplicate-repo" // a repository's warning: another clone of its remote was skipped
	codePathFailed    = "path-failed"
	codeIdentities    = "unmerged-identities"
	codeNoValues      = "no-values"
	codeAILowerBound  = "ai-lower-bound"
	codeValidity      = "validity"
)

// largeRepoLines is the size, in surviving prod lines, from which a
// repository without a .git-blame-ignore-revs gets a caveat: large enough
// that a formatting pass is likely, and that its credit would move numbers.
const largeRepoLines = 10000

// caveatOrder lists the repository caveat codes from the most to the least
// damaging; codes not listed follow, sorted.
var caveatOrder = []string{
	codeFailed, codeShallow, codePartial, codeShallowWindow,
	codeBlameErrors, codeNumstat, codeNoIgnoreRevs,
}

// summaries say what a repository caveat means when it concerns several
// repositories at once.
var summaries = map[string]string{
	codeFailed:        "These repositories failed and add nothing to these numbers.",
	codeShallow:       "Shallow clones: blame did not run, so their surviving lines, ownership and durability are missing.",
	codePartial:       "Partial clones have no line facts (no numstat, no blame): only their landings and commits count, and where their blobs are absent, no path is skipped as too big or generated.",
	codeShallowWindow: "A shallow boundary falls inside the history window, so earlier landings are missing.",
	codeBlameErrors:   "Blame failed on some files, so their lines are missing.",
	codeNumstat:       "Some numstat rows could not be read and count 0 lines.",
	codeNoIgnoreRevs:  "Large repositories without a .git-blame-ignore-revs: a formatting commit would take the credit for every line it touched.",
	codeDuplicate:     "Clones of the same remote as these repositories were skipped as duplicates: each repository is counted once (see their warnings).",
}

func notFetched() Caveat {
	return Caveat{Code: codeNotFetched, Message: "Analysis did not fetch: each repository is only as current as its clone, so a stale clone gives a stale report."}
}

func aiLowerBound() Caveat {
	return Caveat{Code: codeAILowerBound, Message: "AI-assisted shares are a lower bound: editor completions leave no trace in git."}
}

// repoCaveats says what could make one repository's numbers wrong.
func repoCaveats(rf *domain.RepoFacts) []Caveat {
	name := rf.Repo.Name
	var out []Caveat
	add := func(code, msg string) {
		out = append(out, Caveat{Code: code, Message: msg, Repos: []string{name}})
	}
	if rf.Status != domain.RepoStatusOK {
		add(codeFailed, "The repository failed and adds nothing to these numbers: "+rf.Error)
		return out
	}
	if rf.Repo.Shallow {
		add(codeShallow, "Shallow clone: blame did not run, so its surviving lines, ownership and durability are missing.")
	}
	if rf.Repo.Partial {
		add(codePartial, "Partial clone: no line facts (no numstat, no blame), so only its landings and commits count; "+
			"where its blobs are absent, path sizes and attributes are unknown, so no path is skipped as too big or generated.")
	}
	numstat := 0
	for _, w := range rf.Warnings {
		switch w.Code {
		case codeShallow, codePartial:
			// Said above, from the repository's own flags.
		case codeShallowWindow:
			add(codeShallowWindow, "The shallow boundary is inside the history window, so earlier landings are missing: "+w.Message)
		case codeNumstat:
			numstat++
		default:
			add(w.Code, w.Message)
		}
	}
	if numstat > 0 {
		add(codeNumstat, plural(numstat, "numstat row", "numstat rows")+" could not be read and count 0 lines.")
	}
	if n := len(rf.BlameErrors); n > 0 {
		add(codeBlameErrors, "Blame failed on "+plural(n, "file", "files")+", so their lines are missing.")
	}
	if lines := blamedProdLines(rf, nil); rf.LineFacts && rf.IgnoreRevs.Source == "" && lines >= largeRepoLines {
		add(codeNoIgnoreRevs, "No .git-blame-ignore-revs in a repository with "+grouped(lines)+
			" surviving prod lines: a formatting commit would take the credit for every line it touched.")
	}
	return out
}

// stripRepos drops the repository names from a card's caveats: on a card
// every caveat concerns that repository.
func stripRepos(cs []Caveat) []Caveat {
	for i := range cs {
		cs[i].Repos = nil
	}
	return cs
}

// summarize merges repository caveats by code: one caveat per code, naming
// every repository it concerns, in caveatOrder.
func summarize(cs []Caveat) []Caveat {
	byCode := map[string][]Caveat{}
	for _, c := range cs {
		byCode[c.Code] = append(byCode[c.Code], c)
	}
	var codes []string
	for code := range byCode {
		codes = append(codes, code)
	}
	rank := func(code string) int {
		if i := slices.Index(caveatOrder, code); i >= 0 {
			return i
		}
		return len(caveatOrder)
	}
	slices.SortFunc(codes, func(x, y string) int { return cmp.Or(cmp.Compare(rank(x), rank(y)), cmp.Compare(x, y)) })
	var out []Caveat
	for _, code := range codes {
		group := byCode[code]
		var repos []string
		for _, c := range group {
			repos = append(repos, c.Repos...)
		}
		slices.Sort(repos)
		repos = slices.Compact(repos)
		msg := group[0].Message
		if len(repos) > 1 || len(group) > 1 {
			if s, ok := summaries[code]; ok {
				msg = s
			}
		}
		out = append(out, Caveat{Code: code, Message: msg, Repos: repos})
	}
	return out
}

// scopeCaveats says what could make a group's or a rank table's numbers
// wrong.
func (b *builder) scopeCaveats(members []*domain.RepoFacts, scope domain.Scope, aiShown bool) []Caveat {
	out := []Caveat{notFetched()}
	var all []Caveat
	for _, rf := range members {
		all = append(all, repoCaveats(rf)...)
	}
	out = append(out, summarize(all)...)
	if c, ok := b.identityCaveat(scope, members); ok {
		out = append(out, c)
	}
	if aiShown {
		out = append(out, aiLowerBound())
	}
	return out
}

// runCaveats says what could make the whole report wrong. The run-level
// warnings are excluded repositories and paths that gave no repository;
// discovery's warnings about a repository, duplicate-repo among them, are
// that repository's and reach here through its caveats. GitHub data has no
// caveat here: the report's overviews and cards show no number computed
// from it, and each section that does (a rank table of reviews_given@1)
// carries its own, so a git-only report says nothing about GitHub.
func (b *builder) runCaveats(m *Model) []Caveat {
	out := []Caveat{notFetched()}
	byCode := map[string][]domain.Warning{}
	var codes []string
	for _, w := range b.in.Facts.Warnings {
		if byCode[w.Code] == nil {
			codes = append(codes, w.Code)
		}
		byCode[w.Code] = append(byCode[w.Code], w)
	}
	slices.Sort(codes)
	for _, code := range codes {
		ws := byCode[code]
		n := len(ws)
		var msg string
		switch code {
		case codeExcluded:
			msg = "1 repository was left out by --exclude-repo and is in none of these numbers (see warnings)."
			if n > 1 {
				msg = grouped(n) + " repositories were left out by --exclude-repo and are in none of these numbers (see warnings)."
			}
		case codePathFailed:
			// The warning says why, which can be a typo, a directory git
			// cannot read or a repository without a commit yet.
			msg = "1 path gave no repository and added nothing: " + ws[0].Message + "."
			if n > 1 {
				msg = fmt.Sprintf("%s paths gave no repository and added nothing, such as %s (see warnings).", grouped(n), ws[0].Message)
			}
		default:
			msg = ws[0].Message
			if n > 1 {
				msg = fmt.Sprintf("%s (and %d more like it; see warnings)", msg, n-1)
			}
		}
		out = append(out, Caveat{Code: code, Message: msg})
	}
	var all []Caveat
	for _, rf := range b.repos {
		all = append(all, repoCaveats(rf)...)
	}
	out = append(out, summarize(all)...)
	if c, ok := b.identityCaveat(domain.Scope{Kind: domain.ScopeAll}, b.repos); ok {
		out = append(out, c)
	}
	out = append(out, b.noValues()...)
	if aiShown(m) {
		out = append(out, aiLowerBound())
	}
	return out
}

func aiShown(m *Model) bool {
	for _, o := range m.Overviews {
		if o.AIAssisted != nil {
			return true
		}
	}
	for _, r := range m.Repos {
		if r.Card.AIAssisted != nil {
			return true
		}
	}
	return false
}

// identitiesHint says where the .mailmap lines an unmerged-identities
// caveat calls for come from: stillhere identities suggests them, with the
// evidence for each, and applies none.
const identitiesHint = "'stillhere identities' suggests the lines, with the evidence for each."

// identityCaveat names the display names that more than one email shares
// in a scope. git's mailmap is the only identity store, so stillhere never
// merges people by name; it says where a .mailmap line may be missing. A
// shared name that gives one of its addresses away is counted and not
// printed, as no page prints it (displayName).
func (b *builder) identityCaveat(scope domain.Scope, members []*domain.RepoFacts) (Caveat, bool) {
	inScope := map[domain.Scope]bool{}
	for _, rf := range members {
		inScope[domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}] = true
	}
	inScope[scope] = true
	emails := map[string]bool{}
	for _, v := range b.in.Values {
		if v.Subject.Kind == domain.SubjectPerson && (scope.Kind == domain.ScopeAll || inScope[v.Scope]) {
			emails[v.Subject.Key] = true
		}
	}
	byName := map[string][]string{} // a display name → the emails that carry it
	for e := range emails {
		if n, ok := realName(b.in.Who, e); ok {
			byName[n] = append(byName[n], e)
		}
	}
	var shared []string
	withheld := 0
	for n, es := range byName {
		switch {
		case len(es) < 2:
		case slices.ContainsFunc(es, func(e string) bool { return nameWithheld(b.in.Who, e) }):
			withheld++
		default:
			shared = append(shared, strconv.Quote(n))
		}
	}
	total := len(shared) + withheld
	if total == 0 {
		return Caveat{}, false
	}
	slices.Sort(shared)
	named := shared
	if len(named) > 5 {
		named = append(slices.Clone(named[:5]), fmt.Sprintf("%d more", len(shared)-5))
	}
	if withheld > 0 {
		named = append(named, plural(withheld, "name withheld because it gives an address away",
			"names withheld because they give an address away"))
	}
	msg := "1 display name belongs to more than one email and was not merged: "
	if total > 1 {
		msg = grouped(total) + " display names belong to more than one email and were not merged: "
	}
	return Caveat{
		Code: codeIdentities,
		Message: msg + joinNames(named) + ". If the emails of a name are one person, a .mailmap line merges them: " +
			identitiesHint,
	}, true
}

// noValues says which of the metrics a report reads produced no value at
// all although the run has the facts it computes from: its sections are
// empty, which would otherwise look like a real zero.
func (b *builder) noValues() []Caveat {
	var landed, lines, history bool
	for _, rf := range b.repos {
		if rf.Status != domain.RepoStatusOK {
			continue
		}
		landed = landed || b.landingsOf(rf).Window > 0
		lines = lines || (rf.LineFacts && len(rf.Owners) > 0)
		history = history || len(rf.Landings) > 0
	}
	type wanted struct {
		metric string
		facts  bool // the run has the facts the metric computes from
	}
	want := []wanted{{MetricLanded, landed}}
	if b.in.Kind == KindScan {
		want = append(want,
			wanted{MetricSurviving, lines},
			wanted{MetricConcentration, lines},
			wanted{MetricKnowledgeLoss, lines},
			wanted{MetricAIAssisted, history},
		)
	}
	var out []Caveat
	for _, w := range want {
		if w.facts && !b.ix.has(w.metric) {
			out = append(out, Caveat{Code: codeNoValues, Message: w.metric + " produced no values, so the sections it fills are empty."})
		}
	}
	return out
}
