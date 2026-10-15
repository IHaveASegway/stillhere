# Blame collector

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (per-repo ignore-revs, per-commit rows, goroutine pool; then, with #7, the ignore-revs lookup, the list resolved to commits, its warnings, per-commit author keys and shared blame slots; then by #13: no `.gitattributes` file read, `--no-textconv`, `--indent-heuristic`, `--encoding=UTF-8`) · becomes tests in M1

This collector answers one question: whose non-blank lines does `git blame` still attribute on the default branch?

## Tree and command

```
git <common> ls-tree -r -l <sha>
```

The porcelain format is `<mode> <type> <blob> <size>\t<path>`; only type `blob` is used. Classify every path ([classification.md](classification.md)), then blame each path that survives:

```
git <common> --attr-source=<empty tree> blame -w -M --no-textconv --indent-heuristic --encoding=UTF-8 \
  --line-porcelain --no-ignore-revs-file [--ignore-revs-file <file>] <sha> -- <path>
```

- **Flags.** `-w -M` is the default, and matches git-fame's `--loc=surviving` benchmark. `-C`, which tracks copies across files, stays off: it is expensive, and the proof run did not use it.
- **`-M` threshold.** `-M` credits a moved block to its original commit only when the block has at least 20 alphanumeric characters (`git-blame(1)` default). The fixture's blocks are 43 characters for that reason.
- **Bare mirrors.** These work unchanged, because every command names `<sha>` explicitly (verified).
- **`--no-ignore-revs-file`.** It clears a repo-local `blame.ignoreRevsFile`, which would otherwise add its list to the one stillhere records, and make blame fail when that file is missing ([git-invocation.md](git-invocation.md#analysis-commands-offline)).

The other options pin what a checkout, a machine or a repository's config would change (added by #13; each verified on 2.50.1, and recorded with the settings that changed nothing in [git-invocation.md](git-invocation.md#repository-settings-that-change-facts)). Blame reads more config than its own `blame.*` keys: git's blame (`git_blame_config` in `builtin/blame.c`, 2.50.1) also reads the diff heuristic, every diff driver (`diff.<driver>.*`) and git's general settings, `core.*` and `i18n.*` among them.

- **`--attr-source=<empty tree>`.** Blame reads the `diff` attribute when it follows a rename: rename detection compares a file it takes for binary byte for byte, CRs included. Ann's `f.py` with CRLF line endings, renamed to `g.py` and turned to LF by Bob in one commit, kept its 10 lines with Ann in a bare clone, but under an uncommitted `*.py -diff` in a work tree of the same commit blame no longer followed the rename and gave all 10 to Bob. Reading the scanned commit's attributes did the same in a clone with `core.ignorecase=true`, which git on macOS writes, when the commit held `*.PY -diff`. With the empty tree as the source, blame reads no `.gitattributes` file, as History's logs do ([week-collector.md](week-collector.md#attributes-none-from-any-tree)); only `$GIT_DIR/info/attributes` still applies, matched case-sensitively. Blame's line attribution never reads the attribute: it counts every line of a file that History counts none of, which is why History must not honour a committed `-diff` either. The source costs blame nothing measurable: blaming 150 files of Homebrew's `brew` took 15.9 s with no source, 15.6 s with the empty tree and 16.0 s with HEAD's tree.
- **`--no-textconv`.** By default blame runs the textconv program that a `diff=<driver>` attribute and the repository's `diff.<driver>.textconv` name, and counts its output. A driver that turned blank lines into text kept 3 non-blank lines of a commit whose own patch added 2, so `durable@1` would have seen more lines survive than were written; it also ran a program the repository names. History's logs already pass the option, so surviving and added lines are now counted on the same bytes.
- **`--indent-heuristic`.** git's default, which a repository's `diff.indentHeuristic=false` turns off. The heuristic decides which of two identical lines a deletion removes, and so whose line survives: in 3 of 400 random three-commit histories, turning it off moved a line to another author. History needs no such pin: a slide changes which identical line is counted, never how many.
- **`--encoding=UTF-8`.** Blame prints author names in `i18n.logOutputEncoding`: `ISO-8859-1` printed the `ë` of `Zoë` as the byte `0xEB`. The display name rule counts the names of blamed commits the history does not hold, so one person would have had two names, one of them not UTF-8. History's logs print UTF-8 the same way.
- **Not pinned, because blame does not read them or its output does not change:** `diff.renameLimit` and `diff.algorithm` (blame reads neither, so `-l`, which means long revisions to blame, is not needed), and `core.bigFileThreshold` (read, but rename detection has loaded the blob before it asks whether it is binary). Three settings blame does read are pinned in the prefix ([git-invocation.md](git-invocation.md#analysis-commands-offline)). `core.precomposeUnicode=true` rewrites the path argument, which matters only to blame among the collectors, because only blame names a path in its arguments. `diff.default.binary=true` makes rename detection take every file without a `diff` attribute for binary, so the CRLF rename above was lost. `core.ignorecase=true` matches `info/attributes` patterns in any case.

## Ignore-revs, per repository

One `--ignore-revs-file` cannot serve many repos, because commit SHAs belong to a single repository. Per repo:

1. **Use the repo's own file.** GitHub's blame view honours the same root file. Look it up with `git ls-tree --full-tree --format='%(objectmode) %(objecttype) %(objectname)' <sha> -- .git-blame-ignore-revs`, then read the blob by its ID with `git cat-file blob <oid>`, stopping at 1 MiB. Check existence first, because `git show` on a missing path exits fatally, and check it with `ls-tree` rather than `git cat-file -e <sha>:<path>`: `cat-file -e` exits 128 with "path ... does not exist" both for a missing file and for a commit the repo does not have, while `ls-tree` prints nothing and exits 0 for a missing file and fails only for a broken repo (verified). Leave `%(objectsize)` out of the format: it needs the blob itself, so in a partial clone that lacks the blob, `ls-tree` fails outright (verified).
2. **CLI override.** A `--ignore-revs-file` flag overrides the repo's file only on a single-repo run. On a multi-repo run it is a usage error (exit 2).
3. **Resolve the IDs as git does.** git turns each listed ID into a commit with `peel_to_commit_oid`: an annotated tag becomes the commit it tags, and an ID that names no commit (a tree, a blob, or an object the clone lacks) is dropped without a word (verified). One `git cat-file --batch-check='%(objectname) %(objecttype)'` over `<id>^{commit}` lines does the same for every ID. In a shallow clone this drops the commits behind the boundary, which the clone's history cannot contain either.
4. **Record the source.** Per repo, record which file was used (`root`, `cli`, or none), the SHA-256 of its bytes, and the commits git ignores (`IgnoreRevs.Revs`), which `durable@1` and `simplifying@1` exclude ([metrics.md](../metrics.md#change-facts-shared-by-durable1-and-simplifying1)). Shallow and partial clones get no line facts, but their history still needs the list, so a scan calls `Blame` with no paths for them, which only resolves it.
5. **Pass the resolved list.** Write those commits, lowercased, sorted and one per line, to a 0600 file in a new 0700 directory under the system temp directory. Pass that file by its absolute path, because git runs with `-C <repo>` and would resolve a relative one (from a relative `$TMPDIR`) against the repo. Remove it when the repo's blame ends, also on error or cancellation. git then ignores exactly the commits `IgnoreRevs.Revs` records.

git's rules (`oidset_parse_file_carefully`, verified on 2.50.1): everything from a `#` to the end of the line is a comment; spaces, tabs and line endings around a name are trimmed; empty lines are skipped; every other line must be one full object ID, in either case. SHAs absent from the repo are accepted and ignored. An abbreviated or otherwise malformed line makes blame refuse the whole file (`fatal: invalid object name`, exit 128), which would fail every blame in the repo. So:

- **Root file.** The user may not control it, so a problem does not stop the repo. Malformed lines are skipped; a file that is not a regular file, is over 1 MiB, or cannot be read (a bare partial clone lacks the blob) is not used. Either way, one warning with code `ignore-revs` names `.git-blame-ignore-revs` and says why (`BlameFacts.Warnings`, which go to `RepoFacts.Warnings`). It is not an `errors.blame` entry: that list holds only the blamed files whose lines are unknown.
- **CLI file.** The user named it and can fix it, so an unreadable file, one over 1 MiB, or a malformed line is a usage error.

CI checks the floor version.

## Co-authors

Blame names a commit's author, but credit follows [ADR 0007](../adr/0007-ai-assisted-attribution.md): an agent's commit with a human co-author belongs to that human, in the ownership metrics as in `durable@1`. After blaming a repo, read the Co-authored-by trailers of every commit blame attributed lines to with `coAuthors` (`internal/collect/coauthors.go`: one `git log --no-walk --stdin`, one `git check-mailmap --stdin`), and store them on each `LineOwner` in trailer order. The collector stores facts only; `identity.Credit` applies the policy's agent patterns when metrics are computed.

## Porcelain parsing

`--line-porcelain` (what we run) and `--porcelain` (also accepted) differ. Five naive parsers are wrong:

- **Requiring four fields.** The first line of a group is `<sha> <orig> <final> <group-size>`, but continuation lines are `<sha> <orig> <final>` with **no fourth field**. A matcher that requires four fields drops every continuation line.
- **Rejecting repeated headers.** `--line-porcelain` repeats `author`, `author-mail`, `author-time` and the other headers on every line. A parser that treats a repeated `author` line as an error fails on every multi-line group.
- **Requiring headers on every line.** `--porcelain` prints those headers only the first time a commit appears. A parser that requires them on every line fails on it.
- **Multiplying by group size.** Never do this: a group can contain blank lines.
- **Carrying the previous record's author.** In `--porcelain`, a commit that comes back after another commit's lines has no author lines, so the previous record's values belong to the other commit. In a file whose lines go Ann, Ben, Ann, git 2.50.1 printed the second Ann record as a bare header and its content (verified), and this parser gives that line to Ben.

Both forms are a sequence of records, one per line of the file: a header, key lines, then the content. The rules:

- **Header.** `^([0-9a-f]{7,64}) (\d+) (\d+)( (\d+))?$`: remember the SHA and ignore the optional group size.
- **Author lines.** `author `, `author-mail ` and `author-time ` give the record's commit its author. Keep them per commit: when a record omits them, use the values that commit's earlier record gave.
- **Content.** A line starting with a tab is content. If the rest of the line is non-blank (it holds a character outside Unicode's White_Space, [metrics.md](../metrics.md#noise-applied-before-every-metric)), add **one** line to the record's (SHA, email). Blank content adds nothing. The test is `nonBlank` in `internal/collect/scan.go`, the function the history collector counts added and removed lines with (since #13 the same function, not a copy), so `durable@1` compares like with like.
- **Everything else** (`filename`, `summary`, `previous`, `boundary`, and the `ignored` and `unblamable` marks a repo-local `blame.markIgnoredLines` and `blame.markUnblamableLines` add, verified) is ignored.
- **Structure.** Every content line follows exactly one header. Output that breaks this is unreadable porcelain and a file failure, so an unrecognised header cannot hand its line to the previous record.

`author-mail` is already mailmapped by git ([git-invocation.md](git-invocation.md#identity-mailmap-and-bots)). Strip the `<>` and lowercase it; never re-parse mailmap in Go. The measured porcelain for the fixture's `service.py` is in [../../testdata/fixture/SPEC.md](../../testdata/fixture/SPEC.md#measured-blame).

## Persisted facts

One row per (repo, head SHA, path, commit SHA), with `author_email` and `nonblank_lines`. Rows are aggregated per commit rather than per line, but they must keep the commit SHA, because durability joins surviving lines back to the commits and landings that wrote them ([week-collector.md](week-collector.md#landing--commit-map)). Path role is not stored with these facts: it is a policy output, computed when reducing.

## Reductions the fixture asserts

The metric definitions belong to [../metrics.md](../metrics.md). Until they move there, this is the contract the oracles check:

- **Headline:** the sum of non-blank lines on files whose role is `prod`. Other roles are reported per role and never in the headline.
- **Majority owner:** on each headline file, any author with `lines / file_lines >= 0.5`. A 50/50 file counts for both authors. Never use `max()` alone, which hides a real split. Bots can be majority owners, and stay in the bot table.
- **Concentration walk:**
  - Let M = `files_with_majority`, counting each file once even when it has two majority owners.
  - Sort authors by `majority_files` descending, then `prod_nonblank_lines` descending, then canonical email ascending (UTF-8 bytes).
  - Walk down the list until the running sum of `majority_files` is `>= M / 2`, using true division (M = 5 stops at ≥ 2.5).
  - Report `authors_in_prefix` and `bots_in_prefix`, labelled "not a truck factor". The sum of `majority_files` can exceed M.
  - This is not Avelino's degree-of-authorship truck factor, and it is never named truck factor, bus factor or DOA.
- **Group rollups:** sum per email within a group, then re-run the walk on the summed column. A rollup is not the sum of per-repo prefixes. There is no cross-group total.

## Parallelism, errors, budget

- **Concurrency.** A goroutine worker pool, default `min(12, runtime.NumCPU())`, overridable with `--jobs`. Each blame is its own process, so more workers than that thrash a large machine. Every repo shares one `collect.BlameSlots` of that capacity (a `golang.org/x/sync` semaphore, which serves waiters in arrival order), and a repo takes a slot before it starts each file's goroutine, so the slots bound the goroutines as well as the git processes.
- **Calibration.** A proof run over one 80-repo organization blamed 24,188 files in 93 s, with 12 workers on 16 cores.
- **Budget.** A cold scan should finish in under 15 minutes for 100 repos and 50,000 surviving files on 8 cores. It is checked by hand on a large tree, with the `timings` object pasted into the PR. CI never asserts wall-clock time.
- **File failures.** A non-zero exit, a timeout or unreadable porcelain appends `{path, message}` to the repo's `errors.blame`, with the message scrubbed and truncated per [git-invocation.md](git-invocation.md#url-scrubbing). The repo continues, and up to eight messages are echoed to stderr.
- **Repo failures.** A whole-repo failure sets `status = error`.
- **Progress.** Every 1,000 files, stderr shows `blamed N/M, hits H, misses K`.
