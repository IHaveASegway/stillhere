package report

import (
	"cmp"
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
	Candidates []IdentityCandidate `json:"candidates"` // most confident first, the hand-off guesses last
	// Guesses is how many of Candidates are hand-off guesses, which come
	// last: the first len(candidates) - guesses are the candidates the text
	// headline counts (wave 10's review: an agent paging the list could not
	// tell where the guesses began).
	Guesses  int            `json:"guesses"`
	Rules    []IdentityRule `json:"rules"` // every rule's definition, in order
	Caveats  []Caveat       `json:"caveats"`
	rewrites bool           // the scope's facts show git's mailmap rewriting an email (domain.RepoFacts.Aliases)

	addrs []string // the run's addresses (#90); never encoded
}

func (id *Identities) runAddresses() []string { return id.addrs }

// IdentityCandidate is a group of addresses that may belong to one person.
type IdentityCandidate struct {
	Confidence string `json:"confidence"` // high, medium or low: its weakest pair's
	Guess      bool   `json:"guess"`      // a hand-off: never suggested as certain
	// Commented: the mailmap form writes every line of the candidate
	// commented out, since no two of its addresses are linked by
	// high-confidence evidence. A high candidate's lines are all written as
	// lines; a medium or low one's, those of its addresses marked high
	// (IdentityAddress.High, #103), the others commented out.
	Commented bool              `json:"commented"`
	Chained   bool              `json:"chained"`   // two of the addresses are linked only through a third
	Rules     []string          `json:"rules"`     // the rules that link the addresses, in the order of rules
	Name      string            `json:"name"`      // the display name suggested for the person; "" keeps each commit's own
	Addresses []IdentityAddress `json:"addresses"` // by first activity; exactly one is canonical
	Links     []IdentityLink    `json:"links"`     // each rule's evidence: which addresses it links
	// Mailmap holds the candidate's suggested .mailmap lines exactly as
	// the mailmap form writes them (MailmapLines), each commented out
	// unless high evidence backs it. They are made of addresses, so only an answer
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
	// High: high-confidence evidence links this address to the canonical
	// one and to every other address marked high (identity.Candidate.High,
	// #103), so the lines that map it are written as lines. Every address
	// of a high candidate is; in a medium or low one, those that GitHub's
	// links or a local part not made from the name tie together, the
	// canonical one included; in a candidate with no high pair, none.
	High bool `json:"high"`
	// Aliases counts the emails git's mailmap already rewrote to this
	// address in the run's commits (identity.Address.Aliases, #53): lines
	// a .mailmap holds, which the suggested lines follow, since git never
	// chains two. The emails are only in the mailmap lines, which are made
	// of emails and which only a reader that may see them asks for, so no
	// other field carries an address a .mailmap folded away.
	Aliases int `json:"aliases"`
	// NamesDiffer: the address's commits carry names that differ, a name
	// change or a mailbox several people commit as, which only GitHub's link
	// ties to the others (identity.Address.NamesDiffer).
	NamesDiffer bool `json:"names_differ"`
	// Names are, when NamesDiffer, the author names the address's commits
	// carry, less those a page withholds (a name that gives an address of
	// the run away), which NamesWithheld counts: what the evidence asks a
	// reviewer to check (identity.Address.Names); [] otherwise.
	Names         []string `json:"names"`
	NamesWithheld int      `json:"names_withheld"`
	// rewritten is what git rewrote to the address, each email with the
	// names it was written with and where: the mailmap lines read it.
	rewritten []identity.Alias
}

// IdentityLink is one rule's evidence in a candidate.
type IdentityLink struct {
	Rule      string           `json:"rule"`
	Addresses []int            `json:"addresses"` // indexes into the candidate's addresses
	Name      string           `json:"name"`      // same-name: the display name they share, "" when withheld; "" for the other rules
	FromName  bool             `json:"from_name"` // same-local-part: the local part is made from the name, so it adds nothing to it
	HandOff   *IdentityHandOff `json:"hand_off"`  // hand-off: from the first address to the second; null otherwise
	// Login is same-forge-account's GitHub account, as GitHub last showed
	// its login, "@" not included; "" for the other rules, and where it gives
	// an address of the run away (a local part of one), as a page that
	// prints no address must not print it (identity.Directory.LoginWithheld).
	Login string `json:"login"`
	// LoginWithheld: same-forge-account's login is withheld from Login as a
	// local part of one of the linked addresses, which a reader that may see
	// emails reads there (wave 10's review: most links' login was "" with
	// nothing saying why).
	LoginWithheld bool `json:"login_withheld"`
	// login is the account's login whether withheld or not, which only the
	// mailmap form, made of the addresses themselves, prints.
	login string
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
// rewrites says whether the facts show git rewriting any email: a run
// that shows none either applied no line, or was scanned before stillhere
// recorded what git rewrote, and the facts cannot tell which.
func identNoChain(rewrites bool) string {
	s := "git applies one .mailmap line per commit and never follows a chain, so where the run's commits show git " +
		"rewriting other emails to a suggested alias, through lines a .mailmap already holds, the suggestion maps " +
		"those emails, with the names they were written with, to the canonical address too, which replaces those " +
		"lines however they match, and it writes no line git already applies wherever the address's commits are"
	if rewrites {
		return s
	}
	return s + "; this run's commits here show git rewriting no email, either because no .mailmap line applied or " +
		"because the run was scanned before stillhere recorded what git rewrote, and then change such lines to " +
		"the suggested canonical address yourself"
}

// NewIdentities builds the identities answer. Like every answer, it arranges
// what another package computed, identity.Candidates here, and computes
// nothing itself; it adds where the facts come from and what could make the
// candidates wrong, and it withholds every name that gives an address away.
func NewIdentities(in *IdentitiesInput) *Identities {
	scope := in.Scope
	if scope == (domain.Scope{}) {
		scope = domain.Scope{Kind: domain.ScopeAll}
	}
	id := &Identities{Scope: scopeOf(scope), addrs: runAddresses(in.Who)}
	fs := in.Facts
	if fs == nil {
		fs = &domain.FactSet{}
	}
	names := map[string]string{}
	for i := range fs.Repos {
		names[fs.Repos[i].Repo.Key] = fs.Repos[i].Repo.Name
	}
	for i := range fs.Repos {
		id.rewrites = id.rewrites || len(fs.Repos[i].Aliases) > 0
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
		id.Guesses = identGuesses(id.Candidates)
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
			identSentence(identNoChain(id.rewrites)) + ".",
	})
	return id
}

func identCandidate(c *identity.Candidate, names map[string]string, who People) IdentityCandidate {
	shown := identShown(c, who)
	name, _ := shown(c.Name)
	out := IdentityCandidate{
		Confidence: string(c.Confidence),
		Guess:      c.Guess,
		Commented:  len(c.High) == 0,
		Chained:    c.Chained,
		Name:       name,
	}
	for _, r := range c.Rules() {
		out.Rules = append(out.Rules, string(r))
	}
	for i, a := range c.Addresses {
		name, withheld := shown(a.Name)
		out.Addresses = append(out.Addresses, IdentityAddress{
			Email: a.Email, Name: name, NameWithheld: withheld, DomainKind: string(a.Domain),
			Canonical: a.Email == c.Canonical, Commits: a.Commits, CoAuthored: a.CoAuthored,
			First: a.First, Last: a.Last, Repos: slices.Clone(a.Repos), High: slices.Contains(c.High, i),
			Aliases: len(a.Aliases), rewritten: slices.Clone(a.Aliases), NamesDiffer: a.NamesDiffer,
			Names: slices.Clone(a.Names), NamesWithheld: a.NamesWithheld,
		})
	}
	for _, l := range c.Links {
		name, _ := shown(l.Name)
		link := IdentityLink{Rule: string(l.Rule), Addresses: slices.Clone(l.Addresses), Name: name, FromName: l.FromName}
		if !l.LoginWithheld {
			link.Login = l.Login
		}
		link.LoginWithheld, link.login = l.LoginWithheld && l.Login != "", l.Login
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
	"that gives an address away is withheld. Commits are those in this run: its history and the older commits " +
	"blame credits lines to, not every commit of the address. --format mailmap prints the .mailmap lines, and " +
	"--format json the addresses."

// identSummary is "11 addresses of people in 1 repository, 1 bot or agent
// left out · 2 candidates", and the hand-off guesses apart: "· 7
// candidates, and 19 hand-off guesses (--all lists them)" on a page that
// lists them only with --all (wave 10: on an open-source repository most
// candidates are guesses between unrelated people, which buried the real
// ones), "and 19 hand-off guesses, listed last" on one that lists them.
func identSummary(id *Identities, all bool) string {
	s := plural(id.Addresses, "address", "addresses") + " of people in " +
		plural(len(id.Provenance.Heads), "repository", "repositories")
	var aside []string
	if id.Bots > 0 {
		aside = append(aside, plural(id.Bots, "bot or agent", "bots and agents")+" left out")
	}
	if id.Shared > 0 {
		aside = append(aside, plural(id.Shared, "shared address", "shared addresses")+
			" (a role mailbox, or several people's names) that no rule linked")
	}
	if id.Oversized > 0 {
		// identity's limit on a candidate's addresses (candMaxShare).
		aside = append(aside, plural(id.Oversized, "group", "groups")+
			" of more than 10 linked addresses left out as several people's")
	}
	if len(aside) > 0 {
		s += ", " + strings.Join(aside, ", ")
	}
	guesses := id.Guesses
	s += " · " + plural(len(id.Candidates)-guesses, "candidate", "candidates")
	switch {
	case guesses == 0:
	case all:
		s += ", and " + plural(guesses, "hand-off guess", "hand-off guesses") + ", listed last"
	default:
		s += ", and " + plural(guesses, "hand-off guess", "hand-off guesses") + " (--all lists them)"
	}
	return s
}

// identGuesses counts the hand-off guesses among candidates: candidates
// whose only evidence is one address taking over from another.
func identGuesses(cs []IdentityCandidate) int {
	n := 0
	for _, c := range cs {
		if c.Guess {
			n++
		}
	}
	return n
}

// identListed is the candidates a page lists: every one with --all (the
// guesses last, as identity.Candidates sorts them), else those that are not
// guesses. JSON and the mailmap form hold every candidate.
func identListed(p *printer, cs []IdentityCandidate) []IdentityCandidate {
	if p.all {
		return cs
	}
	var out []IdentityCandidate
	for _, c := range cs {
		if !c.Guess {
			out = append(out, c)
		}
	}
	return out
}

// identNone says what an empty answer means.
const identNone = "no candidates: the rules link no two addresses by a shared display name, local part or " +
	"GitHub account, or by one address taking over from another that stopped"

// identTitle is "Ada Example · medium confidence · same-name,
// same-local-part"; a hand-off names each address's person in order.
func identTitle(c IdentityCandidate, esc func(string) string, emails bool) string {
	title := identPerson(c.Name, esc)
	if c.Name == "" {
		// A candidate with no name to suggest is headed by what does say
		// who it is (wave 10's review: the encode organisation's top
		// maintainer was "(no name)"): GitHub's account, where its login may
		// be printed; else why there is no name: its names differ, or the
		// name is withheld, as its addresses' rows say.
		for _, l := range c.Links {
			if login := l.Login; identity.Rule(l.Rule) == identity.RuleSameForgeAccount && (login != "" || emails && l.login != "") {
				title = "@" + esc(cmp.Or(login, l.login))
				break
			}
		}
		switch {
		case title != identPerson("", esc):
		case slices.ContainsFunc(c.Addresses, func(a IdentityAddress) bool { return a.NamesDiffer }):
			title = "(names differ)"
		case slices.ContainsFunc(c.Addresses, func(a IdentityAddress) bool { return a.NameWithheld }):
			title = "(name withheld)"
		}
	}
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

// identAddressText is "main domain · Ada Example · 2 commits in this run ·
// 2026-01-01 → 2026-01-05 · fixture · canonical", or, for Mailmap, the same
// with the email itself first. The commits are the run's (round 3's KT-5:
// a bare "390 commits" beside a range back to 2012 read as the address's
// whole history, where git log counted 1,577), which identIntro says.
func identAddressText(a IdentityAddress, label string, pv Provenance, esc func(string) string) string {
	parts := []string{label, identAddressName(a, esc), plural(a.Commits, "commit", "commits") + " in this run"}
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
// emails: the form prints the addresses themselves (the mailmap form), so a
// login withheld as a local part of one is printed too.
func identWhy(c IdentityCandidate, esc, path func(string) string, emails bool) []string {
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
		case identity.RuleSameForgeAccount:
			account := "one account"
			switch {
			case l.Login != "", emails && l.login != "":
				account = "one account, @" + esc(cmp.Or(l.Login, l.login))
			case l.LoginWithheld:
				account = "one account, whose login is withheld as the local part of an address"
			}
			out = append(out, "GitHub attributes the commits of "+which+" to "+account+": GitHub's own link, the "+
				"attribution its contributor pages show, which no display name makes")
			var differ []int
			for _, i := range l.Addresses {
				if c.Addresses[i].NamesDiffer {
					differ = append(differ, i)
				}
			}
			if len(differ) > 0 {
				out = append(out, identDiffer(c, differ, esc)+", as a name change or a mailbox several people commit "+
					"as is: GitHub gives every commit on such an address to that account, and so does the line, so check "+
					"the names are one person's before applying it")
			}
		case identity.RuleHandOff:
			if h, ok := identHandOffText(l, esc, path); ok {
				out = append(out, h)
			}
		}
	}
	if c.Chained {
		out = append(out, "two of these addresses share nothing and are linked only through another, so the candidate is low")
	}
	// #103: the addresses high evidence ties together in a weaker candidate.
	var high []int
	for i, a := range c.Addresses {
		if a.High {
			high = append(high, i)
		}
	}
	if len(high) > 0 && len(high) < len(c.Addresses) {
		out = append(out, identWhich(high, len(c.Addresses))+" are linked to one another by high-confidence evidence, so "+
			"their lines are written as lines")
	}
	// #53: what the .mailmap lines git already applies change in the lines.
	var canonical IdentityAddress
	for _, a := range c.Addresses {
		if a.Canonical {
			canonical = a
		}
	}
	for i, a := range c.Addresses {
		if a.Canonical {
			continue
		}
		switch everywhere, somewhere := identApplied(canonical, a); {
		case everywhere:
			out = append(out, fmt.Sprintf("a .mailmap line already maps address %d to the canonical address wherever its "+
				"commits are, so it gets no line of its own", i+1))
		case somewhere:
			out = append(out, fmt.Sprintf("a .mailmap line maps address %d to the canonical address in some repositories "+
				"only, and a repository's own .mailmap applies only inside it, so address %d still gets a line", i+1, i+1))
		}
		switch n := a.Aliases; {
		case n == 1:
			out = append(out, fmt.Sprintf("address %d is where a .mailmap line maps another address, which git does not "+
				"follow on to the canonical address, so that address gets a line of its own, naming what the line it "+
				"replaces matched", i+1))
		case n > 1:
			out = append(out, fmt.Sprintf("address %d is where .mailmap lines map %d other addresses, which git does not "+
				"follow on to the canonical address, so each gets a line of its own, naming what the line it replaces "+
				"matched", i+1, n))
		}
	}
	return out
}

// identDiffer is "address 2 is written with several names: Alice Ng and
// Bob Roe", or "each of addresses 1 and 3 is written with several names:
// address 1: A and B; address 3: C and 1 withheld": the names behind
// names_differ, so the reviewer the evidence asks to check them can.
func identDiffer(c IdentityCandidate, differ []int, esc func(string) string) string {
	names := func(a IdentityAddress) string {
		var ns []string
		for _, n := range a.Names {
			ns = append(ns, nameText(n, esc))
		}
		if a.NamesWithheld > 0 {
			ns = append(ns, plural(a.NamesWithheld, "withheld", "withheld"))
		}
		if len(ns) == 0 {
			return "names withheld"
		}
		return joinNames(ns)
	}
	if len(differ) == 1 {
		i := differ[0]
		return fmt.Sprintf("address %d is written with several names: %s", i+1, names(c.Addresses[i]))
	}
	var nums, each []string
	for _, i := range differ {
		nums = append(nums, strconv.Itoa(i+1))
		each = append(each, fmt.Sprintf("address %d: %s", i+1, names(c.Addresses[i])))
	}
	return "each of addresses " + joinNames(nums) + " is written with several names: " + strings.Join(each, "; ")
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

// identCommentedNote explains a candidate some or all of whose lines are
// commented out (identSomeCommented): on a page, where the mailmap form
// writes them so; in that form, above them.
func identCommentedNote(c IdentityCandidate, inMailmap bool) string {
	what := "a weak match"
	switch {
	case c.Guess:
		what = "a guess"
	case c.Confidence == string(identity.ConfidenceMedium):
		what = "a match on the display name, which two people of one name would also make"
	}
	switch {
	case !c.Commented && inMailmap:
		return "the other addresses are " + what + ", so their lines are commented out: uncomment them only if you know " +
			"these addresses are one person"
	case !c.Commented:
		return "the other addresses are " + what + ": the mailmap form writes their lines commented out, to uncomment " +
			"only if you know it is one person"
	case inMailmap:
		return what + ", so its lines are commented out: uncomment them only if you know these addresses are one person"
	}
	return what + ": the mailmap form writes its lines commented out, to uncomment only if you know it is one person"
}

// identSomeCommented reports whether the mailmap form writes any of a
// candidate's lines commented out: every candidate that is not high.
func identSomeCommented(c IdentityCandidate) bool {
	return c.Confidence != string(identity.ConfidenceHigh)
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
	p.wrapped("  ", "  ", identSummary(id, p.all))
	if len(id.Candidates) == 0 {
		p.blank()
		p.wrapped("  ", "  ", identNone)
	}
	for i, c := range identListed(p, id.Candidates) {
		p.blank()
		p.wrapped(fmt.Sprintf("%d. ", i+1), "   ", identTitle(c, clean, false))
		labels := identLabels(c)
		for j, a := range c.Addresses {
			p.wrapped(fmt.Sprintf("   %d. ", j+1), "      ", identAddressText(a, labels[j], id.Provenance, clean))
		}
		for _, why := range identWhy(c, clean, clean, false) {
			p.wrapped("   ", "     ", why)
		}
		if identSomeCommented(c) {
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
	p.line("- " + mdText(identSummary(id, p.all)))
	if len(id.Candidates) == 0 {
		p.blank()
		p.line(mdText(identSentence(identNone)) + ".")
	}
	for i, c := range identListed(p, id.Candidates) {
		p.blank()
		p.line(fmt.Sprintf("## %d. %s", i+1, identTitle(c, mdText, false)))
		p.blank()
		cols := []column{{head: "#", right: true}, {head: "Domain"}, {head: "Name"}, {head: "Commits in this run", right: true},
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
		for _, why := range identWhy(c, mdText, mdCode, false) {
			p.line("- " + identSentence(why) + ".")
		}
		if identSomeCommented(c) {
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
		"line with evidence the display name does not explain (one GitHub account, or a local part not made "+
		"from the name) between its addresses is written as a line; every other line is commented out: uncomment "+
		"it only if you know the addresses are one person. "+identSentence(identNoChain(id.rewrites))+
		". A line takes effect at the next scan.")
	p.wrapped("# ", "# ", identSummary(id, true))
	if len(id.Candidates) == 0 {
		p.line("#")
		p.wrapped("# ", "# ", identSentence(identNone)+".")
		return
	}
	for i, c := range id.Candidates {
		p.blank()
		p.wrapped(fmt.Sprintf("# %d. ", i+1), "#    ", identTitle(c, clean, true))
		for j, a := range c.Addresses {
			p.wrapped(fmt.Sprintf("#   %d. ", j+1), "#      ", identAddressText(a, clean(a.Email), id.Provenance, clean))
		}
		for _, why := range identWhy(c, clean, clean, true) {
			p.wrapped("#   ", "#     ", why)
		}
		if identSomeCommented(c) {
			p.wrapped("#   ", "#     ", identCommentedNote(c, true))
		}
		for _, line := range identLines(c) {
			if !identSafe(line) {
				p.wrapped("#   ", "#     ", "this line cannot be a .mailmap line: a name or an address in it holds "+
					"< or >, a control character, or a name starts with #")
			}
			p.line(line.mailmap())
		}
	}
}

// MailmapLines returns a candidate's suggested .mailmap lines exactly as
// the mailmap form writes them: as lines where high evidence backs them (a
// high candidate's, and a weaker one's between its addresses marked high),
// commented out ("# ") otherwise, and a line git could not read as the
// mapping it means commented out and escaped. The mailmap form and an
// answer that carries the lines (IdentityCandidate.Mailmap) both write
// them through it, so the two cannot drift.
func MailmapLines(c IdentityCandidate) []string {
	var out []string
	for _, line := range identLines(c) {
		out = append(out, line.mailmap())
	}
	return out
}

// identLine is one .mailmap line: name and canonical replace a commit's
// name and email when the commit's email is alias and, when aliasName is
// not "", its name is aliasName (git compares names without case); with
// alias "", the line replaces only the name of canonical's own commits.
type identLine struct {
	name, canonical, aliasName, alias string
	commented                         bool // no high evidence links alias to canonical
}

func (l identLine) text() string {
	s := "<" + l.canonical + ">"
	if l.name != "" {
		s = l.name + " " + s
	}
	if l.aliasName != "" {
		s += " " + l.aliasName
	}
	if l.alias != "" {
		s += " <" + l.alias + ">"
	}
	return s
}

// mailmap is the line as the mailmap form writes it: commented out when no
// high evidence backs it, and, when git could not read it as the mapping it
// means (identSafe), commented out with its text escaped.
func (l identLine) mailmap() string {
	switch {
	case !identSafe(l):
		return "# " + clean(l.text())
	case l.commented:
		return "# " + l.text()
	}
	return l.text()
}

// identLines are a candidate's .mailmap lines: one per alias, after a line
// that gives the canonical address the suggested name when its own differs.
// A line is commented out unless high evidence links its alias to the
// canonical address (IdentityAddress.High); the name line, unless any does.
//
// git applies one line per commit and never follows a chain (#53), so an
// alias that is itself the canonical address of lines git applied (what git
// already rewrote to it) gets a line for each of those emails too, mapping
// them to the candidate's canonical address directly. Such a line names the
// email and each name it was written with: git prefers a line that matches
// a commit's name and email over one that matches its email alone,
// whatever their order, so only that form replaces both kinds of line. It
// also maps no commit the line it replaces did not, where a line of the
// email alone would take the commits a name-keyed line left out on purpose,
// as when one mailbox is split between two people. And an alias git
// already rewrites to the canonical address wherever its commits are gets
// no line of its own: it is there (identApplied).
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
		out = append(out, identLine{name: c.Name, canonical: canonical.Email, commented: c.Commented})
	}
	mapped := map[[2]string]bool{} // the email and name a line already maps: git keeps the last line for one
	line := func(alias, aliasName string, commented bool) {
		key := [2]string{alias, strings.ToLower(aliasName)}
		if !mapped[key] {
			mapped[key] = true
			out = append(out, identLine{name: c.Name, canonical: canonical.Email, aliasName: aliasName, alias: alias,
				commented: commented})
		}
	}
	for _, a := range c.Addresses {
		if a.Canonical {
			continue
		}
		if everywhere, _ := identApplied(*canonical, a); !everywhere {
			line(a.Email, "", !a.High)
		}
		for _, al := range a.rewritten {
			if len(al.Names) == 0 {
				line(al.Email, "", !a.High)
			}
			for _, name := range al.Names {
				line(al.Email, name, !a.High)
			}
		}
	}
	return out
}

// identApplied reports whether git already rewrites a's own commits to the
// address to wherever they are (everywhere), and whether a .mailmap line
// maps a there in any repository at all (somewhere). A repository's own
// .mailmap applies only inside it, so a line one repository holds does
// nothing for a's commits in another. And an address a .mailmap rewrites
// appears in no fact of its repository but as the target of other lines (a's
// own aliases there): where a appears without such a chain, some of its
// commits were left as they were, by a line keyed by a name they do not
// carry, and a still needs its line.
func identApplied(to, a IdentityAddress) (everywhere, somewhere bool) {
	in := func(list []identity.Alias, email, repo string) bool {
		return slices.ContainsFunc(list, func(al identity.Alias) bool {
			return (email == "" || al.Email == email) && slices.Contains(al.Repos, repo)
		})
	}
	for _, al := range to.rewritten {
		somewhere = somewhere || al.Email == a.Email
	}
	everywhere = somewhere && len(a.Repos) > 0
	for _, repo := range a.Repos {
		if !in(to.rewritten, a.Email, repo) || !in(a.rewritten, "", repo) {
			everywhere = false
		}
	}
	return everywhere, somewhere
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
	return l.canonical != "" && ok(l.canonical) && ok(l.alias) && ok(l.name) && ok(l.aliasName) &&
		!strings.HasPrefix(l.name, "#")
}

// identSentence starts s with a capital letter.
func identSentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
