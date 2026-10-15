# Recorded GitHub responses (`TestGitHubSync`)

Status: written by hand for #14 · 2026-10-14 · the contract test of `internal/github` and the oracle of `reviews_given@1`

These files are the GitHub API, recorded: `TestGitHubSync` replays them in order, makes no live call, and checks the facts the adapter returns against the values below. Every response was written by hand from GitHub's documented GraphQL shapes. A live, read-only query of a small public repository (`cli/go-gh`, 2026-10-14) confirmed the shapes, and `rateLimit(dryRun: true)` gave the costs quoted in the code. A live query of `cli/cli#14660` (2026-10-14) confirmed that a bot's commit carries its account in `author.user` (`{"login": "dependabot[bot]"}`), although `GitActor.user` is typed `User`. Nothing was recorded from a real organization: the people, emails and SHAs are made up, and every email is under `example.com` or `.example`.

The expected values were derived from these files and from [docs/metrics.md](../../docs/metrics.md#reviews_given1-m2) by hand. The code never produced them. Change a response and you change an oracle: re-derive it here first.

## Files

| File | Exchange |
| --- | --- |
| `exchanges.json` | The order of the exchanges: operation, variables, status, headers and body file. The test fails on a request that is not the next one, and on any exchange left unrequested. |
| (inline text) | E1: the first request of the run finds the hourly points spent by other tools. GitHub says so in a `200`: an error of type `RATE_LIMITED`, with `X-RateLimit-Remaining: 0` and `X-RateLimit-Reset` in the headers. |
| `repos-1.json`, `repos-2.json` | E2, E3: acme's repositories, two pages. |
| `pulls-asc-1.json` | E4: the first pass over widget, oldest update first, page 1. |
| `secondary-limit.json` | E5: page 2 meets a secondary rate limit, `403` with `Retry-After: 60`. |
| `pulls-asc-2.json` | E6: page 2 again; 40 points are left until 01:00:00. |
| (inline text) | E7: page 3 gets a `502` from GitHub's edge. |
| `pulls-asc-3.json` | E8: page 3 again, at half the page size; PR 9 has more labels, reviews and commits than the page held. |
| (inline text) | E9: PR 9's label follow-up meets a secondary rate limit that GitHub reports in a `200`, by its message, without `Retry-After`. |
| `labels-09.json`, `reviews-09.json`, `commits-09.json` | E10 to E12: the rest of PR 9's lists. |
| `pulls-desc-1.json` | E13: a week later, newest update first. |

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

1. **Clone**: widget's mirror holds `refs/heads/feature` and `main` (no `refs/pull/*`), `HEAD` on `main`, the `+refs/heads/*` refspec, and its node id, `stillhere.forgeId = R_widget`.
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
| 9 | ada | merged | 09-25 18:00 | 09-25 18:00 | needs-review, then enhancement (E10) | bob commented 09-22 09:00, 16, 3 · erin dismissed 09-23 10:00, 0, 0 · then (E11) bob approved 09-24 15:00, 0, 0 · cara approved 09-25 17:00, 0, 0 |
| 5 | erin | open | 10-01 10:00 | | refactor | ada approved 10-01 10:00, 11, 0 |
| 10 | bob | open, draft | 10-01 11:58 | | | none |
| 8 | bob | open | 10-01 12:00 | | feature | ada approved 10-01 12:00, 0, 0 |

Commits: one per pull request (`0N00…01`), two for #2, and three for #9 (`0900…01` and `0900…02` on the page, `0900…03` from E12, with `0900…02` under `ada@other.example`). A merged pull request's merge commit is `f0N0…`, authored by its author, except #7's, whose author has no account.

### First pass (E4 to E12)

The watermark is zero, so the pass reads oldest update first and each page carries the newest update it holds:

| Page | Pull requests | Watermark |
| --- | --- | --- |
| 1 | #3, #7, #1 | 2026-08-10T12:00:00Z (#1) |
| 2 | #6, #2, #4 | 2026-09-20T11:00:00Z (#4) |
| 3 | #9, #5, #10, #8 | 2026-10-01T12:00:00Z (#8) |

Translation checks this pass makes: states lowercased (`MERGED` is `merged`); a deleted author is a user with no login; a Bot is `Bot: true` with GraphQL's login; emails lowercased (`Erin@Old.Example`); a whitespace-only body is 0 characters; labels sorted (`enhancement` before `needs-review`); reviews sorted by submission; commits kept in GitHub's order; no merge commit on an unmerged pull request.

### The waits

Every wait for a reset is measured on GitHub's clock: the `Date` of the response that announced it, plus the time since.

| When | Why | Wait |
| --- | --- | --- |
| E1 | a spent budget reported in a `200`: `X-RateLimit-Reset` is 00:00:00 and the `Date` is 23:50:00 | 10 min, plus a 1 s margin and up to 1 s of jitter |
| E5 | `Retry-After: 60` | 60 s plus up to 1 s of jitter |
| before E7 | E6 left 40 points (below the reserve of 100); the reset is 01:00:00 and E6's `Date` is 00:50:00 | 10 min, plus a 1 s margin and up to 1 s of jitter |
| E7 | a `502`: transient, so the page halves from 50 to 25 (E8's variables) | between 1 s and 2 s (equal jitter, first attempt) |
| E9 | a secondary limit reported in a `200`, with no `Retry-After` and points left | between 1 and 2 min (equal jitter from a 2 min ceiling, first attempt: GitHub asks for at least a minute) |

### Later pass (E13)

Since is the first pass's last watermark, 2026-10-01T12:00:00Z. The pass reads newest update first and stops at the first pull request updated before since less the five-minute overlap, 11:55:00:

| Read | Updated | Kept |
| --- | --- | --- |
| #8 | 10-08 09:00, now merged 10-08 09:00 (merge commit f08), with cara commented 10-07 16:00, 18 chars, 1 inline | yes |
| #5 | 10-04 15:00, now merged 10-04 15:00 (merge commit f05), with bob commented 10-03 09:00, 24 chars, 1 inline | yes |
| #10 | 10-01 11:58, unchanged | yes: inside the overlap, read again |
| #9 | 09-25 18:00 | no: the pass stops here, with no follow-up for it |

One page, watermark 2026-10-08T09:00:00Z (#8).

## reviews_given@1

The replica after both passes, read as of the fixture's `Now`, 2026-10-07T00:00:00Z. The window is [2026-07-09T00:00:00Z, 2026-10-07T00:00:00Z). The policy holds the packaged `[bots]` fragments (`copilot`, `gpt-engineer`, `dependabot`, `renovate`, `[bot]`), and the input has no `identity.Directory`, as `metrics.Compute` may pass it, so the metric builds one.

**The run** scanned widget through a user's own SSH clone, origin `git@<git URL's host>:acme/widget.git`, in group `acme`, not through the managed mirror. The replica keys widget by its HTTPS clone URL, and the two name the same host, owner and name. The clone's history holds the squash commits f01, f02, f03, f04, f05, f07 and f09 with their mailmapped authors (f03 and f05 as `erin@example.com`); f08 landed after `Now`.

**The replica also holds `other-org/secret`** (`R_secret`), which an earlier sync of another organization left there, with one pull request: #1 by `dana`, merged 09-30 12:00, approved by bob 09-30 11:00. The run did not scan that repository, so it counts in no scope, `all` included: bob's value stays 4.

**Logins to people.** Each login takes the run's author of the commits GitHub attributes to it: ada (f01, f09) is `ada@example.com`, bob (f02) is `bob@example.com`, and erin (f03, f05) is `erin@example.com`, the mailmap reaching GitHub's `erin@old.example` through the shared SHA. cara and renovate-approver have no commit in the run, so they are `@cara` and `@renovate-approver`. A Bot reviewer is `@<login>[bot]`.

**Pull requests that count**, merged before `Now`: #1, #2, #3, #4, #5 (merged 10-04, learned in the later pass), #7 and #9. Not #6 (closed unmerged), #8 (merged 10-08, after `Now`), #10 (open) or other-org's #1 (not scanned).

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
| other-org #1 | | bob approved 09-30: a repository the run did not scan |

**Values**, the same for scope `all` and for group `acme` (which holds the clone), with N equal to the value:

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

## The token

The test plants a token and asserts that it reaches the API only in the `Authorization` header (the replay checks it on every request) and git only as the Basic password the local server checks, and that neither it nor its base64 form appears in the facts, any file under the mirrors, the mirror's local config, the logs, the errors of a refused and of a redirected clone, or a printed `Source`.
