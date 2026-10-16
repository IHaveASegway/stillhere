# ADR 0013: A sync reads bounded passes and checks the replica against GitHub's count

Status: Proposed · 2026-10-15 · Deciders: Joseph Peterson · Amends what [ADR 0004](0004-github-behind-sync-boundary.md)'s "sync watermark" means

## Context

GitHub is the system of record for pull requests and reviews ([ADR 0004](0004-github-behind-sync-boundary.md)). facts.db holds a replica of them, `sync github` is its only writer, and each scan keeps a copy of the part it read ([ADR 0011](0011-a-run-keeps-the-github-facts-it-read.md)). Every number from GitHub data reads that copy within the 365-day history window: `reviews_given@1` over 90 days, `knowledge_loss@1`'s review activity over 180, `who_knows@1`'s recent reviewers over 90. Its readers need the replica complete within the window and an honest "as of"; eventual consistency is fine, a silent hole is not.

Until now the replica kept its place in GitHub's list as the newest `updatedAt` it had read. A first pass read the list oldest update first and took each page's newest value as the watermark; a later pass read newest first and stopped at the first pull request older than the watermark less five minutes. Both relied on GitHub's order agreeing with the `updatedAt` values it returns, and on cli/cli it does not (#101):

- **A hole that later syncs never filled.** One pull request updated in July 2026 was listed among those updated in March. The first pass took July as its watermark when it had read up to March, then failed (#99, below); every later pass stopped at July and reported success. 445 of the 1,281 pull requests updated in the window were missing, and `reviews_given@1` read two of the top five reviewers 23% and 32% low, with no caveat. The checkpoint was a value derived from records in an order someone else owns: a second owner of "where we are in the stream", which drifted from the first.
- **A deleted account failed the whole repository** (#99). GitHub answers the account of a commit whose author's account was deleted with `null` and a `NOT_FOUND` error beside the data, which the GraphQL specification allows; any error was fatal, so cli/cli's first sync failed every time after about 3,850 pull requests.
- **First syncs read history no metric reads** (#100). The first pass read every pull request ever, one request at a time: go-chi's 970 to get the 255 updated in the window, cli/cli's 4,730 for 1,281, and about 37,000 in octocat/Spoon-Knife for 3,711.

## Decision

- **Every pass is bounded by time, newest first.** A full pass reads every pull request updated from the sync's start less 365 days (or from `--since`, which only reaches further back) to now: a repository's first sync, the one after a check found pull requests missing, and one whose `--since` reaches further back than the replica. Any other pass reads down to the watermark less five minutes.
- **The watermark is a position GitHub owns.** It is the time the pass began, on GitHub's clock (the `Date` of its responses), carried only by the page that completes the pass and stored with it in one transaction; never a value read from a pull request. A pass that stops halfway, interrupted or failed, leaves the old watermark, and the next sync reads the same range again: upserts are idempotent, so a rerun costs time bounded by the window, never a hole. This is how a Kafka consumer's offset or a Postgres replication slot's LSN keeps a reader's place (Martin Kleppmann, *Designing Data-Intensive Applications*, ch. 11, "Logs and offsets"). Incremental connectors such as Airbyte's GitHub source keep instead the newest cursor-field value they emitted, per stream and repository: the design stillhere had, which holds only while the source's order agrees with the field.
- **The stop is tolerant.** A pull request older than the stop point is skipped, not taken for the end of the range; a pass ends at the end of the list or at the first page that holds none at or after the stop point. One pull request out of order near the top no longer ends a pass early.
- **A completeness check reconciles the replica with GitHub after every pass.** GitHub's search counts the repository's pull requests updated from the window's start to the pass's start less five minutes (`search(query: "repo:O/R is:pr updated:FROM..TO", type: ISSUE) { issueCount }`, 1 point), whose last update was settled before the pass began; the replica counts its own updated since the window's start, at their newer update too. A complete replica holds at least as many. A shortfall after a pass down to the watermark means a hole below it, as an earlier stillhere left: a full pass follows in the same sync, and the check again. A shortfall that remains is recorded with the watermark, every page of a run that read it carries a `forge-incomplete` caveat, and the next sync reads the whole window again. This is anti-entropy (Kleppmann, ch. 5, "Read repair and anti-entropy"), reduced to a count because GitHub offers a cheap count and no cheaper digest.
- **The store says how complete it is.** Migration 6 adds to each watermark, and to each run's copy of one, `since` (the oldest update the replica is complete from), `missing` (the last check's shortfall) and `pending` (pages of an unfinished pass are stored: with no `synced`, the first sync stopped halfway). A row stored before it reads as complete from the repository's first pull request, as it claimed, and the next sync's check finds a hole it hides.
- **A tolerant reader takes GitHub's nulls** (Martin Fowler, "TolerantReader", 2011): a response whose only errors are `NOT_FOUND` on a field that the data holds as `null` is read as that null, and counted in the log. Any other error stays fatal.
- **Three repositories sync at once,** one API request each in flight, and a rate limit any of them meets holds them all until it is over.

The same change makes `sync github --repo OWNER/NAME` sync named repositories without their organization (#111), moves an archived repository's mirror to `<mirrors>/.stale` unless `--include-archived` (#110, as git-invocation.md already did for deleted ones), writes a commit-graph with changed-path filters into each mirror (#56), and ends every sync with a summary line.

## Alternatives considered

- **Keep GitHub's cursor as the first pass's checkpoint, and set the time watermark when the pass completes** (#101's first option). It keeps the order that disagreed, the oldest-first full-history first pass that #100 measured at minutes to hours, and a stored cursor whose lifetime GitHub does not document. Rejected.
- **Bounded passes, a watermark only on completion, and a count check (chosen).** Each first sync is bounded by the window, and the check catches what an ordering anomaly would hide.
- **Webhooks**, as push-based connectors such as Fivetran's offer for GitHub: no polling and no order to trust, but they need a server GitHub can reach, which is M3's ([ADR 0005](0005-web-server-github-app-login.md)), and a replay after downtime still needs a pass. Revisit with M3.
- **Compare the sets, not the counts:** list every pull request number in the window on both sides each sync. Exact, but it costs a page per 100 pull requests on every sync, where the count costs 1 point, and search returns at most 1,000 results.
- **`pullRequests { totalCount }`**, which counts every pull request the list returns. It has no time filter, and a bounded replica does not hold every pull request ever, so the two cannot be compared.

## Consequences

Measured on 2026-10-15 against github.com, one machine, the same token, fresh `STILLHERE_HOME` each:

| | Before (5eb40a1) | After |
| --- | --- | --- |
| go-chi, first sync (20 repositories) | 69 s, 970 pull requests | 28 s, 255; every check equal |
| go-chi, next sync | | 18 s, 0 to read |
| cli, first sync (6 repositories) | 334 s; cli/cli failed on #99 after 3,850, watermark four months ahead | 118 s, all synced; cli/cli 1,281, GitHub's count 1,280 |
| cli/cli, first sync interrupted by SIGINT at 60 s | | 800 stored, no watermark, `pending`; the rerun took 93 s, 1,281 stored, nothing missing |
| The dogfood's damaged cli replica (835 of the window's 1,281) | | one sync: the check found 444 missing, the full pass that followed filled them, 98 s |
| `reviews_given@1` on cli/cli, top five people, from that replica | 66/46/36/24/13, 208 reviews counted | 98/55/43/25/21, 265: the same as a scan of a replica that was never damaged |
| A 52,000-commit synthetic repository, scan without and with a commit-graph with changed-path filters (#56) | 223 s, and the hint | 45 s; the report the same byte for byte at the same `--now`, timings left out |

- **The count is a lower bound.** GitHub's search does not count a pull request whose author's account is gone: on cli/cli the replica's one extra is #11952, which search does not index at all. A hole no larger than such pull requests plus those updated during the pass passes the check. The #101 hole was 444.
- **A stale copy is invisible to a count.** A pull request whose place in GitHub's order disagrees with its `updatedAt` by more than a page, and which changes again, may keep its older copy until the next full pass; the check sees the pull request and not its version.
- **Cost.** The tolerant stop reads one page more than the old stop rule when a pass found anything new (2 points); a quiet repository's pass reads one page. The check costs 1 point per repository per sync, a repair pass the window again.
- **`--now` far in the past.** A replica is complete from `since`, the window's start at its last full pass. A scan with `--now` more than about 185 days before that sync reads review windows that start before it, and no caveat says so yet; `provenance.forge.repos[].from` shows it.
- **Exit codes are unchanged.** A partial sync still exits 0, as documented; its closing line says how many repositories synced, failed, or are incomplete.

## Door

Two-way. The watermark's meaning is internal to the replica, which `ForgeStore` hides; migration 6 only adds columns; and facts.db is disposable, since deleting it loses nothing GitHub does not hold. The commit-graphs are git's own files: deleting them changes only speed.

## Revisit if

- The check finds pull requests missing again and again on repositories nobody changed during the pass: search and the list disagree systematically, and the check needs another oracle.
- Full passes dominate sync time on a large organization whose repositories keep coming up short.
- GitHub's pull request list gains an "updated since" filter: read it instead of ordering by `updatedAt`.
- A stale copy turns up that the count cannot see: add a full pass on a schedule (weekly, say), anti-entropy by time rather than by count.
- git 2.46 becomes the floor: every mirror then gets changed-path filters.
