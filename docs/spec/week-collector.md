# Week collector

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (trailers, subjects, reverts, landing map; then by #6: `-z` numstat, own patches, pinned diff options, clone health; then by #6's review: `--since-as-filter`, NUL-separated text fields, Unix times, reverts over the landing map, six more pinned settings; then by #13: no `.gitattributes` read by any log, the `-c` settings moved into the runner's prefix, one non-blank predicate) · becomes tests in M0 (M1 for reverts and the landing map)

This collector answers one question: what landed on the default branch in the window? It produces one row per landing commit, credited to that commit's author.

## The log

```
git <common> --attr-source=<empty tree> \
  log --first-parent --diff-merges=first-parent \
  --since-as-filter=<window start> --until=<window end> \
  -z --numstat -M -l1000 --root --diff-algorithm=myers --no-textconv \
  --ignore-submodules=none --submodule=short \
  --no-color --encoding=UTF-8 \
  --pretty='format:%x00%H%x1f%P%x1f%ct%x1f%at%x00%aN%x00%aE%x00%s%x00%(trailers:key=Co-authored-by,valueonly,separator=%x1e,unfold)%x00' \
  <sha> --
```

`<common>` is the analysis prefix in [git-invocation.md](git-invocation.md#analysis-commands-offline), which pins `core.bigFileThreshold`, `diff.default.binary`, `core.commentChar`, `trailer.separators` and `core.ignoreCase` for every command. Partial clones run the same command without `--numstat`, its diff options and `--diff-merges=first-parent` ([shallow and partial clones](#shallow-and-partial-clones)). `-z`, the pinned options and settings, `--since-as-filter` and the field layout were added by #6, the attribute source by #13; [Parsing](#parsing), [Repository config](#repository-config) and [Attributes](#attributes-none-from-any-tree) say why.

| Trap | Rule and reason |
| --- | --- |
| Merge diffs | `--first-parent` limits the walk, and the diff is taken against the first parent. Never use `-m`: it means `--diff-merges=separate`, one diff per parent, so the second-parent diff gets summed too. On 2.50.1, `--first-parent` alone already yields first-parent diffs for merges (verified); the explicit flag documents the intent. |
| `--no-merges` | Wrong. It drops the merge commit and keeps side-branch commits whose own dates fall in the window, so it misses a merge of older commits and counts a side commit that never landed on its own. |
| Mailmap placeholders | Use capital `%aN`/`%aE`, which apply the mailmap even with `log.mailmap=false` and even under `--no-use-mailmap`. Lowercase `%an`/`%ae` print the raw headers (verified). Repo-local config survives `GIT_CONFIG_GLOBAL=/dev/null`, so a lowercase format splits one person in two wherever a repo has turned `log.mailmap` off. |
| Renames | Never pass `--no-renames` here. With default rename detection, a pure rename is one numstat row; `--no-renames` turns it into a full delete plus a full add and inflates `lines_added`. `-M` pins that default against a repository's own `diff.renames=false`, which does the same (verified). |
| Date field | `--since-as-filter` and `--until` filter on committer date. The report records `date_field = "committer"` and `walk = "first-parent"`. |
| Backdated commits | Never plain `--since`. It stops the walk at the first commit older than the date, and under `--first-parent` there is only one chain, so one first-parent commit whose committer time is older than the window (`git rebase --committer-date-is-author-date`, `git am --committer-date-is-author-date`, a wrong clock) hides every landing below it: week and scan saw no landing at all in a window below such a commit (verified). `--since-as-filter` (git 2.37) walks the whole chain and only filters. The price is reading every first-parent commit's header: on a 200,000-commit linear history, with a one-week window at its top, 0.395 s instead of 0.015 s without a commit-graph, and 0.042 s instead of 0.016 s with one, which `git gc` writes by default (best of three, 2.50.1). Filtered commits print no diff, so numstat costs what it did. |
| Window boundaries | `--since-as-filter` and `--until` both include their boundary instant (verified), so the collector keeps a landing only when `Window.Contains` its committer time: `[since, until)`. |

## Parsing

A record starts at NUL. Its header holds the fields whose shape is fixed, separated by U+001F:

1. commit SHA;
2. parent SHAs, space-separated;
3. committer time, in Unix seconds;
4. author time, in Unix seconds.

Four fields of free text follow, each ending with NUL, read by position whatever bytes they hold:

5. mailmapped name;
6. mailmapped email;
7. subject;
8. co-author trailer values, separated by U+001E (verified: the key matches case-insensitively, so `Co-Authored-By:` is found; `unfold` joins a value continued on an indented line).

**Why this layout (changed by #6's review).** Free text never shares a token with another field. git accepts U+001F inside a name, an email or a trailer value. The earlier layout separated all eight fields with U+001F and put the subject last, so one such byte shifted every field after it: a trailer `Co-authored-by: Evil<U+001F>Name <evil@x>` lost its co-author and put an email address into the subject, while `coAuthors` read the same commit correctly (verified). No free-text field can hold a NUL: git reads a commit message only up to its first NUL, and `git fsck` rejects a NUL in a commit header as `nulInHeader` (both verified). A U+001E inside a trailer value splits that value; `coAuthors` reads the same format with the same parser, so history and blame still give a commit the same co-authors. Times are read as Unix seconds (`%ct`, `%at`) because git accepts any offset up to ±1400 with any minute value, and `%cI` prints `+0099` as `+00:99`, which is not RFC 3339: one such commit failed the whole repository (verified). Error messages never quote a free-text field.

With `-z`, every numstat row ends with NUL and every path is printed exactly as stored, never quoted. A row is `<added>\t<removed>\t<path>`; both counts are `-` for a binary file, whose insertions count as 0. A rename prints an empty path, then the source and the **destination** as two more NUL-terminated fields, `<added>\t<removed>\t\0<source>\0<destination>\0`, and insertions are attributed to the destination only. After the fields, where the separators and the newline before the first row fall depends on the format kind (`format:` separates commits, `tformat:` terminates them), so the reader does not count them (verified). It classifies every token that is not a positional field: an empty token is a separator; a token that starts with the newline, or reads as a row, is a row; any other token is the next commit's header; and the two tokens after a rename row are its paths, whatever bytes they hold.

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

A revert's subject is `Revert "<original subject>"`, but the reverted SHA is in the body, as `This reverts commit <sha>.` (verified). `%b` cannot share a stream with `--numstat`, because body lines can look like numstat rows. Revert detection is therefore a second query, in scan runs, with every commit of every window landing's [map](#landing--commit-map), the landings included, on standard input:

```
git <common> log --no-walk --stdin -E --grep='^This reverts commit [0-9a-f]{7,64}' \
  -z --no-color --encoding=UTF-8 --pretty='format:%x00%H%x1f%B' --
```

Each revert landing links to the commit it reverts. A *revert landing* is a landing whose map holds a commit whose message carries that line: the landing itself for a squash, rebase or direct revert, or a commit that a merge landing brought in. Reverts made with GitHub's revert button and then squash-merged may not carry that line; the pull-request link from the GitHub sync covers them (verify in M2).

- **Over the map, never `--first-parent` (changed by #6's review).** A pull request merged with a merge commit, GitHub's default merge method, lands as a merge whose message names no revert; the revert commit sits on the side branch the merge brought in. A first-parent query found no link for a `--no-ff` merge of the branch that `git revert -m 1` made (verified), so neither metric saw the revert: `simplifying@1` would have kept the reverted landing and credited the lines the revert removed, and `durable@1` would have credited the lines a revert restores. The link's `SHA` is the landing whose map holds the revert commit, so `domain.Revert` keeps its meaning (a revert landing, and the commit it names), and the metrics read it unchanged.
- **A commit and its revert in one landing.** A branch that reverts one of its own commits before it merges is a landing whose revert names a commit of its own map, so the landing is both a revert landing and a reverted one: neither metric counts any of its commits, including the work left on the branch. A merge that brings in a revert next to new work loses that work the same way. Telling the revert commit from the rest needs revert facts at commit grain, a metrics decision: [#43](https://github.com/IHaveASegway/stillhere/issues/43).
- A revert counts only when the landing that brought it in is in the window: the query reads window landings' maps only.
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
git <common> --attr-source=<empty tree> \
  log --no-walk --stdin --diff-merges=off -p -U0 -w \
  -M -l1000 --root --diff-algorithm=myers --no-textconv \
  --ignore-submodules=none --submodule=short \
  --no-color --no-ext-diff --default-prefix --pretty='format:%x00%H' --
```

- **Never `--first-parent`.** Under it, git prints a merge's first-parent diff, which credits the merged branch's lines to the person who merged it. With `--no-walk` each commit is diffed against its first parent, a root commit against the empty tree, and a merge prints nothing; merges are also left out of stdin.
- **`-w`** drops whitespace-only changes: a re-indent adds and removes nothing, and a file whose only changes are whitespace prints no section at all (verified).
- **Hunks are read by their counts.** With `-U0` a hunk is exactly the lines its `@@ -a,b +c,d @@` header counts, so an added line whose text is `++ b/x` (printed `+++ b/x`) is never read as a header. A line counts when anything is left after trimming whitespace. One function decides it for both collectors, `nonBlank` in `internal/collect/scan.go`, so `durable@1` divides the lines blame keeps by lines counted the same way (changed by #13: history had its own copy, `strings.TrimSpace` on a string where blame used `bytes.TrimSpace` on bytes; the two agreed on 2,000,000 random strings built from every Unicode space, invalid UTF-8 and zero-width characters, but nothing kept them agreeing).
- **Paths come from the `---` and `+++` lines**, never from `diff --git a/a b/b => c`. Git quotes a path that holds a quote, a backslash, a control character or a non-ASCII byte (octal escapes), and ends the line with a tab when the path holds a space. A deletion is named by its `---` line. A typechange prints two files with one path, which are summed.
- Each file with hunks is joined to its numstat row by (commit, destination path). Both commands pin the same rename detection, so a file with hunks and no row is a bug in how a path was read, and fails the repository rather than passing silently.

Numstat rows of the commits a merge brought in, which the first-parent log never prints, come from one more `git log --no-walk --stdin --diff-merges=off -z --numstat` with the same settings, diff options and format as the landing log. So every row carries both counts against the commit's first parent; a merge landing's numstat is its first-parent diff, as `week` counts it, and its own patch is empty in v1.

## Repository config

Repository-local config is read on every command; only command-line options and `-c` settings beat it. These pin what a repository could otherwise change. Each was checked on 2.50.1 by setting the config and comparing the output with and without the option: in the repository's own config, except `diff.algorithm`, set with `git -c`. The `-c` settings are in the runner's common prefix, so every command gets them (changed by #13: the first three were per call, before the subcommand of the landing, brought-in, own-patch and co-author logs), because no `log` option overrides them; each value is git's default. The options stay where each log is built: an option also beats the more specific config a `-c` cannot reach, and [git-invocation.md](git-invocation.md#repository-settings-that-change-facts) records, setting by setting, what each one changed and why it is pinned where it is.

| Option | Setting it overrides | Effect without it |
| --- | --- | --- |
| `-c core.bigFileThreshold=512m` (prefix) | `core.bigFileThreshold` | every blob above it is binary: under `10`, every row became binary with 0 lines, and a typechange row got more non-blank lines from the patch than numstat counted |
| `-c core.commentChar=#` (prefix) | `core.commentChar`, and `core.commentString`, the same setting | git's trailer parser skips comment lines, so `C` hides every `Co-authored-by:` line, and with it AI-assisted tags and Copilot credit |
| `-c trailer.separators=:` (prefix) | `trailer.separators` | without `:` no line is a trailer: `#` hides every co-author |
| `-c diff.default.binary=auto` (prefix) | `diff.default.binary`, the driver of every file that no `diff` attribute names | `true` makes every such file binary: numstat `-` `-` and no hunks, so it adds no lines while blame still counts them |
| `-c core.ignoreCase=false` (prefix) | `core.ignorecase=true`, which git on macOS and Windows writes into every repository it creates or clones | attribute patterns match in any case: `*.PY -diff` in `info/attributes` made every `.py` file binary |
| `--submodule=short` | `diff.submodule` | `log` or `diff` prints a gitlink change as a summary line with no hunk, so the patch counts 0 lines where numstat counts 1 and 1 |
| `--ignore-submodules=none` | `diff.ignoreSubmodules`, and `submodule.<name>.ignore` in the config or in the checked-out `.gitmodules` | `all` hides every gitlink change from numstat and from the patch; `-c diff.ignoreSubmodules=none` does not override the per-submodule setting, which the option does (verified) |
| `-M` | `diff.renames=false` | a rename becomes a full delete plus add |
| `-l1000` | `diff.renameLimit` | above the limit, git skips the inexact part of rename detection, so `1` made a full delete plus add of every rename that also edits the file. 1000 is git's default: 1000 such renames in one commit are detected and 1001 are not, with or without `-l1000` (verified) |
| `--root` | `log.showRoot=false` | the root commit prints no diff; row 1 of the change oracles loses its 7 lines |
| `--no-textconv` | a `diff=<driver>` attribute with `diff.<driver>.textconv` | `log -p` runs the repository's program and counts its output |
| `--no-color` | `color.diff=always` | ANSI escapes inside the patch |
| `--default-prefix` | `diff.noprefix`; by git's documentation also `diff.srcPrefix`, `diff.dstPrefix` and `diff.mnemonicPrefix` | patch paths lose their `a/` and `b/` |
| `--diff-algorithm=myers` | `diff.algorithm` | one change counts differently from one repository to the next: on Bram Cohen's patience-diff example, 10 non-blank lines added and 11 removed under myers, 9 and 10 under patience |
| `--encoding=UTF-8` | `i18n.logOutputEncoding` | names and subjects arrive in that encoding: `ISO-8859-1` prints the `ë` of `Zoë` as the single byte `0xEB`, which is not UTF-8 |

`--no-textconv` also narrows the residual risk named in [git-invocation.md](git-invocation.md#analysis-commands-offline) for history: no `log` the collector runs starts a textconv program. These settings change nothing, checked on a history with ten renames in one commit, a copy, a gitlink, a typechange and co-author trailers (verified): `diff.interHunkContext`, `diff.suppressBlankEmpty`, `core.quotePath=false`, `diff.renames=copies`, `diff.mnemonicPrefix`, `diff.relative`, `diff.context`, `diff.indentHeuristic`, `diff.orderFile`, `diff.wordRegex`, `log.showSignature`, `log.follow` and `format.pretty`. `diff.ignoreSubmodules=all` and `diff.renameLimit=1` change nothing on a history without a gitlink or without a rename that also edits the file, which is how an earlier check missed them; both are pinned above.

**Not pinned: trailer aliases.** A `trailer.<alias>.key` in the repository's config renames trailer keys before `%(trailers)` filters them, matching a key by prefix: `trailer.co-authored-by.key=Signed-off-by` hides every co-author, and `trailer.cab.key=Co-authored-by` makes co-authors of `cab:` lines and of `Co:` lines too (verified). A `-c` cannot remove an alias whose name it does not know, and pinning `trailer.co-authored-by.key=Co-authored-by` would add one: on its own it turned a `Co:` line into a co-author (verified). Like a textconv driver, an alias is a deliberate setting in a repository the user already trusts, so it is named here rather than papered over. `trailer.<alias>.cmd` and `.command` do not run during `log` (verified).

## Attributes: none from any tree

Every `log` that can print a diff (the landing, brought-in and own-patch logs) starts with `--attr-source=<empty tree>`, the empty tree of the repository's object format, so it reads no `.gitattributes` file (added by #13). In these logs attributes decide one thing, through the `diff` attribute: whether git counts a file's lines at all. Without a source git reads attributes from wherever it finds them. A work tree reads its checked-out files, uncommitted edits included; a bare clone reads HEAD's tree on git 2.43.0 to 2.45.2, CI's 2.45.0 among them, and none from 2.45.3 and 2.46.0 on; and a repository's `attr.tree` names any tree, in a bare clone too. Verified on 2.50.1 with a work tree and a bare clone of one repository holding a committed `blob.dat -diff` and, in the work tree only, an uncommitted `code.py -diff`:

| | work tree | bare clone | both, with the empty tree |
| --- | --- | --- | --- |
| `blob.dat` (committed `-diff`) | binary: `-` `-` | text: `1` `0` | text |
| `code.py` (uncommitted `-diff`) | binary | text | text |
| `other.py` (`-diff` in `$GIT_DIR/info/attributes` of both) | binary | binary | binary |

So History, in week and scan runs alike, gave the work tree and the bare clone different `FileChange` rows for one commit, and gives them the same rows with the source; blame's case is in [blame-collector.md](blame-collector.md#tree-and-command).

**Why no attributes, rather than the scanned commit's.** Reading the scanned commit's `.gitattributes`, as classification does with `check-attr --source`, makes the result independent of the checkout too. #13 first did that, and History and blame then disagreed about the same lines. Blame attributes every line of a file, whatever its `diff` attribute says, so a committed `-diff` would leave lines out of a commit's added lines that blame then counts as surviving. With a committed `api.py -diff` and Ann's one commit adding 20 lines to `api.py` and 2 to `core.py`, `durable@1` gave her 22 surviving lines over 2 added, a durability of 11; the empty tree, like a bare clone on a recent git, gives 22 over 22 (verified). Read from the scanned commit, the patterns also depend on the machine: under `core.ignorecase=true`, which git on macOS writes, a committed `*.PY -diff` marked every `.py` file binary. Which files count is classification's decision, and classification does not read the `diff` attribute either: it reads `linguist-generated` and `linguist-vendored` at the scanned commit, and its header sniff reads every file as text (`git grep -a`, [classification.md](classification.md#header-sniff)). A team that wants its `-diff` files left out marks them `linguist-generated`, or adds a deny pattern; honouring `-diff` itself would be a policy decision, which would need an ADR and would have to apply to blame's lines as well.

**What still makes a file binary.** Two things: git's content check, which takes a file with a NUL byte in its first 8,000 bytes for binary, and `$GIT_DIR/info/attributes`, which belongs to the repository rather than to a checkout and is the same in a work tree and a bare clone. The runner's prefix makes the patterns there match case-sensitively, and pins `core.bigFileThreshold` and `diff.default.binary`, which would otherwise make files binary too. History gives a binary file no lines: numstat prints `-` `-`, and the own patch has no hunks. Blame still attributes each of its lines, so `durable@1` counts them in Σ surviving and not in Σ added: a `prod` file with a NUL byte gave Ann 5 surviving lines over 2 added (verified; this predates #13). Leaving such lines out of both sums, as `durable@1` already does for a file whose blame failed ([metrics.md](../metrics.md#change-facts-shared-by-durable1-and-simplifying1)), is the metric's fix, left for its own issue.

The revert, root and co-author logs print no diff and take no source. The cost of the source is in [git-invocation.md](git-invocation.md#repository-settings-that-change-facts): almost nothing, where the scanned commit's tree cost about 40% more on the history logs.

## Shallow and partial clones

- **Partial clone:** commit facts only (landings, map, authors, trailers, subjects, reverts), and no `FileChange` rows. The landing log drops `--numstat`, its diff options and `--diff-merges=first-parent`: that option alone makes git diff every commit, and rename detection then reads blobs the clone does not have, which fails under `GIT_NO_LAZY_FETCH` even with nothing to print (verified).
- **Shallow clone:** a parentless commit is the clone's boundary, whose diff against the empty tree is its whole tree, not its change (verified: in a `--depth 3` clone of the main fixture, numstat gives Ada's one-line commit, the boundary, 73 inserted lines). It keeps its commit and landing facts but gets no `FileChange` rows. The facts carry warning `shallow-window` when a boundary reachable from the scanned SHA has a committer time at or after the window start, or when a landing in the window brought in a boundary commit. A boundary is told from a true root only by the shallow file, which the collector does not read, so a true root inside a shallow clone's window is treated as a boundary too.

## Repository age

`RepoFacts.FirstCommit` is the author time of the earliest root commit reachable from the scanned SHA. A repository can have several roots (a merged-in history), so take the minimum:

```
git <common> log --max-parents=0 -z --pretty='format:%H%x1f%at%x1f%ct' <sha> --
```

Scan runs only: week runs leave it zero and run this command only in a shallow clone, for the boundary check above. In a shallow clone the roots are hidden behind the boundary, so `FirstCommit` is left zero (unknown) rather than reporting the boundary's date as the repository's age.
