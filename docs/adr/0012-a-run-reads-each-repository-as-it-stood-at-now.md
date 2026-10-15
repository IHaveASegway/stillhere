# ADR 0012: A run reads each repository as it stood at --now

Status: Proposed · 2026-10-15 · Deciders: Joseph Peterson

## Context

Every metric window ends at `--now`, but until #70 a scan blamed, classified and sized each repository at its default branch's head as discovery resolved it ([ADR 0009](0009-default-branch-as-upstream-last-fetched.md)), whatever its date. With `--now` before the head, the code a run described and the activity it counted were from different moments: people who landed after `--now` held lines while no window held their landings, so `knowledge_loss@1` counted them as gone and `surviving@1` showed them with `landings_365d` 0. The run's provenance named the head, and no caveat said so.

The whole-MVP review measured it (#70). The main fixture at `--now 2026-03-15` gave `knowledge_loss@1` 13 / 34 with 8 people, 4 of them inactive; the same repository as it stood then gives 0 / 18 with 4 people, none inactive. A public repository (spf13/cobra) at `--now 2026-04-20` gave 5,713 / 6,037 with 146 of 153 people inactive. The date form of `--now`, midnight UTC, is always earlier than that day's landings: on the fixture head's own day it gave 34 / 34. `scan --help` recommends `--now` for reproducible output, so the documented use produced it.

## Decision

A run reads each repository at the newest first-parent commit of its default branch whose committer time is before `--now`, strictly, as the windows are half-open. Newest means nearest the branch's head on its first-parent chain: `collect.SnapshotAt` walks that chain from the head (`git rev-list --first-parent --timestamp`) and stops at the first commit committed before `--now`. Blame, classification, sizes, the ignore-revs file and the history read it, and `Repo.Head`, every section's provenance `heads` and `repos[].head` record it ([run model](../spec/codebase.md#run-model)).

- When it is not the head, the repository gets a `head-after-now` warning, a caveat wherever its numbers are, naming the commit read and how many landings came at or after `--now`.
- A repository whose branch has no commit before `--now` yields no facts: a run-level `no-commit-before-now` warning says so, and the run is not partial, since the repository did not exist yet.
- `week` reads the same commit for the same `--now`.

## Alternatives considered

- **Read the head and add only the caveat** (the review's minimum). It costs no git process, and a reader is warned. Rejected: the numbers stay wrong, and a caveat cannot make 34 / 34 usable.
- **The commit with the latest committer time before `--now`.** It reads the true state when a backdated commit sits above newer ones. Rejected: a backdated head (`git rebase --committer-date-is-author-date` at the tip) would not be read even with the wall clock as `--now`, so a run without `--now` would change although no commit is dated in the future.
- **The commit below the lowest one dated at or after `--now`** (a commit cannot land before its parent, so its landing time is at least the latest committer time below it). It is the most faithful when clocks err only backwards. Rejected: one future-dated commit anywhere in the history, a machine's wrong clock years ago, would freeze every later run below it until its date passed.
- **The reflog**, which records when the ref moved. Rejected: it is local, expires, and is absent in fresh clones and mirrors, so two clones would read different commits.

## Consequences

- With the wall clock as `--now`, nothing changes unless the head itself is dated in the future. A reproducible report at a fixed `--now` no longer moves when the clone fetches, except the caveat's count of later landings.
- What lies below the commit read is read with it, whatever its date: a commit dated after `--now` below an older-dated one is in the snapshot. A chain whose committer times are not monotone has no single true state at `--now`, and chain order keeps the wall-clock case exact.
- One more git process per repository, stopped once the commit is found: usually after one line.
- A stored run's head means the commit read from #70 on. Runs before it read the fetched head and carry no caveat.

## Door

Two-way in code: one collector function and one call in the pipeline. But, as for ADR 0009, every run stores its head, so the rule changes what runs at an earlier `--now` mean from that point on, and a trend that crosses the change compares different code. Reverting needs a release note, not a migration.

## Revisit if

- Users expect `--now` to move only the windows, to read today's code with an old window.
- Backdated chains turn out to be common enough that the snapshot often holds commits dated after `--now`: count, per scan, the commits below the snapshot dated after it.
- Sync starts recording when each mirror's default branch moved, which would give the true state at `--now` for mirrors.
