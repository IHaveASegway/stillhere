# Getting started in five minutes

Status: 2026-10-15, samples regenerated 2026-10-16 (wave 10) · a first session on your own repository, every step with the output it gives · the commands are in [the README](../README.md#getting-started-in-five-minutes), the shapes in [spec/report-json.md](spec/report-json.md)

This walks through one repository, from nothing to ranks, people to ask and an agent that can read it all. The output below is real: [cli/cli](https://github.com/cli/cli), GitHub's command line (602 landings in a year, 93,000 lines of production code), scanned at `--now 2026-10-15` with GitHub data synced, then abbreviated (`…`). People's names, and the ids that stand in for them, are replaced by placeholders (Person A, Person B, …, `p-…`), as they would be in anything you share; bots keep theirs. Steps 6 and 7, which rank people, use placeholders of their own (Person P, Q, R) and placeholder commits (`<sha>`, `<a commit subject>`), so no rank on this page can be joined to a name in another step or to a commit you could look up. Run the same commands in a clone of your own.

stillhere reads only what is already cloned, and only `sync github` touches the network. Every number it prints is a property of changes and code, never of a person's worth: it must not be the basis for performance or pay decisions.

## 1. Install

```sh
go install github.com/IHaveASegway/stillhere/cmd/stillhere@main
command -v stillhere            # prints its path when the Go bin directory is on your PATH
stillhere version
```

git 2.45 or newer must be on your `PATH`. `@main` is the newest commit; `@latest` can lag it by hours through the Go module proxy, and `GOPROXY=direct go install github.com/IHaveASegway/stillhere/cmd/stillhere@main` skips the proxy once. If `go env GONOPROXY` (or `GOPRIVATE`) matches github.com, as a company's settings often do, the first command fetches with your git directly, so git must be able to read github.com over HTTPS. Or build from a checkout with `make build` ([Install](../README.md#install)).

No release exists yet, so there is no Homebrew cask, no `@latest` release and no Docker image: `brew install --cask ihaveasegway/tap/stillhere` fails with `Cask 'stillhere' is unavailable` until the first release. [Install](../README.md#after-the-first-release-none-yet) lists those commands for then.

## 2. What landed this week

```sh
cd ~/src/cli                     # any clone; fetch first for fresh numbers
stillhere week .
```

```text
stillhere week · as of 2026-10-15 UTC
landed window 2026-10-08 → 2026-10-15 UTC: 7 days (policy), sorted by lines_added (policy)
…
  What landed, 2026-10-08 → 2026-10-15 UTC, by lines added
    17 landings · 20 commits · 15 merges · 1,989 lines added, 801 prod
    person           landings  commits  lines added  prod lines added  merges
    Person A                7        8        1,970               790      13
    Person B                1        1            9                 9       1
    Person C                1        1            2                 2       0
    Person D                1        1            0                 0       0
    Person E                0        0            0                 0       1
    bots
    dependabot[bot]         8        8            8                 0       0
    cli automation          1        1            0                 0       0
```

How to read it:

- A **landing** is a commit on the default branch's first-parent chain: a merge, a squash, or a direct push. It counts for everyone who wrote one of its commits, so a merged branch counts for its authors, not for whoever pressed merge (`landed@2`). **merges** says who did the merging: Person E merged one branch and wrote none of this week's code.
- Bots have a table of their own and are never ranked. dependabot's eight bumps are its own, though people merged them. `cli automation` is a workflow committing as GitHub's own address, `noreply@github.com`, which the packaged policy counts as a bot. A service account it does not know would be a person's row: `stillhere policy > stillhere.toml` and a `[bots]` fragment move it to the bots.
- **lines added** are activity: insertions on paths with a role, blank lines included. They are not comparable to the surviving lines below, and they reward volume, which is why nothing here is ranked.

## 3. Record a run

```sh
stillhere scan .                 # history, blame, metrics and ranks into ~/.stillhere/facts.db (5 s here)
```

`scan` prints the run as a report, and `stillhere report` prints it again later, from facts.db alone. The repository's card comes first:

```text
Repository cli · trunk @ ff67d76 (GitHub's default branch) · ok
  age 2,567 days (first commit 2019-10-04 UTC) · 1,495 files at HEAD, 530 prod paths · 93,356
  surviving prod lines in 530 prod files · 468 people, 4 bots · landings: 17 in the window, 602 in
  the history
  made of: Person E 9,840 (10.5%, active 2026-10-14), Person F 9,838 (10.5%, no activity in
    365 days), Person A 8,138 (8.7%, active 2026-10-14), Person G 7,977 (8.5%, no activity in
    365 days), Person H 7,750 (8.3%, active 2026-10-14)
  knowledge loss 59.6% (55,538 of 93,196 human-held lines) · AI-assisted 33.6% (202 of 602 landings)
  · bot share 0.2% (160 of 93,356 lines)
  going quiet within 30 days: Person K 475 lines (quiet in 3 days), (name withheld 1) 28 lines
    (quiet in 6 days), Person L 3 lines (quiet in 9 days), …
  review coverage 78.8% (104 of 132 human changes, 90 days) · 19 pull requests merged without
    another's review · 1 pull request merged into another branch, which a merge brought · 9 landings
    with no pull request in the GitHub data · 11 landings naming a pull request the GitHub data
    lacks, left out (sync github again) · 54 reviewed only by an approval with no comment
  majority-owner concentration: 6 authors (0 bots) hold the majority in half of the 384 files with a
  majority owner (not a truck factor)

  Areas: top-level directories, by the prod lines people hold there
    area        lines  knowledge loss  people gone quiet  authors
    pkg/       71,898           63.2%         382 of 421        8
    internal/  13,240           36.8%           77 of 97        3
    api/        5,905           63.1%           67 of 72        1
    …
    3 more areas (--all lists them)
  …
  Still here at ff67d76: surviving non-blank lines (1,046 files blamed)
    person                  prod lines  majority files  landings 365d  last active   other roles
    Person E                     9,840              38             95  2026-10-14    doc 270 · iac …
    Person F                     9,838              44              0  none in 365d  doc 129 · iac …
    …
    458 more people (--all lists them)
```

How to read it:

- **made of** and **still here** are surviving lines: what blame still credits to each person at HEAD. They reward tenure, so they describe the code, not the people.
- **knowledge loss** is the share of people's surviving lines held by people with no activity in 180 days: no landing, no review, no pull request opened or pushed to, and no commit on any branch or tag of the clone. 59.6% says most of this code was written by people who have gone quiet here (not necessarily left). The **areas** say where: `pkg/` holds most of it. Activity is the whole run's, so a repository scanned alone can read higher than in its organization's scan: someone active in a sibling repository looks gone here. Each person in **made of** and **still here** shows when they were last active, and "quiet in N days" when they are within 30 days of the line (`quiet in 3d` in a table, which never cuts it); **going quiet within 30 days** names them, the soonest first, and a `going-quiet` caveat says what knowledge loss becomes once they cross it.
- **review coverage** needs GitHub data (step 8). Without it the line is absent, not 0. It is the share of human changes whose pull request someone other than its author reviewed before it merged. A change is a pull request (a rebase merge's landings together), a pull request merged into another branch, such as `stable`, which a merge brought, or a landing with no pull request. "With no pull request in the GitHub data" is what the data says, not proof of a direct push: here those 9 are security fixes merged from private forks. A landing after the last sync, or whose subject names a pull request (`Merge pull request #N`, `… (#N)`) the data lacks, is left out rather than counted as unreviewed, and the line says how many. Here 11 are, because this replica has a hole (an interrupted first sync): syncing again fills it. One naming a pull request the data holds, merged into another branch, is left out too, and no sync changes it. A merge that only brought another branch's pull requests, each counted on its own, is no change. `stillhere explain review_coverage --repo cli` lists every change with whether someone else reviewed it, the unreviewed first.
- **landings 365d** counts the landings that hold a commit of the person's, as the week table credits them: who wrote what landed, not who merged it, and a cherry-pick a maintenance branch brings back counts once. `stillhere explain landed --person "Person E" --days 365` lists them; the merges a person made are `merged_365d` in `--format json`.
- **concentration** is how few people hold the majority of half the files that have a majority owner. It is not a truck factor.
- **prod** is the role of product code. Example code, benchmarks, documentation tooling and build, packaging and CI tooling have other roles (doc, test, iac), so no prod number counts them: not the headline, made of, still here's prod lines, ownership, knowledge loss, concentration, `durable@1` or `simplifying@1`. Their lines still count in the still-here table's **other roles** column and in the lines added of what landed. The card's `skipped paths` line, below the concentration line (cut from the sample above), counts the paths classification left out entirely, which count nowhere. `stillhere paths --role prod` lists what counts as product code here and `stillhere paths --skipped` what was left out, each path with the rule that decided it, so a layout the packaged policy reads wrongly shows up at once, and a `stillhere.toml` path rule fixes it.
- **AI-assisted** counts landings with an agent's commit or co-author trailer: a lower bound, since editor completions leave no trace.
- Each table prints its first 10 rows, and a narrow terminal cuts its other roles before a name; `--all` prints every row, and `--format json` or `--format md` the same report for tools or for pasting.
- Read **What could make this wrong** first: here it says the clone has 5 newer landings than `--now`, that the repository has no `.git-blame-ignore-revs`, and that 42 names belong to more than one email.

## 4. Merge one person's addresses

Unmerged addresses split one person into two, which makes knowledge loss and ownership look more spread, and more gone, than they are.

```sh
stillhere identities                                 # candidates, with the evidence for each
stillhere identities --format mailmap >> ~/cli.mailmap
```

```text
identities · every repository · run 7 · as of 2026-10-15 UTC
  565 addresses of people in 1 repository, 7 bots and agents left out, … · 59 candidates, and 34
  hand-off guesses (--all lists them)

1. (name withheld) · high confidence · same-name, same-forge-account
   1. GitHub noreply · (name withheld) · 14 commits in this run · 2022-04-27 → 2023-10-23 · cli
   2. other domain · (name withheld) · 2 commits in this run · 2022-05-06 → 2022-05-09 · cli ·
      canonical
   both addresses carry one display name, withheld because it gives an address away
   GitHub attributes the commits of both addresses to one account, whose login is withheld as the
     local part of an address: GitHub's own link, the attribution its contributor pages show, which
     no display name makes
…
```

Read the file: the high-confidence lines are written, every other candidate is commented out, and stillhere applies none of them. Hand-off guesses (one address stopping as another starts in the same files) are listed last and counted apart in the headline; on an open-source repository most of them link unrelated people, so the text form lists them only with `--all`. A candidate GitHub links to one account is high even when an address carries two names, and says so: a name change looks exactly like a mailbox two people share, so check those before you apply them. Keep it outside the repository, which may have a `.mailmap` of its own that git applies too, and append to it (`>>`) rather than write over it: once a scan applies a line, the suggestions leave it out, so a file written anew would lose it. Scan again with it:

```sh
stillhere --mailmap ~/cli.mailmap scan .             # here: 468 people become 427
```

## 5. Who knows a path

```sh
stillhere who-knows pkg/cmd/pr
```

```text
Repository cli · 9,983 surviving prod lines in 40 prod files, 21 with a majority owner · not
  counted: 5 doc lines in 1 file, 3,116 iac lines in 37 files, 18,478 test lines in 30 files
  knowledge loss   74.4% (7,422 of 9,979 human-held lines) · 119 of 126 people gone quiet
  concentration    4 authors (0 bots) hold the majority in half of the 21 files with a majority
                   owner (not a truck factor)
  majority owners  Person F (4 files, no activity in 365 days) · Person G (4 files, no
                   activity in 365 days) · Person F (2 files, no activity in 365 days) ·
                   Person A (2 files, active 2026-10-14) · … · 1 more person (--all lists them)
  top authors      Person F 1,557 (no activity in 365 days) · Person G 1,374 (no activity in
                   365 days) · Person F 1,132 (no activity in 365 days) · Person E 1,031
                   (active 2026-10-14) · Person A 979 (active 2026-10-14)
  recent landers   Person E (8 landings) · (name withheld 3) (5 landings) · Person A (3
                   landings) · …
  recent reviewers Person H (24 pull requests) · Person E (3 pull requests) · Person M (3 pull
                   requests) · …
```

It is a routing answer, not a rank: who holds the code (owners, authors) against who works on it now (landers, reviewers). Here three quarters of `pkg/cmd/pr` is held by people gone quiet, and the people landing and reviewing it are others: the ones to ask, and the ones to pair with the owners while they can. Person F appearing twice is one person on two addresses (step 4), and `(name withheld 3)` someone whose display name would give an address away. `PATH` is relative to the repository's root, down to one file; `--repo` takes the repository's name as a card heads it, `OWNER/NAME` or its remote URL. Each owner and author also shows when they were last active (`active 2026-10-14`, `quiet in 12 days`, `no activity in 365 days`), so you can tell a holder you can still ask from one who has gone: the first three of `pkg/cmd/pr`'s top authors have not been active here in a year. The first line says what the answer leaves out, here the package's test and fixture lines. A path with no prod line, such as `tests` or `docs`, is answered for its own test or doc lines, and says so; a path with no role at all, such as `go.mod`, says why and what would make it count instead of "nobody".

## 6. Ranks, and why so few people have one

```sh
stillhere report --rank durable --rank simplifying
```

```text
Ranks · durable@1 · repository cli · 2025-10-15 → 2026-07-17 UTC
  Did the code a person wrote last?
  Validity: Beta: not yet checked against a lead's labels (stillhere label, then stillhere validate:
  AUC >= 0.7 on at least 50 scored labels; docs/validation.md). Reference (model panel, 4 public
  repositories, 2026-10-15): does not separate meaningful from routine changes: AUC 0.42 (0.30–0.55)
  on 76, while change size alone gives 0.82. The model panel is three AI models that labelled public
  changes without seeing any metric value: evidence, not a lead's judgment.
  #    person      durable  95% interval   n
  1–3  Person P        77%        70–82%  27
  1–3  Person Q        72%        67–88%  20
  1–3  Person R        70%        54–83%  34
       bots
  —    Copilot App     85%                 2
  53 people listed, not ranked: fewer than 10 changes each (--all lists them)
  intervals are 95% · people whose rank intervals overlap are not ordered · bots are never ranked
```

How to read it:

- A rank is an interval, from a bootstrap over each person's changes. `1–3` three times means the three cannot be told apart: no one here is first.
- Only people with at least 10 changes in the metric's window are ranked; everyone else is listed and counted, not ranked. On most repositories that is a handful of people, which is the honest answer: fewer changes than that give intervals too wide to mean anything.
- `durable@1` is the share of a person's added lines, in changes 90 to 365 days old, that blame still attributes to them. `simplifying@1` is lines removed that stayed removed, in the last 90 days.
- Every ranked metric is **beta** until it is checked against a lead's labels (`stillhere validate`), and the `Validity` line under each table's question says so. It also gives a reference result from a model panel, three AI models that labelled public changes without seeing any metric value, which is evidence, not a lead's judgment: a verdict, such as "does not separate meaningful from routine changes", with the numbers it follows from ([validation.md](validation.md#reference-results)). Read a rank with its metric's verdict.

## 7. Trace any number to its changes

```sh
stillhere explain durable --person "Person P"
```

A name two addresses share is refused, with each one's id to pass instead:

```text
stillhere explain: 2 people are named "Person P" in run 7: "Person P" (id p-0a1b2c3d4e, 1 surviving
prod lines, in "cli"); "Person P" (id p-5f6a7b8c9d, … surviving prod lines, in "cli"). Pass
--person one of the ids. If they are one person, a .mailmap line merges them ('stillhere identities'
suggests it).
```

```sh
stillhere explain durable --person p-5f6a7b8c9d
```

```text
explain durable@1 · Person P · run 7 · as of 2026-10-15 UTC · policy <hash>
  scope             window                                        value   n
  every repository  2025-10-15 → 2026-07-17 UTC  76.6% (1,570 of 2,049)  27
  …
  The 27 changes behind it, by repository and time
    landing  repo  landed (UTC)      num  den  title
    <sha>    cli   <date>              5    8  <a commit subject>
    <sha>    cli   <date>            179  269  <a commit subject>
    …
    17 more changes (--all lists them)
```

`--person` takes a display name (or the start of each word), a GitHub login (`@name`), an id, or an email. `explain landed`, `explain surviving` and `explain majority_owner` list the landings, the commits and the files behind those numbers, and `explain landed --days 365` the year behind a still-here row's "landings 365d". A number about a repository, a group or a top-level directory is traced the same way:

```sh
stillhere explain review_coverage --repo cli      # every change of the 90 days: reviewed, not, or left out and why
stillhere explain landed --repo cli --days 365    # the year's landings, with the copies each brought
stillhere explain knowledge_loss --area cli:pkg   # a top-level directory's knowledge loss and its detail
stillhere explain review_coverage --all-repos     # every repository's changes, for a run in no group
```

`--repo` takes a repository as the report names it (or its key, `GROUP/NAME`, `OWNER/NAME` or its remote URL, as who-knows and paths do), `--group` a group's name, `--area` a repository and one of its top-level directories, and `--all-repos` the whole run's own numbers. `explain` lists no lines behind knowledge loss yet ([#129](https://github.com/IHaveASegway/stillhere/issues/129)): `stillhere who-knows pkg` lists who holds the most of them and who landed or reviewed there in the last 90 days. `--days` reaches back as far as the history the run read, 365 days for a scan, and no further: what landed before it is unknown, not none. `stillhere me --email you@example.com` is your own page: every value and rank the run holds about you.

## 8. Reviews (optional, needs GitHub)

Review coverage, recent reviewers, `reviews_given@1` and the review half of knowledge loss read the pull requests and reviews `sync github` copies into facts.db, with your `gh auth login` token:

```sh
stillhere sync github --org your-org      # mirrors and pull requests; a first sync of a large org takes a while
stillhere --mailmap ~/your.mailmap scan ~/.stillhere/mirrors/your-org
stillhere identities --format mailmap >> ~/your.mailmap   # again: GitHub's links make most of the sure merges
stillhere --mailmap ~/your.mailmap scan ~/.stillhere/mirrors/your-org
stillhere report --rank reviews_given
```

Run step 4 again after the first sync: GitHub's own links from commits to accounts arrive with it, and they make most of the high-confidence merges (on honojs/hono, 4 lines before the sync and 18 after). Keep passing the same `--mailmap`, or the scan loses step 4's merges.

Scanning your own clone instead of the mirrors works the same: its origin names the repository. If GitHub renamed the repository or moved it to another owner since you cloned it, sync it by the name your clone's origin holds (`stillhere sync github --repo old-owner/name`): GitHub answers with the repository under its new name, the sync records the move, and your clone's card says its GitHub data was joined by the old name, with the `git remote set-url` that names it as GitHub does, in your origin's own form (SSH stays SSH). A card with a `forge-unmatched` caveat is one whose origin no synced repository matches, while a repository in facts.db shares its history: run the `sync github --repo` it names, your origin's name, which records the move, or syncs the fork if your clone is one. A repository of the same name that shares no history is not yours and gets no caveat.

The `reviews_given@1` table shows each reviewer's share of approvals with no comment beside the count, because rubber stamps count as reviews.

## 9. Ask from your agent

```sh
claude mcp add --scope user stillhere -- "$(command -v stillhere)" mcp --me you@example.com
```

The MCP server reads the latest scan read-only and withholds emails and commit subjects unless you allow them ([Use it from an agent](../README.md#use-it-from-an-agent-mcp)). Ask it the same questions: "who should review changes to pkg/cmd/pr?", "where has knowledge gone quiet?", "what landed this week, and who merged it?".

## Where the numbers can be wrong

- A stale clone gives a stale report: stillhere never fetches.
- Unmerged identities split people (step 4).
- A reformat without a `.git-blame-ignore-revs` takes the credit for every line it touched.
- Shallow and partial clones have no blame, so no surviving lines: their numbers are absent, never 0.
- No GitHub data, no review numbers: absent, never 0.

Each page lists what could make its own numbers wrong, where the numbers are.
