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
