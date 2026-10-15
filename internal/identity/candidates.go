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
// person; addresses linked only through a third are never more than a weak
// match; and a hand-off is always a guess.

// Rule is one way two addresses can be linked.
type Rule string

// The rules, in the order Rules lists their definitions.
const (
	RuleSameName      Rule = "same-name"           // the same normalised display name
	RuleSameLocalPart Rule = "same-local-part"     // the same local part on different domains
	RuleSameAccount   Rule = "same-github-account" // GitHub noreply addresses of one account id
	RuleHandOff       Rule = "hand-off"            // one address stopped as another started, in the same files: a guess
)

// Confidence is how sure a candidate is that its addresses are one person.
type Confidence string

// The confidences, most sure first.
const (
	ConfidenceHigh   Confidence = "high"   // two rules agree, or GitHub's own account id does
	ConfidenceMedium Confidence = "medium" // one full name (two words or more) on several addresses
	ConfidenceLow    Confidence = "low"    // one weak signal, a chain through a third address, or a hand-off
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
	candMaxShare     = 10                  // a name or local part more addresses share is a default, not a person's
	candMinLocal     = 3                   // shorter local parts (initials) are compared with nothing
	candMaxFiles     = 5                   // shared files a hand-off lists; it counts them all
	candNoreply      = "users.noreply.github.com"
)

// Rules returns the definition of every rule, in a fixed order. Every
// answer carries them, so whoever reads a candidate also reads how it can
// be wrong.
func Rules() []RuleDefinition {
	return []RuleDefinition{
		{
			Rule: RuleSameName,
			Matches: "Addresses whose display names are the same once case, spacing and punctuation are ignored. " +
				"A default name (root, Your Name, unknown and the like) matches nothing, nor does a name that more " +
				"than 10 addresses share or that two GitHub accounts use.",
			Confidence: "Medium for a name of two words or more, low for one word, high when the local parts match too.",
			Risk: "Different people share names, the more often the shorter and commoner the name, so two people " +
				"with one common name would become one: a name alone is never high. A name written without spaces, as " +
				"Chinese, Japanese and Korean names often are, counts as one word and stays low, and a name spelt " +
				"two ways (José and Jose, Bob and Robert) is missed.",
		},
		{
			Rule: RuleSameLocalPart,
			Matches: "Addresses with the same local part (before the @) on different domains, or that differ only " +
				"by a +tag. A GitHub noreply address counts as its login: 12345+login@users.noreply.github.com is " +
				"login. Role and default local parts (admin, dev, info, root and the like), local parts shorter " +
				"than 3 characters, one that more than 10 addresses share, and one that people with different " +
				"names or two GitHub accounts use match nothing.",
			Confidence: "Low on its own, high with the same display name.",
			Risk: "A first name or a common login as a local part (alex@, sam@) belongs to many people at different " +
				"domains, and git makes up user@host addresses from a machine's login name, which several people " +
				"can share.",
		},
		{
			Rule:       RuleSameAccount,
			Matches:    "GitHub noreply addresses with the same account id, such as 12345+old@ and 12345+new@users.noreply.github.com after a rename.",
			Confidence: "High: the id is GitHub's own, and a rename keeps it.",
			Risk:       "A forged address: anyone can write any address into a commit, so a commit is no proof of the account.",
		},
		{
			Rule: RuleHandOff,
			Matches: "One address's last commit at most 30 days before another's first, both in a file of the same " +
				"repository, when the first address has no commit in the 90 days before the run's as-of time. Only " +
				"addresses no other rule links are compared, and an address with two possible successors, or two " +
				"possible predecessors, gets none.",
			Confidence: "Always low, and a guess: the mailmap form writes its line commented out.",
			Risk: "A teammate who takes over someone's code when they leave looks exactly like one person changing " +
				"address. Only someone who knows them can tell.",
		},
	}
}

// Survey is what Candidates found in a fact set.
type Survey struct {
	Candidates []Candidate // most confident first, then by suggested name and first address
	Addresses  int         // human addresses the facts show: the population the candidates come from
	Bots       int         // bot and agent addresses (Directory.Bot), which never appear in a candidate
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
	Name       string    // its display name (Directory.Name); "" when the facts show none
	Commits    int       // commits it authored, each counted once
	CoAuthored int       // commits whose Co-authored-by trailers name it
	First      time.Time // the earliest author time of those commits
	Last       time.Time // the latest
	Repos      []string  // Repo.Key of every repository it appears in, sorted
}

// Link is one rule's evidence in a candidate: the addresses it links.
type Link struct {
	Rule      Rule
	Addresses []int    // indexes into Candidate.Addresses, ascending
	Name      string   // RuleSameName: the shared display name, as the first of those addresses shows it
	HandOff   *HandOff // RuleHandOff: from Addresses[0] to Addresses[1]; nil for the other rules
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
	s := &Survey{}
	if fs == nil {
		return s
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
	comps, linked := candComponents(addrs)
	for _, comp := range comps {
		s.Candidates = append(s.Candidates, comp.candidate(domains))
	}
	var singles []*candAddress
	for i, a := range addrs {
		if !linked[i] {
			singles = append(singles, a)
		}
	}
	for _, chain := range candHandOffs(fs, singles) {
		s.Candidates = append(s.Candidates, candChainCandidate(chain, domains))
	}
	slices.SortFunc(s.Candidates, func(a, b Candidate) int {
		return cmp.Or(
			cmp.Compare(candRank(a.Confidence), candRank(b.Confidence)),
			candFalseFirst(a.Guess, b.Guess),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Addresses[0].Email, b.Addresses[0].Email),
		)
	})
	return s
}

// candAddress is one human address while the rules compare it.
type candAddress struct {
	Address
	key     string          // candNameKey of the display name: "" for none or a default name
	words   int             // the words in key
	login   bool            // the display name is the address's own local part, as GitHub names a commit by a login without a profile name
	handle  string          // the local part the local-part rule compares; "" when it compares none
	account string          // a GitHub noreply address's account id; "" for every other address
	domain  string          // after the last @; "" for an address without one
	noreply bool            // a GitHub noreply address
	files   map[File]bool   // what it touched or holds lines in; collected only for a possible hand-off (candFiles)
	repos   map[string]bool // while collecting
	counted map[string]bool // while collecting: commit SHAs already counted, so a commit two repositories share counts once
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
		a := &candAddress{Address: Address{Email: email}, repos: map[string]bool{}, counted: map[string]bool{}}
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
	addrs := make([]*candAddress, 0, len(byEmail))
	for _, a := range byEmail {
		a.Name = who.Name(a.Email)
		a.key, a.words = candNameKey(a.Name)
		a.handle, a.account, a.domain, a.noreply = candLocal(a.Email)
		if a.handle != "" {
			local, _ := candNameKey(a.handle)
			a.login = a.key != "" && a.key == local
		}
		for r := range a.repos {
			a.Repos = append(a.Repos, r)
		}
		slices.Sort(a.Repos)
		a.repos, a.counted = nil, nil
		addrs = append(addrs, a)
	}
	slices.SortFunc(addrs, func(x, y *candAddress) int { return cmp.Compare(x.Email, y.Email) })
	return addrs, len(bots)
}

// candPeople calls fn for a commit's author and each distinct co-author:
// the people a commit is evidence about.
func candPeople(author domain.Person, coAuthors []domain.Person, fn func(email string, authored bool)) {
	self := canonical(author.Email)
	fn(self, true)
	for i, co := range coAuthors {
		email := canonical(co.Email)
		dup := func(p domain.Person) bool { return canonical(p.Email) == email }
		if email != self && !slices.ContainsFunc(coAuthors[:i], dup) {
			fn(email, false)
		}
	}
}

// candSee records one commit for its author and each distinct co-author:
// the repository and, the first time the commit is seen, the count and the
// author time.
func candSee(address func(email string) *candAddress, repo, sha string, at time.Time,
	author domain.Person, coAuthors []domain.Person) {
	candPeople(author, coAuthors, func(email string, authored bool) {
		a := address(email)
		if a == nil {
			return
		}
		a.repos[repo] = true
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
			candPeople(c.Author, c.CoAuthors, func(email string, _ bool) {
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
			candPeople(o.Author, o.CoAuthors, func(email string, _ bool) {
				if a := need[email]; a != nil {
					add(a, File{Repo: repo, Path: o.Path})
				}
			})
		}
	}
}

// candNameKey normalises a display name for the same-name rule: its words
// (runs of letters, marks and digits) lowercased and joined by one space,
// so case, spacing and punctuation never tell two names apart. A default
// name, which a machine or a tutorial wrote, has no key.
func candNameKey(name string) (string, int) {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r)
	})
	key := strings.Join(words, " ")
	if key == "" || candDefaultName(key) {
		return "", 0
	}
	return key, len(words)
}

// candDefaultName reports whether a normalised name is one that git's
// documentation, an operating system or a tool fills in, which says nothing
// about who committed.
func candDefaultName(key string) bool {
	switch key {
	case "your name", "root", "admin", "administrator", "user", "unknown", "none", "nobody",
		"anonymous", "ubuntu", "ec2 user", "vagrant", "test", "test user", "localhost", "git", "github",
		"gitlab", "developer", "dev", "default":
		return true
	}
	return false
}

// candLocal reads an address's local part for the local-part rule: a
// GitHub noreply address counts as its login, and its id is the account; a
// +tag is dropped elsewhere. The handle is "" for a local part the rule
// compares with nothing: a role or default one, or one shorter than
// candMinLocal.
func candLocal(email string) (handle, account, domain string, noreply bool) {
	local := email
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		local, domain = email[:i], email[i+1:]
	}
	if domain == candNoreply {
		noreply = true
		if id, login, ok := strings.Cut(local, "+"); ok && candDigits(id) {
			account, local = id, login
		}
	} else if i := strings.IndexByte(local, '+'); i > 0 {
		local = local[:i]
	}
	if utf8.RuneCountInString(local) >= candMinLocal && !candDefaultLocal(local) {
		handle = local
	}
	return handle, account, domain, noreply
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
}

// candComponents groups the addresses by each rule's key, keeps the groups
// that are evidence, and joins groups that share an address into
// components: one candidate each. A group is no evidence when more than
// candMaxShare addresses share its key, or when it holds two GitHub
// accounts (GitHub's terms allow one person one free personal account), or,
// for a local part, when its addresses carry different names: then the key
// is evidently several people's.
func candComponents(addrs []*candAddress) ([]*candComponent, map[int]bool) {
	byName, byHandle, byAccount := map[string][]int{}, map[string][]int{}, map[string][]int{}
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
	}
	shared := func(idx []int) bool { return len(idx) >= 2 && len(idx) <= candMaxShare }
	validName, validHandle := map[string]bool{}, map[string]bool{}
	var groups []candGroup
	for _, key := range candKeys(byName) {
		if idx := byName[key]; shared(idx) && candAccounts(addrs, idx) <= 1 {
			validName[key] = true
			groups = append(groups, candGroup{rule: RuleSameName, members: idx})
		}
	}
	for _, key := range candKeys(byHandle) {
		if idx := byHandle[key]; shared(idx) && candAccounts(addrs, idx) <= 1 && candNamesAgree(addrs, idx) {
			validHandle[key] = true
			groups = append(groups, candGroup{rule: RuleSameLocalPart, members: idx})
		}
	}
	for _, key := range candKeys(byAccount) {
		if idx := byAccount[key]; len(idx) >= 2 {
			groups = append(groups, candGroup{rule: RuleSameAccount, members: idx})
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
	var out []*candComponent
	linked := map[int]bool{}
	for _, g := range groups {
		root := find(g.members[0])
		c := byRoot[root]
		if c == nil {
			c = &candComponent{addrs: addrs, validName: validName, validHandle: validHandle}
			byRoot[root] = c
			out = append(out, c)
		}
		c.groups = append(c.groups, g)
		for _, m := range g.members {
			if !linked[m] {
				linked[m] = true
				c.members = append(c.members, m)
			}
		}
	}
	for _, c := range out {
		slices.Sort(c.members)
	}
	slices.SortFunc(out, func(x, y *candComponent) int { return cmp.Compare(x.members[0], y.members[0]) })
	return out, linked
}

// candKeys returns a map's keys, sorted.
func candKeys(m map[string][]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// candAccounts counts the distinct GitHub accounts among the addresses.
func candAccounts(addrs []*candAddress, idx []int) int {
	var seen []string
	for _, i := range idx {
		if a := addrs[i].account; a != "" && !slices.Contains(seen, a) {
			seen = append(seen, a)
		}
	}
	return len(seen)
}

// candNamesAgree reports whether no two of the addresses carry different
// names: two names differ when neither's words are all among the other's,
// so "ada" and "Ada Example" agree, and "Ada Example" and "Ada Lovelace" do
// not. A name that is only the address's own local part says nothing about
// who it is, so "jdoe" on jdoe@ agrees with any name.
func candNamesAgree(addrs []*candAddress, idx []int) bool {
	for i, x := range idx {
		for _, y := range idx[i+1:] {
			a, b := addrs[x], addrs[y]
			if a.key == "" || b.key == "" || a.login || b.login || a.key == b.key {
				continue
			}
			wa, wb := strings.Fields(a.key), strings.Fields(b.key)
			if !candSubset(wa, wb) && !candSubset(wb, wa) {
				return false
			}
		}
	}
	return true
}

func candSubset(a, b []string) bool {
	for _, w := range a {
		if !slices.Contains(b, w) {
			return false
		}
	}
	return true
}

// The strength of the direct evidence between two addresses.
const (
	candNone = iota // linked only through a third address
	candLow
	candMedium
	candHigh
)

// strength is the direct evidence that two addresses of a component are one
// person: GitHub's account id, or a full name and a local part together, is
// high; a full name alone is medium; a one-word name or a local part alone
// is low.
func (c *candComponent) strength(a, b *candAddress) int {
	if a.account != "" && a.account == b.account {
		return candHigh
	}
	name := a.key != "" && a.key == b.key && c.validName[a.key]
	handle := a.handle != "" && a.handle == b.handle && c.validHandle[a.handle]
	switch {
	case name && a.words >= 2 && handle:
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
	for i, a := range members {
		for _, b := range members[i+1:] {
			weakest = min(weakest, c.strength(a, b))
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
		if g.rule == RuleSameName {
			l.Name = members[l.Addresses[0]].Name
		}
		out.Links = append(out.Links, l)
	}
	candSortLinks(out.Links)
	canon := slices.MinFunc(members, candPreferred(domains, false))
	out.Canonical, out.Name = canon.Email, candName(canon, members)
	out.Addresses = candPublic(members)
	return out
}

// candByFirst orders addresses by first activity, then email.
func candByFirst(x, y *candAddress) int {
	return cmp.Or(x.First.Compare(y.First), cmp.Compare(x.Email, y.Email))
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
// co-authored, the latest activity, and the smallest email.
func candPreferred(domains map[string]int, newest bool) func(a, b *candAddress) int {
	return func(a, b *candAddress) int {
		if a.noreply != b.noreply {
			return candFalseFirst(a.noreply, b.noreply)
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
// gets the person's full name), else the canonical address's own, which is
// "" for a default name: the line then keeps each commit's own name.
func candName(canon *candAddress, members []*candAddress) string {
	if canon.words >= 2 {
		return canon.Name
	}
	// fuller orders names of more words first, then the address with more
	// commits, the later activity and the smaller name.
	fuller := func(a, b *candAddress) int {
		return cmp.Or(cmp.Compare(b.words, a.words), cmp.Compare(b.Commits, a.Commits),
			b.Last.Compare(a.Last), cmp.Compare(a.Name, b.Name))
	}
	if best := slices.MinFunc(members, fuller); best.words > canon.words {
		return best.Name
	}
	if canon.key == "" {
		return ""
	}
	return canon.Name
}

// candPublic copies the evidence of the addresses, in order.
func candPublic(members []*candAddress) []Address {
	out := make([]Address, len(members))
	for i, m := range members {
		out[i] = m.Address
	}
	return out
}

// candEdge is one possible hand-off.
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
func candHandOffs(fs *domain.FactSet, singles []*candAddress) [][]candEdge {
	if fs.Now.IsZero() || len(singles) < 2 {
		return nil
	}
	quiet := fs.Now.Add(-candHandOffQuiet)
	sorted := slices.Clone(singles)
	slices.SortFunc(sorted, candByFirst)
	// The pairs the times allow, before any file is read: few addresses
	// stopped long enough ago, and fewer began soon after.
	var pairs [][2]*candAddress
	need := map[string]*candAddress{}
	for _, a := range sorted {
		if a.Last.IsZero() || !a.Last.Before(quiet) {
			continue
		}
		limit := a.Last.Add(candHandOffGap)
		start := sort.Search(len(sorted), func(i int) bool { return sorted[i].First.After(a.Last) })
		for _, b := range sorted[start:] {
			if b.First.After(limit) {
				break
			}
			pairs = append(pairs, [2]*candAddress{a, b})
			need[a.Email], need[b.Email] = a, b
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	candFiles(fs, need)
	var edges []candEdge
	for _, p := range pairs {
		a, b := p[0], p[1]
		if n, files := candShared(a, b); n > 0 {
			edges = append(edges, candEdge{from: a, to: b,
				handOff: HandOff{Gap: b.First.Sub(a.Last), SharedFiles: n, Files: files}})
		}
	}
	out, in := map[*candAddress]int{}, map[*candAddress]int{}
	for _, e := range edges {
		out[e.from]++
		in[e.to]++
	}
	next, hasPrev := map[*candAddress]candEdge{}, map[*candAddress]bool{}
	for _, e := range edges {
		if out[e.from] == 1 && in[e.to] == 1 {
			next[e.from] = e
			hasPrev[e.to] = true
		}
	}
	var chains [][]candEdge
	for _, a := range sorted {
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

// candShared counts the files two addresses both touched or hold lines in,
// in the same repository, and lists the first candMaxFiles of them.
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
		shared = slices.Clip(shared[:candMaxFiles])
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
