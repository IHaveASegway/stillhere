# ADR 0008: Apache-2.0, public on a personal account

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

The goal is a tool people can easily install and run on their own code. Adoption and contributions matter more than control. Homebrew's core repository requires notability (75 stars, or 225 if you submit your own project, and a repo at least 30 days old); a personal tap has no such bar.

## Decision

- Public repository on the author's personal GitHub account. A new organization would be a second owner to administer for a one-person tool; it can wait until a second maintainer needs shared admin.
- Apache-2.0: permissive, with an explicit patent grant, which matters for a tool that runs next to employer code.
- Distribution through a personal Homebrew tap from M0; a homebrew-core submission once notability is met.

## Alternatives considered

- **MIT**: also fine, but it has no explicit patent grant.
- **FSL** (source-available, converting to Apache after two years): it would block competing hosted offerings, but it is not open source by the OSI definition and attracts fewer contributors.
- **Private until v1**: delays feedback and the tap.

## Consequences

- Anyone can use stillhere, including inside a commercial hosted product.
- Every file committed to the public repo must be free of real people's data. The 2026-10-07 plan, which named real colleagues, is not committed.

## Door

One-way for code already released under Apache-2.0. Relicensing later is possible only for new code, or with every contributor's consent.

## Revisit if

A paid team edition becomes a goal. Then decide whether it is a separate product on top of the Apache-2.0 core.
