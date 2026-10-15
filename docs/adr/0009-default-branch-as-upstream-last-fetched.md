# ADR 0009: A work tree's default branch is measured as upstream last fetched it

Status: Proposed · 2026-10-14 · Deciders: Joseph Peterson

## Context

Every later command names `Repo.Head`, the commit discovery resolves a repository's default branch to. So this choice decides what "landed" means in every report and every stored run.

A work-tree clone holds two records of its default branch:

- The remote-tracking ref (`refs/remotes/origin/main`). Every `git fetch` moves it to upstream's commit, including the background fetches many editors run.
- The local branch (`refs/heads/main`). Only the user moves it, with pull, commit or reset.

The two differ in three common states:

- fetched but not pulled: the local branch lags behind;
- unpushed commits: the local branch is ahead, with work that never landed;
- `git clone -b feature`: there is no local `main` at all.

The first draft of #4 read the remote-tracking ref only when `origin/HEAD` existed, and the local branch otherwise. Two clones of one repository in the same state then got different Heads, depending on a ref users never think about. `origin/HEAD` is missing in these cases:

- a repository started with `git init` and pushed with `git push -u` (or `gh repo create --push`);
- after `git remote set-head origin --delete`;
- in init-then-fetch setups on git before 2.48.

A review of PR #34 reproduced the split: Head was origin/main in a fresh clone, but the local branch, unpushed commit included, in the same clone without `origin/HEAD`.

## Decision

A work tree is measured at upstream's commit as of the last fetch, whenever the clone knows it:

1. `origin/HEAD`'s target;
2. else `refs/remotes/origin/main`;
3. else `refs/remotes/origin/master`.

Only a clone with none of these refs (no `origin`, or never fetched) falls back to its local `main`, then `master`, then `HEAD`. An `origin/HEAD` whose target no longer exists is an error, with the command that repairs it, not a fall-through to a branch upstream deleted.

Bare repositories keep `HEAD`. For a managed mirror, `HEAD` is upstream's default branch, because sync re-points it on every run.

[discovery.md](../spec/discovery.md#default-branch) holds the rule, and the fixture rows `tracking`, `no-origin-head`, `origin-master` and `dangling-head` pin it ([../../testdata/fixture/SPEC.md](../../testdata/fixture/SPEC.md#discovery-fixture-testdiscover)).

## Alternatives considered

- **Measure the clone as it is.** Use the local branch for every rule, and the remote-tracking ref only when the local branch is missing. It is simpler to explain ("what `git log main` shows") and it counts a solo developer's unpushed commits. It was rejected for two reasons. It reports work that never landed as landed. And a fetched-but-not-pulled clone, the state background fetches leave behind, reports stale numbers although the clone holds fresh ones.
- **Keep the split**: the remote-tracking ref only when `origin/HEAD` exists. Rejected, because Head's meaning would depend on an incidental ref.
- **Ask the forge.** Read the default branch's commit from the GitHub API. Analysis is offline by design (ADR 0004), and a local clone may not be on GitHub at all.

## Consequences

- Unpushed commits never count, in any clone. A user who wants them counted must push.
- A report can disagree with `git log main` in the same clone. It agrees with `git log origin/main`, and the report's "analysis did not fetch" caveat applies as before.
- A work tree without `origin/HEAD` costs one more git process (four instead of three). That process tells a missing `origin/HEAD` from one whose target is gone.
- Known gap: a bare repository is still measured at `HEAD`'s branch in `refs/heads`. For a managed mirror or a plain bare clone, that branch is upstream's. In the bare-clone-plus-worktrees layout, which discovery reads as a bare repository, `refs/heads` holds the user's own branches, so unpushed commits there do count. The fix is to apply this rule to any repository with remote-tracking refs, bare or not. It is left until that layout matters, because it would change what a mirror's Head means.

## Door

Two-way in code: one function and four fixture rows. But every run stores its Head, so a change of rule changes what runs mean from that point on, and a trend that crosses the change compares different refs. Reverting therefore needs a release note, not a migration.

## Revisit if

- Users of repositories without pull requests expect unpushed commits to count.
- Reports disagree with `git log main` often enough to confuse.
- The forge sync (M2, #14) records each default branch's commit, which would let a scan check both local records against upstream.
