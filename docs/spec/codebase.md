# Code map and contracts

Status: v1 · 2026-10-14 · the layout, package contracts and conventions every change follows · product intent in [../product.md](../product.md), system design in [../architecture.md](../architecture.md)

Read this before touching code. It names every package, what it owns, what it may import, and the signatures between packages (the idea is Aleksey Kladov's "ARCHITECTURE.md"). Signatures here are contracts: a change to one lands in the same commit as the change to this file.

## Layout

| Package | Owns | Kind |
| --- | --- | --- |
| `cmd/stillhere` | `main`: signal handling, then `cli.Execute` | entry point |
| `internal/cli` | Commands, flags, exit codes, and the composition root (the only place adapters are built and wired) | inbound adapter |
| `internal/app` | Use cases: week, scan, report, explain, me, who-knows, validate, identities, sync, mcp | application |
| `internal/collect` | git output → facts: discovery, clone health, the commit a run reads at `--now`, tree and attributes, history, blame | anti-corruption layer for git |
| `internal/domain` | Fact types, metric value types, port interfaces, typed errors | core |
| `internal/policy` | The policy model, embedded default policy and deny list, parsing, hashing | core |
| `internal/classify` | The ordered path classifier | core |
| `internal/identity` | Display names, bots, AI-assisted tags, who gets credit; identity candidates, suggested and never applied ([identities.md](identities.md)) | core |
| `internal/metrics` | The metric registry and every metric version | core |
| `internal/rank` | Cluster bootstrap, rank intervals, eligibility, AUC | core |
| `internal/redact` | URL scrubbing and message truncation; the address rule, which finds email addresses in text and in a published document for every output (`address.go`) | core |
| `internal/report` | The report model (`stillhere.report/v1`) and its text, Markdown and JSON renderers, and the identities answer's `.mailmap` form | core |
| `internal/gitcli` | `domain.GitRunner` over the `git` binary: hardening, profiles, version floor | outbound adapter |
| `internal/sqlite` | `domain.FactStore`, and from M2 `domain.ForgeStore`, over `modernc.org/sqlite`, embedded migrations | outbound adapter |
| `internal/github` | `domain.ForgeSource` over GitHub's GraphQL API, and managed mirrors, which git clones and fetches over HTTPS (M2) | outbound adapter |
| `internal/mcp` | The MCP server (M4) | inbound adapter |
| `internal/fixture` | Builds the fixture repositories of [../../testdata/fixture/SPEC.md](../../testdata/fixture/SPEC.md) for tests | test support |

### The dependency rule

Dependencies point inward, toward the core (ports and adapters: Alistair Cockburn, "Hexagonal Architecture").

- **Core** packages import the standard library and other core packages only, and from the standard library nothing that does I/O, starts a process or opens a connection: no `os` (so no `os/exec`), `path/filepath`, `io/ioutil`, `syscall`, `net`, `net/http`, `net/rpc`, `net/smtp` or `database/sql`. The only third-party module is the TOML parser, and only `policy` imports it. Core code is a functional core: plain functions over facts, testable without git or a database (Gary Bernhardt, "Boundaries").
- **`collect`** imports core and `golang.org/x/sync`. It runs git only through `domain.GitRunner`: never `os/exec`, `syscall` or `golang.org/x/sys`, no network package, and never an adapter or `app`. It owns the files a run touches outside git: discovery reads directories (`os.ReadDir`, `os.Stat`), and blame reads `--ignore-revs-file` and writes the list git reads to a temp file, 0600 in a new 0700 directory, removed once blame is done.
- **`app`** imports core, `collect` and `golang.org/x/sync`, and reaches git, facts.db and GitHub only through the ports in `Deps`. It never imports an adapter; `os` (so no `os/exec`), `path/filepath`, `io/ioutil` or `syscall`, the standard library's general file and process APIs, which the lint denies; a network package; `database/sql`; or cobra: the filesystem a run reads and writes outside git is `collect`'s (above).
- **Outbound adapters** (`gitcli`, `sqlite`, `github`) import core and the module they wrap (`modernc.org/sqlite`, `cli/go-gh`). They never import `app`, `cli`, `collect` or one another.
- **Inbound adapters** (`cli`, `mcp`) call `app`. Only `cli` builds adapters: it is the composition root (Mark Seemann, "Composition Root"), which builds them and hands them to `app`. So `mcp` imports `app` and core, its SDK, and no adapter; it serves over stdio, so it imports no network or process package either.
- **`fixture`** is test support: only tests import it.
- **A package this layout does not list** is held to the core's rule until it is added here and to the lint config.

`.golangci.yml` enforces the rule with depguard, so a violation fails CI. Each kind of package has an allow list (the standard library, the internal packages it may import, the modules it owns), with deny entries that win over the standard library and say why; anything else fails. That makes it an architectural fitness function (Ford, Parsons and Kua, *Building Evolutionary Architectures*, ch. 2), and like any test it was checked against planted violations. A change that needs an exception edits this file and the lint config together.

### Third-party modules

Every module is code someone must keep current, so each one says why it beat the runner-up. Only the package named next to it imports it.

| Module | Package | Why | Runner-up |
| --- | --- | --- | --- |
| `github.com/spf13/cobra` | `cli` | The convention of the CLIs stillhere sits beside (gh, kubectl, Helm, golangci-lint, GoReleaser), so its help layout, `completion` command and code are familiar to users and contributors. Man pages come from `cobra/doc`; Charm's fang can polish the help later. | `github.com/urfave/cli/v3`: a `context.Context` in every action, flag values from environment variables, no dependencies, and far more active (100+ commits in the six months to 2026-10-14, against cobra's 5). Since v3 it parses flags after arguments exactly as cobra does (tested on v3.14.0). Switching rewrites `cli` and nothing else. |
| `github.com/BurntSushi/toml` | `policy` | A TOML 1.0 decoder with no dependencies; a policy file is small, so decoding speed does not matter. | `github.com/pelletier/go-toml/v2` |
| `modernc.org/sqlite` | `sqlite` | SQLite without cgo, so the binary stays static and cross-compiles ([architecture.md](../architecture.md)). | `github.com/mattn/go-sqlite3`, which needs cgo |
| `golang.org/x/sync` | `collect`, `app` | `errgroup` with `SetLimit` bounds the number of git processes, and `semaphore` holds the blame slots every repo shares (`collect.BlameSlots`) ([Concurrency](#concurrency)). | a hand-written semaphore |
| `github.com/cli/go-gh/v2` (M2, #14; v2.16.2) | `github` | gh's own auth and API client, so a user's `gh auth login` works unchanged: the token comes from `GH_TOKEN`, `GITHUB_TOKEN` or gh's stored token, as gh finds it. `sync github` reaches github.com only; an Enterprise host needs a `--host` flag and per-host mirrors (#44), which `github.Config.Host` already supports. It adds 13 modules to `go.mod`. `pkg/auth` alone would add 4; the other 9 (shurcooL-graphql, httpretty, termenv and three of its own, go-timezone-local, `x/term`, `x/text`) come with `pkg/api`, mostly for `GH_DEBUG` logging and terminal handling that stillhere turns off. Revisit if that count matters more than gh's token lookup: replacing `pkg/api` with `net/http` touches `internal/github` only. | `github.com/google/go-github` with our own token handling |
| `github.com/modelcontextprotocol/go-sdk` (M4, #15; v1.8.0) | `mcp` | The official MCP SDK ([ADR 0006](../adr/0006-ai-mcp-and-narrated-highlights.md)), published by the specification's own organisation, so it follows the specification's revisions first (v1.7 and later speak 2026-07-28 down to 2024-11-05); its in-memory transport is what `TestMCPTools` drives the real server through. It adds 7 modules to `go.mod`: itself, and 6 that the packages stillhere links need (`google/jsonschema-go` for tool schemas, `segmentio/encoding` and `segmentio/asm` for its JSON, `yosida95/uritemplate`, and `x/oauth2` and `x/time` for its HTTP transports' auth and rate limits). It adds 2.1 MB, 15%, to the stripped binary (13.8 to 15.9 MB on darwin/arm64, 2026-10-14), most of it in those HTTP transports, which stdio does not use; the `mcp` package of the SDK holds them all. Revisit if the size matters more than following the specification: a stdio-only server is a small JSON-RPC loop. | `github.com/mark3labs/mcp-go`: older and widely used, not the specification's own |

## Domain types (`internal/domain`)

Facts are plain values with no behaviour beyond small helpers. Times are UTC. Slices are sorted before they leave a package (see [Determinism](#determinism)).

```go
// Repo is one repository as discovered for a run.
type Repo struct {
	Key     string // dedupe and storage key: the scrubbed origin URL, else "local:" + Name (no origin, or one that does not parse)
	Name    string // directory basename without a trailing ".git"
	Group   string // group name; "" when the repo was named directly
	Path    string // absolute path
	Bare    bool
	Remote  string // scrubbed origin URL; "" when there is no origin
	Branch  string // default branch (spec/discovery.md#default-branch)
	Head    string // full SHA the branch resolved to; in a run's facts (#70), the commit the run read: the newest first-parent commit
	               // of that SHA whose committer time is before --now (collect.SnapshotAt, run model); every later command names it
	Shallow bool
	Partial bool
}

type Group struct {
	Name  string   // basename; "-2", "-3" suffixes for duplicates
	Path  string
	Repos []string // Repo.Key, sorted
}

type Warning struct {
	Code    string // duplicate-repo, shallow-clone, shallow-window, partial-clone, ignore-revs, head-after-now, dated-after-now, excluded-repo, path-failed, no-commit-before-now, ...
	Message string // already scrubbed
	Repo    string // Repo.Key; "" for run-level warnings
}

type Role string // prod, sql, markup, iac, doc, test, migration, seed
type Skip string // denied_path, too_big, generated_header, unclassified

// PathClass is the classifier's verdict on one path; one per path in a run.
type PathClass struct {
	Path   string
	Role   Role  // "" when skipped
	Skip   Skip  // "" when a role was assigned
	Size   int64 // bytes the too_big check read: at HEAD, else at the commit that touched the path; -1 when unknown
	AtHead bool  // the path exists at HEAD; false for a path only the history touched, which gets no header sniff
}

// Person is an identity after git applied the mailmap.
type Person struct {
	Name  string // as git printed it (%aN, blame "author")
	Email string // lowercased: the canonical key everywhere
}

type Commit struct {
	SHA        string
	Parents    []string
	Author     Person
	AuthorTime time.Time
	CommitTime time.Time
	CoAuthors  []Person // Co-authored-by trailers, canonicalised with git check-mailmap; trailer order, which credit reads
	Subject    string   // local only; never sent to an AI
}

type Landing struct {
	SHA     string    // a first-parent commit of the default branch
	Time    time.Time // committer time
	Merge   bool
	Commits []string  // git rev-list SHA^1..SHA, the landing included; [SHA] for a non-merge
}

type FileChange struct {
	Commit          string
	Path            string // destination path after rename resolution
	OldPath         string // rename source, else ""
	Added           int    // numstat insertions, blank lines included; 0 when Binary
	Removed         int
	Binary          bool
	AddedNonBlank   int // from the commit's own -U0 -w patch; -1 when not collected (week runs)
	RemovedNonBlank int
}

type Revert struct{ SHA, Reverted string } // full SHAs

type LineOwner struct { // one row per (Path, Commit, OrigPath)
	Path       string
	Commit     string
	OrigPath   string   // #54: the path the lines had in Commit, blame's porcelain "filename"; "" when it is Path. (OrigPath, Commit) is the key
	                    // FileChange's (Path, Commit) shares, whatever renames lie between, so durable@1 matches a binary change's lines by it,
	                    // and (#70) classifies a blamed line by OrigPath's role, as Σ added classifies a change by its Path
	Author     Person
	AuthorTime time.Time
	CoAuthors  []Person // Commit's Co-authored-by trailers, canonicalised and ordered like Commit.CoAuthors; credit reads them (ADR 0007)
	Lines      int      // non-blank lines blame attributes to Commit, from OrigPath, in Path at HEAD
}

type IgnoreRevs struct {
	Source string   // "root", "cli" or ""
	SHA256 string
	Revs   []string // the commits git blame ignores: each listed ID that names a commit, or an annotated tag of one (peeled), lowercased and sorted; durable@1 and simplifying@1 exclude these commits
}

type FileError struct{ Path, Message string }

type Window struct{ Since, Until time.Time } // [Since, Until)
// The one definition of a Sum metric's week blocks (docs/metrics.md#ranking-rules), which simplifying@1,
// reviews_given@1 and rank.Intervals' check all use (Information Expert: Larman, "Applying UML and Patterns", ch. 17):
func (w Window) Weeks() int           // ⌈(Until − Since) / 7 days⌉, 0 when empty: rank.Options.Blocks over w (13 for 90 days)
func (w Window) Week(t time.Time) int // ⌈(Until − t) / 7 days⌉ − 1, half-open: block 0 is [Until − 7 days, Until); rank.Cluster.Block

// RepoFacts is everything one run learned about one repository.
type RepoFacts struct {
	Repo        Repo
	Status      string // "ok" or "error"
	Error       string // scrubbed and truncated; "" when ok
	Warnings    []Warning
	FirstCommit time.Time   // author time of the earliest root commit reachable from Head; zero when unknown (a shallow clone)
	History     Window      // the landing window the history covers
	Paths       []PathClass
	Commits     []Commit    // landing commits plus every commit a landing brought in
	Landings    []Landing
	Changes     []FileChange
	Reverts     []Revert
	LineFacts   bool        // blame ran (false for week runs, shallow and partial clones)
	Owners      []LineOwner
	IgnoreRevs  IgnoreRevs  // scan runs, shallow and partial clones included (collect.Blame with no paths resolves it)
	BlameErrors []FileError
}

type PolicyRef struct {
	Version int
	SHA256  string
}

type FactSet struct {
	Now      time.Time
	Policy   PolicyRef
	Groups   []Group
	Repos    []RepoFacts // sorted by Repo.Key
	Warnings []Warning   // run-level
	Forge    *ForgeFacts // nil until GitHub data is synced (M2)
}
```

Metric results:

```go
type MetricID struct {
	Name    string
	Version int
} // String() is "durable@1"; ParseMetricID accepts "durable@1" and "durable" (latest)

type Subject struct{ Kind, Key string } // person (canonical email), area (repo key + ":" + path prefix), repo, group (Group.Name: a group's own numbers), change (landing SHA or PR id)
type Scope struct{ Kind, Key string }   // repo (Repo.Key), group (Group.Name), all ("")

type MetricValue struct {
	Metric  MetricID
	Scope   Scope
	Subject Subject
	Window  Window
	Value   float64
	Num     float64            // ratio metrics; otherwise equal to Value
	Den     float64            // ratio metrics; otherwise 1
	N       int                // the sample size the eligibility rule reads (landings, reviewed pull requests)
	Detail  map[string]float64 // context numbers shown next to Value (encoding/json sorts the keys)
}

type RankInterval struct {
	Metric         MetricID
	Scope          Scope
	Subject        Subject
	Window         Window
	Estimate       float64
	Lo, Hi         float64 // 95% interval of the value; 0 when not eligible
	RankLo, RankHi int     // 95% interval of the rank; 0 when not eligible
	N              int
	Eligible       bool
}
```

Runs and errors:

```go
type RunKind string   // "scan", "sync"
type RunStatus string // "running", "complete", "partial", "failed"

type Run struct {
	ID          int64
	Kind        RunKind
	Status      RunStatus
	Now         time.Time
	Started     time.Time
	Finished    time.Time
	ToolVersion  string
	Policy       PolicyRef
	PolicySource []byte            // policy.Policy.Source: the bytes Policy.SHA256 hashes; readers rebuild the run's policy from it; empty for sync runs
	Options      map[string]string // effective options; never secrets
}

type UsageError struct{ Msg string } // exit 2
var ErrNoFacts = errors.New("no repository produced facts") // exit 1
var ErrNotFound = errors.New("not found")
```

## Ports

```go
type Profile int // Analysis (offline, hardened) or Sync (network, mirrors only)

type GitCmd struct {
	Dir     string        // passed as -C
	Args    []string      // git arguments after the common prefix
	Stdin   io.Reader
	Timeout time.Duration // 0: the profile's default
	OKExit  []int         // non-zero exits that are not errors (git grep exits 1 on no match)
	Env     []string      // Sync only: credential variables; never logged
	Profile Profile
}

type GitRunner interface {
	Run(ctx context.Context, cmd GitCmd) ([]byte, error)                     // buffered stdout
	Stream(ctx context.Context, cmd GitCmd, fn func(io.Reader) error) error // streamed stdout, for log -p and blame
}

// GitError is returned for an exit outside OKExit, a timeout, or a failed start. A timeout or a
// cancellation is a GitError even when fn also failed on the output the kill truncated.
type GitError struct {
	Args   []string // the caller's arguments, redact.Scrub applied: never contains credentials
	Exit   int      // -1 for a timeout or a failed start
	Stderr string   // redact.Message applied
	Err    error    // errors.Is(err, context.DeadlineExceeded) for a timeout, context.Canceled for a cancellation
}
func GitSubcommand(args []string) string // "log" for ["-c", "k=v", "log", ...]: how every message names a git command

type FactStore interface {
	BeginRun(ctx context.Context, r Run) (int64, error)
	PutRepoFacts(ctx context.Context, runID int64, f *RepoFacts) error
	PutRunFacts(ctx context.Context, runID int64, groups []Group, warnings []Warning) error
	PutMetrics(ctx context.Context, runID int64, values []MetricValue, ranks []RankInterval) error
	FinishRun(ctx context.Context, runID int64, status RunStatus) error
	// The read side sees complete and partial runs only: an unknown, running or failed run is ErrNotFound.
	Run(ctx context.Context, id int64) (Run, error)           // status, tool version, times, options, policy source
	LatestRun(ctx context.Context, kind RunKind) (Run, error) // the newest one: the last begun, whatever its Now
	LoadFacts(ctx context.Context, runID int64) (*FactSet, error)
	LoadMetrics(ctx context.Context, runID int64) ([]MetricValue, []RankInterval, error)
	Close() error
}
```

```go
// domain/forge.go (#14): the Forge context. GitHub is the system of record; facts.db holds a replica.
type ForgeRepo struct {
	ID, Key       string // node id (stable across renames); the HTTPS clone URL, which is its mirror's Repo.Key
	Owner, Name   string
	DefaultBranch string // "" for an empty repository
	Archived      bool
	Fork          bool
}
type ForgeUser struct { Login string; Bot bool }                  // Login "" for a deleted account; Bot: an app account
type ForgeCommit struct { SHA, AuthorEmail, AuthorLogin string }  // the forge's author account for a commit: how logins link to people
type Review struct {
	ID         string
	Author     ForgeUser
	State      string    // ReviewApproved, ReviewChangesRequested, ReviewCommented, ReviewDismissed
	Submitted  time.Time
	BodyLength int       // characters of the review's own text, trimmed; the text is not kept
	Comments   int       // inline comments in the review
}
type PullRequest struct { // replaced as a whole on every sync that sees it updated
	ID, Repo                                string // node id; ForgeRepo.ID
	Number                                  int
	Title                                   string // local only; never sent to an AI
	Author                                  ForgeUser
	State                                   string // PullOpen, PullClosed, PullMerged
	Draft                                   bool
	BaseBranch                              string
	Created, Updated, Closed, Merged        time.Time
	MergeCommit                             ForgeCommit   // zero unless merged
	Labels                                  []string      // sorted
	Reviews                                 []Review      // sorted by Submitted, then ID
	Commits                                 []ForgeCommit // the forge's order
}
type ForgeWatermark struct { Repo string; Updated, Synced time.Time } // every PR updated before Updated is replicated; Synced: the "as of" time
type ForgePage struct { Repo string; PullRequests []PullRequest; Updated time.Time } // stored with its watermark in one transaction
// ForgeFacts: LoadForge's replica, or a scan run's own part of it (FactSet.Forge): the forge repositories ForgeJoin joins
// to the run's repositories, their watermarks (a zero one where the replica held none) and pull requests, which the run
// keeps (PutRunForge) and its readers read back (RunForge), so a stored run computes over what it read (ADR 0011).
// Join (#38's review): the run's join, ForgeRepo.ID → its Repo.Keys, as its scan made it and its watermarks record it
// (RunWatermark.Keys); every metric counts by it, so a later ForgeJoin moves no stored run. nil for LoadForge's replica,
// which no run joined: a metric over the replica itself joins it with ForgeJoin
type ForgeFacts struct { Repos []ForgeRepo; PullRequests []PullRequest; Watermarks []ForgeWatermark; Join map[string][]string }
// RemoteRepo: "host/owner/name" of a remote, lowercased, with no user, port or ".git", for https://, ssh:// and
// git@host:o/r remotes. A run's repository (Repo.Remote) is a forge repository (ForgeRepo.Key) when the two match.
func RemoteRepo(remote string) (string, bool)
// ForgeJoin (#13): the one join of a run's repositories to a replica's: ForgeRepo.ID → the sorted Repo.Key of every
// repository whose origin names it (RemoteRepo), a failed one (status error) left out; nil when none. A scan joins by it
// once, and the run keeps that join (ForgeFacts.Join, RunWatermark.Keys), which every metric counts by and every page dates
// by, so the two cannot disagree about which GitHub data is a run's.
func ForgeJoin(repos []RepoFacts, forge []ForgeRepo) map[string][]string
// RunWatermark (#13): a run's forge repository (ForgeJoin) as the run read it, which it keeps beside the pull requests
// (PutRunForge). All of it is the run's own, so a later sync that renames the repository changes nothing a stored run shows.
type RunWatermark struct {
	ForgeWatermark          // zero times when the replica held no page of the repository
	Name           string   // "owner/name" as the replica listed it
	Keys           []string // Repo.Key of the run's repositories whose origin names it (ForgeJoin), sorted; never empty
}

type MirrorMove struct { // a mirror ReconcileMirrors moved, or could not
	Repo, From, To string // ForgeRepo.ID; absolute paths
	Reason         string // MirrorRenamed, MirrorRestored, MirrorStale, MirrorDuplicate
	Err            string // why it could not move; a listed repository whose mirror could not move is not mirrored that sync
}

type ForgeSource interface { // GitHub's GraphQL API and git remotes (internal/github); GitLab later (ADR 0004)
	Repos(ctx context.Context, owner string) ([]ForgeRepo, error) // sorted by Key; an unknown login is a *UsageError
	// Once per sync, before any fetch, with owner's whole listing: mirrors follow their repositories by node id, which a
	// rename or a transfer keeps; a mirror under root/<owner> of a repository no longer listed moves to root/.stale.
	ReconcileMirrors(ctx context.Context, owner string, listed []ForgeRepo, root string) ([]MirrorMove, error)
	Mirror(ctx context.Context, repo ForgeRepo, root string) (string, error) // root/<owner>/<name>.git, created or fetched
	PullRequests(ctx context.Context, repo ForgeRepo, since time.Time, put func(*ForgePage) error) error // updated at or after since
}
type ForgeStore interface { // the replica in facts.db (*sqlite.Store, sqlite/forge.go)
	PutForgeRepos(ctx context.Context, repos []ForgeRepo) error
	ForgeWatermark(ctx context.Context, repo string) (ForgeWatermark, error)
	PutForgePage(ctx context.Context, page *ForgePage) error // the watermark only rises; a pull request is replaced only by a copy updated as late or later
	MarkForgeSynced(ctx context.Context, repo string, at time.Time) error
	LoadForge(ctx context.Context, since time.Time) (*ForgeFacts, error) // PRs updated at or after since; nil for an empty replica
	PutRunWatermarks(ctx context.Context, runID int64, watermarks []RunWatermark) error // a running run: what it read, with its join; no Keys is an error
	RunWatermarks(ctx context.Context, runID int64) ([]RunWatermark, error) // sorted by Repo; ErrNotFound unless complete or partial; no Name or Keys before schema version 3
	// #38 (ADR 0011): a running run's GitHub data, FactSet.Forge, kept whole; a pull request of a repository not in repos is an error
	PutRunForge(ctx context.Context, runID int64, repos []ForgeRepo, pulls []PullRequest) error
	// what PutRunForge kept, sorted as LoadForge sorts, Watermarks from RunWatermarks and Join from their Keys; nil when the run kept none (no forge
	// repository, or stored before schema version 4); ErrNotFound unless complete or partial
	RunForge(ctx context.Context, runID int64) (*ForgeFacts, error)
}
```

#14 owns `domain/forge.go`, `internal/github`, `sqlite/forge.go` and `sqlite/migrations/0002_forge.sql` (where `*sqlite.Store` implements `ForgeStore`), `cli/forge.go` (which builds the `ForgeSource` adapter), `cli/sync.go`, `app/sync.go` and `metrics/reviews.go`. `ForgeStore` is a port of its own rather than more `FactStore` methods: `FactStore` holds per-run snapshots, while forge facts are a replica that outlives runs ([run model](#run-model)), and a separate port leaves `FactStore` and #8's implementation untouched (Interface Segregation Principle). What a run read of the replica, its watermarks and (#38) its pull requests, is on the port too, so a reader without it reads no GitHub data at all, neither the dating nor the reviews. Mirroring is on `ForgeSource` because the credentials are the forge's: the adapter holds the token, so it runs the clone and the fetch (through `domain.GitRunner`), and the token never reaches `app`. A mirror carries its repository's node id in its own git config (`stillhere.forgeId`), because its path does not identify it: a rename or a transfer changes the path and keeps the id, and discovery keys repositories by origin URL, so a mirror left under the old name would be scanned twice. `ReconcileMirrors` is a separate, sequential call because only it sees the whole listing: it can tell a rename from a deletion, and settle two repositories that swapped names, before the parallel fetches start. Store migrations are embedded files, `internal/sqlite/migrations/NNNN_<name>.sql`, applied in order, one transaction each, and numbered by `PRAGMA user_version`: 1 (`0001_facts.sql`) is #8's schema, 2 (`0002_forge.sql`) is #14's forge tables (`forge_run_watermarks` references #8's `runs`), 3 (`0003_forge_run_join.sql`) is #13's: each run watermark's name, and the run's repositories it joined (`forge_run_repos`), so a reader never matches a stored run against today's `forge_repos`. 4 (`0004_forge_snapshot.sql`, with `sqlite/forgesnapshot.go`) is #38's: the GitHub data a run read, its forge repositories and their pull requests with their labels, reviews and commits (`forge_snapshot_*`), kept with the run ([ADR 0011](../adr/0011-a-run-keeps-the-github-facts-it-read.md)). 5 (`0005_line_owner_orig_path.sql`) is #54's: each line owner's `OrigPath` (`orig_path`, `''` for a row stored before it, which reads as the path at HEAD). M4 adds narrations the same way.

## Package APIs

```go
// redact
func URL(raw string) string     // userinfo, query and fragment removed; "<unparsed-remote>" when unparsable or a scheme-less string with an @ that is not user@host:path
func Scrub(s string) string     // credentials removed from free text (URL() on every URL, user:pass@ prefixes, also on a path segment, and the userinfo, query and fragment of a URL a path cleaner collapsed, "/cwd/https:/user:TOKEN@host/r.git?access_token=TOKEN"; a one-letter scheme is a Windows drive, so "C:\src\C#" keeps its "#"); nothing else changes
func Message(s string) string   // Scrub, then one line truncated to 180 characters
// address.go (#68): the one rule for finding email addresses, which the report's renderers and the MCP server's egress both
// apply, each with its own operator (a fixed label; an id). Scrub keeps every address.
type Addresses struct{ /* a vocabulary: whole addresses, and local parts outside the shape's class */ }
func NewAddresses(emails []string) *Addresses // any case; an empty local part (@example.com) is an address, a login ("@name") is not but cannot
                                              // be told apart here: callers leave logins out (identity.LoginKey)
func (a *Addresses) Replace(s string, path bool, repl func(address string) string) string // the vocabulary's addresses anywhere, as whole
                                              // addresses; unless path, whatever looks like one (a domain ending in letters, or an IP literal;
                                              // not a version pin, a metric id or an scp-like remote's user@host:path). nil: the shape alone
type Place struct{ Member string; Name, Person bool } // where a string sits in a JSON document: the member it is the value of, whether it
                                              // is a member's name, whether its object's kind, read before it, is "person"
func (p Place) Email() bool                   // the published shapes' only places for an email: a member named email, a person's key
func (p Place) Path() bool                    // path, old_path: the vocabulary layer only
func RewriteJSON(doc []byte, fn func(p Place, s string) string, opts ...jsontext.Options) ([]byte, error) // every string, names included,
                                              // token by token; byte for byte when fn changes nothing and opts are the document's own
func JSONStrings(doc []byte, fn func(p Place, s string)) error // reads every string with its place

// gitcli
type Config struct {
	Git         string // binary; default "git", resolved once to an absolute path
	MailmapFile string // org mailmap, passed as -c mailmap.file=; made absolute
	Logger      *slog.Logger
}
func New(ctx context.Context, cfg Config) (*Runner, error) // git older than 2.45 → *domain.UsageError
func (r *Runner) Version() string                          // *Runner implements domain.GitRunner

// policy: the model (types.go) is frozen; #5 implements the functions, and added only Paths' unexported prefilter.
// Every key of a required table is required: nothing is filled in from the packaged default,
// whose default.toml holds the values the specs call defaults ([blame] max_bytes 400000).
type Policy struct {
	Name     string
	Version  int    // the file's own schema version
	SHA256   string // hex digest of Source
	Source   []byte // "<byte length of the TOML, decimal>\n<TOML><deny list text>": exactly what SHA256 hashes; a run stores it.
	                // The deny list text is generated_paths.txt for the packaged policy and empty for a user file, whose
	                // deny list is in the TOML; the prefix makes the split unambiguous, so no edit moves bytes across it
	                // and keeps the hash
	Headline Headline // [headline] Metric string
	Week     Week     // [week] Days int, 1 to 36500 (--days should hold to the same bound); Sort string
	Blame    Blame    // [blame] MaxBytes int64
	Bots     Bots     // [bots] Fragments []string, lowercased
	AI       AI       // [ai] Version int; CoauthorEmails, AgentAuthors []string. Matches co-author emails and agent
	                  // authors only: a rule on another trailer (Made-with:) needs a collector field and a new ai_assisted version
	Roles    Roles    // [roles] Extensions (one dot each: classify reads the last dot suffix), Names map[string]domain.Role; Path []PathRule
	Paths    Paths    // [paths] Deny []*regexp.Regexp, matched with Denies; Source "package:generated_paths.txt" or "policy"
}
type PathRule struct {
	Role    domain.Role
	Pattern *regexp.Regexp // RE2 over the repo-relative path
	When    []domain.Role  // empty: any path; else only paths whose extension or name role is one of these
} // tried in order: the first whose Pattern matches and whose When admits the extension or name role wins
func Default() (*Policy, error)                             // embedded default.toml + generated_paths.txt
func Parse(name string, data []byte) (*Policy, error)       // *domain.UsageError for a missing table or key, an unknown key, or a bad pattern or value
func FromSource(name string, source []byte) (*Policy, error) // rebuilds a run's policy from Run.PolicySource alone, never today's packaged files; same Ref; a plain error, not a usage error
func (p *Policy) File() ([]byte, error)                     // #71: the policy as a file Parse reads back to the same rules, what 'stillhere policy' prints: a user's
                                                            // file as it was read; the packaged one with its header comment replaced and [paths] package_file by
                                                            // deny = [...], every pattern of its deny list in order with the list's comments (Linguist's license
                                                            // among them), so a file started from it is complete and its hash covers every pattern; from Source alone
const MaxWeekDays = 36500                                   // the bound on [week] days and on --days
func (p *Policy) Ref() domain.PolicyRef
func (ps *Paths) Denies(path string) bool                   // any Deny pattern matches path. Default, Parse and FromSource also store, unexported in
                                                            // Paths, the literals each pattern needs (FilteredRE2's prefilter): a path tries a
                                                            // handful of the ~240 packaged patterns, not all; a pattern added by hand is always tried

// classify
type Input struct {
	Path      string
	AtHead    bool   // the path exists at HEAD; false for a path only the history touched
	NotFile   bool   // HEAD holds a symbolic link or a submodule at the path: no role, so unclassified unless denied
	Size      int64  // bytes at HEAD, else at the commit that touched the path; -1 when unknown: no too_big check
	Generated string // linguist-generated at HEAD: "unspecified", "unset", "set" or a value
	Vendored  string // linguist-vendored at HEAD
	HeaderHit bool   // header sniff matched within the first 40 lines; always false when !AtHead (the sniff reads HEAD)
}
func Classify(p *policy.Policy, in Input) domain.PathClass
func Role(p *policy.Policy, path string) domain.Role // path and extension rules only

// collect
func Discover(ctx context.Context, git domain.GitRunner, paths []string) (*Discovery, error) // an error only when ctx ends
type Discovery struct {
	Repos    []domain.Repo      // health-checked, duplicates removed, sorted by Key
	Groups   []domain.Group     // sorted by Name
	Warnings []domain.Warning   // duplicate-repo, shallow-clone, partial-clone; sorted by Repo, Code, Message
	Failed   []domain.FileError // paths that gave no repository, each once, sorted by Path: a PATH slot that was neither a repo nor a group (#71: a directory with no repository among its
	                            // immediate children is none, and its Message says what to pass: two subdirectories that hold repositories, else the work tree it is inside),
	                            // or a repo (named or a group's member) git cannot read or whose default branch has no commit; each Message one line, scrubbed, and cut
	                            // where it quotes git; app reports each as a run-level path-failed warning and marks the run partial (see Errors and logging)
}
// SnapshotAt (#70): the commit a run reads repo at, the repository as it stood at now (run model): one
// rev-list --first-parent --timestamp of repo.Head, read down from the head to the first commit whose committer
// time is before now, as Window.Contains compares, and on to the chain's end to count Below
func SnapshotAt(ctx context.Context, git domain.GitRunner, repo domain.Repo, now time.Time) (Snapshot, error)
type Snapshot struct {
	Commit string    // the newest first-parent commit of repo.Head committed before now; repo.Head unless that is not; "" when none is
	Time   time.Time // Commit's committer time
	After  int       // the first-parent commits above Commit: landings at or after now, which the run does not read
	Below  int       // the first-parent commits below Commit committed at or after now: landings it reads, which no window holds
}
type PathAt struct{ Commit, Path string } // a path in the tree of a commit that touched it
// ClassifyPaths: every path at Head, symbolic links and submodules included (they get no role), plus each touched
// path Head no longer has, sized at its Commit. touched: every path the history touched (week and scan), one entry
// per path at the newest commit that added lines to it; later duplicates are ignored. Returns one PathClass per
// distinct path, sorted by Path, so a path the history touched has a class; a consumer may treat a missing one
// as an error.
func ClassifyPaths(ctx context.Context, git domain.GitRunner, repo domain.Repo, p *policy.Policy, touched []PathAt) ([]domain.PathClass, error)
type HistoryMode int // HistoryWeek or HistoryScan
type HistoryOptions struct {
	Window domain.Window // landings by committer time: the week, or for scan the 365 days before --now
	Mode   HistoryMode
}
func History(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt HistoryOptions) (*HistoryFacts, error)
// HistoryFacts: each slice nil when empty and sorted by the key named, which a store's round trip
// keeps; Commit.Parents and Commit.CoAuthors keep git's order (first parent first, trailer order).
type HistoryFacts struct {
	FirstCommit time.Time           // git log --max-parents=0 --format=%at <head>: the earliest root's author time; zero when unknown, and in week runs
	Commits     []domain.Commit     // by SHA: the landing commits, plus in scan runs every commit a landing brought in
	Landings    []domain.Landing    // by SHA; Landing.Commits, the map, in scan runs only (nil in week runs)
	Changes     []domain.FileChange // by Commit, then Path; NonBlank -1 in week runs; no rows in a partial clone or for a shallow clone's boundary commit
	Reverts     []domain.Revert     // by SHA, then Reverted; scan runs only; SHA is the window landing whose map holds the revert commit (itself, or a commit a merge brought in)
	Warnings    []domain.Warning    // by Code, then Message: shallow-window
}
type BlameOptions struct {
	Paths          []string              // classified paths at Head that received a role; none: only resolve IgnoreRevs, which a scan does for a repo whose line facts it skips (shallow, partial)
	IgnoreRevsFile string                // CLI override; single-repo runs only; an unreadable or malformed file is a *domain.UsageError
	Slots          *BlameSlots           // NewBlameSlots(jobs), shared by every repo; nil is an error
	Timeout        time.Duration         // per file; 0 means 120 s
	Progress       func(done, total int) // after every 1,000th file and after the last
}
type BlameSlots struct{ /* a golang.org/x/sync semaphore; only Blame takes and frees slots */ }
func NewBlameSlots(n int) *BlameSlots // n git blame processes at once, across the run; below 1 means 1
func Blame(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt BlameOptions) (*BlameFacts, error)
type BlameFacts struct {
	Owners     []domain.LineOwner // sorted by Path, Commit, then OrigPath; CoAuthors from coAuthors
	IgnoreRevs domain.IgnoreRevs  // the list blame used: exactly the commits git ignored
	Errors     []domain.FileError // one per blamed path that failed, sorted by Path; RepoFacts.BlameErrors
	Warnings   []domain.Warning   // at most one, Code "ignore-revs": the root file was not used, or not whole; the caller adds it to RepoFacts.Warnings
}

// identity
func New(fs *domain.FactSet, p *policy.Policy) *Directory
func (d *Directory) Name(email string) string           // display name rule, spec/git-invocation.md#identity-mailmap-and-bots; for a LoginKey, the login
                                                        // as GitHub last showed it ("@Zoe" for "@zoe", #69), "" when the run's GitHub data never named it
func (d *Directory) Bot(email string) bool              // a [bots] fragment in the email or display name, or an [ai] agent: agents are never people (ADR 0007); a forge app account's "@login[bot]" too
func (d *Directory) AIAssisted(c *domain.Commit) bool    // ADR 0007; only a commit credited to a human
func (d *Directory) Credit(author domain.Person, coAuthors []domain.Person) string // the author, or an agent's first human co-author (ADR 0007); commits and blamed lines alike
// identity/reviewer.go (#38): the one rule for who a forge account is, which every metric that reads reviews asks. A login
// is the run's mailmapped author of the commits FactSet.Forge attributes to it (a pull request's commits and merge commit
// that are in the run's history; the most, then the smallest email), else "@login"; an app "@login[bot]"; no login, the zero Subject.
// #69: it is where a forge identity becomes a person key, so it folds a login to lower case before it links or keys it, as GitHub
// compares logins: "@zoe" for Zoe and zoe, as the collectors lowercase emails, so every reader's lowercasing finds the stored key
func (d *Directory) Reviewer(u domain.ForgeUser) domain.Subject
func CanonicalKey(key string) string                 // #69: a stored person key as keys are made now: a LoginKey folded as Reviewer folds it ("@Zoe" is
                                                     // "@zoe"), any other key as it is; app's load upcasts a run stored before the fold by it
func (d *Directory) Logins(email string) []string    // #69: the logins Reviewer links to the person, as GitHub last showed each, sorted without case; nil for none
func (d *Directory) LoginWithheld(login string) bool // #69: a linked login a page that prints no address must not print beside its person: a local
                                                     // part of a person's address in the run (localForms), as a noreply address holds its login
// identity/candidates.go (#26, spec/identities.md): addresses that may be one person, suggested and never applied
func Candidates(fs *domain.FactSet, who *Directory) *Survey // pure; fs is mailmapped facts, who built over the whole run; bots and agents never appear
// identity/blocks.go (spec/identities.md#counting-people-for-a-floor): the same keys read for recall, for a count that must not err high
func Blocks(fs *domain.FactSet, who *Directory) map[string]string // pure; each human address → its block's smallest address: what Candidates links, and
                                                                 // every hand-off chain, and every two sharing the words of any name seen with them (a default one too), a local part or an account id, without the rules' vetoes
type Survey struct { Candidates []Candidate; Addresses, Bots, Shared, Oversized int } // most confident first; the human addresses examined, the bots left out,
                                                                                    // the addresses several people use (compared with nothing), the groups of more than 10 left out
type Candidate struct { // an address is in one candidate at most, so no two .mailmap lines map one alias
	Confidence      Confidence // the weakest pair's: high (account id, or a full name and a local part not made from it), medium (a full name), low
	Guess, Chained  bool       // a hand-off, never suggested as certain; two addresses linked only through a third
	Canonical, Name string     // the suggested canonical address (one of Addresses) and display name, never a default or one that gives an address away
	Addresses       []Address  // by first activity, then email
	Links           []Link     // each rule's evidence, in the order of Rules
}
func (c *Candidate) Rules() []Rule
type Address struct { Email, Name string; Domain DomainKind; Commits, CoAuthored int; First, Last time.Time; Repos []string } // author times; history and blamed commits
type Link struct { Rule Rule; Addresses []int; Name string; FromName bool; HandOff *HandOff } // indexes into Addresses; Name for same-name; FromName: a same-local-part made from the name
type HandOff struct { Gap time.Duration; SharedFiles int; Files []File }      // Files: the first five, by repository and path
type File struct{ Repo, Path string }                                          // Repo.Key
type Rule string       // RuleSameName, RuleSameLocalPart, RuleSameAccount, RuleHandOff
type Confidence string // ConfidenceHigh, ConfidenceMedium, ConfidenceLow
type DomainKind string // DomainMain, DomainNoreply, DomainMachine, DomainOther: what a domain says, without naming it
type RuleDefinition struct { Rule Rule; Matches, Confidence, Risk string }
func Rules() []RuleDefinition // how each rule links and how it can be wrong; every identities answer carries them
// #69: the same-name rule's key for email's display name (words lowercased: case, spacing and punctuation ignored); "" for no
// name, a default one, a bot's or an agent's, and a LoginKey. The one rule: Candidates and every report caveat that says two
// emails share a name compare by it
func (d *Directory) NameKey(email string) string
func RevealsAddress(name, email string) bool // the name holds an @, or is the address's local part (a login used as a name): a page that prints no address must not print it
// identity/withheld.go (#52): which names a page may show, decided per name over every person's address in the run, so
// no page prints on one row what it withholds on another (report-json.md, "Where emails go")
func (d *Directory) Withheld(email string) bool // its display name holds an @, or is a local part (localForms: whole, before a +tag, a noreply
                                                // login), ignoring case, of any human address the facts show; a bot's or an agent's address
                                                // withholds no name, but a bot's name that holds an @ is withheld; false without a name and for a LoginKey
func LoginKey(key string) bool                  // "@login" or "@login[bot]": a GitHub account no commit of the run links to an email (ADR 0004), the
                                                // form Reviewer mints, lowercased; "@example.com" is an address. report, mcp and metrics parse keys
                                                // with it alone (#69: knowledge_loss@1's review activity too), never by the first character

// metrics
type Definition struct {
	ID                                                       domain.MetricID
	Question, Unit                                           string
	Ranked                                                   bool
	Subjects                                                 []string // #69: the kinds of subject of the values a scan stores (none for who_knows@1, no person for
	                                                                  // ai_assisted@1): the one declaration explain refuses every other kind by, in every run
	Forge                                                    bool // reads FactSet.Forge (#38): the one declaration the report dates and caveats GitHub numbers by
	Ratio                                                    bool // its values are shares, Num of Den (durable@1, knowledge_loss@1, ai_assisted@1): the one declaration a report prints a percent by, never the unit's words,
	                                                              // a rank table included (RankSet.Percent), and the one a ranked metric's bootstrap follows (RankKind)
	Eligibility, BlindSpots, Gaming, Oracle, Validity        string
}
func (d Definition) RankKind() rank.Kind // ARCH-10: rank.Ratio for a share (Ratio), else rank.Sum; no metric declares a kind of its own, so ranks and values print in one unit
type Input struct {
	Facts  *domain.FactSet     // required, with Now
	Policy *policy.Policy      // nil: no bot or AI patterns and no [week] days, so Days must be set
	Who    *identity.Directory // nil: identity.New(Facts, Policy); else built over the same facts, Forge included (it links logins)
	Days   int                 // landed@1 window, at least 1; 0: the policy's [week] days. Compute checks it before any metric runs
}
type Metric interface {
	Definition() Definition
	Compute(in *Input) ([]domain.MetricValue, error)
}
type Ranked interface {
	Metric
	Clusters(in *Input, scope domain.Scope) (RankInput, error)
}
// RankInput: the metric owns its window and eligibility (both are part of its versioned definition), and its kind
// follows from its definition, so a caller ranks with rank.Intervals(def.ID, scope, ri.Window, def.RankKind(), ri.Data,
// ri.Options) and restates nothing.
type RankInput struct {
	Window  domain.Window // the metric's window for Facts.Now (durable@1: [now-365d, now-90d)); joins ranks to MetricValue.Window
	Options rank.Options  // MinN from the eligibility rule; Blocks for Sum; Replicates 0 = 1000
	Data    map[domain.Subject][]rank.Cluster // each person's clusters, sorted by Change; bots left out; nil when empty
}
func All() []Metric                                    // stable order, docs/metrics.md's catalog's: the registry, one list (a new metric is one file plus one line at its place)
func Lookup(id string) (Metric, error)                 // an unknown id is a *domain.UsageError
func Compute(in *Input) ([]domain.MetricValue, error) // every metric, sorted: what a scan stores
// WhoKnows answers who_knows@1 for the code under a repo-relative path prefix ("" is the root) in one repository,
// or in all when repo is ""; per query, never stored by a scan. A repo that is no key is matched by its
// redact.URL form, so a remote pasted with credentials finds its repository; a repo that matches no key either
// way wraps domain.ErrNotFound, and the error quotes only the scrubbed form.
func WhoKnows(in *Input, repo, prefix string) ([]domain.MetricValue, error)
// PersonalAIShare answers ai_assisted@1's share for one person (email: canonical) in every scope they have a
// cluster in; per query, never part of Compute or stored by a scan, because ADR 0007 shows it only to that
// person: app.Me is its only caller. A bot, or an email without clusters, gets no values.
func PersonalAIShare(in *Input, email string) ([]domain.MetricValue, error)

// rank
type Kind int // Ratio or Sum
type Cluster struct {
	Change string  // landing SHA or PR id
	Num    float64
	Den    float64 // Ratio only
	Block  int     // Sum only: w.Week(t), the window's 7-day block the change landed in, counted back from w.Until (Now
	               // for every Sum metric) and half-open like the window: block k holds [Until − 7(k+1) days,
	               // Until − 7k days), so a landing at t is in block ⌈(Until − t) / 7 days⌉ − 1, and 0 is the last 7
	               // days; in [0, Blocks)
}
type Options struct { // supplied by the ranked metric (metrics.RankInput)
	Replicates int // B; 0 means 1000
	MinN       int // eligibility: clusters per subject, from the metric's definition
	Blocks     int // Sum only: w.Weeks(), the window's 7-day blocks, ⌈(Until − Since) / 7 days⌉, the oldest one short: 13 for 90 days
}
// Intervals panics on what only a bug in the ranked metric produces, rather than publish plausible but wrong
// intervals: a Kind other than Ratio and Sum and, for Sum, an empty window, a Blocks other than the window's, or a
// Block outside [0, Blocks) in any subject's clusters, eligible or not. The options are checked even when data is empty.
func Intervals(id domain.MetricID, scope domain.Scope, w domain.Window, kind Kind, data map[domain.Subject][]Cluster, opt Options) []domain.RankInterval
func Seed(id domain.MetricID, scope domain.Scope, w domain.Window) uint64
func AUC(pos, neg []float64) float64 // Mann-Whitney, a tie counting 1/2; 0.5 when pos or neg is empty: no pairs, not chance

// report: a view that computes no metric. values.go is the one place that maps each printed
// number to the metric@version, subject, scope and Detail key it reads (spec/report-json.md#where-the-numbers-come-from).
type Model struct{ /* stillhere.report/v1: overviews (per group; every repository with --scope all or without groups), repo cards, ranks, caveats and provenance on every section */ }
type Document interface{ /* sealed: *Model, *Explain, *WhoKnows, *Me, *Validation, *Catalog, *Identities */ }
func Text(w io.Writer, d Document) error     // a terminal: 100 columns, untrusted text escaped, no emails and no name that gives one away
func Markdown(w io.Writer, d Document) error // GitHub-flavoured: untrusted text escaped, mentions and references unlinked, no emails, names that give one away or repository paths
func JSON(w io.Writer, d Document) error     // stable order; empty lists [] and maps {}, absent sections null; invalid UTF-8 as U+FFFD, NaN and ±Inf as null; written only when whole
// addresses.go (#68): all three print an email only at the published shape's places for one (redact.Place.Email: email, a person
// subject's key; text and Markdown print none of those), and every other address the redact rule finds, with the page's people as
// its vocabulary, as "(address withheld)": a commit subject's, a git message's, a directory's. The model keeps the text, which the
// MCP server withholds by its own operator; text and Markdown render a copy, decoded from the withheld JSON, when there is any.
func WholeRun(scope string, groups int) bool // a report holds the overview of every repository (scope all, or no groups): Build's rule,
                                             // which app.AIShares reads too, since only that overview shows the whole run's AI-assisted share
type People interface{ Name(email string) string; Bot(email string) bool; Withheld(email string) bool; NameKey(email string) string } // *identity.Directory
// implements it; NameKey (#69) is what every unmerged-identities caveat groups names by, the identities same-name rule's key
// #52: every person row (still-here, week, rank, made_of, who-knows) and person Subject names the person one way in every
// format, Name and NameWithheld from one call (names.go, personName): Name is the display name, the login as GitHub shows it
// ("@Zoe", the key "@zoe", #69) for a GitHub account no commit links to an email (identity.LoginKey, ADR 0004), and "" when git recorded none or when the name gives
// an address of the run away (People.Withheld; NameWithheld, json "name_withheld", true). Text and Markdown label such
// people per page, "(no name 1)" and "(name withheld 1)", in the order the page first names them: a counter, never a hash
// of the email; a name shaped like a label, or starting with a double quote, is printed Go-quoted (nameText).
type Input struct {
	Kind, Tool string // KindWeek or KindScan; the binary's version
	Run        *domain.Run // nil for week, and for a scan that stored nothing
	Facts      *domain.FactSet
	Policy     *policy.Policy
	Who        People
	Values     []domain.MetricValue
	Ranks      []RankSet // one per metric named by --rank and scope
	Landed     Landed    // landed@1's days and sort, and whether the command line set them
	History    domain.Window
	Timings    *Timings
	Scope      string    // the scan's rank scope: domain.ScopeAll (or a run without groups) adds an overview of every repository
	Forge      *ForgeRead // the GitHub data a scan run read, which app builds; nil for week, and when the reader cannot tell
	Commands   Commands   // #69: how its sentences name the commands they send the reader to; nil: the command line's (CLI)
}
// commands.go (#69): the reader's vocabulary. A hint in a caveat or a use case's error names a command through it, so an
// agent is sent to the MCP server's tools and a person at a terminal to stillhere's commands, one mechanism for both: the
// MCP server passes its own (app.Deps.Commands); ExplainInput, WhoKnowsInput and MeInput carry it too
type Commands interface {
	Metrics() string                             // the registry: CLI "'stillhere metrics'"
	Identities() string                          // suggested .mailmap lines: CLI "'stillhere identities'"
	Report() string                              // a run's numbers per repository and group: CLI "'stillhere report'"
	Ranks(metric string) string                  // a ranked metric's tables: CLI "'stillhere report --rank M'"
	Explain(metric string, kinds []string) string // explain about one of kinds; "" when the reader cannot: the CLI asks about a person only
	WhoKnows() string                            // who knows a path, who_knows@1, which no scan stores: CLI "'stillhere who-knows PATH'"
	OwnPage() string                             // a person's own page: CLI "'stillhere me --email EMAIL'"
}
type CLI struct{}                     // the command line's Commands
func CommandsOr(c Commands) Commands  // c, or CLI{} when nil
// #69: a caveat goes where the number it qualifies is (#48): every answer that shows a ranked metric's values, ranks or clusters
// carries its validity note; the multi-metric ones (NewMe, app's Changes and Highlights) through this, one caveat per note
// naming its metrics; a rank table and explain word their one metric's. NewMe adds ai-lower-bound when the page holds
// ai_assisted@1, and NewExplain when it explains one; NewWhoKnows adds unmerged-identities when two people it lists share a
// name (People.NameKey), as the report's sections, explain and me do
func ValidityCaveats(ms ...MetricInfo) []Caveat
func NoValuesCaveat(message string) Caveat // the no-values caveat in report's code, for a use case that finds an answer missing itself:
                                          // app.WhoKnows, for a path the run does not have (#71); its renderer then prints "no answer"
// forge.go: the GitHub data a run read, which dates its forge numbers (ADR 0004). ExplainInput, WhoKnowsInput, MeInput and
// IdentitiesInput carry Forge too. Every Provenance gets Forge *ForgeProvenance (json "forge"): {AsOf, Repos, Metrics} for the
// repositories in its Heads that had GitHub data (a page or a finished sync), AsOf the oldest Synced (nil while a first sync
// has not finished), Metrics the section's metrics that read GitHub data (which text and Markdown date), and nil when none had
// any. On me and explain, a forge metric's value in a group or in all puts every repository of that scope in Heads. Only a
// section that shows a forge metric's numbers gets a forge-not-synced caveat, and in a run that read no GitHub data only a
// section about that metric alone (its rank table, explain of it) does (#38). A card's or an overview's knowledge_loss@1
// counts a review anywhere in the run, so a second one names the run's other repositories without GitHub data.
type ForgeRead struct {   // per domain.RunWatermark and joined key, sorted by Repo, Key; built alike by scan and readers
	Repos   []ForgeSync
	Metrics []string      // #38: the registry's metrics that read GitHub data (metrics.Definition.Forge), which app copies in
}
type ForgeSync struct {                    // a watermark as the run read it
	Repo, Key       string     // "owner/name" and the run's Repo.Key it joined, both as the run read them (RunWatermark)
	Updated, Synced *time.Time // ForgeWatermark's, nil for its zero times
}
func OwnPageOnly(metric domain.MetricID) bool // a person's values only their own page shows: ai_assisted (ADR 0007); explain refuses them
func AIShareIn(v Value) (whole bool, key string) // #68: where a value holds an AI-assisted share: whole for ai_assisted's own values, key
                                                 // "ai_assisted_share" for surviving's copy of its scope's share; the one place, so a reader that withholds a share
                                                 // (the MCP server, by app.AIShares) withholds every copy of it
type RankSet struct { Metric MetricInfo; Scope domain.Scope; Window domain.Window; Percent bool; MinN int; Intervals []domain.RankInterval } // bots left out; Percent: metrics.Definition.Ratio
func Build(in *Input) *Model
// The read commands' answers live here too, so the renderers and the MCP server share one shape.
// Each has Provenance (run id, as-of, policy, HEAD SHAs, metric@version, when the GitHub data was synced) and Caveats:
type Explain struct { Provenance Provenance; Metric MetricInfo; Subject Subject; Values []Value; Changes []Change; Caveats []Caveat }
type WhoKnows struct { Provenance Provenance; Path, Repo string; Repos []WhoKnowsRepo; Caveats []Caveat } // owners, top authors, recent landers and reviewers (#38), bots
type Me struct { Provenance Provenance; Person Subject; Values []Value; Ranks []MeRank; Metrics []MetricInfo; Caveats []Caveat }
// #66: Metrics are the definitions of the page's metrics (NewMe keeps them from MeInput.Metrics); text and Markdown print a value
// as a share exactly when its metric's Ratio says so, as explain prints by its Metric's, so a share of den 1 is "100.0% (1 of 1)"
type Validation struct { Provenance Provenance; Labels, Labellers, Meaningful, Routine, Noise, Unmatched int; Metrics []MetricAUC; Caveats []Caveat }
type MetricAUC struct { Metric string; AUC *float64; N, Meaningful, Routine int; Beta bool } // AUC nil without both labels
type Catalog struct { Metrics []MetricInfo }
// #26 (spec/identities.md): emails only in fields named email, and a name that gives an address away withheld as "", by the
// rule every page applies (Who.Withheld, #52), so the MCP server can drop emails by field; text and Markdown print no
// address and no part of one
type Identities struct { Provenance Provenance; Scope Scope; Addresses, Bots, Shared, Oversized int; Candidates []IdentityCandidate; Rules []IdentityRule; Caveats []Caveat }
type IdentityCandidate struct { Confidence string; Guess, Commented, Chained bool; Rules []string; Name string; Addresses []IdentityAddress; Links []IdentityLink;
	Mailmap *[]string } // Commented: all but high; Mailmap (#69): MailmapLines when IdentitiesInput.Mailmap asks, else nil (json null): they are made of emails
type IdentityAddress struct { Email, Name string; NameWithheld bool; DomainKind string; Canonical bool; Commits, CoAuthored int; First, Last time.Time; Repos []string }
type IdentityLink struct { Rule string; Addresses []int; Name string; FromName bool; HandOff *IdentityHandOff }
type IdentitiesInput struct { Run *domain.Run; Facts *domain.FactSet; Scope domain.Scope; Survey *identity.Survey; Who People; Forge *ForgeRead; Mailmap bool } // Facts: the scope's;
                                                                                                                                               // Who: the run's directory, which Survey was found with
func NewIdentities(in *IdentitiesInput) *Identities // identity.Candidates' survey as an answer, with provenance (AnswerProvenance) and caveats (mailmap-at-scan)
func Mailmap(w io.Writer, id *Identities) error    // the suggested .mailmap lines, evidence as # comments; every candidate's but a high one's commented out
func MailmapLines(c IdentityCandidate) []string    // #69: one candidate's lines exactly as Mailmap writes them, which it writes through
func NewExplain(in *ExplainInput) *Explain    // values, and a ranked metric's clusters per repository as changes
func NewWhoKnows(in *WhoKnowsInput) *WhoKnows // who_knows@1's values under the path, as lists
func NewMe(in *MeInput) *Me                   // the person's values and their row in each RankSet
// For an answer app builds itself (the MCP server's, validate's) over every repository of fs:
func AnswerProvenance(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metrics []string) Provenance // dated by forge, as every section is
func AnswerCaveats(fs *domain.FactSet) []Caveat                                                          // what could make it wrong
func ForgeCaveats(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metrics []string) []Caveat        // its GitHub numbers' caveats, per metric in metrics that reads GitHub data; nil for none, and in a run that read none

// sqlite
func Open(ctx context.Context, path string) (*Store, error) // the writers' (scan, sync): creates a missing dir 0700 and file 0600; migrated, then WAL (a newer facts database, or a file that is not one, SQLite or not, is a *domain.UsageError, refused unchanged and without waiting for a lock); deletes runs left running.
                                                            // #71: a file or directory it cannot write, tried and not read off mode bits, is a *domain.UsageError that names it, before any work that would need the write.
                                                            // Every *domain.UsageError it or OpenExisting returns starts with the path, and names no Go operation: the command line prints it as it is
// #71: the read commands' (report, explain, who-knows, me, identities, validate). It creates nothing, neither a directory nor a file (SQLite's mode=rw, so no
// stat races the open): a missing file is a *domain.UsageError that names the path and says what to run. It refuses what Open refuses, migrates an older
// file (the one write a reader makes), and deletes no run left running, which is the next writer's housekeeping
func OpenExisting(ctx context.Context, path string) (*Store, error)
// #71: for an error that holds a SQLite failure a user can act on (a full or failing disk, a damaged file, a file or directory it cannot write or open, a
// lock held longer than a write waits), one sentence of what happened and what to do, without the path; "" for any other error. The command line prints
// it after the database's path in place of the error's chain
func Advice(err error) string
// *Store implements domain.FactStore (store.go, #8) and domain.ForgeStore (forge.go, #14). Inside the package every
// write goes through Store.write and every read through Store.read (store.go), forge.go's included: write waits for
// another process's lock and renews the leases of the store's runs; a statement on the writer connection itself fails
// with SQLITE_BUSY after a second.
// readonly.go (#15): the MCP server's store. Both pools connect with SQLite's mode=ro, so every write fails inside
// SQLite (SQLITE_READONLY); nothing is created, migrated or cleaned up. A missing file, a directory, another program's
// file, and a facts database older or newer than this binary are each a *domain.UsageError saying what to run.
func OpenReadOnly(ctx context.Context, path string) (*Store, error)

// github (#14): domain.ForgeSource over GitHub's GraphQL API and git over HTTPS
type Config struct {
	Host      string            // "github.com" when empty; go-gh derives the API endpoint. cli leaves it empty (#44)
	Token     string            // "" looks one up as gh does: GH_TOKEN, GITHUB_TOKEN, then gh's stored token
	Git       domain.GitRunner  // runs the mirror commands, Sync profile
	Logger    *slog.Logger
	UserAgent string            // "stillhere/<version>"
	GitURL    string            // base URL mirrors clone from; "https://" + Host when empty; https unless loopback (tests)
	Transport http.RoundTripper // nil: http.DefaultTransport; tests replay recorded responses
	Sleep     func(ctx context.Context, d time.Duration) error // nil: a timer; tests record the waits
}
func New(ctx context.Context, cfg Config) (*Source, error) // no token → *domain.UsageError
// #71: the one rule for an owner (organization or user) or repository name, which Repos, Mirror and ReconcileMirrors apply: ASCII letters, digits, ".",
// "_" and "-", neither "." nor "..". The command line checks --org with it before a sync begins its run, so a URL pasted as an organization, token
// and all, is refused before anything records it (#71)
func ValidName(s string) bool
// *Source implements domain.ForgeSource. API requests go one at a time, paced by each query's rateLimit
// cost; Retry-After and X-RateLimit-Reset are obeyed, inside a 200 too; secondary limits and 5xx back off
// with jitter; a connection whose cursor does not advance fails. Mirrors get the token only as an
// environment-scoped http.extraheader, and git follows no redirect (git-invocation.md#credentials).

// app
type Deps struct {
	Git        domain.GitRunner
	Store      domain.FactStore   // nil for week
	Forge      domain.ForgeSource // sync only
	ForgeStore domain.ForgeStore  // the same *sqlite.Store, given wherever facts.db is: sync writes the replica, scan reads it
	                              // and records what it read (its watermarks and pull requests), readers read that back, to date
	                              // and compute over the run's own GitHub data; nil: no GitHub data read, and none reported (no
	                              // forge provenance or caveat) by scan or reader alike
	Policy     *policy.Policy     // week and scan only; read use cases rebuild the run's own
	Log        *slog.Logger
	Version    string
	Commands   report.Commands    // #69: the inbound adapter's vocabulary, which every hint of an answer or an error names a command in;
	                              // nil: the command line's. The MCP server passes its tools
}
type WeekRequest struct {
	Paths, Exclude []string // Exclude: --exclude-repo globs, as for ScanRequest
	Now            time.Time
	Days           int; DaysFromCLI bool    // policy [week] days unless DaysFromCLI
	Sort           string; SortFromCLI bool // policy [week] sort unless SortFromCLI
}
type ScanRequest struct {
	Paths          []string
	Exclude        []string // path.Match globs against Repo.Name after discovery; each repo left out is a run-level Warning{Code: "excluded-repo"}
	Now            time.Time
	Jobs           int      // 0: min(12, NumCPU)
	IgnoreRevsFile string   // absolute; single-repo runs only
	Rank           []domain.MetricID
	Scope          string   // domain.ScopeGroup or domain.ScopeAll
}
type ReportRequest struct { RunID int64; Rank []domain.MetricID }                 // RunID 0: the latest complete or partial scan run
type ExplainRequest struct { RunID int64; Metric domain.MetricID; Subject domain.Subject } // the CLI sends a person; MCP any subject
type WhoKnowsRequest struct { RunID int64; Path, Repo string }
type MeRequest struct { RunID int64; Email string }                                // Email required: the CLI resolves no default (me needs --email)
type ValidateRequest struct { RunID int64; Labels []byte; LabelsName string }      // the CLI reads the file; app opens none
type SyncRequest struct { Org, Mirrors string; IncludeArchived, IncludeForks bool; Now time.Time } // Now: read once by cli; stamps the run and ForgeWatermark.Synced.
                                                                                                  // #71: the run records every option scrubbed (redact.Scrub), as a scan does
type IdentitiesRequest struct { RunID int64; Scope domain.Scope; Mailmap bool }   // the zero Scope: every repository of the run; else a group or one repository;
                                                                                  // Mailmap (#69): each candidate's .mailmap lines, for a reader that may see emails
func Week(ctx context.Context, d Deps, r WeekRequest) (*report.Model, error)
func Scan(ctx context.Context, d Deps, r ScanRequest) (*report.Model, error)
// The read side reads the latest complete or partial run unless a run id is given, with the run's own policy:
func Report(ctx context.Context, d Deps, r ReportRequest) (*report.Model, error)
func Explain(ctx context.Context, d Deps, r ExplainRequest) (*report.Explain, error) // #69: a metric about a kind of subject its Definition.Subjects
                                                                                     // leaves out is a *domain.UsageError naming its kinds and what answers instead,
                                                                                     // before any run is read (who_knows@1, stored by no scan: Commands.WhoKnows)
func WhoKnows(ctx context.Context, d Deps, r WhoKnowsRequest) (*report.WhoKnows, error) // #71: the path is scrubbed (redact.Scrub) before it is asked about or echoed. One
                                                                                       // that no collected repository asked about has (classified, touched or blamed)
                                                                                       // gets a no-values caveat: missing, never "nobody", and still no error. It states
                                                                                       // what the run holds, for every reader: with Repo, the run's other repositories
                                                                                       // that have the path; else that the run knows paths relative to a repository's root
func Me(ctx context.Context, d Deps, r MeRequest) (*report.Me, error)
func Validate(ctx context.Context, d Deps, r ValidateRequest) (*report.Validation, error)
func Metrics() *report.Catalog // the registry's definitions: the metrics command and the MCP server's metrics()
func Identities(ctx context.Context, d Deps, r IdentitiesRequest) (*report.Identities, error) // #26: suggested .mailmap merges with evidence, never applied; the identities command and the MCP server's identity_candidates(). Reads the run's facts and policy, not its metrics; a scope that names no group or repository of the run is a *domain.UsageError
func Sync(ctx context.Context, d Deps, r SyncRequest) error // #14. #71: logs at info every 100 pull requests stored, with the repository and its place ("3 of 80")
// Week and Scan return the model alongside domain.ErrNoFacts when no repository produced facts (a scan then
// stores nothing). A cancellation or a *domain.UsageError from a collector (an unreadable --ignore-revs-file) stops
// the run (runStopped); any other failure is the repository's, recorded on its RepoFacts. Scan reads the forge replica
// (ForgeStore.LoadForge) after discovery and before any git process, and a read error stops it; once collected, the
// replica's part ForgeJoin joins to its repositories is FactSet.Forge. It writes the run after computing everything:
// BeginRun, PutRepoFacts in key order, PutRunFacts, PutRunWatermarks and PutRunForge (when it read any),
// PutMetrics (every value, and every ranked metric's intervals in every rank scope, whichever --rank names), then
// FinishRun with complete or partial (a failed repository or PATH), or failed if a write fails. Ranks leave bots out
// of rank.Intervals.
// The MCP server's own use cases (overview, person, changes, highlights; ADR 0006) go in app/mcp.go, which #15
// owns. They answer from #11's report model (the overviews and repo cards), so #15 builds on #11's report types.

// app/mcp.go (#15): the MCP server's own read use cases (ADR 0006), answered from #11's report model and the ranked
// metrics' clusters, as explain reads them; their answers' JSON shape is docs/spec/mcp.md. Unexported names carry mcp….
type NamesRequest struct { RunID int64 }
type Names struct { Run domain.Run; Groups []NamedGroup; Repos []NamedRepo; People []NamedPerson; AIShares AIShares } // what names resolve against
type NamedGroup struct { Name string; Repos []string }
type NamedRepo struct { Key, Name, Group, Path string }
// app/aishare.go (#68, ADR 0007's amendment): the run's one decision about which AI-assisted shares leave the machine, which
// RunNames computes and every MCP answer reads, wherever report.AIShareIn says a share is. A share is made of the humans credited
// with a non-merge commit of a landing in its ai_assisted@1 value's window, counted as one person per block of addresses
// (identity.Blocks; aiShareCount: the fewest people the evidence allows, 0 only for no person's landings; a link only lowers it, and an empty address adds one only alone). Primary: a scope of fewer than AIShareMinPeople people is withheld; secondary,
// bottom-up over the run, its groups and their repositories (by Repo.Group), more children are withheld, the smallest first, while
// the withheld part of a scope covers 1 or 2 people, and the scope itself when no child can close it; the whole run is decided on
// only when report.WholeRun. A shown share less the shown shares below it then covers no one or at least three people.
const AIShareMinPeople = 3
type AIShares struct { Shown []domain.Scope } // sorted by kind (all, group, repo) and key; a scope not listed is withheld, so the zero value withholds all
func (a AIShares) Show(sc domain.Scope) bool   // the zero Scope is the whole run
type NamedPerson struct { Key, Name string; Bot bool; Repos []string; Lines int; Logins, ShownLogins []string } // Key: canonical email, or
// "@login", as load hands it (RunNames folds no key again: every key is canonical where it is made, #69, and load upcasts those of a run
// stored before). Logins (#69): the GitHub
// logins the run links to the person (identity.Directory.Logins), which "@login" resolves by, without case; ShownLogins: those an answer
// may print beside the person (not identity.Directory.LoginWithheld)
type OverviewRequest struct { RunID int64; Scope domain.Scope; Rank []domain.MetricID } // zero Scope: the whole run; no Rank: no rank table (a report adds the scan's)
type OverviewAnswer struct { Provenance report.Provenance; Run report.Run; Scope domain.Scope; Overviews []report.Overview;
	Repos []report.Repo; Ranks []report.RankTable; Warnings []report.Warning; Caveats []report.Caveat }
type PersonRequest struct { RunID int64; Email string }
type ChangesRequest struct { RunID int64; Scope domain.Scope; Since time.Time; Person, Me string; Titles bool }
type HighlightsRequest struct { RunID int64; Scope domain.Scope; Since time.Time; Titles bool }
type DiffRequest struct { RunID int64; Repo, Change, Path string; MaxBytes int } // Change: a landing SHA or 7+ hex digits
// ChangesAnswer, HighlightsAnswer and DiffAnswer, and their Change, ChangePerson, Contribution, TopTier,
// HighlightChange and DiffFile, carry their JSON names: what no report shows, in the shape docs/spec/mcp.md gives.
// TopTier: Of eligible people, PeopleTotal whose 95% rank interval reaches first place, People them by name unless
// they are more than a third of Of (no one stands out: a no-standout caveat).
func RunNames(ctx context.Context, d Deps, r NamesRequest) (*Names, error)
func Overview(ctx context.Context, d Deps, r OverviewRequest) (*OverviewAnswer, error) // report.Build's sections for one scope; Provenance.Metrics: every number's
func Person(ctx context.Context, d Deps, r PersonRequest) (*report.Me, error) // Me without the AI-assisted share (ADR 0007); both build through one helper, so they cannot drift
func Changes(ctx context.Context, d Deps, r ChangesRequest) (*ChangesAnswer, error) // landings with durable@1 and simplifying@1 clusters
func Highlights(ctx context.Context, d Deps, r HighlightsRequest) (*HighlightsAnswer, error) // top tiers, lasted, removed
func Diff(ctx context.Context, d Deps, r DiffRequest) (*DiffAnswer, error) // git diff-tree on the analysis profile; needs Deps.Git

// mcp (#15): the MCP server, an inbound adapter over app; docs/spec/mcp.md is its tool contract
type Config struct {
	Open        func(ctx context.Context) (domain.FactStore, error) // sqlite.OpenReadOnly; called on the first call that reads, again after a failure
	Git         domain.GitRunner // diff only; nil unless AllowSource
	RunID       int64            // 0: the latest complete or partial scan at each call
	AllowSource bool             // commit subjects and diffs reach the agent
	AllowEmails bool             // emails reach the agent; else each is the id "p-" + 10 hex digits of its SHA-256
	Me          string           // the user's canonical email: only their own answers show their AI-assisted share
	Version     string
	Log         *slog.Logger     // stderr
}
func Serve(ctx context.Context, cfg Config, in io.ReadCloser, out io.Writer) error // stdio until in closes or ctx ends; only protocol on out;
                                                                                   // a line on in that is no JSON-RPC message gets JSON-RPC's error and is skipped

// cli
type Build struct{ Version, Commit, Date string } // from -ldflags -X main.version, main.commit, main.date
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, build Build) int // the exit code

// fixture (test support, never imported by production code; repositories in ../../testdata/fixture/SPEC.md)
var Now = time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC) // the --now of every oracle
const MailmapText = "Ada Example <ada@example.com> <ada@other.example>\n"
const SiblingMailmapText = "Pia Pair <pia@example.com> <81234567+piapair@users.noreply.github.com>\n"
func Main(t testing.TB) string                                  // builds <t.TempDir()>/fixture, the main fixture
func Sibling(t testing.TB) string                               // builds <t.TempDir()>/sibling, the change-case fixture
func Mailmap(t testing.TB) string                               // writes MailmapText to a 0600 temp file
func SiblingMailmap(t testing.TB) string                        // writes SiblingMailmapText to a 0600 temp file
func Build(dir string) error                                    // BuildContext with context.Background
func BuildContext(ctx context.Context, dir string) error        // the main fixture in dir, which must be empty or absent
func BuildSibling(dir string) error                             // BuildSiblingContext with context.Background
func BuildSiblingContext(ctx context.Context, dir string) error // the sibling in dir, which must be empty or absent
// guards.go (#13): the regression guards' repositories, scanned without a mailmap
func Backdated(t testing.TB) string                             // builds <t.TempDir()>/backdated: a first-parent commit older than the scan window, above landings in it, and on top a rebased one, authored before the window and committed in it by someone else
func BuildBackdated(ctx context.Context, dir string) error      // the backdated guard in dir, which must be empty or absent
func MergedRevert(t testing.TB) string                          // builds <t.TempDir()>/merged-revert: reverts on side branches that --no-ff merges land
func BuildMergedRevert(ctx context.Context, dir string) error   // the merged-revert guard in dir, which must be empty or absent
func Binary(t testing.TB) string                                // builds <t.TempDir()>/binary (#54): files git takes for binary at some commits, one renamed by a commit older than the window, one moved out of vendor/;
                                                                // (#70) text files whose path changes role on the way to HEAD, onto prod paths and off them
func BuildBinary(ctx context.Context, dir string) error         // the binary-file guard in dir, which must be empty or absent
// discovery.go (#4): the discovery and clone-health repositories, as structs of absolute paths
const DiscoveryToken = "TOKENVALUE"                             // planted in remote URLs; TestDiscover asserts no output holds it
const DiscoveryWidgetRemote = "https://user:" + DiscoveryToken + "@github.com/example/widget.git"
type DiscoveryRepos struct{ Root, Upstream, WidgetA, WidgetB, Other, Mirror /* ... one field per path */ string }
type CloneHealthRepos struct{ Upstream, Full, Shallow, Partial, Extension, OtherPromisor, NotPromisor string }
func Discovery(t testing.TB) *DiscoveryRepos                    // builds <t.TempDir()>/discovery
func BuildDiscovery(ctx context.Context, dir string) (*DiscoveryRepos, error)
func CloneHealth(t testing.TB, promisor string) *CloneHealthRepos // the partial clone's origin is promisor
func BuildCloneHealth(ctx context.Context, dir, promisor string) (*CloneHealthRepos, error)
// mergemethods.go (#38's review): how GitHub's merge methods land pull requests, for who_knows@1's reviewers
func MergeMethods(t testing.TB) string                          // builds <t.TempDir()>/merge-methods: a rebase merge's two commits in one committer second, and two squashes in another
func BuildMergeMethods(ctx context.Context, dir string) error   // the merge-methods guard in dir, which must be empty or absent
```

## Run model

Every `scan` is a **run**: a snapshot of facts at one set of HEADs, under one policy and one mailmap, with the metric values computed from them.

- **A run reads each repository as it stood at `--now`** (#70, [ADR 0012](../adr/0012-a-run-reads-each-repository-as-it-stood-at-now.md)). Every window is measured back from `--now` and none extends past it, so the code the run blames, classifies and sizes must be the code of that moment: a run that read a newer head would credit lines written after `--now` to people whose landings no window holds yet, and `knowledge_loss@1` would count them as gone. The run's HEAD in each repository is the newest first-parent commit of the default branch, as discovery resolved it, whose committer time is before `--now`, strictly, as the windows are half-open (`collect.SnapshotAt`). Newest means nearest the branch's head on its first-parent chain, the chain landings are counted on, not the latest committer time: what lies below that commit is read with it, whatever its date, as a wrong clock or a rebase that kept old committer dates can leave a chain. Blame, classification, sizes, the ignore-revs file and the history all read that commit, `Repo.Head` records it, and so do the provenance `heads` of every section. When it is not the branch's head, the repository gets a `head-after-now` warning, a caveat wherever its numbers are, naming the commit read and how many landings came at or after `--now`. When landings below it are dated at or after `--now` (`Snapshot.Below`), blame counts their lines while no window counts them, so their authors can look gone: a scan that blamed the repository gives it a `dated-after-now` warning, also a caveat wherever its numbers are, counting them. The run then describes no single moment, and says so rather than claim `--now`. A repository whose branch has no commit before `--now` yields no facts: it is left out of the run with a run-level `no-commit-before-now` warning, like a repository `--exclude-repo` leaves out, and the run is not partial. Without `--now`, the wall clock, nothing changes unless a commit is dated in the future. `week` reads the same commit for the same `--now`, so the two reports agree on the code they describe.
- **Git-derived facts are stored per run**, not once per commit. The mailmap and the policy can change between runs, and every number in a run must come from one HEAD set, one policy and one mailmap. The cost is repeated rows across runs, bounded by retention. The store is disposable, so this is a two-way door.
- **Publishing** is a status flip. A run is written as `running` and becomes `complete` (or `partial` when some repo failed) only after its last row. Readers select complete and partial runs only, so nobody sees half a run: every `FactStore` read method returns `ErrNotFound` for a running or failed run. A run left `running` by a crash is deleted by the next writer's open (`sqlite.Open`, a scan's or a sync's; a read command's leaves it, #71) once its lease has lapsed. The store that began the run holds the lease: every write it makes renews it, a sync's forge writes included, and it lapses after a day without one, so an open deletes a running run only after its writer has written nothing for a day.
- **Retention.** When a run finishes, the store keeps, among the runs of its kind, that run, the newest 8 published runs and the newest published run of each of the 12 most recent months that have one (by `Now`, as restic's `--keep-last` and `--keep-monthly` count), and deletes the others, failed runs included.
- **The policy travels with the run.** `Run.PolicySource` holds exactly the bytes `PolicyRef.SHA256` hashes. Every reader of a stored run (`report`, `explain`, `who-knows`, `me`, `validate`, `mcp`, and a new metric version computed over old facts) rebuilds that policy with `policy.FromSource` and checks its `Ref` against `Run.Policy`. Today's `stillhere.toml`, or a newer packaged default, never changes a stored run's numbers; only `week` and `scan` load a policy file.
- **Forge facts** (M2) are a replica of GitHub, upserted by node id with a watermark per repo, which outlives runs; each run keeps the part it read, its watermarks and its pull requests ([ADR 0011](../adr/0011-a-run-keeps-the-github-facts-it-read.md)).
  - **Scan.** `scan` reads the replica once, with `ForgeStore.LoadForge(now − 365 days)`, after discovery and before any git process starts. Once collection has run, its own part of the replica is its GitHub data, `FactSet.Forge`: the forge repositories `domain.ForgeJoin` joins to one of its repositories' origins (the join every metric that reads GitHub data counts by), their watermarks (a zero one for such a repository the replica holds no page of) and their pull requests. While the run is running, it records with `ForgeStore.PutRunWatermarks` each watermark with the repository's name and the run's repositories it joined (`domain.RunWatermark`), and with `ForgeStore.PutRunForge` the repositories and their pull requests, whole. Another organization's repositories in the replica are none of the run's, and neither is a repository whose collection failed: it adds nothing to the run's numbers, its GitHub ones included. An empty replica is normal: the run has no forge facts, and every forge-based value is absent, never zero.
  - **A replica that cannot be read stops the scan**, before it collects anything. It is the facts.db the run would be written to, so the run could most likely not be stored either, and a run published without the GitHub data the replica holds would read as "not synced": plausible, but wrong.
  - **Readers.** `report`, `explain`, `who-knows` and `me` (the read use cases' shared `load`) read the watermarks back with `RunWatermarks`, with the names and the join the run recorded, and show in every section's provenance when each repository's GitHub data was synced (`Synced`; [ADR 0004](../adr/0004-github-behind-sync-boundary.md)); and they read the run's GitHub data back with `RunForge` into the stored run's `FactSet.Forge`, so what they compute from it, such as `who_knows@1`'s recent reviewers for any path, comes from the run's own snapshot, as its git facts do. They never read the replica, which a sync renames and replaces, so a later sync, a rename or a new review included, moves none of a stored run's answers or "as of" times. What they compute counts by the join the run recorded (`ForgeFacts.Join`), never one made again. A run recorded before schema version 3 kept no join: its readers report no GitHub data for it rather than guess. One recorded before version 4 kept its watermarks and no pull requests: its pages date the GitHub numbers it stored by those watermarks, and `who-knows`, which computes reviewers from the pull requests when the run is read, says nothing about GitHub for it rather than list no reviewers as if there were none; the log says to scan again. One stored before #69 kept a GitHub login's case in its keys (`@Zoe`), where its readers fold what they are handed and what they compute from its pull requests (`@zoe`): `load` upcasts its values and ranks as keys are made now (`identity.CanonicalKey`), in a copy, so its keys join theirs again without a migration, unless it holds one login in two cases, an account it split and ranked as two, which stays as stored while the log says to scan again. `identities`, which dates the run's GitHub data and computes nothing from it, reads the watermarks alone. A section that shows a forge metric's numbers gets a `forge-not-synced` caveat when its collected repositories have no GitHub data, or only part ([report-json.md](report-json.md#caveats)); in a run that read none at all, only a section about that metric alone (its rank table, `explain` of it) does, so a git-only report says nothing about GitHub.
  - **Ranks are stored too.** The stored metric values are the run's forge numbers, and so are its ranks: a scan ranks every ranked metric in every rank scope and stores the intervals, whichever ones `--rank` names (it picks the tables the scan's report prints). Readers use the stored ranks, and bootstrap only a table the run did not store, such as a newer metric version's (`app.rankAll`): over the run's own facts and GitHub data, seeded from the metric, scope and window (`rank.Seed`), so it is the table a scan by the same binary would have stored. Storing the ranks beats both other ways a reader could answer: ranking over today's replica would put, in one table, ranks from one state of the replica beside values (and bot rows) from another, and refusing would make a user scan again, blame and all, for a rank the scan stores in well under a second ([ADR 0010](../adr/0010-rank-intervals-bootstrap-per-kind.md#consequences): 250 ms for a ratio and 65 ms for a sum over 500 people). A run that keeps its pull requests could rank its GitHub data again from them, alike; the stored ranks also serve runs from before it did.
  - **A newer metric version** computed over a stored run's facts gets that run's own GitHub data, as it gets its git facts and policy.
  - A sync is recorded as a run of kind `sync` (no policy, no facts of its own), so its status is kept like a scan's.
- **Metric values** are stored with the run, keyed by `metric@version`. A new metric version can be computed over an old run's facts with `LoadFacts`, which is why `LoadFacts(PutRepoFacts(x)) == x` is tested.

## Command surface

Global flags: `--db` (default `$STILLHERE_HOME/facts.db`, where `STILLHERE_HOME` defaults to `~/.stillhere`), `--policy FILE`, `--mailmap FILE`, `--log-level`, `--format text|md|json` on every command that prints a report. The root command alone also takes `--version` (`-v`), which prints the first line of `version` (#71).

`--db`, `sync github --mirrors` and `STILLHERE_HOME` take a local path. One that is a URL or holds a credential (`redact.Scrub` would change it) is a usage error, quoted scrubbed, before anything is created or recorded: it is a remote pasted into the wrong place, and as a path it would name a directory after its token, which a scan of that directory would store and print (#71).

The policy is `--policy FILE`, else `./stillhere.toml`, else the packaged one, and the organisation mailmap `--mailmap FILE`, else `./.mailmap` (#71). The working directory is often a clone's root, so a file there may be the clone's: it must be a regular file, not a symbolic link (a link to `/dev/zero` would be read without end, one to a FIFO would hang), and the log says at info which one was used; a file a flag names may be a link. A policy file is read only up to 1 MiB.

| Command | Reads | Writes | Milestone |
| --- | --- | --- | --- |
| `week PATH...` `[--now] [--days N] [--week-sort lines_added\|commits] [--exclude-repo GLOB]... [--ignore-revs-file F]` | git | nothing; `--ignore-revs-file` is accepted for symmetry with scan and ignored, with a warning: git log has no such option | M0 |
| `scan PATH...` `[--now] [--jobs N] [--ignore-revs-file F] [--rank METRIC]... [--scope group\|all] [--exclude-repo GLOB]...` | git, the forge replica in facts.db | a run in facts.db, which it creates when missing and must be able to write: an unwritable file or directory is a usage error before any git work (#71) | M1 |
| `report [--run ID] [--rank METRIC]...` | facts.db | nothing, but an older facts.db is migrated (#71) | M1 |
| `explain METRIC --person EMAIL [--run ID]` | facts.db | nothing, but an older facts.db is migrated | M1 |
| `who-knows PATH [--repo KEY] [--run ID]` | facts.db | nothing, but an older facts.db is migrated | M1 |
| `me --email E [--run ID]` | facts.db | nothing, but an older facts.db is migrated | M2 |
| `metrics` | the registry | nothing | M1 |
| `identities [--run ID] [--format mailmap\|text\|md\|json]` | facts.db | nothing, but an older facts.db is migrated | M2 (#26) |
| `validate --labels FILE [--run ID]` | facts.db, a labels CSV | nothing, but an older facts.db is migrated | M2 |
| `sync github --org ORG [--mirrors DIR] [--include-archived] [--include-forks]` | GitHub | mirrors, forge facts; facts.db as for scan, and `--org` checked with GitHub's name rule before the run begins (#71) | M2 |
| `mcp [--run ID] [--me EMAIL] [--allow-source] [--allow-emails]` | facts.db, read-only; git for `diff` with `--allow-source` | nothing | M4 |
| `policy` | `--policy`, `./stillhere.toml` or the packaged policy | nothing: it prints the policy week and scan would use as a complete file, the packaged deny list written out (`stillhere policy > stillhere.toml`; an empty `./stillhere.toml`, which that redirection makes before stillhere starts, counts as absent) (#71) | M2 (#71) |
| `version` | | | M0 |
| `completion bash\|zsh\|fish\|powershell` | | nothing: it prints the shell's completion script (cobra's own command; the release archives carry the bash, zsh and fish ones) | M0 |

The read commands (`report` to `validate`) never create facts.db or its directory (`sqlite.OpenExisting`): a missing one is a usage error that names the path and says what to run, where a new, empty database would send the user to scan again after a mistyped `--db`. The one write they make is migrating a facts.db an older stillhere wrote, which they could not read otherwise.

Exit codes are in [report-json.md](report-json.md#exit-codes): 0 when any repo produced facts, 1 when none did, 2 for a usage error, death by the signal (128 + n) when interrupted.

## Conventions

### Errors and logging

- Wrap with `%w` and the operation that failed. Exit codes come from typed errors (`*domain.UsageError`, `domain.ErrNoFacts`), never from string matching.
- A repository is a bulkhead: its failure becomes `RepoFacts.Status = "error"` and the run continues.
- A path that discovery could not turn into a repository has no `Repo.Key`, so it cannot be a `RepoFacts`. It is a `Discovery.Failed` entry: `week` and `scan` turn each one into a run-level `Warning{Code: "path-failed", Message: Path + ": " + Message}`, which `PutRunFacts` stores and the report lists; only the path is cut to one stored line (`redact.Message`), so a long path leaves room for the reason (#71). Such an entry, like a repository with status `error`, makes a scan run `partial`. Exit codes do not change ([report-json.md](report-json.md#exit-codes)): a run is exit 1 only when no repository produced facts.
- `log/slog` to stderr. Info level carries repo names, counts, durations and skip reasons, and never emails, tokens or remotes with userinfo. Debug may print emails. A value that can carry a path or a URL the user typed (`--db`, `--mirrors`, a PATH, a mirror under them) is scrubbed (`redact.Scrub`) where it is logged, and the command line's handler scrubs every value again: the one place every line passes, so a line that forgets cannot leak a token (#71).
- The error line (#71): a command prints at most one, `stillhere <command>: <message>`, scrubbed, with each line of the message as one clause. It is the error's whole chain, except for three kinds of failure whose chain is internal detail, which goes to `--log-level debug` instead: `interrupted` when a signal stopped the command; for a facts database that holds no such run or person (`domain.ErrNotFound`), the database's path and the use case's own sentence, the outermost context, which says what to do; and for a SQLite failure the user can act on, the database's path and `sqlite.Advice`. The words never choose the exit code, which comes from the error's type.
- No `panic` outside programmer errors, no global mutable state, and no `init()` beyond compiling constant patterns.

### Concurrency

- `golang.org/x/sync/errgroup` with `SetLimit` for fan-out, and one `collect.BlameSlots` (a `golang.org/x/sync` semaphore) shared across repos, so the number of git processes stays bounded on large machines.
- Every goroutine is waited for; every I/O call takes the caller's `context.Context`; `cmd/stillhere` cancels it on SIGINT, SIGTERM and SIGHUP (unless SIGHUP is ignored, as under `nohup`), then ends with that signal.
- Large git outputs are streamed (`GitRunner.Stream`), never buffered whole. Line readers allow long lines (generated files have megabyte lines).

### Determinism

- Time comes from `--now` (default: the wall clock in UTC, read once in `cli`, which also stamps a sync), and every number reads it alone: the windows, the commit each repository is read at, the metrics, the ranks and the as-of every page prints. The clock is read elsewhere only for what no number depends on: `timings` and log durations (`app`, `gitcli`, `mcp`), a run's start and finish times (`app` and `sqlite`, which a report shows only in `timings`), `sqlite`'s leases and lock waits, `github`'s rate pacing and its clean-up of what a crashed clone left, and `gitcli`'s timeouts.
- Every slice is sorted before it is stored or printed, map keys are sorted, and bootstrap seeds derive from inputs. Two runs on the same inputs produce byte-identical JSON once `timings` is removed.
- Inside the core an empty slice or map is `nil`, never an allocated empty one: a root commit's `Parents`, a repo without warnings, a `Detail` with no context numbers. Producers return `nil` and the store loads `nil`, so `LoadFacts(PutRepoFacts(x)) == x` is plain `reflect.DeepEqual`. Renderers print `nil` as `[]` or `{}`, never `null` ([report-json.md](report-json.md#rules-that-carry-forward-whatever-the-format)).

### Security

- Treat author names, paths and commit subjects as untrusted input: `html/template` for any HTML, and no names in shell strings.
- `facts.db`, mirrors and temp files: directories 0700, files 0600.
- Credentials only through `GitCmd.Env` on the Sync profile ([git-invocation.md](git-invocation.md#credentials)).

### Commits and branches

- One issue, one branch (`m<milestone>/<issue>-<slug>`), one pull request that says `Closes #<issue>`.
- Conventional Commit subjects (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:`, `refactor:`). GoReleaser groups the changelog by them, and `change_type@1` will read them.
- Never `git add -A` or `git add .`; add paths by name. `plan.original.md` is local-only and must never be committed.

### Working in parallel

Issues are built in parallel branches, so a shared package or file needs a rule that keeps two branches from colliding, or from compiling apart and failing only at merge.

- **`collect` is shared by #4, #5, #6 and #7.** Each collector's unexported identifiers carry its prefix (`discover…`, `classify…`, `history…`, `blame…`, and #70's `snapshot…`). The readers every collector needs are in `scan.go` (`newScanner`, which allows long lines, `scanNUL` for `-z` output, and `nonBlank`, the one non-blank predicate that the history and blame collectors both count with), and the test helper in `collect_test.go` (`newRunner`, a real `gitcli.Runner`). The co-author reader `coAuthors` is in `coauthors.go`: #6 implements it, and #7 calls it for the commits blame attributes lines to, which can be older than any history window. The package has at most one `TestMain`, and it goes in `collect_test.go`.
- **Files that belong to one issue.** #14: `domain/forge.go`, `internal/github`, `sqlite/forge.go`, `sqlite/migrations/0002_forge.sql`, `cli/forge.go`, `cli/sync.go`, `app/sync.go`, `metrics/reviews.go` (registered with one line in `metrics.All`). #15: `app/mcp.go`, `cli/mcp.go`, `internal/mcp`, `sqlite/readonly.go`, `docs/spec/mcp.md`, `plugins/stillhere`. #26: `identity/candidates.go`, `app/identities.go`, `report/identities.go`, `cli/identities.go`. #38: `identity/reviewer.go`, `sqlite/forgesnapshot.go`, `sqlite/migrations/0004_forge_snapshot.sql`, `app/reviews_oracle_test.go`, `fixture/mergemethods.go`. #5: everything in `policy` but `types.go`, which is this file's contract. Where such a file shares a package with another issue's, its unexported identifiers carry a prefix, as in `collect`: `forge…` and `snapshot…` in `sqlite`, `reviews…` in `metrics`, `mcp…` and `aiShare…` in `app`, `review…` in `app`'s tests and `readOnly…` in `sqlite`.
- **A stub another branch calls.** When one branch calls a use case another builds in parallel, the signature lands here first, as a stub that returns an error naming its issue, and the caller's test skips that one call until both merge. The MCP server's `identity_candidates()` (#15) was built this way against `app.Identities` (#26), and `report.Identities` was a `Document` from the start, so the server serves whatever #26 put in it (Consumer-Driven Contracts: Ian Robinson, "Consumer-Driven Contracts: A Service Evolution Pattern", 2006). The walking skeleton's last stub went with it, and so did `domain.ErrNotImplemented`.
- **Shared documents.** Each issue edits only the sections its scope names: in [metrics.md](../metrics.md), the entries of the metrics it builds or derives oracles for; in this file, the signatures it changes; in [report-json.md](report-json.md), the shape (#11). Where two branches still touch the same lines, #13 resolves them when it merges, as it does `go.mod` and `go.sum`.

## Tests

Tests sit at stable boundaries, so a refactor does not rewrite them (Kent Beck, "Test Desiderata": tests should be structure-insensitive). They run real git; no test mocks git. The fixture oracles are the contract. A metric without an oracle does not ship, and no test exists outside this list without a reason in its pull request.

| Test | Package | Asserts |
| --- | --- | --- |
| `TestWeekOracle` | `app` | the week table in the fixture spec, people and bots apart, and the `--days 1` window against `[week] days = 7`: `window.days` 1, `days_from` `cli`, `since` `2026-10-06T00:00:00Z`, and `history`, the window the run collected, equal to it |
| `TestStillHereOracle` | `app` | the still-here table, people and bots apart, and the concentration walk |
| `TestChangeOracles` | `app` | `durable@1` and `simplifying@1` per person and per landing, with their rank tables and eligibility, reverts and AI-assisted tags on both fixtures; `knowledge_loss@1` (and `explain` of it about a person refused with what answers instead, #69) and `who_knows@1` on the main fixture, where who-knows of a path no repository has is missing with a `no-values` caveat that states what the run holds, never "nobody" or one reader's advice (#71), and with a replica written by hand their reviews (#38): a reviewer who keeps an area's knowledge from counting as lost, with a comment on her own open pull request, recent reviewers of a path (one known only as `@login`, whose login two pull requests carry in two cases, one subject `@zoe` named `@Zoe` (#69); not the author, a bot, a review outside each window, or a squash read as a rebase), on the merge-methods guard a rebase merge's commits in one committer second and a squash that takes no other pull request's landing in its second, a stored run that a later sync leaves unchanged, and one whose keys kept a login's case (`@Zoe`, as a stillhere before #69 stored it) that `explain` reads as `@zoe`; `surviving@1` credit on the sibling, where an agent's blamed lines go to its human co-author; and the regression guards: a backdated first-parent commit hides no landing below it; a rebased one lands, and is dated, by its committer time, and is credited to its author; and a revert merged with `--no-ff` is linked to the merge landing, which neither change metric counts, while `surviving@1` keeps the lines the revert restored; and a change git takes for binary leaves both of `durable@1`'s sums, and `ai_assisted@1`'s cohort ratio, with the lines blamed to its commit from its path, through a rename the history never collected and from a path with no role, while its commit's other changes and the file's text commits, before and after its binary ones, keep theirs; and both sums classify a line by the path it had in its commit (#70), so text files moved onto `prod` paths from `vendor/`, `tests/` and a name with no role keep no line their commits did not add, a `prod` file moved into `tests/` keeps its lines and one moved under `vendor/`, which blame does not read, keeps none; a run reads each repository as it stood at `--now` (#70): the main fixture at `--now 2026-03-15` reads its newest landing before then, records it as its head with a `head-after-now` caveat, gives `knowledge_loss@1` 0 / 18, and renders as the same repository cut back to that commit does, while at a `--now` before its root commit it yields no facts and a `no-commit-before-now` warning, and the backdated guard at `--now 2026-07-01` reads its backdated commit and gives a `dated-after-now` caveat for the 1 landing below it dated after `--now`; and the identity candidates of each fixture, without its mailmap and with it, whose mailmap form changes nothing in `git check-mailmap` until its lines are uncommented |
| `TestDeterminism` | `app` | two scans of both fixtures as one group, into separate facts.db files, give byte-identical JSON without `timings`, and identical text and Markdown; `report` on the stored run reproduces the scan; `me` prints Gina's own AI-assisted share of 0 of 1 as `0.0% (0 of 1)`, by its metric's declaration, as `explain` prints a share (#66); a token planted in a URL given as a PATH, in its userinfo, query or fragment, is in no byte of facts.db or its `-wal` and `-shm`, no rendering and no log line, nor one pasted into `sync github`'s `--org` or `--mirrors`, or into `--db`, each of which the command line refuses before a run begins with an error line that holds none and gives its reason, nor into `--org` and `--mirrors` of a sync run GitHub refused, which records its options scrubbed, nor given to `who-knows` as its PATH (#71); with the cli's Deps (the store as both ports) and a replica that holds the fixture's forge repository beside another organization's, the run keeps only its own repository's watermark, with its name and join, `reviews_given@1` counts its reviews in scope `all`, the group and the repository, a review 7 days before `Now` in week block 0 and one at the window's start in block 12, every value the run stores is about a kind of subject its metric declares (`metrics.Definition.Subjects`, which `explain` refuses every other kind by, #69), and `report` reproduces the scan after a later sync renamed the repository; and a repository written with `git fast-import` (#52), whose display names are a login, the same name on another address, an address, none twice, a label's shape, two bots', one name written two ways on two addresses and a default name on two (#69), gives the same bytes over two scans and `report`, labels included, the report's and who-knows' `unmerged-identities` caveat naming the name written two ways once, counting jdoe's and leaving the default out, no text or Markdown page (who-knows included) holding a local part a name would give away or a domain, two `(no name N)` labels and four `(name withheld N)`, the label-shaped name printed quoted, and JSON names withheld on both of jdoe's addresses and for the bot named by an address, not for `dependabot[bot]`; and a commit subject that names an address no one in the run has and jdoe's, which `explain` prints in text, Markdown and JSON with each address as `(address withheld)` (#68) |
| `TestDiscover` | `collect` | duplicates, bare repos, scp vs HTTPS remotes; a directory with no repository among its children is no group but a failure that says what to pass: two subdirectories that hold repositories, named before the work tree it is inside, and a symbolic link to a FIFO there is never opened (#71); a planted token appears in no output |
| `TestCloneHealth` | `collect` | shallow and partial clones warn and lose only the facts they should |
| `TestAttributes` | `collect` | `check-attr --source` on a work tree and a bare clone: the scanned commit's attributes, not the checked-out ones or `attr.tree`'s, matched case-sensitively under `core.ignorecase=true`; an empty value is on; an attribute that settles a generated header; a deleted path classified by Head's attributes; a header sniff that no attribute changes (`-diff`, `binary`, an uncommitted edit), with each of its rules, prose, a generator program and a generator script's string template that writes an rST comment (#70) that stay `prod`, a Haskell module whose generated header opens a `{-` comment, as proto-lens writes it, and the line-40 limit; a symbolic link and a submodule unclassified. History (scan) and Blame on the same pair, under the repository config that each pin of [git-invocation.md](git-invocation.md#repository-settings-that-change-facts) overrides: the same facts in both clones, and the expected ones, with no file binary although Head, the checkout and `attr.tree` each mark files `-diff`, a CRLF-to-LF rename followed, the indent heuristic's owner, a UTF-8 author name, a co-author, a root commit's diff, a gitlink's row and a decomposed path blamed; and no textconv driver or filter run |
| `TestNumstatRename` | `collect` | the rename path forms |
| `TestBlamePorcelain` | `collect` | `--line-porcelain` and `--porcelain` parse to the same counts and the same path in each commit (`OrigPath`), a commit that blame reached through two paths included |
| `TestClassify` | `classify` | roles, role precedence, test directories for code and configuration only, `too_big`, segment matching, the deny list's minimum contents and what it leaves out, Linguist's attribute values, no role for a link or a submodule |
| `TestPolicy` | `policy` | a missing table or key exits 2; `[week] days` is the file's, in its hash, and 1 to 36500 (the `--days` override itself is `TestWeekOracle`'s); an extension holds one dot; the hash covers the deny list, and the packaged files hold no CR; `FromSource(p.Source)` has `p.Ref()`; every deny line compiles and the file names RE2, Linguist (MIT) and the upstream commit; the deny prefilter never changes an answer; the packaged policy written out as a file (`File`, #71) parses to the same rules with every deny pattern in order, keeps Linguist's license, and a user file writes out as it was read |
| `TestRankIntervals` | `rank` | reproducible intervals; ineligible subjects unranked; a sum's week blocks checked against its window |
| `TestAUC` | `rank` | the validity check's statistic: a hand-counted example with ties, 1 and 0 for perfect separation, 0.5 when either class is empty, and the value counting every pair gives |
| `TestRedact` | `redact` | no credential survives `URL`, `Scrub` or `Message` (userinfo, scp-like and malformed remotes, query strings and fragments, a URL given as a path and made absolute with any of those, quotes, the 180-rune cut); text that holds none is unchanged, a Windows path with a `#` included |
| `TestRunner` | `gitcli` | per-user and system attributes change neither numstat nor `check-attr`; no askpass, signature or external diff program runs; an inherited `GIT_DIR` changes nothing; a timeout or cancellation kills git's process group and is reported as such even when the reader fails first on the truncated output; a helper that left the group cannot hold `Stream` |
| `TestStoreRoundTrip` | `sqlite` | `LoadFacts(PutRepoFacts(x)) == x`, a zero loading as +0; `Run(id)` returns the run as begun and finished, policy source included; `RunWatermarks` returns the last `PutRunWatermarks` of the run, names and joins included, and a watermark that joins nothing is refused; `RunForge` returns the last `PutRunForge` of the run with its watermarks and its join (#38), and a pull request of a repository not the run's is refused; readers never see a running or failed run (`ErrNotFound`, `RunWatermarks` and `RunForge` included) |
| `TestStoreLifecycle` | `sqlite` | `Open` creates a missing directory 0700 and files 0600, in WAL mode; it refuses another program's SQLite file, a file that is not SQLite and a newer facts database with a `UsageError`, without waiting for a held lock and leaving the file byte for byte, and a file or directory it cannot write with one that names it (#71); `OpenExisting` creates no directory or file for a missing path, refuses it with a `UsageError` that names it, refuses what `Open` refuses, reads what a writer wrote without deleting a run left running, and migrates an empty file (#71); first opens racing in separate processes all succeed; a run left running is deleted once its store has written nothing for a day, and not before; retention and `LatestRun` match a model of the run-model rule across gaps, year-long pauses, ties and backfills, and no table with a `run_id` keeps a row of a deleted run, the GitHub data a run kept included (#38) |
| `TestGitHubSync` | `github` | recorded responses ([testdata/github](../../testdata/github/README.md)) become the expected repositories and pull request pages, through rate limits (a 403 with Retry-After, a nearly spent budget, and a spent budget and a secondary limit that GitHub reports inside a 200), a 502 and nested follow-ups; a mirror clones and fetches from a local git server that takes only the token, follows a rename by node id, moves to `.stale` and back, and never follows a redirect to another host; the token is in no fact, mirror file, log or error; the pages the passes return, stored through `sqlite.Open` and `PutForgePage`, come back from `LoadForge` as the last page that carried each pull request said, reviews in submission order, and from a `since` between the passes only the pull requests updated at or after it; `reviews_given@1` over what `LoadForge` returns matches its oracle in scope `all`, the group and the repository, another organization's repository and a failed one counting nowhere (M2) |
| `TestMCPTools` | `mcp` | each tool through the SDK's in-memory client (M4); and AI-assisted shares that add up, against the local report's: no answer, alone or less another, gives back a withheld share, a group's, a repository's or with `--scope all` the whole run's, nor surviving@1's copy of it (`ai_assisted_share`), and one person's three unmerged addresses count as one (#68) |
