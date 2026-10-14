# Blame collector

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (per-repo ignore-revs, per-commit rows, goroutine pool; then, with #7, the ignore-revs lookup and parsed list, per-commit author keys and shared blame slots) · becomes tests in M1

This collector answers one question: whose non-blank lines does `git blame` still attribute on the default branch?

## Tree and command

```
git <common> ls-tree -r -l <sha>
```

The porcelain format is `<mode> <type> <blob> <size>\t<path>`; only type `blob` is used. Classify every path ([classification.md](classification.md)), then blame each path that survives:

```
git <common> blame -w -M --line-porcelain --no-ignore-revs-file [--ignore-revs-file <file>] <sha> -- <path>
```

- **Flags.** `-w -M` is the default, and matches git-fame's `--loc=surviving` benchmark. `-C`, which tracks copies across files, stays off: it is expensive, and the proof run did not use it.
- **`-M` threshold.** `-M` credits a moved block to its original commit only when the block has at least 20 alphanumeric characters (`git-blame(1)` default). The fixture's blocks are 43 characters for that reason.
- **Bare mirrors.** These work unchanged, because every command names `<sha>` explicitly (verified).
- **`--no-ignore-revs-file`.** It clears a repo-local `blame.ignoreRevsFile`, which would otherwise add its list to the one stillhere records, and make blame fail when that file is missing ([git-invocation.md](git-invocation.md#analysis-commands-offline)).

## Ignore-revs, per repository

One `--ignore-revs-file` cannot serve many repos, because commit SHAs belong to a single repository. Per repo:

1. **Use the repo's own file.** GitHub's blame view honours the same root file. Look it up with `git ls-tree --full-tree --format='%(objectmode) %(objecttype) %(objectname) %(objectsize)' <sha> -- .git-blame-ignore-revs`, then read the blob by its ID with `git cat-file blob <oid>`. Check existence first, because `git show` on a missing path exits fatally, and check it with `ls-tree` rather than `git cat-file -e <sha>:<path>`: `cat-file -e` exits 128 with "path ... does not exist" both for a missing file and for a commit the repo does not have, while `ls-tree` prints nothing and exits 0 for a missing file and fails only for a broken repo (verified).
2. **CLI override.** A `--ignore-revs-file` flag overrides the repo's file only on a single-repo run. On a multi-repo run it is a usage error (exit 2).
3. **Record the source.** Per repo, record which file was used (`root`, `cli`, or none), the SHA-256 of its bytes, and the commits it lists (`IgnoreRevs.Revs`), which `durable@1` and `simplifying@1` exclude ([metrics.md](../metrics.md#change-facts-shared-by-durable1-and-simplifying1)).
4. **Pass the parsed list.** Parse the file with git's rules, then write the commit IDs, lowercased, sorted and one per line, to a 0600 file in a new 0700 directory under the system temp directory, pass that file, and remove it when the repo's blame ends, also on error or cancellation. git then ignores exactly the commits `IgnoreRevs.Revs` records.

git's rules (`oidset_parse_file_carefully`, verified on 2.50.1): everything from a `#` to the end of the line is a comment; spaces, tabs and line endings around a name are trimmed; empty lines are skipped; every other line must be one full object ID, in either case. SHAs absent from the repo are accepted and ignored. An abbreviated or otherwise malformed line makes blame refuse the whole file (`fatal: invalid object name`, exit 128), which would fail every blame in the repo. So:

- **Root file.** The user may not control it, so a problem does not stop the repo. Malformed lines are skipped; a file that is not a regular file, or is over 1 MiB, is not used. Either way, one entry in `errors.blame` names `.git-blame-ignore-revs` and says why.
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
- **Content.** A line starting with a tab is content. If the rest of the line is non-blank after trimming whitespace, add **one** line to the record's (SHA, email). Blank content adds nothing.
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

- **Concurrency.** A goroutine worker pool, default `min(12, runtime.NumCPU())`, overridable with `--jobs`. Each blame is its own process, so more workers than that thrash a large machine. Every repo shares one slot channel of that capacity, and a repo takes a slot before it starts each file's goroutine, so the slots bound the goroutines as well as the git processes.
- **Calibration.** A proof run over one 80-repo organization blamed 24,188 files in 93 s, with 12 workers on 16 cores.
- **Budget.** A cold scan should finish in under 15 minutes for 100 repos and 50,000 surviving files on 8 cores. It is checked by hand on a large tree, with the `timings` object pasted into the PR. CI never asserts wall-clock time.
- **File failures.** A non-zero exit, a timeout or unreadable porcelain appends `{path, message}` to the repo's `errors.blame`, with the message scrubbed and truncated per [git-invocation.md](git-invocation.md#url-scrubbing). The repo continues, and up to eight messages are echoed to stderr.
- **Repo failures.** A whole-repo failure sets `status = error`.
- **Progress.** Every 1,000 files, stderr shows `blamed N/M, hits H, misses K`.
