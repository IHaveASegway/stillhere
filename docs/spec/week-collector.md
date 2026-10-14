# Week collector

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (trailers, subjects, reverts, landing map) · becomes tests in M0 (M1 for reverts and the landing map)

This collector answers one question: what landed on the default branch in the window? It produces one row per landing commit, credited to that commit's author.

## The log

```
git <common> log <sha> --first-parent --diff-merges=first-parent \
  --since=<window start> --until=<window end> \
  --numstat --date=iso-strict \
  --pretty='format:%x00%H%x1f%P%x1f%cI%x1f%aN%x1f%aE%x1f%(trailers:key=Co-authored-by,valueonly,separator=%x1e)%x1f%s'
```

`<common>` is the analysis prefix in [git-invocation.md](git-invocation.md#analysis-commands-offline). Partial clones run the same command without `--numstat` ([discovery.md](discovery.md#clone-health)).

| Trap | Rule and reason |
| --- | --- |
| Merge diffs | `--first-parent` limits the walk, and the diff is taken against the first parent. Never use `-m`: it means `--diff-merges=separate`, one diff per parent, so the second-parent diff gets summed too. On 2.50.1, `--first-parent` alone already yields first-parent diffs for merges (verified); the explicit flag documents the intent. |
| `--no-merges` | Wrong. It drops the merge commit and keeps side-branch commits whose own dates fall in the window, so it misses a merge of older commits and counts a side commit that never landed on its own. |
| Mailmap placeholders | Use capital `%aN`/`%aE`, which apply the mailmap even with `log.mailmap=false` and even under `--no-use-mailmap`. Lowercase `%an`/`%ae` print the raw headers (verified). Repo-local config survives `GIT_CONFIG_GLOBAL=/dev/null`, so a lowercase format splits one person in two wherever a repo has turned `log.mailmap` off. |
| Renames | Never pass `--no-renames` here. With default rename detection, a pure rename is one numstat row; `--no-renames` turns it into a full delete plus a full add and inflates `lines_added`. |
| Date field | `--since` filters on committer date. The report records `date_field = "committer"` and `walk = "first-parent"`. |

## Parsing

A record starts at NUL. Fields are separated by U+001F, in this order:

1. commit SHA;
2. parent SHAs, space-separated;
3. committer time;
4. mailmapped name;
5. mailmapped email;
6. co-author trailer values, separated by U+001E (verified: the key matches case-insensitively, so `Co-Authored-By:` is found);
7. subject.

The subject is last, so a stray U+001F inside it cannot shift the other fields.

Lines after the format line that match `^(\d+|-)\t(\d+|-)\t(.+)$` are numstat rows. `-` means binary: insertions count as 0 and the path is not added to line sums. Records are separated by a blank line, which is ignored (verified).

Rename path fields: insertions are attributed to the **destination** only.

| Path field | Destination |
| --- | --- |
| `kept.py` | `kept.py` |
| `old_name.py => new_name.py` | the text after ` => ` |
| `dir/{old => new}/file.py` | `dir/` + `new` + `/file.py` |
| `{old => new}` | the text after ` => ` and before `}` |

For the brace form, split on the first `{`, then on the matching `}`, then split the inside on ` => `. The destination is prefix + new + suffix. Any other row that contains `=>` produces warning `numstat-rename` and contributes 0 lines; it is never treated as a path that happens to contain `=>`.

## lines_added

One predicate, shared by the collector and the tests: sum the numstat insertions (blank lines included) on every path that [classification.md](classification.md) assigns a role. Denied, too-big, header-generated and unclassified paths add 0, and the commit still counts. A dependency-bot commit that touches only lockfiles is still a landing, which is why those paths are not excluded by pathspec. `prod_lines_added` is the `prod` subset. Neither number is comparable to still-here non-blank lines, and neither is ever named `nonblank_lines`.

Rows are per person, per repo, with `commits`, `lines_added`, and `prod_lines_added`. A person whose only rows are bot rows appears only in the bot table. The default sort is `lines_added` descending, then email ascending; `--week-sort commits` exists. Tests assert the set of rows, not the order among ties.

## Window

- `--until` is the `--now` instant. It defaults to the system clock in UTC and accepts ISO-8601, so tests don't depend on the clock.
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

- Cursor's trailer string is unverified (sources show both `Made-with: Cursor` and `Co-authored-by: Cursor`), so it is not a default.
- Editor completions leave no trace, so every AI-assisted figure is a lower bound.
- Human co-authors are facts too: GitHub squash merges add a `Co-authored-by` line for every other author of the pull request, and those must never be tagged AI-assisted.

## Subjects and reverts

The subject is collected for Conventional-Commit typing (M5). It stays local and is never sent to any AI.

A revert's subject is `Revert "<original subject>"`, but the reverted SHA is in the body, as `This reverts commit <sha>.` (verified). `%b` cannot share a stream with `--numstat`, because body lines can look like numstat rows. Revert detection is therefore a second query, over the range the durability metric needs:

```
git <common> log <sha> --first-parent -E --grep='^This reverts commit [0-9a-f]{7,64}' \
  --format='%x00%H%x1f%b' <range limits>
```

Each revert landing links to the commit it reverts. Reverts made with GitHub's revert button and then squash-merged may not carry that line; the pull-request link from the GitHub sync covers them (verify in M2).

## Landing → commit map

Blame credits a line to the commit that wrote it. For a merged branch, that is a side-branch commit, not the landing merge. Durability ([../metrics.md](../metrics.md)) needs the map from each landing to the commits it brought in:

```
git <common> rev-list <landing>^1..<landing>
```

- **Merge landing:** the result is the merge itself plus every commit it brought in. For a two-parent merge that is the merge plus `M^1..M^2` (verified); the `^1..` form also covers octopus merges.
- **Squash or rebase landing:** maps to itself.
- **Lines blamed to the merge commit itself** are conflict resolutions, and belong to the merge's author.

The map is stored as facts (landing SHA, commit SHA) alongside the landings.
