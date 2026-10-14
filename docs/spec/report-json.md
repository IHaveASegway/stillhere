# Report JSON

Status: v1 shape as emitted by `report.Build` and `report.JSON` (#11) · 2026-10-14 · `stillhere.report/v1`. No external consumer exists yet, so the shape may still change until the first tag; after it, the rules below hold.

`stillhere week`, `scan` and `report` print one `stillhere.report/v1` document with `--format json`. The read commands (`explain`, `who-knows`, `me`, `validate`, `metrics`) print their own answers, described [below](#the-read-commands-answers). The Go types in `internal/report` are the same shapes, so the MCP server (#15) answers from them too.

## Rules that carry forward, whatever the format

- **Schema changes.** Adding a field does not change the version. Renaming a field, removing one, or changing what it means does. Reports can always be regenerated, so the old and new shapes are never written side by side.
- **Null and empty values.** An absent section is `null`; an empty list is `[]` and an empty map `{}`, never `null` and never omitted.
  - `null`: `run` in a week report (and in a scan that stored nothing); `still_here` for a repository without line facts (every week run, shallow and partial clones) and, with `week`, for a failed repository; `card.age` when the first commit is unknown; a share or a concentration whose metric gave no value.
  - `[]`: `errors.blame` in a clean scan, `warnings` with none, `ranks` without `--rank`.
  - Each kind of error has exactly one list, never a second list under another name.
- **Where emails go.** Emails appear in JSON, the machine output, as the join key of every person row, and never in text or Markdown, which people read, paste and share. AI payloads never contain emails, absolute paths, warnings or errors; that is specified with the narrator in M4.
- **Overviews replace group rollups.**
  - `overviews` has one element per group, in `groups` order, with `scope: {"kind": "group", "key": <name>}`.
  - Its person rows are the metrics' values for the group scope: counts for one email summed within the group, and `files_with_majority` summed per repository with the concentration walk re-run on the summed column ([blame-collector.md](blame-collector.md#reductions-the-fixture-asserts)).
  - A person in two groups is a row in each. There is no cross-group total, so a repository named directly, outside any group, has a card and no overview.
- **Joining.** People are joined on the canonical email after mailmap, never on a private person id. Repositories are joined on `key` (`groups[].repos`, `provenance.heads[].key`, `scope.key`).
- **Hotspot units.** `window_main_dev_ownership` is a 0–1 ratio and coupling `degree` is an integer percent ([hotspots-code-maat.md](hotspots-code-maat.md)). These fields exist only if hotspots ship.
- **Warning codes.** Warnings are `{code, message}`. Codes: `duplicate-repo`, `shallow-clone`, `shallow-window`, `partial-clone`, `numstat-rename`, `excluded-repo` (a run-level warning per repository `--exclude-repo` left out, which is how the report lists them) and `path-failed` (a PATH that gave no repository: neither a repository nor a group, or one git cannot read).
- **Determinism.** Two runs on the same inputs and `--now` render byte-identical JSON once `timings` is removed: object fields in a fixed order, map keys sorted, every list sorted. A second scan into the same facts.db also gets the next `run.id` (and `provenance.run_id`), so a comparison across one database removes those too.

## Shape

A scan of `~/src/acme` with `--rank durable`, where `acme` holds the main fixture and a repository `--exclude-repo 'legacy-*'` left out. The numbers are the fixture's oracles ([SPEC.md](../../testdata/fixture/SPEC.md)), and the document was rendered by `report.JSON`; `…` marks rows and repeated objects cut from this page.

```jsonc
{
  "schema": "stillhere.report/v1",
  "kind": "scan",                       // "week" or "scan"
  "tool": {"name": "stillhere", "version": "0.1.0"},
  "run": {"id": 12, "status": "complete", "tool_version": "0.1.0",
          "options": {"exclude_repo": "legacy-*", "history_days": "365", "jobs": "12", "paths": "/home/lead/src/acme",
                      "policy": "stillhere.toml", "rank": "durable@1", "scope": "group"}},
  "now": "2026-10-07T00:00:00Z",
  "policy": {"version": 1, "sha256": "<hex>", "headline": "prod_nonblank_lines"},
  "window": {"days": 7, "days_from": "policy", "sort": "lines_added", "sort_from": "policy",
             "since": "2026-09-30T00:00:00Z", "until": "2026-10-07T00:00:00Z",
             "date_field": "committer", "walk": "first-parent", "diff": "first-parent"},
  "history": {"since": "2025-10-07T00:00:00Z", "until": "2026-10-07T00:00:00Z"},
  "metrics": ["ai_assisted@1", "durable@1", "knowledge_concentration@1", "knowledge_loss@1", "landed@1", "surviving@1"],
  "groups": [{"name": "acme", "path": "/home/lead/src/acme", "repos": ["local:fixture"]}],
  "warnings": [{"code": "excluded-repo",
                "message": "left out legacy-billing (/home/lead/src/acme/legacy-billing): its name matches --exclude-repo \"legacy-*\""}],
  "caveats": [
    {"code": "not-fetched", "message": "Analysis did not fetch: each repository is only as current as its clone, so a stale clone gives a stale report.", "repos": []},
    {"code": "excluded-repo", "message": "1 repository was left out by --exclude-repo and is in none of these numbers (see warnings).", "repos": []},
    {"code": "ai-lower-bound", "message": "AI-assisted shares are a lower bound: editor completions leave no trace in git.", "repos": []}
  ],
  "overviews": [{
    "scope": {"kind": "group", "key": "acme"},
    "provenance": {"run_id": 12, "as_of": "2026-10-07T00:00:00Z", "policy": "<hex>",
                   "heads": [{"repo": "fixture", "key": "local:fixture", "branch": "main", "sha": "<sha>"}],
                   "metrics": ["ai_assisted@1", "knowledge_concentration@1", "knowledge_loss@1", "landed@1", "surviving@1"]},
    "totals": {"repos": 1, "repos_ok": 1, "repos_failed": 0, "prod_files": 10, "prod_lines": 37,
               "people": 8, "landers": 7, "bots": 1, "landings_window": 9, "landings_history": 16},
    "people": {"files_blamed": 11,
               "humans": [{"email": "ada@example.com", "name": "Ada Example", "bot": false, "prod_nonblank_lines": 10,
                           "majority_files": 3, "lines_by_role": {"prod": 10, "test": 2}, "repos": 1, "landings_365d": 3}, …],
               "bots": [{"email": "copilot@bot.example", "name": "GitHub Copilot", "bot": true, "prod_nonblank_lines": 3,
                         "majority_files": 1, "lines_by_role": {"prod": 3}, "repos": 1, "landings_365d": 1}]},
    "week": {"humans": [{"email": "bob@example.com", "name": "Bob Builder", "bot": false,
                         "commits": 2, "lines_added": 11, "prod_lines_added": 11}, …],
             "bots": [{"email": "copilot@bot.example", "name": "GitHub Copilot", "bot": true,
                       "commits": 1, "lines_added": 3, "prod_lines_added": 3}]},
    "concentration": {"metric": "knowledge_concentration@1", "files_with_majority": 10, "authors_in_prefix": 2,
                      "bots_in_prefix": 0, "label": "not a truck factor"},
    "knowledge_loss": {"metric": "knowledge_loss@1", "num": 4, "den": 34, "value": 0.11764705882352941},
    "ai_assisted": {"metric": "ai_assisted@1", "num": 0, "den": 16, "value": 0},
    "bot_share": {"metric": "surviving@1", "num": 3, "den": 37, "value": 0.08108108108108109},
    "caveats": [{"code": "not-fetched", …}, {"code": "ai-lower-bound", …}]
  }],
  "ranks": [{
    "metric": "durable@1", "question": "Did the code a person wrote last?",
    "scope": {"kind": "group", "key": "acme"},
    "window": {"since": "2025-10-07T00:00:00Z", "until": "2026-07-09T00:00:00Z"},
    "percent": true, "min_n": 10,
    "provenance": {"run_id": 12, …, "metrics": ["durable@1"]},
    "ranked": [],
    "below_min": [{"email": "ada@example.com", "name": "Ada Example", "rank_lo": 0, "rank_hi": 0,
                   "estimate": 1, "lo": 0, "hi": 0, "n": 2}, …],
    "bots": [],
    "caveats": [{"code": "not-fetched", …},
                {"code": "validity", "message": "Validity: unvalidated until the M2 labelled-sample check", "repos": []}]
  }],
  "repos": [{
    "name": "fixture", "key": "local:fixture", "group": "acme", "path": "/home/lead/src/acme/fixture",
    "remote_scrubbed": "", "branch": "main", "head": "<sha>",
    "bare": false, "shallow": false, "partial": false,
    "status": "ok", "error": "", "line_facts": true,
    "card": {
      "provenance": {"run_id": 12, …, "metrics": ["ai_assisted@1", "knowledge_concentration@1", "knowledge_loss@1", "surviving@1"]},
      "age": {"first_commit": "2026-01-01T12:00:00Z", "days": 278},
      "size": {"files": 18, "prod_files": 10, "prod_lines": 37},
      "made_of": [{"email": "ada@example.com", "name": "Ada Example", "prod_lines": 10, "share": 0.2702702702702703}, …],
      "people": 8, "bots": 1,
      "concentration": {"metric": "knowledge_concentration@1", "files_with_majority": 10, "authors_in_prefix": 2,
                        "bots_in_prefix": 0, "label": "not a truck factor"},
      "knowledge_loss": {"metric": "knowledge_loss@1", "num": 4, "den": 34, "value": 0.11764705882352941},
      "ai_assisted": {"metric": "ai_assisted@1", "num": 0, "den": 16, "value": 0},
      "bot_share": {"metric": "surviving@1", "num": 3, "den": 37, "value": 0.08108108108108109},
      "landings": {"window": 9, "history": 16},
      "caveats": [{"code": "not-fetched", …}, {"code": "ai-lower-bound", …}]
    },
    "warnings": [],
    "errors": {"blame": []},
    "skipped": {"denied_path": 4, "too_big": 0, "generated_header": 1, "unclassified": 2},
    "blame_ignore_revs": {"source": "", "sha256": "", "revs": []},
    "week": {"humans": […], "bots": […]},
    "still_here": {"files_blamed": 11, "humans": […], "bots": […]}
  }],
  "timings": {"started": "2026-10-14T09:30:00Z", "finished": "2026-10-14T09:30:02Z", "total_ms": 2140,
              "discover_ms": 31, "collect_ms": 1650, "metrics_ms": 12, "rank_ms": 40, "store_ms": 380, "load_ms": 0,
              "repos": [{"repo": "fixture", "key": "local:fixture", "history_ms": 410, "classify_ms": 95, "blame_ms": 1120}]}
}
```

A week report has the same top level with `"kind": "week"`, `"run": null`, `history` equal to `window`, `"metrics": ["landed@1"]`, `still_here: null` everywhere, overviews whose `people` is `null`, and no knowledge, concentration or AI sections.

Changes from the 2026-10-07 draft:

- `generated_at` is gone: a wall-clock field breaks byte-identical output. The run's start and finish are in `timings`.
- `group_rollups` became `overviews`, one per group, with totals, the people's breadth (repos), depth (majority files), survival and activity, bots, the AI-assisted and bot shares, knowledge loss, concentration, provenance and caveats.
- Every repository has a `card` (age, size, who it is made of, concentration, knowledge loss, AI and bot share, landings, caveats): what an agent or a lead reads first.
- `majority_owner_concentration` moved from `still_here` to `card.concentration` and the overview's `concentration`; `files_with_majority` went with it.
- `policy.weights_applied_to_headline` is gone with open decision #2's weights; `blame_ignore_revs` gained `revs`; `groups[].repo_names` became `groups[].repos`, the repository keys.
- `caveats` (what could make this wrong) are new at every level, and so are `kind`, `run`, `history`, `metrics`, `ranks` and `line_facts`. `timings` lost the cache and hotspot counters until those ship, and `hotspots` returns with M5.
- Narration, `geared_fp` and `geared_fp_estimated` stay out: narration is specified with the MCP and narrator work in M4, gearing only if open decision #2 is approved. The hotspot row shape lives in [hotspots-code-maat.md](hotspots-code-maat.md).

## The objects

| Object | Fields | Notes |
| --- | --- | --- |
| `run` | `id`, `status`, `tool_version`, `options` | `status` is `complete` or `partial` (a repository failed, or a PATH gave no repository). `options` are the effective scan options: `jobs`, `scope`, `rank` (comma-separated), `exclude_repo` and `paths` (newline-separated), `ignore_revs_file`, `history_days`, `policy` (the file read). Never a secret. |
| `window` | `days`, `days_from`, `sort`, `sort_from`, `since`, `until`, `date_field`, `walk`, `diff` | The landed@1 window: what "landed" means. `*_from` is `cli` or `policy`. |
| `history` | `since`, `until` | The landing window the history covers: 365 days for a scan, the window for a week run. |
| `provenance` | `run_id`, `as_of`, `policy`, `heads[]`, `metrics[]` | On every overview, card, rank table and answer, so each reads on its own. `run_id` is 0 for a week report. `heads` are `{repo, key, branch, sha}`. |
| `caveats[]` | `code`, `message`, `repos[]` | What could make the section wrong ([codes](#caveats)). `repos` names the repositories a caveat concerns; on a card it is empty. |
| `overviews[]` | `scope`, `provenance`, `totals`, `people`, `week`, `concentration`, `knowledge_loss`, `ai_assisted`, `bot_share`, `caveats` | One per group. `totals.people` counts humans with surviving prod lines, `totals.landers` humans with a landing in the window, `totals.bots` bots with either. |
| `ranks[]` | `metric`, `question`, `scope`, `window`, `percent`, `min_n`, `provenance`, `ranked[]`, `below_min[]`, `bots[]`, `caveats` | One per metric named by `--rank` and scope: each group and each repository named directly, or one `all` scope with `--scope all`. Rows are `{email, name, rank_lo, rank_hi, estimate, lo, hi, n}`; `lo`, `hi` and the ranks are 0 below the minimum. Bots are never ranked. |
| `repos[]` | identity, `status`, `error`, `line_facts`, `card`, `warnings`, `errors`, `skipped`, `blame_ignore_revs`, `week`, `still_here` | Sorted by group, name and key. `skipped` counts every classified path, at HEAD or touched by the history. |
| `card` | `provenance`, `age`, `size`, `made_of[]`, `people`, `bots`, `concentration`, `knowledge_loss`, `ai_assisted`, `bot_share`, `landings`, `caveats` | `made_of` is the top five humans by surviving prod lines, with their share of the repository's. |
| shares | `metric`, `num`, `den`, `value` | `value` is `num / den`, a 0–1 ratio. A group's share is its own value when the metric gives one, else the ratio of its repositories' summed parts, which is exact because the parts are counts. |
| `timings` | `started`, `finished`, `total_ms`, `discover_ms`, `collect_ms`, `metrics_ms`, `rank_ms`, `store_ms`, `load_ms`, `repos[]` | Wall clock: the only part two identical runs do not share. `report` gives the stored run's `started` and `finished`, and its own `load_ms` and `rank_ms`. |

## Where the numbers come from

The report computes no metric: it arranges the values `internal/metrics` computed, as `internal/report/values.go` lays them out, and counts facts that need no identity or policy rule. This table is that file for readers; a metric that lays its values out differently changes both.

| Report field | Source |
| --- | --- |
| `week` rows: `commits`, `lines_added`, `prod_lines_added` | `landed@1`, person subjects, in the repository's scope (`repos[].week`) or the group's (`overviews[].week`): `Value` is the landings credited; `Detail` `lines_added`, `prod_lines_added` |
| `still_here` and `people` rows: `prod_nonblank_lines`, `majority_files`, `lines_by_role`, `repos`, `landings_365d` | `surviving@1`, person subjects, in the repository's or the group's scope: `Value` is prod non-blank lines; `Detail` `majority_files` (else `majority_owner@1`'s value for the person), `lines_<role>` for every role, `repos` (breadth), `landings_365d` (activity) |
| `concentration` | `knowledge_concentration@1`, the value about the scope as a whole (subject the repository, or its root area `<key>:`; in a group scope the one value about neither a person nor a change): `Value` is `authors_in_prefix`; `Detail` `files_with_majority`, `bots_in_prefix` |
| `knowledge_loss` | `knowledge_loss@1`, the value about the scope as a whole: `Num` lines held by people gone quiet, `Den` lines held by people |
| `ai_assisted` | `ai_assisted@1`, the value about the scope as a whole: `Num` AI-assisted landings, `Den` landings in 365 days. A person's own share appears only in `stillhere me`. |
| `bot_share` | `surviving@1`'s person values in the scope: lines held by bots over all lines |
| `made_of`, `card.people`, `card.bots` | the repository's `still_here` rows |
| ranks | `rank.Intervals` over the metric's own clusters, window and eligibility (`metrics.Ranked.Clusters`), with bots left out by the caller; intervals the run stored are used as they are |
| `card.size`, `card.landings`, `card.age`, `totals`, `skipped`, `files_blamed` | facts: path classes at HEAD, line owners on prod paths, landings in each window, `FirstCommit`, blame errors |

A metric whose values are missing although the run has the facts it computes from gets a `no-values` caveat, so an empty section never passes for a real zero.

## Caveats

| Code | When |
| --- | --- |
| `not-fetched` | Always: analysis never fetches, so a stale clone gives a stale report. |
| `failed` | A repository failed and adds nothing. |
| `shallow-clone` | Blame did not run: its surviving lines, ownership and durability are missing. |
| `partial-clone` | No line facts: only its landings and commits count. |
| `shallow-window` | The shallow boundary is inside the history window. |
| `blame-errors` | Blame failed on some files; their lines are missing. |
| `numstat-rename` | Numstat rows that could not be read count 0 lines. |
| `no-ignore-revs` | A repository with at least 10,000 surviving prod lines has no `.git-blame-ignore-revs`: a formatting commit would take the credit for every line it touched. |
| `excluded-repo`, `duplicate-repo`, `path-failed` | Run-level warnings, counted; `warnings` lists each one. |
| `unmerged-identities` | Display names that more than one email shares were not merged; a `.mailmap` line merges them if they are one person. |
| `no-values` | A metric the report reads gave no value although the run has its facts. |
| `ai-lower-bound` | An AI-assisted share is shown: editor completions leave no trace. |
| `validity` | A ranked metric's validity note, such as unvalidated until the labelled-sample check. |
| `unmatched-labels` | `validate` only: labelled changes that are not landings of the run. |

Text and Markdown print `not-fetched` and `ai-lower-bound` once, at the top; JSON repeats them in every section, so a section handed to an agent carries them.

## The read commands' answers

Each answer starts with `provenance` and ends with `caveats`. Values print as `{metric, scope, subject, window, value, num, den, n, detail}`, where `subject` is `{kind, key, name, bot}` and `key` is a person's canonical email.

| Command | Answer | Fields |
| --- | --- | --- |
| `explain METRIC --person EMAIL` | `Explain` | `metric` (the definition: `id`, `question`, `unit`, `ranked`, `eligibility`, `blind_spots`, `gaming`, `oracle`, `validity`), `subject`, `values[]` (one per scope and window), `changes[]` (a ranked metric's clusters for the person: `{repo, repo_key, landing, time, merge, title, num, den}`; `title` is the landing's commit subject, local output only and never sent to an AI) |
| `who-knows PATH [--repo KEY]` | `WhoKnows` | `path`, `repo`, `repos[]` of `{repo, key, majority_owners[], top_authors[], recent_landers[], bots[]}`, each a `{email, name, lines, majority_files, recent_landings}`: `who_knows@1` computed over the run's facts under the path, so any prefix works, down to one file |
| `me --email EMAIL` | `Me` | `person`, `values[]`, `ranks[]` of `{metric, scope, window, percent, min_n, eligible, rank_lo, rank_hi, of, estimate, lo, hi, n}`: the person's place in every ranked metric's table, in the scan's rank scope |
| `validate --labels FILE` | `Validation` | `labels`, `labellers`, `meaningful`, `routine`, `noise`, `unmatched`, `metrics[]` of `{metric, auc, n, meaningful, routine, beta}`; `auc` is `null` without both meaningful and routine changes scored, and `beta` is true below AUC 0.7 or 50 scored labels |
| `metrics` | `Catalog` | `metrics[]` of definitions, in registry order |

The labels file is CSV: change id (a landing SHA, or an unambiguous prefix of at least 7 hex digits), label (`meaningful`, `routine` or `noise`), and optionally the labeller; a header row and `#` comments are allowed.

## Text and Markdown

- **Text** is for a terminal, read top to bottom: the provenance header, what could make the report wrong, each group's overview, the ranks, then each repository's card and tables. No line is wider than 100 columns, except one that holds a single unbreakable word such as a URL; long names are cut with `…`. Untrusted text (names, paths, subjects, git messages) is printed with control and format characters escaped as Go quotes them, so an escape sequence in an author name cannot reach the terminal.
- **Markdown** is GitHub-flavoured, with the same sections, for pasting into an issue or a pull request. Untrusted text is backslash-escaped and `&` becomes `&amp;`, so `<img src=x>` and `Frank & Sons` render as text; paths, SHAs and remotes are code spans. A repository's local path is left out, though a warning that concerns a path still names it.
- **Neither** prints an email. Ranked metrics appear only as rank tables, with their 95% intervals, the people below the minimum listed and not ranked, and bots apart ([ADR 0001](../adr/0001-measure-changes-rank-per-metric.md)): a table sorted by a ranked metric's point values would be a rank without its uncertainty.

## Exit codes

| Code | When |
| --- | --- |
| 0 | At least one repo produced week or still-here facts. This includes partial failure, clone-health skips, and skipped hotspots. |
| 1 | No repo produced facts: every path failed, none was a repo, or `--exclude-repo` left none. The report is still printed, with each repository's error, and a scan stores no run. A read command asked about a run or a person the database does not hold also exits 1. |
| 2 | A usage error (see below). Until the integration issue (#13) removes the last stub, a command whose use case is not built yet also exits 2. |
| 128 + n | Interrupted by signal n (SIGINT, SIGTERM or SIGHUP). stillhere stops every git process, then ends itself with the same signal, so a shell sees it die by that signal (`$?` is 130 for Ctrl-C) and stops a loop. Where a process cannot signal itself (Windows), it exits 130. A scan interrupted after it began writing marks its run failed, which no reader sees. |

Usage errors:

- missing paths, or an unreadable policy file;
- a user policy that lacks a required table: `headline`, `week`, `blame`, `bots`, `ai`, `roles`, `paths`, plus `hotspots` once M5 ships, and `gearing` only if open decision #2 is approved;
- `--ignore-revs-file` on a multi-repo run, or one that cannot be read;
- a malformed `--exclude-repo` glob, `--rank` on a metric that is never ranked, or a metric id the registry does not know;
- git older than the supported floor ([git-invocation.md](git-invocation.md#version-floors)).
