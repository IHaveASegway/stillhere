package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// SyncRequest asks to bring an organization's mirrors and forge facts up to
// date: the only use case that touches the network (ADR 0004).
type SyncRequest struct {
	Org             string // GitHub organization (or user) login
	Mirrors         string // absolute directory of the bare mirrors, one <owner>/<repo>.git each
	IncludeArchived bool
	IncludeForks    bool
	// Now is when the sync started, read once by the command line. It
	// stamps the sync run and each repository's ForgeWatermark.Synced, the
	// "as of" time of its forge facts.
	Now time.Time
}

// syncJobs bounds how many repositories sync at once. Their mirrors fetch
// in parallel, which is where a sync spends most of its time; the forge
// adapter sends API requests one at a time whatever this is, as GitHub asks.
const syncJobs = 4

// Sync lists the organization's repositories, keeps a bare mirror of each,
// and copies the pull requests updated since the last sync into the forge
// replica. Archived repositories and forks are left out unless asked for,
// and empty repositories always are.
//
// Each repository is a bulkhead (Michael Nygard, Release It!): its failure
// is logged and recorded as a partial run, and the others continue. The
// sync is recorded as a run of kind sync, so its outcome is kept like a
// scan's. Sync returns domain.ErrNoFacts when no repository synced.
func Sync(ctx context.Context, d Deps, r SyncRequest) error {
	if d.Forge == nil || d.ForgeStore == nil || d.Store == nil {
		return errors.New("app.Sync: Deps needs Forge, ForgeStore and Store")
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	runID, err := d.Store.BeginRun(ctx, domain.Run{
		Kind:        domain.RunKindSync,
		Status:      domain.RunStatusRunning,
		Now:         r.Now,
		Started:     r.Now,
		ToolVersion: d.Version,
		Options:     r.options(),
	})
	if err != nil {
		return fmt.Errorf("sync: recording the run: %w", err)
	}
	status, err := syncOrg(ctx, d, r, log)
	// The run is finished even after a cancellation, so that it does not
	// stay running until its lease lapses, but within finishTimeout.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if ferr := d.Store.FinishRun(fctx, runID, status); ferr != nil {
		err = errors.Join(err, fmt.Errorf("sync: finishing run %d: %w", runID, ferr))
	}
	return err
}

// options are the effective options a sync run records. They hold no
// secret: the token never reaches the use case.
func (r SyncRequest) options() map[string]string {
	return map[string]string{
		"org":              r.Org,
		"mirrors":          r.Mirrors,
		"include_archived": strconv.FormatBool(r.IncludeArchived),
		"include_forks":    strconv.FormatBool(r.IncludeForks),
	}
}

// syncOrg syncs every selected repository and returns the run's status.
func syncOrg(ctx context.Context, d Deps, r SyncRequest, log *slog.Logger) (domain.RunStatus, error) {
	listed, err := d.Forge.Repos(ctx, r.Org)
	if err != nil {
		return domain.RunStatusFailed, fmt.Errorf("listing the repositories of %s: %w", r.Org, err)
	}
	repos, skipped := selectRepos(listed, r)
	log.Info("sync github", "org", r.Org, "listed", len(listed), "syncing", len(repos),
		"skipped_archived", skipped.archived, "skipped_forks", skipped.forks, "skipped_empty", skipped.empty)
	if len(repos) == 0 {
		return domain.RunStatusFailed, fmt.Errorf(
			"%s has no repository to sync (%d archived, %d forks, %d empty; see --include-archived and --include-forks): %w",
			r.Org, skipped.archived, skipped.forks, skipped.empty, domain.ErrNoFacts)
	}
	if err := d.ForgeStore.PutForgeRepos(ctx, repos); err != nil {
		return domain.RunStatusFailed, fmt.Errorf("storing the repositories of %s: %w", r.Org, err)
	}
	unmirrored := reconcileMirrors(ctx, d, r, listed, log)

	errs := make([]error, len(repos))
	var g errgroup.Group
	g.SetLimit(syncJobs)
	for i, repo := range repos {
		g.Go(func() error {
			errs[i] = syncRepo(ctx, d, r, repo, unmirrored[repo.ID], log)
			return nil // a bulkhead: one repository's failure stops no other
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return domain.RunStatusFailed, err
	}

	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			log.Warn("sync failed", "repo", repoName(repos[i]), "error", err)
		}
	}
	switch {
	case failed == 0:
		return domain.RunStatusComplete, nil
	case failed < len(repos):
		log.Warn("sync partial", "org", r.Org, "failed", failed, "synced", len(repos)-failed)
		return domain.RunStatusPartial, nil
	default:
		return domain.RunStatusFailed, fmt.Errorf("no repository of %s synced: %w", r.Org, domain.ErrNoFacts)
	}
}

// skips counts the repositories a sync leaves out, by reason.
type skips struct{ archived, forks, empty int }

// selectRepos keeps the repositories the request asks for, in order. An
// empty repository has no default branch to mirror and no pull request.
func selectRepos(listed []domain.ForgeRepo, r SyncRequest) ([]domain.ForgeRepo, skips) {
	var keep []domain.ForgeRepo
	var s skips
	for _, repo := range listed {
		switch {
		case repo.DefaultBranch == "":
			s.empty++
		case repo.Archived && !r.IncludeArchived:
			s.archived++
		case repo.Fork && !r.IncludeForks:
			s.forks++
		default:
			keep = append(keep, repo)
		}
	}
	return keep, s
}

// reconcileMirrors moves the mirrors after their repositories, by node id,
// before any is fetched (ForgeSource.ReconcileMirrors), and logs each move.
// It returns, by ForgeRepo.ID, why a repository must not be mirrored this
// sync: its existing mirror is somewhere it could not be moved from, and a
// clone at its path would be a second copy, which scan would count twice.
// When the mirrors cannot be taken stock of at all, no repository is
// mirrored, for the same reason; pull requests still sync.
func reconcileMirrors(ctx context.Context, d Deps, r SyncRequest, listed []domain.ForgeRepo, log *slog.Logger) map[string]error {
	moves, err := d.Forge.ReconcileMirrors(ctx, r.Org, listed, r.Mirrors)
	if err != nil {
		err = fmt.Errorf("taking stock of the mirrors: %w", err)
		unmirrored := make(map[string]error, len(listed))
		for _, repo := range listed {
			unmirrored[repo.ID] = err
		}
		return unmirrored
	}
	var unmirrored map[string]error
	for _, m := range moves {
		attrs := []any{"reason", m.Reason, "from", m.From, "to", m.To}
		switch {
		case m.Err == "" && (m.Reason == domain.MirrorRenamed || m.Reason == domain.MirrorRestored):
			log.Info("mirror moved", attrs...)
		case m.Err == "":
			log.Warn("mirror moved out of scan's way", attrs...)
		default:
			log.Warn("mirror not moved", append(attrs, "error", m.Err)...)
			if m.Reason == domain.MirrorRenamed || m.Reason == domain.MirrorRestored {
				if unmirrored == nil {
					unmirrored = map[string]error{}
				}
				unmirrored[m.Repo] = fmt.Errorf("its mirror at %s could not move to %s: %s", m.From, m.To, m.Err)
			}
		}
	}
	return unmirrored
}

// syncRepo brings one repository's mirror and pull requests up to date. The
// two are independent: a mirror that fails to fetch, or that unmirrored
// says to leave alone, does not stop the pull requests, and the other way
// round.
func syncRepo(ctx context.Context, d Deps, r SyncRequest, repo domain.ForgeRepo, unmirrored error, log *slog.Logger) error {
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
		return errors.Join(append(errs, ctx.Err())...)
	}
	pulls, err := syncPullRequests(ctx, d, r, repo)
	if err != nil {
		errs = append(errs, fmt.Errorf("pull requests: %w", err))
	}
	if len(errs) == 0 {
		log.Info("synced", "repo", repoName(repo), "mirror", path, "pull_requests", pulls,
			"took", time.Since(started).Round(time.Millisecond))
	}
	return errors.Join(errs...)
}

// syncPullRequests copies the repository's pull requests updated since its
// watermark, one stored page at a time, and returns how many it stored. A
// sync that stops halfway resumes from the last page stored.
func syncPullRequests(ctx context.Context, d Deps, r SyncRequest, repo domain.ForgeRepo) (int, error) {
	wm, err := d.ForgeStore.ForgeWatermark(ctx, repo.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	err = d.Forge.PullRequests(ctx, repo, wm.Updated, func(p *domain.ForgePage) error {
		n += len(p.PullRequests)
		return d.ForgeStore.PutForgePage(ctx, p)
	})
	if err != nil {
		return n, err
	}
	return n, d.ForgeStore.MarkForgeSynced(ctx, repo.ID, r.Now)
}

func repoName(r domain.ForgeRepo) string { return r.Owner + "/" + r.Name }
