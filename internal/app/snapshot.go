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

// The warning codes of the snapshot step.
const (
	// warnHeadAfterNow is a repository's warning, and its caveat wherever
	// its numbers are: the run read an older commit than the branch's head,
	// which has landings at or after now.
	warnHeadAfterNow = "head-after-now"
	// warnNoCommitBeforeNow is a run-level warning: the repository's branch
	// has no commit before now, so the run has no facts of it.
	warnNoCommitBeforeNow = "no-commit-before-now"
)

// snapshotHeadAfterNow is the warning of a repository read at snap, older
// than its head: the commit read, when it was committed, and how many
// landings the head holds after it, which no number counts.
func snapshotHeadAfterNow(repo domain.Repo, now time.Time, snap collect.Snapshot) domain.Warning {
	return domain.Warning{Code: warnHeadAfterNow, Repo: repo.Key, Message: fmt.Sprintf(
		"Read as it stood at --now (%s): %s at commit %s, committed %s. Its head, %s, is %s later, at or after --now, "+
			"and no number here counts them.",
		snapshotTime(now), repo.Branch, snapshotShort(snap.Commit), snapshotTime(snap.Time),
		snapshotShort(repo.Head), snapshotLandings(snap.After))}
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
