# Code map and contracts

Status: v1 · 2026-10-14 · the layout, package contracts and conventions every change follows · product intent in [../product.md](../product.md), system design in [../architecture.md](../architecture.md)

Read this before touching code. It names every package, what it owns, what it may import, and the signatures between packages (the idea is Aleksey Kladov's "ARCHITECTURE.md"). Signatures here are contracts: a change to one lands in the same commit as the change to this file.

## Layout

| Package | Owns | Kind |
| --- | --- | --- |
| `cmd/stillhere` | `main`: signal handling, then `cli.Execute` | entry point |
| `internal/cli` | Commands, flags, exit codes, and the composition root (the only place adapters are built and wired) | inbound adapter |
| `internal/app` | Use cases: week, scan, report, explain, me, who-knows, validate, identities, sync, mcp | application |
| `internal/collect` | git output → facts: discovery, clone health, the commit a run reads at `--now`, tree and attributes, history, blame, Rust's inline tests | anti-corruption layer for git |
| `internal/domain` | Fact types, metric value types, port interfaces, typed errors | core |
| `internal/policy` | The policy model, embedded default policy and deny list, parsing, hashing | core |
| `internal/classify` | The ordered path classifier, and the rule that decided a path's class (`Explain`) | core |
| `internal/identity` | Display names, bots, AI-assisted tags, who gets credit; the run's addresses and the ids that stand in for them (`Pseudonym`); identity candidates, suggested and never applied ([identities.md](identities.md)) | core |
| `internal/metrics` | The metric registry and every metric version | core |
| `internal/rank` | Cluster bootstrap, rank intervals, eligibility, AUC, and the validity check's bar and verdict | core |
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
| `golang.org/x/term` (v0.45.0) | `cli` | The Go project's terminal package: `label` reads one key at a time (`term.MakeRaw` while it waits for a key, restored before it prints) on macOS, Linux and Windows. It was already in the module graph through go-gh, so it adds no module to `go.sum` ([ADR 0014](../adr/0014-labels-are-evidence-not-the-gate.md)). | `golang.org/x/sys/unix`'s termios, which has no Windows; or a line per answer, which `label` falls back to when its input is not a terminal |
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
// Boundary (#74): c is where a shallow clone ends, a commit with no parent in a shallow clone, whose diff against the
// empty tree is its whole tree: the one rule the history collector (no FileChange for it), the metrics (a window inside
// the clone's history, metrics.md "Missing facts") and a diff of a landing read
func (r Repo) Boundary(c *Commit) bool

type Group struct {
	Name  string   // basename; "-2", "-3" suffixes for duplicates
	Path  string
	Repos []string // Repo.Key, sorted
}

type Warning struct {
	Code    string // duplicate-repo, shallow-clone, shallow-window, partial-clone, ignore-revs, head-after-now, dated-after-now, inline-tests, other-refs-unread, excluded-repo, path-failed, no-commit-before-now, ...
	Message string // already scrubbed
	Repo    string // Repo.Key; "" for run-level warnings
}

type Role string // prod, sql, markup, iac, doc, test, migration, seed
func Roles() []Role        // the classifier's roles, in the order messages list them: the one list policy's role names and paths' --role check against
func (r Role) Known() bool // r is one of Roles
func RoleList() string     // Roles for a message: "prod, sql, markup, ..."
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
	PatchID    string   // wave 10: the patch id (--stable) of its own -U0 -w patch (collect's readPatchIDs), which a cherry-pick keeps;
	                    // "" for a merge, a shallow boundary, an empty patch (whitespace only, under -w: a mode change or a rename alone
	                    // has one), a partial clone's, and in a run stored before migration 9. The metrics' view marks a copy by it and
	                    // the parents, git cherry's rule: a commit a merge brought whose twin landed earlier and is not its ancestor
	                    // (docs/metrics.md#units)
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
	Aliases     []Alias     // #53: what git's mailmap rewrote in the commits the facts hold, by Email, Name, then Canonical; nil in a run stored before facts.db kept them
	RefCommits  []Commit    // wave 10: the commits of the clone's branches, remote-tracking branches and tags that Head does not hold, committed in History (scan runs;
	                        // week-collector.md#commits-on-other-refs-scan), by SHA, with no Parents or Subject: knowledge_loss@1's activity off the default branch;
	                        // nil in week runs and in a run stored before migration 10
}
// #53: an email a commit or a Co-authored-by trailer of the facts was written with, the name written beside it ("" for none),
// and the canonical email git's mailmap gave it (never the same email). stillhere never parses a mailmap: this is what git did
// with one, which the identities answer follows; the name because git prefers a line matching name and email over one matching the email
type Alias struct{ Email, Name, Canonical string }

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

type Subject struct{ Kind, Key string } // person (canonical email), area (repo key + ":" + path prefix), repo, group (Group.Name: a group's own numbers), change (landing SHA or PR id),
                                        // all (Key "": the whole run's own numbers in scope all, landed@2's totals and review_coverage@1's)
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

// #109: what a value of a metric that is not ranked is made of (metrics.Traced), so explain reaches the commits behind every
// number: a person's, and (wave 10) a repository's, a group's or the run's own; each list in the order a page prints it
type Trace struct {
	Window   Window          // the window the value reads; zero for a value at HEAD
	Landings []TracedLanding // landed@1, landed@2 (a person's, or a scope's totals'): by repository, then time
	Commits  []TracedCommit  // surviving@1: the blamed commits holding the person's prod lines at HEAD, most first
	Files    []TracedFile    // majority_owner@1: the files they hold half of, most of their lines first
	Changes  []TracedChange  // review_coverage@1 about a repository, a group or all: its window's changes, the unreviewed in Den first,
	                         // across repositories (then by repository and time)
}
type TracedLanding struct { Repo, SHA string; Time time.Time; Merge bool; Commits, Copies, Lines, ProdLines int } // Lines -1: unknown
                                                                    // (no numstat); Copies: in a scope's totals, the copies it brought
// wave 10: one change review_coverage@1 read. Outcome: reviewed, unreviewed or direct (in Den), or bot, branch_merge, copies, after_sync,
// unmatched, named_elsewhere; Via: a pull request merged into another branch, which the merge Landing brought; Named: the pull request
// a subject names (unmatched, named_elsewhere); Author: the person key Den counts it for, "" when it was left out first
type TracedChange struct { Repo, Landing string; Time time.Time; Pull int; Base string; Via bool; Outcome, Author string
	Reviewers int; ApprovedOnly bool; Named int }
// wave 10: a person's last activity by knowledge_loss@1's rule (metrics.LastActivity): Kind ActivityLanding, ActivityReview, ActivityPullRequest
// or ActivityCommit; QuietIn the whole days left at Now before 180 days have passed since Time, -1 once they have; GoingQuiet within 30 of it
type Activity struct { Time time.Time; Kind string; QuietIn int; GoingQuiet bool }
type TracedCommit struct { Repo, SHA string; Time time.Time; Lines int }                                  // Time: author time, as blame gives it
type TracedFile struct { Repo, Path string; Lines, Of int }
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

type RunScopes struct { // MC-17: a stored run's scopes, read without its facts (FactStore.Runs)
	Run    Run
	Groups []Group // as LoadFacts returns them
	Repos  []Repo  // each RepoFacts.Repo LoadFacts returns, by Key
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
	Runs(ctx context.Context, kind RunKind) ([]RunScopes, error) // MC-17: every complete or partial run of the kind, newest first (by id, as LatestRun),
	                                                            // with its groups and repositories and no other fact; none is an empty list
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
	Aliases       []string // wave 11 (KT-2): other "owner/name"s GitHub resolves to it (a --repo lookup GitHub answered under
	                       // this name, a renamed mirror's old path), lowercased, sorted, never its own; nil for none. Only a sync
	                       // records them (migration 11); ForgeJoin joins an origin that names one
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
// ADR 0013 (#101): positions on the forge's clock that a completed pass reached, never values read from the pull requests.
// Updated: the last completed pass began then and read every PR last updated from From on (while Missing is 0); zero until one
// completes, and a pass that stops halfway moves nothing. From: zero for a replica whose first pass read the whole list (one
// stored before migration 6). Synced: the "as of" time. Missing: the last check's shortfall. Pending: pages of an unfinished
// pass are stored (with Synced zero: the first sync stopped halfway)
type ForgeWatermark struct { Repo string; Updated, From, Synced time.Time; Missing int; Pending bool }
type ForgePage struct { Repo string; PullRequests []PullRequest; Updated time.Time } // Updated: zero but on the last page of a pass
                                                                                   // that read its whole range, the pass's start: stored with it in one transaction
// ForgeFacts: LoadForge's replica, or a scan run's own part of it (FactSet.Forge): the forge repositories ForgeJoin joins
// to the run's repositories, their watermarks (a zero one where the replica held none) and pull requests, which the run
// keeps (PutRunForge) and its readers read back (RunForge), so a stored run computes over what it read (ADR 0011).
// Join (#38's review): the run's join, ForgeRepo.ID → its Repo.Keys, as its scan made it and its watermarks record it
// (RunWatermark.Keys); every metric counts by it, so a later ForgeJoin moves no stored run. nil for LoadForge's replica,
// which no run joined: a metric over the replica itself joins it with ForgeJoin
type ForgeFacts struct { Repos []ForgeRepo; PullRequests []PullRequest; Watermarks []ForgeWatermark; Links []ForgeLink; Join map[string][]string }
// #103: Links are, of the pull requests LoadForge leaves out (updated before since), only their commits' links to an account,
// distinct, by Repo, SHA, AuthorLogin, AuthorEmail; a scan run keeps of them one for each repository, address and account
// (identity.KeptLinks); no metric reads them, identity's same-forge-account rule does
type ForgeLink struct { Repo string; ForgeCommit } // a pull request's own commit or merge commit in forge repository Repo; AuthorLogin never ""
// RemoteRepo: "host/owner/name" of a remote, lowercased, with no user, port or ".git", for https://, ssh:// and
// git@host:o/r remotes. A run's repository (Repo.Remote) is a forge repository (ForgeRepo.Key) when the two match.
func RemoteRepo(remote string) (string, bool)
// ForgeJoin (#13): the one join of a run's repositories to a replica's: ForgeRepo.ID → the sorted Repo.Key of every
// repository whose origin names it (RemoteRepo), a failed one (status error) left out; nil when none. A scan joins by it
// once, and the run keeps that join (ForgeFacts.Join, RunWatermark.Keys), which every metric counts by and every page dates
// by, so the two cannot disagree about which GitHub data is a run's. Wave 11 (KT-2): an origin also names a forge repository
// by one of its Aliases (ForgeAliasNames), unless a forge repository holds that name now: a clone made before a rename or a
// transfer joins the data a sync copied under the new name; the node id is what both names resolve to
func ForgeJoin(repos []RepoFacts, forge []ForgeRepo) map[string][]string
func ForgeAliasNames(r ForgeRepo) []string // r.Aliases as RemoteRepo names them, "host/owner/name" on r's own host
// The run's kept join (Join), else, for GitHub data no run joined (the replica itself), ForgeJoin made now: what metrics
// (reviewsJoin), the report's coverage titles and branch, and app's repository names (NamedRepo.Forge) all read; nil for no data
func (f *ForgeFacts) RunJoin(repos []RepoFacts) map[string][]string
func (f *ForgeFacts) RepoOf(repos []RepoFacts, key string) (ForgeRepo, bool) // the forge repository RunJoin joins key to
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
	Was            string // wave 11 (KT-2), MirrorRenamed: the "owner/name" its path held, which GitHub resolves to the
	                      // repository now, so the sync records it as an alias whether or not the mirror could move; MirrorRestored
	                      // (wave 11's review): the name its root/.stale path held when another, unless it may be freeName's suffix
	Err            string // why it could not move; a listed repository whose mirror could not move is not mirrored that sync
}

type ForgeSource interface { // GitHub's GraphQL API and git remotes (internal/github); GitLab later (ADR 0004)
	Repos(ctx context.Context, owner string) ([]ForgeRepo, error) // sorted by Key; an unknown login is a *UsageError
	Repo(ctx context.Context, owner, name string) (ForgeRepo, error) // #111: one repository, as Repos lists it; an unknown one is a *UsageError;
	                                                                 // wave 11: one GitHub answers under another name carries owner/name as its alias
	// Once per sync, before any fetch, with the repositories the sync mirrors: mirrors follow their repositories by node id,
	// which a rename or a transfer keeps; with owner set, a mirror under root/<owner> of a repository not in listed (no longer
	// listed, or left out by the sync: archived, a fork, #110) moves to root/.stale. owner "": a partial listing (--repo), no other move.
	ReconcileMirrors(ctx context.Context, owner string, listed []ForgeRepo, root string) ([]MirrorMove, error)
	Mirror(ctx context.Context, repo ForgeRepo, root string) (string, error) // root/<owner>/<name>.git, created or fetched, then its commit-graph (#56)
	// ADR 0013: updated at or after since (zero: all), newest first, with a tolerant stop (a page wholly older than since less
	// the overlap ends the pass, an older pull request alone does not); the watermark only on the last page of a whole pass
	PullRequests(ctx context.Context, repo ForgeRepo, since time.Time, put func(*ForgePage) error) error
	// ADR 0013: GitHub's count of repo's PRs last updated at or after from (zero: all) and settled before start, a completed
	// pass's start: start less the overlap. A lower bound: search leaves out a few (a deleted account's)
	CountPullRequests(ctx context.Context, repo ForgeRepo, from, start time.Time) (int, error)
	// #103, ADR 0013: the account each author email of the mirror's history (git log %ae from HEAD) belongs to, but the emails
	// in skip: one commit per email, its newest, 100 a query by alias (1 point); put gets a batch at a time, login "" for an
	// email GitHub links to no account; an email whose commit GitHub does not have is left out, to be asked again
	LinkAuthors(ctx context.Context, repo ForgeRepo, mirror string, skip map[string]bool, put func([]ForgeCommit) error) error
}
type ForgeStore interface { // the replica in facts.db (*sqlite.Store, sqlite/forge.go)
	PutForgeRepos(ctx context.Context, repos []ForgeRepo) error // by ID, with their Aliases (migration 11) as the sync found them, which
	                                                           // replace the repository's rows; a name resolves to the repository that
	                                                           // last answered to it, and a name any repository holds now is no alias
	ForgeAliases(ctx context.Context) (map[string][]string, error) // wave 11's review: the aliases held, by ForgeRepo.ID, sorted, which
	                                                               // each sync looks up again (app's checkAliases), dropping one GitHub
	                                                               // no longer resolves, or resolves to another repository
	ForgeWatermark(ctx context.Context, repo string) (ForgeWatermark, error)
	PutForgePage(ctx context.Context, page *ForgePage) error // a page with an Updated raises the watermark to it (never lowers it) and clears
	                                                          // Pending, any other sets Pending; a PR is replaced only by a copy updated as late or later
	MarkForgeSynced(ctx context.Context, repo string, at time.Time) error
	MarkForgeChecked(ctx context.Context, repo string, at, from time.Time, missing int) error // ADR 0013: Synced, From and Missing in one transaction;
	                                                                                          // missing is the last check's that ran: when this sync's could not, the one stored
	MarkForgeMissing(ctx context.Context, repo string, missing int) error // ADR 0013: a shortfall alone, before the repair pass; Synced and From stay
	CountForgePulls(ctx context.Context, repo string, since time.Time) (int, error)       // ADR 0013: the replica's PRs updated at or after since
	ForgeLinks(ctx context.Context, repo string) (map[string]string, error)             // #103: the emails asked about, each with its login ("" for none)
	PutForgeLinks(ctx context.Context, repo string, links []ForgeCommit) error          // #103: upserted by email, one transaction
	LoadForge(ctx context.Context, since time.Time) (*ForgeFacts, error) // PRs updated at or after since, the others' links (#103); nil for an empty replica
	PutRunWatermarks(ctx context.Context, runID int64, watermarks []RunWatermark) error // a running run: what it read, with its join; no Keys is an error
	RunWatermarks(ctx context.Context, runID int64) ([]RunWatermark, error) // sorted by Repo; ErrNotFound unless complete or partial; no Name or Keys before schema version 3
	// #38 (ADR 0011): a running run's GitHub data, FactSet.Forge, kept whole, with its older pull requests' links (#103, ADR 0011's
	// amendment); a pull request or a link of a repository not in repos is an error
	PutRunForge(ctx context.Context, runID int64, repos []ForgeRepo, pulls []PullRequest, links []ForgeLink) error
	// what PutRunForge kept, sorted as LoadForge sorts, Watermarks from RunWatermarks and Join from their Keys; nil when the run kept none (no forge
	// repository, or stored before schema version 4); ErrNotFound unless complete or partial
	RunForge(ctx context.Context, runID int64) (*ForgeFacts, error)
}
```

#14 owns `domain/forge.go`, `internal/github`, `sqlite/forge.go` and `sqlite/migrations/0002_forge.sql` (where `*sqlite.Store` implements `ForgeStore`), `cli/forge.go` (which builds the `ForgeSource` adapter), `cli/sync.go`, `app/sync.go` and `metrics/reviews.go`. `ForgeStore` is a port of its own rather than more `FactStore` methods: `FactStore` holds per-run snapshots, while forge facts are a replica that outlives runs ([run model](#run-model)), and a separate port leaves `FactStore` and #8's implementation untouched (Interface Segregation Principle). What a run read of the replica, its watermarks and (#38) its pull requests, is on the port too, so a reader without it reads no GitHub data at all, neither the dating nor the reviews. Mirroring is on `ForgeSource` because the credentials are the forge's: the adapter holds the token, so it runs the clone and the fetch (through `domain.GitRunner`), and the token never reaches `app`. A mirror carries its repository's node id in its own git config (`stillhere.forgeId`), because its path does not identify it: a rename or a transfer changes the path and keeps the id, and discovery keys repositories by origin URL, so a mirror left under the old name would be scanned twice. `ReconcileMirrors` is a separate, sequential call because only it sees the whole listing: it can tell a rename from a deletion, and settle two repositories that swapped names, before the parallel fetches start. Store migrations are embedded files, `internal/sqlite/migrations/NNNN_<name>.sql`, applied in order, one transaction each, and numbered by `PRAGMA user_version`: 1 (`0001_facts.sql`) is #8's schema, 2 (`0002_forge.sql`) is #14's forge tables (`forge_run_watermarks` references #8's `runs`), 3 (`0003_forge_run_join.sql`) is #13's: each run watermark's name, and the run's repositories it joined (`forge_run_repos`), so a reader never matches a stored run against today's `forge_repos`. 4 (`0004_forge_snapshot.sql`, with `sqlite/forgesnapshot.go`) is #38's: the GitHub data a run read, its forge repositories and their pull requests with their labels, reviews and commits (`forge_snapshot_*`), kept with the run ([ADR 0011](../adr/0011-a-run-keeps-the-github-facts-it-read.md)). 5 (`0005_line_owner_orig_path.sql`) is #54's: each line owner's `OrigPath` (`orig_path`, `''` for a row stored before it, which reads as the path at HEAD). 6 (`0006_forge_completeness.sql`) is #101's ([ADR 0013](../adr/0013-sync-reads-bounded-passes-and-checks-the-replica.md)): each watermark's, and each run's copy of one's, `since` (`ForgeWatermark.From`), `missing` and `pending`. 7 (`0007_forge_links.sql`) is the same ADR's for #103: `forge_links`, the account GitHub links each author email of a repository's history to, one row per email, `''` for none (`ForgeSource.LinkAuthors`, `ForgeStore.ForgeLinks` and `PutForgeLinks`), which identity's same-forge-account rule reads beside the pull requests' own commits. 8 (`0008_identity_evidence.sql`) is #103's and #53's: the commit-to-account links of a run's older pull requests (`forge_snapshot_links`, `ForgeFacts.Links`, [ADR 0011's amendment](../adr/0011-a-run-keeps-the-github-facts-it-read.md#amendment-the-links-of-older-pull-requests-2026-10-15-103)) and what git's mailmap rewrote, with the name written beside each email (`repo_aliases`, `RepoFacts.Aliases`); a run stored before has neither. A run keeps one link for each repository, address and account (`identity.KeptLinks`), the evidence the rule reads, not one per commit. 9 (`0009_commit_patch_id.sql`) is wave 10's: each commit's patch id (`commits.patch_id`, `Commit.PatchID`, `''` for none and for every row stored before it), by which the metrics credit a copy of an earlier landing's change once. 10 (`0010_ref_commits.sql`) is wave 10's too: the commits of a repository's other refs that its HEAD does not hold (`ref_commits`, `ref_commit_coauthors`, `RepoFacts.RefCommits`), who wrote each and when, which `knowledge_loss@1` counts as activity; a run stored before has none. 11 (`0011_forge_repo_aliases.sql`) is wave 11's (round 3's KT-2): the names GitHub resolves to a repository besides its own (`forge_repo_aliases`, one row per name, `ForgeRepo.Aliases`), which a sync learns from a `--repo` lookup GitHub answers under another name and from a renamed mirror's old path (`MirrorMove.Was`), and, kept with a scan run, those of the forge repositories it joined (`forge_snapshot_repo_aliases`), so a reader resolves a repository argument by them; a run stored before has none. M4 adds narrations the same way.

## Package APIs

```go
// redact
func URL(raw string) string     // userinfo, query and fragment removed; "<unparsed-remote>" when unparsable or a scheme-less string with an @ that is not user@host:path
func Scrub(s string) string     // credentials removed from free text (URL() on every URL, user:pass@ prefixes, also on a path segment, and the userinfo, query and fragment of a URL a path cleaner collapsed, "/cwd/https:/user:TOKEN@host/r.git?access_token=TOKEN"; a one-letter scheme is a Windows drive, so "C:\src\C#" keeps its "#"); nothing else changes
func Message(s string) string   // Scrub, then one line truncated to 180 characters
func PathMessage(s string) string // Message for a path: a longer one keeps its last 179 characters after "…", where what is wrong with a path most often is
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
func (r *Runner) AtLeast(major, minor int) bool            // #56: a feature above the floor, such as version 2 changed-path filters (2.46)

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
	Role     domain.Role
	Pattern  *regexp.Regexp // RE2 over the repo-relative path
	When     []domain.Role  // empty: any path; else only paths whose extension or name role is one of these
	MaxBytes int64          // revision 3: optional max_bytes; 0 none; else a larger file the rule admits is too_big, by this rule
	Note     string         // the first sentence, up to a colon, of the comment in the rule's [[roles.path]] table (a block a key follows), else of the
	                        // last paragraph of the comment that runs into its header with no blank line; "" without one
} // tried in order: the first whose Pattern matches and whose When admits the extension or name role wins
type DenyRule struct {
	Pattern string // as written
	Index   int    // from 1, among the deny patterns: [paths] deny's element in a policy file
	Line    int    // its line in generated_paths.txt; 0 for a policy file's
	Note    string // the packaged list's label for its group, the first words of the comment above it; "" for a policy file's
}
func Default() (*Policy, error)                             // embedded default.toml + generated_paths.txt
func Parse(name string, data []byte) (*Policy, error)       // *domain.UsageError for a missing table or key, an unknown key, or a bad pattern or value
func FromSource(name string, source []byte) (*Policy, error) // rebuilds a run's policy from Run.PolicySource alone, never today's packaged files; same Ref; a plain error, not a usage error
func (p *Policy) File() ([]byte, error)                     // #71: the policy as a file Parse reads back to the same rules, what 'stillhere policy' prints: a user's
                                                            // file as it was read; the packaged one with its header comment replaced and [paths] package_file by
                                                            // deny = [...], every pattern of its deny list in order with the list's comments (Linguist's license
                                                            // among them), so a file started from it is complete and its hash covers every pattern; from Source alone
const MaxWeekDays = 36500                                   // the bound on [week] days and on --days
func (p *Policy) Ref() domain.PolicyRef
func (p *Policy) Packaged() bool                            // wave 10: the policy stillhere ships, or a stored run's copy of it (Paths.Source names a package file): no
                                                            // file holds it, so advice to change it starts with 'stillhere policy > stillhere.toml'
func (ps *Paths) Denies(path string) bool                   // any Deny pattern matches path. Default, Parse and FromSource also store, unexported in
                                                            // Paths, the literals each pattern needs (FilteredRE2's prefilter): a path tries a
                                                            // handful of the ~240 packaged patterns, not all; a pattern added by hand is always tried
func (ps *Paths) Denying(path string) (DenyRule, bool)      // the first pattern that matches, the one Denies stops at, and where the policy holds it
func (p *Policy) Packaged() bool                            // p is the packaged policy, or a run's copy of it: its deny list is a package file
func PackagedRevision(sha256 string) int                    // revisions.go: the packaged policy's revision whose hash (Policy.SHA256) sha256 is, for every packaged
                                                            // policy stillhere has shipped; 0 for a policy file or an unknown one. TestPolicy fails until a change to
                                                            // default.toml or generated_paths.txt lists its hash: the packaged policy's version bump

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
func Classify(p *policy.Policy, in Input) domain.PathClass // a header hit makes generated_header only a path a rule counts (a role, or a rule's max_bytes)
func Role(p *policy.Policy, path string) domain.Role // path and extension rules only, a rule's max_bytes as if the size were unknown
func RoleByRuleAlone(p *policy.Policy, path string) bool // only a [[roles.path]] rule (one without when) gives path a role, no name or extension:
                                                         // collect's header sniff names such files by path, so it reads every file that can get a role
// Explain (revision 3, stillhere paths): the rule that decided c, a class Classify gave under p, found again from p and c
// alone with the functions Classify decides with (Paths.Denying, one role search), so a stored run's classes are explained
// from its own policy. What the class does not record is told apart by what it says (a denied path no pattern matches: an
// attribute; a skipped path at HEAD with no size and no rule: a submodule). A class this classifier would not give under p
// is RuleUnknown.
func Explain(p *policy.Policy, c domain.PathClass) Rule
type Rule struct {
	Kind   RuleKind // deny, attribute, size, header, not-file, path, name, extension, none, unknown
	Text   string   // the rule as the policy writes it: a pattern, ".go", "Dockerfile", "max_bytes = 400000"
	Where  string   // "generated_paths.txt line 478", "[paths] deny pattern 3", "[[roles.path]] rule 4", "[roles.extensions] prod", "[blame] max_bytes", ".gitattributes"
	Source string   // SourcePackaged, SourceFile, SourceAttribute, SourceHeader or SourceFileType
	Note   string   // the rule's own reason (PathRule.Note, DenyRule.Note), the same for every path the rule decides
	Detail string   // what this path adds: its size under a size bound, the deny pattern an attribute overrode; "" for none
}

// collect
func Discover(ctx context.Context, git domain.GitRunner, paths []string) (*Discovery, error) // an error only when ctx ends
type Discovery struct {
	Repos    []domain.Repo      // health-checked, duplicates removed, sorted by Key
	Groups   []domain.Group     // sorted by Name
	Warnings []domain.Warning   // duplicate-repo, shallow-clone, partial-clone; sorted by Repo, Code, Message
	Failed   []domain.FileError // paths that gave no repository, each once, sorted by Path: a PATH slot that was neither a repo nor a group (#71: a directory with no repository among its
	                            // immediate children is none, and its Message says what to pass: two subdirectories that hold repositories, else the work tree it is inside),
	                            // or a repo (named or a group's member) git cannot read or whose default branch has no commit; each Message one line, scrubbed, and cut
	                            // where it quotes git; app reports each as a run-level path-failed warning and marks the run partial (see Errors and logging).
	                            // #93: a PATH, or a group's child, whose absolute path redact.Scrub would change (a remote's userinfo, token or query pasted as a
	                            // directory name) fails before anything of it is read, its Path scrubbed: refused, never scrubbed, since Repo.Path is where git runs
}
// LacksObjects (RB-8): err is git failing because a partial clone lacks an object the command needs, which analysis never fetches
// (GIT_NO_LAZY_FETCH: "lazy fetching disabled", "could not fetch <oid> from promisor remote"); a use case decides by it, never by git's text
func LacksObjects(err error) bool
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
// as an error. The header sniff reads every file at Head the policy can give a role: its extensions and names as
// pathspecs, and each file only a path rule gives a role (classify.RoleByRuleAlone) by ":(literal)<path>", or every
// file when more than 256 need naming; none when no file can get a role.
func ClassifyPaths(ctx context.Context, git domain.GitRunner, repo domain.Repo, p *policy.Policy, touched []PathAt) ([]domain.PathClass, error)
// InlineTests (inlinetests.go): among paths (a scan passes its prod paths at Head), the Rust files whose items #[cfg(test)]
// or #![cfg(test)] gates, and those items' non-blank lines, from the attribute to the item's closing brace or semicolon
// (an inner attribute: to the end of its block), comments, strings and character literals skipped; one git grep -l at
// Head, then one cat-file --batch of the hits paths names; no command when paths holds no .rs file
// (classification.md#rust-inline-tests). A scan with line facts runs it after blame for the inline-tests warning
func InlineTests(ctx context.Context, git domain.GitRunner, repo domain.Repo, paths []string) (InlineTestCount, error)
type InlineTestCount struct{ Files, Lines int }
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
	Commits     []domain.Commit     // by SHA: the landing commits, plus every commit a landing brought in (week runs too since #108: landed@2 credits them);
	                                // in a clone with blobs, each with a patch of its own carries its patch id, in both modes (wave 10: the own
	                                // -U0 -w patch streamed into git's patch-id --stable through a pipe, two git processes at once, readPatchIDs)
	Landings    []domain.Landing    // by SHA; Landing.Commits, the map, in both modes
	Changes     []domain.FileChange // by Commit, then Path; NonBlank -1 in week runs; no rows in a partial clone or for a shallow clone's boundary commit
	                                // (domain.Repo.Boundary); the own-patch reader holds no hunk line whole, so a line of any length counts (#73)
	Reverts     []domain.Revert     // by SHA, then Reverted; scan runs only; SHA is the window landing whose map holds the revert commit (itself, or a commit a merge brought in)
	Warnings    []domain.Warning    // by Code, then Message: other-refs-unread, shallow-window
	Aliases     []domain.Alias      // #53: what the mailmap rewrote in the commits collected, %an and %ae beside %aN and %aE and git check-mailmap's trailers, sorted
	RefCommits  []domain.Commit     // wave 10, scan runs: one log --ignore-missing --branches --remotes --tags --not <head> over the window (readRefCommits),
	                                // by SHA; a commit Head holds is never one, and none is read at or after the window's end. A ref to an object the clone
	                                // lacks is skipped; any other failure of that log is a WarnOtherRefs warning and no RefCommits, never the repository's error
}
func MergeAliases(lists ...[]domain.Alias) []domain.Alias // History's and Blame's, as RepoFacts.Aliases holds them: sorted, each once; nil for none
const WarnOtherRefs = "other-refs-unread" // wave 10's review: the repository warning of a history that could not read the other refs' commits
type BlameOptions struct {
	Paths          []string              // classified paths at Head that received a role; none: only resolve IgnoreRevs, which a scan does for a repo whose line facts it skips (shallow, partial)
	IgnoreRevsFile string                // CLI override; single-repo runs only; an unreadable or malformed file is a *domain.UsageError
	Slots          *BlameSlots           // NewBlameSlots(jobs), shared by every repo; nil is an error
	Timeout        time.Duration         // per file; 0 means 120 s
	Progress       func(done, total int) // after every 1,000th file and after the last
}
// commitgraph.go (#56): whether repo has a commit-graph (the single graph, or the newest of a chain), and whether it has
// changed-path filters (a BIDX chunk), from git rev-parse --git-path and the graph file's chunk table; false, never an error,
// when it has none or it cannot be read. Scan's hint reads it (app.commitGraphHint: on git 2.46 and later, the version 2
// filters command for a clone without filters; on 2.45, the command without filters for a clone without a graph); nothing is
// ever written into the clone
func CommitGraph(ctx context.Context, git domain.GitRunner, repo domain.Repo) (graph, filters bool, err error)
type BlameSlots struct{ /* a golang.org/x/sync semaphore; only Blame takes and frees slots */ }
func NewBlameSlots(n int) *BlameSlots // n git blame processes at once, across the run; below 1 means 1
func Blame(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt BlameOptions) (*BlameFacts, error)
type BlameFacts struct {
	Owners     []domain.LineOwner // sorted by Path, Commit, then OrigPath; CoAuthors from coAuthors
	IgnoreRevs domain.IgnoreRevs  // the list blame used: exactly the commits git ignored
	Errors     []domain.FileError // one per blamed path that failed, sorted by Path; RepoFacts.BlameErrors
	Warnings   []domain.Warning   // at most one, Code "ignore-revs": the root file was not used, or not whole; the caller adds it to RepoFacts.Warnings
	Aliases    []domain.Alias     // #53: what the mailmap rewrote in the blamed commits, read by the coAuthors call that reads their trailers
}

// identity
func New(fs *domain.FactSet, p *policy.Policy) *Directory
func (d *Directory) Name(email string) string           // display name rule, spec/git-invocation.md#identity-mailmap-and-bots (wave 11: a person's name
                                                        // before a default, one a page can show before one it withholds, a full
                                                        // name before a single word, then the most seen, an authored one, the newest);
                                                        // for a LoginKey, the login as GitHub last showed it ("@Zoe" for "@zoe", #69), "" when the
                                                        // run's GitHub data never named it
func (d *Directory) Bot(email string) bool              // a [bots] fragment in the email or display name, or an [ai] agent: agents are never people (ADR 0007); a forge app account's "@login[bot]" too
func (d *Directory) AIAssisted(c *domain.Commit) bool    // ADR 0007; only a commit credited to a human
func (d *Directory) Credit(author domain.Person, coAuthors []domain.Person) string // the author, or an agent's first human co-author (ADR 0007); commits and blamed lines alike
func (d *Directory) Agent(email string) bool            // an [ai] agent, an email or a forge account's subject ("@copilot-swe-agent[bot]"): a bot whose work Credit gives a person;
                                                        // review_coverage@1 credits an agent's pull request to the person its commits are credited to
func (d *Directory) Addresses() []string // #90: every address the facts show someone by (authors and co-authors, of commits and of blamed commits), a bot's
                                         // included, lowercased and sorted, logins left out: what every page withholds in repository text
// identity/pseudonym.go (PF-7): the id that stands in for an email where an answer prints none, "p-" and the first 10 hex digits of
// the SHA-256 of the canonical email; the MCP server sends it in place of every email, and both inbound adapters resolve it back
func Pseudonym(email string) string
func IsPseudonym(s string) bool
// identity/reviewer.go (#38): the one rule for who a forge account is, which every metric that reads reviews asks. A login
// is the run's address of the commits FactSet.Forge attributes to it, as forgeLinkReader reads every link (#117: a pull request's
// commits and merge commit, and ForgeFacts.Links, the older pull requests' and the history's author emails'; the history's or a blamed
// commit's mailmapped author, else the email GitHub read when the facts show it; the most commits the run holds, then the most it does
// not, then the smallest email: wave 11, so a squash's co-author trailer never names its pull request's author), else "@login";
// an app "@login[bot]"; no login, the zero Subject.
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
// identity/reviewer.go (#103): the one reading of a forge commit's link to an account (forgeLinkReader: the run's address of its
// SHA, history then blamed, else the email GitHub read when the facts show it; the login folded; an app's counts for nothing),
// which the same-forge-account rule, Blocks and this share
func KeptLinks(fs *domain.FactSet, links []domain.ForgeLink) []domain.ForgeLink // the first link for each repository, address and account, in order;
                                                                               // what a scan run keeps of ForgeFacts.Links: the rule reads the same
// identity/blocks.go (spec/identities.md#counting-people-for-a-floor): the same keys read for recall, for a count that must not err high
func Blocks(fs *domain.FactSet, who *Directory) map[string]string // pure; each human address → its block's smallest address: what Candidates links, and
                                                                 // every hand-off chain, and every two sharing the words of any name seen with them (a default one too), a local part, an account id
                                                                 // or an account GitHub's links attribute them to (#103), without the rules' vetoes
type Survey struct { Candidates []Candidate; Addresses, Bots, Shared, Oversized int } // most confident first, the guesses (hand-offs) last (wave 10); the human addresses examined, the bots left out,
                                                                                    // the addresses several people use that no rule linked, the groups of more than 10 left out
type Candidate struct { // an address is in one candidate at most, so no two .mailmap lines map one alias
	Confidence      Confidence // the weakest pair's: high (account id, or a full name and a local part not made from it), medium (a full name), low
	Guess, Chained  bool       // a hand-off, never suggested as certain; two addresses linked only through a third
	Canonical, Name string     // the suggested canonical address (one of Addresses) and display name, never a default or one that gives an address away
	Addresses       []Address  // by first activity, then email
	Links           []Link     // each rule's evidence, in the order of Rules
	High            []int      // #103: indexes of the largest set of Addresses whose every pair is high, the canonical one among them (the canonical
	                           // address is the preferred one of the set): all of a high candidate's, nil when no pair is high; its lines are written as lines
}
func (c *Candidate) Rules() []Rule
type Address struct { Email, Name string; Domain DomainKind; Commits, CoAuthored int; First, Last time.Time; Repos []string; Aliases []Alias;
	NamesDiffer bool; Names []string; NamesWithheld int } // author times; history and blamed commits; Aliases (#53): what git's mailmap rewrote to it;
	                   // NamesDiffer (wave 10): its commits carry names that differ (a name change or a shared mailbox), which only same-forge-account
	                   // links, high; Names, with it: those names, sorted, less any that gives an address of the run away (Directory.Withheld's rule),
	                   // which NamesWithheld counts; nil and 0 otherwise
type Alias struct { Email string; Names, Repos []string } // #53: an email git rewrote to an address, the names it was written with, and the repositories
                                                          // whose mailmap rewrote it (a repository's own .mailmap applies only inside it); sorted
type Link struct { Rule Rule; Addresses []int; Name string; FromName bool; HandOff *HandOff; Login string; LoginWithheld bool } // indexes into Addresses;
                       // Name for same-name; FromName: a same-local-part made from the name; Login (wave 10): same-forge-account's account, as GitHub
                       // last showed it, LoginWithheld when it is a local part of an address of the run (Directory.LoginWithheld)
type HandOff struct { Gap time.Duration; SharedFiles int; Files []File }      // Files: the first five, by repository and path
type File struct{ Repo, Path string }                                          // Repo.Key
type Rule string       // RuleSameName, RuleSameLocalPart, RuleSameAccount, RuleSameForgeAccount (#103: GitHub's commit-to-account links in
                       // FactSet.Forge, its pull requests' commits and Links, read by Directory, a noreply address no link names by its login when
                       // the run links that login; vetoed by two account ids; high, wave 10 for an address whose commits carry names that differ too), RuleHandOff
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
	Eligibility, BlindSpots, Gaming, Oracle, Validity        string // Validity: a ranked metric's beta mark, which they share
	Reference                                                *Reference // a ranked metric's reference result (docs/validation.md#reference-results); nil for none
}
// Reference holds validate's numbers on the model panel's labels, from which the verdict follows (rank.Separation.Verdict), so
// the words a page prints cannot drift from the numbers; Source and About are the panel run's, the same for every metric
type Reference struct { Source, About string; rank.Separation }
func (d Definition) RankKind() rank.Kind // ARCH-10: rank.Ratio for a share (Ratio), else rank.Sum; no metric declares a kind of its own, so ranks and values print in one unit
type Input struct {
	Facts  *domain.FactSet     // required, with Now
	Policy *policy.Policy      // nil: no bot or AI patterns and no [week] days, so Days must be set
	Who    *identity.Directory // nil: identity.New(Facts, Policy); else built over the same facts, Forge included (it links logins)
	Days   int                 // landed@1's and landed@2's window, at least 1; 0: the policy's [week] days. Compute checks it before any metric runs
	// #104: the run's identity candidates (identity.Candidates over the whole run, with Who), which a scan passes: knowledge_loss@1 and
	// knowledge_concentration@1 count those holding lines in each area and give their value with them merged in Detail (metrics/identities.go,
	// the metrics' own functions over merged lines, so the definition is applied once); nil: no such Detail
	Candidates []identity.Candidate
}
type Metric interface {
	Definition() Definition
	Compute(in *Input) ([]domain.MetricValue, error)
}
type Ranked interface {
	Metric
	Clusters(in *Input, scope domain.Scope) (RankInput, error)
}
// trace.go (#109): a metric that is not ranked whose values name the facts they counted, by its own projection and rule:
// about a person, landed@1 and landed@2 (the landings), surviving@1 (the blamed commits), majority_owner@1 (the files); (wave 10)
// about a repository, a group or all, landed@2's totals (their landings, each with its copies) and review_coverage@1 (every
// change of its window with its outcome, reviewCoverageItems: the rule its values count by, so the two cannot disagree)
type Traced interface {
	Metric
	Trace(in *Input, subject domain.Subject) (domain.Trace, error) // every repository of the run (the subject's scope's, for one about
	                                                                // code); an empty trace, with the window, for no value or a kind it does not trace
}
// RankInput: the metric owns its window, its eligibility and its population (all part of its versioned definition), and its
// kind follows from its definition, so a caller ranks with rank.Intervals(def.ID, scope, ri.Window, def.RankKind(), ri.Data,
// ri.Options) and restates nothing: no caller filters bots.
type RankInput struct {
	Window  domain.Window // the metric's window for Facts.Now (durable@1: [now-365d, now-90d)); joins ranks to MetricValue.Window
	Options rank.Options  // MinN from the eligibility rule; Blocks for Sum; Replicates 0 = 1000
	Data    map[domain.Subject][]rank.Cluster // each person's clusters, sorted by Change: what every caller ranks (app.rankAll), scores
	                                          // (validate) or lists as people's (the MCP server's changes); never a bot's or an agent's; nil when empty
	Bots    map[domain.Subject][]rank.Cluster // each bot's and agent's (identity.Directory.Bot), for explain alone (#75); nil when none
}
func All() []Metric                                    // stable order, docs/metrics.md's catalog's: the registry, one list (a new metric is one file plus one line at its place)
func Lookup(id string) (Metric, error)                 // an unknown id is a *domain.UsageError
func Compute(in *Input) ([]domain.MetricValue, error) // every metric, sorted: what a scan stores
// WhoKnows answers who_knows@1 for the code under a repo-relative path prefix ("" is the root) in one repository,
// or in all when repo is ""; per query, never stored by a scan. Wave 10: it counts prod lines, or, under a prefix that holds no
// surviving prod line, the lines of every other role there (the path value's Detail <role>_files names them), so tests/ and
// docs/ are answered for their own lines rather than as nobody's. A repo that is no key is matched by its
// redact.URL form, so a remote pasted with credentials finds its repository; a repo that matches no key either
// way wraps domain.ErrNotFound, and the error quotes only the scrubbed form.
// Wave 11 (Ruby F10): days is the window of recent landers and reviewers, [now - days, now); 0 or less is WhoKnowsDays (90). Whether
// the run's history holds the window is app.WhoKnows' to check, as explain's is.
func WhoKnows(in *Input, repo, prefix string, days int) ([]domain.MetricValue, error)
const WhoKnowsDays = 90 // who_knows@1's window of recent landers and reviewers when the query names none
// PathKnowledge (#105, #106) answers knowledge_loss@1 and knowledge_concentration@1 for the code under a repo-relative path
// prefix ("" is the root, whole segments as WhoKnows matches them) in one repository, or in each when repo is "" (matched
// as WhoKnows matches it): the computation a scan stores per repository and top-level directory, over the prefix's files,
// activity read over every repository of the run, so a top-level directory's answer is its stored area value and the
// root's the repository's, the identity detail included when in.Candidates holds the run's candidates, as a scan's input
// and a stored run's (app's stored.input) do. Values are about the prefix (subject repo for the root, else area
// "<key>:<file>" or "<key>:<dir>/"), in the repository's scope; per query, never stored. app.WhoKnows calls it, for the
// command line's who-knows and the MCP server's who_knows, to drill into an area such as pkg/ without a second
// implementation of either metric; overview's areas are the stored values (report.Areas).
func PathKnowledge(in *Input, repo, prefix string) ([]domain.MetricValue, error)
// corrected.go (wave 10): the one list of the corrections made to a metric's definition in place, before the first release (keeping its
// version), as a stored value shows one by its data, never by a version: the Detail key every value of the metric about a subject of Kind
// has carried since. review_coverage@1 without via_other_branch (counted by landing), surviving@1's person values without merged_365d,
// and knowledge_loss@1 without going_quiet_num (activity on the default branch alone; knowledge_concentration@1 With it, whose identity
// detail the same wave moved; Lacks the commits of other refs, which such a run never kept)
type Correction struct { Metric domain.MetricID; Kind, Key string; With []domain.MetricID; Lacks string }
// Corrected: the corrections a stored run's values predate (some value of the metric about a subject of its kind lacks the key), in the
// order they were made. app's load computes those metrics (Metric and With) again from the run's facts (stored.computeUnstored), so a
// page never shows a stored value beside what it computes per query by the corrected rule (explain's list of review_coverage@1's
// changes, each person's last activity, PathKnowledge), and logs "scan again" for a correction that Lacks facts the run never kept
func Corrected(values []domain.MetricValue) []Correction
// activity.go (wave 10): knowledge_loss@1's activity, the one rule for who has gone quiet, which knowledge_loss@1, PathKnowledge and every
// page's last activity of a person read: in the history window before Now, a landing holding a commit credited to them (the landing's time),
// a review they submitted, a pull request they opened (its creation; GitHub's update time is anyone's) or pushed a commit of their own to that
// the clone holds (its author time), and a commit credited to them on another ref (RepoFacts.RefCommits, its author time). A forge account is
// identity.Directory.Reviewer's person. Per query, never stored; bots and agents left out; one entry per canonical email or "@login"
func LastActivity(in *Input) (map[string]domain.Activity, error)
// PersonalAIShare answers ai_assisted@1's share for one person (email: canonical) in every scope they have a
// cluster in; per query, never part of Compute or stored by a scan, because ADR 0007 shows it only to that
// person: app.Me is its only caller. A bot, or an email without clusters, gets no values.
func PersonalAIShare(in *Input, email string) ([]domain.MetricValue, error)
// changes.go (#77, docs/validation.md): the run's changes, the grain validate scores labels at and label samples from, and each
// ranked metric's measure of one change: what the change adds to the metric's clusters, so no second definition of a metric
type Change struct {
	Repo     string    // Repo.Key
	Landings []string  // the pull request's merge commit's landing (or the landing) first, then the earlier ones a rebase merge put on the branch
	Time     time.Time // Landings[0]'s committer time
	Pull     *Pull     // nil for a landing no pull request of the run's GitHub data landed
	Bot      bool      // a bot's change ((*view).botLandings)
	Added, Removed, Files int // numstat lines of its landings' own diffs (a merge's first-parent diff, as landed@1 reads it) on paths with a role; those paths
}
// (reviewsIndex).changes (unexported, wave 10): the one grouping of a repository's changes, which Changes lists (every change
// with landings) and review_coverage@1 counts: a merged pull request's landings (reviewsIndex.landings), a landing none landed, and
// a pull request merged into another branch whose merge commit a merge landing's map holds (changeView.via), which claims its merge
// commit and its own commits the map holds; a change's own work (changeView.own) is its commits that are no copy and that no such
// pull request claimed. (*repoView).markCopies (wave 10): a copy is a non-merge commit a merge brought whose patch id a commit of
// an earlier, not reverted, landing has, by landing time, that is not its ancestor (git cherry's rule: descends walks its parents
// and stops at the first-parent chain, placed by firstParentOrder); counts() leaves it out of durable@1 and simplifying@1,
// landedV2Parts out of landed@2 (a scope's totals count copies) and of surviving@1's landings_365d
// (*view).botLandings (unexported): the one rule for a bot's change or landing (metrics.md#units): every commit the landings
// brought in, merges aside, is credited (identity.Credit, so an agent's commit counts for its first human co-author) to a bot;
// an empty merge by its own commit's credit (botCommits, which review_coverage@1 applies to a change's own commits). Who wrote it, as landed@2 credits it, not who merged it or opened its pull
// request. A metric that leaves a bot's landings out (review_coverage@1's bot_landings) reads it rather than making another
type Pull struct { ID, Repo string; Number int; Title string } // ID: what reviews_given@1's clusters are keyed by; Repo "owner/name"
// Changes: every landing of every collected repository in exactly one change, a pull request's landings grouped as who_knows@1's
// reviewers find them (reviewsIndex.landings); sorted by Repo, Time, then first SHA. The one landing-to-pull-request map: a
// metric that needs it (review_coverage@1) reads it rather than making another
func Changes(in *Input) ([]Change, error)
type Scorer struct{ /* one ranked metric's clusters in scope all, summed per cluster Change, or its own measure of a change */ }
func NewScorer(m Ranked, in *Input) (*Scorer, error)
func (s *Scorer) Window() domain.Window              // the metric's window (RankInput.Window)
func (s *Scorer) Scores() string                     // which changes it measures, as a phrase for a caveat (an unexported scores method on the metric)
func (s *Scorer) Score(c *Change) (float64, bool)   // false for a change that landed outside Window; else a ratio's Σ Num / Σ Den, a sum's Σ Num,
                                                    // over the clusters keyed by the change's landings, false without one; or, for a metric
                                                    // whose clusters are not the change's own (an unexported measureChanges method), its own
                                                    // measure: reviews_given@1's is the people other than its author, bots aside, who
                                                    // reviewed its pull request before Now (0 for none), and a change that is no pull
                                                    // request of the run's GitHub data is not measured

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
func AUCInterval(id domain.MetricID, pos, neg []float64) (lo, hi float64) // 95% percentile interval over 1,000 replicates that resample pos and
                                                                        // neg apart, each sorted first, so the order of the labels does not
                                                                        // matter, from a stream derived from id; 0.5, 0.5 when either is empty
func AUCDiffInterval(id domain.MetricID, pos, neg [][2]float64) (lo, hi float64) // the same of AUC(a) − AUC(b) for two scores {a, b} of each
                                    // change, paired: each replicate resamples the changes once and takes both AUCs over them (DeLong et al.
                                    // 1988 is the closed form); its own stream; 0, 0 when either is empty. validate's metric against size alone
const BarAUC, BarLabels = 0.7, 50 // the bar (product.md, ADR 0014): validate's Beta and status, label's progress, the verdict below
// Separation is validate's numbers for one metric: AUC, Lo, Hi; N; Size, size alone's AUC over the same changes; DiffLo, DiffHi,
// AUCDiffInterval of the metric and size. Verdict is docs/metrics.md#validity-checks' rule, the first that holds: VerdictTooFew
// (N < BarLabels), VerdictNone (the interval holds 0.5), VerdictReverse (below it), VerdictWeak (AUC < BarAUC), VerdictBeyond
// (DiffLo > 0), else VerdictLikeSize. Result is the verdict with its numbers, the Validity line's words; the two size verdicts add
// the difference and its interval. One rule for validate's output and for every published reference (metrics.Reference)
type Separation struct { AUC, Lo, Hi float64; N int; Size, DiffLo, DiffHi float64 }
func (s Separation) Verdict() string
func (s Separation) Result() string

// report: a view that computes no metric. values.go is the one place that maps each printed
// number to the metric@version, subject, scope and Detail key it reads (spec/report-json.md#where-the-numbers-come-from).
type Model struct{ /* stillhere.report/v1: overviews (per group; every repository with --scope all or without groups), repo cards, ranks, caveats and provenance on every section */ }
type Document interface{ /* sealed: *Model, *Explain, *WhoKnows, *Me, *Validation, *Catalog, *Identities, *LabelSample, *LabelCard */ }
func Text(w io.Writer, d Document, opts ...Option) error     // a terminal: 100 columns, untrusted text escaped, no emails and no name that gives one away;
                                                             // times say UTC (#79); each table's first 10 rows and a count of the rest (SR-14)
func Markdown(w io.Writer, d Document, opts ...Option) error // GitHub-flavoured: untrusted text escaped, mentions and references unlinked, no emails, names that give one away or repository paths
type Option func(*printer) // how Text and Markdown print; JSON holds every row
func AllRows() Option      // every row of every table: the command line's --all
func JSON(w io.Writer, d Document) error     // stable order; empty lists [] and maps {}, absent sections null; invalid UTF-8 as U+FFFD, NaN and ±Inf as null; written only when whole
// addresses.go (#68): all three print an email only at the published shape's places for one (redact.Place.Email: email, a person
// subject's key; text and Markdown print none of those), and every other address the redact rule finds, with the page's people as
// its vocabulary, as "(address withheld)": a commit subject's, a git message's, a directory's. The model keeps the text, which the
// MCP server withholds by its own operator; text and Markdown render a copy, decoded from the withheld JSON, when there is any.
// #90: the vocabulary is the run's people, not the page's: Build, NewExplain, NewWhoKnows, NewMe and NewIdentities keep People.Addresses
// in a field no format encodes, which the renderers read (an unexported knower interface), so a page withholds the address of a person
// of the run who is not on it, a dotless domain's included, as the MCP server's egress does over app.Names.People
func WholeRun(scope string, groups int) bool // a report holds the overview of every repository (scope all, or no groups): Build's rule,
                                             // which app.AIShares reads too, since only that overview shows the whole run's AI-assisted share
type People interface{ Name(email string) string; Bot(email string) bool; Withheld(email string) bool; NameKey(email string) string;
	Addresses() []string } // *identity.Directory implements it; NameKey (#69) is what every unmerged-identities caveat groups names by, the identities
	                       // same-name rule's key; Addresses (#90) the run's addresses every page withholds
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
	Landed     Landed    // the landed window's days and sort (landed@2's, #108), and whether the command line set them
	History    domain.Window
	Timings    *Timings
	Scope      string    // the scan's rank scope: domain.ScopeAll (or a run without groups) adds an overview of every repository
	Forge      *ForgeRead // the GitHub data a scan run read, which app builds; nil for week, and when the reader cannot tell
	Commands   Commands   // #69: how its sentences name the commands they send the reader to; nil: the command line's (CLI)
	Activity   map[string]domain.Activity // wave 10: each person's last activity (metrics.LastActivity), which still-here rows, a card's made_of and
	                                      // who-knows' people show (last_active, last_activity, quiet_in_days, going_quiet); nil: none shown
	Candidates []identity.Candidate       // wave 11 (TS F10): the run's identity candidates (week: its own facts'); ExplainInput, WhoKnowsInput
	                                      // and MeInput carry them too. An unmerged-identities caveat names a shared display name only when one
	                                      // links two of its emails, the names identities suggests a line for, and counts the others apart
	                                      // (week's says only that its own commits link them by no rule: identities reads a scan)
}
// commands.go (#69): the reader's vocabulary. A hint in a caveat or a use case's error names a command through it, so an
// agent is sent to the MCP server's tools and a person at a terminal to stillhere's commands, one mechanism for both: the
// MCP server passes its own (app.Deps.Commands); ExplainInput, WhoKnowsInput and MeInput carry it too
type Commands interface {
	Metrics() string                             // the registry: CLI "'stillhere metrics'"
	Identities() string                          // suggested .mailmap lines: CLI "'stillhere identities'"
	Mailmap() string                             // #104: those lines as lines: CLI "'stillhere identities --format mailmap'"; MCP the candidates' mailmap field
	Report() string                              // a run's numbers per repository and group: CLI "'stillhere report'"
	Ranks(metric string) string                  // a ranked metric's tables: CLI "'stillhere report --rank M'"
	Explain(metric string, kinds []string) string // explain about one of kinds; "" when the reader cannot: the CLI by --person, --repo, --group or --area (wave 10)
	WhoKnows() string                            // who knows a path, who_knows@1, which no scan stores: CLI "'stillhere who-knows PATH'"
	OwnPage() string                             // a person's own page: CLI "'stillhere me --email EMAIL'"
	PersonKeys() string                          // where the id or email of a person whose name every page withholds is: CLI the JSON report, MCP the id in every answer
	Paths() string                               // wave 10: where the paths under a path are listed with the rule that decided each (classify.Explain): CLI
	                                             // "'stillhere paths PATH'"; "" for MCP, which has no such tool, so who-knows' no-role and other-roles caveats name none
}
type CLI struct{}                     // the command line's Commands
func CommandsOr(c Commands) Commands  // c, or CLI{} when nil
// #69: a caveat goes where the number it qualifies is (#48): every answer that shows a ranked metric's values, ranks or clusters
// carries its validity note, ValidityNote: MetricInfo.Validity, then its Reference ("Reference (Source): Result. About"), the
// words of its docs/metrics.md Validity line; the multi-metric ones (NewMe, app's Changes and Highlights, a report's own caveats
// for its rank tables, which text and Markdown print instead of each table's: Caveat.Standing) through this, one caveat for the
// metrics that share a note and a reference's source, naming them, saying the note, the source and About once and each metric's
// Result after its name; a rank table and explain word their one metric's. NewMe adds ai-lower-bound when the page holds
// ai_assisted@1, and NewExplain when it explains one; NewWhoKnows adds unmerged-identities when two people it lists share a
// name (People.NameKey), as the report's sections, explain and me do
func ValidityCaveats(ms ...MetricInfo) []Caveat
func ValidityNote(m MetricInfo) string
// MetricInfo.Reference *Reference {Source, Verdict, Result, About string} (json "reference", null for none): app's metricInfo
// fills it from metrics.Definition.Reference through NewReference(source, about, rank.Separation)
func NewReference(source, about string, s rank.Separation) *Reference
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
	Repo, Key             string     // "owner/name" and the run's Repo.Key it joined, both as the run read them (RunWatermark)
	Updated, Synced, From *time.Time // ForgeWatermark's, nil for its zero times
	Missing               int        // ADR 0013: the last check's shortfall, which gives a forge-incomplete caveat
	Pending               bool       // json "-": pages of an unfinished pass, so GitHub data although no Synced
}
func OwnPageOnly(metric domain.MetricID) bool // a person's values only their own page shows: ai_assisted (ADR 0007); explain refuses them
func AIShareIn(v Value) (whole bool, key string) // #68: where a value holds an AI-assisted share: whole for ai_assisted's own values, key
                                                 // "ai_assisted_share" for surviving's copy of its scope's share; the one place, so a reader that withholds a share
                                                 // (the MCP server, by app.AIShares) withholds every copy of it. #87: a whole share's own Detail, ai_assisted's
                                                 // cohort comparisons, can be one or two people's, so the MCP server sends a share it shows with no Detail at all
                                                 // (fail closed: a key a later version adds stays local too)
type Area struct { Path string; Lines int; KnowledgeLoss *Share; People, PeopleInactive *int; Concentration *Concentration; ProdFiles *int }
func Areas(values []domain.MetricValue, repo string) []Area // #106: one repository's area values of knowledge_loss@1 and knowledge_concentration@1,
                                  // a row per top-level directory, largest first (lines, then lines gone quiet, then path): the one mapping of them,
                                  // which MCP's overview of a repository and app.WhoKnows, for a path that is a top-level directory (WhoKnowsRepo.Area),
                                  // read, and a page that lists areas should read too
type RankSet struct { Metric MetricInfo; Scope domain.Scope; Window domain.Window; Percent bool; MinN int; Intervals []domain.RankInterval } // bots left out; Percent: metrics.Definition.Ratio
// #104: knowledge_loss@1's share and knowledge_concentration@1 carry, from their values' Detail, what the run's identity candidates
// holding lines in the scope would make them: Share.IfMerged *ShareIfMerged {Candidates, High int; HighNum, HighValue, AllNum, AllValue float64}
// (nil for every other share) and Concentration.IfMerged *ConcentrationIfMerged {Candidates, High, HighAuthors, AllAuthors int}, High
// counting the candidates with high-confidence lines (identity.Candidate.High); every card, overview and explain of either metric where
// merging would move one of them then carries an unmerged-identities caveat with the range (candidatesCaveat, joined to the
// shared-names one in a section that has both); where it moves neither, the IfMerged fields alone
// wave 10: knowledge_loss@1's share carries GoingQuiet *ShareGoingQuiet {People int; Lines, Num, Value float64; Holders *[]QuietHolder} from its
// going_quiet detail, Holders the section's still-here rows within 30 days of the activity line (QuietHolder {Email, Name string; NameWithheld
// bool; Lines int; LastActive time.Time; LastActivity string; QuietInDays int}), soonest first, nil (json null) for a share no rows back (an
// area's, a who-knows path's), since JSON writes a nil list as []: a card and an overview name who goes quiet,
// which text and Markdown print beside the share and the MCP server cuts to its rows; a going-quiet caveat says the count and where they are listed
// The report's sections that are new in wave 9 (report-json.md#the-objects): the week table is landed@2's (#108), WeekTable{Total *WeekTotal;
// Humans, Bots []WeekRow}, a row {Landings, Commits, Merged int; LinesAdded, ProdLinesAdded *int (nil: unknown); LinesUnknown int}; a card's and an
// overview's ReviewCoverage *ReviewCoverage (#107, nil without GitHub data); an overview's Repos []RepoRow (#105, one line per member, read from
// the cards); a card's Areas []Area (Areas, #105) and Landings.Last; a rank table's Beside, the Detail key it prints beside each estimate
// (rankBeside in values.go: reviews_given@1's no_comment_share, PF-5), and each row's Beside value.
// Wave 10: WeekTotal.Copies (landed@2's totals' copies); ReviewCoverage counts changes, with ViaOtherBranch, BranchMerges, Copies and
// NamedElsewhere beside its wave-9 counts; StillHereRow.Merged365d beside Landings365d; a RankRow's and a MeRank's RankLo, RankHi, Lo
// and Hi are *int and *float64, nil (JSON null) for a row no bootstrap ranked (below the minimum, or a bot's), never 0:
func RankedRow(sub Subject, ri domain.RankInterval) RankRow // an eligible person's row with its intervals, any other's with none; the
                                                            // rank tables, me's ranks and the MCP server's top tiers build rows by it
type Area struct { Path string; Lines int; KnowledgeLoss *Share; People, PeopleInactive *int; Concentration *Concentration; ProdFiles *int }
func Areas(values []domain.MetricValue, repo string) []Area // #106: one repository's area values of knowledge_loss@1 and knowledge_concentration@1,
                                  // a row per top-level directory, largest first (lines, then lines gone quiet, then path): the one mapping of them, which
                                  // a card's areas and the MCP server's overview of a repository read
func Build(in *Input) *Model
// The read commands' answers live here too, so the renderers and the MCP server share one shape.
// Each has Provenance (run id, as-of, policy, HEAD SHAs, metric@version, when the GitHub data was synced) and Caveats:
type Explain struct { Provenance Provenance; Metric MetricInfo; Subject Subject; Values []Value; Changes []Change;
	Landings []ExplainLanding; Commits []ExplainCommit; Files []ExplainFile; Coverage []CoverageChange; Caveats []Caveat } // #109: a Traced
	// metric's lists, a domain.Trace with names and subjects; Coverage (wave 10): review_coverage@1's changes, each with its pull request
	// (and its title, local only), whether it merged into another branch, its author as a person Subject and its outcome; text and Markdown
	// list the first 10: Markdown with coverageOutcomeText's sentence, text, whose table shares 100 columns, with coverageOutcomeCode's
	// short code (a column marked keep, which textTable never cuts) and a legend line per code under the table, and a repository column
	// only when the list spans several. Neither says "pushed directly" of a change with no pull request in the GitHub data
type WhoKnows struct { Provenance Provenance; Path, Repo string; Days int; Repos []WhoKnowsRepo; Caveats []Caveat } // owners, top authors, recent landers and reviewers (#38), bots;
	// wave 11: Days, the window of recent landers and reviewers the answer was computed over (WhoKnowsInput.Days, which app.WhoKnows resolves);
	// wave 10: each WhoKnowsRepo's Roles and Files (the roles counted: ["prod"], or a path's own roles where it holds no prod line, with
	// an other-roles caveat), its Uncounted []RoleLines{Role string; Lines, Files int} (the other roles' lines a path with prod lines leaves
	// out, with an uncounted-roles caveat where those of roles other than test outnumber them), and each Knower's last activity (WhoKnowsInput.Activity);
	// #105, #106: each WhoKnowsRepo's KnowledgeLoss, People, PeopleInactive and Concentration, metrics.PathKnowledge over the path, so
	// a top-level directory's are its row of Areas; KnowledgeLoss and Concentration carry IfMerged (#104), and an unmerged-identities
	// caveat says how far merging would move them (candidatesCaveat, joined to the shared-names one)
type Me struct { Provenance Provenance; Person Subject; Values []Value; Ranks []MeRank; Metrics []MetricInfo; Caveats []Caveat }
// #66: Metrics are the definitions of the page's metrics (NewMe keeps them from MeInput.Metrics); text and Markdown print a value
// as a share exactly when its metric's Ratio says so, as explain prints by its Metric's, so a share of den 1 is "100.0% (1 of 1)"
type Validation struct { Provenance Provenance; Labels, Labellers, Changes, Meaningful, Routine, Noise, Unmatched int; Metrics []MetricAUC;
	Scores []ChangeScore; Caveats []Caveat } // Labels: rows; Changes, Meaningful, Routine, Noise: distinct changes; Scores: --scores only
type MetricAUC struct { Metric string; AUC, Lo, Hi *float64; N, Meaningful, Routine int; SizeAUC, SizeLo, SizeHi, SizeDiffLo, SizeDiffHi *float64;
	Verdict string; Beta bool }
	// AUC nil without both labels; Lo, Hi its 95% interval (rank.AUCInterval); N: changes the metric scored; SizeAUC: change size
	// alone (Added + Removed) over those same changes, its interval by the same bootstrap; SizeDiffLo, SizeDiffHi: AUC less SizeAUC,
	// paired (rank.AUCDiffInterval); Verdict: rank.Separation.Verdict of them, "" with no AUC, which text and Markdown print with its
	// numbers (Result) under the table; Beta: these labels do not clear the bar, which lifts no mark and asks nothing of size
func (a MetricAUC) Separation() (rank.Separation, bool) // false with no AUC
type ChangeScore struct { Change, Repo string; Pull *LabelPull; Landed time.Time; Label, Size string; Added, Removed int;
	Scores map[string]*float64 } // one labelled change (noise included), each ranked metric's measure of it or nil: what each AUC reads
var ScoreColumns = []string{"change", "label", "repo", "pull_request", "landed", "size", "added", "removed"} // then one per metric, by id
func Scores(w io.Writer, v *Validation) error // validate --scores as a CSV, the sheet's cell rules
// labels.go (#77, ADR 0014): stillhere label's answers, blinded: no metric value, rank or author
type LabelSample struct { Provenance Provenance; Seed uint64; Asked, Pool int; Labeller string; OtherLabellers, Labelled int; Progress []LabelProgress;
	Strata []LabelStratum; Changes []LabelChange; Caveats []Caveat } // Asked 0: until the bar; Labelled: the labeller's labels and skips
type LabelProgress struct { Metric, Ago string; Labelled, Sampled, Needed int } // per ranked metric: its labels toward rank.BarLabels
func (s *LabelSample) ProgressText() string // one sentence of Progress; "" with nothing to label
func (s *LabelSample) Ready() bool          // every ranked metric has rank.BarLabels labels it scores
func LabelHeader(w io.Writer, s *LabelSample) error // what the interactive session (and --sheet, on stderr) prints first: summary, progress, caveats
type LabelStratum struct { Window Interval; Ago string; Metrics []string; Size, Repo, RepoKey string; Pool, Sample int } // window × size × repository
type LabelChange struct { Change, Repo, RepoKey string; Landings []string; Landed time.Time; Ago, Size string; Pull *LabelPull; Title string;
	Commits []string; MoreCommits, Added, Removed int; Files []LabelFile; MoreFiles int; Label string } // Label: the labels file's so far, or ""
type LabelCard struct { Index, Of int; Change LabelChange; Patch string; Truncated bool } // one change of the interactive session; Patch once asked for
var LabelSizes = []string{LabelSmall, LabelMedium, LabelLarge}
var SheetColumns = []string{"change", "label", "labeller", "repo", "pull_request", "landed", "window", "size", "added", "removed", "files", "title", "commits"}
func Sheet(w io.Writer, s *LabelSample) error // CSV for a spreadsheet, the labeller in every row, addresses withheld as on every page, no cell a
                                               // formula; validate reads it as it is
type Catalog struct { Metrics []MetricInfo }
// #26 (spec/identities.md): emails only in fields named email, and a name that gives an address away withheld as "", by the
// rule every page applies (Who.Withheld, #52), so the MCP server can drop emails by field; text and Markdown print no
// address and no part of one
type Identities struct { Provenance Provenance; Scope Scope; Addresses, Bots, Shared, Oversized int; Candidates []IdentityCandidate; Guesses int; Rules []IdentityRule; Caveats []Caveat }
	// Guesses (wave 10's review): how many Candidates are hand-off guesses, which come last
type IdentityCandidate struct { Confidence string; Guess, Commented, Chained bool; Rules []string; Name string; Addresses []IdentityAddress; Links []IdentityLink;
	Mailmap *[]string } // Commented: every line commented out, no high set; Mailmap (#69): MailmapLines when IdentitiesInput.Mailmap asks, else nil (json null): they are made of emails
type IdentityAddress struct { Email, Name string; NameWithheld bool; DomainKind string; Canonical bool; Commits, CoAuthored int; First, Last time.Time; Repos []string;
	High bool; Aliases int; NamesDiffer bool; Names []string; NamesWithheld int } // NamesDiffer, Names, NamesWithheld: identity.Address's (wave 10); High (#103): in the candidate's high set, so its lines are written as lines; Aliases (#53): how many emails git's mailmap rewrote to it,
	                         // which MailmapLines maps to the canonical address too, by name and email (git prefers that form), and only there: no field names them;
	                         // MailmapLines writes no line for an address git rewrites to the canonical one wherever its commits are
type IdentityLink struct { Rule string; Addresses []int; Name string; FromName bool; HandOff *IdentityHandOff; Login string; LoginWithheld bool } // Login (wave 10):
	// same-forge-account's account, "" where it is a local part of an address of the run, which LoginWithheld says; the mailmap form prints it
type IdentitiesInput struct { Run *domain.Run; Facts *domain.FactSet; Scope domain.Scope; Survey *identity.Survey; Who People; Forge *ForgeRead; Mailmap bool } // Facts: the scope's;
                                                                                                                                               // Who: the run's directory, which Survey was found with
func NewIdentities(in *IdentitiesInput) *Identities // identity.Candidates' survey as an answer, with provenance (AnswerProvenance) and caveats (mailmap-at-scan)
func Mailmap(w io.Writer, id *Identities) error    // the suggested .mailmap lines, evidence as # comments; every line commented out but those high evidence backs
func MailmapLines(c IdentityCandidate) []string    // #69: one candidate's lines exactly as Mailmap writes them, which it writes through
func NewExplain(in *ExplainInput) *Explain    // values, a ranked metric's clusters per repository as changes, and a Traced metric's
                                              // trace (ExplainInput.Trace, #109); no value: a no-values caveat saying why, from ExplainInput.Window (#92)
func NewWhoKnows(in *WhoKnowsInput) *WhoKnows // who_knows@1's values under the path, as lists, and the path's knowledge (WhoKnowsInput.Knowledge, #105);
                                              // its caveats count the facts under the path (WhoKnowsInput.Facts), but the one about a repository itself,
                                              // no-ignore-revs, reads the repository's whole facts (WhoKnowsInput.Whole, wave 11), so it says what the card says
func NewMe(in *MeInput) *Me                   // the person's values and their row in each RankSet
// paths.go (stillhere paths): the answer and its rows, arranged from rows app explained (PathsInput.Rows, by Repo.Key,
// each with classify.Explain's rule as a PathRule), with each repository's count of classified paths and of the listed
// ones by class, and a legend of the rules with how many listed paths each decided; JSON per report-json.md
type PathsInput struct { Run *domain.Run; Facts *domain.FactSet; Prefixes []string; Repo, Role string; Skipped bool; PolicySource string; Rows map[string][]PathRow; Who People }
// (wave 11: Who tells a bot's commit from a person's in the caveats, as AnswerCaveats' who does; nil: they do not say)
func NewPaths(in *PathsInput) *Paths
func SkipClass(s domain.Skip) string               // "skipped:" + s, a skipped class as Paths counts it
func SurvivingProdLines(rf *domain.RepoFacts) int  // the lines blame attributes on a repository's prod paths at HEAD, which the inline-tests warning states its share of
func Grouped(n int) string                         // n with thousands separators, as every page writes numbers, for a message a use case words
// For an answer app builds itself (the MCP server's, validate's) over every repository of fs:
func AnswerProvenance(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metrics []string) Provenance // dated by forge, as every section is
func AnswerCaveats(fs *domain.FactSet, who People) []Caveat                                              // what could make it wrong; who (wave 11) tells a bot's commit from a
                                                                                                          // person's in the no-ignore-revs caveat's formatting candidates (formattingCommits); nil: it does not say
func ForgeCaveats(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metrics []string) []Caveat        // its GitHub numbers' caveats, per metric in metrics that reads GitHub data; nil for none, and in a run that read none
func ForgeCaveatsAlone(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metric string) []Caveat     // one such metric's, as a section about it alone says them: in a run that read none too

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
// #79: a reader in a directory it cannot write (a read-only mount), where SQLite refuses even a read-only open of a WAL-mode database for want
// of its -shm file (SQLITE_READONLY_DIRECTORY), reads it read-only with immutable=1 when no -wal file holds data: OpenReadOnly, and OpenExisting
// in place of its read-write open. A -wal that holds commits is a *domain.UsageError that says to make the directory writable or copy both
// files; neither error says the reader must write. Another user may still write the directory (a team server's copy), and immutable=1 takes
// no lock, so each read guards itself: refused while a -wal holds commits (a writer at work), on a connection of its own (no page cache
// outlives a read), and read again once, then refused, when the file changed under it (a checkpoint)

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
	Clock     func() time.Time  // nil: time.Now; the local clock, for the time since GitHub's last Date and a rate limit's hold
	ChangedPaths bool           // #56: git writes version 2 changed-path filters (gitcli.Runner.AtLeast(2, 46)): mirrors get them
}
func New(ctx context.Context, cfg Config) (*Source, error) // no token → *domain.UsageError
// #71: the one rule for an owner (organization or user) or repository name, which Repos, Mirror and ReconcileMirrors apply: ASCII letters, digits, ".",
// "_" and "-", neither "." nor "..". The command line checks --org, and each --repo's owner and name (#111), with it before a sync begins its
// run, so a URL pasted as an organization or a repository, token and all, is refused before anything records it (#71)
func ValidName(s string) bool
// *Source implements domain.ForgeSource. At most three API requests are in flight (#100), paced by each query's
// rateLimit cost; Retry-After and X-RateLimit-Reset are obeyed, inside a 200 too, and a rate limit one request meets
// holds every other until it is over, one that meets a shorter limit during a longer hold included, which waits out the
// longer; secondary limits and 5xx back off with jitter; a connection whose cursor does not
// advance fails. A response whose only errors are NOT_FOUND on fields its data holds as null (a deleted account, #99) is
// read as those nulls, counted in the log; any other error fails. Mirrors get the token only as an
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
type ExplainRequest struct { RunID int64; Metric domain.MetricID; Subject domain.Subject; Person, Repo, Group, Area string; AllRepos bool
	Days int } // MCP sends any subject (subject_kind all: domain.SubjectAll); the CLI sends one of Person, a person as a reader names them
	// (PF-7), which Explain resolves against the run's people (FindPerson), and (wave 10) Repo, Group or Area (REPO:DIR, a top-level
	// directory), resolved by FindScopes, or AllRepos (--all-repos), the whole run's own numbers. Days: landed@1's and landed@2's window
	// for the values and the trace alike, computed over the run's facts when it is not the run's [week] days; a *domain.UsageError for any
	// other metric, worded with no flag (the CLI's --days and MCP's days both reach it), and for a window longer than the history the run
	// read (historyOf: 365 days for a scan), whose answer is unknown, not empty. A subject with no value is domain.ErrNotFound worded
	// for its kind (explainMissing): an area's names the repository as the report does and points to Commands.WhoKnows
type WhoKnowsRequest struct { RunID int64; Path, Repo string; Days int } // wave 11: Days, the window of recent landers and reviewers, 0: metrics.WhoKnowsDays;
	// 1 to policy.MaxWeekDays and at most the history the run read (inHistory, explain's check), else a *domain.UsageError
type PathsRequest struct { RunID int64; Prefixes []string; Repo, Role string; Skipped bool } // stillhere paths
type MeRequest struct { RunID int64; Email string }                                // Email required: the CLI resolves no default (me needs --email); PF-7: any form
                                                                                  // a reader names a person by, which Me resolves (FindPerson)
type ValidateRequest struct { RunID int64; Labels []byte; LabelsName, Labeller string; Scores bool } // the CLI reads the file; app opens none.
                                                                                                    // Labeller "": every row; Scores: Validation.Scores
type SyncRequest struct {
	Org     string   // an organization's repositories, or
	Repos   []string // #111: these "owner/name" (whatever their flags but empty), with no other mirror moved
	Mirrors string; IncludeArchived, IncludeForks bool
	Since   time.Time // --since: a full pass reaches back to it when it is before the window; zero: the 365-day window
	Now     time.Time // the wall clock, read once by cli (sync takes no --now); stamps the run and ForgeWatermark.Synced
} // #71: the run records every option scrubbed (redact.Scrub), as a scan does
type SyncSummary struct { Repos, Synced, Failed, Incomplete, Unchecked, Pulls int; Took time.Duration; Moved []string } // what the closing line
	// prints; Moved (wave 11, KT-2): each move GitHub reported, "old/name → new/name", sorted: a --repo name it answered under another, or a
	// renamed mirror's old path (MirrorMove.Was), which the sync records as the repository's Aliases (PutForgeRepos)
type IdentitiesRequest struct { RunID int64; Scope domain.Scope; Mailmap bool }   // the zero Scope: every repository of the run; else a group or one repository;
                                                                                  // Mailmap (#69): each candidate's .mailmap lines, for a reader that may see emails
func Week(ctx context.Context, d Deps, r WeekRequest) (*report.Model, error)
func Scan(ctx context.Context, d Deps, r ScanRequest) (*report.Model, error)
// The read side reads the latest complete or partial run unless a run id is given, with the run's own policy:
func Report(ctx context.Context, d Deps, r ReportRequest) (*report.Model, error)
func Explain(ctx context.Context, d Deps, r ExplainRequest) (*report.Explain, error) // #69: a metric about a kind of subject its Definition.Subjects
                                                                                     // leaves out is a *domain.UsageError naming its kinds and what answers instead,
                                                                                     // before any run is read (who_knows@1, stored by no scan: Commands.WhoKnows)
// Paths (stillhere paths): every path of the run's repositories that passes the filters (under a prefix, of Repo, of Role,
// or Skipped), with classify.Explain's rule from the run's own policy; it reads the run, its policy and its facts, no
// metric. A Role that is none, Role with Skipped, or a Repo the run lacks is a *domain.UsageError. Repo is checked as
// WhoKnows checks it (repoArg: a name, GROUP/NAME, OWNER/NAME, a remote URL or the key, app.MatchRepos), the run's GitHub
// data read for it alone (wave 11: the names GitHub gives a repository, NamedRepo.Forge). A prefix no
// repository asked about holds gets who-knows' no-values caveat (noPathCaveat), which says how the run writes paths
func Paths(ctx context.Context, d Deps, r PathsRequest) (*report.Paths, error)
func WhoKnows(ctx context.Context, d Deps, r WhoKnowsRequest) (*report.WhoKnows, error) // #71: the path is scrubbed (redact.Scrub) before it is asked about or echoed. One
                                                                                       // that no collected repository asked about has (classified, touched or blamed)
                                                                                       // gets a no-values caveat: missing, never "nobody", and still no error. It states
                                                                                       // what the run holds, for every reader: with Repo, the run's other repositories
                                                                                       // that have the path; else that the run knows paths relative to a repository's root
func Me(ctx context.Context, d Deps, r MeRequest) (*report.Me, error)
func Validate(ctx context.Context, d Deps, r ValidateRequest) (*report.Validation, error) // #77: a label names a landing or a pull request (KB-19:
                                                                                         // #950, pr:950, owner/name#950), each metric scores it by metrics.Scorer.
                                                                                         // A change counts once: each labeller's last row for it, and over several
                                                                                         // labellers the label most gave (a tie is left out), so n counts changes and
                                                                                         // the interval resamples changes
// label.go (#77, ADR 0014): a stratified sample of the run's changes to label: per ranked window, by repository and size in
// proportion within it; each window's part a prefix of the window's sequence, so a larger sample extends a smaller one; people's
// changes a ranked metric scores, only. N > 0: the windows take turns up to N. N 0, until the bar: each window's shortest prefix in
// which each of its metrics scores rank.BarLabels changes its labeller has not answered noise or skip, at most 2 × rank.BarLabels
type LabelRequest struct { RunID int64; N int; Seed uint64; Labels []byte; LabelsName, Labeller string } // Labels: the file so far; Labeller:
                                                                                                          // whose rows it resumes ("" for unnamed)
const MaxLabelSample = 10000
func Label(ctx context.Context, d Deps, r LabelRequest) (*report.LabelSample, error)
func Metrics() *report.Catalog // the registry's definitions: the metrics command and the MCP server's metrics()
// wave 11 (KT-8): whether a scan stores the metric's values about people (metrics.Definition.Subjects), which the MCP server's explain
// asks before it reads a subject with no subject_kind as a person: a scope_arg names its kind, and a metric never about a person reads
// a bare subject as the repository or group it names (FindScopes), lets Explain refuse the kind for a person's, and answers any other
// with the scopes; unknown: a *domain.UsageError
func AboutPeople(id domain.MetricID, cmds report.Commands) (bool, error)
func Identities(ctx context.Context, d Deps, r IdentitiesRequest) (*report.Identities, error) // #26: suggested .mailmap merges with evidence, never applied; the identities command and the MCP server's identity_candidates(). Reads the run's facts, policy and GitHub data (#103), not its metrics. Its candidates are the whole run's, as the scan's caveats count them: a scope lists those with an address in its repositories, whole, with the scope's own address counts; a scope that names no group or repository of the run is a *domain.UsageError
// #14; ADR 0013 (#101, #100): the repositories are listed (or each --repo looked up) before the run begins, so a name GitHub
// does not know is a *UsageError with no run and no summary. Then per repository, a full pass (the window, or --since) for a
// first sync, after a check that found pull requests missing, or when --since reaches past the replica's From, else a pass
// down to the watermark; then the completeness check (ForgeSource.CountPullRequests against ForgeStore.CountForgePulls over
// the window), a full pass at once on a shortfall after the other kind (the shortfall recorded first, MarkForgeMissing), and
// MarkForgeChecked, a check that could not run included (From set, the stored Missing kept); then, whenever the mirror is up
// to date, the links pass (ForgeSource.LinkAuthors for the emails ForgeStore.ForgeLinks lacks, #103), whose failure is
// logged and fails nothing; three repositories at once. #71: logs at info every 10 pages, with the repository and its place
// ("3 of 80"). The summary comes back whenever the run began, interrupted or failed included
func Sync(ctx context.Context, d Deps, r SyncRequest) (*SyncSummary, error)
// Week and Scan return the model alongside domain.ErrNoFacts when no repository produced facts (a scan then
// stores nothing). A cancellation or a *domain.UsageError from a collector (an unreadable --ignore-revs-file) stops
// the run (runStopped); any other failure is the repository's, recorded on its RepoFacts. Scan reads the forge replica
// (ForgeStore.LoadForge) after discovery and before any git process, and a read error stops it; once collected, the
// replica's part ForgeJoin joins to its repositories is FactSet.Forge, and (wave 11, KT-2) a repository whose origin names its forge
// repository by an alias gets a forge-moved warning, one that joined nothing while the replica holds GitHub data of a repository
// whose history it shares forge-unmatched (forgeNameWarnings), which the run keeps. Wave 11's review: an alias joins only a clone
// holding a commit the forge repository's data names (aliasJoinsShared), and a clone of an old name beside one of the current
// name is left out with a duplicate-repo warning on the kept one (forgeDuplicates). It writes the run after computing everything:
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
type NamedRepo struct { Key, Name, Group, Path string; Forge []string } // Forge (wave 11): "host/owner/name" of the forge repository the
                                                                        // run joined it to and its aliases (ForgeFacts.RunJoin); nil without
                                                                        // GitHub data and in a StoredRun
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
	Repos []report.Repo; Areas []report.Area; Ranks []report.RankTable; Warnings []report.Warning; Caveats []report.Caveat } // Areas (#106):
	// one repository's, report.Areas over the stored values; nil for another scope
type StoredRun struct { Run domain.Run; Groups []NamedGroup; Repos []NamedRepo } // MC-17: a stored scan run by its scopes, as Names lists them
type PersonRequest struct { RunID int64; Email string }
type ChangesRequest struct { RunID int64; Scope domain.Scope; Since time.Time; Person, Me string; Titles bool }
type HighlightsRequest struct { RunID int64; Scope domain.Scope; Since time.Time; Titles bool }
type DiffRequest struct { RunID int64; Repo, Change, Path string; MaxBytes int; Earlier []string } // Change: a landing SHA or 7+ hex digits;
	// Earlier: the rest of its change's landings, newest first (metrics.Change.Landings[1:], a rebase merge's), each the first parent
	// of the one before, or a *UsageError: the diff then runs from the oldest's first parent, the whole change (label's d)
// ChangesAnswer, HighlightsAnswer and DiffAnswer, and their Change, ChangePerson, Contribution, TopTier,
// HighlightChange and DiffFile, carry their JSON names: what no report shows, in the shape docs/spec/mcp.md gives.
// TopTier: Of eligible people, PeopleTotal whose 95% rank interval reaches first place, People them by name unless
// they are more than a third of Of and do not stand apart (no one stands out: a no-standout caveat). A tier stands apart
// when someone is outside it and the best place anyone outside can take lies below the worst place any of the tier
// reaches, so it is named in a table of any size (MC-16); a tier of everyone never stands apart.
func RunNames(ctx context.Context, d Deps, r NamesRequest) (*Names, error)
// app/people.go (PF-7): the one rule for a person as a reader names them, which the MCP server's resolver and the command line's explain
// and me both call: an id (identity.Pseudonym), an email, a GitHub login (Names' Logins, without case), or a display name without case, or by
// the start of each word (a name-match caveat); two of one name a *domain.UsageError that lists them (by q.ID, else their ids); a miss wraps
// domain.ErrNotFound and suggests near names
type PersonQuery struct { ID func(NamedPerson) string; Again string; Commands report.Commands }
func FindPerson(n *Names, query string, q PersonQuery) (NamedPerson, *report.Caveat, error) // wave 10: an id of an address git's mailmap now
	// rewrites to the person (RepoFacts.Aliases, kept unexported on NamedPerson) names them, with an alias-id caveat; an email is not resolved so,
	// since --me and a person's own page compare canonical addresses exactly: such an address is refused with the person's name and id to pass
// app/scopes.go (wave 10): the one finder of a repository or a group as a reader names it, which the MCP server's scope arguments and
// the command line's explain --repo, --group and --area resolve by: groups by name, repositories as MatchRepos matches them, without
// case, a repo: or group: prefix keeping one kind, one spelt exactly as asked winning among several
func FindScopes(n *Names, query string) []domain.Scope
func ScopeName(n *Names, sc domain.Scope) string // the group's name, the repository's, or "" for the whole run
// app/repos.go (wave 10): the one rule both inbound adapters match a repository argument by (who-knows, paths and explain --repo,
// the MCP server's repo and scope, the last two through FindScopes): a key or a scrubbed remote, else a name without case, else GROUP/NAME, else a remote URL in any form, HOST/OWNER/NAME or OWNER/NAME of
// its origin (domain.RemoteRepo), a trailing slash ignored, else (wave 11, KT-2) those forms of a name the run's GitHub data gives it
// (NamedRepo.Forge: its forge repository's own name and aliases); none or several for the caller to refuse
func MatchRepos(repos []NamedRepo, query string) []NamedRepo
func StoredRuns(ctx context.Context, d Deps) ([]StoredRun, error) // MC-17: every stored scan run by its scopes, newest first (FactStore.Runs): which
                                                                  // run holds a scope the served run lacks; reads no facts
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
                                                                                   // a line on in that is no JSON-RPC message gets JSON-RPC's error and is skipped;
                                                                                   // #58: when in closes, every call read before is answered first

// cli
type Build struct{ Version, Commit, Date, Committed string } // from -ldflags -X main.version, main.commit, main.date; RB-13: what -ldflags leaves
	// empty is read from the build information: vcs.revision and vcs.time (Committed, the commit's time, never the build's), or, for go install
	// of a commit, the pseudo-version's SHA prefix and time
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
func Formatting(t testing.TB) string                            // builds <t.TempDir()>/formatting (wave 11, KT-10): 20,000 prod lines, no .git-blame-ignore-revs; a rewrite a
                                                                // refactor mostly replaced, an import, a bot's formatting pass over a little of every module, a one-file feature,
                                                                // and a person's small edit of every module
func BuildFormatting(ctx context.Context, dir string) error     // the formatting-pass guard in dir, which must be empty or absent
// discovery.go (#4): the discovery and clone-health repositories, as structs of absolute paths
const DiscoveryToken = "TOKENVALUE"                             // planted in remote URLs; TestDiscover asserts no output holds it
const DiscoveryWidgetRemote = "https://user:" + DiscoveryToken + "@github.com/example/widget.git"
type DiscoveryRepos struct{ Root, Upstream, WidgetA, WidgetB, Other, Mirror /* ... one field per path */ string }
type CloneHealthRepos struct{ Upstream, Full, Shallow, Partial, Extension, OtherPromisor, NotPromisor string }
func Discovery(t testing.TB) *DiscoveryRepos                    // builds <t.TempDir()>/discovery
func BuildDiscovery(ctx context.Context, dir string) (*DiscoveryRepos, error)
func CloneHealth(t testing.TB, promisor string) *CloneHealthRepos // the partial clone's origin is promisor
func BuildCloneHealth(ctx context.Context, dir, promisor string) (*CloneHealthRepos, error)
// #74: src cloned into <t.TempDir()>/<name> through file:// with options ("--depth", "18"; "--filter=blob:none", which src's
// config then allows), origin set to remote, which a partial clone keeps as its promisor: TestChangeOracles' shallow and partial clones
func Clone(t testing.TB, src, name, remote string, options ...string) string
// mergemethods.go (#38's review): how GitHub's merge methods land pull requests, for who_knows@1's reviewers
func MergeMethods(t testing.TB) string                          // builds <t.TempDir()>/merge-methods: a rebase merge's two commits in one committer second, and two squashes in another
func BuildMergeMethods(ctx context.Context, dir string) error   // the merge-methods guard in dir, which must be empty or absent
// mergeback.go (wave 10): a maintenance branch merged back, for copies and pull requests merged into another branch
func MergeBack(t testing.TB) string                             // builds <t.TempDir()>/merge-back: stable carries cherry-picks of two of main's fixes,
                                                                // a push of its own and three pull requests' squashes, merged into main three times;
                                                                // then main and a branch again add back files main removed, which are no copies
func BuildMergeBack(ctx context.Context, dir string) error      // the merge-back guard in dir, which must be empty or absent
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
  - **Readers.** `report`, `explain`, `who-knows` and `me` (the read use cases' shared `load`) read the watermarks back with `RunWatermarks`, with the names and the join the run recorded, and show in every section's provenance when each repository's GitHub data was synced (`Synced`; [ADR 0004](../adr/0004-github-behind-sync-boundary.md)); and they read the run's GitHub data back with `RunForge` into the stored run's `FactSet.Forge`, so what they compute from it, such as `who_knows@1`'s recent reviewers for any path, comes from the run's own snapshot, as its git facts do. They never read the replica, which a sync renames and replaces, so a later sync, a rename or a new review included, moves none of a stored run's answers or "as of" times. What they compute counts by the join the run recorded (`ForgeFacts.Join`), never one made again. A run recorded before schema version 3 kept no join: its readers report no GitHub data for it rather than guess. One recorded before version 4 kept its watermarks and no pull requests: its pages date the GitHub numbers it stored by those watermarks, and `who-knows`, which computes reviewers from the pull requests when the run is read, says nothing about GitHub for it rather than list no reviewers as if there were none; the log says to scan again. One stored before #69 kept a GitHub login's case in its keys (`@Zoe`), where its readers fold what they are handed and what they compute from its pull requests (`@zoe`): `load` upcasts its values and ranks as keys are made now (`identity.CanonicalKey`), in a copy, so its keys join theirs again without a migration, unless it holds one login in two cases, an account it split and ranked as two, which stays as stored while the log says to scan again. `identities` reads the run's GitHub data the same way (`app.withForge`, which `load` reads it through too), since GitHub's commit-to-account links are its `same-forge-account` rule's evidence (#103). A section that shows a forge metric's numbers gets a `forge-not-synced` caveat when its collected repositories have no GitHub data, or only part ([report-json.md](report-json.md#caveats)); in a run that read none at all, only a section about that metric alone (its rank table, `explain` of it) does, so a git-only report says nothing about GitHub.
  - **Ranks are stored too.** The stored metric values are the run's forge numbers, and so are its ranks: a scan ranks every ranked metric in every rank scope and stores the intervals, whichever ones `--rank` names (it picks the tables the scan's report prints). Readers use the stored ranks, and bootstrap only a table the run did not store, such as a newer metric version's (`app.rankAll`): over the run's own facts and GitHub data, seeded from the metric, scope and window (`rank.Seed`), so it is the table a scan by the same binary would have stored. Storing the ranks beats both other ways a reader could answer: ranking over today's replica would put, in one table, ranks from one state of the replica beside values (and bot rows) from another, and refusing would make a user scan again, blame and all, for a rank the scan stores in well under a second ([ADR 0010](../adr/0010-rank-intervals-bootstrap-per-kind.md#consequences): 250 ms for a ratio and 65 ms for a sum over 500 people). A run that keeps its pull requests could rank its GitHub data again from them, alike; the stored ranks also serve runs from before it did.
  - **A newer metric version** computed over a stored run's facts gets that run's own GitHub data, as it gets its git facts and policy.
  - A sync is recorded as a run of kind `sync` (no policy, no facts of its own), so its status is kept like a scan's.
- **Metric values** are stored with the run, keyed by `metric@version`. A new metric version can be computed over an old run's facts with `LoadFacts`, which is why `LoadFacts(PutRepoFacts(x)) == x` is tested. The read side does so (`app.load`, `computeUnstored`): a metric a scan stores (its definition declares `Subjects`) of which the run holds no value, such as `landed@2` and `review_coverage@1` in a run an older stillhere stored, is computed from the run's own facts, policy and GitHub data as it is read, like a rank table the run did not store, so an old run's report shows what landed rather than "nothing", and the log says which metrics it computed. The same holds for a metric corrected in place before the first release (wave 10, `metrics.Corrected`, the one list): a run whose values of it lack the `Detail` key the corrected definition always writes (`review_coverage@1` without `via_other_branch`, counted by landing; `surviving@1`'s person values without `merged_365d`; `knowledge_loss@1` without `going_quiet`, stored under the activity rule before wave 10, with `knowledge_concentration@1`) was stored by the old definition, so its readers drop those values and compute the metric again, and a page never prints a stored value beside what it computes per query by the corrected rule (`explain`'s list of changes, each person's last activity, `metrics.PathKnowledge`). The old run is told by its data, not by a version. A metric that reads GitHub data is not computed for a run that read none, nor computed again for a run that read GitHub data but kept none (stored before schema version 4), whose old value then stands. Where the corrected definition counts facts such a run never kept (the commits of other refs), the log says to scan again.

## Command surface

Global flags: `--db` (default `$STILLHERE_HOME/facts.db`, where `STILLHERE_HOME` defaults to `~/.stillhere`), `--policy FILE`, `--mailmap FILE`, `--log-level`, `--format text|md|json` on every command that prints a report. The root command alone also takes `--version` (`-v`), which prints the first line of `version` (#71).

`--db`, `sync github --mirrors` and `STILLHERE_HOME` take a local path. One that is a URL or holds a credential (`redact.Scrub` would change it) is a usage error, quoted scrubbed, before anything is created or recorded: it is a remote pasted into the wrong place, and as a path it would name a directory after its token, which a scan of that directory would store and print (#71). A relative `STILLHERE_HOME` is a usage error too (RB-12): it would name another facts database in every directory a command runs in, as the XDG Base Directory specification rules for its own variables. `scan` and `sync` log the facts database they write, as an absolute path.

The organisation mailmap, `--mailmap` or `./.mailmap`, is git's to read; the command line reads it once more, by git's `read_mailmap_line` rules, only to warn, with line numbers, about each line git will skip (no `<email>`, an unclosed `<`, an empty `<>`) or cut (text after the first `<email>` that holds no closed second one, which git drops, so the line renames the first address alone), about a line that looks like a comment but is not (only a `#` in the first column makes one, so git reads `  # Old Name <ann@example.com>` as a mapping that renames ann to `# Old Name`), and about a file that is not text (RB-17, `cli/mailmap.go`). It never fails a command.

The policy is `--policy FILE`, else `./stillhere.toml`, else the packaged one, and the organisation mailmap `--mailmap FILE`, else `./.mailmap` (#71). The working directory is often a clone's root, so a file there may be the clone's: it must be a regular file, not a symbolic link (a link to `/dev/zero` would be read without end, one to a FIFO would hang), and the log says at info which one was used; a file a flag names may be a link. A policy file is read only up to 1 MiB.

| Command | Reads | Writes | Milestone |
| --- | --- | --- | --- |
| `week PATH...` `[--now] [--days N] [--week-sort lines_added\|commits] [--exclude-repo GLOB]... [--ignore-revs-file F] [--all]` | git | nothing; `--ignore-revs-file` is accepted for symmetry with scan and ignored, with a warning: git log has no such option | M0 |
| `scan PATH...` `[--now] [--jobs N] [--ignore-revs-file F] [--rank METRIC]... [--scope group\|all] [--exclude-repo GLOB]... [--all]` | git, the forge replica in facts.db | a run in facts.db, which it creates when missing and must be able to write: an unwritable file or directory is a usage error before any git work (#71) | M1 |
| `report [--run ID] [--rank METRIC]... [--all]` | facts.db | nothing, but an older facts.db is migrated (#71) | M1 |
| `explain METRIC (--person WHO \| --repo NAME \| --group NAME \| --area REPO:DIR \| --all-repos) [--days N] [--run ID] [--all]` | facts.db | nothing, but an older facts.db is migrated; WHO is an email, `@login`, id or display name (PF-7); NAME as the report prints it (wave 10, `app.FindScopes`); `--all-repos` the whole run's own numbers; `--days` sets `landed@1`'s and `landed@2`'s window, at most the run's history | M1 |
| `who-knows PATH [--repo NAME] [--days N] [--run ID] [--all]` | facts.db | nothing, but an older facts.db is migrated; NAME as a card heads the repository, GROUP/NAME, OWNER/NAME, a remote URL or the key (`app.MatchRepos`, wave 10) | M1 |
| `paths [PATH...] [--repo NAME] [--role ROLE] [--skipped] [--run ID]` | facts.db: the run's path classes and its own policy, no metric | nothing, but an older facts.db is migrated; every listed path with the rule that decided its class, found again from the run's policy by `classify.Explain` ([classification.md](classification.md#which-rule-decided-a-path-stillhere-paths)) | M2 |
| `me --email WHO [--run ID]` (`--person` the same) | facts.db | nothing, but an older facts.db is migrated; WHO as for explain (PF-7) | M2 |
| `metrics` | the registry | nothing | M1 |
| `identities [--run ID] [--format mailmap\|text\|md\|json] [--all]` | facts.db | nothing, but an older facts.db is migrated; text and Markdown list hand-off guesses only with `--all` (wave 10) | M2 (#26) |
| `label [--n N] [--seed S] [--labels FILE] [--labeller NAME] [--sheet \| --plan] [--run ID]` | facts.db; git for a change's diff | the labels file it appends each answer to (`labels.csv` beside facts.db unless `--labels`, 0600, opened before the first card, at most 16 MiB read; a warning when it is inside a git work tree); `--sheet` and `--plan` print the sample and write nothing; a run with no change to label exits 1 with the caveats that say why | M2 (#77) |
| `validate [--labels FILE] [--labeller NAME] [--scores] [--run ID]` | facts.db, a labels CSV (`labels.csv` beside facts.db unless `--labels`, at most 16 MiB) | nothing, but an older facts.db is migrated; `--scores` prints each labelled change's scores as a CSV on stdout, the answer on stderr (in the JSON with `--format json`; refused with `md`) | M2 |
| `sync github (--org ORG \| --repo OWNER/NAME...) [--mirrors DIR] [--since DATE] [--include-archived] [--include-forks]` | GitHub | mirrors with their commit-graphs, forge facts; facts.db as for scan, and `--org` and each `--repo` checked with GitHub's name rule before the run begins (#71, #111); one closing summary line on stdout | M2 |
| `mcp [--run ID] [--me EMAIL] [--allow-source] [--allow-emails]` | facts.db, read-only; git for `diff` with `--allow-source` | nothing | M4 |
| `policy` | `--policy`, `./stillhere.toml` or the packaged policy | nothing: it prints the policy week and scan would use as a complete file, the packaged deny list written out (`stillhere policy > stillhere.toml`; an empty `./stillhere.toml`, which that redirection makes before stillhere starts, counts as absent) (#71) | M2 (#71) |
| `version` | | | M0 |
| `completion bash\|zsh\|fish\|powershell` | | nothing: it prints the shell's completion script (cobra's own command; the release archives carry the bash, zsh and fish ones) | M0 |

The read commands (`report` to `validate`, and `label`) never create facts.db or its directory (`sqlite.OpenExisting`): a missing one is a usage error that names the path and says what to run, where a new, empty database would send the user to scan again after a mistyped `--db`. The one write they make is migrating a facts.db an older stillhere wrote, which they could not read otherwise. In a directory they cannot write they read it read-only, as the MCP server does (#79). A missing PATH of `week` or `scan` is said in stillhere's words, not cobra's (RB-19). `--all` on `report`, `scan`, `week`, `explain` and `who-knows` prints every row of every table in text and Markdown, which otherwise print the first 10 of each and count the rest (SR-14); on `identities` it lists the hand-off guesses too.

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

- `golang.org/x/sync/errgroup` with `SetLimit` for fan-out, and one `collect.BlameSlots` (a `golang.org/x/sync` semaphore) shared across repos, so the number of git processes stays bounded on large machines. A repository's history is collected one git process at a time but for one pipe (wave 10): the own patch streams into git's patch id command, two processes as a shell pipeline runs them, through an `io.Pipe` both ends of which an errgroup waits for, so the patches are never held whole; the reader closes its end when it stops, so the writer never blocks on a reader that is gone.
- Every goroutine is waited for; every I/O call takes the caller's `context.Context`; `cmd/stillhere` cancels it on SIGINT, SIGTERM and SIGHUP (unless SIGHUP is ignored, as under `nohup`), then ends with that signal.
- Large git outputs are streamed (`GitRunner.Stream`), never buffered whole. Line readers allow long lines (generated files have megabyte lines).

### Determinism

- Time comes from `--now` (default: the wall clock in UTC, read once in `cli`, which also stamps a sync), and every number reads it alone: the windows, the commit each repository is read at, the metrics, the ranks and the as-of every page prints. The clock is read elsewhere only for what no number depends on: `timings` and log durations (`app`, `gitcli`, `mcp`), a run's start and finish times (`app` and `sqlite`, which a report shows only in `timings`), `sqlite`'s leases and lock waits, `github`'s rate pacing and its clean-up of what a crashed clone left, and `gitcli`'s timeouts. A sync's own times are the wall clock's too (sync takes no `--now`): its start bounds the window a full pass reads, and a pass's watermark is GitHub's clock, the `Date` of its responses (ADR 0013).
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
- **Files that belong to one issue.** #14: `domain/forge.go`, `internal/github`, `sqlite/forge.go`, `sqlite/migrations/0002_forge.sql`, `cli/forge.go`, `cli/sync.go`, `app/sync.go`, `metrics/reviews.go` (registered with one line in `metrics.All`). #15: `app/mcp.go`, `cli/mcp.go`, `internal/mcp`, `sqlite/readonly.go`, `docs/spec/mcp.md`, `plugins/stillhere`. #26: `identity/candidates.go`, `app/identities.go`, `report/identities.go`, `cli/identities.go`. #38: `identity/reviewer.go`, `sqlite/forgesnapshot.go`, `sqlite/migrations/0004_forge_snapshot.sql`, `app/reviews_oracle_test.go`, `fixture/mergemethods.go`. #56: `collect/commitgraph.go`, `app/commitgraph.go`. #101: `sqlite/migrations/0006_forge_completeness.sql`, `sqlite/migrations/0007_forge_links.sql`, `github/links.go`. #5: everything in `policy` but `types.go`, which is this file's contract. #77 (wave 9's validation lane): `metrics/changes.go`, `app/label.go`, `report/labels.go`, `cli/label.go`, `docs/validation.md`. Where such a file shares a package with another issue's, its unexported identifiers carry a prefix, as in `collect`: `forge…` and `snapshot…` in `sqlite`, `reviews…` in `metrics`, `mcp…`, `aiShare…` and `label…` in `app`, `review…` in `app`'s tests and `readOnly…` in `sqlite`.
- **A stub another branch calls.** When one branch calls a use case another builds in parallel, the signature lands here first, as a stub that returns an error naming its issue, and the caller's test skips that one call until both merge. The MCP server's `identity_candidates()` (#15) was built this way against `app.Identities` (#26), and `report.Identities` was a `Document` from the start, so the server serves whatever #26 put in it (Consumer-Driven Contracts: Ian Robinson, "Consumer-Driven Contracts: A Service Evolution Pattern", 2006). The walking skeleton's last stub went with it, and so did `domain.ErrNotImplemented`.
- **Shared documents.** Each issue edits only the sections its scope names: in [metrics.md](../metrics.md), the entries of the metrics it builds or derives oracles for; in this file, the signatures it changes; in [report-json.md](report-json.md), the shape (#11). Where two branches still touch the same lines, #13 resolves them when it merges, as it does `go.mod` and `go.sum`.

## Tests

Tests sit at stable boundaries, so a refactor does not rewrite them (Kent Beck, "Test Desiderata": tests should be structure-insensitive). They run real git; no test mocks git. The fixture oracles are the contract. A metric without an oracle does not ship, and no test exists outside this list without a reason in its pull request.

| Test | Package | Asserts |
| --- | --- | --- |
| `TestWeekOracle` | `app` | the week table in the fixture spec, `landed@2`'s since #108, people and bots apart, with its total, and the `--days 1` window against `[week] days = 7`: `window.days` 1, `days_from` `cli`, `since` `2026-10-06T00:00:00Z`, and `history`, the window the run collected, equal to it |
| `TestStillHereOracle` | `app` | the still-here table, people and bots apart, and the concentration walk |
| `TestChangeOracles` | `app` | `durable@1` and `simplifying@1` per person and per landing, with their rank tables and eligibility, each table's validity note in its JSON and the report's text saying the ranked metrics' validity once, at its top, reverts and AI-assisted tags on both fixtures; `knowledge_loss@1` (and `explain` of it about a person refused with what answers instead, #69) and `who_knows@1` on the main fixture, where who-knows of a path no repository has is missing with a `no-values` caveat that states what the run holds, never "nobody" or one reader's advice (#71), and with a replica written by hand their reviews (#38): a reviewer who keeps an area's knowledge from counting as lost, with a comment on her own open pull request, recent reviewers of a path (one known only as `@login`, whose login two pull requests carry in two cases, one subject `@zoe` named `@Zoe` (#69); not the author, a bot, a review outside each window, or a squash read as a rebase), on the merge-methods guard a rebase merge's commits in one committer second, which `label` shows whole (both subjects, oldest first, and `Diff` with `Earlier` from R1's parent, refusing a landing that is not the one before's first parent), and a squash that takes no other pull request's landing in its second, and #2's author, whose own commit's address only S1's `Co-authored-by` trailer shows, named by the address of the commit the run holds, Ali Squash (wave 11, Ruby F11), `explain` by that trailer address's id refused with its name and id and no email (KT-6), and the card saying it read GitHub's default branch (org F10), a stored run that a later sync leaves unchanged, an open pull request of Hank's opened before the window and updated in it by someone else, which leaves him quiet (wave 10), and one whose keys kept a login's case (`@Zoe`, as a stillhere before #69 stored it) that `explain` reads as `@zoe`; `validate` over that replica (#77), whose labels name landings by SHA and prefix and pull requests as `#3`, `pr:4` and `acme/fixture#6`, scoring `reviews_given@1` by its pull request's reviewers at any time (1 and 3, frank's review before the window counted, against 1: AUC 0.75), with `--scores` one row per labelled change, noise included (row 11's `simplifying@1` 0), each metric's measure or null, from which each AUC and its size-alone column recompute, size alone as derived by hand (`durable@1` 0.5, `simplifying@1` 5/6, `reviews_given@1` 1), a paired interval of each AUC less size alone that holds the difference, and each verdict "cannot tell yet", and docs/metrics.md's Validity line of each ranked metric holding its definition's model-panel reference word for word, as report-json.md's example of a rank table's caveat holds `durable@1`'s note, the two direct pushes and `#1`, merged before its window, unscored, with an `unscored-labels` caveat, `durable@1` by landing (0.75) and `simplifying@1` (0.5), with a `#9` unmatched and a caveat that says how a change is named, row 8 named twice counted once (`repeated-labels`), a second labeller's agreement on `#3` and tie on `#6` (left out) pooled, and `--labeller` keeping one labeller's rows, and a file of `#` lines that are not pull requests refused with their count (KB-19); and `label` over it, whose pool is the 14 people's changes a ranked metric scores (not the bot's), its windows taking turns, a larger sample extending a smaller one, a labelled pull request marked, and another labeller's rows not resumed; `surviving@1` credit on the sibling, where an agent's blamed lines go to its human co-author; and the regression guards: a backdated first-parent commit hides no landing below it; a rebased one lands, and is dated, by its committer time, and is credited to its author; and a revert merged with `--no-ff` is linked to the merge landing, which neither change metric counts, while `surviving@1` keeps the lines the revert restored; and a change git takes for binary leaves both of `durable@1`'s sums, and `ai_assisted@1`'s cohort ratio, with the lines blamed to its commit from its path, through a rename the history never collected and from a path with no role, while its commit's other changes and the file's text commits, before and after its binary ones, keep theirs; and both sums classify a line by the path it had in its commit (#70), so text files moved onto `prod` paths from `vendor/`, `tests/` and a name with no role keep no line their commits did not add, a `prod` file moved into `tests/` keeps its lines and one moved under `vendor/`, which blame does not read, keeps none; a run reads each repository as it stood at `--now` (#70): the main fixture at `--now 2026-03-15` reads its newest landing before then, records it as its head with a `head-after-now` caveat, gives `knowledge_loss@1` 0 / 18, and renders as the same repository cut back to that commit does, while at a `--now` before its root commit it yields no facts and a `no-commit-before-now` warning, and the backdated guard at `--now 2026-07-01` reads its backdated commit and gives a `dated-after-now` caveat for the 1 landing below it dated after `--now`; and the identity candidates of each fixture, without its mailmap and with it, whose mailmap form changes nothing in `git check-mailmap` until its lines are uncommented; with a replica whose one pull request, older than the window, GitHub attributes to one account with both of Ada's addresses, her candidate high and its line uncommented, `MailmapText` (#103), and Gina → Hank counted as the answer's one guess (`guesses`, wave 10), and the card's `knowledge_loss@1` 11 / 34 with `if_merged` 4 / 34 for the high candidate and for every one, its concentration 3 authors with 2 merged, and the card and `explain` of it about the repository carrying the unmerged-identities caveat with that range (#104); and with a mailmap that folds Jules Note <jules@> into ada@other.example by name and email beside `MailmapText`, Ada's one line mapping Jules Note <jules@> to ada@example.com in the same form and none for ada@other.example, which `git check-mailmap` then maps as meant over that line and over an email-only one, where a line of the email alone would not (#53); and two repositories written with `git fast-import` and a replica (SPEC's two-repository case): a line one repository's own `.mailmap` applies still written for the other, which `git check-mailmap` there maps as meant, a scoped answer listing the run's candidates with an address in the scope, whole, a medium candidate whose GitHub-linked addresses, a co-author's noreply address by its login among them, keep their lines as lines while a name-only address's is commented out, a name that is the linked login leaving an address unshared, a mailbox two people commit as linked high by GitHub's link, its names said to differ and listed, Alice Ng and Bob Roe, and its login withheld as a local part, which the link says and the mailmap form prints (wave 10), two account ids of one login linked by nothing, the run keeping one link for each address and account, and the card where merging moves the numbers carrying the caveat with its range while the one where it moves nothing carries `if_merged` alone; `landed@2` on the main fixture, per person with `commits` and `merged` and per scope, the repository's and the run's totals, and `landed@1`'s M0 table there (#108); `explain`'s traces (#109) of `landed@2`, `landed@1`, `surviving@1` and `majority_owner@1`, with the person named by a display name, the start of a word, an email and an id (PF-7), a value of the right kind the person has none of saying why (#92), a misspelt name suggesting the right one, and the id of Ada's mailmapped second address naming her with an `alias-id` caveat (wave 10), while the address itself is refused naming her; who-knows of `notes.csv`, present with no role, empty with a `no-values` caveat that says so (PF-8), of `vendor/new.py` and `gen_out.py`, denied and generated, whose caveats say what would make each count (a role does nothing for a denied path) and name the deny pattern and its line in `generated_paths.txt` (`classify.Explain`) and `stillhere paths PATH`, of `tests`, which holds no `prod` line and is answered for its `test` lines with an `other-roles` caveat that names `stillhere paths PATH` for the rule behind each role, of the root, whose 6 `test` lines it leaves out and says so, by `--repo` the repository's name, and refused for a name the run lacks with the names it holds (wave 10), and of `app.py`, a real "nobody"; `paths` of the main fixture (revision 3), every classified path with its rule from the run's policy (`service.py` by its extension, the test file by the test-directory rule, `notes.csv` by none, `app.py` only in the history), a legend that counts every listed path, `--skipped`, `--role` under a prefix, `--repo` by the repository's name (`app.MatchRepos`, as who-knows), the packaged revision in the provenance, a `no-values` caveat for a PATH no repository holds (an absolute path, a missing one) and none for one it holds, and a role that is none, a role with `--skipped` and a repository the run lacks refused as usage errors; `metrics.PathKnowledge` of the main fixture's root, which equals its stored repository values, of two files, and of a prefix that is no whole segment, and who-knows of `split.py` showing its knowledge loss and concentration (#105); `review_coverage@1` over the replica written by hand, with a seventh pull request reviewed only by its author, a bot and someone after its merge, and on the merge-methods guard, counting #1's two rebased landings as one change (wave 10), as stored and as the card and the overview show it (#107), where a pull request GitHub's Copilot coding agent opened for Rob is Rob's landing, not a bot's, and his own approval of it counts for nothing, a replica that lacks a pull request a squash's subject names leaves that landing out as `unmatched`, not pushed directly, and a sync older than three landings leaves them out as `after_sync`; the binary-file guard's `binary-changes` caveat, counting its 4 changes to `prod` files (#64); and missing facts (#74): a partial clone of the main fixture, alone and beside it, whose `landed@1` and `landed@2` have no lines and count its landings in `landings_lines_unknown`, and a shallow clone of the sibling whose boundary is older than `simplifying@1`'s window, which has the sibling's `simplifying@1` there and no `ai_assisted@1` value, and a week in which nothing landed, a total of 0 that pages print as nothing; and a shallow clone of the main fixture whose boundary is inside the week, whose `landed@2` has no value and whose week is `null`, which text and Markdown print as unknown, never nothing; and the merge-back guard (wave 10): copies of main's fixes that a maintenance branch brings back, credited once by `landed@2` over 365 days (`explain --days 365`, `copies` 2) and by `surviving@1`'s `landings_365d` (merges apart, `merged_365d`), and no cluster of `durable@1` or `simplifying@1`; and `review_coverage@1` counting changes: pull requests merged into `stable` changes of their own, a merge that brought only one left out (`branch_merges`), a cherry-pick naming a pull request merged into `stable` left out as `named_elsewhere`, and `explain` of it about the repository listing every change with its outcome, the unreviewed first, in no rendering "pushed directly", and in text each outcome whole as a code its legend explains; and two changes made again on top of their twins, a direct push and a commit a merge brought, which are no copies (git cherry's rule: neither the first draft's patch-id-only rule nor one sparing only first-parent commits passes), and `explain landed@2 --days 400` refused, the run's history covering 365 days; and the main fixture with a branch of Gina's, one of Hank's under an address the history never shows, a tag dated after `--now`, and a replica whose one pull request Hank opened before the window and pushed that commit to in it, linked to him only by the links pass's answer for his history address (#117): `knowledge_loss@1` 0 / 34, Gina active through her commit on another branch and going quiet in 10 days (`going_quiet` 1 person and 2 lines, its `holders` naming her, and the card's `going-quiet` caveat), the text report printing `quiet in 10d` and her line under "going quiet within 30 days" whole, Hank's last activity his pull request, 163 days from the line, `--repo` as a URL without its scheme or with a trailing slash, and the run keeping the two commits made before `--now` (wave 10, SPEC's "Activity off the default branch"); wave 11: `who_knows@1` over the window a query names (`split.py`'s January landers in 300 days, the answer's `days`, and a window past the run's history refused, SPEC's "who_knows@1"), and the formatting-pass guard (`fixture.Formatting`): the card's no-ignore-revs caveat names the bot's pass alone, not the rewrite that is the oldest owner of its files, the import, the one-file feature or the small wide edit, with the route that works in a clone (`--ignore-revs-file`), and who-knows of `pkg/` says the card's caveat, of the whole repository |
| `TestDeterminism` | `app` | two scans of both fixtures as one group, into separate facts.db files, give byte-identical JSON without `timings`, and identical text and Markdown; `report` on the stored run reproduces the scan, and so does a run that holds no `landed@2` or `review_coverage@1` value, as an older stillhere stored it, whose readers compute them from its facts, and one whose `review_coverage@1` and `surviving@1` values lack `via_other_branch` and `merged_365d`, as a stillhere before wave 10's corrections stored them, whose readers compute both again, and one whose `knowledge_loss@1` values lack `going_quiet`, every line counted gone, and whose concentration is one author off, as a stillhere before wave 10 could store them, whose readers compute both knowledge metrics again (`metrics.Corrected`); `me` prints Gina's own AI-assisted share of 0 of 1 as `0.0% (0 of 1)`, by its metric's declaration, as `explain` prints a share (#66); a token planted in a URL given as a PATH, in its userinfo, query or fragment, which discovery refuses as holding a credential with one path-failed warning that quotes it scrubbed (#93), is in no byte of facts.db or its `-wal` and `-shm`, no rendering and no log line, nor one pasted into `sync github`'s `--org` or `--mirrors`, or into `--db`, each of which the command line refuses before a run begins with an error line that holds none and gives its reason, nor into `--org` and `--mirrors` of a sync run that found no repository to sync, which records its options scrubbed, nor into the error of one whose organization GitHub refused, which records no run, nor given to `who-knows` as its PATH (#71); with the cli's Deps (the store as both ports) and a replica that holds the fixture's forge repository beside another organization's, the run keeps only its own repository's watermark, with its name and join, `reviews_given@1` counts its reviews in scope `all`, the group and the repository, a review 7 days before `Now` in week block 0 and one at the window's start in block 12, every value the run stores is about a kind of subject its metric declares (`metrics.Definition.Subjects`, which `explain` refuses every other kind by, #69), and `report` reproduces the scan after a later sync renamed the repository; and a repository written with `git fast-import` (#52), whose display names are a login, the same name on another address, an address, none twice, a label's shape, two bots', one name written two ways on two addresses and a default name on two (#69), gives the same bytes over two scans and `report`, labels included, an address that changed its name from a handle to a full name named by the full name and one a later trailer spells otherwise by its own (wave 11), and (wave 11's review) one named by its full name over its own local part on more and newer commits, one by its full name over a later first name alone, and a team mailbox by its most-seen name over a later commit's other person's, and `explain` of `landed@2` about a person whose one landing, older than the stored week, adds no line refused by her name and id with what a run stores of a person, and answered with that landing over 365 days, the report's and who-knows' `unmerged-identities` caveat naming the name written two ways once, counting jdoe's and leaving the default out, and a name a person and a team's info mailbox share counted apart, as identities suggests no line for it (wave 11, TS F10), while `week` of the same repository says only that its own commits link the name by no rule (wave 11's review), who-knows of the root saying in it what the card says of the identity candidates holding lines there (#104), no text or Markdown page (who-knows included) holding a local part a name would give away or a domain, two `(no name N)` labels and four `(name withheld N)`, the label-shaped name printed quoted, and JSON names withheld on both of jdoe's addresses and for the bot named by an address, not for `dependabot[bot]`; and a commit subject that names an address no one in the run has, jdoe's and, in a dotless domain, root's, who is on no page with it, which `explain` prints in text, Markdown and JSON with each address as `(address withheld)` (#68, #90); and the `reviews_given@1` rank table's `no_comment_share` beside each count, zed's 1 (PF-5) |
| `TestDiscover` | `collect` | duplicates, bare repos, scp vs HTTPS remotes; a directory with no repository among its children is no group but a failure that says what to pass: two subdirectories that hold repositories, named before the work tree it is inside, and a symbolic link to a FIFO there is never opened (#71); a planted token appears in no output, and a repository named, or found in a group, under a directory whose name holds one fails with its path scrubbed, as holding a credential (#93) |
| `TestCloneHealth` | `collect` | shallow and partial clones warn and lose only the facts they should; and a full clone's other refs (wave 10's review): a ref to an object the clone lacks is skipped, the side branch's commit still read, and a branch naming a parent the clone lacks costs the other refs' commits alone, with an `other-refs-unread` warning that names no ref, Head's facts unchanged |
| `TestAttributes` | `collect` | `check-attr --source` on a work tree and a bare clone: the scanned commit's attributes, not the checked-out ones or `attr.tree`'s, matched case-sensitively under `core.ignorecase=true`; an empty value is on; an attribute that settles a generated header; a deleted path classified by Head's attributes; a header sniff that no attribute changes (`-diff`, `binary`, an uncommitted edit), with each of its rules, prose, a generator program and a generator script's string template that writes an rST comment (#70) that stay `prod`, a Haskell module whose generated header opens a `{-` comment, as proto-lens writes it, and the line-40 limit; a file only a path rule gives a role sniffed by name (pip-compile's `requirements.txt` generated, a hand-written `requirements/dev.in` `iac`); a symbolic link and a submodule unclassified; `InlineTests` over the prod paths at Head, the same in both clones: a test module whose braces in a comment, a nested block comment, a string, a raw string and a character literal count for nothing, a `use`, a file module and a function under the attribute, the attribute in a comment, in a string and as `cfg(testing)`, which gate nothing, an inner `#![cfg(test)]` that makes a file a test, and a denied and a test file that hold it, not read (3 files, 28 lines). History (scan) and Blame on the same pair, under the repository config that each pin of [git-invocation.md](git-invocation.md#repository-settings-that-change-facts) overrides: the same facts in both clones, and the expected ones, with no file binary although Head, the checkout and `attr.tree` each mark files `-diff`, a CRLF-to-LF rename followed, the indent heuristic's owner, a UTF-8 author name, a co-author, a root commit's diff, a gitlink's row and a decomposed path blamed; and no textconv driver or filter run |
| `TestNumstatRename` | `collect` | the rename path forms, and a committed line of 64 MiB and more, which the own patch counts like any other (#73) |
| `TestBlamePorcelain` | `collect` | `--line-porcelain` and `--porcelain` parse to the same counts and the same path in each commit (`OrigPath`), a commit that blame reached through two paths included |
| `TestClassify` | `classify` | roles, role precedence, test directories for code and configuration only, `too_big`, segment matching, the deny list's minimum contents and what it leaves out, Linguist's attribute values, no role for a link or a submodule; revision 3's rules on the paths the census checked (examples at any depth and samples at the top, benchmarks, `docs/` but not `doc/`, the Go packages that keep their role, CI, build tooling and configuration as code, a top-level `setup.py` and not a `setup` module, `build.rs` outside `src/`, requirements, Jinja), a rule's `max_bytes` (httpx's 208,844-byte `whatwg.json` too big, smaller data, data of unknown size and large test code test), a directory rule that denies no file of its name (`scripts/coverage`); revision 3's review: Linguist's documentation and benchmark directories with a capital first letter (`Examples/`, `Samples/`, `Demo/`, `Docs/`, `Benchmarks/`, a Go package under `Docs/` still `prod`), `fabfile.py` `iac` past the deny list, test data at and over the 100 KB bound, a header on a path no rule gives a role unclassified and on `requirements.txt` generated, and `RoleByRuleAlone`; and `Explain`'s rule for every step: kind, text, where, source and the policy's reason, true of every path the rule decides (small test data's rule 1 too), with a path's size or an attribute's override as its `Detail`, a submodule's no size and no rule, and `unknown` for a class this classifier would not give; revision 4's rules on the paths its census checked: Gradle's test source sets (okhttp's `jvmTest`, `commonTest`, `androidHostTest`, a variant's `testDemo` and `androidTestDebug`, `functionalTest`, `testFixtures`, resources) and the main sets beside them, a feature named like a test with no language directory, Gradle build logic and a Gradle plugin's own `gradle/` package, a workspace package's JavaScript scripts and its Python ones, dot-directories, a changeset's Markdown, and a VitePress site; and `Explain`'s rule for code under a dot-directory |
| `TestPolicy` | `policy` | the packaged policy's tables, its `[ai]` list at version 2 with GitHub Copilot's three co-author addresses among them (#102), revision 3's names, Jinja extensions and path rules in their order with each rule's note from its comment and the large-test-data rule's `max_bytes`, `Packaged` true for it and false for a policy file, and a policy file's optional `max_bytes` and note (0 exits 2), a note from a comment right above a rule's header (its last paragraph under a comment on all the rules) and none from one a blank line parts from it; `PackagedRevision`: the packaged policy's hash listed under its revision (4 since wave 11), revisions 2's and 3's under theirs, a policy file 0; `fabfile.py` not denied; a `generated.rb` group's label in words; the deny list's directory rules that deny no file of their name, CI not denied, and `Denying`'s pattern, line and label in the packaged list and index in a policy file's; a missing table or key exits 2; `[week] days` is the file's, in its hash, and 1 to 36500 (the `--days` override itself is `TestWeekOracle`'s); an extension holds one dot; the hash covers the deny list, and the packaged files hold no CR; `FromSource(p.Source)` has `p.Ref()`; every deny line compiles and the file names RE2, Linguist (MIT) and the upstream commit; the deny prefilter never changes an answer; the packaged policy written out as a file (`File`, #71) parses to the same rules with every deny pattern in order, keeps Linguist's license, and a user file writes out as it was read; revision 4 (wave 11): the two test-directory rules with Gradle's test source sets, and the documentation-site, workspace-scripts, Gradle-build-logic and dot-directory rules in their order with their notes, |
| `TestRankIntervals` | `rank` | reproducible intervals; ineligible subjects unranked; a sum's week blocks checked against its window |
| `TestAUC` | `rank` | the validity check's statistic: a hand-counted example with ties, 1 and 0 for perfect separation, 0.5 when either class is empty, and the value counting every pair gives; and its interval (`AUCInterval`): [1, 1] for a perfect separation, one that holds the AUC, the same for the same labels in any order, and [0.5, 0.5] without a class; the paired interval of one AUC less another's on the same changes (`AUCDiffInterval`): [0, 0] for a score against itself, [1, 1] for a perfect separation against its reverse, one that holds the difference, the same in any order, [0, 0] without a class, and narrower than an AUC's own interval for two scores that move together; and the verdict (`Separation`), the first that holds at each boundary of docs/metrics.md's rule, with its words and numbers (no "-0.00") |
| `TestRedact` | `redact` | no credential survives `URL`, `Scrub` or `Message` (userinfo, scp-like and malformed remotes, query strings and fragments, a URL given as a path and made absolute with any of those, quotes, the 180-rune cut, and `PathMessage`'s, which keeps a path's end); text that holds none is unchanged, a Windows path with a `#` included |
| `TestRunner` | `gitcli` | per-user and system attributes change neither numstat nor `check-attr`; no askpass, signature or external diff program runs; an inherited `GIT_DIR` changes nothing; a timeout or cancellation kills git's process group and is reported as such even when the reader fails first on the truncated output; a helper that left the group cannot hold `Stream` |
| `TestStoreRoundTrip` | `sqlite` | `LoadFacts(PutRepoFacts(x)) == x`, a zero loading as +0; `Run(id)` returns the run as begun and finished, policy source included, and `Runs` the run with the groups and repositories `LoadFacts` gives, and no run of another kind (MC-17); `RunWatermarks` returns the last `PutRunWatermarks` of the run, names, joins and completeness (`From`, `Missing`, `Pending`, ADR 0013) included, and a watermark that joins nothing is refused; `RunForge` returns the last `PutRunForge` of the run with its watermarks and its join (#38), and a pull request or a link of a repository not the run's is refused, with the older pull requests' links (#103) and every mailmap alias (#53) back as put, and the commits of the other refs with their co-authors (migration 9), and a repository's aliases (migration 11); readers never see a running or failed run (`ErrNotFound`, `RunWatermarks` and `RunForge` included) |
| `TestStoreLifecycle` | `sqlite` | `Open` creates a missing directory 0700 and files 0600, in WAL mode; it refuses another program's SQLite file, a file that is not SQLite and a newer facts database with a `UsageError`, without waiting for a held lock and leaving the file byte for byte, and a file or directory it cannot write with one that names it (#71); `OpenExisting` creates no directory or file for a missing path, refuses it with a `UsageError` that names it, refuses what `Open` refuses, reads what a writer wrote without deleting a run left running, and migrates an empty file (#71); `OpenExisting` and `OpenReadOnly` read a facts database in a directory they cannot write, leaving no file there, and refuse one whose write-ahead log holds a commit with a `UsageError` that names the log, and an immutable reader refuses a read while another writer's log holds a commit and reads what it wrote once it closed (#79); first opens racing in separate processes all succeed; a run left running is deleted once its store has written nothing for a day, and not before; retention and `LatestRun` match a model of the run-model rule across gaps, year-long pauses, ties and backfills, and no table with a `run_id` keeps a row of a deleted run, the GitHub data a run kept included (#38), an older pull request's link among it (#103) |
| `TestGitHubSync` | `github` | recorded responses ([testdata/github](../../testdata/github/README.md)) become the expected repositories and pull request pages, newest update first, through rate limits (a 403 with Retry-After, a nearly spent budget, and a spent budget and a secondary limit that GitHub reports inside a 200, each waited out once and holding every other request; and a request in flight that meets a 30-second limit while another's 10-minute hold is set, which waits out the rest of the longer hold), a 502, nested follow-ups and a `NOT_FOUND` on a field the data holds as null, read as null (#99); only a pass's last page carries the watermark, the pass's start on GitHub's clock (#101), and a later pass skips a pull request older than its stop point that sits before a newer one and ends at a page wholly older (ADR 0013); a mirror clones and fetches from a local git server that takes only the token, gets a commit-graph with changed-path filters where git writes version 2 (#56), follows a rename by node id, moves to `.stale` and back, and never follows a redirect to another host; the token is in no fact, mirror file, log or error; the pages the passes return, stored through `sqlite.Open` and `PutForgePage`, come back from `LoadForge` as the last page that carried each pull request said, reviews in submission order, and from a `since` between the passes only the pull requests updated at or after it, with the commit-to-account links of the others (#103); `reviews_given@1` over what `LoadForge` returns matches its oracle in scope `all`, the group and the repository, another organization's repository and a failed one counting nowhere, the bots' clusters apart from the people's (`RankInput.Bots`), which no caller ranks or scores and explain lists (#75) (M2); and `app.Sync` through the adapter, the store and git (the sync scenario): a first `--repo` sync whose second page fails moves no watermark and leaves its page pending, its rerun reads the window and sets the watermark to its start, a check that GitHub counts more is recorded and a scan of the mirror carries `forge-incomplete`, reads the links pass's answers among `LoadForge`'s `Links` and links ada's two addresses by `same-forge-account`, a replica with a hole a record-derived watermark left is repaired by one sync's second, full pass, an archived repository's mirror moves to `.stale` (#110, #111), a shortfall the check finds is recorded before the repair pass, which fails, so `Missing` stays and `Synced` does not move, a first sync whose check fails records `From` (the window's start) and nothing missing, asked for by an old name GitHub answers under the current one, records the old name as an alias and names the move in its summary, so a clone whose origin names it joins the data with `forge-moved` and answers `who-knows --repo` by the current name, while a replica that learned no alias gives that clone `forge-unmatched` naming the sync that records it (wave 11, KT-2), and (wave 11's review) the old name's clone, in SSH's scp-like form, is told the set-url in that form, `forge-unmatched` offers none, a clone of a rename under the same owner gets it too while an unrelated repository of the same name gets no warning, the old name's clone beside the mirror is one repository with a `duplicate-repo` warning, an unrelated repository under the old name joins nothing by the alias, a later sync (run 7) looks the alias up again and drops it once GitHub resolves the name to another repository, and a transferred repository's mirror that comes back from `.stale` under its new owner names the old path's name (`MirrorMove.Was`), and every sync with an up-to-date mirror asks GitHub about each author email of its history once, the first even when its pull requests failed, through the email's newest commit, a `NOT_FOUND` on a deleted account read as no account, and the next asks about none (#103) |
| `TestMCPTools` | `mcp` | each tool through the SDK's in-memory client (M4); and AI-assisted shares that add up, against the local report's: no answer, alone or less another, gives back a withheld share, a group's, a repository's or with `--scope all` the whole run's, nor surviving@1's copy of it (`ai_assisted_share`), nor the detail of a shown share, its cohort comparisons, which the stored value holds (#87), and one person's three unmerged addresses count as one (#68); and the agent's dead ends of the dogfood (wave 9): `identity_candidates` paged, stdin's end drained, `changes(person: "")` the `--me` person, the index's `scope_arg`, a group's as `explain`'s subject too, a scope only an earlier run holds named with that run, and a bare name it holds twice with both `scope_arg`s, a repository's areas (`null` without line facts) and `who_knows` of a top-level directory carrying its numbers, and of a deeper path its own (`metrics.PathKnowledge`), a top tier set apart by its intervals in a table of two, arguments the schema refuses refused with what to pass, an id with no method answered, and `diff` of a shallow clone's boundary and of a partial clone refused with why, and of a moved partial clone not blamed on its contents; and wave 10's: `explain` with `days` on a metric of its own window refused in words that name no flag, `days` past the run's 365-day history refused, an area holding nothing sent to `who_knows` by the repository's name, and `subject_kind: all` explaining the whole run's `landed@2` with its landings, and (wave 11, KT-8) with no `subject_kind` a `repo:` scope_arg and a bare repository name explaining `knowledge_loss` about the repository, `durable` about `repo:fixture` refused as about a repository, and `knowledge_loss` about a person's name refused with the kinds it is about, while (wave 11's review) `review_coverage` about a name that is no scope and no person is refused with the run's scopes, and `path` with no `subject_kind` explains an area (`nosuchdir` reaching the area's refusal) and with `subject_kind: repo` is refused, and a page cut below the limit asked for saying so in `page.note` ([mcp.md](mcp.md#the-test)); wave 11: `who_knows` over the `days` asked for, which its answer states, and refused past the run's history |
