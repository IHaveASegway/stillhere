# ADR 0007: AI-assisted work: human credited, AI tagged

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

In 2026, much AI-written code is committed under a human's name. Detecting bots by author name misses it. What git records:

- Claude Code adds `Co-Authored-By: <model name> <noreply@anthropic.com>`. The name varies, so match the email.
- Aider adds `Co-authored-by: aider (<model>) <aider@aider.chat>`.
- GitHub's Copilot coding agent is the commit author, with the human as co-author.
- Cursor's trailer string could not be verified.
- Editor completions leave no trace at all.

## Decision

- **Credit the human.** When an agent is the author and a human is a co-author, the human is credited.
- **Tag the commit** `ai_assisted` when a trailer or author matches the policy's pattern list. The list is versioned (`ai_assisted@N`) because conventions change.
- **Compare cohorts**, AI-assisted against other changes, in team and code-area views. A person's own AI-assisted share is shown only to that person.
- **Agents are never ranked rows.** Name-matched bots (dependency updaters and the like) keep their separate table.

## Alternatives considered

- **Agents as contributors**, with their own rows and split credit. Any split rule is arbitrary and would have to be defended.
- **Ignoring AI signals** (the 2026-10-07 plan). AI-assisted work becomes invisible.

Prior art: git-ai records line-level AI attribution in git notes, but only for commits made after it is installed.

## Consequences

- The impact of AI tools becomes visible, as a lower bound.
- The pattern list needs maintenance as tools change their trailers.

## Door

Two-way.

## Revisit if

A standard AI-attribution trailer emerges, or git-ai style notes become common enough to read alongside trailers.

## Amendment: the shares that leave the machine

Status: Proposed · 2026-10-15 · Deciders: Joseph Peterson · Amends "A person's own AI-assisted share is shown only to that person" for the MCP server's answers ([mcp.md](../spec/mcp.md))

### Context

The MCP server sent a repository's or a group's AI-assisted share only when at least three people landed in its window, so that no share it sent was close to one person's own. It decided one scope at a time. Shares add up over the scopes, since a group's landings are its repositories' and the whole run's are every repository's, so shares sent side by side can be subtracted. The review of 2026-10-15 (#68) found it in the first answer an agent asks for: a group of `team` (four people, 1 of 12 landings AI-assisted) and `solo` (one person, 3 of 4) showed the group at 4 of 16 and `team` at 1 of 12, and (4 − 1) of (16 − 12) is the solo person's own 3 of 4. The refusal for `solo` pointed at the group's share as well.

### Decision

- **The threshold stays three.** A share leaves the machine only when at least three humans are credited with a non-merge commit of a landing in its 365-day window (`app.AIShareMinPeople`). Three is the minimum frequency rule of disclosure control: one of three people who takes away their own landings still sees two people's together.
- **Secondary (complementary) suppression.** Bottom-up over the tree of scopes (the whole run, its groups, their repositories, and the repositories named on their own): when the withheld children of a scope leave a part of it that covers one or two people, more children are withheld, the smallest first (fewest people, then kind and key), until the part covers three or more. When no child can do that, the scope's own share is withheld; only the whole run can come to that, when every child it still shows is a group whose repositories are all shown. Every residual, a shown share less the shown shares below it, then covers no one or at least three people, at every level and across levels. `internal/app/aishare.go` holds the argument.
- **One decision per run.** `app.AIShares` is a pure function of a run's facts, which `app.RunNames` computes with the vocabulary every call reads, so every tool reads the same decision and no two answers disagree or can be subtracted: `overview` and `explain` hold scopes' shares, `person` holds only the user's own on their own page, and `changes` and `highlights` hold none. A withheld share leaves with neither its `num` nor its `den`. The `ai-few-people` caveat and `explain`'s refusal say that a share is withheld, and point at no share it could be worked out from.
- **The whole run's share** is decided on only where an answer can show it, the overview of every repository, which a run has when it was scanned with `--scope all` or has no groups (`report.WholeRun`).
- **The local report is unchanged.** It shows every share; whether it should apply the same rule is an open decision for the owner.

### Prior art

- Statistical agencies publish nested tables this way: a primary rule, such as a minimum frequency, and secondary cell suppression, solved over hierarchical tables by Statistics Netherlands' τ-ARGUS (its modular method) and R's sdcTable (Hundepool et al., *Statistical Disclosure Control*, Wiley 2012, ch. 4).
- Developer analytics apply the primary rule as a minimum group size: GitHub's Copilot metrics API answers for a team only on days when it had five or more members with an active Copilot licence, and Microsoft Viva Insights shows no group smaller than its minimum group size, five by default and at the least.

### Alternatives considered

- **The threshold per scope alone**, the rule this amends: it leaks by subtraction, as above.
- **Withholding the parent of every withheld child**: simple and safe, but one person's repository would cost its group's share and the run's.
- **Deciding per answer**: an answer about a group and another about one of its repositories could each be safe alone and leak together.
- **Noise or rounding** (differential privacy, controlled rounding): every share stays, but its numbers would no longer be the report's, which every answer promises ([mcp.md](../spec/mcp.md)), and noise that hides one person among three swamps the share.
- **A higher threshold**, five as GitHub and Viva Insights use: on its own it does nothing against subtraction. It is a separate choice, which this amendment leaves at three.

### Consequences

- A share of three or more people can be withheld to protect a smaller one (`team` above). A scope withholds at most one more child for it, since a shown child's residual covers no one or at least three people, and the caveat names every withheld scope without saying why each is withheld.
- Known limits, each with what would close it:
  - **Consecutive runs.** Two runs a week apart cover overlapping 365-day windows, so the difference of one scope's shares in the two is the landings that entered and left the window, which can be one person's. Closing it means deciding against the runs already published, keeping per scope what earlier answers showed and checking the residuals across runs, or showing shares over fixed windows that do not overlap, such as calendar quarters, whose differences are whole windows that each pass the threshold.
  - **The cohort comparisons** in `explain`'s detail (`durable_ai_n`, `durable_other_n`, `reverted_ai`, `reverted_other`) count a narrower population than the share: changes 90 to 365 days old, and reverted landings. The detail of a shown share can be one or two people's. Closing it means counting the people behind each comparison and withholding it below three.
  - **All or none.** A shown share of 0% or 100% tells each of its people their own tag: no landing was tagged, or every one was.

### Door

Two-way: the decision changes only what the MCP server sends, and nothing stored.

### Revisit if

Answers of one run are read beside another's (the consecutive-runs limit above), or the owner decides that the local report applies the rule too.
