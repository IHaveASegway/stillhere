# ADR 0003: Facts, then versioned metrics, then views, on SQLite

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

Metric definitions will change as we learn what "meaningful" means, and people need to trust that a number means the same thing it meant last month. Agents and people need to query and audit numbers, not only read them. GitHub data needs a local store for incremental sync. History and backfill are in scope.

The 2026-10-07 plan wrote one JSON report per run. Under that design a new metric needs a rescan, agents see only precomputed numbers, and there is nowhere to keep incrementally synced GitHub data.

## Decision

- **Collectors write facts** into `facts.db` (SQLite, WAL mode): commits, landings, files, trailers, line owners per run, and pull requests and reviews.
- **Metrics are versioned** (`id@N`): computed over facts, each with a definition, a fixture oracle and a validity note ([metrics.md](../metrics.md)). A definition change is a new version. An old version stays computable until a release removes it with a changelog entry.
- **Results are stored per run** with their versions. Readers see only runs marked complete; each run commits in one transaction.
- **`explain`** traces any value to the facts behind it, for the CLI, the web UI and MCP.
- **`report.json`** is an export of one run, for consumers that do not want SQL.

## Alternatives considered

- **A JSON report only** (the 2026-10-07 plan). Simplest, but it fails the three needs above.
- **DuckDB with Parquet.** Excellent for analytics, but its Go binding needs CGO, which breaks static cross-compiled binaries and the Homebrew story.

Prior art: Airbnb's Minerva keeps metric definitions in one place with review, validation and test runs before a metric is used, the same idea at company scale. See also "SQLite As An Application File Format" (sqlite.org).

## Data ownership

`facts.db` is a derived replica. The sources of truth stay git, GitHub and `.mailmap`. The file can be deleted and rebuilt, except for GitHub data that has since been deleted upstream, which is gone either way.

## Consequences

- New metrics and new versions can be computed over existing facts without rescanning.
- Every number has provenance, and agents and people get SQL.
- Schema migrations become real work: a migrations table, and a full rebuild as the fallback because the file is disposable.
- `facts.db` contains emails, so it is created with mode 0600 and documented as sensitive.

## Door

Two-way for the storage engine, behind the `FactStore` port. One-way for metric ids once published: people and agents bind to `durable@1`.

## Revisit if

`facts.db` grows past about 10 GB, or p95 query time passes one second. Then consider Postgres for the team server, or DuckDB with CGO for analysis.
