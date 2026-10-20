# Architecture decision records

One file per decision that is hard to reverse. Format after Michael Nygard, "Documenting Architecture Decisions" (2011), plus two fields: **Door** (one-way or two-way, and why) and **Revisit if** (the evidence that would show the decision was wrong).

| # | Decision | Status |
|---|---|---|
| [0001](0001-measure-changes-rank-per-metric.md) | Measure changes; rank one metric at a time | Accepted |
| [0002](0002-go-single-binary.md) | Go, one static binary, released with GoReleaser | Accepted |
| [0003](0003-facts-metrics-views-sqlite.md) | Facts, then versioned metrics, then views, on SQLite | Accepted |
| [0004](0004-github-behind-sync-boundary.md) | GitHub in v1, behind an explicit sync boundary | Accepted |
| [0005](0005-web-server-github-app-login.md) | Team web server with GitHub App sign-in; local mode first (amendment) | Accepted; amendment Proposed |
| [0006](0006-ai-mcp-and-narrated-highlights.md) | AI through MCP and narrated highlights, never in the ranking path | Accepted |
| [0007](0007-ai-assisted-attribution.md) | AI-assisted work: human credited, AI tagged; the shares that leave the machine (amendment) | Accepted; amendment Proposed |
| [0008](0008-apache-2-public.md) | Apache-2.0, public on a personal account | Accepted |
| [0009](0009-default-branch-as-upstream-last-fetched.md) | A work tree's default branch is measured as upstream last fetched it | Proposed |
| [0010](0010-rank-intervals-bootstrap-per-kind.md) | Rank intervals: a bootstrap per metric kind, reproducible by derived seeds | Proposed |
| [0011](0011-a-run-keeps-the-github-facts-it-read.md) | A run keeps the GitHub facts it read | Proposed |
| [0012](0012-a-run-reads-each-repository-as-it-stood-at-now.md) | A run reads each repository as it stood at `--now` | Proposed |
| [0013](0013-sync-reads-bounded-passes-and-checks-the-replica.md) | A sync reads bounded passes and checks the replica against GitHub's count | Proposed |
| [0014](0014-labels-are-evidence-not-the-gate.md) | A lead's labels are the gate; a model panel's are reference evidence | Proposed |
| [0015](0015-shares-are-decided-against-a-record-of-what-left.md) | AI-assisted shares are decided against a record of what left the machine | Proposed |

Open decisions that have not reached an ADR yet are listed in [product.md](../product.md#open-decisions).

## Template

```
# ADR NNNN: <decision>

Status: Proposed | Accepted | Superseded by NNNN · <date> · Deciders: <names>

## Context
## Decision
## Alternatives considered
## Consequences
## Door
## Revisit if
```
