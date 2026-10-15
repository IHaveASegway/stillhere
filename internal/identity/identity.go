// Package identity answers who a canonical email is: the display name, whether
// it is a bot, whether a change was AI-assisted, and who gets credit for it
// (docs/spec/git-invocation.md#identity-mailmap-and-bots, ADR 0007); and who
// a forge login is (Reviewer, ADR 0004).
//
// It is part of the functional core. git's mailmap is the only identity
// store: by the time facts reach this package, git has already mapped every
// alias, and this package never parses a mailmap or merges people by name.
//
// Credit is the one credit rule. Commits and blamed lines both go through
// it, so the change metrics (durable@1, simplifying@1) and the ownership
// metrics (surviving@1, majority_owner@1, knowledge_loss@1) agree on who
// wrote an agent's commit. A second, local copy of the rule in any metric
// would be a second owner of the same decision, free to drift.
package identity

import (
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// Directory resolves canonical emails within one fact set under one policy's
// [bots] and [ai] patterns. It is built once and only read afterwards, so
// any number of goroutines may share it.
//
// Two kinds of identity are not people:
//
//   - A bot: its canonical email or display name contains a [bots]
//     fragment, such as a dependency updater.
//   - An agent: a coding agent the [ai] table names, either because its
//     email is one of coauthor_emails (noreply@anthropic.com) or because its
//     email or display name contains an agent_authors fragment (copilot).
//     Agents are bots everywhere (ADR 0007: agents are never ranked rows),
//     and an agent's commit is credited to its first human co-author when
//     it has one.
//
// A forge app account, the subject "@login[bot]" that Reviewer gives it, is
// a bot too, whatever the policy's fragments say.
type Directory struct {
	names        map[string]string // canonical email → display name
	kinds        map[string]kind   // canonical email → its classification, for every email in names
	locals       map[string]bool   // the folded local-part forms of every person's address: the names Withheld withholds (withheld.go)
	botFragments []string          // [bots] fragments, lowercased, never empty
	agentEmails  map[string]bool   // [ai] coauthor_emails, lowercased
	agentAuthors []string          // [ai] agent_authors fragments, lowercased, never empty
	logins       map[string]string // forge login → canonical email (reviewer.go); nil without GitHub data
}

// kind is an identity's classification under the policy.
type kind struct {
	bot   bool // a bot or an agent: never a person in a rank
	agent bool // a coding agent: its commits credit a human co-author
}

// New builds the directory for a fact set under a policy's bot and AI
// patterns. A nil policy has no patterns: nobody is a bot or an agent but a
// forge app account. The fact set's GitHub data (FactSet.Forge) links forge
// logins to its people (Reviewer).
func New(fs *domain.FactSet, p *policy.Policy) *Directory {
	d := &Directory{names: displayNames(fs), logins: reviewerLogins(fs)}
	if p != nil {
		d.botFragments = fragments(p.Bots.Fragments)
		d.agentAuthors = fragments(p.AI.AgentAuthors)
		for _, e := range p.AI.CoauthorEmails {
			if e = canonical(e); e != "" {
				if d.agentEmails == nil {
					d.agentEmails = make(map[string]bool)
				}
				d.agentEmails[e] = true
			}
		}
	}
	if len(d.names) > 0 {
		d.kinds = make(map[string]kind, len(d.names))
		for email, name := range d.names {
			d.kinds[email] = d.classify(email, name)
		}
	}
	d.locals = personLocals(fs, d) // after kinds: a bot's address is no person's
	return d
}

// Name returns the display name for a canonical email, or "" when the fact
// set never shows the email with a name.
//
// git has already applied the mailmap to every name in the facts, so a name
// the mailmap supplies is the name every sighting carries. Otherwise the
// rule of docs/spec/git-invocation.md applies: the name seen most often with
// that email, counting each commit once as its author and once as a
// co-author; ties go to the newest committer time among the commits that
// used the name, then to the name's UTF-8 bytes, ascending.
func (d *Directory) Name(email string) string {
	return d.names[canonical(email)]
}

// Bot reports whether a canonical email belongs to a bot or an agent: a
// [bots] fragment occurs in the email or the display name, or the identity
// is an [ai] agent. A forge app account's subject ("@login[bot]", Reviewer)
// is a bot too. Bots are separated in every table and never ranked.
func (d *Directory) Bot(email string) bool {
	return d.kind(canonical(email)).bot
}

// AIAssisted reports whether c is a person's AI-assisted commit (ADR 0007):
// the person it is credited to is human, and either a co-author's email is
// one of the policy's coding-agent emails or the author is an agent. A
// co-author is matched by email, never by name, so a person named Claude is
// a person. An agent's commit with no human co-author is a bot commit, not
// an AI-assisted one.
func (d *Directory) AIAssisted(c *domain.Commit) bool {
	if c == nil || d.Bot(d.Credit(c.Author, c.CoAuthors)) {
		return false
	}
	if d.kind(canonical(c.Author.Email)).agent {
		return true
	}
	for _, co := range c.CoAuthors {
		if d.agentEmails[canonical(co.Email)] {
			return true
		}
	}
	return false
}

// Credit returns the canonical email credited for work by author with
// coAuthors in trailer order: the author, or the first human co-author when
// the author is an agent (ADR 0007). Commits and blamed lines both go
// through it (Commit.Author/CoAuthors, LineOwner.Author/CoAuthors), so the
// change metrics and the ownership metrics agree on who wrote an agent's
// commit.
//
// An agent's commit without a human co-author stays with the agent, which
// Bot reports, so it lands in the bot table. A human author keeps the credit
// whoever the co-authors are: the co-authors GitHub lists on a squash merge
// earn nothing.
func (d *Directory) Credit(author domain.Person, coAuthors []domain.Person) string {
	email := canonical(author.Email)
	if !d.kind(email).agent {
		return email
	}
	for _, co := range coAuthors {
		if e := canonical(co.Email); e != "" && !d.Bot(e) {
			return e
		}
	}
	return email
}

// kind returns a canonical email's classification: precomputed for every
// email the fact set names, computed from the email alone otherwise.
func (d *Directory) kind(email string) kind {
	if k, ok := d.kinds[email]; ok {
		return k
	}
	return d.classify(email, "")
}

// classify applies the policy's patterns to one identity. A forge app
// account is a bot by its account type, which the forge reports, so no
// fragment is needed for it.
func (d *Directory) classify(email, name string) kind {
	name = strings.ToLower(name)
	agent := d.agentEmails[email] || containsAny(d.agentAuthors, email, name)
	return kind{
		bot:   agent || appAccount(email) || containsAny(d.botFragments, email, name),
		agent: agent,
	}
}

// canonical returns the canonical key for an email: lowercased, as every
// collector stores it. Lowercasing again costs nothing and keeps a caller's
// mixed-case input from splitting one person in two.
func canonical(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// fragments lowercases a policy's fragment list and drops empty entries: an
// empty fragment is a substring of every string and would make everyone a
// bot.
func fragments(in []string) []string {
	var out []string
	for _, f := range in {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// containsAny reports whether any fragment is a substring of any of ss.
func containsAny(frags []string, ss ...string) bool {
	for _, f := range frags {
		for _, s := range ss {
			if strings.Contains(s, f) {
				return true
			}
		}
	}
	return false
}

// nameTally counts one name's sightings for one email.
type nameTally struct {
	count  int
	newest time.Time // the newest committer time among the commits that used the name
}

// displayNames applies the display-name rule to every email the fact set
// shows: commit authors and co-authors, and the authors and co-authors of
// blamed commits the history does not hold (blame can attribute lines to
// commits older than any history window, which carry only an author time).
// Each commit counts once per (email, name).
func displayNames(fs *domain.FactSet) map[string]string {
	if fs == nil {
		return nil
	}
	tallies := make(map[string]map[string]*nameTally)
	see := func(p domain.Person, at time.Time) {
		if p.Name == "" {
			return
		}
		email := canonical(p.Email)
		byName := tallies[email]
		if byName == nil {
			byName = make(map[string]*nameTally)
			tallies[email] = byName
		}
		t := byName[p.Name]
		if t == nil {
			t = &nameTally{}
			byName[p.Name] = t
		}
		t.count++
		if at.After(t.newest) {
			t.newest = at
		}
	}
	same := func(a, b domain.Person) bool {
		return a.Name == b.Name && canonical(a.Email) == canonical(b.Email)
	}
	// seeCommit records one commit's author and co-authors, each distinct
	// (email, name) once. Co-author lists are short, so the quadratic
	// duplicate check allocates nothing.
	seeCommit := func(author domain.Person, coAuthors []domain.Person, at time.Time) {
		see(author, at)
		for i, co := range coAuthors {
			dup := func(p domain.Person) bool { return same(p, co) }
			if !same(co, author) && !slices.ContainsFunc(coAuthors[:i], dup) {
				see(co, at)
			}
		}
	}
	for i := range fs.Repos {
		r := &fs.Repos[i]
		inHistory := make(map[string]bool, len(r.Commits))
		for j := range r.Commits {
			c := &r.Commits[j]
			inHistory[c.SHA] = true
			seeCommit(c.Author, c.CoAuthors, c.CommitTime)
		}
		blamed := make(map[string]bool)
		for j := range r.Owners {
			o := &r.Owners[j]
			if inHistory[o.Commit] || blamed[o.Commit] {
				continue
			}
			blamed[o.Commit] = true
			seeCommit(o.Author, o.CoAuthors, o.AuthorTime)
		}
	}
	if len(tallies) == 0 {
		return nil
	}
	names := make(map[string]string, len(tallies))
	for email, byName := range tallies {
		var best string
		var bestTally *nameTally
		for name, t := range byName {
			if bestTally == nil || better(name, t, best, bestTally) {
				best, bestTally = name, t
			}
		}
		names[email] = best
	}
	return names
}

// better reports whether name a, with tally ta, beats name b with tally tb:
// more sightings, then the newer committer time, then the smaller UTF-8
// bytes. The order is total, so map iteration order cannot change the
// winner.
func better(a string, ta *nameTally, b string, tb *nameTally) bool {
	if ta.count != tb.count {
		return ta.count > tb.count
	}
	if !ta.newest.Equal(tb.newest) {
		return ta.newest.After(tb.newest)
	}
	return a < b
}
