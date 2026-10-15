# ADR 0001: Measure changes; rank one metric at a time

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

stillhere is for recognition and curiosity. Tech leads are the primary audience, and they want ranked views of who did meaningful work. Three facts constrain how ranking can be done honestly:

- **Small samples.** At about 40 landings, a durability estimate carries roughly ±13 percentage points; at 12 landings, about ±24. Most rank differences on a team-sized leaderboard are inside that noise.
- **Goodhart's law.** Once a measure becomes a target, people optimize the measure. A single score is the easiest target.
- **Blind spots.** git and GitHub do not see design, mentoring, incident response or planning.

Rules also apply when people are ranked. The EU AI Act (Annex III 4(b)) treats *AI systems* used to "monitor and evaluate the performance and behaviour of persons" at work as high-risk; those obligations apply from 2 December 2027 (Regulation (EU) 2026/1744). Under recital 12 and the Commission's 2025 guidelines, software whose rules are written entirely by people is not an AI system. In Germany, BetrVG §87(1) Nr. 6 gives works councils co-determination over technical systems objectively suited to monitoring performance, whatever the intent. GDPR Article 22 bars decisions with significant effects based solely on automated processing. None of this is legal advice; it shapes the design.

## Decision

- Rank **one metric at a time**: `durable`, `simplifying`, `reviews_given` (M2), `load_bearing` (M5). No composite score.
- Every rank shows a **95% interval** from a bootstrap, seeded so it is reproducible: over the person's landings for a ratio metric, and over the window's weeks for a sum (amended 2026-10-14; the method is [ADR 0010](0010-rank-intervals-bootstrap-per-kind.md)). People below the minimum sample (10 landings, or 10 reviewed pull requests) are listed and not ranked.
- Activity (`landed`) and `surviving` lines are context, never ranked.
- **Visibility**: members of a named GitHub team (leads) see ranks. Every person can see their own page with exactly the facts their lead sees. Org members see change highlights and code-area views.
- Bots are never ranked. AI-assisted work is credited to the human and tagged ([ADR 0007](0007-ai-assisted-attribution.md)).
- **AI never computes, changes or orders a measurement or rank.** Ranking rules are written by people and stay rule-based.
- The README and the first screen state the purpose and the limits.

## Alternatives considered

- **A composite score whose weights the team defines.** It blends incomparable units, hides which property drove a rank, and needs a validation study before anyone should trust it.
- **A built-in formula.** It imposes our values on every team.
- **An AI-produced score.** Not reproducible, hard to explain, and it would make stillhere an AI system under the Act.
- **Codebase-only output, or self-view only.** Neither serves recognition.

## Consequences

- Ranks are explainable, reproducible and defensible.
- Several separate metrics give less incentive to game any one of them than a single score would.
- There is no single "top contributor". At real sample sizes many intervals overlap, so many ranks are ties. That is the honest answer.
- Work outside git and GitHub stays invisible. The UI says so.

### Before anyone uses this for performance reviews or pay

Not supported in v1. An organization that wants it would need, at minimum: notice to staff before use; self-access (already built); a way for a person to annotate or contest a number; an audit log of who viewed ranked data (M3, not built); a works-council agreement where one applies; a person, not the tool, making every decision; retention limits; and AI kept out of the ranking path.

## Door

Two-way technically, since ranks are a view over facts. One-way socially: once people have seen a leaderboard, removing or changing it is a trust event.

## Revisit if

- Leads have not opened ranked views by the end of M3. Then drop ranks and keep the highlights.
- The labelled-sample check gives a ranked metric an AUC below 0.7. Then change the definition (a new version) or take it out of ranks.
