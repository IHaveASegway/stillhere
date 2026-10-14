# Hotspots

Status: candidate design, open decision #1 ([../product.md](../product.md#open-decisions)): code-maat jar vs in-process Go · tests in M5

Both options produce the same per-file facts for a time window. The decision is only about who computes them.

## Definition for both options

In Tornhill's and CodeScene's sense, a hotspot is change frequency **overlapping** size, with non-blank lines of code as the proxy for complexity. Revisions alone are not a hotspot. Rank on both measures; a list sorted by revisions only is a change-frequency list and must be titled that way.

The input log for both options covers the scanned SHA, `--after` = `--now` minus `[hotspots].since_days` (default 365), and excludes every path the governing deny list matches. Old history confounds the analysis. The exclusion is deliberately the opposite of the week log: week must count a lockfile-only landing, but hotspots must not turn into a lockfile chart.

## Option A: code-maat 1.0.4 jar (as designed on 2026-10-07)

**The jar and its licence**

- Pin code-maat v1.0.4, released 2023-02-20 and maintenance-only since. The image PR records the asset's filename and SHA-256 and fails the build on a mismatch; this document does not invent the hash.
- code-maat is GPL-3.0. It stays a separate program behind a subprocess and a CSV, and never goes into the stillhere binary or the Homebrew formula. Bundling both in one Docker image is a structure for counsel to review, not a legal opinion.
- Jar resolution, first hit wins, no network: `--maat-jar PATH`, then `$STILLHERE_MAAT_JAR`, then `~/.cache/stillhere/code-maat-1.0.4-standalone.jar`. Never download the jar at runtime.
- Skip cleanly when `java` is missing, `java -version` fails, the jar is missing, or `--no-hotspots` is passed. The report is still written and the exit code is unaffected. `hotspots.status` is `skipped`, and `reason` is one of `java-not-found`, `jar-not-found`, `disabled`.

**The log and the invocation**

The log uses code-maat's git2 format, deliberately without `--all` and with the org mailmap:

```
git <common> log <sha> --numstat --date=short --pretty=format:'--%h--%ad--%aN' \
  --no-renames --after=<YYYY-MM-DD> -- . <exclude pathspecs>
```

`--no-renames` is required by the git2 format, which is the opposite of the week log.

Each analysis runs as:

```
java -Djava.awt.headless=true -Xmx1g -jar <jar> -l <log> -c git2 -a <analysis> <flags>
```

- One JVM at a time per scan, even when repos are processed in parallel.
- Timeout of 180 s per analysis.
- `headless` is needed because code-maat pulls in Incanter, which otherwise opens an AWT window.
- A non-zero exit, a timeout, or an unrecognised CSV header skips that one analysis for that repo only.

**The analyses**

| `-a` | Required header | Meaning | Units and pitfalls |
| --- | --- | --- | --- |
| `revisions --rows 200` | `entity,n-revs` | change frequency | `--rows` caps an already sorted list. Do not pass `-n 1`: in 1.0.4, `--min-revs` (default 5) is not read by `entities/by-revision`, so it is a no-op that looks like a threshold. |
| `entity-churn` | `entity,added,deleted,commits` | lines added and deleted in the window | not fragmentation |
| `main-dev` | `entity,main-dev,added,total-added,ownership` | the author with the most lines added in the window | `ownership` is a 0–1 ratio (`math.clj`), not a percent, and not blame majority |
| `fragmentation` | `entity,fractal-value,total-revs` | `1 − Σ (author_revs / total_revs)²`; 0 means one author | 0–1; not a health score |
| `age --age-time-now YYYY-MM-dd` | `entity,age-months` | months since the last change in the log | The date is the UTC calendar date of `--now`, because 1.0.4 rejects a timestamp; a rejected date skips age only. Files with no change in the window are absent. |
| `coupling -i 30 -m 5 -s 30` | `entity,coupled,degree,average-revs` | files that change in the same commits | `degree` is an integer percent (`logical_coupling.clj`). The thresholds (minimum coupling, minimum shared revisions, maximum changeset size) are passed explicitly rather than trusted as defaults. |

`author-churn`, `communication` and `messages` are not run: they are activity scores and graphs about people.

**The join (anti-corruption layer)**

- Normalise `entity` to forward slashes, strip one leading `./`, then join on the exact path at the scanned SHA.
- **Files still at the scanned SHA** get revisions, churn, fragmentation, age, the window main-dev name, and `ownership` exactly as the CSV gave it, plus the blame majority owners.
- **Files no longer there** are kept with `on_head: false` and null owners, at most 10 per repo. Never guess a successor.
- **Matching names to people.** The window main-dev name is matched to a person only when exactly one person in the repo has that display name. Otherwise `owner_match` is `ambiguous` or `unmatched`. A code-maat name never replaces a blame owner.
- **Units.** `ownership` and `degree` stay in separate fields with their own units. Never rescale them.

**Tests without Java.** Checked-in CSVs under `testdata/maat_csv/` cover the required headers, an ambiguous name, and a path that no longer exists. The header test against the real jar runs locally only.

## Option B: in-process Go

The same window and the same exclude pathspecs, computed from one `git log --numstat --no-renames` stream, a format the week parser already handles:

- **revisions:** the number of commits touching the file.
- **churn:** the sums of lines added and deleted.
- **age:** months since the last change, relative to `--now`.
- **fragmentation:** `1 − Σ pᵢ²`, where pᵢ is each author's share of the revisions.
- **main developer:** the author with the most lines added, ties broken by email.
- **coupling:** for every pair of files changed in the same commit, skipping commits that touch more than 30 files, `degree = shared_revs / average(revs_a, revs_b)` as an integer percent. A pair is kept when its degree is at least 30 and it has at least 5 shared revisions.

Validation: run Option A's jar once, offline, on the fixture, and assert that the tables are equal.

Option B needs no JVM, has no GPL boundary, and works from the Homebrew install. The cost is owning roughly 100 lines of coupling logic.
