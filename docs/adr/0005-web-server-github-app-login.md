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
