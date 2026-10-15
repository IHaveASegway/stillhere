# stillhere: product brief

Status: draft v2 · 2026-10-14 · supersedes the 2026-10-07 plan
Decisions: [adr/](adr/) · Metrics: [metrics.md](metrics.md) · System: [architecture.md](architecture.md) · Implementation contracts: [spec/](spec/)

## In one line

Change analytics for git and GitHub (what landed, what lasted, who knows it) for tech leads and their agents, with every number traceable to the commits behind it.

## Why

A tech lead responsible for dozens of repositories cannot cheaply answer:

- What landed this week, across all of them?
- Which recent changes lasted, and which simplified the code?
- Who knows this code, and where has that knowledge left?
- Who is doing the reviewing, and how is AI-assisted code holding up?

Commit counts answer these badly. In a proof run over one 80-repo organization (2026-10-06), a dependency bot had 7,137 commits and 137 surviving production lines, and two AI bot accounts held 7.66% of 2.73 million surviving production lines. Activity is easy to count and is mostly the wrong thing to count.

Without this tool the options are a one-off script, a paid seat (CodeScene, LinearB, Swarmia), or a stack to operate (Apache DevLake on MySQL and Grafana; GrimoireLab on OpenSearch with 8 GB of RAM).

## What exists, and what is new

| Tool | What it answers | Why it is not enough here |
|---|---|---|
| gitinspector (revived Sept 2026) | Surviving lines per author from blame, several repos, HTML/JSON | Per-author totals only: no per-change facts, reviews, or knowledge risk |
| git-fame | Surviving lines per author | No per-file fractions; recursive multi-repo still open (issue 113) |
| git-who (Go, Homebrew) | Who touched which part of the tree | Commits and changed lines, not survival; no reviews |
| git-of-theseus | Survival curves by cohort year | One repo per run; dormant since 2023 |
| git-truck | Local visual of a repo's history | History-based, one repo at a time |
| CodeScene | Hotspots, knowledge loss, off-boarding risk | Paid (free for open source); hosted analysis |
| Apache DevLake | Pull request, review and DORA metrics | A platform to operate; no blame-based survival |

What stillhere adds is **facts about each change**: did it last, did it simplify, who reviewed it, what kind of change was it. Those facts are attributed to the people who made the changes, rolled up across an organization, ranked one metric at a time with an uncertainty range, and explainable down to commits and pull requests. Agents get the same facts over MCP.

## Who it serves

**Primary: tech leads and maintainers.** The first screen answers their questions.

| Who | Question | Where |
|---|---|---|
| Tech lead | Who knows `payments/`? Who should review this? | Web, CLI, MCP |
| Tech lead | Where is knowledge concentrated, or held by people who have gone quiet? | Web, CLI |
| Tech lead | What landed this week? Whose recent work deserves a shout-out? | Web (ranks), CLI `--rank` |
| Tech lead | How are AI-assisted changes holding up against the rest? | Web, CLI |
| Any engineer | What did my changes do? | `stillhere me`, own page on the web |
| Any org member | What were this month's highlights? | Web |
| An agent | Who should I ask? How risky is this file? Is this number trustworthy? | MCP |

## Purpose and limits

stillhere is built for **recognition and curiosity**. It ranks people on one property of their changes at a time, shows an uncertainty range on every rank, and lets every person see exactly what their lead sees about them.

It cannot see design work, mentoring, incident response, planning, or anything that happens outside git and GitHub. The README and the first screen of the web UI say:

> stillhere measures specific properties of code changes: how long they lasted, how much they simplified, how much review they got. It does not measure a person's value, and it must not be the basis for performance or pay decisions.

Using it for performance reviews or pay would need more than software. [ADR 0001](adr/0001-measure-changes-rank-per-metric.md) lists what.

## What "meaningful" means in v1

Noise is stripped before anything is counted: generated and vendored files, lockfiles, whitespace-only edits, moved lines, renames, and formatting commits listed in `.git-blame-ignore-revs`.

Four lenses, never blended into one score:

| Lens | Question | Ships |
|---|---|---|
| Durable | Are this change's lines still here months later? Was it reverted? | M1 |
| Simplifying | Did it remove code, substantively, without being reverted? | M1 |
| Load-bearing | Did it touch code that matters (hotspots, concentrated knowledge)? | M5 |
| Typed | Feature, fix, refactor, test or chore (from pull request labels and commit conventions)? | M5 |

Alongside the lenses: ownership (whose lines are still here), knowledge concentration and loss, reviews given (M2), and an AI-assisted tag on changes. Definitions, blind spots, and how each metric can be gamed are in [metrics.md](metrics.md).

Ranked metrics: durable, simplifying, reviews given, load-bearing. Activity (changes landed) and surviving lines are shown as context and never ranked. Ranking by volume rewards volume (the dependency-bot lesson). Ranking by surviving lines rewards tenure.

## How people use it

```
brew install --cask ihaveasegway/tap/stillhere             # from the first release; until then, see the README

stillhere week ~/src/acme                                  # what landed, offline
stillhere sync github --org acme                           # the only command that uses the network
stillhere scan ~/.stillhere/mirrors/acme --rank durable@1  # blame, durability, ownership facts; ranks (leads)
stillhere me --email you@example.com                       # your own page
stillhere explain durable@1 --person ada@example.com       # the commits behind a number
stillhere mcp --me you@example.com                         # the same facts for your agent
stillhere serve                                            # M3, not built yet: team web UI, Sign in with GitHub
stillhere narrate                                          # M4, not built yet: highlights narrated by Anthropic, pseudonymized
```

What a lead sees on the web (M3):

```
pay · last 90 days · rank by [Durable ▾]               metric durable@1
#     Person   Durable    n    Removed   Reviews
1–3   Ada      81% ±13    38   1,240     22
1–3   Cara     77% ±24    12      80      9
1–3   Bob      69% ±13    51     310     41
—     Dan      4 changes: below 10, not ranked
± is a 95% interval · ranks overlap when intervals do · bots listed separately
```

Everyone else sees their own page, the highlights ("biggest simplification this month", "most durable feature", "most helpful reviewer"), and code-area views (who to ask, knowledge concentration, AI-assisted share).

## Principles

1. **Measure changes; rank one metric at a time.** No composite score. Every rank shows its interval, and people below the minimum sample are not ranked.
2. **Every number is explainable.** `explain` and the web UI go from any number to the commits and pull requests that produced it.
3. **Facts, then metrics, then views.** Collectors write facts. Metrics are versioned (`durable@1`) and recomputable over stored facts. Every report cites the versions it used. A definition change is a new version, never a silent edit.
4. **Offline, deterministic analysis.** Only `sync` touches the network. The same inputs and `--now` produce the same report, all but its wall-clock `timings`.
5. **Sources of truth stay where they are.** git holds code and history; GitHub holds pull requests, reviews, teams and verified emails; `.mailmap` holds email aliases. stillhere stores derived copies only and never writes to any of them.
6. **Noise first.** Nothing is counted before classification.
7. **Bots are separated; AI-assisted work is tagged.** Humans are credited for their changes. AI involvement is a tag and a cohort comparison, never a ranked row.
8. **AI never computes, changes or orders a measurement.** It narrates highlights and answers questions over facts. Ranks stay rule-based and reproducible.
9. **Every metric ships with a fixture oracle and a validity note**: what it misses and how it can be gamed.

## Milestones

Each milestone ships and gets used before the next starts. Numbers come before UI.

| | Scope | Done when |
|---|---|---|
| **M0** Walking skeleton | Go module; discovery; classification; `stillhere week`; CI on git 2.45 (the version floor) and latest; GoReleaser publishing to a Homebrew tap and a Docker image | `brew install` works; the fixture's week oracle passes |
| **M1** What lasted | Blame collector; ownership, knowledge concentration and loss; `durable@1`, `simplifying@1`; revert detection; AI-assisted tag; facts store (SQLite) and metric registry; `explain`; snapshots with cheap backfill | Fixture oracles for every metric; a real 80-repo scan inside the budget |
| **M2** Reviews and ranks | GitHub App; `sync github` (pull requests, reviews, comments, labels, managed mirrors); `reviews_given@1`; per-metric ranks with intervals (`--rank`); `stillhere me`; validity check against hand labels (`stillhere label`, `stillhere validate`) | Contract tests on recorded API responses; rank intervals verified on the fixture; the labelled-sample check run once |
| **M3** Team web | `stillhere serve` with Sign in with GitHub; leads team sees ranks; own pages; highlights; code-area views | Auth tests (state, sessions, escaping); one real deployment |
| **M4** Agents | `stillhere mcp`; narrated highlights (Anthropic, pseudonymized) | MCP tools tested with the official SDK client; redaction and numbers-check tests |
| **M5** Load-bearing and typed | Hotspots and coupling; `load_bearing@1`; `change_type@1` | Hotspot engine decided (open decision 1); oracles pass |

## Open decisions

Recommendations that were not approved stay open. The baseline is what happens if nobody decides. Decisions already written up as ADRs await the owner too, with status Proposed in the [ADR index](adr/README.md): today 0009, 0010, 0011, 0012 and 0014, and 0007's amendment on the AI-assisted shares the MCP server sends.

| # | Decision | Baseline (2026-10-07 plan) | Recommendation | Decide by | Evidence that settles it |
|---|---|---|---|---|---|
| 1 | Hotspot engine | code-maat 1.0.4 jar plus a Java runtime | Compute in Go; run code-maat once as a test oracle | Before M5 | Whether a Java dependency keeps Homebrew users from hotspots; code-maat has had no release since Feb 2023 |
| 2 | Function-point weighting ("gearing") | Secondary column `geared_fp` | Cut it | Before the M1 schema | Whether it changes any ranking; in the proof run it did not change the top 10, and 21 of 28 language rows are estimates |
| 3 | Blame cache | Built alongside blame (M1) | Defer until measured | During M1 | Cold scan of a real org against the 15-minute budget; the proof run took 93 s for 80 repos |
| 4 | Server refresh | Unspecified | The server only reads; a refresh builds a new run that readers see only once complete; refresh is scheduled inside the same container | Before M3 | Whether refresh should run in-process, as a separate job, or on demand from the UI |
| 5 | Minimum sample and cohort window | None | At least 10 landings; durability cohort is changes 90–365 days old | M2, on real data | Interval widths on a real org |
| 6 | Moved lines in "substantive" counts | Rename detection only | `durable@2` using blame at the landing commit | After M1 data | How often moved code inflates added lines |
| 7 | AI-assisted shares in the local report | As built (the plan had no rule): the report's overviews and cards show every share, a repository's, a group's and the whole run's, however few people landed in it; the 3-person floor and the secondary rule apply only to what the MCP server sends ([ADR 0007's amendment](adr/0007-ai-assisted-attribution.md#amendment-the-shares-that-leave-the-machine)) | Keep the terminal as it is; apply the MCP server's one decision per run (`app.AIShares`) to every page the web server serves | Before M3 | Whether a report's readers can already read the clones it is built from: a terminal user can (every trailer is in `git log`), a lead on the web server may not, and a pasted report travels without them |

## Non-goals (v1)

- A composite score of a person, or use for performance or pay decisions.
- AI-produced scores or AI-judged changes. Judging needs a hand-labelled evaluation set first. The model panel of [validation.md](validation.md#reference-validation-a-model-panel) labels public repositories' changes for a published reference number; none of its judgments reaches stillhere's outputs ([ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md)).
- Forges other than GitHub. The `ForgeSource` port leaves room for GitLab later.
- Code-health or complexity scoring, real-time updates, IDE plugins, telemetry.
- Sending source code anywhere by default.

## How we will know it works

- **It measures what we mean.** A lead labels at least 50 recent changes as meaningful, routine or noise (`stillhere label`, a blinded, stratified sample of the changes the ranked metrics score). Each ranked metric must separate meaningful from routine with an AUC of at least 0.7 (0.5 is chance) on at least 50 of its own scored labels (`stillhere validate`, which prints each AUC with its 95% interval). Only a lead's labels lift a metric's beta mark. Until a lead has labelled, a reference result on public repositories, labelled by a panel of model raters with the same rubric, is published beside the mark as "not yet a lead's labels": evidence, not the gate ([ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md), [validation.md](validation.md)). If a metric fails a lead's check, the definition changes, as a new metric version, before more UI is built on it. Canonical reading: Kaner and Bond, "Software Engineering Metrics: What Do They Measure and How Do We Know?" (METRICS 2004).
- **It gets used.** Leads open who-knows and highlights weekly. If ranked views go unused after M3, drop them and keep the highlights.
- **It is reliable.** Fixture oracles pass on git 2.45 and on the latest git. Two runs with the same inputs are byte-identical once their wall-clock `timings` are removed. A cold scan of 100 repos and 50,000 files finishes in under 15 minutes on 8 cores.
- **It spreads.** Homebrew installs, and 75 GitHub stars (the homebrew-core bar).

## Glossary

- **Landing**: a first-parent commit on the default branch. A merge landing owns itself and the side-branch commits it brought in.
- **Change**: a pull request when GitHub knows one, otherwise a landing.
- **Durable**: the share of a change's non-blank added lines that blame still attributes to it today.
- **Majority owner**: an author of at least half of a file's non-blank lines.
- **Knowledge loss**: surviving lines whose authors have not landed or reviewed anything in the organization for 180 days.
- **Hotspot**: a file that both changes often and is large.
