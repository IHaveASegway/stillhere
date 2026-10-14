# Metric catalog

Status: draft v2 · 2026-10-14 · the published language people and agents bind to

Every metric has an id and a version, such as `durable@1`. A change to its definition, inputs or defaults that can change any value is a new version. The old version stays computable until a release removes it with a changelog entry. Reports, web pages and MCP answers cite the version they used.

Each entry states: the question, the unit, the definition, eligibility, uncertainty, blind spots, how it can be gamed, its fixture oracle, and its validity status. A metric without all of these does not ship.

## Units

- **Commit**: one git commit, with its author identified by the mailmapped email.
- **Landing**: a first-parent commit on the default branch. A merge landing `M` owns the commits in `git rev-list M^1..M`: the merge itself plus everything it brought in, which also covers octopus merges. A squash or rebase landing owns itself. See [spec/week-collector.md](spec/week-collector.md#landing--commit-map).
- **Change**: a landing in M1. From M2, a pull request when GitHub knows one (its landings grouped together). Moving to pull-request grouping is a version bump for any metric whose values move.
- **Person**: a canonical email after `.mailmap`. Bots (policy name patterns) are never people in ranks.
- **Area**: a directory prefix, or a CODEOWNERS entry when the repo has one.
- **Scope**: a repo or a group of repos. Ranks never mix groups unless asked to.

## Noise (applied before every metric)

Classification follows [spec/classification.md](spec/classification.md): denied paths (vendored, generated, lockfiles), the generated-header sniff, `linguist-generated`/`linguist-vendored` attributes, and roles. Headline metrics count role `prod` only. Lines are non-blank (`strip() != ""`). Blame runs with `-w -M` and the repo's own `.git-blame-ignore-revs`. Pure renames add nothing. Moved lines inside a file keep their original author through `-M`. Moves across files are a known gap (see open decision 6 in [product.md](product.md#open-decisions)).

## Context metrics (shown, never ranked)

### landed@1
- **Question**: What landed in the window?
- **Definition**: landings on the default branch with committer date in `[now − days, now)`, walked with `--first-parent --diff-merges=first-parent`. Per person: landings they authored (the pull request author from M2, otherwise the landing commit's author), `lines_added` (numstat insertions on every path that received a role, blank lines included) and `prod_lines_added` (the `prod` subset), exactly as the week oracle in [../testdata/fixture/SPEC.md](../testdata/fixture/SPEC.md#week-oracle-m0-testweekoracle) fixes them. These are activity numbers and are not comparable to non-blank still-here lines.
- **Why not ranked**: volume rewards volume. A dependency bot led every activity table in the proof run.

### surviving@1
- **Question**: Whose lines are still on the default branch?
- **Definition**: non-blank `prod` lines at HEAD, grouped by the blame author.
- **Why not ranked**: it rewards tenure.

## Ownership and knowledge

### majority_owner@1
An author with at least 50% of a file's non-blank lines. A 50/50 file has two majority owners and counts once in `files_with_majority`.

### knowledge_concentration@1 (per area)
Sort authors by `majority_files` descending, then `prod_nonblank_lines` descending, then email ascending. Walk from the top until the running sum is at least `files_with_majority / 2` (true division). Report the number of authors in that prefix and how many are bots. Labelled "not a truck factor": it is not Avelino's degree-of-authorship algorithm.

### knowledge_loss@1 (per area)
- **Definition**: the share of an area's surviving `prod` lines whose blame author has had no landing, and (from M2) no submitted review, anywhere in the scanned organization in the last 180 days.
- **Blind spots**: people who moved to repos outside the scan look gone. Mailmap gaps split one person into two and can make either half look gone.

### who_knows@1 (per path)
Majority owners of files under the path, the top surviving-line authors, recent landers (90 days), and from M2 recent reviewers (90 days). It is a routing answer, not a rank.

## Ranked metrics

### durable@1
- **Question**: Did the code a person wrote last?
- **Cohort**: commits in landings that landed 90 to 365 days before `--now`.
- **Definition**: for each commit `c`, `added(c)` is the non-blank `prod` lines it added (from its own patch against its first parent, after noise), and `surviving(c)` is the non-blank `prod` lines at HEAD that blame attributes to `c`. Per person, `durable = Σ surviving / Σ added` over their commits in the cohort. Lines blamed to a merge commit itself (conflict resolutions) count toward `surviving@1` for the merge's author. They are excluded from `durable@1` in v1, because a merge's own added lines need a combined diff.
- **Reverts**: a landing reverted by a later commit ("This reverts commit <sha>") is flagged. Its lines are already gone, so it already counts as not durable.
- **Eligibility**: at least 10 landings in the cohort (open decision 5).
- **Blind spots**: a planned replacement of correct code looks "not durable"; moves across files credit the mover; changes younger than 90 days are not scored.
- **How it can be gamed**: writing in stable, low-traffic code; avoiding risky areas. Load-bearing (M5) is the counterweight.
- **Validity**: unvalidated until the M2 labelled-sample check.

### simplifying@1
- **Question**: Did a person remove code, and did the removal stick?
- **Window**: landings in the last 90 days.
- **Definition**: per person, the sum of non-blank substantive `prod` lines removed by their commits, excluding commits in reverted landings. A landing is *simplifying* when its net non-blank change is −20 lines or fewer. The count of simplifying landings is shown as context.
- **Eligibility**: at least 10 landings in the window.
- **Blind spots**: deleting a feature or its tests looks like simplifying; removed code that comes back later is not detected in v1.
- **How it can be gamed**: deleting then re-adding. Revert detection catches the plain case.
- **Validity**: unvalidated until the M2 labelled-sample check.

### reviews_given@1 (M2)
- **Question**: Who carries the review load?
- **Definition**: per person, distinct merged pull requests authored by someone else on which they submitted at least one review (approved, changes requested, or commented) in the last 90 days. Shown with it: review comments written, and the share of approvals with no comment.
- **Eligibility**: at least 10 reviewed pull requests.
- **How it can be gamed**: rubber-stamp approvals. The no-comment share is shown next to the count for that reason.

### load_bearing@1 (M5)
- **Definition**: the share of a person's landings in the window that touched a top-decile hotspot, with hotspots computed from the 12 months *before* the window so a person's own changes cannot make a file hot.
- **Hotspot**: change frequency overlapping size (revisions in 12 months, and current non-blank lines), as Tornhill and CodeScene define it. Revisions alone are change frequency, not a hotspot.

## Tags and types

### ai_assisted@1
A commit is AI-assisted when a trailer or author matches the policy's versioned pattern list. Defaults: a co-author email of `noreply@anthropic.com` (Claude Code; match the email, not the name), `aider@aider.chat` (Aider), and the Copilot coding agent as author with a human co-author. When an agent is the author and a human the co-author, the human is credited. This is a lower bound, because editor completions leave no trace. It is shown as cohort comparisons in team views; a person's own AI-assisted share is shown only to that person.

### change_type@1 (M5)
A pull request label mapped by policy; otherwise the Conventional Commit prefix of the pull request title or squash subject; otherwise `unknown`. Reverts are typed `revert`.

## Ranking rules

- One metric at a time, within one scope and window.
- **Uncertainty**: a cluster bootstrap that resamples a person's *landings* (lines inside a landing are not independent, so resampling lines would give falsely narrow intervals), with B = 1,000 replicates. The seed is derived from `metric@version`, scope and window, so the same inputs give the same intervals. Each person's rank interval is the 2.5th to 97.5th percentile of their rank across replicates, shown as "1–3".
- Bots are never ranked. AI-assisted work is included, with a filter.
- Below the eligibility threshold: listed, not ranked.
- Canonical reading on why rank intervals matter: Goldstein and Spiegelhalter, "League Tables and Their Limitations" (J. R. Statist. Soc. A, 1996).

## Highlights (visible to all signed-in members)

Each highlight is about a *change*, links to its evidence, and shows ties together:
- **Biggest simplification**: the most substantive lines removed in a non-reverted landing in the last 30 days.
- **Most durable feature**: the largest landing in the durability cohort with `durable ≥ 0.9` (typed `feat` once M5 ships).
- **Most helpful reviewer** (M2): the most pull requests reviewed with at least one comment in the last 30 days, minimum 5.

## Validity checks

`stillhere validate --labels labels.csv` reads hand labels (change id, `meaningful` / `routine` / `noise`, labeller) and reports, per metric version, how well the metric separates meaningful from routine changes (AUC). A ranked metric stays marked "beta" until it reaches AUC ≥ 0.7 on at least 50 labelled changes. The labels file holds judgments about colleagues' work, so it is kept outside the repo by default.

## Adding a metric

Copy this block, fill every line, add a fixture oracle, and register the metric:

```
### <id>@1
- Question:
- Unit and scope:
- Inputs (fact tables):
- Definition:
- Eligibility:
- Uncertainty:
- Blind spots:
- How it can be gamed:
- Fixture oracle: testdata/...
- Validity: unvalidated | AUC x.xx on n labels (date)
```
