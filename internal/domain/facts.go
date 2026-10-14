package domain

import "time"

// Repo is one repository as discovered for a run.
type Repo struct {
	Key     string // dedupe and storage key: the scrubbed origin URL, else "local:" + Name (no origin, or one that does not parse)
	Name    string // directory basename without a trailing ".git"
	Group   string // group name; "" when the repo was named directly
	Path    string // absolute path
	Bare    bool   // a bare repository, such as a managed mirror
	Remote  string // scrubbed origin URL; "" when there is no origin
	Branch  string // default branch (docs/spec/discovery.md#default-branch)
	Head    string // full SHA the branch resolved to; every later command names it
	Shallow bool   // a shallow clone: no blame facts
	Partial bool   // a partial clone: no line facts
}

// Group is a directory named on the command line whose immediate children
// are repositories.
type Group struct {
	Name  string   // basename; "-2", "-3" suffixes for duplicates
	Path  string   // absolute path
	Repos []string // Repo.Key, sorted
}

// Warning is a condition that degraded a run without failing it, such as a
// duplicate or shallow clone.
type Warning struct {
	Code    string // duplicate-repo, shallow-clone, shallow-window, partial-clone, numstat-rename, ...
	Message string // already scrubbed
	Repo    string // Repo.Key; "" for run-level warnings
}

// Role is the kind of code a path holds, assigned by the classifier
// (docs/spec/classification.md#roles). Only RoleProd counts toward the
// still-here headline.
type Role string

// The roles of the default policy.
const (
	RoleProd      Role = "prod"
	RoleSQL       Role = "sql"
	RoleMarkup    Role = "markup"
	RoleIaC       Role = "iac"
	RoleDoc       Role = "doc"
	RoleTest      Role = "test"
	RoleMigration Role = "migration"
	RoleSeed      Role = "seed"
)

// Skip is the reason the classifier dropped a path
// (docs/spec/classification.md#order). A skipped path adds nothing to any
// line count and is never blamed.
type Skip string

// The skip reasons, in the classifier's order.
const (
	SkipDeniedPath      Skip = "denied_path"
	SkipTooBig          Skip = "too_big"
	SkipGeneratedHeader Skip = "generated_header"
	SkipUnclassified    Skip = "unclassified"
)

// PathClass is the classifier's verdict on one path. A run has one per
// path: a path the history touched but Head no longer has is classified at
// the commit that touched it (docs/spec/classification.md#order).
type PathClass struct {
	Path   string
	Role   Role  // "" when skipped
	Skip   Skip  // "" when a role was assigned
	Size   int64 // bytes the too_big check read: at Head, else at the commit that touched the path; -1 when unknown
	AtHead bool  // the path exists at Head; false for a path only the history touched, which gets no header sniff
}

// Person is an identity after git applied the mailmap.
type Person struct {
	Name  string // as git printed it (%aN, blame "author")
	Email string // lowercased: the canonical key everywhere
}

// Commit is one git commit that a landing brought onto the default branch.
type Commit struct {
	SHA        string
	Parents    []string
	Author     Person
	AuthorTime time.Time
	CommitTime time.Time
	CoAuthors  []Person // Co-authored-by trailers, canonicalised with git check-mailmap
	Subject    string   // local only; never sent to an AI
}

// Landing is a first-parent commit of the default branch, with the commits
// it brought in.
type Landing struct {
	SHA     string    // a first-parent commit of the default branch
	Time    time.Time // committer time
	Merge   bool
	Commits []string // git rev-list SHA^1..SHA, the landing included; [SHA] for a non-merge
}

// FileChange is what one commit did to one path.
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

// Revert links a revert landing to the commit it reverts.
type Revert struct{ SHA, Reverted string } // full SHAs

// LineOwner is how many non-blank lines blame attributes to one commit in
// one path at HEAD.
type LineOwner struct {
	Path       string
	Commit     string
	Author     Person
	AuthorTime time.Time
	Lines      int // non-blank lines blame attributes to Commit in Path at HEAD
}

// IgnoreRevs records which --ignore-revs-file blame used for a repository.
type IgnoreRevs struct {
	Source string // IgnoreRevsRoot, IgnoreRevsCLI or ""
	SHA256 string
}

// The sources of an ignore-revs file (docs/spec/blame-collector.md).
const (
	IgnoreRevsRoot = "root" // the repository's own .git-blame-ignore-revs at Head
	IgnoreRevsCLI  = "cli"  // --ignore-revs-file, single-repository runs only
)

// FileError is a failure confined to one path, such as a blame timeout.
type FileError struct{ Path, Message string }

// Window is the half-open time interval [Since, Until).
type Window struct{ Since, Until time.Time }

// Contains reports whether t lies in [w.Since, w.Until).
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Since) && t.Before(w.Until)
}

// RepoFacts is everything one run learned about one repository.
type RepoFacts struct {
	Repo        Repo
	Status      string // RepoStatusOK or RepoStatusError
	Error       string // scrubbed and truncated; "" when ok
	Warnings    []Warning
	FirstCommit time.Time // author time of the earliest root commit reachable from Head; zero when unknown, such as a shallow clone whose boundary hides the roots
	History     Window    // the landing window the history covers
	Paths       []PathClass
	Commits     []Commit // landing commits plus every commit a landing brought in
	Landings    []Landing
	Changes     []FileChange
	Reverts     []Revert
	LineFacts   bool // blame ran (false for week runs, shallow and partial clones)
	Owners      []LineOwner
	IgnoreRevs  IgnoreRevs
	BlameErrors []FileError
}

// The values of RepoFacts.Status. A repository is a bulkhead: its failure
// is recorded here and the run continues.
const (
	RepoStatusOK    = "ok"
	RepoStatusError = "error"
)

// PolicyRef identifies the policy a run used.
type PolicyRef struct {
	Version int
	SHA256  string
}

// FactSet is everything one run learned: the input to every metric.
type FactSet struct {
	Now      time.Time
	Policy   PolicyRef
	Groups   []Group
	Repos    []RepoFacts // sorted by Repo.Key
	Warnings []Warning   // run-level
	Forge    *ForgeFacts // nil until GitHub data is synced (M2)
}

// ForgeFacts is the replica of GitHub data (pull requests, reviews, login
// links and watermarks) that a run read. It is empty until the GitHub sync
// (#14) adds those facts in M2.
type ForgeFacts struct{}
