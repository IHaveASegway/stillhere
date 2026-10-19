# Recorded GitHub responses (`TestGitHubSync`)

Status: written by hand for #14 · 2026-10-14, re-cut newest first with the sync scenario for #101 (ADR 0013) · 2026-10-15 · the contract test of `internal/github`, of `app.Sync`'s passes and checks, and the oracle of `reviews_given@1`

These files are the GitHub API, recorded: `TestGitHubSync` replays them in order, makes no live call, and checks the facts the adapter returns against the values below. Every response was written by hand from GitHub's documented GraphQL shapes. A live, read-only query of a small public repository (`cli/go-gh`, 2026-10-14) confirmed the shapes, and `rateLimit(dryRun: true)` gave the costs quoted in the code. A live query of `cli/cli#14660` (2026-10-14) confirmed that a bot's commit carries its account in `author.user` (`{"login": "dependabot[bot]"}`), although `GitActor.user` is typed `User`. Nothing was recorded from a real organization: the people, emails and SHAs are made up, and every email is under `example.com` or `.example`.

The expected values were derived from these files and from [docs/metrics.md](../../docs/metrics.md#reviews_given1-m2) by hand. The code never produced them. Change a response and you change an oracle: re-derive it here first.

## Files

| File | Exchange |
| --- | --- |
| `exchanges.json` | The order of the exchanges: operation, variables, status, headers and body file. The test fails on a request that is not the next one, and on any exchange left unrequested. |
| (inline text) | E1: the first request of the run finds the hourly points spent by other tools. GitHub says so in a `200`: an error of type `RATE_LIMITED`, with `X-RateLimit-Remaining: 0` and `X-RateLimit-Reset` in the headers. |
| `repos-1.json`, `repos-2.json` | E2, E3: acme's repositories, two pages. |
| `pulls-first-1.json` | E4: the first pass over widget, newest update first, page 1. |
| `secondary-limit.json` | E5: page 2 meets a secondary rate limit, `403` with `Retry-After: 60`. |
| `pulls-first-2.json` | E6: page 2 again; 40 points are left until 13:00:00; PR 9 has more labels, reviews and commits than the page held. |
| (inline text) | E7: PR 9's label follow-up meets a secondary rate limit that GitHub reports in a `200`, by its message, without `Retry-After`. |
| `labels-09.json`, `reviews-09.json`, `commits-09.json` | E8 to E10: the rest of PR 9's lists. |
| (inline text) | E11: page 3 gets a `502` from GitHub's edge. |
| `pulls-first-3.json` | E12: page 3 again, at half the page size, the end of the list; GitHub could not resolve the account of PR 7's commit's author, and says so with a `NOT_FOUND` error beside the data, which holds it as `null` (#99). |
| `pulls-later-1.json` | E13: a week later, newest update first; PR 9, older than the stop point, sits before PR 10. |
| `pulls-later-2.json` | E14: page 2, all older than the stop point. |
| (inline text) | E15: GitHub's search count of widget's pull requests settled before the later pass began. |
| (no file) | The hold: two count requests in flight at once, `a` and `b`, through a transport of the test's own (`holdTransport`), not the replay. `a`'s first try gets a `403` with `Retry-After: 600` once `b`'s is in flight; `b`'s a `403` with `Retry-After: 30` once `a` waits; every later try a count. The clock stands still, so `a` waits its 600 s, and `b` its own 30 s and then the 600 s `a`'s hold still has on that clock, where it once sent again after its 30 s: a shorter limit met during a longer hold does not end it. |
| `sync-exchanges.json` | The sync scenario's exchanges, S1 to S24 and L1 to L4 ([below](#the-sync-scenario)). |
| `sync-repo.json`, `sync-pulls-1.json`, `sync-pulls-2.json`, `sync-repos.json` | The sync scenario's responses: widget looked up, its pull requests as they stood a week after the first pass (from `pulls-later-1.json`, PR 9's lists completed with E8 to E10's), and acme listing widget archived. |
| `sync-links.json` | L1 to L4: GitHub's answer about the 12 author emails of the main fixture's history, each through its newest commit (`c0` to `c11`, newest first): `ada@example.com` and `ada@other.example` are both `ada`; `bob`, `cara`, `erin`, `frank` and `hank` are their own; `copilot@bot.example`, `dana`, `ivy` and `jules` have no account; `gina`'s account was deleted since, a `NOT_FOUND` beside a null, read as no account. |

Cursors are opaque strings, so these are readable ones. The nested pages are short (one or two nodes before `hasNextPage`) so that the follow-up queries run on a few nodes; GitHub would fill each page first.

## The organization

| ID | Repository | Flags | Default branch |
| --- | --- | --- | --- |
| `R_empty` | `empty-repo` | empty: no default branch | none |
| `R_gadget` | `gadget` | | `trunk` |
| `R_legacy` | `legacy` | archived | `main` |
| `R_fork` | `upstream-fork` | fork | `main` |
| `R_widget` | `widget` | | `main` |

`Repos` returns all five, sorted by `Key`, the clone URL `<git URL>/acme/<name>.git`; the use case decides which to sync. The test serves widget's repository (a bare clone of the main fixture, plus a `refs/pull/1/head` ref, as GitHub advertises one per pull request) from a local git server that accepts only the token.

## The mirror steps

These use the local git server, not the recorded responses.

1. **Clone**: widget's mirror holds `refs/heads/feature` and `main` (no `refs/pull/*`), `HEAD` on `main`, the `+refs/heads/*` refspec, its node id, `stillhere.forgeId = R_widget`, and `objects/info/commit-graph`, which holds changed-path filters (`collect.ChangedPathFilters`) exactly when git is 2.46 or later (#56).
2. **Fetch**: upstream deletes `feature` and adds `topic`, which the API now calls the default branch. After the fetch the mirror holds `main` and `topic`, with `HEAD` on `topic`.
3. **Rename**: upstream renames widget to gizmo under the same node id. `ReconcileMirrors` moves `acme/widget.git` to `acme/gizmo.git` (`renamed`), and the fetch reads the new URL. The owner directory then holds one mirror, `gizmo.git`, whose origin is `<git URL>/acme/gizmo.git`.
4. **Stale and back**: a listing without `R_widget` moves the mirror to `.stale/acme/gizmo.git` (`stale`), which leaves `acme/` with no mirror. The full listing brings it back (`restored`). A third reconcile has nothing to move.
5. **Redirect**: a git URL that redirects every request to another host (127.0.0.1 to localhost) fails the clone, and the other host gets no request.
6. **Refused token**: a clone with a token the server refuses fails at once, with no prompt and no directory left behind.

## People

| Login | Account | Commit email GitHub reports | In the run's history as |
| --- | --- | --- | --- |
| `ada` | User | `ada@example.com`; `ada@other.example` on one commit | `ada@example.com` (squash commits f01, f09) |
| `bob` | User | `bob@example.com` | `bob@example.com` (f02) |
| `cara` | User | none: cara only reviews | not in the run |
| `erin` | User | `erin@old.example` (once as `Erin@Old.Example`) | `erin@example.com`, mailmapped (f03, f05) |
| `dependabot` | Bot (GraphQL drops the `[bot]`) | `49699333+dependabot[bot]@users.noreply.github.com`, account `dependabot[bot]` | that address (f04) |
| `copilot-pull-request-reviewer` | Bot | none | not in the run |
| `renovate-approver` | User: a machine account that the `[bots]` fragment `renovate` makes a bot | none | not in the run |
| ghost | deleted (`author: null`) | `former@example.com`, no account | `former@example.com` (f07) |

## Pull requests in widget

Times are UTC, 2026. "Merge commit" is the commit the merge put on main (`f0N…`). Review bodies count characters after trimming white space; "inline" is the review's `comments.totalCount`.

| # | Author | State (first pass) | Updated | Merged | Labels | Reviews: author, state, submitted, body chars, inline |
| --- | --- | --- | --- | --- | --- | --- |
| 3 | erin | merged | 07-20 12:00 | 06-01 09:00 | | bob approved 06-01 08:00, 0, 0 · ada commented 07-20 12:00, 34, 0 |
| 7 | ghost | merged | 08-01 09:00 | 08-01 09:00 | docs | erin approved 07-30 17:00, 4, 0 |
| 1 | ada | merged | 08-10 12:00 | 08-10 12:00 | feature | cara commented 08-09 15:00, 38, 2 · bob approved 08-10 10:00, 0, 0 |
| 6 | ada | closed | 09-02 08:00 | | wontfix | bob changes requested 09-01 16:00, 33, 3 |
| 2 | bob | merged | 09-15 16:00 | 09-15 16:00 | bug | ada changes requested 09-14 11:00, 34, 1 · bob commented 09-14 13:00, 15, 1 · ada approved 09-15 14:00, 0 (body `"  \n"`), 0 · renovate-approver approved 09-15 15:00, 0, 0 |
| 4 | dependabot (bot) | merged | 09-20 11:00 | 09-20 11:00 | dependencies | copilot-pull-request-reviewer (bot) commented 09-19 06:05, 87, 1 · bob approved 09-20 10:30, 0, 0 |
| 9 | ada | merged | 09-25 18:00 | 09-25 18:00 | needs-review, then enhancement (E8) | bob commented 09-22 09:00, 16, 3 · erin dismissed 09-23 10:00, 0, 0 · then (E9) bob approved 09-24 15:00, 0, 0 · cara approved 09-25 17:00, 0, 0 |
| 5 | erin | open | 10-01 10:00 | | refactor | ada approved 10-01 10:00, 11, 0 |
| 10 | bob | open, draft | 10-01 11:58 | | | none |
| 8 | bob | open | 10-01 12:00 | | feature | ada approved 10-01 12:00, 0, 0 |

Commits: one per pull request (`0N00…01`), two for #2, and three for #9 (`0900…01` and `0900…02` on the page, `0900…03` from E10, with `0900…02` under `ada@other.example`). A merged pull request's merge commit is `f0N0…`, authored by its author, except #7's, whose author has no account.

### First pass (E4 to E12)

The first pass of a repository reads newest update first, down to a year before the sync (the test passes 2025-10-01T12:00:01Z, before every pull request here), so it reads the whole list. Only the page that completes the pass carries a watermark ([ADR 0013](../../docs/adr/0013-sync-reads-bounded-passes-and-checks-the-replica.md)): the time the pass began on GitHub's clock, E3's `Date` plus the time since, which the test's clock holds still. No watermark is a time read from a pull request (#101).

| Page | Pull requests | Watermark |
| --- | --- | --- |
| 1 | #8, #10, #5 | none |
| 2 | #9, #4, #2 | none |
| 3 | #6, #1, #7, #3 | 2026-10-01T12:00:01Z (E3's `Date`) |

Translation checks this pass makes: states lowercased (`MERGED` is `merged`); a deleted author is a user with no login; a Bot is `Bot: true` with GraphQL's login; emails lowercased (`Erin@Old.Example`); a whitespace-only body is 0 characters; labels sorted (`enhancement` before `needs-review`); reviews sorted by submission; commits kept in GitHub's order; no merge commit on an unmerged pull request. E12 carries an error beside its data, `NOT_FOUND` at `node.pullRequests.nodes.2.commits.nodes.0.commit.author.user`, where the data holds `null`, as GitHub answers for a deleted account (#99; live on cli/cli#13075, 2026-10-15): the page is read, #7's commit has no login, and the pass logs one field it could not resolve.

### The waits

Every wait for a reset is measured on GitHub's clock: the `Date` of the response that announced it, plus the time since. A rate limit one request meets holds every other request until it is over, and the request that met it waits once.

| When | Why | Wait |
| --- | --- | --- |
| E1 | a spent budget reported in a `200`: `X-RateLimit-Reset` is 12:00:00 and the `Date` is 11:50:00 | 10 min, plus a 1 s margin and up to 1 s of jitter |
| E5 | `Retry-After: 60` | 60 s plus up to 1 s of jitter |
| before E7 | E6 left 40 points (below the reserve of 100); the reset is 13:00:00 and E6's `Date` is 12:50:00 | 10 min, plus a 1 s margin and up to 1 s of jitter |
| E7 | a secondary limit reported in a `200`, with no `Retry-After` and points left | between 1 and 2 min (equal jitter from a 2 min ceiling, first attempt: GitHub asks for at least a minute) |
| E11 | a `502`: transient, so the page halves from 50 to 25 (E12's variables) | between 1 s and 2 s (equal jitter, first attempt) |

### Later pass (E13, E14)

A week later (the test moves its clock seven days on), since is the first pass's watermark, 2026-10-01T12:00:01Z. The pass reads newest update first and skips every pull request updated before since less the five-minute overlap, 11:55:01; it ends at the first page that holds none at or after that, or at the end of the list:

| Page | Read | Updated | Kept |
| --- | --- | --- | --- |
| 1 | #8 | 10-08 09:00, now merged 10-08 09:00 (merge commit f08), with cara commented 10-07 16:00, 18 chars, 1 inline | yes |
| 1 | #5 | 10-04 15:00, now merged 10-04 15:00 (merge commit f05), with bob commented 10-03 09:00, 24 chars, 1 inline | yes |
| 1 | #9 | 09-25 18:00, before #10 in GitHub's order | no, and no follow-up for it; the pass goes on |
| 1 | #10 | 10-01 11:58, unchanged | yes: inside the overlap, read again. The old rule, which stopped at the first older pull request, stopped at #9 (#101) |
| 2 | #4, #2 | 09-20, 09-15 | no: a whole page older ends the pass |

Two pages: #8, #5 and #10 with no watermark, then none with the watermark 2026-10-08T13:02:10Z, the pass's start (E12's `Date`, a week on). E15 then counts widget's pull requests on GitHub that were settled before it, updated up to five minutes before it (`updated:<=2026-10-08T12:57:10Z`, from the first, since the test passes no from): 10.

## reviews_given@1

The replica after both passes, stored in a facts.db as sync stores it (`sqlite.Open`, `PutForgeRepos`, `PutForgePage`) and read back as scan reads it (`LoadForge` from a year before `Now`), which returns each pull request as the last page that carried it said; read as of the fixture's `Now`, 2026-10-07T00:00:00Z. The window is [2026-07-09T00:00:00Z, 2026-10-07T00:00:00Z). The policy holds the packaged `[bots]` fragments (`copilot`, `gpt-engineer`, `dependabot`, `renovate`, `[bot]`), and the input has no `identity.Directory`, as `metrics.Compute` may pass it, so the metric builds one.

**`LoadForge`'s since** is checked apart, from #10's update, 2026-10-01T11:58:00Z, which the later pass read again: it returns #5 (10-04 15:00), #8 (10-08 09:00) and #10 itself, at or after since, each with its labels, reviews and commits, and every repository and watermark, whatever since.

**The run** scanned widget through a user's own SSH clone, origin `git@<git URL's host>:acme/widget.git`, in group `acme`, not through the managed mirror. The replica keys widget by its HTTPS clone URL, and the two name the same host, owner and name. The clone's history holds the squash commits f01, f02, f03, f04, f05, f07 and f09 with their mailmapped authors (f03 and f05 as `erin@example.com`); f08 landed after `Now`.

**The run also scanned `acme/sprocket`** (`R_sprocket`), in group `acme` too, through an SSH clone whose collection failed (status `error`). Its one pull request, #1 by `dana`, merged 09-29 12:00, was approved by bob 09-29 10:00. A failed repository adds nothing to any number, its GitHub ones included (`domain.ForgeJoin` joins it to no forge repository), so it counts in no scope, its group's included: bob's value stays 4.

**The replica also holds `other-org/secret`** (`R_secret`), which an earlier sync of another organization left there, with one pull request: #1 by `dana`, merged 09-30 12:00, commented on by cara 09-30 10:00 and approved by bob 09-30 11:00. The run did not scan that repository, so it counts in no scope, `all` included: bob's value stays 4. cara's comment has the larger review ID (`PRR_s1b`), so the round trip checks that reviews come back by submission time, then ID.

**Logins to people.** Each login takes the run's author of the commits GitHub attributes to it: ada (f01, f09) is `ada@example.com`, bob (f02) is `bob@example.com`, and erin (f03, f05) is `erin@example.com`, the mailmap reaching GitHub's `erin@old.example` through the shared SHA. cara and renovate-approver have no commit in the run, so they are `@cara` and `@renovate-approver`. A Bot reviewer is `@<login>[bot]`.

**Pull requests that count**, merged before `Now`: #1, #2, #3, #4, #5 (merged 10-04, learned in the later pass), #7 and #9. Not #6 (closed unmerged), #8 (merged 10-08, after `Now`), #10 (open), sprocket's #1 (its repository failed) or other-org's #1 (not scanned).

**Reviews**, per pull request, in the window, approved, changes requested or commented, by someone other than the author:

| # | Counted | Left out |
| --- | --- | --- |
| 1 | cara commented 08-09 (2 inline); bob approved 08-10 (no comment) | |
| 2 | ada changes requested 09-14 (1 inline); ada approved 09-15 (no comment); renovate-approver approved 09-15 (no comment) | bob commented 09-14: the author |
| 3 | ada commented 07-20, after the merge | bob approved 06-01: before the window |
| 4 | copilot-pull-request-reviewer commented 09-19 (1 inline); bob approved 09-20 (no comment) | |
| 5 | ada approved 10-01 (a body); bob commented 10-03 (1 inline) | |
| 7 | erin approved 07-30 (a body) | |
| 9 | bob commented 09-22 (3 inline); bob approved 09-24 (no comment); cara approved 09-25 (no comment) | erin dismissed 09-23 |
| 8 | | ada approved 10-01 and cara commented 10-07: merged after `Now` (and 10-07 is outside the window) |
| 6 | | bob changes requested 09-01: never merged |
| sprocket #1 | | bob approved 09-29: a repository whose collection failed |
| other-org #1 | | cara commented 09-30 and bob approved 09-30: a repository the run did not scan |

**Values**, the same for scope `all`, for group `acme` (which holds the clone, and sprocket) and for the repository scope of the clone, in which a scan ranks a repository named directly; none for sprocket's. N equals the value:

| Subject | Value | reviews | review_comments | approvals | approvals_no_comment | no_comment_share |
| --- | --- | --- | --- | --- | --- | --- |
| `@cara` | 2 (#1, #9) | 2 | 2 | 1 | 1 | 1 |
| `@copilot-pull-request-reviewer[bot]` | 1 (#4) | 1 | 1 | 0 | 0 | absent: no approvals |
| `@renovate-approver` | 1 (#2) | 1 | 0 | 1 | 1 | 1 |
| `ada@example.com` | 3 (#2, #3, #5) | 4 | 1 | 2 | 1 | 0.5 |
| `bob@example.com` | 4 (#1, #4, #5, #9) | 5 | 4 | 3 | 3 | 1 |
| `erin@example.com` | 1 (#7) | 1 | 0 | 1 | 0 | 0 |

**Clusters** for scope `all`: one per person and pull request, Num 1, in week block `⌈(Now − first counted review) / 7 days⌉ − 1` (`domain.Window.Week`, the rule every Sum metric and `rank.Intervals` share: block 0 is `[Now − 7 days, Now)`); 13 blocks, eligibility 10. No review below is a whole number of weeks before `Now`, so `⌊(Now − t) / 7 days⌋`, which this table used before the rule had one home, gives the same blocks. The two bots have none: `@copilot-pull-request-reviewer[bot]` is an app account, and `@renovate-approver` matches the fragment `renovate`, which `identity.Directory.Bot` decides.

| Subject | Pull request: first counted review → block |
| --- | --- |
| `@cara` | #1: 08-09 15:00, 58.4 days → 8 · #9: 09-25 17:00, 11.3 days → 1 |
| `ada@example.com` | #2: 09-14 11:00, 22.5 days → 3 · #3: 07-20 12:00, 78.5 days → 11 · #5: 10-01 10:00, 5.6 days → 0 |
| `bob@example.com` | #1: 08-10 10:00, 57.6 days → 8 · #4: 09-20 10:30, 16.6 days → 2 · #5: 10-03 09:00, 3.6 days → 0 · #9: 09-22 09:00, 14.6 days → 2 |
| `erin@example.com` | #7: 07-30 17:00, 68.3 days → 9 |

Nobody reaches 10 pull requests, so `rank.Intervals` would list everyone and rank no one; the eligibility boundary is #10's to test.

## The sync scenario

`app.Sync` through the adapter, a real facts.db (`sqlite.Open`) and git, with `sync-exchanges.json` as GitHub and a second local git server serving widget ([ADR 0013](../../docs/adr/0013-sync-reads-bounded-passes-and-checks-the-replica.md)). Each sync's `Now` is its start; the window it reads in a full pass starts 365 days before. The test's clock moves only when the adapter waits, so a pass's start, its watermark, is the `Date` of the response before it. The page responses hold widget's pull requests as they stood a week after the first pass: page 1 #8 (10-08 09:00) and #5 (10-04 15:00), page 2 #10 (10-01 11:58) and #9 (09-25 18:00), the end of the list.

| Run | Request | Exchanges | What happens | Watermark after it |
| --- | --- | --- | --- | --- |
| 1 | `--repo acme/widget`, `Now` 10-09 00:00, a fresh facts.db A | S1 to S3, L1 | the repository is looked up and mirrored (a clone), page 1 is stored, page 2 fails with a `FORBIDDEN` error beside a null, which no retry fixes and no deleted account explains: the pass stops halfway. The mirror is up to date, so the links pass asks about every email (L1) and stores all 12 answers. No repository synced (`ErrNoFacts`); summary: 1 repository, 1 failed, 2 pull requests read | none: `Pending`, no `Updated`, `From` or `Synced`; the replica holds #5 and #8 |
| 2 | the same, `Now` 10-09 01:00 | S4 to S7 | the rerun reads the whole window again (a first sync: no pass completed), both pages; the check counts 5 on GitHub updated from 2025-10-09T01:00:00Z to 00:55:00, the pass's start (S4's `Date`) less five minutes, against 4 in the replica: 1 missing, recorded, and no second pass, since this one read the whole window already. No links query: run 1 asked about every email. Summary: 1 synced, 1 with pull requests missing, 4 read | `Updated` 10-09 01:00:00, `From` 2025-10-09T01:00:00Z, `Synced` 10-09 01:00, `Missing` 1 |
| scan | `app.Scan` of `<mirrors>/acme` with facts.db A, `Now` 10-09 01:30 | none | the scan reads the replica through the mirror's origin, and its JSON carries a `forge-incomplete` caveat | |
| 3 | `--repo acme/widget`, `Now` 10-09 02:00, facts.db B, which holds what an earlier stillhere left: #8 alone, with the watermark 10-08 09:00, #8's own update, and synced 10-08 10:00 | S8 to S14, L2 | a pass down to the watermark (since 10-08 09:00, stop 08:55): page 1 keeps #8 and skips #5, page 2 holds none that recent and ends it. The check counts 4 on GitHub against the replica's 1, so the same sync reads the whole window (S12, S13) and checks again: 4 and 4. Summary: 1 synced, 5 pull requests read (1, then 4) | `Updated` 10-09 02:00:03 (S11's `Date`, the second pass's start), `From` 2025-10-09T02:00:00Z, `Synced` 10-09 02:00, `Missing` 0 |
| 4 | `--org acme`, `Now` 10-09 03:00, facts.db A | S15 | acme lists widget archived: the sync leaves it out, its mirror moves to `<mirrors>/.stale/acme/widget.git` (#110), and nothing is left to sync (`ErrNoFacts`); summary: no repository | |
| 5 | `--repo acme/widget`, `Now` 10-09 04:00, facts.db C, holding what B held before run 3 | S16 to S20, L3 | the pass down to the watermark completes (stop 10-08 08:55: #8 kept, the rest older), the check counts 4 on GitHub updated from 2025-10-09T04:00:00Z to 03:55:00 against the replica's 1, and the shortfall, 3, is recorded before the full pass, whose first page fails as S3 did. The links pass asks about every email (L3). No repository synced; summary: 1 repository, 1 failed, 1 pull request read | `Updated` 10-09 04:00:00 (S16's `Date`, the completed pass's start), `Synced` 10-08 10:00 as it was, `From` none, `Missing` 3 |
| 6 | `--repo old-acme/widget`, `Now` 10-09 05:00, a fresh facts.db D | S21 to S24, L4 | GitHub answers the old name with acme/widget (S21), as it does a transferred repository, so the replica records `old-acme/widget` as its alias (wave 11, KT-2). A first sync: the whole window, both pages; the check fails with a `FORBIDDEN` error, which no retry fixes. The pass completed, so the replica is synced and complete from the window's start; nothing is recorded missing. Summary: 1 synced, 1 not checked, 4 read, the move `old-acme/widget → acme/widget` | `Updated` 10-09 05:00:00, `From` 2025-10-09T05:00:00Z, `Synced` 10-09 05:00, `Missing` 0 |
| scans | `app.Scan` of a clone of the mirror whose origin names `old-acme/widget`, `Now` 10-09 05:30, with facts.db D, then with facts.db A | none | with D, the clone joins widget's GitHub data by the alias, its card carries `forge-moved`, and `who-knows --repo acme/widget` finds it by the name GitHub gives it now; with A, whose replica learned no alias (runs 1 and 2 asked for acme/widget), nothing joins and the card carries `forge-unmatched`, naming acme/widget and `stillhere sync github --repo old-acme/widget` | |

## The token

The test plants a token and asserts that it reaches the API only in the `Authorization` header (the replay checks it on every request) and git only as the Basic password the local server checks, and that neither it nor its base64 form appears in the facts, any file under the mirrors, the mirror's local config, the logs, the errors of a refused and of a redirected clone, or a printed `Source`.
