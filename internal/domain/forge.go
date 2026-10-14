package domain

import (
	"context"
	"time"
)

// This file is the Forge bounded context (docs/architecture.md#bounded-contexts):
// the facts a sync copies from a forge, and the two ports the sync runs
// through. GitHub is the system of record for every fact here (ADR 0004);
// facts.db holds a read-only replica of them. The GitHub adapter
// (internal/github) is an anti-corruption layer: it translates GitHub's
// names and states into these types, so no GitHub field name reaches a
// metric. The GitHub sync (#14) owns this file.

// ForgeRepo is one repository as the forge lists it.
type ForgeRepo struct {
	ID            string // the forge's node id: the replica's key, stable across renames
	Key           string // the HTTPS clone URL without userinfo, which is also its managed mirror's Repo.Key
	Owner         string // organization or user login, as the forge spells it
	Name          string
	DefaultBranch string // "" for an empty repository
	Archived      bool
	Fork          bool
}

// ForgeUser is an account as it appears on a pull request or a review.
type ForgeUser struct {
	Login string // "" for a deleted account (GitHub's ghost)
	Bot   bool   // an app or bot account, by the forge's own account type
}

// ForgeCommit is a commit the forge associates with a pull request, with the
// account the forge resolved its author email to. It is how a login is
// linked to a person (ADR 0004): the run's own mailmapped author of the same
// SHA is that login's canonical email.
type ForgeCommit struct {
	SHA         string // full, lowercase
	AuthorEmail string // the commit's author email as the forge read it, lowercased; no mailmap applied
	AuthorLogin string // the account the forge resolved AuthorEmail to; "" when none
}

// The states of a pull request.
const (
	PullOpen   = "open"
	PullClosed = "closed" // closed without being merged
	PullMerged = "merged"
)

// The states of a submitted review. A pending review is never synced.
const (
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewCommented        = "commented"
	ReviewDismissed        = "dismissed" // an approval or a change request that was dismissed later
)

// Review is one submitted review of a pull request. Every inline comment in
// a review is written by the review's author.
type Review struct {
	ID         string // the forge's node id
	Author     ForgeUser
	State      string    // one of the Review* constants; another forge state is kept lowercased
	Submitted  time.Time // UTC
	BodyLength int       // characters in the review's own text, surrounding white space trimmed; 0 when it has none
	Comments   int       // inline comments in the review
}

// PullRequest is one pull request with everything the replica keeps about
// it. It is replaced as a whole on every sync that sees it updated, so a
// removed label or a force-pushed commit does not linger.
type PullRequest struct {
	ID          string // the forge's node id: the replica's key
	Repo        string // ForgeRepo.ID
	Number      int
	Title       string // untrusted text; local only, like Commit.Subject, and never sent to an AI
	Author      ForgeUser
	State       string // one of the Pull* constants
	Draft       bool
	BaseBranch  string
	Created     time.Time
	Updated     time.Time     // the forge's last-update time: what the watermark follows
	Closed      time.Time     // zero while open
	Merged      time.Time     // zero unless merged
	MergeCommit ForgeCommit   // the commit the merge put on BaseBranch (the merge, the squash, or the last rebased commit); zero unless merged
	Labels      []string      // sorted
	Reviews     []Review      // submitted reviews, sorted by Submitted, then ID
	Commits     []ForgeCommit // the pull request's own commits, in the forge's order (oldest first)
}

// ForgeWatermark is how far the replica of one repository's pull requests
// reaches.
type ForgeWatermark struct {
	Repo string // ForgeRepo.ID
	// Updated: every pull request whose last update is earlier than Updated
	// is in the replica, as of that update or a later one. Zero until the
	// first page of the repository is stored. It never moves back.
	Updated time.Time
	// Synced is the start of the last sync that brought the repository's
	// pull requests up to date: the "as of" time pages show. Zero until one
	// has.
	Synced time.Time
}

// ForgePage is one page of a repository's pull requests, with the watermark
// the replica reaches once the page is stored. A sync stores each page and
// its watermark in one transaction, so a sync that stops halfway resumes
// from the last page it stored.
type ForgePage struct {
	Repo         string        // ForgeRepo.ID
	PullRequests []PullRequest // each complete: every label, review and commit; empty only on the last page of a pass
	Updated      time.Time     // the new ForgeWatermark.Updated; never earlier than the one the pass started from
}

// ForgeFacts is the forge replica a run read: the repositories, their
// watermarks, and the pull requests the run's metrics may use.
type ForgeFacts struct {
	Repos        []ForgeRepo      // sorted by Key, then ID
	PullRequests []PullRequest    // sorted by Repo, then Number
	Watermarks   []ForgeWatermark // sorted by Repo: the watermarks the run read, which date every forge number
}

// ForgeSource is the port to a forge: its API and its git remotes. GitHub's
// GraphQL API is the v1 adapter (internal/github); GitLab can be another
// (ADR 0004). An adapter holds its own credentials and never returns them.
type ForgeSource interface {
	// Repos lists the repositories an organization or user owns, sorted by
	// Key. A login the forge does not know is a *UsageError.
	Repos(ctx context.Context, owner string) ([]ForgeRepo, error)
	// Mirror creates the bare mirror of repo under root, or brings an
	// existing one up to date, points its HEAD at repo.DefaultBranch, and
	// returns its path (docs/spec/git-invocation.md#sync-commands-network).
	Mirror(ctx context.Context, repo ForgeRepo, root string) (string, error)
	// PullRequests calls put with every pull request of repo last updated
	// at or after since, a page at a time, and returns put's first error.
	// A zero since is the repository's first pass. Pages may repeat a pull
	// request the replica already holds; storing one again changes nothing.
	PullRequests(ctx context.Context, repo ForgeRepo, since time.Time, put func(*ForgePage) error) error
}

// ForgeStore is the port to the forge replica in facts.db: pull requests and
// their reviews, labels and commits, upserted by node id, with a watermark
// per repository. It is a port of its own rather than more FactStore methods
// because the two hold different kinds of data: FactStore holds per-run
// snapshots of git facts, while forge facts are a replica that outlives
// runs (docs/spec/codebase.md#run-model). Keeping them apart also leaves
// FactStore and its implementation untouched (Interface Segregation
// Principle).
type ForgeStore interface {
	// PutForgeRepos upserts the repositories a sync is about to bring up to
	// date, by ID.
	PutForgeRepos(ctx context.Context, repos []ForgeRepo) error
	// ForgeWatermark returns a repository's watermark, with zero times when
	// the replica has none.
	ForgeWatermark(ctx context.Context, repo string) (ForgeWatermark, error)
	// PutForgePage stores a page in one transaction: each pull request
	// replaces what the replica held for it, and the repository's watermark
	// rises to page.Updated (it never falls).
	PutForgePage(ctx context.Context, page *ForgePage) error
	// MarkForgeSynced records that the sync that started at `at` brought the
	// repository's pull requests up to date.
	MarkForgeSynced(ctx context.Context, repo string, at time.Time) error
	// LoadForge returns the replica's repositories and watermarks, and every
	// pull request last updated at or after since, sorted; nil when the
	// replica holds no repository.
	LoadForge(ctx context.Context, since time.Time) (*ForgeFacts, error)
}
