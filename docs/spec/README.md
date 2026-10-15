# Implementation specs

Status: carried over from the 2026-10-07 plan, updated for the 2026-10-14 decisions · each row names the milestone whose tests encode it

These are the contracts the code must meet: exact git commands, parsing rules, and the traps that make a naive implementation wrong. Each trap is stated once, in the file that owns it, and linked from elsewhere. Product intent lives in [../product.md](../product.md), metric definitions in [../metrics.md](../metrics.md), system design in [../architecture.md](../architecture.md). When a spec and the fixture disagree, the fixture oracle wins and the spec is fixed.

Behaviour marked "verified" was checked on Git 2.50.1 (Apple Git-155) on 2026-10-14. CI re-checks it on the floor version ([git-invocation.md](git-invocation.md#version-floors)).

| File | Covers | Tests land in |
| --- | --- | --- |
| [codebase.md](codebase.md) | Package map, the dependency rule, domain types, ports, signatures between packages, run model, commands, conventions, the test list | Every milestone |
| [git-invocation.md](git-invocation.md) | The one git adapter: hardening flags, environment, version floors, offline analysis vs network sync, credentials, URL scrubbing, identity (mailmap) and bots, logging | M0 (analysis), M2 (sync) |
| [discovery.md](discovery.md) | Which paths are repos or groups, bare mirrors, duplicates, default branch, shallow and partial clone detection | M0 |
| [classification.md](classification.md) | Ordered path classifier, RE2 deny list, `.gitattributes`, header sniff, roles; weights and gearing (open decision #2) | M0 (week predicate), M1 (blame roles) |
| [week-collector.md](week-collector.md) | What landed: first-parent log, numstat and rename parsing, `lines_added`, co-author trailers and AI-assisted tags, subjects, reverts, landing → commit map | M0 (M1 for reverts and the landing map) |
| [blame-collector.md](blame-collector.md) | What is still here: tree listing, blame flags, porcelain parsing, per-repo ignore-revs, persisted per-commit rows, majority owner, concentration walk | M1 |
| [cache.md](cache.md) | Blob-level blame reuse and its invalidation | Deferred, open decision #3 |
| [hotspots-code-maat.md](hotspots-code-maat.md) | Hotspot facts: code-maat jar design vs in-process Go | M5, open decision #1 |
| [report-json.md](report-json.md) | Draft JSON shape, warning and error semantics, rollups, timings, exit codes | M0 (draft), superseded in M1 |
| [mcp.md](mcp.md) | The MCP server's tools: arguments, answers, names, privacy, paging and the token budget | M4 |
| [../../testdata/fixture/SPEC.md](../../testdata/fixture/SPEC.md) | The fixture repository and every oracle number | M0 onward |
