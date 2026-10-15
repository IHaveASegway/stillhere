package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Scan runs discovery, then history, classification and blame per
// repository (each one a bulkhead, every blame sharing one pool of --jobs
// slots), computes every metric and the ranks asked for, stores it all as
// one run and publishes the run. It returns domain.ErrNoFacts, alongside
// the report, when no repository produced facts; then nothing is stored.
//
// The run is written only after everything is computed, so it is "running"
// for as short a time as possible, and a scan that fails before then leaves
// nothing behind. Readers never see it until FinishRun flips it to complete
// or partial (docs/spec/codebase.md#run-model).
func Scan(ctx context.Context, d Deps, r ScanRequest) (*report.Model, error) {
	d, err := d.ready("app.Scan", true, true, true)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	timings := &report.Timings{Started: started.UTC()}
	scope := r.Scope
	if scope == "" {
		scope = domain.ScopeGroup
	}
	if scope != domain.ScopeGroup && scope != domain.ScopeAll {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("invalid rank scope %q: want group or all", scope)}
	}
	// Resolve --rank before any git process starts: a typo should cost
	// nothing.
	ranked, err := resolveRanked(r.Rank)
	if err != nil {
		return nil, err
	}
	jobs := r.Jobs
	if jobs <= 0 {
		jobs = defaultJobs()
	}

	pl, err := discover(ctx, d, r.Paths, r.Exclude)
	if err != nil {
		return nil, err
	}
	if r.IgnoreRevsFile != "" && len(pl.jobs) > 1 {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"--ignore-revs-file needs a single-repository run, and this one has %d: commit ids belong to one repository", len(pl.jobs))}
	}
	timings.DiscoverMS = time.Since(started).Milliseconds()

	start := time.Now()
	window := domain.Window{Since: r.Now.Add(-scanHistoryDays * 24 * time.Hour), Until: r.Now}
	results, err := collectAll(ctx, pl.jobs, jobs, collector(d, collectOptions{
		mode: collect.HistoryScan, window: window, ignoreRevsFile: r.IgnoreRevsFile,
		// One slot per running git blame, shared by every repository.
		slots: collect.NewBlameSlots(jobs),
	}))
	if err != nil {
		return nil, err
	}
	timings.CollectMS = time.Since(start).Milliseconds()
	timings.Repos = repoTimings(results)

	fs := factSet(r.Now, d.Policy, pl, results)
	who := identity.New(fs, d.Policy)
	landed := report.Landed{Days: d.Policy.Week.Days, Sort: d.Policy.Week.Sort}
	build := func(run *domain.Run, values []domain.MetricValue, sets []report.RankSet) *report.Model {
		finish(timings, started)
		return report.Build(&report.Input{
			Kind: report.KindScan, Tool: d.Version, Run: run, Facts: fs, Policy: d.Policy, Who: who,
			Values: values, Ranks: sets, Landed: landed, History: window, Timings: timings, Scope: scope,
		})
	}
	if !produced(fs) {
		return build(nil, nil, nil), domain.ErrNoFacts
	}

	start = time.Now()
	in := &metrics.Input{Facts: fs, Policy: d.Policy, Who: who, Days: d.Policy.Week.Days}
	values, err := metrics.Compute(in)
	if err != nil {
		return nil, fmt.Errorf("computing metrics: %w", err)
	}
	timings.MetricsMS = time.Since(start).Milliseconds()
	start = time.Now()
	sets, intervals, err := rankAll(in, ranked, rankScopes(scope, fs), nil, nil)
	if err != nil {
		return nil, err
	}
	timings.RankMS = time.Since(start).Milliseconds()

	start = time.Now()
	run := domain.Run{
		Kind: domain.RunKindScan, Status: runStatus(fs), Now: r.Now, Started: started.UTC(),
		ToolVersion: d.Version, Policy: d.Policy.Ref(), PolicySource: d.Policy.Source,
		Options: scanOptions(r, d, pl, jobs, scope, ranked),
	}
	id, err := storeRun(ctx, d.Store, run, fs, values, intervals)
	timings.StoreMS = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		// The analysis is still worth reading: print it, with no run.
		return build(nil, values, sets), err
	}
	run.ID = id
	d.Log.Info("stored run", "run", id, "status", string(run.Status), "repos", len(fs.Repos),
		"values", len(values), "ranks", len(intervals))
	return build(&run, values, sets), nil
}

// runStatus is complete when every repository and every path produced
// facts, and partial when some failed: pages say so.
func runStatus(fs *domain.FactSet) domain.RunStatus {
	for _, rf := range fs.Repos {
		if rf.Status != domain.RepoStatusOK {
			return domain.RunStatusPartial
		}
	}
	for _, w := range fs.Warnings {
		if w.Code == warnPathFailed {
			return domain.RunStatusPartial
		}
	}
	return domain.RunStatusComplete
}

// scanOptions are the effective options a run records: enough to know how
// it was made, and never a secret. The paths are those discovery resolved
// (plan.paths), not the arguments as typed, and every value is scrubbed, so
// a token pasted into an argument reaches neither facts.db nor a report.
// The ranked metrics are recorded with the versions they resolved to, so a
// later report ranks the same versions.
func scanOptions(r ScanRequest, d Deps, pl *plan, jobs int, scope string, ranked []rankedMetric) map[string]string {
	opts := map[string]string{
		"jobs":         strconv.Itoa(jobs),
		"scope":        scope,
		"history_days": strconv.Itoa(scanHistoryDays),
		"paths":        strings.Join(pl.paths, "\n"),
		"policy":       d.Policy.Name,
	}
	if len(ranked) > 0 {
		var ids []string
		for _, m := range ranked {
			ids = append(ids, m.def.ID.String())
		}
		opts["rank"] = strings.Join(ids, ",")
	}
	if len(r.Exclude) > 0 {
		opts["exclude_repo"] = strings.Join(r.Exclude, "\n")
	}
	if r.IgnoreRevsFile != "" {
		opts["ignore_revs_file"] = r.IgnoreRevsFile
	}
	for k, v := range opts {
		opts[k] = redact.Scrub(v)
	}
	return opts
}

// storeRun writes a run and publishes it with run.Status. If any write
// fails, the run is marked failed, which hides it from every reader, even
// when ctx was cancelled.
func storeRun(ctx context.Context, s domain.FactStore, run domain.Run, fs *domain.FactSet,
	values []domain.MetricValue, ranks []domain.RankInterval) (int64, error) {
	status := run.Status
	run.Status = domain.RunStatusRunning
	id, err := s.BeginRun(ctx, run)
	if err != nil {
		return 0, fmt.Errorf("storing the run: %w", err)
	}
	abort := func(err error) (int64, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		defer cancel()
		if ferr := s.FinishRun(fctx, id, domain.RunStatusFailed); ferr != nil {
			err = errors.Join(err, fmt.Errorf("marking run %d failed: %w", id, ferr))
		}
		return 0, err
	}
	for i := range fs.Repos {
		if err := s.PutRepoFacts(ctx, id, &fs.Repos[i]); err != nil {
			return abort(fmt.Errorf("storing %s: %w", fs.Repos[i].Repo.Name, err))
		}
	}
	if err := s.PutRunFacts(ctx, id, fs.Groups, fs.Warnings); err != nil {
		return abort(fmt.Errorf("storing run %d's groups and warnings: %w", id, err))
	}
	if err := s.PutMetrics(ctx, id, values, ranks); err != nil {
		return abort(fmt.Errorf("storing run %d's metrics: %w", id, err))
	}
	if err := s.FinishRun(ctx, id, status); err != nil {
		return abort(fmt.Errorf("publishing run %d: %w", id, err))
	}
	return id, nil
}
