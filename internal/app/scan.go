package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
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

// Scan runs discovery, reads the forge replica, then runs history,
// classification and blame per repository (each one a bulkhead, every blame
// sharing one pool of --jobs slots), computes every metric and every rank,
// stores it all as one run and publishes the run. It returns
// domain.ErrNoFacts, alongside the report, when no repository produced
// facts; then nothing is stored.
//
// The run is written only after everything is computed, so it is "running"
// for as short a time as possible, and a scan that fails before then leaves
// nothing behind. Readers never see it until FinishRun flips it to complete
// or partial (docs/spec/codebase.md#run-model).
//
// GitHub data comes from the replica that sync github keeps in facts.db
// (Deps.ForgeStore), never from the network (ADR 0004). The scan reads it
// once, before any git process starts. Once collection says which of its
// repositories produced facts, its own part of the replica (the forge
// repositories domain.ForgeJoin joins to them, with their watermarks and
// pull requests) is the run's GitHub data, FactSet.Forge, which the run
// keeps with the watermarks that date it, so every later reader computes
// and dates over what the scan read (ADR 0011). An empty replica is normal:
// the run then has no GitHub data, and every forge-based value is absent,
// never zero. A replica that cannot be read stops the scan: it is the
// facts.db the run would be written to, so the run could most likely not
// be stored either, and a run published without the GitHub data the
// replica holds would read as "not synced", plausible but wrong (fail fast:
// Shore, "Fail Fast", IEEE Software, 2004).
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
	ranked, err := resolveRanked(r.Rank, d.Commands)
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

	window := domain.Window{Since: r.Now.Add(-scanHistoryDays * 24 * time.Hour), Until: r.Now}
	replica, err := loadReplica(ctx, d, window.Since)
	if err != nil {
		return nil, err
	}

	start := time.Now()
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
	var forge *report.ForgeRead // nil without the replica port: the scan cannot speak for the replica, as a reader without it cannot
	var watermarks []domain.RunWatermark
	if d.ForgeStore != nil {
		fs.Forge, watermarks = runForge(fs.Repos, replica)
		forge = forgeRead(watermarks)
	}
	// After the run's GitHub data: it links forge logins to the run's people.
	who := identity.New(fs, d.Policy)
	landed := report.Landed{Days: d.Policy.Week.Days, Sort: d.Policy.Week.Sort}
	build := func(run *domain.Run, values []domain.MetricValue, sets []report.RankSet) *report.Model {
		finish(timings, started)
		return report.Build(&report.Input{
			Kind: report.KindScan, Tool: d.Version, Run: run, Facts: fs, Policy: d.Policy, Who: who,
			Values: values, Ranks: sets, Landed: landed, History: window, Timings: timings, Scope: scope,
			Forge: forge,
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
	// Every ranked metric is ranked in every scope and stored with the run,
	// whichever ones --rank names, so a reader uses the stored ranks and
	// never bootstraps again: one rule for every metric, which leaves no
	// metric to forget (docs/spec/codebase.md#run-model). --rank picks the
	// tables this report prints.
	all, intervals, err := rankAll(in, allRanked(), rankScopes(scope, fs), nil, nil)
	if err != nil {
		return nil, err
	}
	sets := chosenSets(all, ranked)
	timings.RankMS = time.Since(start).Milliseconds()

	start = time.Now()
	run := domain.Run{
		Kind: domain.RunKindScan, Status: runStatus(fs), Now: r.Now, Started: started.UTC(),
		ToolVersion: d.Version, Policy: d.Policy.Ref(), PolicySource: d.Policy.Source,
		Options: scanOptions(r, d, pl, jobs, scope, ranked),
	}
	id, err := storeRun(ctx, d, run, fs, values, intervals, watermarks)
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
// when ctx was cancelled. The GitHub data the run read, its watermarks and
// its pull requests, is recorded while it is still running, so no reader
// sees the run without it.
func storeRun(ctx context.Context, d Deps, run domain.Run, fs *domain.FactSet,
	values []domain.MetricValue, ranks []domain.RankInterval, watermarks []domain.RunWatermark) (int64, error) {
	s := d.Store
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
	// Watermarks and pull requests come only from a replica the ForgeStore
	// loaded.
	if len(watermarks) > 0 && d.ForgeStore != nil {
		if err := d.ForgeStore.PutRunWatermarks(ctx, id, watermarks); err != nil {
			return abort(fmt.Errorf("storing run %d's forge watermarks: %w", id, err))
		}
		if err := d.ForgeStore.PutRunForge(ctx, id, fs.Forge.Repos, fs.Forge.PullRequests); err != nil {
			return abort(fmt.Errorf("storing run %d's GitHub data: %w", id, err))
		}
	}
	if err := s.PutMetrics(ctx, id, values, ranks); err != nil {
		return abort(fmt.Errorf("storing run %d's metrics: %w", id, err))
	}
	if err := s.FinishRun(ctx, id, status); err != nil {
		return abort(fmt.Errorf("publishing run %d: %w", id, err))
	}
	return id, nil
}

// loadReplica reads the forge replica a scan computes over (FactSet.Forge):
// every repository and watermark, and the pull requests updated since the
// start of the history window, which holds every metric's window. It returns
// nil without the replica port, and for an empty replica: the run then has
// no GitHub data.
func loadReplica(ctx context.Context, d Deps, since time.Time) (*domain.ForgeFacts, error) {
	if d.ForgeStore == nil {
		return nil, nil
	}
	f, err := d.ForgeStore.LoadForge(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub data in facts.db, before collecting anything: %w", err)
	}
	if f == nil {
		d.Log.Info("no GitHub data in facts.db: forge-based metrics get no values ('stillhere sync github' copies it)")
		return nil, nil
	}
	d.Log.Info("read the GitHub data in facts.db", "forge_repos", len(f.Repos), "pull_requests", len(f.PullRequests))
	return f, nil
}

// runForge returns the run's GitHub data, FactSet.Forge, and the watermarks
// that date it (ForgeStore.PutRunWatermarks), both cut from the replica the
// scan read by the one join (domain.ForgeJoin) of its forge repositories to
// the repositories it collected (repos), so what the metrics compute over,
// what the run keeps (PutRunForge) and what its watermarks date are the
// same repositories. The replica's other repositories, such as another
// organization's, are none of the run's business, and neither is a
// repository whose collection failed. A forge repository the replica holds
// no watermark for gets a zero one: the run read none of its pull requests.
// Both are nil when no forge repository is the run's.
func runForge(repos []domain.RepoFacts, replica *domain.ForgeFacts) (*domain.ForgeFacts, []domain.RunWatermark) {
	if replica == nil {
		return nil, nil
	}
	join := domain.ForgeJoin(repos, replica.Repos)
	if len(join) == 0 {
		return nil, nil
	}
	marks := make(map[string]domain.ForgeWatermark, len(replica.Watermarks))
	for _, wm := range replica.Watermarks {
		marks[wm.Repo] = wm
	}
	// The join travels with the data: the metrics count by it, and the
	// watermarks record it (their Keys), so the run has one join, made here.
	own := &domain.ForgeFacts{Join: join}
	var watermarks []domain.RunWatermark
	for _, r := range replica.Repos {
		keys := join[r.ID]
		if len(keys) == 0 {
			continue
		}
		wm, ok := marks[r.ID]
		if !ok {
			wm = domain.ForgeWatermark{Repo: r.ID}
		}
		own.Repos = append(own.Repos, r)
		own.Watermarks = append(own.Watermarks, wm)
		watermarks = append(watermarks, domain.RunWatermark{ForgeWatermark: wm, Name: r.Owner + "/" + r.Name, Keys: keys})
	}
	for i := range replica.PullRequests {
		if p := &replica.PullRequests[i]; len(join[p.Repo]) > 0 {
			own.PullRequests = append(own.PullRequests, *p)
		}
	}
	// The order ForgeFacts documents, which RunForge reads back: repositories
	// as the replica sorted them, watermarks by repository, pull requests by
	// repository, then number, as the replica sorted them.
	slices.SortFunc(own.Watermarks, func(a, b domain.ForgeWatermark) int { return cmp.Compare(a.Repo, b.Repo) })
	slices.SortFunc(watermarks, func(a, b domain.RunWatermark) int { return cmp.Compare(a.Repo, b.Repo) })
	return own, watermarks
}

// forgeRead dates a run's GitHub data for the report: one ForgeSync for
// each watermark the run kept and each of its repositories the watermark
// joined, sorted by forge repository, then key, with the metrics that read
// it, as the registry declares them. The scan and every reader of the
// stored run build it from the same watermarks, the run's own, so a stored
// run dates its GitHub data exactly as its scan did, whatever a later sync
// renamed.
func forgeRead(watermarks []domain.RunWatermark) *report.ForgeRead {
	read := &report.ForgeRead{Metrics: forgeMetrics()}
	for _, wm := range watermarks {
		for _, key := range wm.Keys {
			read.Repos = append(read.Repos, report.ForgeSync{Repo: wm.Name, Key: key,
				Updated: instant(wm.Updated), Synced: instant(wm.Synced)})
		}
	}
	slices.SortFunc(read.Repos, func(a, b report.ForgeSync) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key))
	})
	return read
}

// forgeMetrics lists, sorted, the metric@version of every metric in the
// registry that reads GitHub data (metrics.Definition.Forge): the one
// declaration the report dates and caveats their numbers by.
func forgeMetrics() []string {
	var out []string
	for _, m := range metrics.All() {
		if def := m.Definition(); def.Forge {
			out = append(out, def.ID.String())
		}
	}
	slices.Sort(out)
	return out
}

// instant is t in UTC, or nil for the zero time, which a watermark uses for
// "not yet".
func instant(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// chosenSets keeps the rank tables of the metrics --rank named, in the
// order named, each in its scopes' order.
func chosenSets(sets []report.RankSet, named []rankedMetric) []report.RankSet {
	var out []report.RankSet
	for _, m := range named {
		id := m.def.ID.String()
		for _, s := range sets {
			if s.Metric.ID == id {
				out = append(out, s)
			}
		}
	}
	return out
}
