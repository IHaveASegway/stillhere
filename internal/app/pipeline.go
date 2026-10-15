package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"runtime"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// This file is what week and scan share: discovery, --exclude-repo, and the
// collection of each repository's facts. Every repository is a bulkhead
// (Michael Nygard, Release It!, "Bulkheads"): its failure is recorded on its
// own RepoFacts and the run goes on. Three things stop a run instead: a
// cancellation, which the user asked for; a usage error, which is the
// user's to fix; and a collector that is not built yet (see runStopped).

// The warning codes this package records.
const (
	warnExcluded   = "excluded-repo" // a repository --exclude-repo left out
	warnPathFailed = "path-failed"   // a PATH that gave no repository: neither a repository nor a group, or one git cannot read
)

// blameTimeout bounds one git blame (docs/spec/git-invocation.md).
const blameTimeout = 120 * time.Second

// scanHistoryDays is how far back a scan collects landings: the durable@1
// cohort reaches back 365 days.
const scanHistoryDays = 365

// defaultJobs is --jobs when it is 0: one blame per CPU, at most 12, which
// the proof run found keeps a large machine from thrashing
// (docs/spec/blame-collector.md#parallelism-errors-budget).
func defaultJobs() int { return min(12, runtime.NumCPU()) }

// plan is what a run will collect: the repositories discovery found, less
// the ones --exclude-repo left out.
type plan struct {
	jobs     []repoJob
	groups   []domain.Group   // members limited to the repositories kept
	warnings []domain.Warning // run-level: excluded repositories, failed paths, discovery's own
	// paths are the PATH arguments as discovery resolved them: each group's
	// directory and each repository named directly, absolute and sorted. A
	// run records these rather than the arguments as typed, which depend on
	// the directory the command ran in and may be a URL with a token in it;
	// a PATH that gave no repository is a path-failed warning instead.
	paths []string
}

// repoJob is one repository to collect, with the warnings discovery
// recorded about it.
type repoJob struct {
	repo     domain.Repo
	warnings []domain.Warning
}

// discover expands the command-line paths into repositories and plans the
// run over them.
func discover(ctx context.Context, d Deps, paths, exclude []string) (*plan, error) {
	for _, g := range exclude {
		if _, err := path.Match(g, ""); err != nil {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("invalid --exclude-repo %q: %v", g, err)}
		}
	}
	disc, err := collect.Discover(ctx, d.Git, paths)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	pl := planRun(disc, exclude)
	d.Log.Info("discovered repositories", "repos", len(pl.jobs), "groups", len(pl.groups),
		"excluded", len(disc.Repos)-len(pl.jobs), "failed_paths", len(disc.Failed))
	return pl, nil
}

// planRun applies --exclude-repo to the names of the repositories discovery
// found, records each one it leaves out as a run-level warning, and routes
// discovery's warnings to the repositories they concern: a warning about a
// repository the run leaves out goes with it.
//
// Every message it composes is scrubbed (redact.Message, redact.Scrub): a
// failed path is the argument as discovery made it absolute, and a URL
// given as a PATH, token included, becomes "/cwd/https:/user:TOKEN@host".
func planRun(disc *collect.Discovery, exclude []string) *plan {
	pl := &plan{}
	kept := map[string]bool{}
	excluded := map[string]bool{}
	for _, r := range disc.Repos {
		if r.Group == "" {
			pl.paths = append(pl.paths, r.Path)
		}
		if glob, ok := matchGlob(exclude, r.Name); ok {
			excluded[r.Key] = true
			pl.warnings = append(pl.warnings, domain.Warning{
				Code:    warnExcluded,
				Message: redact.Scrub(fmt.Sprintf("left out %s (%s): its name matches --exclude-repo %q", r.Name, r.Path, glob)),
			})
			continue
		}
		kept[r.Key] = true
		pl.jobs = append(pl.jobs, repoJob{repo: r})
	}
	byKey := map[string]int{}
	for i, j := range pl.jobs {
		byKey[j.repo.Key] = i
	}
	for _, w := range disc.Warnings {
		switch {
		case w.Repo != "" && kept[w.Repo]:
			i := byKey[w.Repo]
			pl.jobs[i].warnings = append(pl.jobs[i].warnings, w)
		case w.Repo != "" && excluded[w.Repo]:
			// About a repository the run leaves out.
		default:
			w.Repo = ""
			pl.warnings = append(pl.warnings, w)
		}
	}
	for _, f := range disc.Failed {
		pl.warnings = append(pl.warnings, domain.Warning{
			Code:    warnPathFailed,
			Message: redact.Message(f.Path + ": " + f.Message),
		})
	}
	for _, g := range disc.Groups {
		pl.paths = append(pl.paths, g.Path)
		var keys []string
		for _, k := range g.Repos {
			if kept[k] {
				keys = append(keys, k)
			}
		}
		g.Repos = keys
		pl.groups = append(pl.groups, g)
	}
	slices.Sort(pl.paths)
	pl.paths = slices.Compact(pl.paths)
	sortWarnings(pl.warnings)
	return pl
}

// matchGlob returns the first glob that matches name.
func matchGlob(globs []string, name string) (string, bool) {
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return g, true
		}
	}
	return "", false
}

// collectOptions says how much to collect about each repository.
type collectOptions struct {
	mode           collect.HistoryMode
	window         domain.Window
	ignoreRevsFile string
	slots          *collect.BlameSlots // scan only: shared by every repository's blame
}

// repoResult is one repository's facts and how long collecting them took.
type repoResult struct {
	facts  domain.RepoFacts
	timing report.RepoTiming
}

// collectAll collects every repository with each, at most limit at a time,
// and returns the results in the order of jobs. each records a repository's
// own failure in its result; an error from each stops the run, and
// collectAll returns the first one once every running each has returned.
func collectAll(ctx context.Context, jobs []repoJob, limit int,
	each func(context.Context, repoJob) (repoResult, error)) ([]repoResult, error) {
	results := make([]repoResult, len(jobs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(limit, 1))
	for i, job := range jobs {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			res, err := each(gctx, job)
			if err != nil {
				return err
			}
			results[i] = res
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("collecting repositories: %w", err)
	}
	return results, nil
}

// collector returns the per-repository collection for collectAll.
func collector(d Deps, opt collectOptions) func(context.Context, repoJob) (repoResult, error) {
	return func(ctx context.Context, job repoJob) (repoResult, error) {
		return collectRepo(ctx, d, job, opt)
	}
}

// collectRepo collects one repository: its history, then the class of every
// path (the history says which paths no longer at HEAD it touched), then,
// for a scan of a clone that has its blobs and its whole history, blame.
// A failure at any stage becomes the repository's error, and none of its
// facts are kept: a repository is either collected or failed, never half.
func collectRepo(ctx context.Context, d Deps, job repoJob, opt collectOptions) (repoResult, error) {
	repo := job.repo
	t := report.RepoTiming{Repo: repo.Name, Key: repo.Key}
	fail := func(stage string, err error) (repoResult, error) {
		if stop := runStopped(ctx, err); stop != nil {
			return repoResult{}, stop
		}
		msg := redact.Message(stage + ": " + err.Error())
		d.Log.Warn("repository failed", "repo", repo.Name, "error", msg)
		return repoResult{facts: domain.RepoFacts{
			Repo: repo, Status: domain.RepoStatusError, Error: msg,
			Warnings: repoWarnings(repo, job.warnings, nil), History: opt.window,
		}, timing: t}, nil
	}

	start := time.Now()
	h, err := collect.History(ctx, d.Git, repo, collect.HistoryOptions{Window: opt.window, Mode: opt.mode})
	t.HistoryMS = time.Since(start).Milliseconds()
	if err != nil {
		return fail("history", err)
	}
	rf := domain.RepoFacts{
		Repo: repo, Status: domain.RepoStatusOK,
		Warnings:    repoWarnings(repo, job.warnings, h.Warnings),
		FirstCommit: h.FirstCommit, History: opt.window,
		Commits: h.Commits, Landings: h.Landings, Changes: h.Changes, Reverts: h.Reverts,
	}
	// A partial clone is classified too: its tree lists every path at HEAD
	// with lazy fetch off, and where its blobs are absent, ClassifyPaths
	// reads sizes as unknown and finds no attributes or generated headers,
	// which the partial-clone caveat says. Its history touched no paths: it
	// has no line facts.
	start = time.Now()
	paths, err := collect.ClassifyPaths(ctx, d.Git, repo, d.Policy, touchedPaths(h))
	t.ClassifyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fail("classification", err)
	}
	rf.Paths = paths
	if opt.mode == collect.HistoryScan {
		// Blame credits every line older than a shallow boundary to the
		// boundary commit, and a partial clone has no blobs to blame, so
		// neither gets line facts. Blame still runs for them with no paths:
		// that resolves only the ignore-revs list, which durable@1 and
		// simplifying@1 exclude whether or not lines were blamed, and turns
		// a bad --ignore-revs-file into a usage error
		// (collect.BlameOptions.Paths).
		lineFacts := !repo.Shallow && !repo.Partial
		bopt := collect.BlameOptions{
			IgnoreRevsFile: opt.ignoreRevsFile,
			Slots:          opt.slots,
			Timeout:        blameTimeout,
			Progress:       blameProgress(d.Log, repo.Name),
		}
		if lineFacts {
			bopt.Paths = blamePaths(rf.Paths)
		}
		start = time.Now()
		bf, err := collect.Blame(ctx, d.Git, repo, bopt)
		t.BlameMS = time.Since(start).Milliseconds()
		if err != nil {
			return fail("blame", err)
		}
		rf.LineFacts = lineFacts
		rf.Owners, rf.IgnoreRevs, rf.BlameErrors = bf.Owners, bf.IgnoreRevs, bf.Errors
		rf.Warnings = repoWarnings(repo, rf.Warnings, bf.Warnings)
		for i, e := range bf.Errors {
			if i == 8 {
				d.Log.Warn("more blame failures", "repo", repo.Name, "files", len(bf.Errors)-8)
				break
			}
			d.Log.Warn("blame failed", "repo", repo.Name, "path", e.Path, "error", e.Message)
		}
	}
	d.Log.Info("collected", "repo", repo.Name, "landings", len(rf.Landings), "paths", len(rf.Paths),
		"blamed", rf.LineFacts, "ms", t.HistoryMS+t.ClassifyMS+t.BlameMS)
	return repoResult{facts: rf, timing: t}, nil
}

// runStopped returns the error that stops the whole run, or nil when err is
// the repository's own failure. A cancellation stops the run, while a
// timeout, which only one git command hit, is the repository's failure. A
// usage error, such as an unreadable --ignore-revs-file, is the user's to
// fix and exits 2 rather than failing the repository. A collector that is
// not built yet stops the run too, so the command still exits 2 rather than
// reporting every repository as failed (domain.ErrNotImplemented goes with
// the last stub, #13).
func runStopped(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("collecting repositories: %w", context.Cause(ctx))
	}
	var usage *domain.UsageError
	if errors.As(err, &usage) || errors.Is(err, domain.ErrNotImplemented) {
		return err
	}
	return nil
}

// repoWarnings merges a repository's discovery and history warnings, each
// naming the repository, sorted.
func repoWarnings(repo domain.Repo, lists ...[]domain.Warning) []domain.Warning {
	var out []domain.Warning
	for _, ws := range lists {
		for _, w := range ws {
			w.Repo = repo.Key
			out = append(out, w)
		}
	}
	sortWarnings(out)
	return out
}

func sortWarnings(ws []domain.Warning) {
	slices.SortFunc(ws, func(a, b domain.Warning) int {
		return cmp.Or(cmp.Compare(a.Code, b.Code), cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Message, b.Message))
	})
}

// touchedPaths lists each path the history touched, at the newest commit
// that added lines to it (else the newest that touched it), which is a
// commit whose tree holds the path: classification sizes a path HEAD no
// longer has there (collect.ClassifyPaths).
func touchedPaths(h *collect.HistoryFacts) []collect.PathAt {
	when := make(map[string]time.Time, len(h.Commits))
	for _, c := range h.Commits {
		when[c.SHA] = c.CommitTime
	}
	type best struct {
		at    collect.PathAt
		added bool
		time  time.Time
	}
	newer := func(a, b best) bool {
		if a.added != b.added {
			return a.added
		}
		if !a.time.Equal(b.time) {
			return a.time.After(b.time)
		}
		return a.at.Commit < b.at.Commit
	}
	byPath := map[string]best{}
	for _, fc := range h.Changes {
		cand := best{at: collect.PathAt{Commit: fc.Commit, Path: fc.Path}, added: fc.Added > 0, time: when[fc.Commit]}
		if cur, ok := byPath[fc.Path]; !ok || newer(cand, cur) {
			byPath[fc.Path] = cand
		}
	}
	var out []collect.PathAt
	for _, b := range byPath {
		out = append(out, b.at)
	}
	slices.SortFunc(out, func(a, b collect.PathAt) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

// blamePaths lists the paths at HEAD that received a role: the ones blame
// reads.
func blamePaths(paths []domain.PathClass) []string {
	var out []string
	for _, pc := range paths {
		if pc.AtHead && pc.Role != "" {
			out = append(out, pc.Path)
		}
	}
	slices.Sort(out)
	return out
}

// blameProgress logs a repository's blame progress every 1,000 files and at
// the end. It is called from the blame workers, and slog is safe for that.
func blameProgress(log *slog.Logger, repo string) func(done, total int) {
	return func(done, total int) {
		if done == total || done%1000 == 0 {
			log.Info("blamed", "repo", repo, "files", done, "of", total)
		}
	}
}

// factSet assembles a run's facts from what discovery planned and the
// collectors found.
func factSet(now time.Time, p *policy.Policy, pl *plan, results []repoResult) *domain.FactSet {
	fs := &domain.FactSet{Now: now, Policy: p.Ref(), Groups: pl.groups, Warnings: pl.warnings}
	for _, r := range results {
		fs.Repos = append(fs.Repos, r.facts)
	}
	slices.SortFunc(fs.Repos, func(a, b domain.RepoFacts) int { return cmp.Compare(a.Repo.Key, b.Repo.Key) })
	slices.SortFunc(fs.Groups, func(a, b domain.Group) int { return cmp.Compare(a.Name, b.Name) })
	return fs
}

// produced reports whether any repository produced facts: the difference
// between exit 0 and exit 1 (docs/spec/report-json.md#exit-codes).
func produced(fs *domain.FactSet) bool {
	for _, rf := range fs.Repos {
		if rf.Status == domain.RepoStatusOK {
			return true
		}
	}
	return false
}

// repoTimings lists the collectors' timings in report order.
func repoTimings(results []repoResult) []report.RepoTiming {
	var out []report.RepoTiming
	for _, r := range results {
		out = append(out, r.timing)
	}
	slices.SortFunc(out, func(a, b report.RepoTiming) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key))
	})
	return out
}
