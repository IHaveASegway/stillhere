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

Classification follows [spec/classification.md](spec/classification.md): denied paths (vendored, generated, lockfiles), the generated-header sniff, `linguist-generated`/`linguist-vendored` attributes, and roles. Headline metrics count role `prod` only. Lines are non-blank: a line counts when it holds a character outside Unicode's `White_Space` property (Go's `unicode.IsSpace`, which every collector's count uses; Python's `strip()` differs only in also stripping U+001C–U+001F). Blame runs with `-w -M` and the repo's own `.git-blame-ignore-revs`. Pure renames add nothing. Moved lines inside a file keep their original author through `-M`. Moves across files are a known gap (see open decision 6 in [product.md](product.md#open-decisions)).

## Change facts (shared by durable@1 and simplifying@1)

These rules were settled on 2026-10-14, before the first release, so no metric version changed. Their oracles are in [../testdata/fixture/SPEC.md](../testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles). The fixtures keep every commit at least a day from every window boundary, so no oracle covers the half-open boundary itself.

- **Added and removed lines.** `added(c)` and `removed(c)` count non-blank lines on `prod` paths in the commit's own patch against its first parent (a root commit against the empty tree): `git log -p -U0 -w`, with git's default rename detection, pinned so that no repository's config can change it ([spec/week-collector.md](spec/week-collector.md#own-patches-scan)). A path is classified at the scanned HEAD or, when it is absent there, at the newest commit that added lines to it, without the header sniff ([classification.md](spec/classification.md#order)). Because of `-w`, a whitespace-only change adds and removes nothing; blame `-w` already gives those lines to their earlier author. A pure rename adds and removes nothing.
- **Credit.** Each commit is credited to one person (`identity.Credit`): its mailmapped author or, when an agent is the author ([ai_assisted@1](#ai_assisted1)) and a human is a co-author, the first human co-author in trailer order after `git check-mailmap`. An agent commit with no human co-author stays with the agent, in the bot table. No other co-author is credited, so the co-authors GitHub lists on a squash merge earn nothing. The commits a merge landing brought in are credited to their own authors, never to the person who merged them. Blamed lines are credited by the same rule, over the blamed commit's author and co-authors (`LineOwner.CoAuthors`), so the ownership metrics and `durable@1` agree on who wrote an agent's commit.
- **Time.** A commit takes its landing's time (the landing commit's committer time), not its own dates. Every window is half-open, `[since, until)`, like `domain.Window`, counted in 24-hour UTC days. Git's `--since` and `--until` both include the boundary instant (verified on 2.50.1), so a landing at exactly `until` must be dropped in code.
- **Excluded commits.** These add and remove nothing in either metric and are never part of a cluster. They still count as landings in `landed@1`.
  - A merge commit itself. In v1 it has no patch of its own, which would need a combined diff. `git log -p` prints no diff for a merge, except under `--first-parent`, where it prints the first-parent diff: the side branch's lines, as the merger's. Lines that blame credits to a merge commit still count in `surviving@1`.
  - A commit listed in the ignore-revs file that blame used for the repository ([blame-collector.md](spec/blame-collector.md#ignore-revs-per-repository)), because formatting commits are noise ([product.md](product.md#what-meaningful-means-in-v1)).
  - A revert landing: one whose own message, or the message of a commit it brought in, carries `This reverts commit <sha>.` ([spec/week-collector.md](spec/week-collector.md#subjects-and-reverts)). An undo is not new work. Counting it would let delete-then-revert earn durable lines (blame credits the restored lines to the revert) and let add-then-revert earn removed lines.
- **Reverted landings.** A landing is *reverted* when its commit map contains the SHA that a revert landing names. `durable@1` keeps it: its lines are gone, so it already scores 0. `simplifying@1` drops all of its commits.
- **Clusters.** A cluster is one person's counted commits in one landing. `N`, the number eligibility reads, counts clusters, and the bootstrap resamples them.

## Context metrics (shown, never ranked)

### landed@1
- **Question**: What landed in the window?
- **Definition**: landings on the default branch with committer date in `[now − days, now)`, walked with `--first-parent --diff-merges=first-parent`. Per person: landings they authored (the pull request author from M2, otherwise the landing commit's author), `lines_added` (numstat insertions on every path that received a role, blank lines included) and `prod_lines_added` (the `prod` subset), exactly as the week oracle in [../testdata/fixture/SPEC.md](../testdata/fixture/SPEC.md#week-oracle-m0-testweekoracle) fixes them. These are activity numbers and are not comparable to non-blank still-here lines.
- **Why not ranked**: volume rewards volume. A dependency bot led every activity table in the proof run.

### surviving@1
- **Question**: Whose lines are still on the default branch?
- **Definition**: non-blank `prod` lines at HEAD, grouped by the person credited for the blamed commit ([credit](#change-facts-shared-by-durable1-and-simplifying1)): its author, or the first human co-author of an agent's commit.
- **Why not ranked**: it rewards tenure.

## Ownership and knowledge

### majority_owner@1
A person credited with at least 50% of a file's non-blank lines. A 50/50 file has two majority owners and counts once in `files_with_majority`.

### knowledge_concentration@1 (per area)
Sort authors by `majority_files` descending, then `prod_nonblank_lines` descending, then email ascending. Walk from the top until the running sum is at least `files_with_majority / 2` (true division). Report the number of authors in that prefix and how many are bots. Labelled "not a truck factor": it is not Avelino's degree-of-authorship algorithm.

### knowledge_loss@1 (per area)
- **Definition**: the share of an area's surviving `prod` lines whose credited person has had no landing, and (from M2) no submitted review, anywhere in the scanned organization in the last 180 days.
- **Activity**: a person has had a landing when a commit credited to them, merge commits included, is in a landing within `[now − 180 days, now)`. Through the landing map, a side-branch author is active when the branch lands, whoever merged it.
- **Bots**: lines credited to bots are left out of both the numerator and the denominator, because knowledge is held by people. `surviving@1` shows the bot share alongside it.
- **Blind spots**: people who moved to repos outside the scan look gone. Mailmap gaps split one person into two and can make either half look gone.
- **Fixture oracle**: [SPEC.md, knowledge_loss@1](../testdata/fixture/SPEC.md#knowledge_loss1-main-fixture).

### who_knows@1 (per path)
Majority owners of files under the path, the top surviving-line authors, recent landers (90 days), and from M2 recent reviewers (90 days). It is a routing answer, not a rank.
- **Top authors**: up to five, by `prod` non-blank lines under the path, then email ascending.
- **Recent landers**: people credited with a non-merge commit whose own patch changed a path under the query, in a landing within `[now − 90 days, now)`. A side-branch author counts when the branch lands; the person who merged it does not.
- **Bots**: listed apart, never mixed into these lists.
- **Fixture oracle**: [SPEC.md, who_knows@1](../testdata/fixture/SPEC.md#who_knows1-main-fixture).

## Ranked metrics

### durable@1
- **Question**: Did the code a person wrote last?
- **Cohort**: landings in `[now − 365 days, now − 90 days)`. A landing exactly 90 days old belongs to the simplifying window, not to this cohort.
- **Definition**: for each counted commit `c` ([change facts](#change-facts-shared-by-durable1-and-simplifying1)), `added(c)` comes from its own patch, and `surviving(c)` is the non-blank `prod` lines at HEAD that blame (`-w -M`, with the repo's ignore-revs file) attributes to `c`. Per person, `durable = Σ surviving / Σ added` over their commits in the cohort. Lines blamed to a merge commit itself (conflict resolutions) count toward `surviving@1` for the merge's author. They are excluded from `durable@1` in v1, because a merge's own added lines need a combined diff.
- **Clusters**: a cluster needs at least one added line. A landing that adds no `prod` line (a pure rename, a whitespace-only or a test-only change) says nothing about durability, so it is not a cluster and does not count toward eligibility.
- **Reverts**: a reverted landing stays in the cohort and is flagged. Its lines are already gone, so it already counts as not durable. A revert landing is excluded.
- **Eligibility**: at least 10 clusters in the cohort (open decision 5).
- **Blind spots**: a planned replacement of correct code looks "not durable"; moves across files credit the mover; a move inside one file counts as lines the mover added, which blame then gives back to their first author, so the mover scores 0 on them (open decision 6); a revert that restores deleted lines takes their blame from the original author; changes younger than 90 days are not scored.
- **How it can be gamed**: writing in stable, low-traffic code; avoiding risky areas. Load-bearing (M5) is the counterweight.
- **Fixture oracle**: [SPEC.md, change oracles](../testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles). Bob's move and Ada's rename in the main fixture; the revert, requote, re-indent, squash, Copilot and eligibility cases in the sibling.
- **Validity**: unvalidated until the M2 labelled-sample check.

### simplifying@1
- **Question**: Did a person remove code, and did the removal stick?
- **Window**: landings in `[now − 90 days, now)`.
- **Definition**: per person, the sum of `removed(c)` over their counted commits in the window ([change facts](#change-facts-shared-by-durable1-and-simplifying1)), leaving out every commit of a reverted landing. A landing is *simplifying* when the net change of its counted commits, `Σ added − Σ removed`, is −20 lines or fewer; −20 counts. The count of simplifying landings is shown as context.
- **Clusters**: every cluster counts, including one that removed nothing, because for a sum a zero is evidence.
- **Eligibility**: at least 10 clusters in the window.
- **Blind spots**: deleting a feature looks like simplifying (deleting its tests does not, because only `prod` lines count); a move counts its lines as removed, though its net is 0, so it is never a simplifying landing by itself; removed code that comes back later is not detected in v1.
- **How it can be gamed**: deleting then re-adding. Revert detection catches the plain case: neither the reverted landing nor the revert counts.
- **Fixture oracle**: [SPEC.md, change oracles](../testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles). Erin's rename and Bob's merge in the main fixture; the −20 boundary, blank lines, a test deletion, the revert pairs and the requote in the sibling.
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
- **Matching**: the trailer key matches in any case (`Co-Authored-By:` counts). Patterns match the co-author's email, never the name: a co-author named Claude with a GitHub noreply address is a person. A Copilot commit without a human co-author is a bot commit, not an AI-assisted one.
- **Landings**: a landing is AI-assisted when any commit in its commit map is, so a merge landing can be tagged through a side-branch commit.
- **Fixture oracle**: [SPEC.md, sibling AI-assisted tags](../testdata/fixture/SPEC.md#ai-assisted-tags-and-credit).

### change_type@1 (M5)
A pull request label mapped by policy; otherwise the Conventional Commit prefix of the pull request title or squash subject; otherwise `unknown`. Reverts are typed `revert`.

## Ranking rules

- One metric at a time, within one scope and window.
- **Uncertainty**: a bootstrap with B = 1,000 replicates (Efron and Tibshirani, *An Introduction to the Bootstrap*, 1993). How a replicate is drawn depends on the metric's kind:
  - **Ratio metrics** (`durable@1`) resample each person's clusters, their *landings*, with replacement. Lines inside a landing are not independent, so resampling lines would give falsely narrow intervals.
  - **Sum metrics** (`simplifying@1`) resample *weeks*. The window is cut into 7-day blocks counted back from now, the oldest one shorter when the window is not a whole number of weeks (a 90-day window holds 13), and each replicate draws as many blocks as the window holds, with replacement, the same blocks for every person: a block bootstrap (Künsch, "The Jackknife and the Bootstrap for General Stationary Observations", Ann. Statist., 1989). A total depends on how many changes a person landed, and resampling their own clusters would hold that number fixed, which hides most of a total's uncertainty: for a pure count, such as pull requests reviewed, every replicate would return the observed count. Drawing the same weeks for everyone keeps what a week did to the whole team, such as a holiday, out of the comparison between people.
- **Intervals**: a person's value interval runs from the 25th to the 975th of their 1,000 sorted replicate values (the percentile interval). In every replicate the eligible people are ranked, the largest value first, and a person's rank interval runs from the 25th to the 975th of their 1,000 ranks, shown as "1–3". Two value intervals can overlap while the two rank intervals do not, because swapping ranks needs both values at their unlikely ends in the same replicate (Schenker and Gentleman, "On Judging the Significance of Differences by Examining the Overlap Between Confidence Intervals", Am. Statist., 2001).
- **Ties**: tied values share the best rank they cover, as in "1, 1, 3" (standard competition ranking), in every replicate, so a tie never ranks anyone below the people they tie with. Values are compared exactly: sums of counts are exact, and equal ratios of counts are equal numbers. For a sum metric, people with the same totals in every week are drawn from the same weeks, so they tie in every replicate and get equal intervals. For a ratio metric, each person is drawn from their own stream, so people with equal data get intervals that agree only up to Monte Carlo error (see Reproducibility): an end can differ.
- **Eligibility**: below the metric's minimum number of clusters, a person is listed with their value and N, and not ranked. They take no rank in any replicate, so they move nobody else's rank, and no value or rank interval is reported for them (both are 0 in the data). A bootstrap of a few clusters understates its own uncertainty; a ratio over one cluster always gets a zero-width interval.
- **Reproducibility**: the seed is derived from `metric@version`, scope and window. Each person's random stream (ratio metrics) and the week blocks' stream (sum metrics) are derived from the seed, so a person's value interval does not move when someone joins the scope. Draws come from PCG-DXSM (Go's `math/rand/v2` PCG) through stillhere's own unbiased bounded draw (Lemire, "Fast Random Integer Generation in an Interval", ACM TOMACS, 2019), and each person's clusters are sorted first. The same `metric@version`, scope, window and clusters give the same intervals on every platform and Go release. A new window is a new seed, though, and the window moves with `--now`. Each end is one of 1,000 draws, with a Monte Carlo error of about half a percentage point of probability, so a later scan can move an end even when a person's clusters did not change: usually to a neighbouring value, and further where the bootstrap distribution has a gap. On the sibling fixture, Lena's `durable@1` interval starts at 20/30 for 62% of seeds, at 19/30 for 37% and at 18/30 for 1%.
- **Limits**: each interval is about one person, not a joint statement about the whole table. Bootstrap rank intervals are least reliable for people who are tied or nearly tied (Hall and Miller, "Using the Bootstrap to Quantify the Authority of an Empirical Ranking", Ann. Statist., 2009). A person whose clusters all have the same value gets a zero-width value interval, because a bootstrap shows only the variation its sample holds. At the sample sizes ranks start at, a 95% percentile interval covers less than 95%. In simulation ([ADR 0010](adr/0010-rank-intervals-bootstrap-per-kind.md#consequences)), value intervals covered about 87% of true values at 10 landings and 88–91% for 13-week sums, and rank intervals covered 91–97% of true ranks, 88% in a team of near-equals.
- Bots are never ranked. AI-assisted work is included, with a filter.
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
