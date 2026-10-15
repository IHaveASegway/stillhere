package report

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
)

// Identities is the answer of `stillhere identities` and of the MCP server's
// identity_candidates tool: groups of a stored run's addresses that may
// belong to one person, each with the evidence for it, for a person to
// review (docs/spec/identities.md). stillhere never applies one: git's
// mailmap is the only identity store (ADR 0004,
// docs/spec/git-invocation.md#identity-mailmap-and-bots).
//
// Emails appear only in fields named email, so a reader that must not show
// them, such as the MCP server without --allow-emails, can drop them, and
// in a candidate's mailmap lines, which only a reader that may show them
// asks for (IdentitiesInput.Mailmap). A
// display name that gives an address away (identity.Directory.Withheld: it
// holds an @, or it is a local part of a person's address in the run, as
// GitHub names a commit by a login) is withheld in every field, as on
// every other page: it is "", and an address's name_withheld says so. So
// no other field holds a person's address or a part of one. Text and
// Markdown print none either, as everywhere
// (docs/spec/report-json.md): an address is shown by its number and the kind
// of its domain. Only Mailmap, whose output is made of addresses, prints
// them.
type Identities struct {
	Provenance Provenance          `json:"provenance"`
	Scope      Scope               `json:"scope"`      // the repositories the candidates come from: all, a group, or one repository
	Addresses  int                 `json:"addresses"`  // human addresses the scope's facts show
	Bots       int                 `json:"bots"`       // bot and agent addresses, which never appear in a candidate
	Shared     int                 `json:"shared"`     // of addresses, those several people use (a role local part, or names that differ): no rule compares them
	Oversized  int                 `json:"oversized"`  // groups of more than 10 addresses that the rules chain together, left out as several people's
	Candidates []IdentityCandidate `json:"candidates"` // most confident first
	Rules      []IdentityRule      `json:"rules"`      // every rule's definition, in order
	Caveats    []Caveat            `json:"caveats"`
}

// IdentityCandidate is a group of addresses that may belong to one person.
type IdentityCandidate struct {
	Confidence string            `json:"confidence"` // high, medium or low
	Guess      bool              `json:"guess"`      // a hand-off: never suggested as certain
	Commented  bool              `json:"commented"`  // the mailmap form writes its lines commented out: every candidate that is not high
	Chained    bool              `json:"chained"`    // two of the addresses are linked only through a third
	Rules      []string          `json:"rules"`      // the rules that link the addresses, in the order of rules
	Name       string            `json:"name"`       // the display name suggested for the person; "" keeps each commit's own
	Addresses  []IdentityAddress `json:"addresses"`  // by first activity; exactly one is canonical
	Links      []IdentityLink    `json:"links"`      // each rule's evidence: which addresses it links
	// Mailmap holds the candidate's suggested .mailmap lines exactly as
	// the mailmap form writes them (MailmapLines), commented out unless
	// the candidate is high. They are made of addresses, so only an answer
	// that may show emails asks for them (IdentitiesInput.Mailmap: the MCP
	// server with --allow-emails); otherwise it is nil, null in JSON: an
	// absent section, never [], which would read as no lines.
	Mailmap *[]string `json:"mailmap"`
}

// IdentityAddress is one address of a candidate and the evidence for it,
// counted over the run's history and the older commits blame attributes
// lines to.
type IdentityAddress struct {
	Email        string    `json:"email"`         // the canonical email; JSON only
	Name         string    `json:"name"`          // its display name; "" when withheld or when the facts show none
	NameWithheld bool      `json:"name_withheld"` // the name gives an address away (identity.Directory.Withheld), so it is not shown
	DomainKind   string    `json:"domain_kind"`   // main, noreply, machine or other (identity.DomainKind): the domain without naming it
	Canonical    bool      `json:"canonical"`
	Commits      int       `json:"commits"`     // commits it authored
	CoAuthored   int       `json:"co_authored"` // commits whose Co-authored-by trailers name it
	First        time.Time `json:"first"`       // the earliest author time of those commits
	Last         time.Time `json:"last"`
	Repos        []string  `json:"repos"` // the repositories' keys, sorted
}

// IdentityLink is one rule's evidence in a candidate.
type IdentityLink struct {
	Rule      string           `json:"rule"`
	Addresses []int            `json:"addresses"` // indexes into the candidate's addresses
	Name      string           `json:"name"`      // same-name: the display name they share, "" when withheld; "" for the other rules
	FromName  bool             `json:"from_name"` // same-local-part: the local part is made from the name, so it adds nothing to it
	HandOff   *IdentityHandOff `json:"hand_off"`  // hand-off: from the first address to the second; null otherwise
}

// IdentityHandOff is the evidence for one hand-off: an address stopped, and
// another started in the same files soon after.
type IdentityHandOff struct {
	GapDays     float64        `json:"gap_days"`     // from the first address's last commit to the second's first
	SharedFiles int            `json:"shared_files"` // files both touched or hold lines in
	Files       []IdentityFile `json:"files"`        // the first five of them
}

// IdentityFile is a file two addresses share.
type IdentityFile struct {
	Repo    string `json:"repo"` // name
	RepoKey string `json:"repo_key"`
	Path    string `json:"path"`
}

// IdentityRule is one rule's definition: what it links, the confidence it
// gives, and how it can be wrong.
type IdentityRule struct {
	ID         string `json:"id"`
	Matches    string `json:"matches"`
	Confidence string `json:"confidence"`
	Risk       string `json:"risk"`
}

// IdentitiesInput is what NewIdentities builds the identities answer from.
type IdentitiesInput struct {
	Run    *domain.Run
	Facts  *domain.FactSet  // the scope's facts: provenance, caveats and repository names
	Scope  domain.Scope     // the zero Scope: every repository of the run
	Survey *identity.Survey // identity.Candidates over Facts
	// Who is the run's directory, which Survey was found with: it withholds
	// a name as every page does, over every person's address in the run
	// (identity.Directory.Withheld). nil: a name is checked against the
	// candidate's own addresses only (identity.RevealsAddress).
	Who   People
	Forge *ForgeRead // the GitHub data the run read, which dates its repositories; nil: the reader cannot tell
	// Mailmap adds each candidate's suggested .mailmap lines
	// (IdentityCandidate.Mailmap): only for a reader that may see emails,
	// since the lines are made of them.
	Mailmap bool
}

// identCodeMailmap is the caveat every identities answer carries: the
// addresses are the run's, which git mailmapped when it was scanned.
const identCodeMailmap = "mailmap-at-scan"

// identNoChain says how git applies a mailmap, which a person editing one
// must know: the caveat and the mailmap form's header both say it.
const identNoChain = "git applies one .mailmap line per commit and never follows a chain: if an alias in a " +
	"suggested line is already the canonical address of lines in a .mailmap, change those lines to the " +
	"suggested canonical address too, or their commits stay apart and the next scan suggests the same line again"

// NewIdentities builds the identities answer. Like every answer, it arranges
// what another package computed, identity.Candidates here, and computes
// nothing itself; it adds where the facts come from and what could make the
// candidates wrong, and it withholds every name that gives an address away.
func NewIdentities(in *IdentitiesInput) *Identities {
	scope := in.Scope
	if scope == (domain.Scope{}) {
		scope = domain.Scope{Kind: domain.ScopeAll}
	}
	id := &Identities{Scope: scopeOf(scope)}
	fs := in.Facts
	if fs == nil {
		fs = &domain.FactSet{}
	}
	names := map[string]string{}
	for i := range fs.Repos {
		names[fs.Repos[i].Repo.Key] = fs.Repos[i].Repo.Name
	}
	if s := in.Survey; s != nil {
		id.Addresses, id.Bots, id.Shared, id.Oversized = s.Addresses, s.Bots, s.Shared, s.Oversized
		for i := range s.Candidates {
			c := identCandidate(&s.Candidates[i], names, in.Who)
			if in.Mailmap {
				lines := MailmapLines(c)
				c.Mailmap = &lines
			}
			id.Candidates = append(id.Candidates, c)
		}
	}
	for _, def := range identity.Rules() {
		id.Rules = append(id.Rules, IdentityRule{ID: string(def.Rule), Matches: def.Matches, Confidence: def.Confidence, Risk: def.Risk})
	}
	// The answer covers every repository of the scope's facts and reads no
	// metric, so it shows no GitHub number and carries no forge caveat.
	id.Provenance = AnswerProvenance(in.Run, fs, in.Forge, nil)
	id.Caveats = append(AnswerCaveats(fs), Caveat{
		Code: identCodeMailmap,
		Message: "These are the run's addresses, which git mailmapped when the run was scanned: an address the " +
			"mailmap merged is in no candidate, and a .mailmap line added since takes effect at the next scan. " +
			identSentence(identNoChain) + ".",
	})
	return id
}

func identCandidate(c *identity.Candidate, names map[string]string, who People) IdentityCandidate {
	shown := identShown(c, who)
	name, _ := shown(c.Name)
	out := IdentityCandidate{
		Confidence: string(c.Confidence),
		Guess:      c.Guess,
		Commented:  c.Confidence != identity.ConfidenceHigh,
		Chained:    c.Chained,
		Name:       name,
	}
	for _, r := range c.Rules() {
		out.Rules = append(out.Rules, string(r))
	}
	for _, a := range c.Addresses {
		name, withheld := shown(a.Name)
		out.Addresses = append(out.Addresses, IdentityAddress{
			Email: a.Email, Name: name, NameWithheld: withheld, DomainKind: string(a.Domain),
			Canonical: a.Email == c.Canonical, Commits: a.Commits, CoAuthored: a.CoAuthored,
			First: a.First, Last: a.Last, Repos: slices.Clone(a.Repos),
		})
	}
	for _, l := range c.Links {
		name, _ := shown(l.Name)
		link := IdentityLink{Rule: string(l.Rule), Addresses: slices.Clone(l.Addresses), Name: name, FromName: l.FromName}
		if h := l.HandOff; h != nil {
			ho := &IdentityHandOff{GapDays: h.Gap.Hours() / 24, SharedFiles: h.SharedFiles}
			for _, f := range h.Files {
				ho.Files = append(ho.Files, IdentityFile{Repo: names[f.Repo], RepoKey: f.Repo, Path: f.Path})
			}
			link.HandOff = ho
		}
		out.Links = append(out.Links, link)
	}
	return out
}

// identShown returns how the answer shows a display name of the candidate:
// "" and true when the name gives an address away, as an address typed as
// a name or a login used as one does. Every name the answer prints is one
// of its addresses' names, and who, the run's directory, decides each over
// every person's address in the run, as every page does
// (identity.Directory.Withheld), so the answer withholds exactly what the
// tables withhold. Without a directory, or for a name no address carries,
// the name is checked against the candidate's own addresses
// (identity.RevealsAddress).
func identShown(c *identity.Candidate, who People) func(name string) (string, bool) {
	withheld := map[string]bool{}
	if who != nil {
		for _, a := range c.Addresses {
			if a.Name != "" {
				withheld[a.Name] = withheld[a.Name] || who.Withheld(a.Email)
			}
		}
	}
	return func(name string) (string, bool) {
		w, ok := withheld[name]
		if !ok {
			w = slices.ContainsFunc(c.Addresses, func(a identity.Address) bool { return identity.RevealsAddress(name, a.Email) })
		}
		if w {
			return "", true
		}
		return name, false
	}
}

// identIntro says what the page holds, and why it shows no address.
const identIntro = "addresses that may belong to one person, with the evidence, for a person to review: stillhere " +
	"applies none. This page names no address and no part of one, as no text or Markdown page does: an address " +
	"is shown by its number and the kind of its domain (the main domain, which most of the scope's addresses " +
	"use; GitHub noreply; a machine's; or another, lettered when a candidate has several), and a display name " +
	"that gives an address away is withheld. --format mailmap prints the .mailmap lines, and --format json the " +
	"addresses."

// identSummary is "11 addresses of people in 1 repository, 1 bot or agent
// left out · 2 candidates".
func identSummary(id *Identities) string {
	s := plural(id.Addresses, "address", "addresses") + " of people in " +
		plural(len(id.Provenance.Heads), "repository", "repositories")
	var aside []string
	if id.Bots > 0 {
		aside = append(aside, plural(id.Bots, "bot or agent", "bots and agents")+" left out")
	}
	if id.Shared > 0 {
		aside = append(aside, plural(id.Shared, "shared address", "shared addresses")+
			" (a role mailbox, or several people's names) compared with nothing")
	}
	if id.Oversized > 0 {
		// identity's limit on a candidate's addresses (candMaxShare).
		aside = append(aside, plural(id.Oversized, "group", "groups")+
			" of more than 10 linked addresses left out as several people's")
	}
	if len(aside) > 0 {
		s += ", " + strings.Join(aside, ", ")
	}
	return s + " · " + plural(len(id.Candidates), "candidate", "candidates")
}

// identNone says what an empty answer means.
const identNone = "no candidates: the rules link no two addresses by a shared display name, local part or " +
	"GitHub account, or by one address taking over from another that stopped"

// identTitle is "Ada Example · medium confidence · same-name,
// same-local-part"; a hand-off names each address's person in order.
func identTitle(c IdentityCandidate, esc func(string) string) string {
	title := identPerson(c.Name, esc)
	if c.Guess {
		var names []string
		for _, a := range c.Addresses {
			names = append(names, identAddressName(a, esc))
		}
		title = strings.Join(names, " → ")
	}
	title += " · " + c.Confidence + " confidence"
	if c.Guess {
		title += " · a guess"
	}
	return title + " · " + strings.Join(c.Rules, ", ")
}

// identPerson is a display name, or "(no name)"; a name that could be read
// as that label is printed quoted (nameText).
func identPerson(name string, esc func(string) string) string {
	if name == "" {
		return "(no name)"
	}
	return nameText(name, esc)
}

// identAddressName is an address's display name, or what stands for it.
func identAddressName(a IdentityAddress, esc func(string) string) string {
	if a.NameWithheld {
		return "(name withheld)"
	}
	return identPerson(a.Name, esc)
}

// identAddressText is "main domain · Ada Example · 2 commits · 2026-01-01 →
// 2026-01-05 · fixture · canonical", or, for Mailmap, the same with the
// email itself first.
func identAddressText(a IdentityAddress, label string, pv Provenance, esc func(string) string) string {
	parts := []string{label, identAddressName(a, esc), plural(a.Commits, "commit", "commits")}
	if a.CoAuthored > 0 {
		parts[2] += ", co-author of " + plural(a.CoAuthored, "commit", "commits")
	}
	if date(a.First) == date(a.Last) {
		parts = append(parts, date(a.First))
	} else {
		parts = append(parts, date(a.First)+" → "+date(a.Last))
	}
	var repos []string
	for _, key := range a.Repos {
		repos = append(repos, identRepoName(key, pv))
	}
	parts = append(parts, repoList(repos, esc))
	if a.Canonical {
		parts = append(parts, "canonical")
	}
	return strings.Join(parts, " · ")
}

// identLabels names a candidate's addresses without printing any part of
// one: by the kind of their domain (identity.DomainKind), with a letter
// when a candidate has two addresses of a kind that can differ (a machine,
// another domain), the same letter for the same domain. A letter says
// whether two addresses share a domain and matches nothing outside the
// page, where a hash of the address would confirm a guess of it.
func identLabels(c IdentityCandidate) []string {
	count := map[string]int{}
	for _, a := range c.Addresses {
		count[a.DomainKind]++
	}
	letters, used := map[string]string{}, map[string]int{}
	out := make([]string, len(c.Addresses))
	for i, a := range c.Addresses {
		label := identKind(a.DomainKind)
		lettered := a.DomainKind == string(identity.DomainMachine) || a.DomainKind == string(identity.DomainOther)
		if lettered && count[a.DomainKind] > 1 {
			key := a.DomainKind + "\x00" + identDomain(a.Email)
			letter, ok := letters[key]
			if !ok {
				letter = string(rune('A' + used[a.DomainKind]))
				used[a.DomainKind]++
				letters[key] = letter
			}
			label += " " + letter
		}
		out[i] = label
	}
	return out
}

// identKind is how a page names a kind of domain.
func identKind(kind string) string {
	switch identity.DomainKind(kind) {
	case identity.DomainMain:
		return "main domain"
	case identity.DomainNoreply:
		return "GitHub noreply"
	case identity.DomainMachine:
		return "machine"
	}
	return "other domain"
}

// identDomain is an email's domain, compared only to letter the labels.
func identDomain(email string) string {
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		return email[i+1:]
	}
	return ""
}

// identRepoName is a repository's name, from the heads the answer covers.
func identRepoName(key string, pv Provenance) string {
	for _, h := range pv.Heads {
		if h.Key == key {
			return h.Repo
		}
	}
	return key
}

// identWhy says, one clause per link, why the candidate's addresses may be
// one person. esc escapes prose, such as a name; path escapes a path.
func identWhy(c IdentityCandidate, esc, path func(string) string) []string {
	var out []string
	for _, l := range c.Links {
		which := identWhich(l.Addresses, len(c.Addresses))
		switch identity.Rule(l.Rule) {
		case identity.RuleSameName:
			if l.Name == "" {
				out = append(out, which+" carry one display name, withheld because it gives an address away")
			} else {
				out = append(out, which+" carry the display name \""+esc(l.Name)+"\"")
			}
		case identity.RuleSameLocalPart:
			if l.FromName {
				out = append(out, which+" have the same local part, made from the name, so it says no more than the name does")
			} else {
				out = append(out, which+" have the same local part")
			}
		case identity.RuleSameAccount:
			out = append(out, which+" are GitHub noreply addresses of one account")
		case identity.RuleHandOff:
			if h, ok := identHandOffText(l, esc, path); ok {
				out = append(out, h)
			}
		}
	}
	if c.Chained {
		out = append(out, "two of these addresses share nothing and are linked only through another, so the candidate is low")
	}
	return out
}

// identWhich is "both addresses", "all 3 addresses" or "addresses 1 and 3".
func identWhich(idx []int, n int) string {
	if len(idx) == n {
		if n == 2 {
			return "both addresses"
		}
		return fmt.Sprintf("all %d addresses", n)
	}
	var nums []string
	for _, i := range idx {
		nums = append(nums, strconv.Itoa(i+1))
	}
	return "addresses " + joinNames(nums)
}

// identHandOffText is "address 2's first commit came 1 day after address
// 1's last, in 1 shared file: split.py".
func identHandOffText(l IdentityLink, esc, path func(string) string) (string, bool) {
	h := l.HandOff
	if h == nil || len(l.Addresses) != 2 {
		return "", false
	}
	gap := time.Duration(h.GapDays * float64(24*time.Hour))
	s := fmt.Sprintf("address %d's first commit came %s after address %d's last, in %s",
		l.Addresses[1]+1, identGap(gap), l.Addresses[0]+1, plural(h.SharedFiles, "shared file", "shared files"))
	repos := map[string]bool{}
	for _, f := range h.Files {
		repos[f.RepoKey] = true
	}
	var files []string
	for _, f := range h.Files {
		name := path(f.Path)
		if len(repos) > 1 {
			name = esc(f.Repo) + ": " + name
		}
		files = append(files, name)
	}
	if more := h.SharedFiles - len(h.Files); more > 0 {
		files = append(files, plural(more, "more", "more"))
	}
	if len(files) > 0 {
		s += ": " + joinNames(files)
	}
	return s, true
}

// identGap is a hand-off's gap in whole days, else hours, else minutes.
func identGap(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return plural(int(math.Round(d.Hours()/24)), "day", "days")
	case d >= time.Hour:
		return plural(int(math.Round(d.Hours())), "hour", "hours")
	}
	return plural(max(1, int(math.Round(d.Minutes()))), "minute", "minutes")
}

// identCommentedNote explains a candidate whose lines are commented out:
// on a page, where the mailmap form writes them so; in that form, above
// them.
func identCommentedNote(c IdentityCandidate, inMailmap bool) string {
	what := "a weak match"
	switch {
	case c.Guess:
		what = "a guess"
	case c.Confidence == string(identity.ConfidenceMedium):
		what = "a match on the display name, which two people of one name would also make"
	}
	if inMailmap {
		return what + ", so its lines are commented out: uncomment them only if you know these addresses are one person"
	}
	return what + ": the mailmap form writes its lines commented out, to uncomment only if you know it is one person"
}

// identRules are the definitions of the rules the candidates use, in order.
func identRules(id *Identities) []IdentityRule {
	used := map[string]bool{}
	for _, c := range id.Candidates {
		for _, r := range c.Rules {
			used[r] = true
		}
	}
	var out []IdentityRule
	for _, r := range id.Rules {
		if used[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

func (id *Identities) writeText(p *printer) {
	p.wrapped("", "  ", "identities · "+scopeText(id.Scope, clean, id.Provenance)+" · "+provenanceText(id.Provenance))
	p.wrapped("  ", "  ", identIntro)
	p.wrapped("  ", "  ", identSummary(id))
	if len(id.Candidates) == 0 {
		p.blank()
		p.wrapped("  ", "  ", identNone)
	}
	for i, c := range id.Candidates {
		p.blank()
		p.wrapped(fmt.Sprintf("%d. ", i+1), "   ", identTitle(c, clean))
		labels := identLabels(c)
		for j, a := range c.Addresses {
			p.wrapped(fmt.Sprintf("   %d. ", j+1), "      ", identAddressText(a, labels[j], id.Provenance, clean))
		}
		for _, why := range identWhy(c, clean, clean) {
			p.wrapped("   ", "     ", why)
		}
		if c.Commented {
			p.wrapped("   ", "     ", identCommentedNote(c, false))
		}
	}
	if rules := identRules(id); len(rules) > 0 {
		p.blank()
		p.line("How these rules can be wrong")
		for _, r := range rules {
			p.wrapped("  - ", "    ", r.ID+": "+clean(r.Risk))
		}
	}
	textCaveats(p, "", "What could make this wrong", id.Caveats, false)
}

func (id *Identities) writeMarkdown(p *printer) {
	p.line("# Identity candidates · " + scopeText(id.Scope, mdText, id.Provenance))
	p.blank()
	p.line("- " + provenanceText(id.Provenance))
	p.line("- " + mdText(identSentence(identIntro)))
	p.line("- " + mdText(identSummary(id)))
	if len(id.Candidates) == 0 {
		p.blank()
		p.line(mdText(identSentence(identNone)) + ".")
	}
	for i, c := range id.Candidates {
		p.blank()
		p.line(fmt.Sprintf("## %d. %s", i+1, identTitle(c, mdText)))
		p.blank()
		cols := []column{{head: "#", right: true}, {head: "Domain"}, {head: "Name"}, {head: "Commits", right: true},
			{head: "Co-authored", right: true}, {head: "First"}, {head: "Last"}, {head: "Repositories"}, {head: "Canonical"}}
		labels := identLabels(c)
		var rows [][]string
		for j, a := range c.Addresses {
			var repos []string
			for _, key := range a.Repos {
				repos = append(repos, identRepoName(key, id.Provenance))
			}
			canonical := ""
			if a.Canonical {
				canonical = "yes"
			}
			rows = append(rows, []string{strconv.Itoa(j + 1), mdText(labels[j]), identAddressName(a, mdText),
				grouped(a.Commits), grouped(a.CoAuthored), date(a.First), date(a.Last), repoList(repos, mdText), canonical})
		}
		p.mdTable(cols, rows)
		p.blank()
		for _, why := range identWhy(c, mdText, mdCode) {
			p.line("- " + identSentence(why) + ".")
		}
		if c.Commented {
			p.line("- " + identSentence(identCommentedNote(c, false)) + ".")
		}
	}
	if rules := identRules(id); len(rules) > 0 {
		p.blank()
		p.line("## How these rules can be wrong")
		p.blank()
		for _, r := range rules {
			p.line("- " + mdID(r.ID) + ": " + mdText(r.Risk))
		}
	}
	mdCaveats(p, "##", id.Caveats, false)
}

// Mailmap renders an identities answer as .mailmap lines for a person to
// review, each candidate's evidence above its lines as # comments
// (gitmailmap(5)). Only a high candidate's lines, which rest on evidence the
// display name does not explain, are written as lines; every other
// candidate's are written commented out, so adding the output to a .mailmap
// unread merges nobody the facts cannot tell from someone else. Each line
// maps one alias to the candidate's canonical address and suggested name:
// "Name <canonical> <alias>", which replaces both for every commit with the
// alias, whatever its name.
//
// Git reads a line as a comment only when # is its first character, so
// every comment starts in the first column; an indented one would be a
// mapping. A line whose name or address holds a character git would read as
// syntax (< or >), a control character, or a name that starts with #, is
// written commented out with the reason, its text escaped: it cannot be a
// mapping, and its bytes must not reach a terminal. Unlike text and
// Markdown, this form prints emails: its lines are made of them.
func Mailmap(w io.Writer, id *Identities) error {
	p := &printer{w: w}
	id.writeMailmap(p)
	if p.err != nil {
		return fmt.Errorf("report: write mailmap: %w", p.err)
	}
	return nil
}

func (id *Identities) writeMailmap(p *printer) {
	p.wrapped("# ", "#   ", "stillhere identities · "+scopeText(id.Scope, clean, id.Provenance)+" · "+provenanceText(id.Provenance))
	p.wrapped("# ", "# ", "Suggested .mailmap lines for a person to review: stillhere applies none, because git's "+
		"mailmap is the only identity store. A wrong line merges two people into one who does not exist, so read "+
		"each candidate's evidence before adding its lines to a .mailmap, or to the file --mailmap names. Only a "+
		"candidate with evidence the display name does not explain (one GitHub account, or a local part not made "+
		"from the name) is written as lines; every other one's lines are commented out: uncomment them only if "+
		"you know the addresses are one person. "+identSentence(identNoChain)+". A line takes effect at the next scan.")
	p.wrapped("# ", "# ", identSummary(id))
	if len(id.Candidates) == 0 {
		p.line("#")
		p.wrapped("# ", "# ", identSentence(identNone)+".")
		return
	}
	for i, c := range id.Candidates {
		p.blank()
		p.wrapped(fmt.Sprintf("# %d. ", i+1), "#    ", identTitle(c, clean))
		for j, a := range c.Addresses {
			p.wrapped(fmt.Sprintf("#   %d. ", j+1), "#      ", identAddressText(a, clean(a.Email), id.Provenance, clean))
		}
		for _, why := range identWhy(c, clean, clean) {
			p.wrapped("#   ", "#     ", why)
		}
		if c.Commented {
			p.wrapped("#   ", "#     ", identCommentedNote(c, true))
		}
		for _, line := range identLines(c) {
			if !identSafe(line) {
				p.wrapped("#   ", "#     ", "this line cannot be a .mailmap line: a name or an address in it holds "+
					"< or >, a control character, or a name starts with #")
			}
			p.line(line.mailmap(c.Commented))
		}
	}
}

// MailmapLines returns a candidate's suggested .mailmap lines exactly as
// the mailmap form writes them: as lines for a high candidate, commented
// out ("# ") for every other one, and a line git could not read as the
// mapping it means commented out and escaped. The mailmap form and an
// answer that carries the lines (IdentityCandidate.Mailmap) both write
// them through it, so the two cannot drift.
func MailmapLines(c IdentityCandidate) []string {
	var out []string
	for _, line := range identLines(c) {
		out = append(out, line.mailmap(c.Commented))
	}
	return out
}

// identLine is one .mailmap line: name and canonical replace a commit's
// name and email when the commit's email is alias; with alias "", the line
// replaces only the name of canonical's own commits.
type identLine struct{ name, canonical, alias string }

func (l identLine) text() string {
	s := "<" + l.canonical + ">"
	if l.name != "" {
		s = l.name + " " + s
	}
	if l.alias != "" {
		s += " <" + l.alias + ">"
	}
	return s
}

// mailmap is the line as the mailmap form writes it: commented out when its
// candidate's lines are (commented), and, when git could not read it as
// the mapping it means (identSafe), commented out with its text escaped.
func (l identLine) mailmap(commented bool) string {
	switch {
	case !identSafe(l):
		return "# " + clean(l.text())
	case commented:
		return "# " + l.text()
	}
	return l.text()
}

// identLines are a candidate's .mailmap lines: one per alias, after a line
// that gives the canonical address the suggested name when its own differs.
func identLines(c IdentityCandidate) []identLine {
	var canonical *IdentityAddress
	for i := range c.Addresses {
		if c.Addresses[i].Canonical {
			canonical = &c.Addresses[i]
		}
	}
	if canonical == nil {
		return nil
	}
	var out []identLine
	if c.Name != "" && c.Name != canonical.Name {
		out = append(out, identLine{name: c.Name, canonical: canonical.Email})
	}
	for _, a := range c.Addresses {
		if !a.Canonical {
			out = append(out, identLine{name: c.Name, canonical: canonical.Email, alias: a.Email})
		}
	}
	return out
}

// identSafe reports whether git reads the line as the mapping it means, and
// whether its bytes are safe to print: no < or > (git reads them as an
// address's brackets), no control or format character, no byte that is not
// UTF-8, and no name that starts with #, which makes the line a comment.
func identSafe(l identLine) bool {
	ok := func(s string) bool {
		return !strings.ContainsFunc(s, func(r rune) bool {
			return r == '<' || r == '>' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError
		})
	}
	return l.canonical != "" && ok(l.canonical) && ok(l.alias) && ok(l.name) && !strings.HasPrefix(l.name, "#")
}

// identSentence starts s with a capital letter.
func identSentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
