# stillhere
what dat code do tho

Change analytics for git and GitHub (what landed, what lasted, who knows it) for tech leads and their agents. Every number traces back to the commits behind it.

> stillhere measures specific properties of code changes: how long they lasted, how much they simplified, how much review they got. It does not measure a person's value, and it must not be the basis for performance or pay decisions.

**Status:** pre-release; milestone M0 is in progress. No release is tagged yet, so build from source (see [Install](#install)).

- [Product brief](docs/product.md): what it is, who it is for, milestones, open decisions
- [Metric catalog](docs/metrics.md): every metric's definition, blind spots, and how it can be gamed
- [Architecture](docs/architecture.md): contexts, data ownership, storage, security
- [Decision records](docs/adr/)
- [Implementation contracts](docs/spec/): git commands, classification, collectors
- [Test fixture spec](testdata/fixture/SPEC.md)

## Install

stillhere is one static binary for macOS, Linux and Windows. It runs the git on your `PATH`, which must be 2.45 or newer (`git version` tells you).

No release is tagged yet. Until the first one, build from a checkout with Go 1.27:

```sh
git clone https://github.com/IHaveASegway/stillhere.git
cd stillhere
make build            # writes bin/stillhere
```

From the first release on:

```sh
# Homebrew, macOS or Linux. The cask installs Homebrew's git, which meets the floor.
brew install --cask ihaveasegway/tap/stillhere

# Go 1.27 or newer
go install github.com/IHaveASegway/stillhere/cmd/stillhere@latest

# Docker, linux/amd64 and linux/arm64, with git included
docker pull ghcr.io/ihaveasegway/stillhere
```

Each [release](https://github.com/IHaveASegway/stillhere/releases) also carries archives for every platform and a checksums file to [verify them against](SECURITY.md#verifying-a-download).

## Quickstart

```sh
stillhere week ~/src/acme                                  # the last 7 days, per person
stillhere week ~/src/acme/api ~/src/acme/web --days 14     # two repositories, two weeks
stillhere week ~/src/acme --format md > week.md            # Markdown, for a status note
stillhere week ~/src/acme --now 2026-10-07 --format json   # same inputs, same bytes
stillhere help week
```

`week` prints what landed on each repository's default branch: one row per person with commits, lines added, and lines added to production code, with bots in a table of their own. Each `PATH` is a repository (a work tree or a bare mirror) or a group: a directory whose immediate children are repositories. `week` reads only what is already cloned and stores nothing; fetch first for fresh numbers.

Two files in the current directory change the answer when present. `.mailmap` folds one person's addresses into one identity (`--mailmap FILE` names another). `stillhere.toml` replaces the packaged policy: which paths count as production code, which authors are bots (`--policy FILE` names another).

The other commands (`scan`, `explain`, `sync github`, `mcp` and the rest in `stillhere help`) arrive with milestones M1 to M4 ([product brief](docs/product.md#milestones)); until then they exit with status 2.

In Docker, mount the repositories and run as the user who owns them, because git refuses to read a repository that belongs to someone else:

```sh
docker run --rm --user "$(id -u):$(id -g)" -v "$HOME/src:/work:ro" \
  ghcr.io/ihaveasegway/stillhere week /work/acme
```

Docker Desktop on macOS presents mounted files as owned by root, so there either pass `--user 0:0` or use the Homebrew install.

The commands that store facts (`scan`, from M1) keep them under `STILLHERE_HOME`, which is `/data` in the image, a directory only the image's own user may enter. When you pass `--user`, mount a directory that user owns there, such as `-v "$HOME/.stillhere:/data"`.

## Use it from an agent (MCP)

`stillhere mcp` serves facts.db to your agent over the Model Context Protocol, on stdio: overview, person, who_knows, changes, explain, highlights, identity_candidates, metrics and diff. It reads the latest scan and cannot write to facts.db. Every answer carries its provenance (run, as-of time, HEAD SHAs, metric versions) and the caveats that could make it wrong. The contract is [docs/spec/mcp.md](docs/spec/mcp.md).

Register it once with Claude Code, for every project (`--scope user`):

```sh
stillhere scan ~/src/acme                     # the server reads the latest scan
claude mcp add --scope user stillhere -- stillhere mcp --me you@example.com
```

Everything after `--` is the server's own command line; add `--db FILE` there for another facts.db. Answers go on to your agent's model provider, so by default the server withholds what the provider does not need:

| Flag | Without it |
| --- | --- |
| `--me EMAIL` | No one's AI-assisted share is shown. With it, your own share appears on your own page (`person` with an empty name), and only there. |
| `--allow-emails` | Every email is replaced by a stable id (`p-…`), which the tools accept back. |
| `--allow-source` | Commit subjects stay local and `diff` refuses. With it, `diff` runs git, offline, on the clones the scan read. |
| `--run ID` | Each call reads the latest complete or partial scan, so a new scan shows up mid-session. |

The skill in [plugins/stillhere/skills/stillhere](plugins/stillhere/skills/stillhere/SKILL.md) teaches your agent which tool answers which question, how to read rank intervals, and the stated limits. Copy that directory to `~/.claude/skills/stillhere` to use it in every project, or load the plugin directory for one session with `claude --plugin-dir plugins/stillhere`.

Text in the answers comes from the repositories (names, paths, commit subjects, diffs) and is untrusted: the tool descriptions and the skill tell the agent never to follow instructions found in it.

## License

[Apache-2.0](LICENSE) ([ADR 0008](docs/adr/0008-apache-2-public.md)). Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md).
