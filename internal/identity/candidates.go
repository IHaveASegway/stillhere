package identity

import (
	"cmp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file finds identity candidates (#26, docs/spec/identities.md): groups
// of canonical emails in a run's facts that may belong to one person, each
// with the rules that link them, a confidence and the evidence. It suggests
// and never merges. git's mailmap stays the only identity store (ADR 0004),
// so a candidate becomes a merge only when a person writes its .mailmap
// line.
//
// A wrong merge invents a person, which is worse than a missed one, so every
// rule leans to precision. A key that many addresses share, or that the
// facts show two people using, is no evidence that any two of them are one
// person, and an address several people use is compared with nothing but
// GitHub's own attribution of it, which then counts as no more than a
// name. A local part made from the display name says no more than the
// name, so it never counts as a second signal. Addresses linked only
// through a third are never more than a weak match, and a hand-off is
// always a guess.

// Rule is one way two addresses can be linked.
type Rule string

// The rules, in the order Rules lists their definitions.
const (
	RuleSameName         Rule = "same-name"           // the same normalised display name
	RuleSameLocalPart    Rule = "same-local-part"     // the same local part on different domains
	RuleSameAccount      Rule = "same-github-account" // GitHub noreply addresses of one account id
	RuleSameForgeAccount Rule = "same-forge-account"  // GitHub attributes their commits to one account (#103)
	RuleHandOff          Rule = "hand-off"            // one address stopped as another started, in the same files: a guess
)

// Confidence is how sure a candidate is that its addresses are one person.
type Confidence string

// The confidences, most sure first. Only high rests on evidence the display
// name does not explain, so only a high candidate's .mailmap lines are
// suggested as lines; every other one's are written commented out.
const (
	ConfidenceHigh   Confidence = "high"   // GitHub's own account id or link, or a full name with a local part not made from it
	ConfidenceMedium Confidence = "medium" // one full name (two words or more) and nothing the name does not explain
	ConfidenceLow    Confidence = "low"    // one weak signal, a chain through a third address, or a hand-off
)

// DomainKind is what an address's domain says about it without naming it,
// so a page that prints no address and no part of one can still show it.
type DomainKind string

// The kinds of domain.
const (
	DomainMain    DomainKind = "main"    // the domain most of the scope's addresses use: the organisation's convention
	DomainNoreply DomainKind = "noreply" // GitHub's noreply domain, an alias by design
	DomainMachine DomainKind = "machine" // a domain git makes up from a machine's name: no dot, or .local and the like
	DomainOther   DomainKind = "other"   // any other domain, or none
)

// RuleDefinition is a rule's published definition: what it links, the
// confidence it gives, and how it can be wrong, as a metric's definition
// names its blind spots.
type RuleDefinition struct {
	Rule       Rule
	Matches    string // what the rule links
	Confidence string // the confidence its matches get
	Risk       string // how the rule can be wrong
}

// The hand-off rule's thresholds and the limits every rule shares. Changing
// one changes the candidates, so RuleDefinition texts and
// docs/spec/identities.md quote them.
const (
	candHandOffGap   = 30 * 24 * time.Hour // the most time from one address's last commit to the next one's first
	candHandOffQuiet = 90 * 24 * time.Hour // how long before the run's as-of time the earlier address must have stopped
	candMaxShare     = 10                  // a key more addresses share is a default, and a candidate of more addresses is several people's
	candMinLocal     = 3                   // shorter local parts (initials) are compared with nothing
	candMaxFiles     = 5                   // shared files a hand-off lists; it counts them all
	candNoreply      = "users.noreply.github.com"
)

// Rules returns the definition of every rule, in a fixed order. Every
// answer carries them, so whoever reads a candidate also reads how it can
// be wrong. The texts quote no whole address: the MCP server rewrites
// anything shaped like one.
func Rules() []RuleDefinition {
	return []RuleDefinition{
		{
			Rule: RuleSameName,
			Matches: "Addresses whose display names are the same once case, spacing and punctuation are ignored. " +
				"A default name (root, Your Name, A U Thor, John Doe, a name made only of words such as user, test " +
				"or unknown) matches nothing, nor does a name that more than 10 addresses share or that two GitHub " +
				"accounts use. An address several people use, because its local part is a role (team, admin, build " +
				"and the like) or its commits carry names that differ, is compared by no rule.",
			Confidence: "Medium for a name of two words or more, low for one word. High only with the same local " +
				"part, when that local part is not made from the name's words. A name alone, or with a local part " +
				"made from it, is never high, and the mailmap form writes its lines commented out.",
			Risk: "Different people share names, the more often the shorter and commoner the name, and the facts " +
				"cannot tell two people of one name apart: organisations make addresses from names, so two Alex " +
				"Kims at two employers are both alex.kim. A name written without spaces, as Chinese, Japanese and " +
				"Korean names often are, counts as one word and stays low, and a name spelt two ways (José and " +
				"Jose, Bob and Robert) is missed.",
		},
		{
			Rule: RuleSameLocalPart,
			Matches: "Addresses with the same local part (before the @) on different domains, or that differ only " +
				"by a +tag. A GitHub noreply address counts as its login: 12345+login@ is login. Role and default " +
				"local parts (admin, dev, info, root, team and the like), local parts shorter than 3 characters, " +
				"one that more than 10 addresses share, and one that people with different names, a shared " +
				"address or two GitHub accounts use match nothing.",
			Confidence: "Low on its own. With the same full name, high when the local part is not made from the " +
				"name's words, and medium when it is: ada for Ada Example, or alex.kim or akim2 for Alex Kim, says " +
				"no more than the name does.",
			Risk: "A first name or a common login as a local part (alex@, sam@) belongs to many people at different " +
				"domains, and git makes up user@host addresses from a machine's login name, which several people " +
				"can share.",
		},
		{
			Rule: RuleSameAccount,
			Matches: "GitHub noreply addresses with the same account id, such as 12345+old@ and 12345+new@ at " +
				"GitHub's noreply domain after a rename.",
			Confidence: "High: the id is GitHub's own, and a rename keeps it.",
			Risk:       "A forged address: anyone can write any address into a commit, so a commit is no proof of the account.",
		},
		{
			Rule: RuleSameForgeAccount,
			Matches: "Addresses whose commits GitHub attributes to one account: the pull requests a sync copied, as " +
				"the run read them, link each of their commits to the account that verified its author email, or " +
				"whose noreply address it is. A commit the run holds names its address after the mailmap; another, " +
				"such as a squashed pull request's own commit, names the email GitHub read, when the run shows that " +
				"address. A GitHub noreply address no commit links counts as its login's account, when the run's " +
				"GitHub data links that login, since GitHub gives such an address to its account by construction. " +
				"A name that is the account's login says nothing about who else uses an address, so it differs from " +
				"no other name. An address whose commits carry names that differ, which the other rules leave out as " +
				"several people's, is compared, since GitHub gives every commit on it to that one account; a role " +
				"address (team, admin, build and the like), an address GitHub links to two accounts, an account " +
				"that more than 10 addresses link to, and an account whose addresses hold two GitHub account ids " +
				"match nothing.",
			Confidence: "High: the link is GitHub's own, the attribution its contributor pages show, also for an address " +
				"whose commits carry names that differ, which a name change makes as often as a shared mailbox does: " +
				"the account verified the address, and GitHub gives that account every commit on it.",
			Risk: "GitHub links a commit by its author email, so a mailbox several people commit as, verified by one " +
				"of them, links to that person, as GitHub's own pages do, and its lines are written as lines: check a " +
				"candidate whose addresses carry different names before applying it. A noreply address whose login was renamed " +
				"and later taken by someone else counts as the new holder's. An address links through its pull requests' " +
				"commits, the links a sync kept of older pull requests, or the account GitHub gives its newest commit on the " +
				"default branch, which a sync's links pass asked about, so one seen only in direct pushes links too; an " +
				"address GitHub attributes to no account (not verified when that commit was made, or an account deleted " +
				"since) links nothing, and one person's two accounts (a work and a personal one) stay two.",
		},
		{
			Rule: RuleHandOff,
			Matches: "One address's last commit at most 30 days before another's first, both in a file of the same " +
				"repository, when the first address has no commit in the 90 days before the run's as-of time. Only " +
				"addresses no other rule links, and no shared address, are compared, and an address with two " +
				"possible successors, or two possible predecessors, gets none.",
			Confidence: "Always low, and a guess: the mailmap form writes its line commented out.",
			Risk: "A teammate who takes over someone's code when they leave looks exactly like one person changing " +
				"address. Only someone who knows them can tell.",
		},
	}
}

// Survey is what Candidates found in a fact set.
type Survey struct {
	Candidates []Candidate // most confident first, the guesses (hand-offs) after every other, then by suggested name and first address
	Addresses  int         // human addresses the facts show: the population the candidates come from
	Bots       int         // bot and agent addresses (Directory.Bot), which never appear in a candidate
	Shared     int         // of Addresses, those several people use (a role local part, or names that differ) that no rule linked: in no candidate
	Oversized  int         // groups of more than 10 addresses that the rules chain together, left out as several people's
}

// Candidate is a group of canonical emails that may belong to one person.
type Candidate struct {
	Confidence Confidence
	Guess      bool      // a hand-off: never suggested as certain
	Chained    bool      // two of the addresses share no key and are linked only through a third
	Canonical  string    // the address suggested as the canonical one; one of Addresses
	Name       string    // the display name suggested for the person; "" keeps each commit's own name
	Addresses  []Address // by first activity, then email
	Links      []Link    // the rules' evidence, in the order of Rules, then by first address
	// High holds the indexes into Addresses, ascending, of the addresses
	// that high evidence links to one another, the canonical one among them
	// (#103): every address of a high candidate; in a medium or low one,
	// the largest set whose every pair is high, so its lines are as sure as
	// a high candidate's while the others' stay commented out (candCore);
	// nil when no pair is high. A group is only as sure as its weakest pair
	// (Confidence), and so is this set.
	High []int
}

// Rules returns the rules that link the candidate's addresses, each once, in
// the order of Rules.
func (c *Candidate) Rules() []Rule {
	var out []Rule
	for _, def := range Rules() {
		if slices.ContainsFunc(c.Links, func(l Link) bool { return l.Rule == def.Rule }) {
			out = append(out, def.Rule)
		}
	}
	return out
}

// Address is one canonical email of a candidate with the evidence for it.
// The counts and times cover the facts the candidates were found in: the
// run's history window, and the older commits blame attributes lines to.
type Address struct {
	Email      string
	Name       string     // its display name (Directory.Name); "" when the facts show none
	Domain     DomainKind // what its domain says about it
	Commits    int        // commits it authored, each counted once
	CoAuthored int        // commits whose Co-authored-by trailers name it
	First      time.Time  // the earliest author time of those commits
	Last       time.Time  // the latest
	Repos      []string   // Repo.Key of every repository it appears in, sorted
	// Aliases are the emails git's mailmap rewrote to this one in the
	// facts (RepoFacts.Aliases, #53): the .mailmap lines git already
	// applies, which a suggested line must follow, since git never chains
	// two. By email; nil for none, and in a run stored before facts.db kept
	// them.
	Aliases []Alias
	// NamesDiffer: its commits carry names that differ, a name change or a
	// mailbox several people commit as, which only the same-forge-account
	// rule links (wave 10): the evidence says so, so a reader checks it.
	NamesDiffer bool
	// Names are, when NamesDiffer, the author names its commits carry,
	// sorted, less those a page withholds (Directory.Withheld's rule: a
	// name that gives an address of the run away), which NamesWithheld
	// counts: what a reader asked to check the address is one person's
	// checks (wave 10's review). nil otherwise.
	Names         []string
	NamesWithheld int
}

// Alias is an email git's mailmap rewrote to an address in the facts
// (domain.Alias): what a .mailmap line git applies matched, and where.
// A repository's own .mailmap applies only inside it, so Repos says where
// the line is; and git prefers a line that matches a commit's name and
// email over one that matches its email alone, so Names says what a line
// that replaces it must name.
type Alias struct {
	Email string   // as the commits or the trailers wrote it
	Names []string // the names written beside it, sorted; an empty one left out
	Repos []string // Repo.Key of the repositories where git rewrote it, sorted
}

// Link is one rule's evidence in a candidate: the addresses it links.
type Link struct {
	Rule      Rule
	Addresses []int    // indexes into Candidate.Addresses, ascending
	Name      string   // RuleSameName: the shared display name, as the first of those addresses shows it
	FromName  bool     // RuleSameLocalPart: the local part is made from a name the addresses carry, so it adds nothing to it
	HandOff   *HandOff // RuleHandOff: from Addresses[0] to Addresses[1]; nil for the other rules
	// Login is RuleSameForgeAccount's account, as GitHub last showed its
	// login (wave 10: the evidence names GitHub's link, not a display
	// name); LoginWithheld: a page that prints no address must not print
	// it (Directory.LoginWithheld: it is a local part of an address of the
	// run). "" for the other rules.
	Login         string
	LoginWithheld bool
}

// HandOff is the evidence for one step of a hand-off: an address stopped,
// and another started in the same files soon after.
type HandOff struct {
	Gap         time.Duration // from the earlier address's last commit to the later one's first
	SharedFiles int           // files both addresses touched or hold lines in, in the same repository
	Files       []File        // the first candMaxFiles of them, by repository and path
}

// File is a path in one repository.
type File struct {
	Repo string // Repo.Key
	Path string // repo-relative
}

// RevealsAddress reports whether a display name gives an address away, so
// that a page that prints no address must not print the name either: the
// name holds an @, as an address typed as a name does, or it is the
// address's own local part, as GitHub names a commit by the account's login
// when it has no profile name ("jdoe" on jdoe@ or on 12345+jdoe@ at the
// noreply domain). "John Doe" on john.doe@ is a name, not the local part.
// It compares one name with one address; Directory.Withheld applies the
// same rule to a name over every person's address in the run, which is
// what a page asks.
func RevealsAddress(name, email string) bool {
	return strings.ContainsRune(name, '@') || candSelfNamed(name, email)
}

// Candidates finds the groups of canonical emails in fs that may belong to
// one person (docs/spec/identities.md). fs is a run's facts, which git has
// already mailmapped, so a merge the mailmap makes is no candidate; who
// names the addresses and tells bots apart, and should be built over the
// whole run, so names match every other page. Bots and agents never appear.
// Candidates only reads its inputs, and every slice it returns is sorted.
//
// An address appears in one candidate at most, so the .mailmap lines of
// different candidates never map one alias twice: git would silently keep
// the last.
func Candidates(fs *domain.FactSet, who *Directory) *Survey {
	s, _, _ := candSurvey(fs, who)
	return s
}

// candSurvey is Candidates, with the addresses it compared and the
// hand-off chains it left out as several people's (Survey.Oversized), each
// as its addresses in time order.
func candSurvey(fs *domain.FactSet, who *Directory) (*Survey, []*candAddress, [][]*candAddress) {
	s := &Survey{}
	if fs == nil {
		return s, nil, nil
	}
	if who == nil {
		who = New(fs, nil)
	}
	addrs, bots := candCollect(fs, who)
	s.Addresses, s.Bots = len(addrs), bots
	domains := map[string]int{}
	for _, a := range addrs {
		domains[a.domain]++
	}
	mainDomain := candMainDomain(addrs)
	for _, a := range addrs {
		a.Domain = candDomainKind(a, mainDomain)
	}
	comps, linked, oversized := candComponents(addrs)
	s.Oversized = oversized
	for i, a := range addrs {
		// A shared address only the same-forge-account rule compares, which
		// it linked, is in a candidate (or an oversized group), not left out.
		if a.shared && !linked[i] {
			s.Shared++
		}
	}
	for _, comp := range comps {
		s.Candidates = append(s.Candidates, comp.candidate(domains))
	}
	var singles []*candAddress
	for i, a := range addrs {
		if !linked[i] && !a.shared {
			singles = append(singles, a)
		}
	}
	var leftOut [][]*candAddress
	for _, chain := range candHandOffs(fs, singles) {
		// A chain of more addresses than a candidate may hold is several
		// people's, as a component of that size is.
		if len(chain)+1 > candMaxShare {
			s.Oversized++
			members := []*candAddress{chain[0].from}
			for _, e := range chain {
				members = append(members, e.to)
			}
			leftOut = append(leftOut, members)
			continue
		}
		s.Candidates = append(s.Candidates, candChainCandidate(chain, domains))
	}
	for i := range s.Candidates {
		for j := range s.Candidates[i].Links {
			if l := &s.Candidates[i].Links[j]; l.Login != "" {
				l.LoginWithheld = who.LoginWithheld(l.Login)
				l.Login = cmp.Or(who.forms[l.Login], l.Login)
			}
		}
	}
	// The guesses last, whatever their confidence (wave 10): on an
	// open-source repository most candidates are hand-offs between unrelated
	// people, which buried the few real ones.
	slices.SortFunc(s.Candidates, func(a, b Candidate) int {
		return cmp.Or(
			candFalseFirst(a.Guess, b.Guess),
			cmp.Compare(candRank(a.Confidence), candRank(b.Confidence)),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Addresses[0].Email, b.Addresses[0].Email),
		)
	})
	return s, addrs, leftOut
}

// candAddress is one human address while the rules compare it.
type candAddress struct {
	Address
	key       string          // candNameKey of the display name: "" for none or a default name
	words     int             // the words in key
	selfNamed bool            // the display name is the address's own local part (candSelfNamed)
	handle    string          // the local part the local-part rule compares; "" when it compares none
	account   string          // a GitHub noreply address's account id; "" for every other address
	login     string          // a GitHub noreply address's login, in either form; "" for every other address
	forge     string          // the one GitHub account the run's GitHub data attributes its commits to, folded; "" for none or two (candForge)
	domain    string          // after the last @; "" for an address without one
	noreply   bool            // a GitHub noreply address
	role      bool            // its local part is a role or default one on a domain other than GitHub's noreply one (candLocal)
	shared    bool            // several people use it: a role local part, or commits under names that differ
	files     map[File]bool   // what it touched or holds lines in; collected only for a possible hand-off (candFiles)
	repos     map[string]bool // while collecting
	counted   map[string]bool // while collecting: commit SHAs already counted, so a commit two repositories share counts once
	names     map[string]bool // while collecting: the author names its commits carry
	coNames   map[string]bool // while collecting: the names co-author trailers give it
	nameKeys  []string        // every name seen with it, as author or co-author, by its words (candWords), a default one included: the names Blocks compares
}

// candCollect gathers every human address the facts show, as a commit's
// author or co-author, and as the author or co-author of a commit blame
// attributes lines to, which can be older than the history. It returns them
// sorted by email, with the number of bot and agent addresses it left out.
// It reads no files: only a hand-off needs them, so candFiles reads them
// for the few addresses one can involve, and the facts are not copied
// again for everyone else.
func candCollect(fs *domain.FactSet, who *Directory) ([]*candAddress, int) {
	byEmail := map[string]*candAddress{}
	bots := map[string]bool{}
	address := func(email string) *candAddress {
		if email == "" || bots[email] {
			return nil
		}
		if a, ok := byEmail[email]; ok {
			return a
		}
		if who.Bot(email) {
			bots[email] = true
			return nil
		}
		a := &candAddress{Address: Address{Email: email},
			repos: map[string]bool{}, counted: map[string]bool{}, names: map[string]bool{}}
		byEmail[email] = a
		return a
	}
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		repo := rf.Repo.Key
		for j := range rf.Commits {
			c := &rf.Commits[j]
			candSee(address, repo, c.SHA, c.AuthorTime, c.Author, c.CoAuthors)
		}
		// After the history, so that a commit both hold is the history's;
		// the rest are older commits blame attributes lines to.
		for j := range rf.Owners {
			o := &rf.Owners[j]
			candSee(address, repo, o.Commit, o.AuthorTime, o.Author, o.CoAuthors)
		}
	}
	rewritten := candRewritten(fs)
	linked := map[string]bool{} // every login the run's GitHub data links an address to
	for _, logins := range who.accounts {
		for _, l := range logins {
			linked[l] = true
		}
	}
	addrs := make([]*candAddress, 0, len(byEmail))
	for _, a := range byEmail {
		a.Aliases = rewritten[a.Email]
		a.Name = who.Name(a.Email)
		a.key, a.words = candNameKey(a.Name)
		a.handle, a.account, a.login, a.domain, a.noreply, a.role = candLocal(a.Email)
		a.selfNamed = candSelfNamed(a.Name, a.Email)
		a.nameKeys = candNameKeys(a)
		a.forge = candForge(a, who.accounts[a.Email], linked)
		// A GitHub noreply address is one account's by construction, so
		// names that differ on it are a person's name changing.
		a.shared = !a.noreply && (a.role || !candOwnNamesAgree(a, who.accounts[a.Email]))
		if a.shared && !a.role {
			a.Names, a.NamesWithheld = candShownNames(a.names, who)
		}
		for r := range a.repos {
			a.Repos = append(a.Repos, r)
		}
		slices.Sort(a.Repos)
		a.repos, a.counted, a.names, a.coNames = nil, nil, nil, nil
		addrs = append(addrs, a)
	}
	slices.SortFunc(addrs, func(x, y *candAddress) int { return cmp.Compare(x.Email, y.Email) })
	return addrs, len(bots)
}

// candRewritten returns, for each canonical email, the emails git's
// mailmap rewrote to it in fs (RepoFacts.Aliases), each with the names it
// was written with and the repositories where git rewrote it, by email.
func candRewritten(fs *domain.FactSet) map[string][]Alias {
	type seen struct{ names, repos map[string]bool }
	by := map[string]map[string]*seen{} // canonical → written email → what it was written with, and where
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		for _, al := range rf.Aliases {
			if by[al.Canonical] == nil {
				by[al.Canonical] = map[string]*seen{}
			}
			at := by[al.Canonical][al.Email]
			if at == nil {
				at = &seen{names: map[string]bool{}, repos: map[string]bool{}}
				by[al.Canonical][al.Email] = at
			}
			if al.Name != "" {
				at.names[al.Name] = true
			}
			at.repos[rf.Repo.Key] = true
		}
	}
	out := make(map[string][]Alias, len(by))
	for canonical, emails := range by {
		for _, email := range candKeys(emails) {
			at := emails[email]
			al := Alias{Email: email, Repos: candKeys(at.repos)}
			if len(at.names) > 0 {
				al.Names = candKeys(at.names)
			}
			out[canonical] = append(out[canonical], al)
		}
	}
	return out
}

// candForge is the one GitHub account the run's GitHub data attributes an
// address's commits to (logins, forgeAccounts), folded: "" for none or two.
// A GitHub noreply address no commit links is its login's account, by how
// GitHub makes such addresses, when the run's GitHub data links that login
// (linked) to some address: a co-author's noreply address is the commonest
// one, and only a link it is in can tell its candidate high (#103).
func candForge(a *candAddress, logins []string, linked map[string]bool) string {
	switch {
	case len(logins) == 1:
		return logins[0]
	case len(logins) == 0 && a.noreply && linked[foldLogin(a.login)]:
		return foldLogin(a.login)
	}
	return ""
}

// candNameKeys returns the words (candWords) of every name seen with a,
// sorted: its display name, the author names its commits carry and the
// names co-author trailers give it. A default name stays, since Blocks,
// which reads them, joins whatever may be one person.
func candNameKeys(a *candAddress) []string {
	var out []string
	add := func(name string) {
		if key, _ := candWords(name); key != "" {
			out = append(out, key)
		}
	}
	add(a.Name)
	for name := range a.names {
		add(name)
	}
	for name := range a.coNames {
		add(name)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// candPeople calls fn for a commit's author and each distinct co-author:
// the people a commit is evidence about.
func candPeople(author domain.Person, coAuthors []domain.Person, fn func(p domain.Person, email string, authored bool)) {
	self := canonical(author.Email)
	fn(author, self, true)
	for i, co := range coAuthors {
		email := canonical(co.Email)
		dup := func(p domain.Person) bool { return canonical(p.Email) == email }
		if email != self && !slices.ContainsFunc(coAuthors[:i], dup) {
			fn(co, email, false)
		}
	}
}

// candSee records one commit for its author and each distinct co-author:
// the repository and, the first time the commit is seen, the count and the
// author time. It keeps the author's name, which tells whether several
// people commit as one address; a trailer's name is whatever the commit's
// author typed, so it tells nothing of the kind and is kept apart, for
// Blocks alone.
func candSee(address func(email string) *candAddress, repo, sha string, at time.Time,
	author domain.Person, coAuthors []domain.Person) {
	candPeople(author, coAuthors, func(p domain.Person, email string, authored bool) {
		a := address(email)
		if a == nil {
			return
		}
		a.repos[repo] = true
		switch {
		case p.Name == "":
		case authored:
			a.names[p.Name] = true
		default:
			if a.coNames == nil {
				a.coNames = map[string]bool{}
			}
			a.coNames[p.Name] = true
		}
		if a.counted[sha] {
			return
		}
		a.counted[sha] = true
		if authored {
			a.Commits++
		} else {
			a.CoAuthored++
		}
		if !at.IsZero() {
			if a.First.IsZero() || at.Before(a.First) {
				a.First = at
			}
			if at.After(a.Last) {
				a.Last = at
			}
		}
	})
}

// candFiles reads the files of the addresses in need, by email: the paths
// their commits' file changes touched, the source of a rename included, and
// the paths where blame attributes lines to their commits. A merge's file
// changes are its first-parent diff, the work of the commits it brought in,
// so they are no file of the person who merged.
func candFiles(fs *domain.FactSet, need map[string]*candAddress) {
	add := func(a *candAddress, f File) {
		if a.files == nil {
			a.files = map[File]bool{}
		}
		a.files[f] = true
	}
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		repo := rf.Repo.Key
		byCommit := map[string][]*candAddress{} // a non-merge commit's addresses in need
		for j := range rf.Commits {
			c := &rf.Commits[j]
			if len(c.Parents) > 1 {
				continue
			}
			candPeople(c.Author, c.CoAuthors, func(_ domain.Person, email string, _ bool) {
				if a := need[email]; a != nil {
					byCommit[c.SHA] = append(byCommit[c.SHA], a)
				}
			})
		}
		for _, ch := range rf.Changes {
			for _, a := range byCommit[ch.Commit] {
				add(a, File{Repo: repo, Path: ch.Path})
				if ch.OldPath != "" {
					add(a, File{Repo: repo, Path: ch.OldPath})
				}
			}
		}
		for j := range rf.Owners {
			o := &rf.Owners[j]
			candPeople(o.Author, o.CoAuthors, func(_ domain.Person, email string, _ bool) {
				if a := need[email]; a != nil {
					add(a, File{Repo: repo, Path: o.Path})
				}
			})
		}
	}
}

// NameKey returns the key the same-name rule compares (candNameKey,
// docs/spec/identities.md#rules) for email's display name: its words, runs
// of letters, marks and digits, lowercased and joined by one space, so
// case, spacing and punctuation never tell two names apart. It is "" when
// the rule compares the email with nothing: it has no display name or a
// default one ("Your Name", "John Doe"), it is a bot's or an agent's, which
// no candidate holds, or it is a GitHub login's (LoginKey), whose name is
// the account's own. The rule is identity's, and every page that says two
// emails share a name asks it (#69), so a report's unmerged-identities
// caveat names exactly the names Candidates compares, which the identities
// command then suggests lines for, unless it leaves the addresses out as
// several people's (Survey.Shared, Survey.Oversized).
func (d *Directory) NameKey(email string) string {
	email = canonical(email)
	if email == "" || LoginKey(email) || d.Bot(email) {
		return ""
	}
	key, _ := candNameKey(d.names[email])
	return key
}

// candNameKey normalises a display name for the same-name rule (candWords).
// A default name, which a machine or a tutorial wrote, has no key.
func candNameKey(name string) (string, int) {
	key, words := candWords(name)
	if key == "" || candDefaultName(key, words) {
		return "", 0
	}
	return key, len(words)
}

// candWords returns a display name's words (runs of letters, marks and
// digits), lowercased, and those words joined by one space, so case,
// spacing and punctuation never tell two names apart.
func candWords(name string) (string, []string) {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, " "), words
}

// candDefaultName reports whether a normalised name is one that git's
// documentation, an operating system or a tool fills in, which says nothing
// about who committed: a name made only of generic words ("Unknown User",
// "Build User", "Your Name", "Firstname Lastname"), git's own test author
// ("A U Thor"), or the name the Pro Git book's first-time setup types
// ("John Doe").
func candDefaultName(key string, words []string) bool {
	switch key {
	case "a u thor", "john doe", "jane doe":
		return true
	}
	for _, w := range words {
		if !candGenericWord(w) {
			return false
		}
	}
	return true
}

// candGenericWord reports whether a word of a name is one a default name is
// made of rather than a person's.
func candGenericWord(w string) bool {
	switch w {
	case "user", "users", "unknown", "root", "admin", "administrator", "build", "builder", "git", "github",
		"gitlab", "test", "tester", "testing", "default", "local", "localhost", "example", "name", "firstname",
		"lastname", "first", "last", "full", "real", "your", "my", "none", "nobody", "anonymous", "dev",
		"developer", "ubuntu", "vagrant", "ec2", "ci", "deploy", "system", "server", "author", "committer":
		return true
	}
	return false
}

// candLocal reads an address's local part for the local-part rule
// (candLocalPart). The handle is "" for a local part the rule compares with
// nothing: a role or default one, or one shorter than candMinLocal. role
// reports a role or default local part on any domain but GitHub's noreply
// one, where it is a login.
func candLocal(email string) (handle, account, login, domain string, noreply, role bool) {
	var local string
	local, account, login, domain, noreply = candLocalPart(email)
	if !noreply {
		role = candDefaultLocal(local)
	}
	if utf8.RuneCountInString(local) >= candMinLocal && !candDefaultLocal(local) {
		handle = local
	}
	return handle, account, login, domain, noreply, role
}

// candLocalPart reads the local part the local-part rule compares, before
// its vetoes: a GitHub noreply address counts as its login, and its id,
// when the address has one, is the account; on any other domain a +tag is
// dropped.
func candLocalPart(email string) (local, account, login, domain string, noreply bool) {
	local = email
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		local, domain = email[:i], email[i+1:]
	}
	if domain == candNoreply {
		noreply = true
		if id, l, ok := strings.Cut(local, "+"); ok && candDigits(id) {
			account, local = id, l
		}
		login = local
	} else if i := strings.IndexByte(local, '+'); i > 0 {
		local = local[:i]
	}
	return local, account, login, domain, noreply
}

// candDefaultLocal reports whether a local part belongs to a role, a
// mailbox many people read, or a default, rather than to one person.
func candDefaultLocal(local string) bool {
	switch local {
	case "admin", "administrator", "root", "user", "info", "contact", "support", "hello", "mail",
		"noreply", "no-reply", "git", "github", "gitlab", "dev", "developer", "developers", "team",
		"test", "build", "builder", "deploy", "jenkins", "bot", "ubuntu", "ec2-user", "vagrant",
		"nobody", "unknown", "none", "anonymous", "email", "you", "your.name", "yourname", "office",
		"web", "www", "webmaster", "postmaster", "security", "devops", "ops":
		return true
	}
	return false
}

func candDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// candSelfNamed reports whether a display name is only the address's own
// local part, as GitHub names a commit by the account's login when it has
// no profile name: the whole local part, the part before a +tag, or a
// noreply address's login, ignoring case. Such a name says nothing about
// who the person is, and gives the address away.
func candSelfNamed(name, email string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	return slices.ContainsFunc(localForms(email), func(f string) bool { return strings.EqualFold(name, f) })
}

// candMachine reports whether a domain is one git makes up from a
// machine's name when no address is configured: one without a dot, or a
// local network's.
func candMachine(d string) bool {
	if d == "" {
		return false
	}
	if !strings.Contains(d, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".localdomain", ".lan", ".home", ".internal", ".(none)"} {
		if strings.HasSuffix(d, suffix) {
			return true
		}
	}
	return false
}

// candMainDomain is the domain most of the addresses use, GitHub's noreply
// domain and machine domains aside: the organisation's convention. A tie
// has none.
func candMainDomain(addrs []*candAddress) string {
	count := map[string]int{}
	for _, a := range addrs {
		if a.domain != "" && !a.noreply && !candMachine(a.domain) {
			count[a.domain]++
		}
	}
	best, most, tie := "", 0, false
	for _, d := range candKeys(count) {
		switch n := count[d]; {
		case n > most:
			best, most, tie = d, n, false
		case n == most:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return best
}

// candDomainKind is what an address's domain says about it, given the
// scope's main domain.
func candDomainKind(a *candAddress, mainDomain string) DomainKind {
	switch {
	case a.noreply:
		return DomainNoreply
	case candMachine(a.domain):
		return DomainMachine
	case a.domain != "" && a.domain == mainDomain:
		return DomainMain
	}
	return DomainOther
}

// candGroup is one rule's key and the addresses that share it.
type candGroup struct {
	rule    Rule
	members []int // indexes into the addresses, ascending
}

// candComponent is a set of addresses the rules link, with the groups that
// link them.
type candComponent struct {
	addrs       []*candAddress
	groups      []candGroup
	members     []int // indexes into the addresses, ascending
	validName   map[string]bool
	validHandle map[string]bool
	validForge  map[string]bool // the accounts whose same-forge-account group is evidence
}

// candComponents groups the addresses by each rule's key, keeps the groups
// that are evidence, and joins groups that share an address into
// components: one candidate each. A shared address is in no group but its
// account's (below). A group
// is no evidence when more than candMaxShare addresses share its key, or
// when it holds two GitHub accounts (GitHub's terms allow one person one
// free personal account), or, for a local part, when a shared address uses
// it or its addresses carry different names: then the key is evidently
// several people's. A component of more than candMaxShare addresses is
// several people's chained together, so it is left out, and counted. It
// returns the components, the addresses the groups link (those left out
// included), and the number of components left out.
//
// The same-forge-account rule's groups are GitHub's own links (#103):
// GitHub attributes every commit of an address to the account that
// verified it, so an address whose commits carry names that differ, which
// the other rules leave out as several people's, is in its account's
// group; a role address stays out of it, and so does an account more than
// candMaxShare addresses link to. The rule adds links and vetoes none:
// most commits reach GitHub by direct push or are older than the run's
// GitHub data, so an address without a link says nothing.
func candComponents(addrs []*candAddress) ([]*candComponent, map[int]bool, int) {
	byName, byHandle, byAccount := map[string][]int{}, map[string][]int{}, map[string][]int{}
	byForge := map[string][]int{}
	for i, a := range addrs {
		if a.key != "" {
			byName[a.key] = append(byName[a.key], i)
		}
		if a.handle != "" {
			byHandle[a.handle] = append(byHandle[a.handle], i)
		}
		if a.account != "" {
			byAccount[a.account] = append(byAccount[a.account], i)
		}
		if a.forge != "" && !a.role {
			byForge[a.forge] = append(byForge[a.forge], i)
		}
	}
	// people is the addresses a group links: those that are not shared,
	// when two or more are. A shared address's name is still one of its
	// people's names, so the name stays evidence and the address drops out.
	people := func(idx []int) []int {
		var out []int
		for _, i := range idx {
			if !addrs[i].shared {
				out = append(out, i)
			}
		}
		if len(out) < 2 {
			return nil
		}
		return out
	}
	anyShared := func(idx []int) bool { return slices.ContainsFunc(idx, func(i int) bool { return addrs[i].shared }) }
	validName, validHandle := map[string]bool{}, map[string]bool{}
	var groups []candGroup
	for _, key := range candKeys(byName) {
		idx := byName[key]
		if len(idx) > candMaxShare || candAccounts(addrs, idx) > 1 {
			continue
		}
		if m := people(idx); m != nil {
			validName[key] = true
			groups = append(groups, candGroup{rule: RuleSameName, members: m})
		}
	}
	for _, key := range candKeys(byHandle) {
		// A local part a shared address uses is evidently several people's.
		idx := byHandle[key]
		if len(idx) < 2 || len(idx) > candMaxShare || anyShared(idx) || candAccounts(addrs, idx) > 1 ||
			!candNamesAgree(addrs, idx) {
			continue
		}
		validHandle[key] = true
		groups = append(groups, candGroup{rule: RuleSameLocalPart, members: idx})
	}
	for _, key := range candKeys(byAccount) {
		if m := people(byAccount[key]); m != nil {
			groups = append(groups, candGroup{rule: RuleSameAccount, members: m})
		}
	}
	validForge := map[string]bool{}
	for _, key := range candKeys(byForge) {
		// Two GitHub account ids are two accounts, whatever login the
		// replica holds for them: a login renamed and taken again, or rows
		// synced before and after (#103).
		if idx := byForge[key]; len(idx) >= 2 && len(idx) <= candMaxShare && candIDs(addrs, idx) <= 1 {
			validForge[key] = true
			groups = append(groups, candGroup{rule: RuleSameForgeAccount, members: idx})
		}
	}
	parent := make([]int, len(addrs))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for _, g := range groups {
		for _, m := range g.members[1:] {
			if a, b := find(g.members[0]), find(m); a != b {
				parent[max(a, b)] = min(a, b)
			}
		}
	}
	byRoot := map[int]*candComponent{}
	var comps []*candComponent
	linked := map[int]bool{}
	for _, g := range groups {
		root := find(g.members[0])
		c := byRoot[root]
		if c == nil {
			c = &candComponent{addrs: addrs, validName: validName, validHandle: validHandle, validForge: validForge}
			byRoot[root] = c
			comps = append(comps, c)
		}
		c.groups = append(c.groups, g)
		for _, m := range g.members {
			if !linked[m] {
				linked[m] = true
				c.members = append(c.members, m)
			}
		}
	}
	var out []*candComponent
	oversized := 0
	for _, c := range comps {
		if len(c.members) > candMaxShare {
			oversized++
			continue
		}
		slices.Sort(c.members)
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y *candComponent) int { return cmp.Compare(x.members[0], y.members[0]) })
	return out, linked, oversized
}

// candKeys returns a map's keys, sorted.
func candKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// candAccounts counts the distinct GitHub accounts among the addresses. An
// id-form noreply address's account is its id; an old-form one
// (login@users.noreply.github.com, which accounts made before 18 July 2017
// may still use) names its account by its login, which is the account of an
// id-form address of the same login when the group holds one.
func candAccounts(addrs []*candAddress, idx []int) int {
	var ids, idLogins, logins []string
	for _, i := range idx {
		if a := addrs[i]; a.account != "" {
			if !slices.Contains(ids, a.account) {
				ids = append(ids, a.account)
			}
			idLogins = append(idLogins, strings.ToLower(a.login))
		}
	}
	for _, i := range idx {
		a := addrs[i]
		if login := strings.ToLower(a.login); a.noreply && a.account == "" && login != "" &&
			!slices.Contains(idLogins, login) && !slices.Contains(logins, login) {
			logins = append(logins, login)
		}
	}
	return len(ids) + len(logins)
}

// candIDs counts the distinct GitHub account ids among the addresses: an
// id-form noreply address's id. Unlike a login, an id never changes hands.
func candIDs(addrs []*candAddress, idx []int) int {
	var ids []string
	for _, i := range idx {
		if a := addrs[i].account; a != "" && !slices.Contains(ids, a) {
			ids = append(ids, a)
		}
	}
	return len(ids)
}

// candNamesAgree reports whether no two of the addresses carry different
// names (candAgree). A name that is only the address's own local part says
// nothing about who it is, so "jdoe" on jdoe@ agrees with any name.
func candNamesAgree(addrs []*candAddress, idx []int) bool {
	for i, x := range idx {
		for _, y := range idx[i+1:] {
			a, b := addrs[x], addrs[y]
			if a.key == "" || b.key == "" || a.selfNamed || b.selfNamed {
				continue
			}
			if !candAgree(a.key, b.key) {
				return false
			}
		}
	}
	return true
}

// candShownNames is the author names an address's commits carry, sorted,
// less those that give an address of the run away (Directory.withholds),
// which it counts: the names a page may print beside the address.
func candShownNames(names map[string]bool, who *Directory) ([]string, int) {
	var out []string
	withheld := 0
	for name := range names {
		switch {
		case strings.TrimSpace(name) == "":
		case who.withholds(name):
			withheld++
		default:
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, withheld
}

// candOwnNamesAgree reports whether the names an address's commits carry
// can all be one person's, by the rule candNamesAgree applies across
// addresses; a default name, the address's own local part, or a GitHub
// login the run's GitHub data links the address to (logins), agrees with
// any: GitHub names a commit by its account's login when the account has
// no profile name, and people set user.name to it, so "jdoe" beside "Jane
// Doe" on an address jdoe's account verified is one person. An address
// whose names differ is several people's mailbox.
func candOwnNamesAgree(a *candAddress, logins []string) bool {
	var keys []string
	for name := range a.names {
		if candSelfNamed(name, a.Email) ||
			slices.ContainsFunc(logins, func(l string) bool { return strings.EqualFold(strings.TrimSpace(name), l) }) {
			continue
		}
		key, _ := candNameKey(name)
		if key == "" || slices.Contains(keys, key) {
			continue
		}
		for _, k := range keys {
			if !candAgree(k, key) {
				return false
			}
		}
		keys = append(keys, key)
	}
	return true
}

// candAgree reports whether two normalised names can be one person's: one's
// words are all among the other's, so "ada" and "ada example" agree, and
// "ada example" and "ada lovelace" do not.
func candAgree(x, y string) bool {
	if x == y {
		return true
	}
	wx, wy := strings.Fields(x), strings.Fields(y)
	return candSubset(wx, wy) || candSubset(wy, wx)
}

func candSubset(a, b []string) bool {
	for _, w := range a {
		if !slices.Contains(b, w) {
			return false
		}
	}
	return true
}

// candDerived reports whether a local part is made from a name's words, as
// an organisation's convention or a person's habit makes one: some of the
// words, in the name's order or reversed, each whole or as its initial
// (ada, adaexample, ada.example, aexample, kim.alex), whatever the
// separators, and with any number after them (alex.kim2). Such a local
// part says no more than the name does: two people of that name would both
// have it. A letter outside ASCII matches itself or any run of ASCII
// letters, so José matches jose and a transliterated name its spelling:
// when in doubt, the local part is the name's.
func candDerived(local, nameKey string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(local) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	s := strings.TrimRightFunc(b.String(), unicode.IsDigit)
	words := strings.Fields(nameKey)
	if s == "" || len(words) == 0 {
		return false
	}
	reversed := slices.Clone(words)
	slices.Reverse(reversed)
	return candSpells(s, words) || candSpells(s, reversed)
}

// candSpells reports whether s is made of some of words, in their order,
// each whole or as its first letter, and at least one of them.
func candSpells(s string, words []string) bool {
	// at[p] holds 1 when s[:p] is spelt by none of the words seen so far,
	// and 2 when by at least one: positions s can be cut at.
	at := make([]uint8, len(s)+1)
	at[0] = 1
	for _, w := range words {
		next := slices.Clone(at) // the word left out
		for p, how := range at {
			if how == 0 {
				continue
			}
			for _, q := range candWordEnds(s, p, w) {
				next[q] |= 2
			}
			for _, q := range candWordEnds(s, p, candFirst(w)) {
				next[q] |= 2
			}
		}
		at = next
	}
	return at[len(s)]&2 != 0
}

// candFirst is a word's first letter.
func candFirst(w string) string {
	_, n := utf8.DecodeRuneInString(w)
	return w[:n]
}

// candWordEnds returns where s can be cut after spelling w from position
// from: an ASCII character matches itself; a letter outside ASCII matches
// itself or a run of ASCII letters, as a transliteration does. A spelling
// that uses no character of s does not count.
func candWordEnds(s string, from int, w string) []int {
	reach := []int{from}
	for _, r := range w {
		var next []int
		for _, p := range reach {
			if r < utf8.RuneSelf {
				if p < len(s) && rune(s[p]) == r {
					next = append(next, p+1)
				}
				continue
			}
			if strings.HasPrefix(s[p:], string(r)) {
				next = append(next, p+utf8.RuneLen(r))
			}
			for q := p; ; q++ {
				next = append(next, q)
				if q >= len(s) || s[q] >= utf8.RuneSelf || !unicode.IsLetter(rune(s[q])) {
					break
				}
			}
		}
		slices.Sort(next)
		reach = slices.Compact(next)
		if len(reach) == 0 {
			return nil
		}
	}
	return slices.DeleteFunc(reach, func(p int) bool { return p == from })
}

// The strength of the direct evidence between two addresses.
const (
	candNone = iota // linked only through a third address
	candLow
	candMedium
	candHigh
)

// strength is the direct evidence that two addresses of a component are one
// person. Evidence the display name does not explain is high: GitHub's
// account id, GitHub's attribution of both addresses' commits to one
// account, or a full name with the same local part when that local part
// is not made from the name's words. A full name alone is medium, and so is
// a full name with a local part made from it, which two people of that name
// would both have: counting it as a second rule would count one signal
// twice, where record linkage assumes the fields it compares are
// independent (Fellegi and Sunter, 1969). GitHub's attribution is high for
// an address whose commits carry names that differ too (wave 10): the
// account verified the address, so GitHub's own pages give it every commit
// on it, and a person who changed their name, the commonest reason names
// differ on one address, was split in three by a medium link and their
// lines counted as gone; a role address (team@, build@), which several
// people read, is in no forge group (candComponents). A one-word name or a
// local part alone is low. Two GitHub account ids are two people, however
// the rest of the component links them.
func (c *candComponent) strength(a, b *candAddress) int {
	if a.account != "" && b.account != "" && a.account != b.account {
		return candNone
	}
	if a.account != "" && a.account == b.account {
		return candHigh
	}
	if a.forge != "" && a.forge == b.forge && c.validForge[a.forge] {
		return candHigh
	}
	name := a.key != "" && a.key == b.key && c.validName[a.key]
	handle := a.handle != "" && a.handle == b.handle && c.validHandle[a.handle]
	switch {
	case name && a.words >= 2 && handle && !candDerived(a.handle, a.key):
		return candHigh
	case name && a.words >= 2:
		return candMedium
	case name || handle:
		return candLow
	}
	return candNone
}

// candidate turns a component into a candidate. Its confidence is its
// weakest pair's: a group is only as sure as the two addresses in it that
// have the least evidence between them, so addresses linked only through a
// third make the candidate low, however strong each link is.
func (c *candComponent) candidate(domains map[string]int) Candidate {
	members := make([]*candAddress, len(c.members))
	for i, m := range c.members {
		members[i] = c.addrs[m]
	}
	slices.SortFunc(members, candByFirst)
	at := map[*candAddress]int{}
	for i, m := range members {
		at[m] = i
	}
	weakest := candHigh
	high := make([][]bool, len(members)) // high[i][j]: the pair's evidence is high
	for i := range members {
		high[i] = make([]bool, len(members))
	}
	for i, a := range members {
		for j := i + 1; j < len(members); j++ {
			s := c.strength(a, members[j])
			weakest = min(weakest, s)
			high[i][j], high[j][i] = s == candHigh, s == candHigh
		}
	}
	out := Candidate{Chained: weakest == candNone}
	switch weakest {
	case candHigh:
		out.Confidence = ConfidenceHigh
	case candMedium:
		out.Confidence = ConfidenceMedium
	default:
		out.Confidence = ConfidenceLow
	}
	for _, g := range c.groups {
		l := Link{Rule: g.rule}
		for _, m := range g.members {
			l.Addresses = append(l.Addresses, at[c.addrs[m]])
		}
		slices.Sort(l.Addresses)
		switch g.rule {
		case RuleSameName:
			l.Name = members[l.Addresses[0]].Name
		case RuleSameForgeAccount:
			l.Login = c.addrs[g.members[0]].forge // folded; candSurvey gives it GitHub's case
		case RuleSameLocalPart:
			handle := c.addrs[g.members[0]].handle
			l.FromName = slices.ContainsFunc(g.members, func(m int) bool {
				a := c.addrs[m]
				return a.key != "" && !a.selfNamed && candDerived(handle, a.key)
			})
		}
		out.Links = append(out.Links, l)
	}
	candSortLinks(out.Links)
	// The canonical address is the preferred one of the addresses high
	// evidence links, so the lines that rest on that evidence map to it.
	prefer := candPreferred(domains, false)
	core := candCore(members, high, prefer)
	canon := members[core[0]]
	for _, i := range core[1:] {
		if prefer(members[i], canon) < 0 {
			canon = members[i]
		}
	}
	if len(core) >= 2 {
		out.High = core
	}
	out.Canonical, out.Name = canon.Email, candName(canon, members)
	out.Addresses = candPublic(members)
	return out
}

// candCore returns the indexes into members, ascending, of the largest set
// of addresses whose every pair high marks as high evidence: of sets that
// large, the one whose addresses come first in prefer's order; and, when no
// pair is high, the most preferred address alone. A candidate holds at most
// candMaxShare addresses, so trying every set (2^10 at most) is cheap and
// finds the largest one exactly, where a greedy pass could keep a pair and
// miss a larger set without it.
func candCore(members []*candAddress, high [][]bool, prefer func(a, b *candAddress) int) []int {
	order := make([]int, len(members)) // members' indexes, most preferred first
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(i, j int) int { return prefer(members[i], members[j]) })
	rank := make([]int, len(members))
	for r, i := range order {
		rank[i] = r
	}
	ranks := func(set []int) []int {
		out := make([]int, len(set))
		for k, i := range set {
			out[k] = rank[i]
		}
		slices.Sort(out)
		return out
	}
	best := []int{order[0]}
	for mask := 1; mask < 1<<len(members); mask++ {
		var set []int
		for i := range members {
			if mask&(1<<i) != 0 {
				set = append(set, i)
			}
		}
		if len(set) < 2 || len(set) < len(best) {
			continue
		}
		clique := true
		for k, i := range set {
			for _, j := range set[k+1:] {
				clique = clique && high[i][j]
			}
		}
		if clique && (len(set) > len(best) || slices.Compare(ranks(set), ranks(best)) < 0) {
			best = set
		}
	}
	slices.Sort(best)
	return best
}

// candByFirst orders addresses by first activity, then email.
func candByFirst(x, y *candAddress) int {
	return cmp.Or(x.First.Compare(y.First), cmp.Compare(x.Email, y.Email))
}

// candByLast orders addresses by last activity, then email.
func candByLast(x, y *candAddress) int {
	return cmp.Or(x.Last.Compare(y.Last), cmp.Compare(x.Email, y.Email))
}

// candSortLinks orders links by the order of Rules, then by their first
// address.
func candSortLinks(links []Link) {
	order := map[Rule]int{}
	for i, def := range Rules() {
		order[def.Rule] = i
	}
	slices.SortFunc(links, func(x, y Link) int {
		return cmp.Or(cmp.Compare(order[x.Rule], order[y.Rule]), slices.Compare(x.Addresses, y.Addresses))
	})
}

// candPreferred orders the addresses of a candidate from the one most
// worth suggesting as canonical: not a GitHub noreply address, which is an
// alias by design; then, for a hand-off, the newest, which the person moved
// to; then the one at the domain most of the scope's people use, which is
// the organisation's convention; then the most commits, the most
// co-authored, the latest activity, and the smallest email. A shared
// address is in a candidate only through the same-forge-account rule, and
// comes after every other: several people's mailbox is no one's canonical
// address.
func candPreferred(domains map[string]int, newest bool) func(a, b *candAddress) int {
	return func(a, b *candAddress) int {
		if a.noreply != b.noreply {
			return candFalseFirst(a.noreply, b.noreply)
		}
		if a.shared != b.shared {
			return candFalseFirst(a.shared, b.shared)
		}
		if newest {
			if c := b.Last.Compare(a.Last); c != 0 {
				return c
			}
		}
		return cmp.Or(
			cmp.Compare(domains[b.domain], domains[a.domain]),
			cmp.Compare(b.Commits, a.Commits),
			cmp.Compare(b.CoAuthored, a.CoAuthored),
			b.Last.Compare(a.Last),
			cmp.Compare(a.Email, b.Email),
		)
	}
}

// candName is the display name suggested for a candidate: the canonical
// address's when it has two words or more, else the name of more words
// another address carries (an address whose name is a login or a default
// gets the person's full name), else the canonical address's own. A default
// name, a shared address's, or one that gives away an address of the
// candidate (RevealsAddress), is never suggested: with no other name, it is
// "", and the line keeps each commit's own name.
func candName(canon *candAddress, members []*candAddress) string {
	// A shared address's name is one of several people's, so it names no one.
	usable := func(a *candAddress) bool {
		return a.key != "" && !a.shared &&
			!slices.ContainsFunc(members, func(m *candAddress) bool { return RevealsAddress(a.Name, m.Email) })
	}
	own := 0
	if usable(canon) {
		own = canon.words
	}
	if own >= 2 {
		return canon.Name
	}
	// fuller orders names of more words first, then the address with more
	// commits, the later activity and the smaller name.
	fuller := func(a, b *candAddress) int {
		return cmp.Or(cmp.Compare(b.words, a.words), cmp.Compare(b.Commits, a.Commits),
			b.Last.Compare(a.Last), cmp.Compare(a.Name, b.Name))
	}
	var named []*candAddress
	for _, m := range members {
		if usable(m) {
			named = append(named, m)
		}
	}
	if len(named) > 0 {
		if best := slices.MinFunc(named, fuller); best.words > own {
			return best.Name
		}
	}
	if own > 0 {
		return canon.Name
	}
	return ""
}

// candPublic copies the evidence of the addresses, in order.
func candPublic(members []*candAddress) []Address {
	out := make([]Address, len(members))
	for i, m := range members {
		out[i] = m.Address
		out[i].NamesDiffer = m.shared && !m.role
	}
	return out
}

// candEdge is one hand-off.
type candEdge struct {
	from, to *candAddress
	handOff  HandOff
}

// candHandOffs finds the hand-offs among addresses no other rule links: an
// address whose last commit came at most candHandOffGap before another's
// first, in a file of the same repository, and which then stayed quiet for
// candHandOffQuiet before now, the run's as-of time. An address with two
// possible successors, or two possible predecessors, is ambiguous and gets
// none, so what is left are chains, each in time order: an address, the one
// after it, and so on.
//
// It never lists the pairs the times allow, which a burst of new addresses
// (an organisation moving everyone to a new domain) makes quadratic in time
// and memory. The times alone pick, by binary search, the addresses whose
// files it reads. An index from each file to those addresses, in time
// order, then finds an address's successors in a file by binary search, and
// the search stops at a second one, which settles that the address is
// ambiguous; predecessors are found the same way. The shared files are
// listed only for the hand-offs that survive, each address in two at most,
// so the work is the facts' size times a logarithm.
func candHandOffs(fs *domain.FactSet, singles []*candAddress) [][]candEdge {
	if fs.Now.IsZero() || len(singles) < 2 {
		return nil
	}
	quiet := fs.Now.Add(-candHandOffQuiet)
	starts := slices.Clone(singles)
	slices.SortFunc(starts, candByFirst)
	var stops []*candAddress // the addresses that stopped long enough ago
	for _, a := range singles {
		if !a.Last.IsZero() && a.Last.Before(quiet) {
			stops = append(stops, a)
		}
	}
	if len(stops) == 0 {
		return nil
	}
	slices.SortFunc(stops, candByLast)
	// The addresses the times allow in a hand-off, before any file is read:
	// a quiet address with one that began within the gap after its last
	// commit, and an address that began within the gap after a quiet one's.
	need := map[string]*candAddress{}
	for _, a := range stops {
		if i := candFirstAfter(starts, a.Last); i < len(starts) && !starts[i].First.After(a.Last.Add(candHandOffGap)) {
			need[a.Email] = a
		}
	}
	for _, b := range starts {
		if i := candLastFrom(stops, b.First.Add(-candHandOffGap)); i < len(stops) && stops[i].Last.Before(b.First) {
			need[b.Email] = b
		}
	}
	if len(need) == 0 {
		return nil
	}
	candFiles(fs, need)
	// Each file's addresses in need by first activity, and its quiet ones
	// by last activity: the order of starts and stops.
	after, before := map[File][]*candAddress{}, map[File][]*candAddress{}
	for _, a := range starts {
		for f := range a.files {
			after[f] = append(after[f], a)
		}
	}
	for _, a := range stops {
		for f := range a.files {
			before[f] = append(before[f], a)
		}
	}
	next, hasPrev := map[*candAddress]candEdge{}, map[*candAddress]bool{}
	for _, a := range stops {
		b := candSuccessor(a, after)
		if b == nil || candPredecessor(b, before) != a {
			continue
		}
		n, files := candShared(a, b)
		next[a] = candEdge{from: a, to: b, handOff: HandOff{Gap: b.First.Sub(a.Last), SharedFiles: n, Files: files}}
		hasPrev[b] = true
	}
	var chains [][]candEdge
	for _, a := range starts {
		if _, ok := next[a]; !ok || hasPrev[a] {
			continue
		}
		var chain []candEdge
		for e, ok := next[a]; ok; e, ok = next[e.to] {
			chain = append(chain, e)
		}
		chains = append(chains, chain)
	}
	return chains
}

// candSuccessor is the only address that began within the gap after a's
// last commit in a file a touched, or nil when there is none or more than
// one. after holds each file's addresses by first activity.
func candSuccessor(a *candAddress, after map[File][]*candAddress) *candAddress {
	limit := a.Last.Add(candHandOffGap)
	var only *candAddress
	for f := range a.files {
		list := after[f]
		for i := candFirstAfter(list, a.Last); i < len(list) && !list[i].First.After(limit); i++ {
			switch b := list[i]; {
			case only == nil:
				only = b
			case b != only:
				return nil
			}
		}
	}
	return only
}

// candPredecessor is the only quiet address whose last commit came within
// the gap before b's first in a file b touched, or nil when there is none
// or more than one. before holds each file's quiet addresses by last
// activity.
func candPredecessor(b *candAddress, before map[File][]*candAddress) *candAddress {
	from := b.First.Add(-candHandOffGap)
	var only *candAddress
	for f := range b.files {
		list := before[f]
		for i := candLastFrom(list, from); i < len(list) && list[i].Last.Before(b.First); i++ {
			switch a := list[i]; {
			case only == nil:
				only = a
			case a != only:
				return nil
			}
		}
	}
	return only
}

// candFirstAfter is the index of the first address in list, sorted by
// first activity, that began after t.
func candFirstAfter(list []*candAddress, t time.Time) int {
	return sort.Search(len(list), func(i int) bool { return list[i].First.After(t) })
}

// candLastFrom is the index of the first address in list, sorted by last
// activity, whose last commit came at t or later.
func candLastFrom(list []*candAddress, t time.Time) int {
	return sort.Search(len(list), func(i int) bool { return !list[i].Last.Before(t) })
}

// candShared counts the files two addresses both touched or hold lines in,
// in the same repository, and lists the first candMaxFiles of them in a
// slice of their own, so the evidence holds no more than it shows.
func candShared(a, b *candAddress) (int, []File) {
	small, large := a.files, b.files
	if len(large) < len(small) {
		small, large = large, small
	}
	var shared []File
	for f := range small {
		if large[f] {
			shared = append(shared, f)
		}
	}
	slices.SortFunc(shared, func(x, y File) int { return cmp.Or(cmp.Compare(x.Repo, y.Repo), cmp.Compare(x.Path, y.Path)) })
	n := len(shared)
	if n > candMaxFiles {
		shared = slices.Clone(shared[:candMaxFiles])
	}
	return n, shared
}

// candChainCandidate turns a hand-off chain into a candidate: low, a guess,
// with one link per step and the newest address suggested as canonical.
func candChainCandidate(chain []candEdge, domains map[string]int) Candidate {
	members := []*candAddress{chain[0].from}
	out := Candidate{Confidence: ConfidenceLow, Guess: true}
	for i, e := range chain {
		members = append(members, e.to)
		ho := e.handOff
		out.Links = append(out.Links, Link{Rule: RuleHandOff, Addresses: []int{i, i + 1}, HandOff: &ho})
	}
	canon := slices.MinFunc(members, candPreferred(domains, true))
	out.Canonical, out.Name = canon.Email, candName(canon, members)
	out.Addresses = candPublic(members)
	return out
}

// candRank orders confidences, most sure first.
func candRank(c Confidence) int {
	switch c {
	case ConfidenceHigh:
		return 0
	case ConfidenceMedium:
		return 1
	}
	return 2
}

// candFalseFirst orders false before true.
func candFalseFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}
