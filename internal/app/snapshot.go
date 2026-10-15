package app

import (
	"fmt"
	"time"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// A run reads each repository as it stood at --now (#70,
// docs/spec/codebase.md#run-model): every window ends at now, so the code a
// run blames, classifies and sizes must be the code of that moment too.
// Otherwise a scan with --now before the fetched head credits lines written
// after now to people whose landings no window holds yet, and
// knowledge_loss@1 counts newcomers as gone. collect.SnapshotAt finds the
// commit; this file words what the run says about it.
//
// The commit is the newest one on the branch's first-parent chain committed
// before now, which is the repository as it stood then wherever committer
// times grow toward the head. Where they do not, a backdated commit above
// newer ones or a clock that ran ahead, the commit read holds landings dated
// at or after now, and the run says that too (dated-after-now), rather than
// claim a moment its code does not describe.

// The warning codes of the snapshot step.
const (
	// warnHeadAfterNow is a repository's warning, and its caveat wherever
	// its numbers are: the run read an older commit than the branch's head,
	// which has landings at or after now.
	warnHeadAfterNow = "head-after-now"
	// warnDatedAfterNow is a repository's warning, and its caveat wherever
	// its numbers are: below the commit read, landings dated at or after now
	// hold lines that blame counts and no window does.
	warnDatedAfterNow = "dated-after-now"
	// warnNoCommitBeforeNow is a run-level warning: the repository's branch
	// has no commit before now, so the run has no facts of it.
	warnNoCommitBeforeNow = "no-commit-before-now"
)

// snapshotHeadAfterNow is the warning of a repository read at snap, older
// than its head: the commit read, when it was committed, and how many
// landings the head holds after it, which no number counts.
func snapshotHeadAfterNow(repo domain.Repo, now time.Time, snap collect.Snapshot) domain.Warning {
	return domain.Warning{Code: warnHeadAfterNow, Repo: repo.Key, Message: fmt.Sprintf(
		"Read at commit %s of %s, committed %s, the newest on its first-parent chain committed before --now (%s). "+
			"Its head, %s, is %s later, at or after --now, and no number here counts them.",
		snapshotShort(snap.Commit), repo.Branch, snapshotTime(snap.Time), snapshotTime(now),
		snapshotShort(repo.Head), snapshotLandings(snap.After))}
}

// snapshotDatedAfterNow is the warning of a repository whose commit read
// sits above landings dated at or after now (collect.Snapshot.Below): blame
// counts the lines they hold, while no window counts them, so their authors
// can look gone. Only a run that blames says it: without line facts no
// number reads those lines.
func snapshotDatedAfterNow(repo domain.Repo, now time.Time, snap collect.Snapshot) domain.Warning {
	verb, hold, them, whose := "are", "they hold", "them", "their authors"
	if snap.Below == 1 {
		verb, hold, them, whose = "is", "it holds", "it", "its author"
	}
	return domain.Warning{Code: warnDatedAfterNow, Repo: repo.Key, Message: fmt.Sprintf(
		"%s on %s's first-parent chain below commit %s, the one read, %s dated at or after --now (%s), "+
			"as a backdated commit or a clock that ran ahead leaves: blame counts the lines %s, but no window counts %s, "+
			"so %s can look inactive.",
		snapshotLandings(snap.Below), repo.Branch, snapshotShort(snap.Commit), verb, snapshotTime(now), hold, them, whose)}
}

// snapshotNoCommit is the run-level warning of a repository whose branch
// has no commit before now: the repository did not exist yet, as far as
// its branch shows, so the run leaves it out and says why. A shallow
// clone's branch shows only the history the clone holds.
func snapshotNoCommit(repo domain.Repo, now time.Time, snap collect.Snapshot) domain.Warning {
	held := "its branch " + repo.Branch + " has"
	if repo.Shallow {
		held = "the shallow history of its branch " + repo.Branch + " has"
	}
	return domain.Warning{Code: warnNoCommitBeforeNow, Message: redact.Scrub(fmt.Sprintf(
		"Left out %s: %s no commit before --now (%s), only %s at or after it, so the repository is in none of these numbers.",
		repo.Name, held, snapshotTime(now), snapshotLandings(snap.After)))}
}

// snapshotTime writes an instant as every warning does: RFC 3339, in UTC.
func snapshotTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// snapshotShort is a commit's first 12 hex digits, as warnings name commits.
func snapshotShort(sha string) string { return sha[:min(12, len(sha))] }

// snapshotLandings counts landings in words.
func snapshotLandings(n int) string {
	if n == 1 {
		return "1 landing"
	}
	return fmt.Sprintf("%d landings", n)
}
