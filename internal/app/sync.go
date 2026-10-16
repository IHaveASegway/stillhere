package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// SyncRequest asks to bring mirrors and forge facts up to date: the only use
// case that touches the network (ADR 0004). It names an organization's
// repositories (Org) or some repositories by name (Repos), never both.
type SyncRequest struct {
	Org             string   // GitHub organization (or user) login; "" with Repos
	Repos           []string // "owner/name" each (#111): these repositories only, whatever their flags but empty ones
	Mirrors         string   // absolute directory of the bare mirrors, one <owner>/<repo>.git each
	IncludeArchived bool
	IncludeForks    bool
	// Since reads the pull requests updated since then too, when it is
	// before the history window (--since): a first sync, and one after it
	// that reaches further back than the replica, reads down to it. Zero:
	// the history window, which every metric reads within.
	Since time.Time
	// Now is when the sync started, read once by the command line from the
	// wall clock (sync takes no --now). It stamps the sync run and each
	// repository's ForgeWatermark.Synced, the "as of" time of its forge
	// facts, and the history window a full pass reads ends at it.
	Now time.Time
}

// SyncSummary is what a sync did, which the command line prints as its
// closing line whatever the outcome, so a scheduled sync's log says how it
// went without reading the warnings (#100).
type SyncSummary struct {
	Repos      int // the repositories the sync set out to bring up to date
	Synced     int // of them, those whose mirror and pull requests it brought up to date
	Failed     int // those it did not: the mirror, the pull requests or both failed; not those an interruption stopped
	Incomplete int // synced, but GitHub counts pull requests in the window the replica lacks (ADR 0013)
	Unchecked  int // synced, but the completeness check could not run
	Pulls      int // pull requests read and stored, pages read twice included
	Took       time.Duration
}

// syncJobs bounds how many repositories sync at once: their mirrors fetch
// in parallel, and their passes send one API request each at a time,
// which the forge adapter bounds to three in flight (#100).
const syncJobs = 3

// syncProgressPages is how many pages of pull requests a progress line
// stands for: 500 pull requests at the adapter's 50 a page, half a minute
// or so at GitHub's pace, so a full pass of a large repository shows it is
// moving while an incremental one, a page or two, logs only its "synced"
// line.
const syncProgressPages = 10

// Sync lists the repositories to sync, keeps a bare mirror of each, and
// copies their pull requests into the forge replica, then checks the
// replica against GitHub's own count (ADR 0013). Archived repositories and
// forks of an organization are left out unless asked for, and empty
// repositories always are.
//
// Each repository is a bulkhead (Michael Nygard, Release It!): its failure
// is logged and recorded as a partial run, and the others continue. The
// sync is recorded as a run of kind sync, so its outcome is kept like a
// scan's. Sync returns domain.ErrNoFacts when no repository synced, and a
// summary whenever it began its run, an interrupted one included.
func Sync(ctx context.Context, d Deps, r SyncRequest) (*SyncSummary, error) {
	if d.Forge == nil || d.ForgeStore == nil || d.Store == nil {
		return nil, errors.New("app.Sync: Deps needs Forge, ForgeStore and Store")
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	began := time.Now()
	runID, err := d.Store.BeginRun(ctx, domain.Run{
		Kind:        domain.RunKindSync,
		Status:      domain.RunStatusRunning,
		Now:         r.Now,
		Started:     r.Now,
		ToolVersion: d.Version,
		Options:     r.options(),
	})
	if err != nil {
		return nil, fmt.Errorf("sync: recording the run: %w", err)
	}
	sum := &SyncSummary{}
	status, err := syncRepos(ctx, d, r, sum, log)
	sum.Took = time.Since(began)
	// The run is finished even after a cancellation, so that it does not
	// stay running until its lease lapses, but within finishTimeout.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if ferr := d.Store.FinishRun(fctx, runID, status); ferr != nil {
		err = errors.Join(err, fmt.Errorf("sync: finishing run %d: %w", runID, ferr))
	}
	return sum, err
}

// options are the effective options a sync run records. They hold no
// secret: the token never reaches the use case, and every value is
// scrubbed (redact.Scrub), as a scan's are, so a token pasted into --org,
// --repo or --mirrors, such as a remote URL given where a name or a
// directory belongs, reaches neither facts.db nor a report (#71).
func (r SyncRequest) options() map[string]string {
	opts := map[string]string{
		"org":              r.Org,
		"mirrors":          r.Mirrors,
		"include_archived": strconv.FormatBool(r.IncludeArchived),
		"include_forks":    strconv.FormatBool(r.IncludeForks),
	}
	if len(r.Repos) > 0 {
		opts["repos"] = strings.Join(r.Repos, ",")
	}
	if !r.Since.IsZero() {
		opts["since"] = r.Since.UTC().Format(time.RFC3339)
	}
	for k, v := range opts {
		opts[k] = redact.Scrub(v)
	}
	return opts
}

// syncRepos syncs every selected repository into sum and returns the run's
// status. Its messages and log lines quote the organization, the
// repositories and the mirror paths scrubbed: the command line refuses a
// name that is no GitHub name, but the mirrors directory may be any path
// the user typed.
func syncRepos(ctx context.Context, d Deps, r SyncRequest, sum *SyncSummary, log *slog.Logger) (domain.RunStatus, error) {
	listed, owner, err := syncListing(ctx, d, r)
	what := redact.Scrub(r.Org)
	if owner == "" {
		what = redact.Scrub(strings.Join(r.Repos, ", "))
	}
	if err != nil {
		return domain.RunStatusFailed, err
	}
	repos, left := selectRepos(listed, r, owner == "")
	log.Info("sync github", "of", what, "listed", len(listed), "syncing", len(repos),
		"skipped_archived", left.count(leftArchived), "skipped_forks", left.count(leftFork), "skipped_empty", left.count(leftEmpty))
	// The mirrors follow the repositories this sync mirrors, before any
	// fetch, so an archived repository's mirror leaves scan's way even when
	// nothing else is left to sync (#110).
	unmirrored := reconcileMirrors(ctx, d, r, owner, repos, left, log)
	if len(repos) == 0 {
		return domain.RunStatusFailed, fmt.Errorf(
			"%s has no repository to sync (%d archived, %d forks, %d empty; see --include-archived and --include-forks): %w",
			what, left.count(leftArchived), left.count(leftFork), left.count(leftEmpty), domain.ErrNoFacts)
	}
	sum.Repos = len(repos)
	if err := d.ForgeStore.PutForgeRepos(ctx, repos); err != nil {
		return domain.RunStatusFailed, fmt.Errorf("storing the repositories of %s: %w", what, err)
	}

	results := make([]repoSync, len(repos))
	var g errgroup.Group
	g.SetLimit(syncJobs)
	for i, repo := range repos {
		g.Go(func() error {
			at := syncPlace{repo: repoName(repo), of: fmt.Sprintf("%d of %d", i+1, len(repos))}
			results[i] = syncRepo(ctx, d, r, repo, unmirrored[repo.ID], at, log)
			return nil // a bulkhead: one repository's failure stops no other
		})
	}
	_ = g.Wait()
	for i, res := range results {
		sum.Pulls += res.pulls
		switch {
		case res.err != nil && ctx.Err() != nil:
			// Stopped by the interruption, not failed: the next sync reads
			// its range again.
		case res.err != nil:
			sum.Failed++
			log.Warn("sync failed", "repo", repoName(repos[i]), "error", redact.Scrub(res.err.Error()))
		case !res.checked:
			sum.Synced++
			sum.Unchecked++
		default:
			sum.Synced++
			if res.missing > 0 {
				sum.Incomplete++
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.RunStatusFailed, err
	}
	switch {
	case sum.Failed == 0:
		return domain.RunStatusComplete, nil
	case sum.Failed < len(repos):
		log.Warn("sync partial", "of", what, "failed", sum.Failed, "synced", sum.Synced)
		return domain.RunStatusPartial, nil
	default:
		return domain.RunStatusFailed, fmt.Errorf("no repository of %s synced: %w", what, domain.ErrNoFacts)
	}
}

// syncListing lists what the request names: an organization's whole
// listing, with the organization as the owner whose mirrors follow it, or
// each repository --repo names, looked up one by one, with no owner, so no
// other mirror moves (ForgeSource.ReconcileMirrors). A repository named twice
// is synced once. A name the forge does not know fails the sync before it
// does anything else: it is the user's typo, not a repository's failure.
func syncListing(ctx context.Context, d Deps, r SyncRequest) ([]domain.ForgeRepo, string, error) {
	if len(r.Repos) == 0 {
		listed, err := d.Forge.Repos(ctx, r.Org)
		if err != nil {
			return nil, r.Org, fmt.Errorf("listing the repositories of %s: %w", redact.Scrub(r.Org), err)
		}
		return listed, r.Org, nil
	}
	var listed []domain.ForgeRepo
	for _, name := range r.Repos {
		owner, repo, ok := strings.Cut(name, "/")
		if !ok {
			return nil, "", &domain.UsageError{Msg: fmt.Sprintf("invalid repository %q: want OWNER/NAME", redact.Scrub(name))}
		}
		fr, err := d.Forge.Repo(ctx, owner, repo)
		if err != nil {
			return nil, "", fmt.Errorf("looking up %s: %w", redact.Scrub(name), err)
		}
		if !slices.ContainsFunc(listed, func(x domain.ForgeRepo) bool { return x.ID == fr.ID }) {
			listed = append(listed, fr)
		}
	}
	slices.SortFunc(listed, func(a, b domain.ForgeRepo) int { return strings.Compare(a.Key, b.Key) })
	return listed, "", nil
}

// syncPlace names a repository in a sync's progress lines: "acme/widget",
// and where it stands in the sync's order of repositories, "3 of 80".
type syncPlace struct{ repo, of string }

// Why a sync leaves a listed repository out.
const (
	leftEmpty    = "empty"    // no default branch to mirror and no pull request
	leftArchived = "archived" // see --include-archived
	leftFork     = "fork"     // see --include-forks
)

// leftOut says, by ForgeRepo.ID, why a sync leaves each listed repository
// out that it does.
type leftOut map[string]string

func (l leftOut) count(reason string) int {
	n := 0
	for _, r := range l {
		if r == reason {
			n++
		}
	}
	return n
}

// selectRepos keeps the repositories the request asks for, in order. An
// empty repository has no default branch to mirror and no pull request. A
// repository named with --repo (named) is kept archived or a fork: naming
// it asks for it.
func selectRepos(listed []domain.ForgeRepo, r SyncRequest, named bool) ([]domain.ForgeRepo, leftOut) {
	var keep []domain.ForgeRepo
	left := leftOut{}
	for _, repo := range listed {
		switch {
		case repo.DefaultBranch == "":
			left[repo.ID] = leftEmpty
		case repo.Archived && !r.IncludeArchived && !named:
			left[repo.ID] = leftArchived
		case repo.Fork && !r.IncludeForks && !named:
			left[repo.ID] = leftFork
		default:
			keep = append(keep, repo)
		}
	}
	return keep, left
}

// reconcileMirrors moves the mirrors after the repositories the sync
// mirrors, by node id, before any is fetched (ForgeSource.ReconcileMirrors),
// and logs each move: a renamed repository's to its new path, and, under an
// organization's directory, the mirror of a repository it no longer lists
// or that the sync leaves out (an archived one, #110) to <mirrors>/.stale,
// where scan never looks. It returns, by ForgeRepo.ID, why a repository
// must not be mirrored this sync: its existing mirror is somewhere it could
// not be moved from, and a clone at its path would be a second copy, which
// scan would count twice. When the mirrors cannot be taken stock of at all,
// no repository is mirrored, for the same reason; pull requests still sync.
func reconcileMirrors(ctx context.Context, d Deps, r SyncRequest, owner string, mirrored []domain.ForgeRepo,
	left leftOut, log *slog.Logger) map[string]error {
	moves, err := d.Forge.ReconcileMirrors(ctx, owner, mirrored, r.Mirrors)
	if err != nil {
		err = fmt.Errorf("taking stock of the mirrors: %w", err)
		unmirrored := make(map[string]error, len(mirrored))
		for _, repo := range mirrored {
			unmirrored[repo.ID] = err
		}
		return unmirrored
	}
	var unmirrored map[string]error
	for _, m := range moves {
		from, to := redact.Scrub(m.From), redact.Scrub(m.To)
		attrs := []any{"reason", m.Reason, "from", from, "to", to}
		if why, ok := left[m.Repo]; ok && m.Reason == domain.MirrorStale {
			attrs[1] = why + " (left out: see --include-archived and --include-forks)"
		}
		switch {
		case m.Err == "" && (m.Reason == domain.MirrorRenamed || m.Reason == domain.MirrorRestored):
			log.Info("mirror moved", attrs...)
		case m.Err == "":
			log.Warn("mirror moved out of scan's way", attrs...)
		default:
			log.Warn("mirror not moved", append(attrs, "error", redact.Scrub(m.Err))...)
			if m.Reason == domain.MirrorRenamed || m.Reason == domain.MirrorRestored {
				if unmirrored == nil {
					unmirrored = map[string]error{}
				}
				unmirrored[m.Repo] = fmt.Errorf("its mirror at %s could not move to %s: %s", from, to, redact.Scrub(m.Err))
			}
		}
	}
	return unmirrored
}

// repoSync is how one repository's sync went.
type repoSync struct {
	pulls   int   // pull requests stored
	missing int   // the completeness check's shortfall; 0 when it matched or did not run
	checked bool  // the completeness check ran
	github  int   // the check's counts of the window's pull requests: GitHub's
	replica int   // and the replica's
	err     error // the mirror's or the pull requests' failure
}

// syncRepo brings one repository's mirror and pull requests up to date. The
// two are independent: a mirror that fails to fetch, or that unmirrored
// says to leave alone, does not stop the pull requests, and the other way
// round.
func syncRepo(ctx context.Context, d Deps, r SyncRequest, repo domain.ForgeRepo, unmirrored error, at syncPlace, log *slog.Logger) repoSync {
	started := time.Now()
	var errs []error
	path := ""
	if unmirrored != nil {
		errs = append(errs, fmt.Errorf("mirror: %w", unmirrored))
	} else if p, err := d.Forge.Mirror(ctx, repo, r.Mirrors); err != nil {
		errs = append(errs, fmt.Errorf("mirror: %w", err))
	} else {
		path = p
	}
	if ctx.Err() != nil {
		return repoSync{err: errors.Join(append(errs, ctx.Err())...)}
	}
	res, err := syncPullRequests(ctx, d, r, repo, at, log)
	if err != nil {
		errs = append(errs, fmt.Errorf("pull requests: %w", err))
	}
	res.err = errors.Join(errs...)
	if res.err == nil {
		attrs := []any{"repo", at.repo, "repos", at.of, "mirror", redact.Scrub(path), "pull_requests", res.pulls}
		if res.checked {
			// The completeness check's two counts of the window's pull
			// requests: a replica that holds at least GitHub's is complete.
			attrs = append(attrs, "window_github", res.github, "window_replica", res.replica)
		}
		log.Info("synced", append(attrs, "took", time.Since(started).Round(time.Millisecond))...)
	}
	return res
}

// syncPullRequests brings the repository's replica up to date in bounded
// passes and checks it against GitHub's own count (ADR 0013):
//
//   - A full pass reads every pull request updated in the history window,
//     the scanHistoryDays before the sync's start (or since --since, when
//     that is earlier): the first sync of a repository, the one after a
//     check that found pull requests missing, and one whose --since
//     reaches further back than the replica. Any other pass reads down to
//     the watermark, the start of the last completed pass.
//   - The adapter reads the range newest first, tolerating GitHub's order,
//     and only a pass that read its whole range carries a watermark, its
//     start on GitHub's clock. One that stops halfway, interrupted or
//     failed, leaves the old one, so the next sync reads the same range
//     again: a rerun costs a bounded time and leaves no hole.
//   - The check then compares GitHub's count of the window's pull requests
//     with the replica's (reconciliation, an anti-entropy check: Kleppmann,
//     Designing Data-Intensive Applications, ch. 5). A shortfall after a
//     pass that read down to the watermark means a hole below it, as an
//     earlier stillhere left (#101): a full pass follows at once, and the
//     check again. A shortfall that remains is recorded, which the pages of
//     a run that reads the replica caveat, and the next sync reads the
//     whole window again.
//
// A check that cannot run leaves the replica's last check as it was and
// marks the repository synced: the passes completed, and the next sync
// checks again.
func syncPullRequests(ctx context.Context, d Deps, r SyncRequest, repo domain.ForgeRepo, at syncPlace, log *slog.Logger) (repoSync, error) {
	var res repoSync
	wm, err := d.ForgeStore.ForgeWatermark(ctx, repo.ID)
	if err != nil {
		return res, err
	}
	window := r.Now.AddDate(0, 0, -scanHistoryDays)
	floor := window
	if !r.Since.IsZero() && r.Since.Before(floor) {
		floor = r.Since
	}
	from, since, kind := wm.From, wm.Updated, "incremental"
	switch {
	case wm.Synced.IsZero() || wm.Updated.IsZero():
		kind = "first"
	case wm.Missing > 0:
		kind = "repair"
	case floor.Before(wm.From):
		kind = "since"
	}
	if kind != "incremental" {
		from, since = floor, floor
	}

	pass := func(since time.Time, kind string) (time.Time, error) {
		if kind != "incremental" {
			// A full pass of a large repository takes minutes: say so as it
			// starts. A pass down to the watermark reads a page or two.
			log.Info("reading pull requests", "repo", at.repo, "repos", at.of, "pass", kind, "since", since.UTC().Format(time.DateOnly))
		}
		var start time.Time
		pages := 0
		err := d.Forge.PullRequests(ctx, repo, since, func(p *domain.ForgePage) error {
			if err := d.ForgeStore.PutForgePage(ctx, p); err != nil {
				return err
			}
			res.pulls += len(p.PullRequests)
			pages++
			if !p.Updated.IsZero() {
				start = p.Updated
			}
			if pages%syncProgressPages == 0 {
				log.Info("reading pull requests", "repo", at.repo, "repos", at.of, "pages", pages, "stored", res.pulls,
					"reached", oldestUpdate(p.PullRequests), "down_to", since.UTC().Format(time.DateOnly))
			}
			return nil
		})
		if err == nil && start.IsZero() {
			err = errors.New("the forge ended a pass without its watermark")
		}
		return start, err
	}
	// check counts the window's pull requests on both sides: GitHub's,
	// settled before the pass began, and the replica's, which holds any it
	// read since at their newer update too. A complete replica holds at
	// least as many; GitHub's count leaves out the few its search hides.
	check := func(from, start time.Time) (missing int, err error) {
		if from.Before(window) {
			from = window
		}
		if res.github, err = d.Forge.CountPullRequests(ctx, repo, from, start); err != nil {
			return 0, err
		}
		if res.replica, err = d.ForgeStore.CountForgePulls(ctx, repo.ID, from); err != nil {
			return 0, err
		}
		return max(0, res.github-res.replica), nil
	}

	start, err := pass(since, kind)
	if err != nil {
		return res, err
	}
	missing, err := check(from, start)
	if err == nil && missing > 0 && since.After(floor) {
		log.Warn("GitHub counts pull requests in the window that the replica lacks; reading the whole window again",
			"repo", at.repo, "missing", missing)
		from = floor
		if start, err = pass(floor, "repair"); err != nil {
			return res, err
		}
		missing, err = check(from, start)
	}
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		log.Warn("completeness check failed; the next sync checks again", "repo", at.repo, "error", redact.Scrub(err.Error()))
		return res, d.ForgeStore.MarkForgeSynced(ctx, repo.ID, r.Now)
	}
	res.checked, res.missing = true, missing
	if missing > 0 {
		log.Warn("GitHub data incomplete: GitHub counts pull requests in the window that the replica lacks; scans say so, and the next sync reads the window again",
			"repo", at.repo, "missing", missing)
	}
	return res, d.ForgeStore.MarkForgeChecked(ctx, repo.ID, r.Now, from, missing)
}

// oldestUpdate is the day of the oldest update among pulls, how far down a
// pass has read, for its progress lines; "" for none.
func oldestUpdate(pulls []domain.PullRequest) string {
	var oldest time.Time
	for _, p := range pulls {
		if oldest.IsZero() || p.Updated.Before(oldest) {
			oldest = p.Updated
		}
	}
	if oldest.IsZero() {
		return ""
	}
	return oldest.UTC().Format(time.DateOnly)
}

func repoName(r domain.ForgeRepo) string { return r.Owner + "/" + r.Name }
