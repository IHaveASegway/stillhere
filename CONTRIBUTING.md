# Contributing to stillhere

Thanks for helping. stillhere turns git and GitHub history into numbers that people will read about each other, so correctness and restraint matter more than speed: every number must trace back to the commits behind it, and every metric states its blind spots and how it can be gamed.

Read these before writing code:

- [docs/spec/codebase.md](docs/spec/codebase.md): the package map, the dependency rule, the signatures between packages, the conventions and the test policy. It is the contract; a change to a signature in it lands in the same commit as the code.
- [docs/product.md](docs/product.md) and [docs/architecture.md](docs/architecture.md): what stillhere is for, what it will not do, and why it is built this way.
- [docs/adr/](docs/adr/): the decisions that are hard to reverse.

Security problems go to a private advisory, never an issue: see [SECURITY.md](SECURITY.md).

## Setup

You need:

- **Go 1.27.** go.mod pins the toolchain (`toolchain go1.27.2`); any Go from 1.21 on downloads it by itself.
- **git 2.45 or newer**, stillhere's version floor ([why](docs/spec/git-invocation.md#version-floors)). Tests run the git on your `PATH`.
- **make**.
- **Docker**, only to dry-run a release.

## The dev loop

| Command | What it does |
| --- | --- |
| `make build` | Builds `bin/stillhere`: static (no cgo), with the version, commit and build date that `stillhere version` prints |
| `make test` | `go test -race -count=1 ./...` against real git |
| `make lint` | golangci-lint v2.14.0, pinned and run from the module cache, including the depguard rules below |
| `make fmt` | gofmt, and goimports with this module as the local prefix |
| `make tidy` | `go mod tidy`, then `go mod verify` |

While working on one package, `go test -race ./internal/<pkg>/...` is faster. On every pull request, CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the lint, then build, vet and test on git 2.45.0 and on the latest git release, govulncheck, and a snapshot release that builds every archive and both images.

To run the tests on the floor version yourself on Debian or Ubuntu, build it the way CI does:

```sh
.github/scripts/git-from-source.sh build 2.45.0 "$HOME/.cache/git-from-source/2.45.0"
PATH="$HOME/.cache/git-from-source/2.45.0/bin:$PATH" make test
```

## The dependency rule

stillhere is ports and adapters (Alistair Cockburn, "Hexagonal Architecture"): dependencies point inward, toward a functional core.

- **Core** (`domain`, `policy`, `classify`, `identity`, `metrics`, `rank`, `redact`, `report`): plain functions over facts. The standard library only, and from it nothing that does I/O, starts a process or opens a connection. Only `policy` imports a third-party module, the TOML parser.
- **`collect`**: turns git output into facts. It runs git only through the `domain.GitRunner` port.
- **`app`**: the use cases. It reaches the outside only through the ports in `app.Deps`.
- **Outbound adapters** (`gitcli`, `sqlite`, `github`): each wraps one technology and imports none of the others.
- **Inbound adapters** (`cli`, `mcp`): call `app`. Only `cli` builds adapters: it is the composition root.
- **`fixture`**: test support; only tests import it.

The rule is not a guideline: `make lint` enforces it with depguard ([.golangci.yml](.golangci.yml)), so a violation fails CI. That makes it an architectural fitness function (Ford, Parsons and Kua, *Building Evolutionary Architectures*, ch. 2). If a change truly needs an exception, edit [codebase.md](docs/spec/codebase.md#the-dependency-rule) and `.golangci.yml` together, and say why in the pull request. A new third-party module needs the same: a row in codebase.md's module table naming the package that owns it, why it won, and its runner-up.

## Tests

- **Tests sit at stable boundaries** and run real git; no test mocks git, and porcelain output is never recorded and replayed. The tests that exist, and what each asserts, are listed in [codebase.md](docs/spec/codebase.md#tests). Add a test outside that list only with a reason in the pull request; check other behaviour by running the real thing.
- **The fixture oracles are the contract.** [testdata/fixture/SPEC.md](testdata/fixture/SPEC.md) describes the fixture repositories commit by commit, with every expected value derived by hand. `internal/fixture` builds those repositories with the real git binary. An implementation never produces its own oracle: derive a new value by hand and write it into SPEC.md first, then write the code that has to match it.
- **Determinism is tested.** Pass `--now`, sort before output, and keep empty slices and maps `nil` inside the core ([codebase.md](docs/spec/codebase.md#determinism)); two runs on the same inputs must give byte-identical JSON.

## Adding or changing a metric

Metrics are versioned definitions, like an API: reports cite `durable@1`, and a definition never changes under a published id.

1. **Propose it** with the [metric proposal](.github/ISSUE_TEMPLATE/metric_proposal.yml) issue form. stillhere measures properties of changes, one metric at a time, never a person's value ([ADR 0001](docs/adr/0001-measure-changes-rank-per-metric.md)).
2. **Write the catalog entry** in [docs/metrics.md](docs/metrics.md): the question, the unit, the definition, eligibility, uncertainty, blind spots, how it can be gamed, its fixture oracle, and its validity status. A metric without all of these does not ship.
3. **Derive the oracle by hand** in [testdata/fixture/SPEC.md](testdata/fixture/SPEC.md), with the wrong implementation it exists to catch. If the case needs new history, extend the sibling fixture rather than the main one, so no existing oracle moves.
4. **Implement it** in `internal/metrics`: a type that implements `metrics.Metric` (and `metrics.Ranked` if people are ranked on it), whose `Definition()` mirrors the catalog entry, registered in `metrics.All()`.
5. **Assert the oracle** in the named test that covers it (`TestChangeOracles` for change metrics).

**Versions.** Any change to a definition, its inputs or its defaults that can move any value is a new version: `durable@2` beside `durable@1`, never an edit. The old version stays computable over stored facts until a release removes it with a changelog entry.

## Commits and pull requests

- **One issue, one branch, one pull request.** Branches are named `m<milestone>/<issue>-<slug>`, such as `m1/9-durable`, and the pull request says `Closes #<issue>`.
- **[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)** for every commit subject and pull request title: `feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:` or `refactor:`, with `!` for a breaking change (`feat!:`). The release changelog is grouped by these prefixes ([.goreleaser.yaml](.goreleaser.yaml)), so the subject is what users read.
- **Stage files by name.** Never `git add -A` or `git add .`; `plan.original.md` and local databases must never be committed.
- **No real people's data.** Fixtures, tests, examples and issue text use invented names and `example.com` addresses ([ADR 0008](docs/adr/0008-apache-2-public.md)).
- **Explain decisions.** The [pull request template](.github/pull_request_template.md) asks for the decisions you made and the alternatives you rejected, any contract change, and how you verified the change. A decision that is hard to reverse gets an ADR in [docs/adr/](docs/adr/).

## Releases

A maintainer releases by pushing a `vX.Y.Z` tag on `main`; [.github/workflows/release.yml](.github/workflows/release.yml) then runs GoReleaser, which publishes the archives, the Homebrew cask and the container image. To see what a release would build, without tagging or publishing anything:

```sh
go run github.com/goreleaser/goreleaser/v2@v2.18.3 release --snapshot --clean
.github/scripts/smoke-release.sh
```

A tag such as `v0.2.0-rc.1` publishes a pre-release, which gets no cask update and no `latest` image tag. To promote it, tag the same commit `v0.2.0`.

### One-time setup

Until the tap and its token exist, a tag stops at release.yml's tap check, before anything is published:

1. **The tap**: the public repository `IHaveASegway/homebrew-tap`, which Homebrew finds as `ihaveasegway/tap`. If it is missing, create it with a first commit, such as a README. GoReleaser writes the cask to its `Casks/stillhere.rb`.
2. **A token that can push to the tap and nowhere else.** `GITHUB_TOKEN` cannot: it writes only to the repository its workflow runs in. As the IHaveASegway account, create a fine-grained personal access token with resource owner IHaveASegway, repository access "Only select repositories" set to `IHaveASegway/homebrew-tap` alone, and the repository permission Contents set to read and write; GitHub adds Metadata, read-only, by itself. Give it an expiry, and renew it before then: release.yml checks that the token can push to the tap and stops, before publishing anything, when it is missing, expired or read-only.
3. **The secret**: store the token in this repository as the Actions secret `HOMEBREW_TAP_GITHUB_TOKEN`. `gh secret set HOMEBREW_TAP_GITHUB_TOKEN --repo IHaveASegway/stillhere` asks for the value, so it stays out of your shell history.

After the first release, make the image public, once. GitHub publishes a package for the first time as private, whatever the visibility of the repository it came from, so until then `docker pull ghcr.io/ihaveasegway/stillhere` fails for everyone else. On the IHaveASegway profile's Packages tab, open `stillhere`, then Package settings, Danger Zone, Change visibility, Public. A public package cannot be made private again.

Dependabot cannot read the release toolchain's pins, so they are updated by hand: GoReleaser's version, and the binfmt and BuildKit images, appear in both workflows, and CI's lint job fails if any copy differs. Change every copy in one commit.

## License

stillhere is licensed under [Apache-2.0](LICENSE). By contributing, you agree that your contribution is licensed under the same terms (section 5 of the license).
