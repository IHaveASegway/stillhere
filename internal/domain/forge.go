package domain

import (
	"context"
	"net/url"
	"slices"
	"strings"
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

// RemoteRepo returns the repository a git remote names on its host, as
// "host/owner/name": lowercased, without a user, a port or a trailing
// ".git", whether the remote is a URL (https://github.com/acme/widget.git,
// ssh://git@github.com:22/acme/widget) or scp-like
// (git@github.com:acme/widget.git). ok is false for a remote that names no
// host, or no owner and name on it, such as a local directory.
//
// It is how the Forge context joins the repositories a run scanned: a
// run's repository (Repo.Remote) is a forge repository (ForgeRepo.Key) when
// both name the same RemoteRepo, whichever protocol the clone used. GitHub
// treats owner and repository names case-insensitively, and so does the
// comparison. A host alias, such as an ~/.ssh/config Host or
// ssh.github.com, names another host and does not match.
func RemoteRepo(remote string) (string, bool) {
	var host, path string
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", false
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like, [user@]host:path, needs its colon before any slash;
		// git takes anything else for a local path.
		colon := strings.IndexByte(remote, ':')
		if colon < 0 || strings.Contains(remote[:colon], "/") {
			return "", false
		}
		host, path = remote[:colon], remote[colon+1:]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		if len(host) < 2 {
			return "", false // a Windows drive, C:\src\widget, is a local path
		}
	}
	path = strings.Trim(strings.TrimSuffix(strings.Trim(strings.ToLower(path), "/"), ".git"), "/")
	if host == "" || !strings.Contains(path, "/") {
		return "", false
	}
	return strings.ToLower(host) + "/" + path, true
}

// ForgeJoin is the one join between the forge repositories of a replica and
// the repositories a run scanned: for each ForgeRepo.ID, the Repo.Key of
// every repository of the run whose origin names it (RemoteRepo of
// Repo.Remote and of ForgeRepo.Key), sorted. A forge repository that no
// repository of the run names is absent, and the result is nil when none
// is named. A scan joins by it once: the forge repositories in it, their
// watermarks and their pull requests are the run's GitHub data, which the
// run keeps with the join itself (ForgeFacts.Join, RunWatermark.Keys), and
// every metric that reads GitHub data counts by that kept join, so what a
// run counts and what it dates can never disagree, and a later change to
// this function moves no stored run.
//
// A repository whose collection failed (status error) joins nothing: a
// failed repository adds nothing to a run's numbers, its GitHub ones
// included, as every page that lists it says.
func ForgeJoin(repos []RepoFacts, forge []ForgeRepo) map[string][]string {
	byName := map[string][]string{} // RemoteRepo → Repo.Key of the run's repositories
	for i := range repos {
		rf := &repos[i]
		if rf.Status == RepoStatusError {
			continue
		}
		if name, ok := RemoteRepo(rf.Repo.Remote); ok {
			byName[name] = append(byName[name], rf.Repo.Key)
		}
	}
	var out map[string][]string
	for _, r := range forge {
		name, ok := RemoteRepo(r.Key)
		if !ok || len(byName[name]) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		keys := slices.Clone(byName[name])
		slices.Sort(keys)
		out[r.ID] = slices.Compact(keys)
	}
	return out
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

// ForgeFacts is GitHub data as facts.db holds it: the repositories, their
// watermarks, and their pull requests. LoadForge returns the replica's;
// a scan run's FactSet.Forge is the run's own part of it, the forge
// repositories ForgeJoin joins to the run's repositories with their
// watermarks and pull requests, which the run keeps (PutRunForge) and its
// readers read back (RunForge), so a stored run computes over what it read.
type ForgeFacts struct {
	Repos        []ForgeRepo      // sorted by Key, then ID
	PullRequests []PullRequest    // sorted by Repo, then Number
	Watermarks   []ForgeWatermark // sorted by Repo: the replica's, of which a run keeps those of its own repositories (RunWatermark)
	// Join is the run's join of these forge repositories to its own
	// repositories, as its scan made it with ForgeJoin and recorded it
	// (RunWatermark.Keys): ForgeRepo.ID → the sorted Repo.Key of every
	// repository of the run that it is. The metrics count by it, so a stored
	// run keeps one owner of the join, its record, and a later stillhere
	// whose ForgeJoin reads origins differently moves none of its numbers.
	// It is nil for the replica LoadForge returns, which no run has joined:
	// a metric over the replica itself joins it with ForgeJoin.
	Join map[string][]string
}

// RunWatermark is the watermark of one of a scan run's forge repositories
// (ForgeJoin) as the run read it, with the repository's name then and the
// run's repositories it is. A run keeps these beside the pull requests it
// read (ForgeStore.PutRunForge, docs/spec/codebase.md#run-model), and all
// of it is the run's own: a later sync that renames the repository, which
// the replica follows by node id, or moves its watermark on, changes
// nothing a stored run shows. Matching the watermark to the run's
// repositories again at read time, against today's names, would let every
// later sync rewrite a stored run's pages; what a run read is immutable,
// like its facts (Helland, "Immutability Changes Everything", CIDR 2015).
type RunWatermark struct {
	ForgeWatermark          // Repo is the ForgeRepo.ID; zero times when the replica held no page of it
	Name           string   // "owner/name" as the replica listed it
	Keys           []string // Repo.Key of the run's repositories whose origin names it (ForgeJoin), sorted; never empty
}

// MirrorMove is a mirror that ForgeSource.ReconcileMirrors moved, or found
// out of place and could not move.
type MirrorMove struct {
	Repo   string // the ForgeRepo.ID the mirror carries
	From   string // the absolute path it was at
	To     string // the absolute path it moved to, or would have
	Reason string // MirrorRenamed, MirrorRestored, MirrorStale or MirrorDuplicate
	// Err says why the mirror could not move, scrubbed; "" when it moved.
	// A listed repository whose mirror could not move is not mirrored by
	// that sync, which would otherwise clone a second copy of it.
	Err string
}

// The reasons a mirror moves.
const (
	MirrorRenamed   = "renamed"   // its repository's owner or name changed: a rename, a transfer or a change of case
	MirrorRestored  = "restored"  // its repository is listed again, so it is back from root/.stale
	MirrorStale     = "stale"     // its repository is no longer listed: deleted, transferred away, or hidden from the token
	MirrorDuplicate = "duplicate" // a second mirror of a repository that has one at its path
)

// ForgeSource is the port to a forge: its API and its git remotes. GitHub's
// GraphQL API is the v1 adapter (internal/github); GitLab can be another
// (ADR 0004). An adapter holds its own credentials and never returns them.
type ForgeSource interface {
	// Repos lists the repositories an organization or user owns, sorted by
	// Key. A login the forge does not know is a *UsageError.
	Repos(ctx context.Context, owner string) ([]ForgeRepo, error)
	// ReconcileMirrors runs once per sync, before any mirror is fetched,
	// with owner's whole listing (Repos, archived repositories, forks and
	// empty ones included). A mirror carries its repository's node id, and
	// that, never its path, says which repository it is: a mirror whose
	// repository was renamed or transferred moves to the repository's new
	// path, and a mirror under root/<owner> whose repository the listing no
	// longer holds moves to root/.stale, outside every directory scan
	// reads, until the repository is listed again. Otherwise scan would
	// count a renamed repository twice, once under each name. It returns
	// the moves, sorted by From; a move that failed carries its Err.
	ReconcileMirrors(ctx context.Context, owner string, listed []ForgeRepo, root string) ([]MirrorMove, error)
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
// runs (docs/spec/codebase.md#run-model), of which each run keeps the part
// it read (its watermarks and its pull requests, ADR 0011). Keeping them
// apart also leaves FactStore and its implementation untouched (Interface
// Segregation Principle), and a reader without this port reads no GitHub
// data at all, neither the dating nor the pull requests.
type ForgeStore interface {
	// PutForgeRepos upserts the repositories a sync is about to bring up to
	// date, by ID.
	PutForgeRepos(ctx context.Context, repos []ForgeRepo) error
	// ForgeWatermark returns a repository's watermark, with zero times when
	// the replica has none.
	ForgeWatermark(ctx context.Context, repo string) (ForgeWatermark, error)
	// PutForgePage stores a page in one transaction: each pull request
	// replaces what the replica held for it unless the replica's copy was
	// updated later, so a page that a slower, overlapping sync read first
	// cannot undo a newer one; and the repository's watermark rises to
	// page.Updated (it never falls).
	PutForgePage(ctx context.Context, page *ForgePage) error
	// MarkForgeSynced records that the sync that started at `at` brought the
	// repository's pull requests up to date.
	MarkForgeSynced(ctx context.Context, repo string, at time.Time) error
	// LoadForge returns the replica's repositories and watermarks, and every
	// pull request last updated at or after since, sorted; nil when the
	// replica holds no repository.
	LoadForge(ctx context.Context, since time.Time) (*ForgeFacts, error)
	// PutRunWatermarks records, for a run that is being written, the
	// watermark of each of its forge repositories as it read them with
	// LoadForge, with the name and the run's repositories each one is
	// (RunWatermark). They date its forge numbers: a stored run shows "as
	// of" them (ADR 0004), over the repositories they joined then, after
	// later syncs moved the replica on or renamed a repository. They are
	// deleted with the run. A watermark without Keys is an error.
	PutRunWatermarks(ctx context.Context, runID int64, watermarks []RunWatermark) error
	// RunWatermarks returns the watermarks a complete or partial run
	// recorded, sorted by Repo, or nil when it recorded none. Like
	// FactStore's read side, it returns ErrNotFound for a run that is
	// unknown, running or failed. A watermark recorded before facts.db
	// kept the join (schema version 3) has no Name and no Keys.
	RunWatermarks(ctx context.Context, runID int64) ([]RunWatermark, error)
	// PutRunForge records, for a running run, the GitHub data it computed
	// over (FactSet.Forge): its forge repositories (ForgeJoin) as the
	// replica listed them, and every pull request of them it read, whole,
	// with its labels, reviews and commits (ADR 0011). A pull request of a
	// repository not among repos is an error. Like the watermarks, which
	// PutRunWatermarks records, they are deleted with the run.
	PutRunForge(ctx context.Context, runID int64, repos []ForgeRepo, pulls []PullRequest) error
	// RunForge returns the GitHub data a complete or partial run kept
	// (PutRunForge), sorted as LoadForge sorts, with the watermarks it
	// recorded (PutRunWatermarks) as Watermarks: what the run computed
	// over, so a reader computes over it too, never over today's replica.
	// It returns nil when the run kept none: a run with no forge
	// repository, or one stored before facts.db kept them (schema version
	// 4). ErrNotFound unless the run is complete or partial.
	RunForge(ctx context.Context, runID int64) (*ForgeFacts, error)
}
