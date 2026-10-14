# ADR 0006: AI through MCP and narrated highlights, never in the ranking path

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

AI should help people and agents understand the facts, either through an Anthropic API key or as a plugin. Principle 8 forbids AI from computing, changing or ordering measurements ([ADR 0001](0001-measure-changes-rank-per-metric.md)). The purpose is recognition, which suits a written summary of highlights.

## Decision

- **`stillhere mcp`**: a local stdio server built on the official MCP Go SDK, over `facts.db`. stillhere holds no API key on this path; the user's own agent and account do the AI work. A Claude Code plugin can bundle this server with a skill that teaches the agent the schema and the stated limits.
- **Tools are shaped around the questions an agent asks, not around tables** (amended 2026-10-14, following Anthropic's "Writing effective tools for agents", 2025):
  - `overview(scope)`: a platform (group) or a single repo. Covers age, size, who the code is made of, concentration, knowledge loss, and AI and bot share.
  - `person(name)`: breadth (repos), ownership (majority files), survival and recent work. The AI-assisted share is shown only for the user's own identity ([ADR 0007](0007-ai-assisted-attribution.md)).
  - `who_knows(path)` and `changes(scope, since, person?)`.
  - `explain(metric, subject)`: the commits and pull requests behind a number.
  - `highlights(period)`.
  - `identity_candidates(scope)`: possible duplicate identities, with suggested `.mailmap` lines. They are never applied; `.mailmap` stays the only identity store.
  - `metrics()`: definitions and blind spots.
  - `diff(change)`: only when started with `--allow-source`.
- **Every tool answer** carries:
  - its provenance: the run, the as-of time and the metric versions;
  - the caveats that could make it wrong: missing or shallow repos, identity gaps, formatting passes.

  Answers use display names rather than ids. `response_format` is `concise` or `detailed`, lists paginate under the client's token cap, and errors say what to run next. Emails appear only with `--allow-emails`, because the answer goes on to the agent's model provider.
- **`stillhere narrate`**: uses `anthropic-sdk-go`; the default model is `claude-opus-5-5` and the model is configurable. It writes the highlights summary under these rules:
  - The payload is built from highlight facts only. People become placeholders (`P1`, `P2`, …) and real names are restored locally after the reply. No emails, source code, commit subjects or absolute paths are sent.
  - Every number in the reply must appear in the payload. If one does not, the reply is regenerated once and then dropped.
  - Stored with the model id the API returned and a hash of the request body.
  - Cost: about $0.12 a run (roughly 25K tokens in and 1K out at $4 / $20 per million tokens).
- **AI never computes, changes or orders a measurement or rank.**
- **AI judging of individual changes is deferred** until a hand-labelled set exists. The M2 validity check seeds that set.

## Alternatives considered

- **A narrator only.** Agents would get no structured access.
- **One MCP tool per table or metric** (`get_metric`, `list_people`, and so on). Easy to generate, but an agent then spends many calls and much context stitching rows together, and works with ids where it needs names.
- **AI-judged changes in v1.** This sends source to a provider, its labels are unvalidated, and its cost scales with commit volume.
- **No AI.** It misses a real need, and MCP is cheap with the official SDK.

## Consequences

- The MCP path needs no key handling in stillhere.
- Narration cannot reveal who is who to the provider, and cannot invent numbers.
- Narration needs a key. Remote MCP over HTTP will need authentication, later.

## Door

Two-way.

## Revisit if

Users without an MCP client want question-and-answer over their data, or the numbers check rejects narrations often.
