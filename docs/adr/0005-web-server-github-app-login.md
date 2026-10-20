# ADR 0005: Team web server with GitHub App sign-in

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

Leads see ranks, every person sees their own page, and everyone in the organization sees highlights ([ADR 0001](0001-measure-changes-rank-per-metric.md)). A static site cannot show different viewers different pages. The UI must be easy to run locally and to deploy.

## Decision

- **`stillhere serve`**: `net/http`, `html/template` and embedded assets, reading `facts.db` (complete runs only).
- **Sign in with GitHub through a GitHub App.** GitHub's documentation prefers Apps over OAuth apps for their fine-grained permissions. At sign-in, the user's token fetches their verified emails (matched to people after `.mailmap`) and their membership of the configured leads team. The token is then discarded.
- **Sessions** are signed cookies (`HttpOnly`, `Secure`, `SameSite=Lax`) with a short lifetime. Team membership is re-checked at every sign-in and at least hourly.
- **Authorization is deny-by-default.** Leads see ranks. Each person sees their own page. Org members see highlights and code-area views. Emails are never rendered.
- **An audit log** records who opened ranked views.
- **Locally**, `stillhere serve` runs against your own `facts.db` on localhost. **Deployed**, it is one container with TLS in front.

## Alternatives considered

- **A static site with ranks kept local.** No auth code and deployable anywhere, but no per-viewer pages.
- **A static site behind oauth2-proxy with a GitHub team rule.** Little code, but two components to run, and self-access stays in the CLI.
- **Trusted identity headers from a proxy.** Kept as a second `Authenticator` adapter for enterprises that already run oauth2-proxy, Cloudflare Access or similar.

## Consequences

- One container with per-viewer pages and the smoothest experience.
- We own security-sensitive code: the OAuth state parameter, cookie handling and session lifetime. M3 requires tests for each.
- We own a running service: patching, TLS and uptime.
- A GitHub App must be registered, and an org admin must install it.

## Door

Two-way: authentication sits behind the `Authenticator` port, and the proxy adapter can replace the built-in sign-in.

## Revisit if

A security review of the sign-in code finds problems we cannot own well. Then switch to proxy mode.

## Amendment: local mode first

Status: Proposed · 2026-10-19 · Deciders: Joseph Peterson · Amends the order M3 is built in ([product.md](../product.md#milestones), #17)

### Context

The decision above names two modes: locally, `stillhere serve` against your own facts.db on localhost; deployed, one container behind TLS with GitHub App sign-in. #17 waited for the numbers to be used on a real organization and for open decision 4 (how the team server refreshes). The first has happened: the owner scanned an 83-repository organization and asked for pages to look around in, since a report of 3,100 lines answers only the questions its reader already knows to ask. The command line reaches a number's changes only one typed `explain` at a time. The team server still waits on sign-in, the leads team, the audit log, the AI-assisted shares' floor for other viewers (ADR 0007's amendment, product decision 7) and decision 4.

### Decision

- **Build the local mode first, as M3's first slice.** `stillhere serve` serves the person who scanned on the machine they scanned on, which is everything the command line already shows them, AI-assisted shares included: product decision 7 keeps those open at the terminal, and the browser is that terminal's.
- **The file stays the boundary.** The server listens on a loopback address only and refuses any other (`web.CheckAddr`, then the listener's own address). It answers only requests whose `Host` is the address it printed, so a page elsewhere cannot reach it by DNS rebinding. Every page's address starts with a 32-byte random token, a capability URL (W3C TAG, "Good Practices for Capability URLs", 2014): the address it prints is the only credential, so another account on the machine, which cannot read facts.db (0600), cannot read the pages either. It opens facts.db read-only and writes nothing.
- **No cookie.** Jupyter's server trades its token for a cookie on the first request. A browser sends a cookie to every port of its host (RFC 6265, section 8.5), so any other program listening on 127.0.0.1 that the browser is sent to would receive it and could replay it here: ambient authority, which is how a confused deputy happens (Norm Hardy, "The Confused Deputy", 1988). The review of this slice confirmed the replay. In the address, the token goes only where the reader takes it, and the pages send no referrer and load nothing from elsewhere.
- **People by a keyed handle.** A page links a person by a keyed hash of their key under a key the server makes at each start, never by the unkeyed id the MCP server sends (`identity.Pseudonym`), which a guessed email can be checked against: the pages withhold a name that gives an address away, and their links must not give it back. A page asked about a person by email, name or id answers with a redirect to the same page by handle.
- **A page is the report's Markdown.** Each page renders a read use case's answer with `report.Markdown` (or one of its parts: the run, its ranks, a repository), linked by `report.Links`, and goldmark turns it into HTML. So a page and the command line print every number with one renderer, as `internal/metrics` holds one definition of it. Tables sort and filter in the browser, and each one's CSV is its cells as the page shows them.
- **What the team server adds sits in front, not beside.** Sign-in goes behind the `Authenticator` port, in front of the same handlers. The handlers that show ranks and AI-assisted shares are then gated by role, and the shares' floor is applied by reading the run's decision (`app.AIShares`, ADR 0015's record), as the MCP server does.

### Alternatives considered

- **A static HTML file per run (`report --format html`).** No server, but a file is a copy that gets forwarded with every name and AI-assisted share in it and no floor: the case product decision 7 names as what would show it wrong. It has no drill-down either, unless every explain answer is embedded.
- **Datasette over facts.db** (or Metabase, or DuckDB's UI). Nothing to write, but every insight would be worked out again in SQL, a second definition of each number beside `internal/metrics`. The tables also hold emails.
- **A loopback server without a token** (the shape of `go tool pprof -http`). Less code, but any account on the machine could read every page, which the file's mode keeps from them today.
- **The token traded for a cookie** (Jupyter's way). The address leaves the address bar, but the cookie goes to every port of 127.0.0.1 (above).
- **A random host name under `.localhost`** (`http://<random>.localhost:PORT/`), which keeps a cookie to that name. Chrome and Firefox resolve such names to loopback; whether every browser the owner uses does was not checked, and it still trusts a cookie.

### Consequences

- What stays open: the address is the key until the server stops, wherever it is pasted or logged (`serve > file` makes a file other accounts may read), and the browser's history keeps the pages' addresses, repository names and paths, which a synced history sends to its provider. A person's page downloads as CSV with their AI-assisted share in it: a copy the reader makes, as `me` redirected to a file is, which product decision 7 lets the terminal make.
- One more inbound adapter (`internal/web`) and one module, goldmark. The run cache moved from `mcp` to `app` (`CacheRuns`), so both servers read a run once.
- The pages look like a rendered report rather than a designed dashboard: no charts. A page that needs a layout Markdown cannot express renders `report.Model` through `html/template` instead, a second renderer that would then need tests of its own against the first.
- Speed, on the 83-repository run: the first page builds the run's report in 1.4 s, and a repository's page takes 10 ms after that. An explain page takes 0.25 s. A person's page takes 0.4 s, over the 300 ms budget ([architecture.md](../architecture.md#reliability)), because `me` bootstraps every ranked metric and builds the metric views again on every call, as the MCP server's `person` tool does. A per-run memo of those in `app` is the fix, and it would serve both servers.

### Door

Two-way: one command that writes nothing, and handlers the team server reuses.

### Revisit if

The owner, or a lead, still exports CSVs to a spreadsheet after a week of using the pages: the export is the product then, and a typed CSV of the model, not the page's text, is wanted. Or a reader needs to share a page with someone who cannot read the clones: that is the team server's job, with its floor, and no export may stand in for it.
