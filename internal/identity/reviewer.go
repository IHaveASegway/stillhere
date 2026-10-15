package identity

import (
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file is the one rule for who a forge account is (#38), which every
// metric that reads GitHub data asks (reviews_given@1, knowledge_loss@1,
// who_knows@1), so the three cannot disagree about who reviewed. Before it,
// reviews_given@1 resolved logins itself: a second copy of the rule would be
// a second owner of the same decision, free to drift.

// appSuffix ends the subject of an app account, "@login[bot]": GitHub's
// GraphQL API drops it from a Bot's login, and its REST API and the commits
// it authors keep it.
const appSuffix = "[bot]"

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
func (d *Directory) Reviewer(u domain.ForgeUser) domain.Subject {
	switch {
	case u.Login == "":
		return domain.Subject{}
	case u.Bot:
		return domain.Subject{Kind: domain.SubjectPerson, Key: "@" + strings.TrimSuffix(u.Login, appSuffix) + appSuffix}
	case d.logins[u.Login] != "":
		return domain.Subject{Kind: domain.SubjectPerson, Key: d.logins[u.Login]}
	}
	return domain.Subject{Kind: domain.SubjectPerson, Key: "@" + u.Login}
}

// appAccount reports whether a subject key is an app account's, as Reviewer
// names one.
func appAccount(key string) bool {
	return strings.HasPrefix(key, "@") && strings.HasSuffix(key, appSuffix)
}

// reviewerLogins links each login of the fact set's GitHub data to the
// person Reviewer says it is: the run's author of the commits the forge
// attributes to it, counting each commit once, the most commits winning,
// then the smallest email. It returns nil without GitHub data.
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
	votes := map[string]map[string]int{} // login → email → distinct commits
	count := func(c domain.ForgeCommit) {
		email, ok := author[c.SHA]
		v := vote{c.AuthorLogin, c.SHA}
		if c.AuthorLogin == "" || !ok || seen[v] {
			return
		}
		seen[v] = true
		if votes[c.AuthorLogin] == nil {
			votes[c.AuthorLogin] = map[string]int{}
		}
		votes[c.AuthorLogin][email]++
	}
	for i := range fs.Forge.PullRequests {
		p := &fs.Forge.PullRequests[i]
		for _, c := range p.Commits {
			count(c)
		}
		count(p.MergeCommit)
	}
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
