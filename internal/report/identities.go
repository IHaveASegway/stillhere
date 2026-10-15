package report

import (
	"crypto/sha256"
	"encoding/hex"
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
// them, such as the MCP server without --allow-emails, can drop them: no
// other field holds an address or a part of one. Text and Markdown print
// none either, as everywhere (docs/spec/report-json.md): an address is
// shown by its domain and a short hash. Only Mailmap, whose output is made
// of addresses, prints them.
type Identities struct {
	Provenance Provenance          `json:"provenance"`
	Scope      Scope               `json:"scope"`      // the repositories the candidates come from: all, a group, or one repository
	Addresses  int                 `json:"addresses"`  // human addresses the scope's facts show
	Bots       int                 `json:"bots"`       // bot and agent addresses, which never appear in a candidate
	Candidates []IdentityCandidate `json:"candidates"` // most confident first
	Rules      []IdentityRule      `json:"rules"`      // every rule's definition, in order
	Caveats    []Caveat            `json:"caveats"`
}

// IdentityCandidate is a group of addresses that may belong to one person.
type IdentityCandidate struct {
	Confidence string            `json:"confidence"` // high, medium or low
	Guess      bool              `json:"guess"`      // a hand-off: never suggested as certain
	Commented  bool              `json:"commented"`  // the mailmap form writes its lines commented out: every low candidate
	Chained    bool              `json:"chained"`    // two of the addresses are linked only through a third
	Rules      []string          `json:"rules"`      // the rules that link the addresses, in the order of rules
	Name       string            `json:"name"`       // the display name suggested for the person; "" keeps each commit's own
	Addresses  []IdentityAddress `json:"addresses"`  // by first activity; exactly one is canonical
	Links      []IdentityLink    `json:"links"`      // each rule's evidence: which addresses it links
}

// IdentityAddress is one address of a candidate and the evidence for it,
// counted over the run's history and the older commits blame attributes
// lines to.
type IdentityAddress struct {
	Email      string    `json:"email"` // the canonical email; JSON only
	Name       string    `json:"name"`  // its display name
	Canonical  bool      `json:"canonical"`
	Commits    int       `json:"commits"`     // commits it authored
	CoAuthored int       `json:"co_authored"` // commits whose Co-authored-by trailers name it
	First      time.Time `json:"first"`       // the earliest author time of those commits
	Last       time.Time `json:"last"`
	Repos      []string  `json:"repos"` // the repositories' keys, sorted
}

// IdentityLink is one rule's evidence in a candidate.
type IdentityLink struct {
	Rule      string           `json:"rule"`
	Addresses []int            `json:"addresses"` // indexes into the candidate's addresses
	Name      string           `json:"name"`      // same-name: the display name they share; "" otherwise
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
}

// identCodeMailmap is the caveat every identities answer carries: the
// addresses are the run's, which git mailmapped when it was scanned.
const identCodeMailmap = "mailmap-at-scan"

// NewIdentities builds the identities answer. Like every answer, it arranges
// what another package computed, identity.Candidates here, and computes
// nothing itself; it adds where the facts come from and what could make the
// candidates wrong.
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
	var repos []*domain.RepoFacts
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		names[rf.Repo.Key] = rf.Repo.Name
		repos = append(repos, rf)
	}
	if s := in.Survey; s != nil {
		id.Addresses, id.Bots = s.Addresses, s.Bots
		for i := range s.Candidates {
			id.Candidates = append(id.Candidates, identCandidate(&s.Candidates[i], names))
		}
	}
	for _, def := range identity.Rules() {
		id.Rules = append(id.Rules, IdentityRule{ID: string(def.Rule), Matches: def.Matches, Confidence: def.Confidence, Risk: def.Risk})
	}
	id.Provenance = answerProvenance(in.Run, fs, repos, nil)
	id.Caveats = append(answerCaveats(repos), Caveat{
		Code: identCodeMailmap,
		Message: "These are the run's addresses, which git mailmapped when the run was scanned: an address the " +
			"mailmap merged is in no candidate, and a .mailmap line added since takes effect at the next scan.",
	})
	return id
}

func identCandidate(c *identity.Candidate, names map[string]string) IdentityCandidate {
	out := IdentityCandidate{
		Confidence: string(c.Confidence),
		Guess:      c.Guess,
		Commented:  c.Confidence == identity.ConfidenceLow,
		Chained:    c.Chained,
		Name:       c.Name,
	}
	for _, r := range c.Rules() {
		out.Rules = append(out.Rules, string(r))
	}
	for _, a := range c.Addresses {
		out.Addresses = append(out.Addresses, IdentityAddress{
			Email: a.Email, Name: a.Name, Canonical: a.Email == c.Canonical,
			Commits: a.Commits, CoAuthored: a.CoAuthored, First: a.First, Last: a.Last,
			Repos: slices.Clone(a.Repos),
		})
	}
	for _, l := range c.Links {
		link := IdentityLink{Rule: string(l.Rule), Addresses: slices.Clone(l.Addresses), Name: l.Name}
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

// identIntro says what the page holds, and why it shows no email.
const identIntro = "addresses that may belong to one person, with the evidence, for a person to review: stillhere " +
	"applies none. This page names no email, as no text or Markdown page does: --format mailmap prints the " +
	".mailmap lines, and --format json the addresses."

// identSummary is "11 addresses of people in 1 repository, 1 bot or agent
// left out · 2 candidates".
func identSummary(id *Identities) string {
	s := plural(id.Addresses, "address", "addresses") + " of people in " +
		plural(len(id.Provenance.Heads), "repository", "repositories")
	if id.Bots > 0 {
		s += ", " + plural(id.Bots, "bot or agent", "bots and agents") + " left out"
	}
	return s + " · " + plural(len(id.Candidates), "candidate", "candidates")
}

// identNone says what an empty answer means.
const identNone = "no candidates: no two addresses share a display name, a local part or a GitHub account, and " +
	"no address took over from another that stopped"

// identTitle is "Ada Example · high confidence · same-name, same-local-part";
// a hand-off names each address's person in order.
func identTitle(c IdentityCandidate, esc func(string) string) string {
	title := identPerson(c.Name, esc)
	if c.Guess {
		var names []string
		for _, a := range c.Addresses {
			names = append(names, identPerson(a.Name, esc))
		}
		title = strings.Join(names, " → ")
	}
	title += " · " + c.Confidence + " confidence"
	if c.Guess {
		title += " · a guess"
	}
	return title + " · " + strings.Join(c.Rules, ", ")
}

func identPerson(name string, esc func(string) string) string {
	if name == "" {
		return "(no name)"
	}
	return esc(name)
}

// identAddressText is "…@example.com (6c2d1f) · Ada Example · 2 commits ·
// 2026-01-01 → 2026-01-05 · fixture · canonical", or, for Mailmap, the same
// with the email itself first.
func identAddressText(a IdentityAddress, label string, pv Provenance, esc func(string) string) string {
	parts := []string{label, identPerson(a.Name, esc), plural(a.Commits, "commit", "commits")}
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

// identLabel shows an address without printing it: its domain, and the
// start of its SHA-256, so two addresses at one domain stay apart, as the
// no-name placeholder keeps two nameless people apart (displayName).
func identLabel(email string, esc func(string) string) string {
	sum := sha256.Sum256([]byte(email))
	tag := " (" + hex.EncodeToString(sum[:3]) + ")"
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		return "…@" + esc(email[i+1:]) + tag
	}
	return "…" + tag
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
			out = append(out, which+" carry the display name \""+esc(l.Name)+"\"")
		case identity.RuleSameLocalPart:
			out = append(out, which+" have the same local part")
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
	if c.Guess {
		what = "a guess"
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
		for j, a := range c.Addresses {
			p.wrapped(fmt.Sprintf("   %d. ", j+1), "      ", identAddressText(a, identLabel(a.Email, clean), id.Provenance, clean))
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
		cols := []column{{head: "#", right: true}, {head: "Address"}, {head: "Name"}, {head: "Commits", right: true},
			{head: "Co-authored", right: true}, {head: "First"}, {head: "Last"}, {head: "Repositories"}, {head: "Canonical"}}
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
			rows = append(rows, []string{strconv.Itoa(j + 1), identLabel(a.Email, mdText), identPerson(a.Name, mdText),
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
// (gitmailmap(5)). A high or medium candidate's lines are written as lines;
// a low one's, every guess among them, are written commented out, so adding
// the output to a .mailmap unread merges nobody on a weak match. Each line
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
		"each candidate's evidence before adding its lines to a .mailmap, or to the file --mailmap names. A weak "+
		"match or a guess is written commented out: uncomment it only if you know the addresses are one person. "+
		"A line takes effect at the next scan.")
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
			switch {
			case !identSafe(line):
				p.wrapped("#   ", "#     ", "this line cannot be a .mailmap line: a name or an address in it holds "+
					"< or >, a control character, or a name starts with #")
				p.line("# " + clean(line.text()))
			case c.Commented:
				p.line("# " + line.text())
			default:
				p.line(line.text())
			}
		}
	}
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
