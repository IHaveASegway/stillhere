# Report JSON

Status: v1 shape as emitted by `report.Build` and `report.JSON` (#11) · 2026-10-14 · `stillhere.report/v1`. No external consumer exists yet, so the shape may still change until the first tag; after it, the rules below hold.

`stillhere week`, `scan` and `report` print one `stillhere.report/v1` document with `--format json`. The read commands (`explain`, `who-knows`, `me`, `identities`, `validate`, `metrics`) print their own answers, described [below](#the-read-commands-answers). The Go types in `internal/report` are the same shapes, so the MCP server (#15) answers from them too.

## Rules that carry forward, whatever the format

- **Schema changes.** Adding a field does not change the version. Renaming a field, removing one, or changing what it means does. Reports can always be regenerated, so the old and new shapes are never written side by side.
- **Null and empty values.** An absent section is `null`; an empty list is `[]` and an empty map `{}`, never `null` and never omitted.
  - `null`: `run` in a week report (and in a scan that stored nothing); `still_here` for a repository without line facts (every week run, shallow and partial clones) and, with `week`, for a failed repository; `card.age` when the first commit is unknown; a share or a concentration whose metric gave no value; the numbers `surviving@1` gives (`size.prod_files`, `size.prod_lines`, `card.people`, `totals.prod_files`, `totals.prod_lines`, `totals.people`) and `card.bots` when the repository or scope has no line facts, so that "unknown" never reads as 0.
  - `[]`: `errors.blame` in a clean scan, `warnings` with none, `ranks` without `--rank`.
  - Each kind of error has exactly one list, never a second list under another name.
- **Strings and numbers.** Strings are UTF-8. git does not guarantee it: a commit written without an encoding header (by cvs2git, early git-svn or JGit) keeps its Latin-1 bytes, and `--encoding=UTF-8` leaves them as they are. Such bytes become U+FFFD in every format, as encoding/json v1 wrote them, so one odd commit cannot fail a report. A number that is not finite (NaN, an infinity) is `null`. The document is written only once it is whole, never cut off halfway.
- **Where emails go.** Emails appear in JSON, the machine output, only as the join key of a person row (`email`, and a person subject's `key`), and never in text or Markdown, which people read, paste and share. No other field prints a part of one either.
  - **A display name that gives an address away is withheld in every format**, JSON included: one that holds an `@`, as an address typed as a name does, or that is, ignoring case, a local part of any person's address in the run (the whole of it, the part before a `+tag`, or a GitHub noreply address's login), as GitHub names a commit by the login of an account without a profile name. `John Doe` on `john.doe@` is a name and stays. The rule is the name's, decided over every address of the run (`identity.Directory.Withheld`), not the row's own address alone: when `jdoe@corp.example` is named `jdoe`, the name is withheld on `john.doe@mail.example` too if he is also called `jdoe`, or the page would print on one row the local part it withholds on the other, and the caveats and the MCP server's name lookup would point from one to the other. The identities answer applies the same rule ([identities.md](identities.md#output)).
  - Such a row has `name` `""` and `name_withheld` `true`, so a reader that drops the email fields, as the MCP server does without `--allow-emails`, sends no address under another field's name. A person git records without a name has `name` `""` and `name_withheld` `false`.
  - **Bots.** A bot's or an agent's address is no person's, so its local part withholds no name: `dependabot[bot]` and `Copilot` are shown, though each is its own noreply login. A bot's name that holds an `@` is withheld like anyone's, so no text or Markdown page prints it.
  - **GitHub logins.** An account that no commit of the run links to an email is keyed and named `@login`, an app `@login[bot]` ([ADR 0004](../adr/0004-github-behind-sync-boundary.md)), even when the login is also the local part of an address in the run. A login is the account's public name, which GitHub shows beside every review the row counts, and the page ties it to no address: the forge could not link the two, which is why the row is keyed by login. Withholding it would hide it from text and Markdown only, since `email` and `key` hold it in JSON and over MCP, and a `name_withheld` on a login would tell an agent that the login is a local part in the run, which the login alone does not. Revisit if a rule ever links logins to addresses by their local parts.
  - **Labels.** Text and Markdown show a person without a name as `(no name 1)` and one whose name is withheld as `(name withheld 1)`, numbered in the order the page first names them: the same person keeps one label everywhere on a page, two such people stay apart, and a label matches nothing outside its page. git records any name, so a name with a label's shape, or one that starts with a double quote, is printed quoted, as Go quotes a string (`"(no name 1)"`): no name reads as a label, and no two names print alike unless they are alike. A label is a counter, not a hash: the `(no name 1a2b3c)` of earlier versions, the start of the email's SHA-256, let anyone who guessed an address confirm it with one hash (ENISA, *Pseudonymisation techniques and best practices*, 2019, §7.2).
  - AI payloads never contain emails, absolute paths, warnings or errors; that is specified with the narrator in M4.
- **Overviews replace group rollups.**
  - `overviews` has one element per group, in `groups` order, with `scope: {"kind": "group", "key": <name>}`. A run with `--scope all`, or without groups (only repositories named directly), has one more, first: every repository, `scope: {"kind": "all", "key": ""}`.
  - Its person rows are the metrics' values for the overview's scope: counts for one email summed within the group, and `files_with_majority` summed per repository with the concentration walk re-run on the summed column ([blame-collector.md](blame-collector.md#reductions-the-fixture-asserts)). No metric walks every repository at once, so the every-repository overview's `concentration` is `null`.
  - A person in two groups is a row in each. Groups are kept apart as ranks are ([metrics.md](../metrics.md#ranking-rules)): there is no cross-group total unless `--scope all` asks for one, so in a run with groups a repository named directly has a card and is in no overview.
- **Joining.** People are joined on the canonical email after mailmap, never on a private person id. Repositories are joined on `key` (`groups[].repos`, `provenance.heads[].key`, `scope.key`).
- **Hotspot units.** `window_main_dev_ownership` is a 0–1 ratio and coupling `degree` is an integer percent ([hotspots-code-maat.md](hotspots-code-maat.md)). These fields exist only if hotspots ship.
- **Warning codes.** Warnings are `{code, message}`. A repository's warnings are on the repository (`repos[].warnings`) and its card's caveats: `duplicate-repo` (on the repository kept: another clone of its remote was skipped), `shallow-clone`, `shallow-window`, `partial-clone`, `ignore-revs` (its own `.git-blame-ignore-revs` was not used, or not whole). The run's are in the top-level `warnings`: `excluded-repo` (one per repository `--exclude-repo` left out, which is how the report lists them) and `path-failed` (one per path that gave no repository: a PATH that is neither a repository nor a group, or a repository git cannot read or whose default branch has no commit yet). A `path-failed` warning makes a scan `partial`.
- **Determinism.** Two runs on the same inputs and `--now` render byte-identical JSON once `timings` is removed: object fields in a fixed order, map keys sorted, every list sorted. A second scan into the same facts.db also gets the next `run.id` (and `provenance.run_id`), so a comparison across one database removes those too.

## Shape

`stillhere --mailmap org.mailmap scan --now 2026-10-07 --rank durable --exclude-repo 'legacy-*' acme`, run in `/home/lead/src`, where `acme` holds the main fixture and a repository `legacy-billing` that the glob leaves out; the mailmap merges Ada's two emails. The numbers are the fixture's oracles ([SPEC.md](../../testdata/fixture/SPEC.md)). The document was rendered by `report.JSON` (2026-10-14, the #11 branch merged with the open #4 to #10 branches) and then cut: `…` marks rows and repeated objects left out, `<hex>` and `<sha>` stand for hashes, and `run.id` and `timings` are illustrative.

```jsonc
{
  "schema": "stillhere.report/v1",
  "kind": "scan",                       // "week" or "scan"
  "tool": {"name": "stillhere", "version": "0.1.0"},
  "run": {"id": 12, "status": "complete", "tool_version": "0.1.0",
          "options": {"exclude_repo": "legacy-*", "history_days": "365", "jobs": "12", "paths": "/home/lead/src/acme",
                      "policy": "package:default.toml", "rank": "durable@1", "scope": "group"}},
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
    "totals": {"repos": 1, "repos_ok": 1, "repos_failed": 0, "prod_paths": 10, "prod_files": 10, "prod_lines": 37,
               "people": 8, "landers": 7, "bots": 1, "landings_window": 9, "landings_history": 16},
    "people": {"files_blamed": 11,
               "humans": [{"email": "ada@example.com", "name": "Ada Example", "name_withheld": false, "bot": false,
                           "prod_nonblank_lines": 10, "majority_files": 3, "lines_by_role": {"prod": 10, "test": 2}, "repos": 1,
                           "landings_365d": 4}, …],
               "bots": [{"email": "copilot@bot.example", "name": "GitHub Copilot", "name_withheld": false, "bot": true,
                         "prod_nonblank_lines": 3, "majority_files": 1, "lines_by_role": {"prod": 3}, "repos": 1, "landings_365d": 1}]},
    "week": {"humans": [{"email": "bob@example.com", "name": "Bob Builder", "name_withheld": false, "bot": false,
                         "commits": 2, "lines_added": 11, "prod_lines_added": 11}, …],
             "bots": [{"email": "copilot@bot.example", "name": "GitHub Copilot", "name_withheld": false, "bot": true,
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
    "below_min": [{"email": "ada@example.com", "name": "Ada Example", "name_withheld": false, "rank_lo": 0, "rank_hi": 0,
                   "estimate": 1, "lo": 0, "hi": 0, "n": 2}, …],
    "bots": [],
    "caveats": [{"code": "not-fetched", …},
                {"code": "validity", "message": "Validity: Unvalidated until the M2 labelled-sample check (stillhere validate, AUC >= 0.7 on at least 50 labelled changes).", "repos": []}]
  }],
  "repos": [{
    "name": "fixture", "key": "local:fixture", "group": "acme", "path": "/home/lead/src/acme/fixture",
    "remote_scrubbed": "", "branch": "main", "head": "<sha>",
    "bare": false, "shallow": false, "partial": false,
    "status": "ok", "error": "", "line_facts": true,
    "card": {
      "provenance": {"run_id": 12, …, "metrics": ["ai_assisted@1", "knowledge_concentration@1", "knowledge_loss@1", "surviving@1"]},
      "age": {"first_commit": "2026-01-01T12:00:00Z", "days": 278},
      "size": {"files": 18, "prod_paths": 10, "prod_files": 10, "prod_lines": 37},
      "made_of": [{"email": "ada@example.com", "name": "Ada Example", "name_withheld": false, "prod_lines": 10,
                   "share": 0.2702702702702703}, …],
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

A week report has the same top level with `"kind": "week"`, `"run": null`, `history` equal to `window`, `"metrics": ["landed@1"]`, `still_here: null` everywhere, overviews whose `people` is `null`, no knowledge, concentration or AI sections, and `null` for the numbers only blame gives (`size.prod_files`, `size.prod_lines`, `card.people`, `card.bots` and the matching `totals`).

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
| `run` | `id`, `status`, `tool_version`, `options` | `status` is `complete` or `partial` (a repository failed, or a path gave no repository). `options` are the effective scan options: `jobs`, `scope`, `rank` (comma-separated), `exclude_repo` and `paths` (newline-separated), `ignore_revs_file`, `history_days`, `policy` (the file read). `paths` are the PATH arguments as discovery resolved them, absolute: each group's directory and each repository named directly, never the argument as typed, which may be a URL with a token in it; a PATH that gave no repository is a `path-failed` warning instead. Every value is scrubbed ([git-invocation.md](git-invocation.md#url-scrubbing)): never a secret. |
| `window` | `days`, `days_from`, `sort`, `sort_from`, `since`, `until`, `date_field`, `walk`, `diff` | The landed@1 window: what "landed" means. `*_from` is `cli` or `policy`. |
| `history` | `since`, `until` | The landing window the history covers: 365 days for a scan, the window for a week run. |
| `provenance` | `run_id`, `as_of`, `policy`, `heads[]`, `metrics[]`, `forge` | On every overview, card, rank table and answer, so each reads on its own. `run_id` is 0 for a week report. `heads` are `{repo, key, branch, sha}`. `forge` dates the GitHub data the run read for the repositories in `heads` ([ADR 0004](../adr/0004-github-behind-sync-boundary.md)): `{as_of, repos[]}`, each repository `{repo, key, updated, synced}`, the forge repository (`owner/name`) and the run's repository key it joined, both as the run read them, so a later rename on the forge changes neither, and the replica's watermark as the run read it: every pull request updated before `updated` was in it, and `synced` is when the last sync that brought it up to date started (`null` until one finished). A repository the replica held no GitHub data for (neither a page nor a finished sync, as when a first sync stopped before its first page) is not listed. `as_of` is the oldest `synced`, since a number over several repositories is as current as its stalest one, and `null` while a first sync has not finished. On `me` and on `explain` of `reviews_given@1`, a value in a group or in every repository counts the reviews of each of its repositories, so `heads` and `forge` cover all of them. `forge` is `null` when none of those repositories had GitHub data, and in a week report. |
| `caveats[]` | `code`, `message`, `repos[]` | What could make the section wrong ([codes](#caveats)). `repos` names the repositories a caveat concerns; on a card it is empty. |
| `overviews[]` | `scope`, `provenance`, `totals`, `people`, `week`, `concentration`, `knowledge_loss`, `ai_assisted`, `bot_share`, `caveats` | Every repository first, with `--scope all` or without groups; then one per group. `totals` are `repos`, `repos_ok`, `repos_failed`, `prod_paths` (paths with role prod at each HEAD, whether or not they hold a line), `prod_files` and `prod_lines` (prod files with surviving lines, and those lines), `people` (humans with surviving prod lines), `landers` (humans with a landing in the window), `bots` (bots with either), `landings_window` and `landings_history`. |
| `ranks[]` | `metric`, `question`, `scope`, `window`, `percent`, `min_n`, `provenance`, `ranked[]`, `below_min[]`, `bots[]`, `caveats` | One per metric named by `--rank` and scope: each group and each repository named directly, or one `all` scope with `--scope all`. A table in which nobody has a change the metric counts is still there, empty, with a `no-values` caveat. Rows are `{email, name, name_withheld, rank_lo, rank_hi, estimate, lo, hi, n}`; `lo`, `hi` and the ranks are 0 below the minimum. Bots are never ranked. |
| `repos[]` | identity, `status`, `error`, `line_facts`, `card`, `warnings`, `errors`, `skipped`, `blame_ignore_revs`, `week`, `still_here` | Sorted by group, name and key. `skipped` counts every classified path, at HEAD or touched by the history; a partial clone is classified too, so its paths count. |
| `card` | `provenance`, `age`, `size`, `made_of[]`, `people`, `bots`, `concentration`, `knowledge_loss`, `ai_assisted`, `bot_share`, `landings`, `caveats` | `size` is `{files, prod_paths, prod_files, prod_lines}`: paths and prod paths at HEAD, then the prod files that hold surviving lines and those lines. `made_of` is the top five humans by surviving prod lines, with their share of the repository's: `{email, name, name_withheld, prod_lines, share}`. |
| shares | `metric`, `num`, `den`, `value` | `value` is `num / den`, a 0–1 ratio. A group's share is its own value when the metric gives one, else the ratio of its repositories' summed parts, which is exact because the parts are counts. |
| `timings` | `started`, `finished`, `total_ms`, `discover_ms`, `collect_ms`, `metrics_ms`, `rank_ms`, `store_ms`, `load_ms`, `repos[]` | Wall clock: the only part two identical runs do not share. `report` gives the stored run's `started` and `finished`, and its own `load_ms` and `rank_ms`. |

## Where the numbers come from

The report computes no metric: it arranges the values `internal/metrics` computed, as `internal/report/values.go` lays them out, and counts facts that need no identity or policy rule. This table is that file for readers; a metric that lays its values out differently changes both.

| Report field | Source |
| --- | --- |
| `week` rows: `commits`, `lines_added`, `prod_lines_added` | `landed@1`, person subjects, in the repository's scope (`repos[].week`) or the overview's (`overviews[].week`): `Value` is the landings credited; `Detail` `lines_added`, `prod_lines_added` |
| `still_here` and `people` rows: `prod_nonblank_lines`, `majority_files`, `lines_by_role`, `repos`, `landings_365d` | `surviving@1`, person subjects, in the repository's or the overview's scope: `Value` is prod non-blank lines; `Detail` `majority_files` (else `majority_owner@1`'s value for the person), `lines_<role>` for every role, `repos` (breadth), `landings_365d` (activity) |
| `size.prod_lines`, `size.prod_files`, `card.people`, `card.bot_share`; the same in `totals` and an overview's `bot_share` | `surviving@1`'s value about the repository or the group itself (subject the repository or the group): `Value` is prod lines; `Detail` `prod_files` (prod files with surviving lines), `people` (humans with surviving prod lines), `bot_share` (bots' lines over prod lines; the share's `num` is that times `den`). surviving@1 is the one definition of these numbers, so the report counts none of them from facts. The every-repository overview, which the metric has no such value for, adds up its repositories' values (exact: they are counts per repository) and counts `people` among its own `people` rows, since one person can hold lines in several repositories. |
| `concentration` | `knowledge_concentration@1`, the value about the scope as a whole (subject the repository, or its root area `<key>:`; in a group scope the one value about neither a person nor a change): `Value` is `authors_in_prefix`; `Detail` `files_with_majority`, `bots_in_prefix` |
| `knowledge_loss` | `knowledge_loss@1`, the value about the scope as a whole: `Num` lines held by people gone quiet, `Den` lines held by people |
| `ai_assisted` | `ai_assisted@1`, the value about the scope as a whole: `Num` AI-assisted landings, `Den` landings in 365 days. A person's own share appears only in `stillhere me` (ADR 0007). |
| `made_of`, `card.bots` | the repository's `still_here` rows |
| ranks | `rank.Intervals` over the metric's own clusters, window and eligibility (`metrics.Ranked.Clusters`), with bots left out by the caller; intervals the run stored are used as they are |
| who-knows lists | `who_knows@1` for the path, per repository: the value about the path itself (subject the repository, or the area `<key>:<path>`: `Value` prod lines, `Detail` `prod_files`, `files_with_majority`), and one value per person (`Value` prod lines under the path; `Detail` `majority_files`, `recent_landings`, `top_author`, the place 1 to 5 among the top authors). Without `top_author`, the top five are picked by the same rule: prod lines, then email. |
| `size.files`, `size.prod_paths`, `totals.prod_paths`, `card.landings`, `card.age`, `totals` counts of repositories and landings, `skipped`, `files_blamed` | facts: path classes at HEAD, landings in each window, `FirstCommit`, blame errors |

A metric whose values are missing although the run has the facts it computes from gets a `no-values` caveat, so an empty section never passes for a real zero. So does a rank table asked for in which nobody has a change the metric counts, and a who-knows answer whose `who_knows@1` gave nothing although the run holds surviving prod lines under the path: it reads as missing, never as "nobody".

## Caveats

| Code | When |
| --- | --- |
| `not-fetched` | Always: analysis never fetches, so a stale clone gives a stale report. |
| `failed` | A repository failed and adds nothing. |
| `shallow-clone` | Blame did not run: its surviving lines, ownership and durability are missing. |
| `partial-clone` | No line facts: only its landings and commits count; where its blobs are absent, no path is skipped as too big or generated. |
| `shallow-window` | The shallow boundary is inside the history window. |
| `blame-errors` | Blame failed on some files; their lines are missing. |
| `numstat-rename` | Numstat rows that could not be read count 0 lines. |
| `no-ignore-revs` | A repository with at least 10,000 surviving prod lines has no `.git-blame-ignore-revs`: a formatting commit would take the credit for every line it touched. |
| `duplicate-repo` | Another clone of the repository's remote was skipped, so the repository is counted once; on the kept repository's card. |
| `excluded-repo`, `path-failed` | Run-level warnings, counted, with the first one's reason; `warnings` lists each one. |
| `unmerged-identities` | Display names that more than one email shares were not merged; a `.mailmap` line merges them if they are one person, and `stillhere identities` suggests the lines with the evidence for each. A shared name that gives an address away is counted, never printed. |
| `mailmap-at-scan` | Every `identities` answer: its addresses are the run's, which git mailmapped when the run was scanned, so a `.mailmap` line added since takes effect at the next scan; git applies one line per commit and never follows a chain. |
| `no-values` | A metric the report reads gave no value although the run has its facts; a rank table asked for is empty; a who-knows answer is missing. |
| `ai-lower-bound` | An AI-assisted share is shown: editor completions leave no trace. |
| `validity` | A ranked metric's validity note, such as unvalidated until the labelled-sample check. |
| `unmatched-labels` | `validate` only: labelled changes that are not landings of the run. |
| `forge-not-synced` | A section's repositories that the run collected have no GitHub data in the replica the run read, or one's first sync did not finish: `reviews_given@1` counts none, or only some, of their reviews, and a value it lacks is absent, not 0. Only on the sections that show `reviews_given@1`'s numbers: its rank tables, `me`, and `explain` of `reviews_given@1`; never in the report's own `caveats`, so a report without GitHub data says nothing about it. The message names `stillhere sync github` only when one of those repositories has an origin on a forge to sync from. |
| `forge-not-kept` | `explain` of `reviews_given@1`: a run keeps when GitHub was synced, not the pull requests, so the changes behind the values are not listed. |

Text and Markdown print `not-fetched` and `ai-lower-bound` once, at the top; JSON repeats them in every section, so a section handed to an agent carries them.

## The read commands' answers

Each answer starts with `provenance` and ends with `caveats`. Values print as `{metric, scope, subject, window, value, num, den, n, detail}`, where `subject` is `{kind, key, name, name_withheld, bot}`, `key` is a person's canonical email (or `@login`), and `name` and `name_withheld` follow [the rule above](#rules-that-carry-forward-whatever-the-format) ("Where emails go").

| Command | Answer | Fields |
| --- | --- | --- |
| `explain METRIC --person EMAIL` | `Explain` | `metric` (the definition: `id`, `question`, `unit`, `ranked`, `eligibility`, `blind_spots`, `gaming`, `oracle`, `validity`), `subject`, `values[]` (one per scope and window), `changes[]` (a ranked metric's clusters for the person: `{repo, repo_key, landing, time, merge, title, num, den}`; `title` is the landing's commit subject, local output only and never sent to an AI). `ai_assisted` about a person is a usage error: a person's own AI-assisted share is on their own page only (ADR 0007). |
| `who-knows PATH [--repo KEY]` | `WhoKnows` | `path`, `repo`, `repos[]` of `{repo, key, lines, prod_files, files_with_majority, majority_owners[], top_authors[], recent_landers[], bots[]}`, each person a `{email, name, name_withheld, lines, majority_files, recent_landings}`: `who_knows@1` answered for the path at query time, so any prefix works, down to one file. When it gives nothing although the run holds surviving prod lines under the path, a `no-values` caveat names the repositories and the text says "no answer", not "nobody". |
| `me --email EMAIL` | `Me` | `person`, `values[]`, `ranks[]` of `{metric, scope, window, percent, min_n, eligible, rank_lo, rank_hi, of, estimate, lo, hi, n}`: the person's place in every ranked metric's table, in the scan's rank scope. It holds the facts a lead sees about the person (ADR 0001) and their own AI-assisted share, which text and Markdown label "shown only on your own page (ADR 0007)". The command line has no viewer identity; enforcing who opens whose page is the web server's (M3). |
| `identities [--run ID] [--format mailmap\|text\|md\|json]` | `Identities` | `scope`, `addresses`, `bots`, `shared`, `oversized`, `candidates[]` of `{confidence, guess, commented, chained, rules[], name, addresses[], links[]}`, each address `{email, name, name_withheld, domain_kind, canonical, commits, co_authored, first, last, repos[]}` and each link `{rule, addresses[], name, from_name, hand_off}`, and `rules[]`, each rule's definition and how it can be wrong ([identities.md](identities.md)). Emails are only in fields named `email`, and a name that gives an address away is withheld. `--format mailmap` prints the suggested `.mailmap` lines with their evidence as `#` comments, every candidate's but a high one's commented out; stillhere never applies one. |
| `validate --labels FILE` | `Validation` | `labels`, `labellers`, `meaningful`, `routine`, `noise`, `unmatched`, `metrics[]` of `{metric, auc, n, meaningful, routine, beta}`; `auc` is `null` without both meaningful and routine changes scored, and `beta` is true below AUC 0.7 or 50 scored labels |
| `metrics` | `Catalog` | `metrics[]` of definitions, in registry order |

The labels file is CSV: change id (a landing SHA, or an unambiguous prefix of at least 7 hex digits), label (`meaningful`, `routine` or `noise`), and optionally the labeller; `#` comments are allowed, and so is a header row, which is a first row whose first field is no change id (`change,label,labeller`). A first row that names a change with a misspelt label is an error, as on any other row.

## Text and Markdown

- **Text** is for a terminal, read top to bottom: the provenance header, what could make the report wrong, the overviews, the ranks, then each repository's card and tables. No line is wider than 100 columns, except one that holds a single unbreakable word such as a URL: headers wrap like prose, and a table too wide for the line cuts its widest text columns (names, titles, details) with `…`, none below 8 columns; numbers are never cut. Untrusted text (names, paths, subjects, git messages) is printed with control and format characters escaped as Go quotes them, so an escape sequence in an author name cannot reach the terminal.
- **Markdown** is GitHub-flavoured, with the same sections, for pasting into an issue or a pull request. Untrusted text is backslash-escaped and `&` becomes `&amp;`, so `<img src=x>` and `Frank & Sons` render as text; paths, SHAs and remotes are code spans. GitHub would link `@name` as a mention, which notifies that account, and `#12` as a reference, which posts a back-link on issue 12, so `@` and `#` in untrusted text are followed by a word joiner (U+2060): the text reads the same and links nobody. A repository's local path is left out, though a warning that concerns a path still names it.
- **Neither** prints an email. Ranked metrics appear only as rank tables, with their 95% intervals, the people below the minimum listed and not ranked, and bots apart ([ADR 0001](../adr/0001-measure-changes-rank-per-metric.md)): a table sorted by a ranked metric's point values would be a rank without its uncertainty.

## Exit codes

| Code | When |
| --- | --- |
| 0 | At least one repo produced week or still-here facts. This includes partial failure, clone-health skips, and skipped hotspots. |
| 1 | No repo produced facts: every path failed, none was a repo, or `--exclude-repo` left none. The report is still printed, with each repository's error, and a scan stores no run. A read command asked about a run or a person the database does not hold also exits 1. |
| 2 | A usage error (see below). |
| 128 + n | Interrupted by signal n (SIGINT, SIGTERM or SIGHUP). stillhere stops every git process, then ends itself with the same signal, so a shell sees it die by that signal (`$?` is 130 for Ctrl-C) and stops a loop. Where a process cannot signal itself (Windows), it exits 130. A scan interrupted after it began writing marks its run failed, which no reader sees. |

Usage errors:

- missing paths, or an unreadable policy file;
- a user policy that lacks a required table: `headline`, `week`, `blame`, `bots`, `ai`, `roles`, `paths`, plus `hotspots` once M5 ships, and `gearing` only if open decision #2 is approved;
- `--ignore-revs-file` on a multi-repo run, or one that cannot be read;
- a malformed `--exclude-repo` glob, `--rank` on a metric that is never ranked, or a metric id the registry does not know;
- git older than the supported floor ([git-invocation.md](git-invocation.md#version-floors)).
