package metrics

import (
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file is knowledge_loss@1's activity: the one rule for whether a
// person has gone quiet, which knowledge_loss@1's values, PathKnowledge's
// (who-knows' "gone quiet") and every page's last activity of a person all
// read (LastActivity), so a page cannot show someone as active whom the
// metric counts as gone, or the other way round (Information Expert:
// Larman, "Applying UML and Patterns", ch. 17).
//
// A person is active at the time of each thing they did that the run's
// facts show, in the history window before Now:
//
//   - a landing that holds a commit credited to them, merge commits
//     included (the landing's committer time): a side-branch author is
//     active when the branch lands, whoever merged it;
//   - a review they submitted, approved, changes requested or commented, on
//     any pull request of the run's GitHub data, open, closed or merged,
//     their own included: a reply in a review thread of one's own pull
//     request, which GitHub records as a review, says they are still around;
//   - a pull request they opened (its creation time), and each commit of
//     theirs on one of their own pull requests that the clone holds (the
//     commit's author time): GitHub's update time is not their activity,
//     since anyone's comment or a bot's label moves it, and a pull request
//     of 2019 updated last month says nothing about its author;
//   - a commit credited to them on another ref of the clone, a branch, a
//     remote-tracking branch or a tag (domain.RepoFacts.RefCommits), at its
//     author time: when they wrote it, which a rebase or a cherry-pick by
//     someone else leaves as it was, where the committer time would make the
//     author of a branch someone rebased look active.
//
// A forge account is the person identity.Directory.Reviewer says it is, as
// for every metric that reads GitHub data, so a login the run links to no
// address ("@login") holds no line and moves no value. Every time is
// bounded by Now, which the collectors already bound the facts by, so a
// run stays a function of its inputs. Activity needs no coverage
// (repoView.covers): a landing seen in a window proves activity however
// much history is missing.

// activityKinds orders the kinds of activity, the first winning a tie at
// one instant: the default branch's evidence first.
var activityKinds = map[string]int{
	domain.ActivityLanding: 0, domain.ActivityReview: 1, domain.ActivityPullRequest: 2, domain.ActivityCommit: 3,
}

// LastActivity returns each person's last activity in the run, by canonical
// email (or "@login" for a GitHub account the run links to no address), in
// the history window, [Now - 365 days, Now): the newest time knowledge_loss@1's
// rule counts, and what it was. A person with none in the window is
// absent; bots and agents are left out, as knowledge_loss@1 leaves out
// their lines. Per query, never stored: a page's last activity of a person
// (the still-here table, the card's people, who-knows) reads it, as
// knowledge_loss@1 does.
func LastActivity(in *Input) (map[string]domain.Activity, error) {
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	return v.lastActivity(), nil
}

// lastActivity is LastActivity over the view.
func (v *view) lastActivity() map[string]domain.Activity {
	w := v.window(yearDays, 0)
	out := map[string]domain.Activity{}
	see := func(person string, at time.Time, kind string) {
		if person == "" || !w.Contains(at) || v.who.Bot(person) {
			return
		}
		if a, ok := out[person]; ok && (a.Time.After(at) || a.Time.Equal(at) && activityKinds[a.Kind] <= activityKinds[kind]) {
			return
		}
		out[person] = domain.Activity{Time: at, Kind: kind}
	}
	cloned := map[string]time.Time{} // SHA → author time, of every commit the view's clones hold
	for _, r := range v.repos {
		for _, l := range r.landings {
			for _, c := range l.commits {
				see(c.credit, l.time, domain.ActivityLanding)
			}
		}
	}
	kept := make(map[string]bool, len(v.repos))
	for _, r := range v.repos {
		kept[r.key] = true
	}
	for i := range v.facts.Repos {
		rf := &v.facts.Repos[i]
		if !kept[rf.Repo.Key] {
			continue
		}
		for j := range rf.RefCommits {
			c := &rf.RefCommits[j]
			cloned[c.SHA] = c.AuthorTime
			if c.CommitTime.Before(v.now) {
				see(v.who.Credit(c.Author, c.CoAuthors), c.AuthorTime, domain.ActivityCommit)
			}
		}
		for j := range rf.Commits {
			cloned[rf.Commits[j].SHA] = rf.Commits[j].AuthorTime
		}
	}
	defer v.quietIn(out)
	x := v.reviews()
	if x == nil {
		return out
	}
	for i := range x.pulls {
		p := &x.pulls[i]
		if len(x.join[p.Repo]) == 0 {
			continue
		}
		for j := range p.Reviews {
			if r := &p.Reviews[j]; reviewsSubmitted(r) && r.Author.Login != "" {
				see(v.who.Reviewer(r.Author).Key, r.Submitted, domain.ActivityReview)
			}
		}
		if p.Author.Login == "" || p.Author.Bot {
			continue
		}
		author := v.who.Reviewer(p.Author).Key
		see(author, p.Created, domain.ActivityPullRequest)
		for _, c := range p.Commits {
			if at, ok := cloned[c.SHA]; ok && strings.EqualFold(c.AuthorLogin, p.Author.Login) {
				see(author, at, domain.ActivityPullRequest)
			}
		}
	}
	return out
}

// quietIn sets each activity's QuietIn and GoingQuiet at Now: the whole
// days, of 24 hours and rounded down, before 180 days have passed since
// it, -1 once they have (knowledge_loss@1 then counts the person as gone
// quiet), and whether that is within goingQuietDays, which the
// going_quiet detail counts the same way (goingQuiet).
func (v *view) quietIn(acts map[string]domain.Activity) {
	since := v.window(activityDays, 0).Since
	for email, a := range acts {
		a.QuietIn = -1
		if !a.Time.Before(since) {
			a.QuietIn = int(a.Time.Sub(since) / day)
			a.GoingQuiet = a.QuietIn < goingQuietDays
		}
		acts[email] = a
	}
}
