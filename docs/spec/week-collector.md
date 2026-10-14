# Week collector

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (trailers, subjects, reverts, landing map; then by #6: `-z` numstat, own patches, pinned diff options, clone health) · becomes tests in M0 (M1 for reverts and the landing map)

This collector answers one question: what landed on the default branch in the window? It produces one row per landing commit, credited to that commit's author.

## The log

```
git <common> log --first-parent --diff-merges=first-parent \
  --since=<window start> --until=<window end> \
  -z --numstat -M --root --diff-algorithm=myers --no-textconv \
  --no-color --encoding=UTF-8 --date=iso-strict \
  --pretty='format:%x00%H%x1f%P%x1f%cI%x1f%aI%x1f%aN%x1f%aE%x1f%(trailers:key=Co-authored-by,valueonly,separator=%x1e,unfold)%x1f%s' \
  <sha> --
```

`<common>` is the analysis prefix in [git-invocation.md](git-invocation.md#analysis-commands-offline). Partial clones run the same command without `--numstat`, its diff options and `--diff-merges=first-parent` ([shallow and partial clones](#shallow-and-partial-clones)). `-z` and the pinned options were added by #6; [Parsing](#parsing) and [Repository config](#repository-config) say why.

| Trap | Rule and reason |
| --- | --- |
| Merge diffs | `--first-parent` limits the walk, and the diff is taken against the first parent. Never use `-m`: it means `--diff-merges=separate`, one diff per parent, so the second-parent diff gets summed too. On 2.50.1, `--first-parent` alone already yields first-parent diffs for merges (verified); the explicit flag documents the intent. |
| `--no-merges` | Wrong. It drops the merge commit and keeps side-branch commits whose own dates fall in the window, so it misses a merge of older commits and counts a side commit that never landed on its own. |
| Mailmap placeholders | Use capital `%aN`/`%aE`, which apply the mailmap even with `log.mailmap=false` and even under `--no-use-mailmap`. Lowercase `%an`/`%ae` print the raw headers (verified). Repo-local config survives `GIT_CONFIG_GLOBAL=/dev/null`, so a lowercase format splits one person in two wherever a repo has turned `log.mailmap` off. |
| Renames | Never pass `--no-renames` here. With default rename detection, a pure rename is one numstat row; `--no-renames` turns it into a full delete plus a full add and inflates `lines_added`. `-M` pins that default against a repository's own `diff.renames=false`, which does the same (verified). |
| Date field | `--since` filters on committer date. The report records `date_field = "committer"` and `walk = "first-parent"`. |
| Window boundaries | `--since` and `--until` both include their boundary instant (verified), so the collector keeps a landing only when `Window.Contains` its committer time: `[since, until)`. |

## Parsing

A record starts at NUL. Its header fields are separated by U+001F, in this order:

1. commit SHA;
2. parent SHAs, space-separated;
3. committer time;
4. author time;
5. mailmapped name;
6. mailmapped email;
7. co-author trailer values, separated by U+001E (verified: the key matches case-insensitively, so `Co-Authored-By:` is found; `unfold` joins a value continued on an indented line, so no field holds a newline);
8. subject.

The subject is last, so a stray U+001F inside it cannot shift the other fields. Error messages never quote a header: it holds names, emails and the subject.

With `-z`, every numstat row ends with NUL and every path is printed exactly as stored, never quoted. A row is `<added>\t<removed>\t<path>`; both counts are `-` for a binary file, whose insertions count as 0. A rename prints an empty path, then the source and the **destination** as two more NUL-terminated fields, `<added>\t<removed>\t\0<source>\0<destination>\0`, and insertions are attributed to the destination only. Around the rows, where the separators and the newline before the first row fall depends on the format kind (`format:` separates commits, `tformat:` terminates them) and on whether the commit printed a diff (verified), so the reader does not count them. It classifies each NUL-separated token: an empty token is a separator; a token that starts with the newline, or reads as a row, is a row; any other token is a header, possibly followed by the newline and the first row; and the two tokens after a rename row are its paths, whatever bytes they hold.

**Why `-z` (changed by #6).** The 2026-10-07 plan parsed git's human-readable rename forms, with warning `numstat-rename` for any row it could not read. Those forms cannot be read back in every case. The `git` column is what 2.50.1 prints without `-z` (verified):

| git prints | Destination | What a parser of that form does |
| --- | --- | --- |
| `dir/{old => new}/file.py` | `dir/new/file.py` | right: prefix + new + suffix |
| `x/{y => }/z.py` | `x/z.py` | prefix + new + suffix gives `x//z.py` |
| `a => b => c` | `b => c`, renamed from `a` | cannot tell it from `a => b` renamed to `c` |
| `{x => y}.py` | `{x => y}.py`, a modified file, not a rename | credits `y.py`, and no warning fires |
| `"sp ace/f\"q.py" => "sp ace/f\"q2.py"` | `sp ace/f"q2.py` | no brace rule covers quoted names |
| `"\303\274/na\303\257ve.py"` | `ü/naïve.py` | must decode octal escapes (`core.quotePath`) |

With `-z` no row is ambiguous, nothing is dropped, and the collector never emits `numstat-rename`. `TestNumstatRename` builds each form above and asserts the destination, the source and the counts in week and scan runs.

## lines_added

One predicate, shared by the collector and the tests: sum the numstat insertions (blank lines included) on every path that [classification.md](classification.md) assigns a role. Denied, too-big, header-generated and unclassified paths add 0, and the commit still counts. A dependency-bot commit that touches only lockfiles is still a landing, which is why those paths are not excluded by pathspec. `prod_lines_added` is the `prod` subset. Neither number is comparable to still-here non-blank lines, and neither is ever named `nonblank_lines`.

Rows are per person, per repo, with `commits`, `lines_added`, and `prod_lines_added`. A person whose only rows are bot rows appears only in the bot table. The default sort is `lines_added` descending, then email ascending; `--week-sort commits` exists. Tests assert the set of rows, not the order among ties.

## Window

- `--until` is the `--now` instant. It defaults to the system clock in UTC, so tests pass it and don't depend on the clock. It accepts an RFC 3339 time (`2026-10-07T00:00:00Z`, the profile of ISO 8601 with an explicit offset) or a date (`2026-10-07`), which means midnight UTC.
- `--since` is `--now` minus the effective number of days: `[week].days`, overridden by `--days`. `--week-sort` overrides `[week].sort` the same way.
- The report's `window` stores the effective values plus `days_from` and `sort_from` (`cli` or `policy`). The policy hash stays the hash of the file's bytes.
- `--ignore-revs-file` is accepted on `week` and ignored with one stderr line, because it is not a `git log` option.

Budget: `week` alone finishes in under 60 s for 100 repos.

## AI-assisted changes

A landing is tagged AI-assisted when its author or any co-author matches a pattern in the versioned `[ai]` policy table. A scan also knows the commits a merge landing brought in, and tags the landing when any of them matches ([ai_assisted@1](../metrics.md#ai_assisted1)); a week run reads only the landing commit. Per the 2026-10-14 decision, the person gets the credit and the change carries the tag; an agent is never a row of its own. Defaults:

| Tool | Signal | Rule |
| --- | --- | --- |
| Claude Code | co-author `<name> <noreply@anthropic.com>` | Match the email. The name varies ("Claude", "Claude Code", a model name). |
| Aider | co-author `aider (<model>) <aider@aider.chat>` | Match the email. |
| Copilot coding agent | Copilot is the commit author, and the person is a co-author | The author is a bot per [git-invocation.md](git-invocation.md#identity-mailmap-and-bots). Credit the first human co-author and tag the change AI-assisted. |

- Co-authors are canonicalised in week and scan runs alike, with one `git check-mailmap --stdin` per repository, so a Copilot commit's human co-author is the same person as that human's own commits. A trailer value without `<email>` names no identity and is dropped before `check-mailmap` (which dies on it below 2.47), as is a second trailer for the same canonical email. Trailer order is kept, because credit reads the first human. `internal/collect/coauthors.go` holds this reader; the blame collector calls it too.
- Cursor's trailer string is unverified (sources show both `Made-with: Cursor` and `Co-authored-by: Cursor`), so it is not a default.
- Editor completions leave no trace, so every AI-assisted figure is a lower bound.
- Human co-authors are facts too: GitHub squash merges add a `Co-authored-by` line for every other author of the pull request, and those must never be tagged AI-assisted.

## Subjects and reverts

The subject is collected for Conventional-Commit typing (M5). It stays local and is never sent to any AI.

A revert's subject is `Revert "<original subject>"`, but the reverted SHA is in the body, as `This reverts commit <sha>.` (verified). `%b` cannot share a stream with `--numstat`, because body lines can look like numstat rows. Revert detection is therefore a second query, over the range the durability metric needs:

```
git <common> log --first-parent -E --grep='^This reverts commit [0-9a-f]{7,64}' \
  --since=<window start> --until=<window end> \
  -z --no-color --encoding=UTF-8 --pretty='format:%x00%H%x1f%B' <sha> --
```

Each revert landing links to the commit it reverts. Reverts made with GitHub's revert button and then squash-merged may not carry that line; the pull-request link from the GitHub sync covers them (verify in M2).

- The range is the scan's landing window, and a revert is kept only when its landing is in the window (`Window.Contains`).
- `%B`, the whole message, not `%b`: `--grep` searches the subject too.
- Every line that matches links the revert to the SHA it names; a revert of a merge continues `..., reversing changes made to <sha>.`, and only the first SHA is the reverted commit.
- A full-length SHA is kept as written. An abbreviated one is resolved with one `git cat-file --batch-check` over `<name>^{commit}`, and dropped when it names no commit or several.

## Landing → commit map

Blame credits a line to the commit that wrote it. For a merged branch, that is a side-branch commit, not the landing merge. Durability ([../metrics.md](../metrics.md)) needs the map from each landing to the commits it brought in:

```
git <common> rev-list <landing>^1..<landing>
```

- **Merge landing:** the result is the merge itself plus every commit it brought in. For a two-parent merge that is the merge plus `M^1..M^2` (verified); the `^1..` form also covers octopus merges.
- **Squash or rebase landing:** maps to itself.
- **Root commit:** maps to itself; `rev-list L^1..L` exits 128 on it, because `L^1` does not exist.
- **Lines blamed to the merge commit itself** are conflict resolutions, and belong to the merge's author.

The map is stored as facts (landing SHA, commit SHA) alongside the landings, in scan runs only.

**One walk, not one process per merge.** The collector computes every landing's map from one command, so a repository with a thousand merge landings starts one git process rather than a thousand:

```
git <common> rev-list --parents <newest landing> ^<oldest landing's first parent> --
```

It then follows first parents from the newest landing down to the oldest; that chain partitions the walk. Going up the chain from the oldest commit, each chain commit claims, by depth-first search through parents, every walked commit that no older chain commit claimed. What a chain commit `C` claims is exactly `rev-list C^1..C`: `C^1`, the chain commit below it, has already claimed everything reachable from it, and the walk left out only commits that `C^1` reaches too. Claims of chain commits outside the window, such as one with a wrong clock, are discarded. Checked against `rev-list L^1..L` for every landing of both fixtures, and of a repository with a future-dated first-parent commit and a side branch that merged the default branch back in. Without a merge landing in the window, no walk runs.

## Own patches (scan)

`FileChange.AddedNonBlank` and `RemovedNonBlank` feed `durable@1` and `simplifying@1` ([../metrics.md](../metrics.md#change-facts-shared-by-durable1-and-simplifying1)). They come from each commit's own patch, for every non-merge commit in a window landing's map, the landings included:

```
git <common> log --no-walk --stdin --diff-merges=off -p -U0 -w \
  -M --root --diff-algorithm=myers --no-textconv \
  --no-color --no-ext-diff --default-prefix --pretty='format:%x00%H' --
```

- **Never `--first-parent`.** Under it, git prints a merge's first-parent diff, which credits the merged branch's lines to the person who merged it. With `--no-walk` each commit is diffed against its first parent, a root commit against the empty tree, and a merge prints nothing; merges are also left out of stdin.
- **`-w`** drops whitespace-only changes: a re-indent adds and removes nothing, and a file whose only changes are whitespace prints no section at all (verified).
- **Hunks are read by their counts.** With `-U0` a hunk is exactly the lines its `@@ -a,b +c,d @@` header counts, so an added line whose text is `++ b/x` (printed `+++ b/x`) is never read as a header. A line counts when anything is left after trimming whitespace; the blame collector must use the same predicate, so that added and surviving lines agree.
- **Paths come from the `---` and `+++` lines**, never from `diff --git a/a b/b => c`. Git quotes a path that holds a quote, a backslash, a control character or a non-ASCII byte (octal escapes), and ends the line with a tab when the path holds a space. A deletion is named by its `---` line. A typechange prints two files with one path, which are summed.
- Each file with hunks is joined to its numstat row by (commit, destination path). Both commands pin the same rename detection, so a file with hunks and no row is a bug in how a path was read, and fails the repository rather than passing silently.

Numstat rows of the commits a merge brought in, which the first-parent log never prints, come from one more `git log --no-walk --stdin --diff-merges=off -z --numstat` with the same diff options and format as the landing log. So every row carries both counts against the commit's first parent; a merge landing's numstat is its first-parent diff, as `week` counts it, and its own patch is empty in v1.

## Repository config

Repository-local config is read on every command; only command-line options beat it. These options pin what a repository could otherwise change. Each was checked on 2.50.1 by setting the config and comparing the output with and without the option: in the repository's own config, except `diff.algorithm`, set with `git -c`.

| Option | Setting it overrides | Effect without it |
| --- | --- | --- |
| `-M` | `diff.renames=false` | a rename becomes a full delete plus add |
| `--root` | `log.showRoot=false` | the root commit prints no diff; row 1 of the change oracles loses its 7 lines |
| `--no-textconv` | a `diff=<driver>` attribute with `diff.<driver>.textconv` | `log -p` runs the repository's program and counts its output |
| `--no-color` | `color.diff=always` | ANSI escapes inside the patch |
| `--default-prefix` | `diff.noprefix`; by git's documentation also `diff.srcPrefix`, `diff.dstPrefix` and `diff.mnemonicPrefix` | patch paths lose their `a/` and `b/` |
| `--diff-algorithm=myers` | `diff.algorithm` | one change counts differently from one repository to the next: on Bram Cohen's patience-diff example, 10 non-blank lines added and 11 removed under myers, 9 and 10 under patience |
| `--encoding=UTF-8` | `i18n.logOutputEncoding` | names and subjects arrive in that encoding: `ISO-8859-1` prints the `ë` of `Zoë` as the single byte `0xEB`, which is not UTF-8 |

`--no-textconv` also narrows the residual risk named in [git-invocation.md](git-invocation.md#analysis-commands-offline) for history: no `log` the collector runs starts a textconv program.

## Shallow and partial clones

- **Partial clone:** commit facts only (landings, map, authors, trailers, subjects, reverts), and no `FileChange` rows. The landing log drops `--numstat`, its diff options and `--diff-merges=first-parent`: that option alone makes git diff every commit, and rename detection then reads blobs the clone does not have, which fails under `GIT_NO_LAZY_FETCH` even with nothing to print (verified).
- **Shallow clone:** a parentless commit is the clone's boundary, whose diff against the empty tree is its whole tree, not its change (verified: in a `--depth 3` clone of the main fixture, numstat gives Ada's one-line commit, the boundary, 73 inserted lines). It keeps its commit and landing facts but gets no `FileChange` rows. The facts carry warning `shallow-window` when a boundary reachable from the scanned SHA has a committer time at or after the window start, or when a landing in the window brought in a boundary commit. A boundary is told from a true root only by the shallow file, which the collector does not read, so a true root inside a shallow clone's window is treated as a boundary too.

## Repository age

`RepoFacts.FirstCommit` is the author time of the earliest root commit reachable from the scanned SHA. A repository can have several roots (a merged-in history), so take the minimum:

```
git <common> log --max-parents=0 -z --pretty='format:%H%x1f%aI%x1f%cI' <sha> --
```

Scan runs only: week runs leave it zero and run this command only in a shallow clone, for the boundary check above. In a shallow clone the roots are hidden behind the boundary, so `FirstCommit` is left zero (unknown) rather than reporting the boundary's date as the repository's age.
