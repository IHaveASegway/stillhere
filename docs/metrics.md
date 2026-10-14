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

## Change facts (shared by durable@1 and simplifying@1)

These rules were settled on 2026-10-14, before the first release, so no metric version changed. Their oracles are in [../testdata/fixture/SPEC.md](../testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles). The fixtures keep every commit at least a day from every window boundary, so no oracle covers the half-open boundary itself.

- **Added and removed lines.** `added(c)` and `removed(c)` count non-blank lines on `prod` paths in the commit's own patch against its first parent (a root commit against the empty tree): `git log -p -U0 -w`, with git's default rename detection. A path is classified at the scanned HEAD or, when it is absent there, at the newest commit that added lines to it, without the header sniff ([classification.md](spec/classification.md#order)). Because of `-w`, a whitespace-only change adds and removes nothing; blame `-w` already gives those lines to their earlier author. A pure rename adds and removes nothing.
- **Credit.** Each commit is credited to one person (`identity.Credit`): its mailmapped author or, when an agent is the author ([ai_assisted@1](#ai_assisted1)) and a human is a co-author, the first human co-author in trailer order after `git check-mailmap`. An agent commit with no human co-author stays with the agent, in the bot table. No other co-author is credited, so the co-authors GitHub lists on a squash merge earn nothing. The commits a merge landing brought in are credited to their own authors, never to the person who merged them. Blamed lines are credited by the same rule, over the blamed commit's author and co-authors (`LineOwner.CoAuthors`), so the ownership metrics and `durable@1` agree on who wrote an agent's commit.
- **Time.** A commit takes its landing's time (the landing commit's committer time), not its own dates. Every window is half-open, `[since, until)`, like `domain.Window`, counted in 24-hour UTC days. Git's `--since` and `--until` both include the boundary instant (verified on 2.50.1), so a landing at exactly `until` must be dropped in code.
- **Excluded commits.** These add and remove nothing in either metric and are never part of a cluster. They still count as landings in `landed@1`.
  - A merge commit itself. In v1 it has no patch of its own, which would need a combined diff. `git log -p` prints no diff for a merge, except under `--first-parent`, where it prints the first-parent diff: the side branch's lines, as the merger's. Lines that blame credits to a merge commit still count in `surviving@1`.
  - A commit listed in the ignore-revs file that blame used for the repository ([blame-collector.md](spec/blame-collector.md#ignore-revs-per-repository)), because formatting commits are noise ([product.md](product.md#what-meaningful-means-in-v1)).
  - A revert landing, whose message carries `This reverts commit <sha>.`. An undo is not new work. Counting it would let delete-then-revert earn durable lines (blame credits the restored lines to the revert) and let add-then-revert earn removed lines.
- **Reverted landings.** A landing is *reverted* when its commit map contains the SHA that a revert landing names. `durable@1` keeps it: its lines are gone, so it already scores 0. `simplifying@1` drops all of its commits.
- **Clusters.** A cluster is one person's counted commits in one landing. `N`, the number eligibility reads, counts clusters, and the bootstrap resamples them.
- **Missing facts are not zeros.** A repository without the commits' own patch counts (a week run, a partial clone) adds no cluster to either metric, and one without blame (a shallow or partial clone) adds none to `durable@1`: its added or surviving lines are unknown, and counting them as 0 would score its people as removing nothing or keeping nothing.

## Values: what every metric returns

Each metric returns `domain.MetricValue`s. They are a published language: the report, the store, `explain` and the MCP server all read them, so these rules hold for every metric in this catalog, and each entry names its subjects and `Detail` keys.

- **Scopes and subjects.** A value about a person (subject `person`, the canonical email) is computed in each repository (scope `repo`), each group (scope `group`) and the whole run (scope `all`), over the repositories in that scope. A value about code has the code as its subject: a repository (subject `repo`), a top-level directory (subject `area`, keyed `<repo key>:<dir>/`) or a group (subject `group`), in the scope of its repository or group. A group's code values are recomputed over its repositories, never summed from per-repository values, and code values have no cross-group total. A value about one change has subject `change` (the landing SHA).
- **Window.** The metric's own window for `--now`, half-open. A snapshot at the scanned HEAD (surviving lines, ownership) has the zero window.
- **Value, Num, Den, N.** A ratio sets `Num` and `Den`, and `Value` is their quotient; any other value has `Num = Value` and `Den = 1`. `N` is the sample size eligibility reads: clusters for the ranked metrics, landings or clusters for the shares below, and 0 where nothing is sampled.
- **Detail.** Context numbers, never ranked, with the snake_case keys each entry lists. A ratio over nothing is left out rather than written as NaN.
- **Bots** get values like people, so the report can list them in the bot table; `identity.Directory.Bot` tells them apart, and the ranked metrics leave them out of the clusters they hand to the bootstrap.
- **Missing facts.** A repository whose collection failed gives no value. One without blame gives no blame-based value, and one without patch counts no change value ([change facts](#change-facts-shared-by-durable1-and-simplifying1)). One whose history (`RepoFacts.History`) does not cover a metric's window gives no landing-based number for it, so a week run never yields a 365-day share computed from seven days.
- **Order.** Values are sorted by metric, scope, subject and window, so two runs over the same facts give the same bytes.

## Context metrics (shown, never ranked)

### landed@1
- **Question**: What landed in the window?
- **Unit and scope**: landings per person, in each repository, group and all.
- **Inputs**: landings, commits (authors and co-authors), file changes (numstat), path classes.
- **Definition**: landings on the default branch with committer date in `[now − days, now)`, walked with `--first-parent --diff-merges=first-parent`, where `days` is `--days`, else the policy's `[week] days`. Per person: landings they authored (the pull request author from M2, otherwise the person the landing commit is [credited](#change-facts-shared-by-durable1-and-simplifying1) to: its author, or its first human co-author when an agent wrote it). Value: landings. `Detail`: `lines_added` (numstat insertions on every path that received a role, blank lines included; a merge landing's are its first-parent diff) and `prod_lines_added` (the `prod` subset), exactly as the week oracle in [../testdata/fixture/SPEC.md](../testdata/fixture/SPEC.md#week-oracle-m0-testweekoracle) fixes them. These are activity numbers and are not comparable to non-blank still-here lines.
- **Eligibility and uncertainty**: none: a count, never ranked.
- **Why not ranked**: volume rewards volume. A dependency bot led every activity table in the proof run.
- **Blind spots**: a merged branch's lines count for whoever landed it; numstat counts blank lines and moves; work that never reaches the default branch is invisible.
- **How it can be gamed**: splitting work into many small landings.
- **Fixture oracle**: [SPEC.md, week oracle](../testdata/fixture/SPEC.md#week-oracle-m0-testweekoracle).
- **Validity**: context, exact by construction; it says nothing about value.

### surviving@1
- **Question**: Whose lines are still on the default branch?
- **Unit and scope**: non-blank lines at HEAD per person, in each repository, group and all; and one value about each repository and each group.
- **Inputs**: line owners (blame with the repository's ignore-revs file), path classes, landings and the landing map.
- **Definition**: non-blank `prod` lines at HEAD, grouped by the person credited for the blamed commit ([credit](#change-facts-shared-by-durable1-and-simplifying1)): its author, or the first human co-author of an agent's commit. A person whose surviving lines are all on other roles has the value 0, with those lines in `Detail`.
  - Per person, `Detail`: `repos`, the repositories in the scope where the person has surviving `prod` lines (breadth); `majority_files`, as in [majority_owner@1](#majority_owner1) (depth); `lines_<role>` for each role the person has surviving lines on, such as `lines_prod` and `lines_test`; and `landings_365d`, the landings in `[now − 365 days, now)` whose commit map holds a commit credited to them, merge commits included (activity). Breadth, depth and activity together show a person's shape: platform-wide author, service owner, or someone who edits widely inside other people's files.
  - Per repository and per group (subject `repo` or `group`): Value `prod_lines`, and `Detail` `prod_lines`, `prod_files` (`prod` files with surviving lines), `people` (distinct humans with surviving `prod` lines), `bot_share` (the share of surviving `prod` lines credited to bots) and `ai_assisted_share` (the share of the landings in `[now − 365 days, now)` tagged [ai_assisted@1](#ai_assisted1)).
- **Eligibility and uncertainty**: none: never ranked.
- **Why not ranked**: it rewards tenure.
- **Blind spots**: blame credits the last author of a line, so a reformat missing from `.git-blame-ignore-revs` moves lines to its author; moves across files credit the mover (no `-C`); a squash merge credits every line of a pull request to its author, and its co-authors get none; shallow and partial clones have no value.
- **How it can be gamed**: rewriting other people's lines (a reformat, a mass rename) moves their blame to the rewriter.
- **Fixture oracle**: [SPEC.md, still-here oracle](../testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle).
- **Validity**: context, never ranked. Blame as an authorship signal: Avelino, Passos, Petrillo and Valente, "Who Can Maintain This Code?" (IEEE Software 36(6), 2019; first presented at SBES 2018), compared the maintainers developers named in ten systems with three repository-mining techniques. On the two commercial systems, the share of a file's lines a developer owns in the last version (blame) matched them best (a harmonic mean of hit ratios of 67%, against 66% for commit counts and 63% for degree of authorship); on eight open-source systems the three were within 60–63%. Their threshold was relative to a file's top author (best at 10%), not half of the file, so the study supports the signal, not stillhere's exact rule.

## Ownership and knowledge

### majority_owner@1
- **Question**: Which files does a person hold most of?
- **Unit and scope**: `prod` files at HEAD per person, in each repository, group and all.
- **Inputs**: line owners, path classes.
- **Definition**: a person credited with at least 50% of a file's non-blank lines owns it. A 50/50 file has two majority owners and counts once in `files_with_majority`; never `max()` alone, which hides the split. Value: the files the person owns. Bots can be majority owners and stay in the bot table.
- **Eligibility and uncertainty**: none: never ranked.
- **Blind spots**: blame's, as in `surviving@1`. A file nobody holds half of has no owner, so highly collaborative files are invisible here.
- **How it can be gamed**: rewriting a file's lines takes its ownership.
- **Fixture oracle**: [SPEC.md, still-here oracle](../testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle) (`majority_files`, and the 2/2 split of `split.py`).
- **Validity**: context, never ranked. Bird, Nagappan, Murphy, Gall and Devanbu, "Don't Touch My Code!" (ESEC/FSE 2011), found that ownership measures relate to pre-release faults and post-release failures in Windows Vista and Windows 7: components with more minor contributors (under 5% of the commits) failed more, and a higher top-owner share failed less. Their ownership counts commits per component, not surviving lines per file. Greiler, Herzig and Czerwonka (MSR 2015) replicated the link at Microsoft; Foucault, Falleri and Blanc (EASE 2014) found it weak in open source, where module size predicted faults better. For blame as the authorship signal, see `surviving@1`.

### knowledge_concentration@1 (per area)
- **Question**: How few people hold half of the code's majority-owned files?
- **Unit and scope**: people, per repository (subject `repo`), per top-level directory (subject `area`) and per group (subject `group`).
- **Inputs**: line owners, path classes.
- **Definition**: sort authors by `majority_files` descending, then `prod_nonblank_lines` descending, then email ascending. Walk from the top until the running sum is at least `files_with_majority / 2` (true division). Report the number of authors in that prefix and how many are bots. Labelled "not a truck factor": it is not Avelino's degree-of-authorship algorithm. Value: `authors_in_prefix`. `Detail`: `files_with_majority` (each file once, even with two owners), `bots_in_prefix`, `prod_files`. A group re-runs the walk on its summed columns; it is not the sum of its repositories' prefixes. An area where no file has a majority owner has no value.
- **Eligibility and uncertainty**: none: it describes code, not people.
- **Blind spots**: only majority-owned files count, so code where nobody holds half of any file looks unconcentrated; it ignores who could take a file over.
- **How it can be gamed**: it is no one's target; it moves when ownership moves.
- **Fixture oracle**: [SPEC.md, still-here oracle](../testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle), the concentration walk.
- **Validity**: context. Built on `majority_owner@1` (see its evidence). The validated algorithm it deliberately is not: Avelino, Passos, Hora and Valente's truck factor (ICPC 2016).

### knowledge_loss@1 (per area)
- **Question**: How much of the code is held by people who have gone quiet?
- **Unit and scope**: a share of surviving `prod` lines, per repository (subject `repo`), per top-level directory (subject `area`) and per group (subject `group`).
- **Inputs**: line owners, path classes, landings and the landing map of every scanned repository.
- **Definition**: the share of an area's surviving `prod` lines whose credited person has had no landing, and (from M2) no submitted review, anywhere in the scanned organization in the last 180 days. `Num` is those lines, `Den` all lines held by people. `Detail`: `people` (humans holding lines in the area) and `people_inactive`.
- **Activity**: a person has had a landing when a commit credited to them, merge commits included, is in a landing within `[now − 180 days, now)`. Through the landing map, a side-branch author is active when the branch lands, whoever merged it.
- **Bots**: lines credited to bots are left out of both the numerator and the denominator, because knowledge is held by people. `surviving@1` shows the bot share alongside it.
- **Eligibility and uncertainty**: none: it describes code, not people.
- **Blind spots**: people who moved to repos outside the scan look gone. Mailmap gaps split one person into two and can make either half look gone. Reviews do not count as activity until M2.
- **How it can be gamed**: a quiet expert who lands one trivial change resets their clock.
- **Fixture oracle**: [SPEC.md, knowledge_loss@1](../testdata/fixture/SPEC.md#knowledge_loss1-main-fixture).
- **Validity**: context, unvalidated; inactivity stands in for departure, which it is not. Prior art measures the same thing: Rigby, Zhu, Donadelli and Mockus, "Quantifying and Mitigating Turnover-Induced Knowledge Loss" (ICSE 2016), used files abandoned by departed developers as the proxy at Chrome and Avaya, and CodeScene's knowledge-loss view (Tornhill, *Software Design X-Rays*, 2018) does it with history.

### who_knows@1 (per path)
Majority owners of files under the path, the top surviving-line authors, recent landers (90 days), and from M2 recent reviewers (90 days). It is a routing answer, not a rank, computed per query (`who-knows PATH`, `metrics.WhoKnows`) over a run's facts; a scan stores no values for it, because precomputing every directory of every repository would store more rows than the facts.
- **Top authors**: up to five, by `prod` non-blank lines under the path, then email ascending.
- **Recent landers**: people credited with a non-merge commit whose own patch changed a path under the query, in a landing within `[now − 90 days, now)`. A side-branch author counts when the branch lands; the person who merged it does not. Noise first: the change must touch non-blank lines on a path that received a role, as its destination or as a rename's source, and commits in the ignore-revs file do not count.
- **Bots**: listed apart, never mixed into these lists: their top five is ranked among bots.
- **Path**: a repo-relative prefix that matches whole segments, so `src` covers `src/pay.go` but not `srcutil/x.go`; `""` is the whole repository.
- **Values**: per repository with anything under the path, one value about the path (subject `repo` for the whole repository, else `area`, keyed `<repo key>:<file>` or `<repo key>:<dir>/`) whose Value is its `prod` lines, with `Detail` `prod_files` and `files_with_majority`; and one value per person listed, whose Value is their `prod` lines under the path, with `Detail` `majority_files`, `recent_landings` and, for the top five, `top_author` (1 to 5). The window is the recent landers'.
- **Blind spots**: someone who knows the code but has neither surviving lines nor a recent landing under it does not appear; moves across files credit the mover.
- **How it can be gamed**: it is no one's target; appearing in it earns questions.
- **Fixture oracle**: [SPEC.md, who_knows@1](../testdata/fixture/SPEC.md#who_knows1-main-fixture).
- **Validity**: context. Routing by experience is old: Mockus and Herbsleb, "Expertise Browser" (ICSE 2002), recommended experts from their changes. For blame as an authorship signal, see `surviving@1`.

## Ranked metrics

### durable@1
- **Question**: Did the code a person wrote last?
- **Unit and scope**: a ratio of non-blank `prod` lines per person, in each repository, group and all; ranked one scope at a time.
- **Inputs**: landings and the landing map, commits, the commits' own patch counts, line owners, ignore-revs SHAs, reverts, path classes.
- **Cohort**: landings in `[now − 365 days, now − 90 days)`. A landing exactly 90 days old belongs to the simplifying window, not to this cohort.
- **Definition**: for each counted commit `c` ([change facts](#change-facts-shared-by-durable1-and-simplifying1)), `added(c)` comes from its own patch, and `surviving(c)` is the non-blank `prod` lines at HEAD that blame (`-w -M`, with the repo's ignore-revs file) attributes to `c`. Per person, `durable = Σ surviving / Σ added` over their commits in the cohort: `Num` and `Den`, with `N` the clusters. Lines blamed to a merge commit itself (conflict resolutions) count toward `surviving@1` for the merge's author. They are excluded from `durable@1` in v1, because a merge's own added lines need a combined diff.
- **Clusters**: a cluster needs at least one added line. A landing that adds no `prod` line (a pure rename, a whitespace-only or a test-only change) says nothing about durability, so it is not a cluster and does not count toward eligibility.
- **Reverts**: a reverted landing stays in the cohort and is flagged: `Detail` `reverted_landings` counts the person's clusters in reverted landings. Its lines are already gone, so it already counts as not durable. A revert landing is excluded.
- **Uncertainty**: a Ratio cluster bootstrap ([ranking rules](#ranking-rules)): `Clusters` hands each person's clusters, `(landing, surviving, added)`, to `rank.Intervals`.
- **Eligibility**: at least 10 clusters in the cohort (open decision 5).
- **Blind spots**: a planned replacement of correct code looks "not durable"; moves across files credit the mover; a move inside one file counts as lines the mover added, which blame then gives back to their first author, so the mover scores 0 on them (open decision 6); a revert that restores deleted lines takes their blame from the original author; changes younger than 90 days are not scored.
- **How it can be gamed**: writing in stable, low-traffic code; avoiding risky areas. Load-bearing (M5) is the counterweight.
- **Fixture oracle**: [SPEC.md, change oracles](../testdata/fixture/SPEC.md#change-oracles-m1-testchangeoracles). Bob's move and Ada's rename in the main fixture; the revert, requote, re-indent, squash, Copilot and eligibility cases in the sibling.
- **Validity**: unvalidated until the M2 labelled-sample check.

### simplifying@1
- **Question**: Did a person remove code, and did the removal stick?
- **Unit and scope**: non-blank `prod` lines removed per person, in each repository, group and all; ranked one scope at a time.
- **Inputs**: landings and the landing map, commits, the commits' own patch counts, ignore-revs SHAs, reverts, path classes.
- **Window**: landings in `[now − 90 days, now)`.
- **Definition**: per person, the sum of `removed(c)` over their counted commits in the window ([change facts](#change-facts-shared-by-durable1-and-simplifying1)), leaving out every commit of a reverted landing. A landing is *simplifying* when the net change of its counted commits, everyone's together, `Σ added − Σ removed`, is −20 lines or fewer; −20 counts. The count of simplifying landings is shown as context: `Detail` `simplifying_landings`, the person's clusters in simplifying landings.
- **Clusters**: every cluster counts, including one that removed nothing, because for a sum a zero is evidence.
- **Uncertainty**: resampling a person's own clusters would always return the same sum, so the bootstrap resamples week blocks shared by everyone ([ranking rules](#ranking-rules)): `Clusters` tags each cluster with its block, the week counted back from `--now` (0 is `[now − 7 days, now)`), and passes the 13 blocks the 90 days span.
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
- **Matching**: the trailer key matches in any case (`Co-Authored-By:` counts). Patterns match the co-author's email, never the name: a co-author named Claude with a GitHub noreply address is a person. A Copilot commit without a human co-author is a bot commit, not an AI-assisted one: a commit is AI-assisted only when the person it is credited to is human.
- **Agents**: an identity is an agent when its email is one of `[ai].coauthor_emails` or its email or display name contains an `[ai].agent_authors` fragment. Agents count as bots wherever they are credited (ADR 0007: agents are never ranked rows), even without a `[bots]` fragment, so an agent commit with no human co-author lands in the bot table.
- **Landings**: a landing is AI-assisted when any commit in its commit map is, so a merge landing can be tagged through a side-branch commit.
- **Values**: over landings in `[now − 365 days, now)`.
  - The tag: one value per AI-assisted landing (subject `change`, scope its repository), Value 1. An untagged landing has none.
  - Cohort comparisons per repository and per group (subject `repo` or `group`): the AI-assisted share of the landings (`Num` tagged, `Den` and `N` all), with `Detail` `durable_ai` and `durable_other` (`durable@1`'s `Σ surviving / Σ added` over the clusters of tagged and of untagged landings in its cohort; left out when a side has none), `durable_ai_n` and `durable_other_n` (their clusters), and `reverted_ai` and `reverted_other` (the reverted landings among each).
  - Per person (each repository, group and all, humans only): the share of their clusters, over non-merge commits, in which one of their own commits is AI-assisted. It is shown only to that person (`me`), never in team views.
- **Eligibility and uncertainty**: none: a tag and a cohort comparison, never ranked.
- **Blind spots**: a lower bound, as above; a tool whose trailer the policy does not list (Cursor's is unverified) is missed; GitHub squash merges list human co-authors, who are never tagged.
- **How it can be gamed**: removing a trailer hides the tag; adding one to a commit no agent touched fakes it.
- **Fixture oracle**: [SPEC.md, sibling AI-assisted tags](../testdata/fixture/SPEC.md#ai-assisted-tags-and-credit), and the main fixture's 0 of 16 landings.
- **Validity**: a tag, not a measure of quality: exact for the trailers and authors the policy names, blind to the rest.

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
