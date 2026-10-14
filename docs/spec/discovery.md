# Discovery

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (bare mirrors accepted, clone health checks added) · becomes tests in M0

Repositories are named by positional paths only. There is no config file listing repositories.

```
stillhere scan PATH [PATH ...]
stillhere week PATH [PATH ...]
```

Managed mirrors written by `stillhere sync github` are passed the same way, for example `stillhere scan <mirrors>/<owner>`.

## Rules, in order

1. **Work tree.** `PATH/.git` exists (as a file or a directory) and `git rev-parse --is-inside-work-tree` prints `true`: one repo. Children are not walked, so a checkout that contains nested clones is one repo and the nested clones are not added.
2. **Bare repository.** `git -C PATH rev-parse --is-bare-repository` prints `true`: one repo. Managed mirrors are bare. This check must run before rule 3, because a bare directory has no `.git` child. It replaces the 2026-10-07 rule that warned and skipped bare repos.
3. **Group.** Otherwise, a directory is a group named by its basename. Its members are the immediate children that satisfy rule 1 or 2. There is no deeper walk.
4. Otherwise, the slot is an error. Other paths still run.
5. **Duplicates.** After expansion, the key is the scrubbed `origin` URL string ([git-invocation.md](git-invocation.md#url-scrubbing)) and nothing else. There is no case folding and a trailing `.git` stays. `git@github.com:o/r.git` and `https://github.com/o/r.git` are two repos; equating them would be a later behaviour change with its own test. A repo without `origin`, or whose `origin` scrubs to `<unparsed-remote>`, is keyed by its directory basename: two unparsable remotes are not the same repository. The lexicographically first path wins, and each skip is a `duplicate-repo` warning naming both local paths and the scrubbed URL. A work-tree clone and a managed mirror of the same HTTPS remote are duplicates.
6. Two groups with the same basename get a numeric suffix (`src`, `src-2`), and both are kept.

Worktrees, whose `.git` is a file, are repos. Submodules are not auto-discovered: a submodule is included only when its path is itself a selected repo or an immediate child of a group.

## Default branch

Resolved locally, never fetched.

| Repo | Resolution |
| --- | --- |
| Work tree | `git symbolic-ref --short refs/remotes/origin/HEAD` with a leading `origin/` stripped, if that ref exists; else `main` if `refs/heads/main` exists; else `master`; else the current `HEAD`. |
| Bare | `git symbolic-ref --short HEAD` (verified). Sync re-points a mirror's HEAD at the upstream default branch every run ([git-invocation.md](git-invocation.md#sync-commands-network)). |

The branch name and its resolved SHA are recorded per repo, and every later command names that SHA. Every report states that analysis did not fetch: a stale clone produces a stale report.

## Clone health

Run per repo, before any collector.

| Check | Detection | Warning | Effect |
| --- | --- | --- | --- |
| Shallow | `git rev-parse --is-shallow-repository` prints `true` (verified) | `shallow-clone` | No blame facts, because blame credits every line older than the shallow boundary to the boundary commit. Week facts are produced only if every boundary commit listed in `$GIT_DIR/shallow` is older than the window start; otherwise the repo's week facts carry warning `shallow-window` and are marked incomplete. |
| Partial | `git config --get-regexp '^remote\..*\.promisor$'` reports `true` for any remote, or `extensions.partialClone` is set | `partial-clone` | No line facts: no week numstat and no blame. Commit facts (landings, authors, trailers, subjects) are still collected with `git log` without `--numstat`. |

Verified on 2.50.1: a `--filter=blob:none` clone sets `remote.origin.promisor=true` and `remote.origin.partialclonefilter`, but not `extensions.partialClone`, so detection must not rely on the extension alone. Under `GIT_NO_LAZY_FETCH=1`, both `git log --numstat` (even `-1` at HEAD) and `git blame` fail fatally on the first missing blob, while plain `git log` succeeds. Without this check, a partial clone would surface as a wall of per-file errors rather than one clear warning.
