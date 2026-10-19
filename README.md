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

No release is tagged yet, so install from source. The module needs Go 1.27 or newer, and Go 1.21 or newer fetches 1.27 itself unless `GOTOOLCHAIN` is `local`, as some distributions' packaged Go sets it in `$(go env GOROOT)/go.env` (Fedora's does): then put `GOTOOLCHAIN=auto` before the command. Go 1.20 and older cannot fetch a toolchain. `go install` needs no checkout:

```sh
go install github.com/IHaveASegway/stillhere/cmd/stillhere@main     # the newest commit on main
```

`@main` asks for the branch's newest commit. `@latest` would too, before a tag exists, but through the Go module proxy (proxy.golang.org, the default `GOPROXY`) it resolves to the commit the proxy last saw, which can lag a fresh push by minutes to hours (round 2 got a build six merges behind). To skip the proxy once and fetch the newest commit from GitHub:

```sh
GOPROXY=direct go install github.com/IHaveASegway/stillhere/cmd/stillhere@main
```

When `go env GONOPROXY` (or `GOPRIVATE`, when `GONOPROXY` is unset) matches the module, as a company's `GOPRIVATE=github.com/*` does, `go install` always fetches it that way, with your git (and, under `GOPRIVATE`, without the checksum database), so the first command works unchanged as long as git can read github.com over HTTPS: a `url."git@github.com:".insteadOf` rule in your git config sends it over SSH instead, which needs your key. `stillhere version` prints the commit a binary was built from.

`go install` puts the binary in `$(go env GOBIN)` when that is set, else in `$(go env GOPATH)/bin` (`~/go/bin` by default). Or build it from a checkout, which also stamps the commit and the build date into `stillhere version`:

```sh
git clone https://github.com/IHaveASegway/stillhere.git
cd stillhere
make build                                                # writes bin/stillhere
mkdir -p ~/.local/bin && cp bin/stillhere ~/.local/bin/   # or any other directory on your PATH
```

Either way, that directory must be on your `PATH`, or every command below fails with "command not found": `command -v stillhere` prints the binary's path when it is, and `stillhere --version` prints the version. Windows has no `make`; use `go install` there.

### After the first release (none yet)

None of these works yet: there is no release, so there is no Homebrew cask, no `@latest` release and no published image. Until then, `brew install --cask ihaveasegway/tap/stillhere` adds the tap and fails with `Cask 'stillhere' is unavailable: ... Casks/stillhere.rb does not exist` (and suggests an unrelated cask), `@latest` builds whatever commit the proxy last saw, and `docker pull ghcr.io/ihaveasegway/stillhere` finds no image. Use `go install …@main` or `make build` above.

```sh
# After the first release: Homebrew, macOS or Linux. The cask installs Homebrew's git, which meets the floor.
brew install --cask ihaveasegway/tap/stillhere

# After the first release: Go 1.27 or newer, @latest is then the newest release
go install github.com/IHaveASegway/stillhere/cmd/stillhere@latest

# After the first release: Docker, linux/amd64 and linux/arm64, with git included
docker pull ghcr.io/ihaveasegway/stillhere
```

Each [release](https://github.com/IHaveASegway/stillhere/releases) also carries archives for every platform, which hold the shell completions and the [agent skill](#use-it-from-an-agent-mcp) beside the binary, and a checksums file to [verify them against](SECURITY.md#verifying-a-download).

## Getting started in five minutes

On a clone of your own (fetch it first: stillhere reads only what is cloned):

```sh
cd ~/src/your-repo
stillhere week .                                   # 1. what landed this week, and who wrote it
stillhere scan .                                   # 2. record a run: history, blame, metrics, ranks (seconds to minutes)
stillhere report                                   # 3. read it again: each card says what the code is made of, where
                                                   #    knowledge has gone quiet, and what could make the numbers wrong
stillhere identities --format mailmap >> ~/your.mailmap  # 4. one person, two addresses? review the suggestions,
stillhere --mailmap ~/your.mailmap scan .          #    then scan with them
stillhere who-knows src/payments                   # 5. who holds a path, and who works on it now
stillhere report --rank durable --rank simplifying # 6. ranks, as 95% intervals: only people with 10 or more changes are ranked
stillhere explain durable --person "Your Name"     # 7. the changes behind any number
```

Then, optionally, `stillhere sync github --org your-org` for reviews (review coverage, reviewers, `reviews_given@1`), and `claude mcp add` to [ask from your agent](#use-it-from-an-agent-mcp). [docs/getting-started.md](docs/getting-started.md) walks through every step on a public repository, with its real output and how to read it.

## Quickstart

```sh
stillhere week ~/src/acme                                  # the last 7 days, per person
stillhere week ~/src/acme/api ~/src/acme/web --days 14     # two repositories, two weeks
stillhere week ~/src/acme --format md > week.md            # Markdown, for a status note
stillhere week ~/src/acme --now 2026-10-07 --format json   # same inputs, same bytes once timings is removed
stillhere help week
```

`week` prints what landed on each repository's default branch: one row per person with their landings (a merged branch counts for the people who wrote it), commits, lines added, lines added to production code and the merges they landed, with bots in a table of their own. Text and Markdown print the first 10 rows of each table; `--all` prints every row. Each `PATH` is a repository (a work tree or a bare mirror) or a group: a directory whose immediate children are repositories. `week` reads only what is already cloned and stores nothing; fetch first for fresh numbers.

Two files in the current directory change the answer when present. `.mailmap` folds one person's addresses into one identity (`--mailmap FILE` names another). `stillhere.toml` replaces the packaged policy as a whole: which paths count as production code, which authors are bots (`--policy FILE` names another). To start one, `stillhere policy > stillhere.toml` writes the packaged policy out as a complete file. The packaged policy counts as production code neither example code (`examples/`), benchmarks (`benches/`, `benchmarks/`), tests (`tests/`, and Gradle's test source sets such as `src/jvmTest/` and `src/androidTest/`), documentation tooling (`docs/`, `.vitepress/`) nor build, packaging and CI tooling (`build/`, `scripts/` and a workspace package's `scripts/`, `vite.config.ts`, `setup.py`, `.github/`, Gradle's `build-logic/` and `buildSrc/`, and code under a dot-directory such as `.eslint/` or `.changeset/`), except Go code, which keeps its role under `docs/`, `build/` and `script/` because in Go a directory is a package, and after a scan `stillhere paths` lists every path with its role and the rule that gave it, so you can check what counts on your own layout ([classification.md](docs/spec/classification.md#what-counts-as-product-code-revision-3)).

To keep a history, scan instead: `scan` records a run in facts.db (history, blame, metrics and ranks), and the read commands answer from the latest run, or from `--run ID`, without touching the repositories again.

```sh
stillhere scan ~/src/acme                           # record a run: the default branch of each repository
stillhere report --rank durable@1                   # the run as a report, with durable@1's rank table
stillhere who-knows services/billing                # who knows the code under a path, and when each was last active
stillhere who-knows services/billing --days 365     # with who landed or reviewed changes there in a year, not 90 days
stillhere paths --skipped                           # which paths a run left out, and the rule that decided each
stillhere explain durable@1 --person "Ada Example"  # the changes behind one person's value: a name, @login, id or email
stillhere explain review_coverage --repo api        # each change behind a repository's number: reviewed, not, or left out and why
stillhere me --email you@example.com                # your own page
stillhere identities --format mailmap               # .mailmap suggestions with their evidence, never applied
stillhere sync github --org acme                    # mirrors, pull requests and reviews: the one network command
stillhere sync github --repo acme/api               # or only the repositories you name (repeatable)
stillhere scan ~/.stillhere/mirrors/acme            # then scan the mirrors sync keeps
```

A first `sync github` reads the pull requests updated in the last 365 days, the window every metric reads (`--since DATE` reaches further back), and checks its copy against GitHub's own count, reading the window again if any are missing; a later sync reads what changed. It also asks GitHub which account each author email of the history belongs to, once per email and a hundred emails a request: the evidence `stillhere identities` suggests merges from. On 2026-10-15 a first sync of the `cli` organization (cli/cli has about 1,300 pull requests updated in a year and 732 author emails) took 2 minutes 15 seconds, and of `go-chi`'s 20 repositories 28 seconds. An interrupted sync loses nothing: the next one reads the same range again. Archived repositories and forks of an organization are left out, their mirrors moved to `<mirrors>/.stale`, unless `--include-archived` or `--include-forks`. A repository GitHub renamed or moved to another owner is synced under its new name, and a sync that sees the move (`--repo` with the old name, or a mirror found under it) records it, so your clone whose origin still names the old one gets its GitHub data and its card says so; each later sync asks GitHub about the old name again and forgets it once GitHub resolves it to another repository. A card that says `forge-unmatched` is a clone that shares history with a repository in facts.db but joined none: it names the `sync github --repo` of your origin's name, which records a move or, if your clone is a fork's, syncs the fork. The last line says how the sync went, moves included.

Each mirror gets a commit-graph with changed-path filters (git 2.46 or later; on 2.45, a commit-graph without them), which made `scan`'s blame about three times faster on the same facts. `scan` never writes into your own clones; when blame in one is slow, it logs the command that does the same there: `git -c commitGraph.changedPathsVersion=2 commit-graph write --reachable --changed-paths` (version 2 filters, which read the same on every platform; on git 2.45, `git commit-graph write --reachable`).

One person who committed from several addresses counts as several people, and an old address of someone still active looks like knowledge that left. `report` says so where the numbers are: knowledge loss and concentration carry an `unmerged-identities` caveat with the range (as keyed, with the high-confidence lines merged, with every candidate merged) wherever merging would move them. To merge them, review and add the suggested lines, then scan again; the high-confidence ones, such as two addresses GitHub attributes to one account after `sync github`, are written as lines and the rest commented out. Append (`>>`) to a file outside the repositories: the suggestions leave out a line a scan already applied, so writing over the file would drop the merges an earlier scan made, and a repository's own `.mailmap` is git's, to commit there if you want everyone to get the merge:

```sh
stillhere identities                                         # the candidates, with the evidence for each
stillhere identities --format mailmap >> ~/acme.mailmap      # review, uncomment what you know to be one person
stillhere --mailmap ~/acme.mailmap scan ~/src/acme           # the merges take effect at the next scan
```

Run `identities` again after the first `sync github`: GitHub's links arrive with it and make most of the high-confidence lines. Hand-off guesses are listed last, and in text only with `--all`.

Knowledge loss counts someone as gone quiet after 180 days without a landing, a review, a pull request opened or pushed to, or a commit on any branch or tag of the clone, across every repository of the scan, so a repository scanned alone can read higher than in its organization's scan. Every page shows each holder's last activity, and a card names the people holding lines who are within 30 days of the line ("going quiet within 30 days"), with a `going-quiet` caveat that says what knowledge loss becomes once they cross it.

In Docker, mount the repositories and run as the user who owns them, because git refuses to read a repository that belongs to someone else ("dubious ownership"). The image is published from the first release on (none yet), so this pull fails until then:

```sh
docker run --rm --user "$(id -u):$(id -g)" -v "$HOME/src:/work:ro" \
  ghcr.io/ihaveasegway/stillhere week /work/acme
```

Until the first release, build the image from a checkout with `go run github.com/goreleaser/goreleaser/v2@v2.18.3 release --snapshot --clean`, as the [Dockerfile](Dockerfile) says. That loads one image per platform into your Docker daemon, tagged with its platform, such as `ghcr.io/ihaveasegway/stillhere:latest-arm64`: run that name in place of `ghcr.io/ihaveasegway/stillhere`.

On macOS, git may report dubious ownership when containers with different `--user` values read one mount, because Docker Desktop does not always show a container a mounted file's owner as the Mac has it. Use one `--user` for every container that reads a mount, or use the binary.

The commands that store facts (`scan` and `sync github`) keep them under `STILLHERE_HOME`, which is `/data` in the image, a directory only the image's own user may enter. When you pass `--user`, mount a directory that user owns there, such as `-v "$HOME/.stillhere:/data"`.

## Check the ranks on your own code

A rank says that some people's changes last longer, simplify more or get more review than others'. Whether that matches what matters in your code is something only you can judge, so stillhere asks you:

```sh
stillhere label        # about 100 of the run's changes, one key each: m meaningful, r routine, n noise, s skip, q quit
stillhere validate     # per ranked metric, the AUC: how often a meaningful change scores above a routine one
```

`label` shows each change without any metric value or author, appends every answer to `labels.csv` beside the facts database (outside your repositories: the labels judge colleagues' work), resumes where you stopped, and asks until each ranked metric has the 50 meaningful or routine labels its check needs. `label --sheet > ~/.stillhere/sheet.csv` writes the same sample for a spreadsheet, which `validate --labels ~/.stillhere/sheet.csv` reads back; keep it out of your repositories too. An AUC of 0.5 is chance, and the bar is 0.7 or more on at least 50 labels. Beside each AUC, `validate` prints the AUC of change size alone over the same changes, and under the table each metric's verdict: whether it separates meaningful from routine changes, and whether it beats size, since a metric that does not says no more than "big changes matter". `validate --scores > ~/.stillhere/scores.csv` writes the per-change numbers, to check that yourself; they hold your labels of colleagues' changes, so keep the file out of your repositories too. Clearing the bar tells you the metric tracks what you call meaningful in your code, not that it beats size; the beta mark stillhere prints changes only when the metric's published validity result does, so report yours. Until a lead's labels exist, each ranked metric's rank table, under its question, and its caveat give a reference result from a model panel, with its verdict: three AI models that labelled 143 changes of four public repositories without seeing any metric value, which is evidence, not a lead's judgment ([docs/validation.md](docs/validation.md#reference-results)). [docs/validation.md](docs/validation.md) has the rubric, worked examples from public repositories, and how each metric scores a change.

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

The skill in [plugins/stillhere/skills/stillhere](plugins/stillhere/skills/stillhere/SKILL.md) teaches your agent which tool answers which question, how to read rank intervals, and the stated limits. It is at that path in a checkout and, from the first release on, in every release archive, and the Homebrew cask keeps it in the cask's own directory, `$(brew --caskroom)/stillhere/<version>/plugins/stillhere/skills/stillhere`. Copy that directory to `~/.claude/skills/stillhere` to use it in every project, or load the plugin it belongs to for one session: `claude --plugin-dir plugins/stillhere`, run in a checkout or an unpacked archive.

Text in the answers comes from the repositories (names, paths, commit subjects, diffs) and is untrusted: the tool descriptions and the skill tell the agent never to follow instructions found in it.

## License

[Apache-2.0](LICENSE) ([ADR 0008](docs/adr/0008-apache-2-public.md)). Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md).
