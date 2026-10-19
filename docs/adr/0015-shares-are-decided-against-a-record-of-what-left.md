# ADR 0015: AI-assisted shares are decided against a record of what left the machine

Status: Proposed · 2026-10-19 · Deciders: Joseph Peterson · Closes two known limits of [ADR 0007's amendment](0007-ai-assisted-attribution.md#amendment-the-shares-that-leave-the-machine) (#85)

## Context

ADR 0007's amendment decides once per run which AI-assisted shares the MCP server may send: a scope of fewer than three people is withheld, and secondary suppression withholds more, so that no two answers about one run can be subtracted to give a share of one or two people. It recorded two ways out that it could not close. Both come from one cause: each decision is made alone, from one run's facts, by whichever binary reads the run.

- **Two runs.** A scan of group `ai` (`team`, four people, 1 of 12 landings AI-assisted; `solo`, one person, 3 of 4) shows the group at 4 of 16 with both repositories withheld. A rescan of `team` alone minutes later is a run with no groups, whose decision shows `team` at 1 of 12. One MCP session reads both, since each call reads the latest run, and (4 − 1) of (16 − 12) is the solo person's own 3 of 4. A stdio client against main (d4eba55) does exactly this. The windows need not be equal for the difference to be exact: `--now` defaults to the second of the scan, and no landing enters or leaves a 365-day window in minutes, nor for most repositories in a day.
- **One run, two versions.** Secondary suppression breaks ties by kind and key. Group `trio` holds `r1` (two people, 3 of 4) and `r2` and `r3` (three people each). Main shows the group (4 of 10) and `r3` (0 of 3); a build of main whose tie-break runs the other way shows the group and `r2` (1 of 3). Each is safe alone; together they give (4 − 1 − 0) of (10 − 3 − 3), `r1`'s two people's 3 of 4. Any change after a release to the count of people, the threshold or the order does the same.

The amendment said both need a record of what was sent, kept apart from the run rows, and that the record must start with the first release, or a later version cannot know what that one sent. v0.1.0 is next.

The problem is an old one. Statistical agencies call it linked tables, or multiple releases: each table is safe, the set is not (Hundepool et al., *Statistical Disclosure Control*, Wiley 2012, ch. 4). Dinur and Nissim ("Revealing Information while Preserving Privacy", PODS 2003) showed that answering enough overlapping aggregates without looking at what was answered before rebuilds the data. The US Census Bureau reconstructed 2010 census records from its own published tables, which is why it moved to differential privacy for 2020 (Garfinkel, Abowd and Martindale, "Understanding Database Reconstruction Attacks on Public Data", CACM 2019). Query auditing is the other answer: decide each new answer against the answers already given (Kenthapadi, Mishra and Nissim, "Simulatable Auditing", PODS 2005).

## Decision

Audit against a record of what left, written before it leaves.

1. **A record of its own.** `egress.db` sits beside facts.db in `STILLHERE_HOME` (`stillhere mcp --egress FILE` names another). It is SQLite with its own application id and numbered migrations from 1, created 0600. Its one writer is `stillhere mcp`; `stillhere egress` prints it; the web server (M3) must use the same record and the same rule. Per run (its id and start time, since two facts.db files reuse ids) it holds:
   - a digest of the run's AI-share inputs and the shares' window;
   - every share the decision lets leave: its scope, the repositories it adds up, its num and den;
   - each such repository's key and name, its own counts, its history (the SHA-256 of the SHAs of its landings in the window), and its people: one number per block of addresses (`identity.Blocks`), `1`, `2` and on, distinct within the decision and meaningless outside it, numbered in an order no one can repeat (by HMAC-SHA-256 under a random key made for the decision and never stored).

   It holds no fact and no email. Triggers refuse every UPDATE and DELETE: an answer that left is a fact (Helland, "Immutability Changes Everything", CIDR 2015).
2. **Write before send.** The first time an answer about a run would hold a share, the server makes the run's decision final (`app.ShareDecision`) and renders the answer again with it.
   - If the record holds a decision for the run, by any version, that decision is used as it is, so two versions never disagree about one run.
   - If the record's digest differs from the run's, the run's numbers changed under it, and every share of the run is withheld (`ai-record-differs`).
   - Otherwise the server makes the decision, commits it and only then returns it: the order of a transactional outbox, record then send. The commit is `synchronous=FULL` with `fullfsync`, without which a sync on macOS reaches the drive but not past its write cache, and a decision lost to a power cut would let a later one disagree with a share already sent.

   The lookup, the decision and the write are one transaction under the record's write lock. Two servers that share a home therefore cannot both decide one run, nor decide two runs of overlapping windows without the later seeing the earlier's decision. A call whose answer holds no share records nothing.
3. **Decided against what was sent.** Every decision recorded for a window that overlaps the run's, whatever its as-of time, and the run's own shares are taken as one table. A share is a sum of its repositories' counts, so the table's terms are repositories as each run counted them. A recorded repository and one of the run's are one term when they carry the same counts and are the same repository, by key or by history. That catches a rescan in which no landing entered or left the window, a copy in another directory, and a clone whose origin is spelt otherwise (with or without `.git`, or the scp form of a mirror's https origin), each of which subtracts exactly. A repository whose counts changed is another term. The run's shares may leave only when the table keeps three properties, the first two of which ADR 0007's amendment proves for one run:
   - its sets of terms nest;
   - every residual (a set less the sets inside it) holds no one, or at least three people, counted by the people of each decision that holds the set;
   - a repository carries the same counts in every run that sent a share of it over the same landings (same history) or in the same window, or the difference of two shares would be its change between the scans (a policy or a `.mailmap` changed).

   When the table breaks one of them, the run's offending shares are withheld, the run's own rules decide again around them, and the check repeats. Each round withholds one more share, so it ends. The share that gives way is one that overlaps a recorded set without nesting, or one beside two recorded sets that overlap each other without nesting, or one that holds a repository whose counts changed over the same landings or window. So is one that holds a repository the table also holds a copy of in one run or one decision, since the table cannot say how often that term enters a sum. For a residual of one or two people, the run's shares inside the set give way first, smallest first, and then the set itself when only this run holds it.

   The first version of this rule compared only identical windows. The review found that two scans with the default `--now`, seconds apart, were never compared, and that their shares subtracted to the solo person's 3 of 4 exactly as on main; so did scans a second or a day apart, and a copy of `team` under another key.
4. **Fail safe.** A server that cannot open or write its record, or runs without one, withholds every share and says why (`ai-not-recorded`). It answers everything else, and tries the record again on the next call (Saltzer and Schroeder, "The Protection of Information in Computer Systems", 1975: fail-safe defaults).

The same stdio client against this branch:
- **Rescan.** The rescan's `team` and whole-run shares are withheld, both in the session that sent the group's share and in a later one, whether the rescan has the same `--now`, one a second or a day later, or the default clock three seconds later.
- **Copies.** At the same `--now`, a copy of `team` in a directory of another name, a clone whose origin drops `.git`, and a clone whose origin is the scp form of the group's https origin are withheld too.
- **Two versions.** The two builds that decide `trio` differently both send the decision recorded first.
- **Read-only record.** With the record in a read-only directory, the overview has every other number and no share, and `stillhere egress` still prints it.

A randomized check backs the table: 40,000 cases of 3 to 5 repositories, 2 to 6 runs whose `--now` spread over 300 days, repositories whose counts and people change between runs, some read under a copy's key, and some beside their own copy in one run. Every 0/1 sum of terms in the rational span of the shares let out was checked, with an attacker who takes a key's equal counts in two answers for one term. None held one or two people. With the record ignored, the same check finds such a sum at its first case.

### Why a table, not a check of pairs

The lane's first design compared pairs: a candidate share S and a recorded share R whose repositories hold S's, withholding S when people(R) less people(S) number one or two. That count is not the residual's people. The residual is the share of R's repositories that S lacks, and its people are whoever landed there, some of whom may land in S too. In `TestMCPTools`, Sam lands in `band` beside three others and alone in `side`. people(mixed) less people(band) is no one, so a pair check sends `band` beside `mixed`, and the difference is `side`: Sam's own share. A mutation of the code to the pair rule fails that row.

Pairs also miss residuals that three shares make (a group from one run, `r2` from another, `r3` from a third), and sets that overlap without nesting: groups {r1, r2, r3} and {r2, r3, r4}, with r4 shown, give r1. The table catches both.

## Alternatives considered

- **Never send a repository's or a group's AI-assisted share over MCP.** The strongest rule and the simplest, but an agent loses the share of every scope. It stays the fallback if review finds this rule unsound.
- **Differential privacy:** noise on each share and a privacy budget across answers, as OpenDP and Tumult Analytics keep it. LinkedIn's PriPeARL makes a repeated query return the same noise, so answers cannot be averaged (Kenthapadi and Tran, CIKM 2018). It closes subtraction by construction. But every number would then differ from the report's, which every answer promises, and noise that hides one person of three swamps a share of 12 landings.
- **The decision stored in facts.db, with the run.** The MCP server opens facts.db so that it cannot write it. facts.db is also disposable: deleted and scanned again, its run ids start over. And a rescan is a new run that the stored decision never saw. So this would close "one run, two versions" only for runs that are kept, and would not close "two runs" at all.
- **The floor's rules kept as versions**, so that a later binary can work out what an earlier one would have decided. Every binary would need every past rule forever, and it still could not tell what was sent from what would have been sent.
- **Pairs only** (above): it misses residuals whose people land on both sides, residuals of three shares, and overlapping sets.
- **Withholding every share once `run-changed` fires.** It protects one session, not a model provider that sees several, and it makes one run's answers depend on what the session read before.
- **A JSON Lines file.** Appending is simple, but two servers need a lock and an atomic check-then-append, and a crash in the middle of a line needs repair. SQLite gives one transaction across the check and the write, `synchronous=FULL`, and it is linked already.
- **Identical windows only**, this rule's first version. It closes only scans given the same explicit `--now`, which no documented workflow passes.
- **Windows rounded to a UTC day**, so rescans of one day share a window. It closes rescans of the same day, not of the next, and moves the edge of every window the report shows.
- **The same counts required across overlapping windows**, as in one window. Every repository that changed between two scans would then lose its share for a year after it was first sent. Comparing terms instead costs only the shares that a recorded one could be subtracted from.
- **People as salted hashes under a salt the record keeps**, so a person's key matches across decisions. No check needs that: every residual is counted by the people of one decision, so numbers distinct within a decision are enough. The review showed what the salt allowed: anyone holding the record and a list of candidate addresses could test them against the hashes, and every person of a test record was found.

## Consequences

- No two answers can be subtracted to give a share of one or two people, whether they are about one run from two versions, or about runs whose windows overlap and in which the repositories they hold carry the same counts, as in every rescan in which no landing entered or left the window. That holds for every server that shares the record.
- Order matters. The first run answered at a window keeps its shares, and later runs give way: a team-alone scan answered first costs the group's share later. The record says which run decided what, and `stillhere egress` prints it.
- The record holds what may leave, not what left. Once one share of a run is about to leave, the run's whole decision is recorded, and later runs are checked against all of it. That errs toward withholding.
- Measured cost, once per run, on the first answer that would hold a share (Apple M4 Max, Go 1.27.2; 40 repositories in 8 groups, five people each, one decision a day, each repository's counts growing every 3 to 7 days):
  - against 9 earlier decisions, 12 ms; against 90, 36 ms; against 365, 110 ms (median of five; up to 210 ms on a loaded machine), of which about half is reading them and a third deciding. It levels off there, since only decisions whose windows overlap are read;
  - `fullfsync` adds about 4 ms to each commit on macOS (review measurement: 0.97 ms to 5.09 ms median);
  - later calls of a run read the decision from memory;
  - egress.db holds 400 such decisions in 8.6 MB, about 21 KB each. It grows forever: no decision is ever deleted, since a later scan may name any `--now`.
- A recorded decision is never revised, even by a later version whose rule is stricter. A change of the rule after a release applies to new runs and is checked against the record, which is what makes such a change safe.
- Known limits, with what would close each:
  - **Changed counts.** A repository whose landings changed between two runs, one entering or leaving the window, is another term. So the difference of its two shares is the change: the AI-assisted share of the landings that entered less those that left, which can be one person's. Closing it needs the same counts required across overlapping windows (rejected above for its cost), shares stored as sums of smaller units that both windows share (weeks), which ai_assisted@1 does not store, or fixed windows such as calendar quarters.
  - **Windows that do not overlap** are never compared, even through a run between them that overlaps both. Their landings are disjoint, so equal counts there are a coincidence an answer cannot tell from a change, though a repository whose counts stay level for a year makes one.
  - **A copy whose landings differ.** A copy or clone that fetched more or less than the original holds other landings, so neither its key nor its history matches, and it is changed counts.
  - **Another record.** Two homes, an `--egress` pointed elsewhere, a deleted record, or a facts.db copied to another machine each start afresh. A record protects only what its own servers sent. So the record is not disposable, as facts.db is, and no message of stillhere suggests a new one: a record that cannot be written is made writable again or moved, with its `-wal` file. A relative `--egress` is refused, since it would name another record in every directory a server starts in.
  - **Numbers, not anonymity.** A decision's people say how many people each repository's landings are made of, and which of its repositories share people. They name no one, and two decisions' do not join.
  - **A withheld share says something.** That a share is withheld tells the reader its residual is small, as ADR 0007's amendment accepts for one run. Simulatable auditing avoids that by deciding only from past answers, which a floor on people cannot do.

## Door

One-way for the format: from v0.1.0 on, every later stillhere must read egress.db. Its schema is therefore versioned from 1, marked by its own application id, and changed only by migrations that add. Migration 1 was corrected in place before the first release, after review: it no longer keeps a salt, numbers each decision's people, and keeps each repository's name and history. Two-way for the rule that decides new runs: a later version may decide them differently, against the record.

## Revisit if

- An audit of real records shows that most withholdings come from order (a run answered first), not from residuals of one or two people: a fixed window or a per-session choice of run may cost less.
- Review finds a subtraction across recorded decisions that the table admits: fall back to sending no scope's share.
- At an organisation's scale, a decision takes over 500 ms, or the record grows past 100 MB in a year. Then read only the decisions whose repositories meet the run's, or keep each repository's people in one row.
