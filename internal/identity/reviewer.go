package identity

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file is the one rule for who a forge account is (#38), which every
// metric that reads GitHub data asks (reviews_given@1, knowledge_loss@1,
// who_knows@1), so the three cannot disagree about who reviewed. Before it,
// reviews_given@1 resolved logins itself: a second copy of the rule would be
// a second owner of the same decision, free to drift.
//
// It is also where a forge identity becomes a person key, so it is where a
// login is made canonical (#69): GitHub compares logins without case, as
// mail systems compare an address's domain, and an account can change the
// case of its login, so "Zoe" on one pull request and "zoe" on another are
// one account. Reviewer folds a login to lower case before it links it or
// keys it, as the collectors lowercase every email, and every reader that
// lowercases a key it is handed then finds the key the run stored. The
// account's own case is kept apart, for display only (Name): key and
// display name are separate, as an email's are (Evans, Domain-Driven
// Design, ch. 14: the anti-corruption layer translates at the boundary, so
// the core holds one form).

// appSuffix ends the subject of an app account, "@login[bot]": GitHub's
// GraphQL API drops it from a Bot's login, and its REST API and the commits
// it authors keep it.
const appSuffix = "[bot]"

// foldLogin is a login's canonical form: lowercased, as GitHub compares
// logins. A login is ASCII letters, digits, hyphens and underscores
// (LoginKey), so lowercasing is case folding.
func foldLogin(login string) string { return strings.ToLower(login) }

// CanonicalKey returns a stored person key in the form keys are made in
// now: a login key folded as Reviewer folds it ("@Zoe", as a run stored
// before #69 kept it, is "@zoe"), and any other key as it is, since the
// collectors store every email lowercased. A reader of a run stored before
// the fold asks it, so the run's keys join the ones made since (app's load,
// an upcast at the boundary a stored run enters by).
func CanonicalKey(key string) string {
	if LoginKey(key) {
		return foldLogin(key)
	}
	return key
}

// Reviewer returns who a forge account is in this run. A login is a person
// when a commit the forge attributes to it, one of a pull request's commits
// or its merge commit, is in the run's history: the person is the run's own
// mailmapped author of that commit, the one with the most such commits,
// then the smallest email. So git's mailmap stays the only identity store
// (docs/spec/git-invocation.md#identity-mailmap-and-bots), and a login the
// forge resolved from an address the mailmap folds into another lands on
// the canonical one. Any other login is the subject "@login" (ADR 0004),
// which holds no line and lands no change; an app account is
// "@login[bot]", whatever commits it authored, and Bot reports it. A
// deleted account (no login) is nobody: the zero Subject.
//
// Logins are compared and keyed without case (foldLogin): "@zoe" for Zoe
// and zoe alike, and a login links to a person whatever case its commits
// and its reviews carry it in. Name gives the account's own case back.
func (d *Directory) Reviewer(u domain.ForgeUser) domain.Subject {
	login := foldLogin(u.Login)
	switch {
	case login == "":
		return domain.Subject{}
	case u.Bot:
		return domain.Subject{Kind: domain.SubjectPerson, Key: "@" + strings.TrimSuffix(login, appSuffix) + appSuffix}
	case d.logins[login] != "":
		return domain.Subject{Kind: domain.SubjectPerson, Key: d.logins[login]}
	}
	return domain.Subject{Kind: domain.SubjectPerson, Key: "@" + login}
}

// Logins returns the GitHub logins the run links to the person with the
// canonical email (Reviewer's rule: GitHub attributes to the login commits
// that are theirs in the run), each as GitHub last reported it (loginForms),
// sorted without case; nil when none. An inbound adapter resolves "@login"
// by it, so a person's GitHub handle finds them, and shows beside their
// name those LoginWithheld allows. It reads the links Reviewer reads, never
// another copy.
func (d *Directory) Logins(email string) []string {
	folded := d.linked[canonical(email)]
	if len(folded) == 0 {
		return nil
	}
	out := make([]string, len(folded))
	for i, login := range folded {
		out[i] = cmp.Or(d.forms[login], login)
	}
	return out
}

// LoginWithheld reports whether a page that prints no address must not
// print a login it links to a person: the login is, ignoring case, a local
// part of a person's address in the run (localForms), as a GitHub noreply
// address holds its account's login, so printing it beside the person
// would give their address away, as a display name that is a local part
// does (Withheld). A login no commit links to anyone is shown whatever it
// is (Withheld), since the page ties it to no address; resolving a login
// prints nothing, so an adapter resolves every login and prints only the
// others.
func (d *Directory) LoginWithheld(login string) bool {
	return d.locals[fold(strings.TrimSuffix(login, appSuffix))] || strings.ContainsRune(login, '@')
}

// loginName is how a page names a login key ("@zoe", "@dependabot[bot]"):
// the login as GitHub last reported it, "@Zoe", or "" when the run's GitHub
// data never named it, and the page shows the key itself.
func (d *Directory) loginName(key string) string {
	login := strings.TrimPrefix(key, "@")
	if f := d.forms[login]; f != "" {
		return "@" + f
	}
	if base, ok := strings.CutSuffix(login, appSuffix); ok {
		if f := d.forms[base]; f != "" {
			return "@" + f + appSuffix
		}
	}
	return ""
}

// appAccount reports whether a subject key is an app account's, as Reviewer
// names one: a login key (LoginKey, the one rule for what a login is) that
// ends in the app suffix.
func appAccount(key string) bool {
	return strings.HasSuffix(key, appSuffix) && LoginKey(key)
}

// reviewerLogins links each login of the fact set's GitHub data, folded
// (foldLogin), to the person Reviewer says it is: the run's author of the
// commits the forge attributes to it, counting each commit once, the most
// commits winning, then the smallest email. It returns nil without GitHub
// data.
func reviewerLogins(fs *domain.FactSet) map[string]string {
	if fs == nil || fs.Forge == nil {
		return nil
	}
	author := map[string]string{} // SHA → the run's canonical author email
	for i := range fs.Repos {
		for j := range fs.Repos[i].Commits {
			if c := &fs.Repos[i].Commits[j]; c.Author.Email != "" {
				author[c.SHA] = canonical(c.Author.Email)
			}
		}
	}
	type vote struct{ login, sha string }
	seen := map[vote]bool{}
	votes := map[string]map[string]int{} // folded login → email → distinct commits
	forgeCommits(fs, false, func(c domain.ForgeCommit) {
		email, ok := author[c.SHA]
		login := foldLogin(c.AuthorLogin)
		v := vote{login, c.SHA}
		if login == "" || !ok || seen[v] {
			return
		}
		seen[v] = true
		if votes[login] == nil {
			votes[login] = map[string]int{}
		}
		votes[login][email]++
	})
	if len(votes) == 0 {
		return nil
	}
	out := make(map[string]string, len(votes))
	for login, emails := range votes {
		best, n := "", 0
		for email, k := range emails {
			if k > n || k == n && email < best {
				best, n = email, k
			}
		}
		out[login] = best
	}
	return out
}

// forgeCommits calls fn for every commit of the pull requests the fact
// set's GitHub data holds: each one's own commits and its merge commit, in
// the replica's order; and, with older too, the links the run kept of its
// older pull requests (ForgeFacts.Links). It is the one walk over the
// forge's commit-to-account links: Reviewer's rule (reviewerLogins) reads
// the pull requests, and the same-forge-account rule (forgeAccounts) every
// link, since GitHub's attribution of an address does not age.
func forgeCommits(fs *domain.FactSet, older bool, fn func(c domain.ForgeCommit)) {
	if fs == nil || fs.Forge == nil {
		return
	}
	for i := range fs.Forge.PullRequests {
		p := &fs.Forge.PullRequests[i]
		for _, c := range p.Commits {
			fn(c)
		}
		fn(p.MergeCommit)
	}
	if older {
		for _, l := range fs.Forge.Links {
			fn(l.ForgeCommit)
		}
	}
}

// forgeAccounts returns, for each address of the fact set that GitHub's own
// links attribute to an account, the folded logins (foldLogin) it is
// attributed to, sorted: GitHub links a commit to the account that verified
// its author email, or whose noreply address it is, and the replica keeps
// that link with every pull request's commits (ForgeCommit). It is the
// evidence of the same-forge-account rule (candidates.go) and of Blocks.
// Each link is read by forgeLinkReader. nil without GitHub data.
func forgeAccounts(fs *domain.FactSet) map[string][]string {
	if fs == nil || fs.Forge == nil {
		return nil
	}
	read := forgeLinkReader(fs)
	links := map[string]map[string]bool{} // address → folded logins
	forgeCommits(fs, true, func(c domain.ForgeCommit) {
		email, login, ok := read(c)
		if !ok {
			return
		}
		if links[email] == nil {
			links[email] = map[string]bool{}
		}
		links[email][login] = true
	})
	if len(links) == 0 {
		return nil
	}
	out := make(map[string][]string, len(links))
	for email, logins := range links {
		out[email] = slices.Sorted(maps.Keys(logins))
	}
	return out
}

// forgeLinkReader returns the one reading of a forge commit's link that
// the same-forge-account rule, Blocks and KeptLinks share: the run's
// address it names and the account, folded, or false when it counts for
// nothing.
//
// A link names the run's own address of its commit: the mailmapped author
// git gave the SHA in the history, or in a blamed commit older than the
// history, so a link follows the mailmap as every page does. A commit the
// facts do not hold, such as a squashed pull request's own commit, names
// its email as GitHub read it, when that email is an address the facts
// show: git's mailmap left that address as it is, since one it rewrote
// appears in no fact. Logins are folded as Reviewer folds them, so one
// account is one key whatever case its commits carry; an app's login
// ("name[bot]") and a link to an address the facts never show count for
// nothing.
func forgeLinkReader(fs *domain.FactSet) func(c domain.ForgeCommit) (email, login string, ok bool) {
	author := map[string]string{} // SHA → the run's canonical author email
	known := map[string]bool{}    // every address the facts show, as author or co-author
	see := func(sha string, p domain.Person, co []domain.Person) {
		if e := canonical(p.Email); e != "" {
			known[e] = true
			if _, ok := author[sha]; !ok {
				author[sha] = e
			}
		}
		for _, c := range co {
			if e := canonical(c.Email); e != "" {
				known[e] = true
			}
		}
	}
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		for j := range rf.Commits {
			c := &rf.Commits[j]
			see(c.SHA, c.Author, c.CoAuthors)
		}
		for j := range rf.Owners {
			o := &rf.Owners[j]
			see(o.Commit, o.Author, o.CoAuthors)
		}
	}
	return func(c domain.ForgeCommit) (string, string, bool) {
		login := foldLogin(c.AuthorLogin)
		if login == "" || strings.HasSuffix(login, appSuffix) {
			return "", "", false
		}
		email, ok := author[c.SHA]
		if !ok {
			if email = canonical(c.AuthorEmail); !known[email] {
				return "", "", false
			}
		}
		return email, login, true
	}
}

// KeptLinks returns, of links (the commit-to-account links of the pull
// requests older than a run's window, ForgeFacts.Links), the first for
// each repository, address and account they link, as forgeLinkReader reads
// them over fs, in their order. The same-forge-account rule and Blocks read
// only which addresses a repository's links tie to which accounts, so these
// give them exactly what every link gives, while a run that keeps them
// (ADR 0011's amendment) stores as many as its history shows people, not
// one per commit of every pull request a sync ever copied. A link that
// names no address of fs counts for nothing and is left out. fs must be
// the facts the run keeps, which its readers read the links against.
func KeptLinks(fs *domain.FactSet, links []domain.ForgeLink) []domain.ForgeLink {
	if len(links) == 0 {
		return nil
	}
	read := forgeLinkReader(fs)
	seen := map[[3]string]bool{}
	var out []domain.ForgeLink
	for _, l := range links {
		email, login, ok := read(l.ForgeCommit)
		key := [3]string{l.Repo, email, login}
		if ok && !seen[key] {
			seen[key] = true
			out = append(out, l)
		}
	}
	return out
}

// linkedLogins inverts Reviewer's links: canonical email → the folded logins
// linked to it, sorted. nil without any.
func linkedLogins(logins map[string]string) map[string][]string {
	if len(logins) == 0 {
		return nil
	}
	out := map[string][]string{}
	for login, email := range logins {
		out[email] = append(out[email], login)
	}
	for _, ls := range out {
		slices.Sort(ls)
	}
	return out
}

// loginForms returns, for each login the fact set's GitHub data names (a
// pull request's author, a review's, a commit's), folded, the login as
// GitHub last reported it: its form on the pull request updated last, then
// the smallest. A sync copies a pull request as GitHub returns it then, and
// a later sync copies only what was updated since, so the newest update
// carries the account's current case, as GitHub shows it on its profile. A
// login is kept as reported, an app's with its suffix or without it, as
// GitHub's APIs differ (appSuffix), and loginName reads either. nil without
// GitHub data.
func loginForms(fs *domain.FactSet) map[string]string {
	if fs == nil || fs.Forge == nil {
		return nil
	}
	type seen struct {
		form string
		at   time.Time
	}
	best := map[string]seen{}
	see := func(form string, at time.Time) {
		if form == "" {
			return
		}
		key := foldLogin(form)
		if b, ok := best[key]; ok && (b.at.After(at) || b.at.Equal(at) && b.form <= form) {
			return
		}
		best[key] = seen{form, at}
	}
	for i := range fs.Forge.PullRequests {
		p := &fs.Forge.PullRequests[i]
		see(p.Author.Login, p.Updated)
		for j := range p.Reviews {
			see(p.Reviews[j].Author.Login, p.Updated)
		}
		for _, c := range p.Commits {
			see(c.AuthorLogin, p.Updated)
		}
		see(p.MergeCommit.AuthorLogin, p.Updated)
	}
	if len(best) == 0 {
		return nil
	}
	out := make(map[string]string, len(best))
	for key, b := range best {
		out[key] = b.form
	}
	return out
}
