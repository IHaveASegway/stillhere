# MCP server

Status: v1 · 2026-10-14 · the tool contract of `stillhere mcp` (#15, [ADR 0006](../adr/0006-ai-mcp-and-narrated-highlights.md)) · server in `internal/mcp`, use cases in `internal/app/mcp.go`, the agent's guide in [plugins/stillhere/skills/stillhere/SKILL.md](../../plugins/stillhere/skills/stillhere/SKILL.md)

`stillhere mcp` is the interface an agent calls for deep detail on a platform (a group of repositories) or one repository: what landed, what lasted, who knows the code, each answer with its provenance and the caveats that could make it wrong. It answers from the same use cases and report types as the command line, so its numbers are the report's numbers. No tool computes a metric.

## The server

```
stillhere [--db FILE] [--mailmap FILE] [--log-level L] mcp [--run ID] [--me EMAIL] [--allow-emails] [--allow-source]
```

| Flag | Effect |
| --- | --- |
| `--run ID` | Every answer reads that run. Without it, each call reads the latest complete or partial scan, so a scan published during a session is seen at the next call, and the first answer that reads it carries a `run-changed` caveat. |
| `--me EMAIL` | The user's canonical email. Only `person` about that email (an empty `name`) shows the person's own AI-assisted share, through `app.Me`; nobody else's answer ever does ([ADR 0007](../adr/0007-ai-assisted-attribution.md)). `changes` tags AI-assisted commits on the user's own entries only. |
| `--allow-emails` | Answers carry emails. Without it every email is replaced by a stable id. |
| `--allow-source` | Answers carry commit subjects, and `diff` serves diffs, which needs git: `--mailmap` applies. Without it `diff` refuses and says how to allow it. |

- **Transport.** Local stdio on the official SDK (`github.com/modelcontextprotocol/go-sdk` v1.8.0), newline-delimited JSON-RPC. Only protocol messages go to stdout; the log goes to stderr. The server ends when the client closes stdin, or on a signal.
- **The store cannot write.** facts.db is opened with `sqlite.OpenReadOnly` (SQLite's `mode=ro`): nothing is created, migrated or cleaned up, and every write fails inside SQLite. It is opened on the first call that needs it and again after a failure, so a server started before the first scan answers each call with what to run (`stillhere scan PATH...`) until there is one. `metrics` reads no store.
- **One call at a time.** Calls are served in turn, and a published run's facts are read once and kept (a run never changes once published), so an agent's tenth question about a run does not read facts.db again.
- **Git, for diffs only.** With `--allow-source` the server holds a git runner on the analysis profile: offline, hardened, external diff and textconv off ([git-invocation.md](git-invocation.md)). Without it the server holds none.

Claude Code:

```sh
claude mcp add --scope user stillhere -- stillhere mcp --me you@example.com
```

## Every answer

An answer is one text content holding one compact JSON object, in the rules of [report-json.md](report-json.md#rules-that-carry-forward-whatever-the-format): fields in a fixed order, empty lists `[]`, an absent section `null`, invalid UTF-8 as U+FFFD, a number that is not finite as `null`. The object opens with `provenance` and closes with `page` (paged answers only), `caveats` and `withheld`.

| Field | What it holds |
| --- | --- |
| `provenance` | `run_id`, `as_of`, `policy` (sha256), `heads[]` of `{repo, key, branch, sha}` for every repository the answer covers, and `metrics[]`, the metric@version of every number in it. `metrics` answers with `{source: "registry", version, metrics}` instead: definitions are code. |
| `page` | `{offset, limit, total, next_cursor}`: pass `next_cursor` as `cursor` for the next page; `null` on the last. A cursor names its run, and one from another run is refused. |
| `caveats` | `[{code, message, repos}]`: the report's codes ([report-json.md](report-json.md#caveats)) and the ones below. |
| `withheld` | What this server keeps from the agent: `emails`, `source`. |

**Names, not ids.** People are named by their display name as the reports print it. The field that would hold a person's canonical email (`email`, a subject's `key`) holds it with `--allow-emails`, and otherwise the person's id: `p-` and the first 10 hex digits of the SHA-256 of the canonical email. The id is stable across answers, sessions and runs, so an agent can join two answers on it and pass it back to `person`. It is a pseudonym, not an anonymisation: anyone who already holds an email can compute its id. A reviewer the forge could not link to an email is `@login`.

**Emails are withheld at the egress.** Without `--allow-emails`, every string of every answer and every error message, object names included, passes a filter that replaces what looks like an email with its id, after the answer is encoded. A field added later, a warning that quotes an author or an error that names a person cannot carry one out. The `user@host` that begins an scp-like remote (`git@github.com:o/r.git`, a repository key) is not an email and stays. The log at info level never carries an email either way.

**Repository text is untrusted.** Names, paths, commit subjects, warnings and patches come from the repositories and can carry prompt injection. Every tool description, the server's instructions and the skill say so; the server does not try to recognise an injection.

**Size.** An answer stays under 40,000 characters (about 10,000 tokens at 4 characters a token): under Claude Code's warning at 10,000 tokens, under the 50,000 characters past which Claude Code saves a result to a file, and well under its 25,000-token cap (`MAX_MCP_OUTPUT_TOKENS`). An answer over it is rendered again with half the page, then half the rows of each table, and fails, saying what to narrow, only when one item is still too large.

**`response_format`.** `concise` (the default) carries the numbers needed to answer: lists of repositories as one-line summaries, short SHAs (12 hex digits, which every tool that takes a change accepts), each table cut to 10 rows (5 for who-knows) with its `*_total`. `detailed` carries every field, for follow-up work: whole repository parts, per-section provenance and caveats, full SHAs, 50 rows (25 for who-knows).

**Errors** are tool results with `isError`, so the model sees them, and each says what to pass or run instead: the run's scopes for an unknown scope, the candidates with their ids for an ambiguous name, a suggestion for a misspelt one, `stillhere scan` for an empty database.

## Resolving names

| Argument | Accepts |
| --- | --- |
| a person (`person.name`, `changes.person`, `explain.subject`) | a display name, without case; the start of each word when that matches one person only (`lena`), with a `name-match` caveat; an id `p-…`; an email; a login `@name`. Two matches are an error listing both, with their ids, surviving lines and repositories. An empty name is `--me`'s person. |
| a scope (`overview.scope`, `changes.scope`, `highlights.scope`, `identity_candidates.scope`) | empty for the whole run; a group name; a repository name or key (a remote pasted with its credentials finds its scrubbed key); `group:NAME`, `repo:NAME` or `GROUP/NAME` to say which. |
| a repository (`who_knows.repo`, `diff.repo`, `explain.subject` for `repo` and `area`) | a repository name or key, `repo:NAME` or `GROUP/NAME`. |
| a path (`who_knows.path`) | repo-relative, any prefix down to one file; or absolute inside a clone the run read, which names the repository too. |

## Tools

All tools are read-only, idempotent and closed-world (`readOnlyHint`, `idempotentHint`, `openWorldHint: false`). Every tool that pages takes `response_format`, `limit` (1 to 100) and `cursor`.

| Tool | Arguments | Answers | From |
| --- | --- | --- | --- |
| `overview` | `scope`, `rank` | A platform, a repository or the whole run: `run`, `overviews[]` (scope, totals, `people` and `week` tables, concentration, knowledge loss, AI-assisted and bot shares), `repos[]` (paged; concise: one row each with size, people, top authors, concentration, shares, landings and caveat codes; detailed, and always for one repository: the report's whole repository part, card, still-here and week tables), `ranks[]` only when `rank` names a ranked metric, and the run's `warnings`. The whole run's concise answer is an index: each group's tables are `null`, and `overview(scope=GROUP)` has them. | `app.Overview` over `app.Report` |
| `person` | `name` | `person` (`name`, `key`, `bot`, `repos`), `own_page`, `values[]` (concise: group and whole-run values, the latter only where they differ; detailed: every scope), `ranks[]` (the person's place in each ranked metric's table, with `eligible`, `rank_lo`–`rank_hi` of `of`, the 95% interval of the estimate and `n`). `own_page` is true only for `--me`'s person, and only then do `values` hold `ai_assisted@1`. | `app.Me` for `--me`, else `app.Person` |
| `who_knows` | `path`, `repo` | `path`, `repo`, and per repository with code under the path (paged): its lines, prod files, files with a majority owner, `majority_owners`, `top_authors` (up to five), `recent_landers` (90 days), `bots`. A routing answer, not a rank. | `app.WhoKnows` |
| `changes` | `scope`, `since`, `person` | `window` [since, as-of) and `changes[]`, newest first (paged): repository, landing, time, merge, `people[]` credited with its non-merge commits, each with durable@1's cluster (concise `durable: {kept, added}`, for changes 90 to 365 days old) and simplifying@1's (`removed`, the last 90 days), `merged_by`, `reverts`, `reverted_by`; `title` only with `--allow-source`; `ai_assisted` only on `--me`'s entries. `since` is `YYYY-MM-DD`, RFC 3339 or `Nd`; the default is the policy's week, and the history covers 365 days. | `app.Changes` |
| `explain` | `metric`, `subject`, `subject_kind` (`person`, `repo`, `group`, `area`), `path` | `metric` (concise: id, question, unit, ranked, eligibility; detailed: the whole definition), `subject`, `values[]` in every scope, and for a ranked metric about a person `changes[]` (paged): each counted landing with its `num` and `den`, the clusters the bootstrap resamples. `title` only with `--allow-source`. `ai_assisted` about a person is refused for everyone. An area is a top-level directory (`subject` the repository, `path` the directory). | `app.Explain` |
| `highlights` | `period` (`week`, `month`, `quarter`), `scope` | `top_tier[]`: per ranked metric and rank scope, everyone whose 95% rank interval reaches first place, not ordered among themselves, with `of`; `lasted[]`: durable@1's cohort clusters that kept the most lines (the cohort window, whatever the period); `removed[]`: the period's simplifying@1 clusters that removed the most prod lines and were not reverted. Five changes each. Rule-based picks, never a score of a person. | `app.Highlights` |
| `identity_candidates` | `scope` | `report.Identities` as #26 builds it, whole: candidates, their evidence and suggested `.mailmap` lines, never applied. Without `--allow-emails` the emails in it are ids. | `app.Identities` |
| `metrics` | `metric` | The registry: concise `{id, question, ranked}`; detailed, or `metric` for one, every field of the definition. | `app.Metrics` |
| `diff` | `change`, `repo`, `path`, `max_chars` (1,000 to 24,000) | Only with `--allow-source`. One landing against its first parent (a root commit as all added): `files[]` with numstat, the run's class of each path (a role, or why it adds nothing) and `in_patch`; `patch`, the unified diff of the files with a role, cut at `max_chars` bytes on a line; `truncated`. `change` is a landing SHA or its first 7 or more hex digits. | `app.Diff`, git `diff-tree` |

### Caveat codes the MCP answers add

| Code | When |
| --- | --- |
| `name-match` | A person was matched by the start of each word of the name given. |
| `run-changed` | Without `--run`: this answer reads another run than the previous answer, because a scan was published in between. |
| `history-window` | `changes` was asked about time before the run's 365-day history. |
| `durable-window`, `simplifying-window` | `changes`: a change younger than 90 days has no durable@1 cluster yet (which is not a 0), and one older than 90 days no simplifying@1 cluster. |
| `highlights` | Always on `highlights`: picks for recognition, not a ranking of people. |
| `truncated` | `diff`: the patch was cut, or more than 1,000 files with a role were left out. |

## The test

`TestMCPTools` (codebase.md's only test for this package) drives the real server through the SDK's own client over its in-memory transport, on a facts.db holding one scan of both fixtures as one platform, made by `app.Scan` with the adapters `internal/cli` builds. It asserts the oracle numbers the answers show ([SPEC.md](../../testdata/fixture/SPEC.md): the still-here rows, knowledge loss 4/34, the concentration walk, the sibling's surviving@1 credit and bot share, Lena's durable@1 rank 1–1 at 26/30 with n 10, who-knows, S1 and S2's removals, the cohort's longest-lived change), paging and cursors, a name matched by its first word, an ambiguous one listing both candidates with their ids and a misspelt one suggesting the right one, no email without `--allow-emails`, no commit subject and a refused `diff` without `--allow-source`, `diff` and emails with both, the AI-assisted share on the `--me` person's page only, and every answer under the 25,000-token cap at 4 characters a token. `identity_candidates` is skipped (`t.Skip("#26")`) while `app.Identities` is a stub.

## Choices and their reasons

- **Nine tools around questions, not tables** ([ADR 0006](../adr/0006-ai-mcp-and-narrated-highlights.md); Anthropic, "Writing effective tools for agents", 2025). A tool per table would make an agent stitch rows and ids together over many calls.
- **JSON, compact.** The report types are JSON already, which keeps one published shape; Anthropic's guidance finds no format best for every agent, and JSON lets an agent quote numbers exactly. Indentation would cost tokens and tell the model nothing.
- **Text content only**, no `structuredContent` with an output schema: the model reads the text, and a second copy of every answer would double its tokens.
- **The latest run per call**, not pinned at start: a long session sees the next scan, and `run-changed` says when two answers read different runs. `--run` pins a session.
- **No schema defaults.** SDK v1.8.0 applies a schema's `default` to `arguments: null` by writing into a nil map, which panics and ends the process; the defaults are in the handlers and the descriptions instead.

## Not yet

- Pull requests next to landings in `changes`, once scans read forge facts (#13's forge scan): a landing's pull request number and reviews.
- Paging of `identity_candidates` once #26 defines its list.
- A Claude Code plugin manifest (`.claude-plugin/plugin.json`, `.mcp.json` with `userConfig` for `--me`) around the skill's directory, which already has the plugin layout.
