# Report JSON (draft)

Status: draft carried over from the 2026-10-07 plan · M0's `week` output · superseded in M1 by facts.db plus a snapshot export. The field names are a starting point, not a contract: no external consumer exists yet, so treat `stillhere.report/v1` as unstable until the first tag.

## Rules that carry forward, whatever the format

- **Schema changes.** Adding a field does not change the version. Renaming a field, removing one, or changing what it means does. Reports can always be regenerated, so the old and new shapes are never written side by side.
- **Null and empty values.** `week` runs set `still_here` and `hotspots` to `null`, and `errors.blame` to `[]`. A clean scan also has `errors.blame: []`, never omitted. Each kind of error has exactly one list, never a second list under another name.
- **Where emails go.** Emails appear in machine output and never in rendered pages. AI payloads never contain emails, absolute paths, warnings or errors; that is specified with the narrator in M4.
- **Group rollups.**
  - `group_rollups` lines up with `groups` by `name`. Each element is `{name, week, still_here}`, using the same row objects as a repo.
  - Counts for one email are summed within a group. `files_with_majority` is summed per repo, and the concentration walk is re-run on the summed column ([blame-collector.md](blame-collector.md#reductions-the-fixture-asserts)).
  - A person in two groups is a row in each. There is no cross-group total.
- **Joining people.** People are joined on the published key (the canonical email after mailmap), not on a private person id.
- **Hotspot units.** `window_main_dev_ownership` is a 0–1 ratio and coupling `degree` is an integer percent ([hotspots-code-maat.md](hotspots-code-maat.md)). These fields exist only if hotspots ship.
- **Warning codes.** Warnings are `{code, message}`. Codes used by these specs: `duplicate-repo`, `shallow-clone`, `shallow-window`, `partial-clone`, `excluded-repo` (a run-level warning per repository `--exclude-repo` left out, which is how the report lists them), `discover-failed` (a run-level warning per path that discovery could not turn into a repository, `<path>: <reason>`, which is how the report lists them; it makes a scan run `partial`, [codebase.md](codebase.md#errors-and-logging)), `ignore-revs` (a repository's own `.git-blame-ignore-revs` was not used, or not whole).

## Shape

```json
{
  "schema": "stillhere.report/v1",
  "generated_at": "2026-10-07T00:00:00Z",
  "tool": {"name": "stillhere", "version": "0.1.0"},
  "now": "2026-10-07T00:00:00Z",
  "policy": {"version": 1, "sha256": "<hex>", "headline": "prod_nonblank_lines", "weights_applied_to_headline": false},
  "window": {"days": 7, "days_from": "policy", "sort": "lines_added", "sort_from": "policy",
             "since": "2026-09-30T00:00:00Z", "until": "2026-10-07T00:00:00Z",
             "date_field": "committer", "walk": "first-parent", "diff": "first-parent"},
  "groups": [{"name": "acme", "path": "/abs/acme", "repo_names": ["pay", "ui"]}],
  "warnings": [{"code": "duplicate-repo", "message": "skipped /abs/b; same remote as /abs/a (https://github.com/o/r.git)"}],
  "repos": [{
    "name": "ui", "group": "acme", "path": "/abs/acme/ui",
    "remote_scrubbed": "https://github.com/o/ui.git", "branch": "main", "head": "<sha>",
    "status": "ok", "error": null,
    "errors": {"blame": [{"path": "service.py", "message": "<scrubbed stderr, max 180 chars>"}]},
    "skipped": {"denied_path": 0, "generated_header": 0, "too_big": 0, "unclassified": 0},
    "blame_ignore_revs": {"source": "root", "sha256": "<hex>"},
    "week": {
      "humans": [{"email": "ada@example.com", "name": "Ada Example", "bot": false,
                  "commits": 1, "lines_added": 4, "prod_lines_added": 4}],
      "bots": []
    },
    "still_here": {
      "files_blamed": 0, "files_with_majority": 0,
      "humans": [{"email": "ada@example.com", "name": "Ada Example", "bot": false,
                  "prod_nonblank_lines": 6, "majority_files": 1, "lines_by_role": {"prod": 6}}],
      "bots": [],
      "majority_owner_concentration": {"files_with_majority": 1, "authors_in_prefix": 1,
                                       "bots_in_prefix": 0, "label": "not a truck factor"}
    },
    "hotspots": null
  }],
  "group_rollups": [{"name": "acme", "week": {"humans": [], "bots": []}, "still_here": null}],
  "timings": {"discover_ms": 0, "week_ms": 0, "blame_ms": 0, "hotspot_ms": 0, "render_ms": 0,
              "cache_hits": 0, "cache_misses": 0}
}
```

Changes from the 2026-10-07 shape:

- The narration object is gone. Narrated highlights are specified with the MCP and narrator work in M4.
- `blame_ignore_revs` is added.
- `geared_fp` and `geared_fp_estimated` are left out unless open decision #2 is approved.
- The hotspot row shape lives in [hotspots-code-maat.md](hotspots-code-maat.md).

## Exit codes

| Code | When |
| --- | --- |
| 0 | At least one repo produced week or still-here facts. This includes partial failure, clone-health skips, and skipped hotspots. |
| 1 | No repo produced facts: every path failed, or none was a repo. |
| 2 | A usage error (see below). |
| 128 + n | Interrupted by signal n (SIGINT, SIGTERM or SIGHUP). stillhere stops every git process, then ends itself with the same signal, so a shell sees it die by that signal (`$?` is 130 for Ctrl-C) and stops a loop. Where a process cannot signal itself (Windows), it exits 130. |

Usage errors:

- missing paths, or an unreadable policy file;
- a user policy that lacks a required table: `headline`, `week`, `blame`, `bots`, `ai`, `roles`, `paths`, plus `hotspots` once M5 ships, and `gearing` only if open decision #2 is approved;
- `--ignore-revs-file` on a multi-repo run;
- git older than the supported floor ([git-invocation.md](git-invocation.md#version-floors)).
