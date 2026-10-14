# Blame collector

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (per-repo ignore-revs, per-commit rows, goroutine pool) · becomes tests in M1

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

1. **Use the repo's own file.** If `git cat-file -e <sha>:.git-blame-ignore-revs` succeeds, write `git cat-file blob <sha>:.git-blame-ignore-revs` to a temp file and pass it. GitHub's blame view honours the same root file. Check existence first: `git show` on a missing path exits fatally (verified).
2. **CLI override.** A `--ignore-revs-file` flag overrides the repo's file only on a single-repo run. On a multi-repo run it is a usage error (exit 2).
3. **Record the source.** Per repo, record which file was used (`root`, `cli`, or none) and its SHA-256.

On 2.50.1, `#` comment lines and SHAs absent from the repo are both accepted (verified). CI checks the floor version.

## Porcelain parsing

`--line-porcelain` (what we run) and `--porcelain` (also accepted) differ. Four naive parsers are wrong:

- **Requiring four fields.** The first line of a group is `<sha> <orig> <final> <group-size>`, but continuation lines are `<sha> <orig> <final>` with **no fourth field**. A matcher that requires four fields drops every continuation line.
- **Rejecting repeated headers.** `--line-porcelain` repeats `author`, `author-mail`, `author-time` and the other headers on every line. A parser that treats a repeated `author` line as an error fails on every multi-line group.
- **Requiring headers on every line.** `--porcelain` prints those headers only the first time a commit appears. A parser that requires them on every line fails on it.
- **Multiplying by group size.** Never do this: a group can contain blank lines.

The rules, per line:

- **Header.** `^([0-9a-f]{7,64}) (\d+) (\d+)( (\d+))?$`: remember the SHA and ignore the optional group size.
- **Author lines.** `author `, `author-mail ` and `author-time ` replace the current values. When a header omits them, carry the previous values forward.
- **Content.** A line starting with a tab is content. If the rest of the line is non-blank after trimming whitespace, add **one** line to the current (SHA, email). Blank content adds nothing.
- **Everything else** (`filename`, `summary`, `previous`, `boundary`, and so on) is ignored.

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

- **Concurrency.** A goroutine worker pool, default `min(12, runtime.NumCPU())`, overridable with `--jobs`. Each blame is its own process, so more workers than that thrash a large machine.
- **Calibration.** A proof run over one 80-repo organization blamed 24,188 files in 93 s, with 12 workers on 16 cores.
- **Budget.** A cold scan should finish in under 15 minutes for 100 repos and 50,000 surviving files on 8 cores. It is checked by hand on a large tree, with the `timings` object pasted into the PR. CI never asserts wall-clock time.
- **File failures.** A non-zero exit, a timeout or unreadable porcelain appends `{path, message}` to the repo's `errors.blame`, with the message scrubbed and truncated per [git-invocation.md](git-invocation.md#url-scrubbing). The repo continues, and up to eight messages are echoed to stderr.
- **Repo failures.** A whole-repo failure sets `status = error`.
- **Progress.** Every 1,000 files, stderr shows `blamed N/M, hits H, misses K`.
