# Code map and contracts

Status: v1 · 2026-10-14 · the layout, package contracts and conventions every change follows · product intent in [../product.md](../product.md), system design in [../architecture.md](../architecture.md)

Read this before touching code. It names every package, what it owns, what it may import, and the signatures between packages (the idea is Aleksey Kladov's "ARCHITECTURE.md"). Signatures here are contracts: a change to one lands in the same commit as the change to this file.

## Layout

| Package | Owns | Kind |
| --- | --- | --- |
| `cmd/stillhere` | `main`: signal handling, then `cli.Execute` | entry point |
| `internal/cli` | Commands, flags, exit codes, and the composition root (the only place adapters are built and wired) | inbound adapter |
| `internal/app` | Use cases: week, scan, report, explain, me, who-knows, validate, sync, mcp | application |
| `internal/collect` | git output → facts: discovery, clone health, tree and attributes, history, blame | anti-corruption layer for git |
| `internal/domain` | Fact types, metric value types, port interfaces, typed errors | core |
| `internal/policy` | The policy model, embedded default policy and deny list, parsing, hashing | core |
| `internal/classify` | The ordered path classifier | core |
| `internal/identity` | Display names, bots, AI-assisted tags, who gets credit | core |
| `internal/metrics` | The metric registry and every metric version | core |
| `internal/rank` | Cluster bootstrap, rank intervals, eligibility, AUC | core |
| `internal/redact` | URL scrubbing and message truncation | core |
| `internal/report` | The report model (`stillhere.report/v1`) and its text, Markdown and JSON renderers | core |
| `internal/gitcli` | `domain.GitRunner` over the `git` binary: hardening, profiles, version floor | outbound adapter |
| `internal/sqlite` | `domain.FactStore` over `modernc.org/sqlite`, embedded migrations | outbound adapter |
| `internal/github` | GitHub GraphQL/REST and managed mirrors (M2) | outbound adapter |
| `internal/mcp` | The MCP server (M4) | inbound adapter |
| `internal/fixture` | Builds the fixture repositories of [../../testdata/fixture/SPEC.md](../../testdata/fixture/SPEC.md) for tests | test support |

### The dependency rule

Dependencies point inward, toward the core (ports and adapters: Alistair Cockburn, "Hexagonal Architecture").

- **Core** packages import the standard library and other core packages only. Never `os/exec`, `net`, `net/http`, `database/sql`, a SQL driver, or cobra. `policy` may import the TOML parser. Core code is a functional core: plain functions over facts, testable without git or a database (Gary Bernhardt, "Boundaries").
- **`collect`** imports core. It runs git only through `domain.GitRunner`, never `os/exec`, and never imports an adapter.
- **`app`** imports core and `collect`, and reaches the outside only through ports. It never imports an adapter, `os/exec`, `database/sql` or cobra.
- **Adapters** import core. They never import `app` or `cli`.
- **`cli`** is the composition root (Mark Seemann, "Composition Root"): it builds the adapters and hands them to `app`.

`.golangci.yml` enforces the rule with depguard, so a violation fails CI. That makes it an architectural fitness function (Ford, Parsons and Kua, *Building Evolutionary Architectures*, ch. 2). A change that needs an exception edits this file and the lint config together.

## Domain types (`internal/domain`)

Facts are plain values with no behaviour beyond small helpers. Times are UTC. Slices are sorted before they leave a package (see [Determinism](#determinism)).

```go
// Repo is one repository as discovered for a run.
type Repo struct {
	Key     string // dedupe and storage key: the scrubbed origin URL, else "local:" + Name
	Name    string // directory basename without a trailing ".git"
	Group   string // group name; "" when the repo was named directly
	Path    string // absolute path
	Bare    bool
	Remote  string // scrubbed origin URL; "" when there is no origin
	Branch  string // default branch (spec/discovery.md#default-branch)
	Head    string // full SHA the branch resolved to; every later command names it
	Shallow bool
	Partial bool
}

type Group struct {
	Name  string   // basename; "-2", "-3" suffixes for duplicates
	Path  string
	Repos []string // Repo.Key, sorted
}

type Warning struct {
	Code    string // duplicate-repo, shallow-clone, shallow-window, partial-clone, numstat-rename, ...
	Message string // already scrubbed
	Repo    string // Repo.Key; "" for run-level warnings
}

type Role string // prod, sql, markup, iac, doc, test, migration, seed
type Skip string // denied_path, too_big, generated_header, unclassified

// PathClass is the classifier's verdict on one path.
type PathClass struct {
	Path string
	Role Role  // "" when skipped
	Skip Skip  // "" when a role was assigned
	Size int64 // bytes at HEAD; -1 when the path is absent at HEAD
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

type LineOwner struct {
	Path       string
	Commit     string
	Author     Person
	AuthorTime time.Time
	Lines      int // non-blank lines blame attributes to Commit in Path at HEAD
}

type IgnoreRevs struct {
	Source string   // "root", "cli" or ""
	SHA256 string
	Revs   []string // full SHAs the file lists, lowercased and sorted; durable@1 and simplifying@1 exclude these commits
}

type FileError struct{ Path, Message string }

type Window struct{ Since, Until time.Time } // [Since, Until)

// RepoFacts is everything one run learned about one repository.
type RepoFacts struct {
	Repo        Repo
	Status      string // "ok" or "error"
	Error       string // scrubbed and truncated; "" when ok
	Warnings    []Warning
	History     Window      // the landing window the history covers
	Paths       []PathClass
	Commits     []Commit    // landing commits plus every commit a landing brought in
	Landings    []Landing
	Changes     []FileChange
	Reverts     []Revert
	LineFacts   bool        // blame ran (false for week runs, shallow and partial clones)
	Owners      []LineOwner
	IgnoreRevs  IgnoreRevs
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

type Subject struct{ Kind, Key string } // person (canonical email), area (repo key + ":" + path prefix), repo, change (landing SHA or PR id)
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
	Lo, Hi         float64 // 95% interval of the value
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
	ToolVersion string
	Policy      PolicyRef
	Options     map[string]string // effective options; never secrets
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

// GitError is returned for an exit outside OKExit, a timeout, or a failed start.
type GitError struct {
	Args   []string // never contains credentials
	Exit   int      // -1 for a timeout or a failed start
	Stderr string   // redact.Message applied
	Err    error
}

type FactStore interface {
	BeginRun(ctx context.Context, r Run) (int64, error)
	PutRepoFacts(ctx context.Context, runID int64, f *RepoFacts) error
	PutRunFacts(ctx context.Context, runID int64, groups []Group, warnings []Warning) error
	PutMetrics(ctx context.Context, runID int64, values []MetricValue, ranks []RankInterval) error
	FinishRun(ctx context.Context, runID int64, status RunStatus) error
	LatestRun(ctx context.Context, kind RunKind) (Run, error) // complete or partial runs only; ErrNotFound
	LoadFacts(ctx context.Context, runID int64) (*FactSet, error)
	LoadMetrics(ctx context.Context, runID int64) ([]MetricValue, []RankInterval, error)
	Close() error
}
```

M2 adds the forge facts (`ForgeFacts`, pull requests, reviews, login links, watermarks) and their store methods; M4 adds narrations. Each lands with its milestone.

## Package APIs

```go
// redact
func URL(raw string) string     // userinfo removed; "<unparsed-remote>" when unparsable
func Message(s string) string   // URL() applied to every URL in s, then truncated to 180 characters

// gitcli
type Config struct {
	Git         string // binary; default "git"
	MailmapFile string // org mailmap, passed as -c mailmap.file=
	Logger      *slog.Logger
}
func New(ctx context.Context, cfg Config) (*Runner, error) // git older than 2.44 → *domain.UsageError
func (r *Runner) Version() string                          // *Runner implements domain.GitRunner

// policy
func Default() (*Policy, error)                       // embedded default.toml + generated_paths.txt
func Parse(name string, data []byte) (*Policy, error) // *domain.UsageError for a missing table or a bad pattern
func (p *Policy) Ref() domain.PolicyRef

// classify
type Input struct {
	Path      string
	Size      int64  // -1 when absent at HEAD: path rules only
	Generated string // linguist-generated: "unspecified", "unset", "set" or a value
	Vendored  string // linguist-vendored
	HeaderHit bool   // header sniff matched within the first 40 lines
}
func Classify(p *policy.Policy, in Input) domain.PathClass
func Role(p *policy.Policy, path string) domain.Role // path and extension rules only

// collect
func Discover(ctx context.Context, git domain.GitRunner, paths []string) (*Discovery, error)
type Discovery struct {
	Repos    []domain.Repo    // health-checked, duplicates removed, sorted by Key
	Groups   []domain.Group
	Warnings []domain.Warning
	Failed   []domain.FileError // PATH slots that were neither a repo nor a group
}
type PathAt struct{ Commit, Path string }
func ClassifyPaths(ctx context.Context, git domain.GitRunner, repo domain.Repo, p *policy.Policy, absent []PathAt) ([]domain.PathClass, error)
type HistoryMode int // HistoryWeek or HistoryScan
type HistoryOptions struct {
	Window domain.Window
	Mode   HistoryMode
}
func History(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt HistoryOptions) (*HistoryFacts, error)
type HistoryFacts struct {
	Commits  []domain.Commit
	Landings []domain.Landing
	Changes  []domain.FileChange
	Reverts  []domain.Revert
	Warnings []domain.Warning
}
type BlameOptions struct {
	Paths          []string      // classified paths that received a role
	IgnoreRevsFile string        // CLI override; single-repo runs only
	Slots          chan struct{} // shared by every repo: one slot per running git blame
	Timeout        time.Duration
	Progress       func(done, total int)
}
func Blame(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt BlameOptions) (*BlameFacts, error)
type BlameFacts struct {
	Owners     []domain.LineOwner
	IgnoreRevs domain.IgnoreRevs
	Errors     []domain.FileError
}

// identity
func New(fs *domain.FactSet, p *policy.Policy) *Directory
func (d *Directory) Name(email string) string           // display name rule, spec/git-invocation.md#identity-mailmap-and-bots
func (d *Directory) Bot(email string) bool
func (d *Directory) AIAssisted(c *domain.Commit) bool    // ADR 0007
func (d *Directory) Credit(c *domain.Commit) string      // canonical email credited for c

// metrics
type Definition struct {
	ID                                                       domain.MetricID
	Question, Unit                                           string
	Ranked                                                   bool
	Eligibility, BlindSpots, Gaming, Oracle, Validity        string
}
type Input struct {
	Facts  *domain.FactSet
	Policy *policy.Policy
	Who    *identity.Directory
	Days   int // landed@1 window
}
type Metric interface {
	Definition() Definition
	Compute(in *Input) ([]domain.MetricValue, error)
}
type Ranked interface {
	Metric
	Clusters(in *Input, scope domain.Scope) (rank.Kind, map[domain.Subject][]rank.Cluster, error)
}
func All() []Metric                                    // stable order
func Lookup(id string) (Metric, error)
func Compute(in *Input) ([]domain.MetricValue, error) // every metric, sorted

// rank
type Kind int // Ratio or Sum
type Cluster struct {
	Change string  // landing SHA or PR id
	Num    float64
	Den    float64 // Ratio only
	Block  int     // Sum only: the week (counted back from Now) the change landed in
}
type Options struct {
	Replicates int // B; default 1000
	MinN       int // eligibility: clusters per subject
	Blocks     int // Sum only: week blocks in the window
}
func Intervals(id domain.MetricID, scope domain.Scope, w domain.Window, kind Kind, data map[domain.Subject][]Cluster, opt Options) []domain.RankInterval
func Seed(id domain.MetricID, scope domain.Scope, w domain.Window) uint64
func AUC(pos, neg []float64) float64

// report
type Model struct{ /* stillhere.report/v1; see spec/report-json.md */ }
func Text(w io.Writer, m *Model) error
func Markdown(w io.Writer, m *Model) error
func JSON(w io.Writer, m *Model) error

// app
type Deps struct {
	Git     domain.GitRunner
	Store   domain.FactStore // nil for week
	Policy  *policy.Policy
	Log     *slog.Logger
	Version string
}
func Week(ctx context.Context, d Deps, r WeekRequest) (*report.Model, error)
func Scan(ctx context.Context, d Deps, r ScanRequest) (*report.Model, error)
// Report, Explain, Me, WhoKnows, Validate: read the latest complete run unless a run id is given

// cli
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int // the exit code

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
```

## Run model

Every `scan` is a **run**: a snapshot of facts at one set of HEADs, under one policy and one mailmap, with the metric values computed from them.

- **Git-derived facts are stored per run**, not once per commit. The mailmap and the policy can change between runs, and every number in a run must come from one HEAD set, one policy and one mailmap. The cost is repeated rows across runs, bounded by retention. The store is disposable, so this is a two-way door.
- **Publishing** is a status flip. A run is written as `running` and becomes `complete` (or `partial` when some repo failed) only after its last row. Readers select complete and partial runs only, so nobody sees half a run. A run left `running` by a crash is deleted on the next open.
- **Forge facts** (M2) are not per run. They are a replica of GitHub, upserted by node id with a watermark per repo, and each run records the watermark it read.
- **Metric values** are stored with the run, keyed by `metric@version`. A new metric version can be computed over an old run's facts with `LoadFacts`, which is why `LoadFacts(PutRepoFacts(x)) == x` is tested.

## Command surface

Global flags: `--db` (default `$STILLHERE_HOME/facts.db`, where `STILLHERE_HOME` defaults to `~/.stillhere`), `--policy FILE`, `--mailmap FILE`, `--log-level`, `--format text|md|json` on every command that prints a report.

| Command | Reads | Writes | Milestone |
| --- | --- | --- | --- |
| `week PATH...` `[--now] [--days N] [--week-sort lines_added\|commits]` | git | nothing | M0 |
| `scan PATH...` `[--now] [--jobs N] [--ignore-revs-file F] [--rank METRIC]... [--scope group\|all]` | git | a run in facts.db | M1 |
| `report [--run ID] [--rank METRIC]...` | facts.db | nothing | M1 |
| `explain METRIC --person EMAIL [--run ID]` | facts.db | nothing | M1 |
| `who-knows PATH [--repo KEY]` | facts.db | nothing | M1 |
| `me [--email E]` | facts.db | nothing | M2 |
| `metrics` | the registry | nothing | M1 |
| `validate --labels FILE` | facts.db, a labels CSV | nothing | M2 |
| `sync github --org ORG [--mirrors DIR]` | GitHub | mirrors, forge facts | M2 |
| `mcp [--allow-source]` | facts.db | nothing | M4 |
| `version` | | | M0 |

Exit codes are in [report-json.md](report-json.md#exit-codes): 0 when any repo produced facts, 1 when none did, 2 for a usage error.

## Conventions

### Errors and logging

- Wrap with `%w` and the operation that failed. Exit codes come from typed errors (`*domain.UsageError`, `domain.ErrNoFacts`), never from string matching.
- A repository is a bulkhead: its failure becomes `RepoFacts.Status = "error"` and the run continues.
- `log/slog` to stderr. Info level carries repo names, counts, durations and skip reasons, and never emails, tokens or remotes with userinfo. Debug may print emails.
- No `panic` outside programmer errors, no global mutable state, and no `init()` beyond compiling constant patterns.

### Concurrency

- `golang.org/x/sync/errgroup` with `SetLimit` for fan-out, and one blame slot channel shared across repos, so the number of git processes stays bounded on large machines.
- Every goroutine is waited for; every I/O call takes the caller's `context.Context`; `cmd/stillhere` cancels it on SIGINT and SIGTERM.
- Large git outputs are streamed (`GitRunner.Stream`), never buffered whole. Line readers allow long lines (generated files have megabyte lines).

### Determinism

- Time comes from `--now` (default: the wall clock in UTC, read once in `cli`). No other code reads the clock except to measure `timings`.
- Every slice is sorted before it is stored or printed, map keys are sorted, and bootstrap seeds derive from inputs. Two runs on the same inputs produce byte-identical JSON once `timings` is removed.

### Security

- Treat author names, paths and commit subjects as untrusted input: `html/template` for any HTML, and no names in shell strings.
- `facts.db`, mirrors and temp files: directories 0700, files 0600.
- Credentials only through `GitCmd.Env` on the Sync profile ([git-invocation.md](git-invocation.md#credentials)).

### Commits and branches

- One issue, one branch (`m<milestone>/<issue>-<slug>`), one pull request that says `Closes #<issue>`.
- Conventional Commit subjects (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:`, `refactor:`). GoReleaser groups the changelog by them, and `change_type@1` will read them.
- Never `git add -A` or `git add .`; add paths by name. `plan.original.md` is local-only and must never be committed.

## Tests

Tests sit at stable boundaries, so a refactor does not rewrite them (Kent Beck, "Test Desiderata": tests should be structure-insensitive). They run real git; no test mocks git. The fixture oracles are the contract. A metric without an oracle does not ship, and no test exists outside this list without a reason in its pull request.

| Test | Package | Asserts |
| --- | --- | --- |
| `TestWeekOracle` | `app` | the week table in the fixture spec |
| `TestStillHereOracle` | `app` | the still-here table and the concentration walk |
| `TestChangeOracles` | `app` | `durable@1`, `simplifying@1`, reverts and AI-assisted tags on both fixtures; `knowledge_loss@1` and `who_knows@1` on the main fixture |
| `TestDeterminism` | `app` | two scans of the fixture give byte-identical JSON without `timings` |
| `TestDiscover` | `collect` | duplicates, bare repos, scp vs HTTPS remotes; a planted token appears in no output |
| `TestCloneHealth` | `collect` | shallow and partial clones warn and lose only the facts they should |
| `TestAttributes` | `collect` | `check-attr --source` on a work tree and a bare clone |
| `TestNumstatRename` | `collect` | the rename path forms |
| `TestBlamePorcelain` | `collect` | `--line-porcelain` and `--porcelain` parse to the same counts |
| `TestClassify` | `classify` | roles, role precedence, `too_big`, segment matching |
| `TestPolicy` | `policy` | a missing table exits 2; `--days` overrides; the hash covers the deny list |
| `TestRankIntervals` | `rank` | reproducible intervals; ineligible subjects unranked |
| `TestStoreRoundTrip` | `sqlite` | `LoadFacts(PutRepoFacts(x)) == x`; readers never see a running run |
| `TestGitHubSync` | `github` | recorded responses become the expected facts; the token is never stored (M2) |
| `TestMCPTools` | `mcp` | each tool through the SDK's in-memory client (M4) |
