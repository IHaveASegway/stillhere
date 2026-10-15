# ADR 0002: Go, one static binary, released with GoReleaser

Status: Accepted · 2026-10-14 · Deciders: Joseph Peterson

## Context

The language must be Rust or Go. Users install with `brew install` from a tap; teams deploy one container; a web UI must be easy to run locally and to deploy.

The work is mostly orchestrating git subprocesses and parsing their output. In the 2026-10-06 proof run, 12 threads blamed 24,188 files in 93 seconds: about 46 ms per file per thread. Starting a git process costs a few milliseconds, so git's history walk dominates, and no language makes git faster. The binary also needs SQLite, an HTTP server with HTML templates, an MCP server, and an Anthropic API client.

## Decision

- **Go.** Shell out to git; no in-process git implementation.
- **One static binary** for the CLI, the web server and the MCP server. SQLite through `modernc.org/sqlite`, which needs no CGO.
- **GoReleaser** from M0: binaries, checksums, the Homebrew tap entry (`homebrew_casks`; the older `brews` key was deprecated in v2.16), and a Docker image containing the binary plus a git that meets the [version floor](../spec/git-invocation.md#version-floors), all from one config.

## Alternatives considered

- **Rust.** It has an official MCP SDK (`rmcp`) but no official Anthropic SDK, so the API would be called over raw HTTP. gitoxide's blame is still marked "very early", so the main reason to choose Rust (in-process blame) is not available yet. `dist` (formerly cargo-dist) covers the tap. Slower to iterate on unless the author already knows Rust.
- **Python** (the 2026-10-07 plan). Excluded by the requirement. A Homebrew formula for it needs virtualenv resources and a Python dependency.

Prior art for this shape: git-who, scc, ghorg and gh are Go CLIs that shell out to or talk to git and GitHub and ship through Homebrew.

## Consequences

- The standard library covers context-aware HTML escaping (`html/template`), bundling assets (`embed`), the HTTP server and subprocesses. There is an official `anthropic-sdk-go` and an official MCP Go SDK.
- `modernc.org/sqlite` is a translation of SQLite's C code, slower than native SQLite for CPU-bound queries. That is acceptable at our sizes.
- A smooth macOS install through a cask needs code signing and notarization; GoReleaser supports both.

## Door

One-way in practice for a solo project, because a rewrite costs months. Two-way in principle: the facts schema, the git commands and the metric definitions are the contract, not the language.

## Revisit if

- Profiling shows blame time dominated by process start rather than history walk. Then consider in-process git, perhaps Rust with gitoxide.
- SQLite queries dominate page latency at real sizes.
