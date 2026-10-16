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
// reaches (ADR 0013). Its times are positions on the forge's clock that a
// completed pass reached, never values read from the pull requests: the
// order of the forge's list is the forge's, and a checkpoint derived from
// the records in it drifts when the two disagree (#101).
type ForgeWatermark struct {
	Repo string // ForgeRepo.ID
	// Updated: the last completed pass began at Updated, on the forge's
	// clock, and read every pull request last updated from From until
	// then, so the replica holds each one as of that update or a later one
	// (while Missing is 0). Zero until a pass completes. A pass that stops
	// halfway leaves it as it was, and it never moves back.
	Updated time.Time
	// From is the oldest last update the replica is complete from: the
	// start of the history window, or of --since, that the last full pass
	// read down to. Zero for a replica whose first pass read the whole
	// list, as one stored before ADR 0013 did.
	From time.Time
	// Synced is the start of the last sync that brought the repository's
	// pull requests up to date: the "as of" time pages show. Zero until one
	// has.
	Synced time.Time
	// Missing is what the last completed sync's completeness check found
	// (reconciliation): how many pull requests the forge counts as updated
	// in the window that the replica does not hold. 0 when they match.
	// While it is not 0, every sync reads the whole window again, and the
	// pages of a run that read it say its GitHub data is incomplete.
	Missing int
	// Pending is true while pages of a pass that has not completed are
	// stored: with Synced zero, the first sync of the repository stopped
	// halfway, and the replica holds only the part it reached.
	Pending bool
}

// ForgePage is one page of a repository's pull requests. A sync stores each
// page in one transaction, the watermark with the page that completes a
// pass, so a pass that stops halfway leaves the watermark where it was and
// the next sync reads the same range again: storing a pull request again
// changes nothing (an idempotent upsert), so a rerun costs time, never a
// hole.
type ForgePage struct {
	Repo         string        // ForgeRepo.ID
	PullRequests []PullRequest // each complete: every label, review and commit; empty only on the last page of a pass
	// Updated is zero on every page but the last of a pass that read its
	// whole range, which carries the time the pass began on the forge's
	// clock: the new ForgeWatermark.Updated. It never lowers the
	// watermark.
	Updated time.Time
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
	// Links holds, of the pull requests LoadForge leaves out (those last
	// updated before its since: older than every metric's window), only
	// their commits' links to an account, and the account each author email
	// of the repository's history belongs to, as sync's links pass asked
	// GitHub through the email's newest commit (ForgeSource.LinkAuthors,
	// ForgeStore.ForgeLinks), distinct (#103). No metric reads it: GitHub's
	// attribution of an address to an account is evidence of who it is
	// however old the commit, so identity's same-forge-account rule reads
	// it beside the pull requests' own commits. Sorted by Repo, SHA,
	// AuthorLogin, then AuthorEmail.
	Links []ForgeLink
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

// ForgeLink is one commit with the account the forge resolved its author
// email to (ForgeFacts.Links): a pull request's own commit or its merge
// commit, or the newest commit of an author email the links pass asked
// about, in the forge repository Repo. AuthorLogin is never "".
type ForgeLink struct {
	Repo string // ForgeRepo.ID
	ForgeCommit
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
	MirrorStale     = "stale"     // its repository is not among those the sync mirrors: deleted, transferred away, hidden from the token, or left out (archived, a fork)
	MirrorDuplicate = "duplicate" // a second mirror of a repository that has one at its path
)

// ForgeSource is the port to a forge: its API and its git remotes. GitHub's
// GraphQL API is the v1 adapter (internal/github); GitLab can be another
// (ADR 0004). An adapter holds its own credentials and never returns them.
type ForgeSource interface {
	// Repos lists the repositories an organization or user owns, sorted by
	// Key. A login the forge does not know is a *UsageError.
	Repos(ctx context.Context, owner string) ([]ForgeRepo, error)
	// Repo looks up one repository by owner and name (#111), as Repos
	// lists it. One the forge does not have, or does not show the token, is
	// a *UsageError.
	Repo(ctx context.Context, owner, name string) (ForgeRepo, error)
	// ReconcileMirrors runs once per sync, before any mirror is fetched,
	// with the repositories the sync mirrors. A mirror carries its
	// repository's node id, and that, never its path, says which
	// repository it is: a mirror whose repository was renamed or
	// transferred moves to the repository's new path. When owner is the
	// organization whose listing this is, a mirror under root/<owner> of a
	// repository not in listed (deleted, transferred away, hidden from the
	// token, or one the sync leaves out, such as an archived repository,
	// #110) moves to root/.stale, outside every directory scan reads,
	// until it is listed again. owner "" says listed is not an owner's
	// whole listing (sync --repo), so no other mirror moves. Otherwise scan
	// would count a renamed repository twice, once under each name. It
	// returns the moves, sorted by From; a move that failed carries its
	// Err.
	ReconcileMirrors(ctx context.Context, owner string, listed []ForgeRepo, root string) ([]MirrorMove, error)
	// Mirror creates the bare mirror of repo under root, or brings an
	// existing one up to date, points its HEAD at repo.DefaultBranch, and
	// returns its path (docs/spec/git-invocation.md#sync-commands-network).
	Mirror(ctx context.Context, repo ForgeRepo, root string) (string, error)
	// PullRequests calls put with every pull request of repo last updated
	// at or after since, a page at a time, newest update first, and
	// returns put's first error. A zero since reads the whole list. Pages
	// may repeat a pull request the replica already holds; storing one
	// again changes nothing. Only the last page of a pass that read down to
	// since carries a watermark (ForgePage.Updated): a pass that stops
	// halfway moves none.
	PullRequests(ctx context.Context, repo ForgeRepo, since time.Time, put func(*ForgePage) error) error
	// CountPullRequests is how many pull requests of repo the forge counts
	// as last updated at or after from (zero: since its first) and settled
	// before start, the time a completed pass began (ForgePage.Updated):
	// the completeness check's other side (ADR 0013). One updated in the
	// last minutes before start is left out, since the pass may have read it
	// before that update. The forge may also leave out a few it hides from
	// its count, so the count is a lower bound of what a complete replica
	// holds.
	CountPullRequests(ctx context.Context, repo ForgeRepo, from, start time.Time) (int, error)
	// LinkAuthors asks the forge which account each author email of the
	// mirror's history belongs to (#103's evidence, ADR 0013): every email
	// a commit reachable from the mirror's HEAD was written with, but those
	// skip holds, lowercased, each through one commit, its newest. It calls
	// put with the answers a batch at a time, one ForgeCommit per email: the
	// commit asked about, the email as git wrote it (lowercased) and the
	// account's login, "" for an email the forge links to no account. An
	// email whose commit the forge does not have is left out, to be asked
	// again. A pass whose put fails, or that stops halfway, loses only the
	// batches it had not handed over.
	//
	// The links are what identity's same-forge-account rule reads. The
	// pull requests a sync reads hold links too, but only for the history
	// window; the account of an email does not age, and asking per email,
	// not per commit, costs one request per hundred emails, whatever the
	// length of the history.
	LinkAuthors(ctx context.Context, repo ForgeRepo, mirror string, skip map[string]bool, put func([]ForgeCommit) error) error
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
	// cannot undo a newer one. A page with an Updated completes its pass:
	// the repository's watermark rises to it (it never falls) and Pending
	// clears; any other page sets Pending.
	PutForgePage(ctx context.Context, page *ForgePage) error
	// MarkForgeSynced records that the sync that started at `at` brought the
	// repository's pull requests up to date.
	MarkForgeSynced(ctx context.Context, repo string, at time.Time) error
	// MarkForgeChecked records, in one transaction, what a sync that started
	// at `at` and completed its passes found (ADR 0013): the replica is
	// complete from `from` (ForgeWatermark.From), and the last completeness
	// check that ran counted `missing` pull requests it lacks
	// (ForgeWatermark.Missing): this sync's, or, when its check could not
	// run, the one the replica already records. It marks the repository
	// synced at `at`, as MarkForgeSynced does.
	MarkForgeChecked(ctx context.Context, repo string, at, from time.Time, missing int) error
	// MarkForgeMissing records a completeness check's shortfall alone, before
	// the full pass that is to fill it in (ADR 0013): if that pass stops
	// halfway or fails, the replica still says pull requests are missing,
	// so a scan carries the caveat and the next sync reads the window again.
	// Synced and From stay as they were.
	MarkForgeMissing(ctx context.Context, repo string, missing int) error
	// CountForgePulls is how many pull requests of the repository the
	// replica holds as last updated at or after since (zero: all of them):
	// the completeness check's own side.
	CountForgePulls(ctx context.Context, repo string, since time.Time) (int, error)
	// ForgeLinks returns the author emails of the repository the replica
	// has asked the forge about (ForgeSource.LinkAuthors), each with the
	// login of the account the forge links it to, "" for none. Empty when
	// it has asked about none.
	ForgeLinks(ctx context.Context, repo string) (map[string]string, error)
	// PutForgeLinks upserts the repository's links by email, in one
	// transaction: an email asked about again keeps the newer answer.
	PutForgeLinks(ctx context.Context, repo string, links []ForgeCommit) error
	// LoadForge returns the replica's repositories and watermarks, and every
	// pull request last updated at or after since, sorted, with the
	// commit-to-account links of the others and of the history's author
	// emails (Links, #103); nil when the replica holds no repository.
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
	// with its labels, reviews and commits (ADR 0011), and the
	// commit-to-account links of its older pull requests (ForgeFacts.Links,
	// #103). A pull request or a link of a repository not among repos is an
	// error. Like the watermarks, which PutRunWatermarks records, they are
	// deleted with the run.
	PutRunForge(ctx context.Context, runID int64, repos []ForgeRepo, pulls []PullRequest, links []ForgeLink) error
	// RunForge returns the GitHub data a complete or partial run kept
	// (PutRunForge), sorted as LoadForge sorts, with the watermarks it
	// recorded (PutRunWatermarks) as Watermarks: what the run computed
	// over, so a reader computes over it too, never over today's replica.
	// It returns nil when the run kept none: a run with no forge
	// repository, or one stored before facts.db kept them (schema version
	// 4). ErrNotFound unless the run is complete or partial.
	RunForge(ctx context.Context, runID int64) (*ForgeFacts, error)
}
