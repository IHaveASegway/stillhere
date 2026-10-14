# ADR 0004: GitHub in v1, behind an explicit sync boundary

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

Review work is invisible in git, and ranking people on code authorship alone under-ranks reviewers and mentors. So GitHub data (pull requests and reviews) is in v1. The 2026-10-07 plan had a valuable property: analysis opened no network connection, so it was private and reproducible. And identity now has two sources: git emails (aliased by `.mailmap`) and GitHub logins.

## Decision

- **Only `stillhere sync github` uses the network.** It lists the organization's repositories and keeps bare mirrors current. It also pulls pull requests, reviews, review comments, labels, and pull-request-to-commit links into `facts.db` through the GraphQL API. Sync is incremental by `updated_at`, with idempotent upserts keyed by node id.
- **Mirrors are bare clones, not `--mirror`.** `--mirror` would copy every ref the server advertises, including pull-request refs. Each mirror instead uses an explicit `refs/heads/*` refspec and `fetch --prune`, and sync re-points its `HEAD` at the default branch the API reports, because a fetch never moves a bare repo's `HEAD`. Shallow and partial clones are never used. Details: [spec/git-invocation.md](../spec/git-invocation.md#sync-commands-network).
- **`scan` and everything after it stay offline and deterministic.**
- **Credentials** come from a GitHub App installation (team server) or `GITHUB_TOKEN` / `gh auth token` (laptop). They are passed to git per command and never written to a remote URL, git config, log, report or page.
- **Identity.** `.mailmap` remains the authority for email aliases. Links between GitHub logins and emails come from GitHub itself: commit-author associations in the API (during sync) and the signed-in user's verified emails (at sign-in). There is no hand-maintained people table. An unlinked login is shown as `@login`.
- **GitHub is the system of record** for pull requests, reviews and teams. `facts.db` holds a read-only replica stamped with a sync watermark, and every page shows "as of" that time.

## Alternatives considered

- **git only** (the 2026-10-07 plan): no review credit.
- **GitHub calls inside `scan`**: the network inside the analysis path makes runs non-deterministic and harder to trust.
- **Webhooks**: fresher data, but they need a public endpoint. Later, if anyone needs freshness under an hour.

Prior art, not reused: Apache DevLake (Go; collects GitHub pull request and review data into MySQL with Grafana dashboards) is a platform to operate, not a library. github-to-sqlite (Python) and GrimoireLab's Perceval (Python) are in another language. Any of them would also need a translation layer onto our facts, and the slice we need is small. ghorg remains the recommended way to clone a whole organization onto a laptop. It is not used for server mirrors, because token handling and the repo list must match the sync exactly.

## Consequences

- Review credit, pull-request grouping and labels become available, and analysis stays offline and reproducible.
- GitHub only in v1. The `ForgeSource` port leaves room for GitLab.
- Rate limits (5,000 GraphQL points per hour, plus secondary limits) set the pace of backfill.
- An org admin must install the GitHub App for the team server.

## Door

Two-way: the adapter sits behind the `ForgeSource` port.

## Revisit if

A team on GitLab wants it (add an adapter), or data must be fresher than one hour (add webhooks).
