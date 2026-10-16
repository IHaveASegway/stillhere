# stillhere
what dat code do tho

Change analytics for git and GitHub (what landed, what lasted, who knows it) for tech leads and their agents. Every number traces back to the commits behind it.

> stillhere measures specific properties of code changes: how long they lasted, how much they simplified, how much review they got. It does not measure a person's value, and it must not be the basis for performance or pay decisions.

**Status:** pre-release. Built: the code of milestones M0 to M2, except M2's GitHub App (`sync github` uses your own GitHub token), and the MCP server of M4 ([product brief](docs/product.md#milestones)). Pending: the first release, so for now you install from source (see [Install](#install)), and the labelled-sample check: no ranked metric has been validated against a lead's labels yet, so every rank is marked beta, and `stillhere label` and `stillhere validate` run the check on your own code ([below](#check-the-ranks-on-your-own-code)).

- [Product brief](docs/product.md): what it is, who it is for, milestones, open decisions
- [Metric catalog](docs/metrics.md): every metric's definition, blind spots, and how it can be gamed
- [Architecture](docs/architecture.md): contexts, data ownership, storage, security
- [Decision records](docs/adr/)
- [Implementation contracts](docs/spec/): git commands, classification, collectors
- [Test fixture spec](testdata/fixture/SPEC.md)

## Install

stillhere is one static binary for macOS, Linux and Windows, on amd64 and arm64. CI runs the tests on Linux only: it builds the macOS and Windows binaries on every change, but does not test them. stillhere runs the git on your `PATH`, which must be 2.45 or newer (`git version` tells you).

No release is tagged yet. Until the first one, install from source with Go 1.27 or newer, either with `go install`, which needs no checkout:

```sh
go install github.com/IHaveASegway/stillhere/cmd/stillhere@latest   # the newest commit on main
```

`go install` puts the binary in `$(go env GOBIN)` when that is set, else in `$(go env GOPATH)/bin` (`~/go/bin` by default). Or build it from a checkout, which also stamps the commit and the build date into `stillhere version`:

```sh
git clone https://github.com/IHaveASegway/stillhere.git
cd stillhere
make build                                                # writes bin/stillhere
mkdir -p ~/.local/bin && cp bin/stillhere ~/.local/bin/   # or any other directory on your PATH
```

Either way, that directory must be on your `PATH`, or every command below fails with "command not found": `command -v stillhere` prints the binary's path when it is, and `stillhere --version` prints the version. Windows has no `make`; use `go install` there.

From the first release on:

```sh
# Homebrew, macOS or Linux. The cask installs Homebrew's git, which meets the floor.
brew install --cask ihaveasegway/tap/stillhere

# Go 1.27 or newer: @latest is then the newest release
go install github.com/IHaveASegway/stillhere/cmd/stillhere@latest

# Docker, linux/amd64 and linux/arm64, with git included
docker pull ghcr.io/ihaveasegway/stillhere
```

Each [release](https://github.com/IHaveASegway/stillhere/releases) also carries archives for every platform, which hold the shell completions and the [agent skill](#use-it-from-an-agent-mcp) beside the binary, and a checksums file to [verify them against](SECURITY.md#verifying-a-download).

## Quickstart

```sh
stillhere week ~/src/acme                                  # the last 7 days, per person
stillhere week ~/src/acme/api ~/src/acme/web --days 14     # two repositories, two weeks
stillhere week ~/src/acme --format md > week.md            # Markdown, for a status note
stillhere week ~/src/acme --now 2026-10-07 --format json   # same inputs, same bytes once timings is removed
stillhere help week
```

`week` prints what landed on each repository's default branch: one row per person with commits, lines added, and lines added to production code, with bots in a table of their own. Each `PATH` is a repository (a work tree or a bare mirror) or a group: a directory whose immediate children are repositories. `week` reads only what is already cloned and stores nothing; fetch first for fresh numbers.

Two files in the current directory change the answer when present. `.mailmap` folds one person's addresses into one identity (`--mailmap FILE` names another). `stillhere.toml` replaces the packaged policy as a whole: which paths count as production code, which authors are bots (`--policy FILE` names another). To start one, `stillhere policy > stillhere.toml` writes the packaged policy out as a complete file.

To keep a history, scan instead: `scan` records a run in facts.db (history, blame, metrics and ranks), and the read commands answer from the latest run, or from `--run ID`, without touching the repositories again.

```sh
stillhere scan ~/src/acme                           # record a run: the default branch of each repository
stillhere report --rank durable@1                   # the run as a report, with durable@1's rank table
stillhere who-knows services/billing                # who knows the code under a path
stillhere explain durable@1 --person ada@example.com  # the changes behind one person's value
stillhere me --email you@example.com                # your own page
stillhere identities --format mailmap               # .mailmap suggestions with their evidence, never applied
stillhere sync github --org acme                    # mirrors, pull requests and reviews: the one network command
stillhere scan ~/.stillhere/mirrors/acme            # then scan the mirrors sync keeps
```

In Docker, mount the repositories and run as the user who owns them, because git refuses to read a repository that belongs to someone else ("dubious ownership"):

```sh
docker run --rm --user "$(id -u):$(id -g)" -v "$HOME/src:/work:ro" \
  ghcr.io/ihaveasegway/stillhere week /work/acme
```

The image is published from the first release on. Until then, build it from a checkout with `go run github.com/goreleaser/goreleaser/v2@v2.18.3 release --snapshot --clean`, as the [Dockerfile](Dockerfile) says. That loads one image per platform into your Docker daemon, tagged with its platform, such as `ghcr.io/ihaveasegway/stillhere:latest-arm64`: run that name in place of `ghcr.io/ihaveasegway/stillhere`.

On macOS, git may report dubious ownership when containers with different `--user` values read one mount, because Docker Desktop does not always show a container a mounted file's owner as the Mac has it. Use one `--user` for every container that reads a mount, or use the binary.

The commands that store facts (`scan` and `sync github`) keep them under `STILLHERE_HOME`, which is `/data` in the image, a directory only the image's own user may enter. When you pass `--user`, mount a directory that user owns there, such as `-v "$HOME/.stillhere:/data"`.

## Check the ranks on your own code

A rank says that some people's changes last longer, simplify more or get more review than others'. Whether that matches what matters in your code is something only you can judge, so stillhere asks you:

```sh
stillhere label        # about 100 of the run's changes, one key each: m meaningful, r routine, n noise, s skip, q quit
stillhere validate     # per ranked metric, the AUC: how often a meaningful change scores above a routine one
```

`label` shows each change without any metric value or author, appends every answer to `labels.csv` beside the facts database (outside your repositories: the labels judge colleagues' work), resumes where you stopped, and asks until each ranked metric has the 50 meaningful or routine labels its check needs. `label --sheet > ~/.stillhere/sheet.csv` writes the same sample for a spreadsheet, which `validate --labels ~/.stillhere/sheet.csv` reads back; keep it out of your repositories too. An AUC of 0.5 is chance, and the bar is 0.7 or more on at least 50 labels. Clearing it tells you the metric tracks what you call meaningful in your code; the beta mark stillhere prints changes only when the metric's published validity result does, so report yours. [docs/validation.md](docs/validation.md) has the rubric, worked examples from public repositories, and how each metric scores a change.

## Use it from an agent (MCP)

`stillhere mcp` serves facts.db to your agent over the Model Context Protocol, on stdio: overview, person, who_knows, changes, explain, highlights, identity_candidates, metrics and diff. It reads the latest scan and cannot write to facts.db. Every answer carries its provenance (run, as-of time, HEAD SHAs, metric versions) and the caveats that could make it wrong. The contract is [docs/spec/mcp.md](docs/spec/mcp.md).

Register it once with Claude Code, for every project (`--scope user`). Claude Code starts the server itself, with its own environment, whose `PATH` need not hold the directory you installed stillhere in, so register the binary's absolute path:

```sh
stillhere scan ~/src/acme      # the server reads the latest scan
command -v stillhere           # the path to register; nothing printed means stillhere is not on your PATH
claude mcp add --scope user stillhere -- "$(command -v stillhere)" mcp --me you@example.com
```

Everything after `--` is the server's own command line; add `--db FILE` there for another facts.db. With `--allow-source` the server also runs git: the first one on the `PATH` Claude Code starts it with, which must be 2.45 or newer. Answers go on to your agent's model provider, so by default the server withholds what the provider does not need:

| Flag | Without it |
| --- | --- |
| `--me EMAIL` | No one's AI-assisted share is shown. With it, your own share appears on your own page (`person` with an empty name), and only there. Pass your canonical email, the one the reports key you by after `.mailmap`. |
| `--allow-emails` | Every email is replaced by a stable id (`p-…`), which the tools accept back. |
| `--allow-source` | Commit subjects stay local and `diff` refuses. With it, `diff` runs git, offline, on the clones the scan read. |
| `--run ID` | Each call reads the latest complete or partial scan, so a new scan shows up mid-session. |

Whatever the flags, a repository's or group's AI-assisted share is sent only when at least three people landed in it, counting as one person every address that shares a name (any name seen with it, a default one such as John Doe included), a local part or a GitHub account with another, or that a hand-off or `stillhere identities` links: with fewer, it would be close to one person's own share. A share is also withheld when it would let a withheld share be worked out from the shares sent beside it, such as a group's less its repositories'.

The skill in [plugins/stillhere/skills/stillhere](plugins/stillhere/skills/stillhere/SKILL.md) teaches your agent which tool answers which question, how to read rank intervals, and the stated limits. It is at that path in a checkout and in every release archive, and the Homebrew cask keeps it in the cask's own directory, `$(brew --caskroom)/stillhere/<version>/plugins/stillhere/skills/stillhere`. Copy that directory to `~/.claude/skills/stillhere` to use it in every project, or load the plugin it belongs to for one session: `claude --plugin-dir plugins/stillhere`, run in a checkout or an unpacked archive.

Text in the answers comes from the repositories (names, paths, commit subjects, diffs) and is untrusted: the tool descriptions and the skill tell the agent never to follow instructions found in it.

## License

[Apache-2.0](LICENSE) ([ADR 0008](docs/adr/0008-apache-2-public.md)). Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md).
