package domain

import (
	"slices"
	"strings"
	"time"
)

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

// Boundary reports whether c is where the repository's shallow clone ends:
// a commit the clone holds without its parents, which git prints with
// none. Its diff against the empty tree is its whole tree, not its change,
// so the history collector gives it no FileChange; the metrics place a
// window inside a shallow clone's history by it; and a diff of it is no
// change of its own (docs/spec/week-collector.md). It is the one rule for
// the boundary: a commit with no parent in a shallow clone, which can also
// be a real root that the clone's depth reached, since git tells the two
// apart nowhere in what it prints.
func (r Repo) Boundary(c *Commit) bool { return r.Shallow && len(c.Parents) == 0 }

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
	Code    string // duplicate-repo, shallow-clone, shallow-window, partial-clone, ignore-revs, excluded-repo, path-failed, ...
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

// Roles are the classifier's roles, in the order messages list them: the
// one list that a policy's role names and the paths command's --role are
// checked against.
func Roles() []Role {
	return []Role{RoleProd, RoleSQL, RoleMarkup, RoleIaC, RoleDoc, RoleTest, RoleMigration, RoleSeed}
}

// Known reports whether r is one of Roles.
func (r Role) Known() bool { return slices.Contains(Roles(), r) }

// RoleList names Roles for a message: "prod, sql, markup, ...".
func RoleList() string {
	names := make([]string, 0, len(Roles()))
	for _, r := range Roles() {
		names = append(names, string(r))
	}
	return strings.Join(names, ", ")
}

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
	CoAuthors  []Person // Co-authored-by trailers, canonicalised with git check-mailmap, in trailer order (credit reads the first human)
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
// one path at HEAD, from one path in that commit. Lines are credited like
// commits (identity.Credit over Author and CoAuthors, ADR 0007), so an
// agent's commit with a human co-author counts for that human in every
// blame-based metric.
//
// OrigPath is the path the lines had in Commit, as blame names it (its
// porcelain "filename", git's blame origin): the pair (OrigPath, Commit)
// is the one key that both blame and the history (FileChange's Path and
// Commit) give the same lines, whichever renames lie between Commit and
// HEAD and whether or not the history saw them. Blame usually reaches a
// commit through one path, so a (Path, Commit) pair has one row; a commit
// that a merge reached through two paths has one row per path.
type LineOwner struct {
	Path       string
	Commit     string
	OrigPath   string // the path the lines had in Commit; "" when it is Path, else the path blame followed renames back to
	Author     Person
	AuthorTime time.Time
	CoAuthors  []Person // Commit's Co-authored-by trailers, canonicalised and ordered like Commit.CoAuthors
	Lines      int      // non-blank lines blame attributes to Commit, from OrigPath, in Path at HEAD
}

// IgnoreRevs records which --ignore-revs-file blame used for a repository.
type IgnoreRevs struct {
	Source string   // IgnoreRevsRoot, IgnoreRevsCLI or ""
	SHA256 string   // of the file's bytes; "" when Source is ""
	Revs   []string // the commits git blame ignores: each listed ID that names a commit, or an annotated tag of one (peeled), lowercased and sorted; durable@1 and simplifying@1 exclude these commits
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

// weekLength is the length of the blocks Weeks and Week count.
const weekLength = 7 * 24 * time.Hour

// Weeks returns how many 7-day blocks w holds, counted back from Until:
// ⌈(Until − Since) / 7 days⌉, the oldest one short when w is not a whole
// number of weeks (13 for 90 days), and 0 for an empty window. A Sum
// metric's bootstrap resamples these blocks (docs/metrics.md#ranking-rules),
// so this is rank.Options.Blocks for w.
func (w Window) Weeks() int {
	if !w.Until.After(w.Since) {
		return 0
	}
	return ceilWeeks(w.Until.Sub(w.Since))
}

// Week returns the 7-day block of w that t falls in, counted back from Until
// and half-open like w: block k holds [Until − 7(k+1) days, Until − 7k days),
// so t is in block ⌈(Until − t) / 7 days⌉ − 1, 0 is the last 7 days, and a
// change exactly k weeks before Until is in block k − 1. Every t in w is in
// a block in [0, w.Weeks()), which is rank.Cluster.Block for a change at t.
// Week does not check that t is in w: Until itself is in block −1, and a t
// shortly before Since may share the oldest block's week.
//
// This is the one definition of a week block. A metric that computed
// ⌊(Until − t) / 7 days⌋ instead would put a change exactly k weeks back in
// block k, and a change at Since in block Weeks() when w is a whole number
// of weeks, which rank.Intervals rejects.
func (w Window) Week(t time.Time) int {
	return ceilWeeks(w.Until.Sub(t)) - 1
}

// ceilWeeks is ⌈d / 7 days⌉, exact for a negative d too: integer division
// truncates toward zero, which is the ceiling for a negative quotient.
func ceilWeeks(d time.Duration) int {
	q := d / weekLength
	if d%weekLength > 0 {
		q++
	}
	return int(q)
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
	IgnoreRevs  IgnoreRevs // scan runs, shallow and partial clones included (collect.Blame with no paths resolves it)
	BlameErrors []FileError
	// Aliases are the emails git's mailmap rewrote in the commits the facts
	// hold (#53): sorted by Email, Name, then Canonical, distinct; nil when
	// it rewrote none, and in a run stored before facts.db kept them.
	Aliases []Alias
}

// Alias is an email git's mailmap rewrote in a repository: a commit of the
// facts, or one of its Co-authored-by trailers, was written as Name and
// Email, and git gave it the canonical email Canonical. stillhere never
// parses a mailmap; this is what git did with one, which the identities
// answer reads to follow the lines git applied, since git never chains
// them. The name is part of it because git prefers a line that matches a
// commit's name and email over one that matches its email alone, whatever
// their order in the file, so a line that replaces it must name both.
type Alias struct {
	Email     string // as the commit or the trailer wrote it, lowercased
	Name      string // the name written beside it (%an, the trailer's), as written; "" for none
	Canonical string // the email git's mailmap gave it, lowercased; never Email
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
