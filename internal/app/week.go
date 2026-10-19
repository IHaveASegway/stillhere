package app

import (
	"context"
	"fmt"
	"time"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Week reports what landed on each repository's default branch in the
// window, without storing anything: discovery, then each repository's
// history and classification as a bulkhead, then landed@2 (report.MetricLanded). It returns
// domain.ErrNoFacts, alongside the report, when no repository produced
// facts, so the command can still print each repository's error.
func Week(ctx context.Context, d Deps, r WeekRequest) (*report.Model, error) {
	d, err := d.ready("app.Week", true, true, false)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	landed := report.Landed{Days: d.Policy.Week.Days, Sort: d.Policy.Week.Sort}
	if r.DaysFromCLI {
		landed.Days, landed.DaysFromCLI = r.Days, true
	}
	if r.SortFromCLI {
		landed.Sort, landed.SortFromCLI = r.Sort, true
	}
	if landed.Days < 1 {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("the week window is %d days: want 1 or more", landed.Days)}
	}
	window := domain.Window{Since: r.Now.Add(-time.Duration(landed.Days) * 24 * time.Hour), Until: r.Now}
	timings := &report.Timings{Started: started.UTC()}

	pl, err := discover(ctx, d, r.Paths, r.Exclude)
	if err != nil {
		return nil, err
	}
	timings.DiscoverMS = time.Since(started).Milliseconds()

	start := time.Now()
	results, err := collectAll(ctx, pl.jobs, defaultJobs(), collector(d, collectOptions{mode: collect.HistoryWeek, window: window, now: r.Now}))
	if err != nil {
		return nil, err
	}
	timings.CollectMS = time.Since(start).Milliseconds()
	timings.Repos = repoTimings(results)

	fs := factSet(r.Now, d.Policy, pl, results)
	who := identity.New(fs, d.Policy)
	var values []domain.MetricValue
	if produced(fs) {
		start = time.Now()
		m, err := lookupMetric(report.MetricLanded, d.Commands)
		if err != nil {
			return nil, err
		}
		values, err = m.Compute(&metrics.Input{Facts: fs, Policy: d.Policy, Who: who, Days: landed.Days})
		if err != nil {
			return nil, fmt.Errorf("computing %s: %w", report.MetricLanded, err)
		}
		timings.MetricsMS = time.Since(start).Milliseconds()
	}
	// The week's identity candidates, over its own facts: its caveat names a
	// shared display name only when they link two of its emails, as a
	// scan's does (wave 11).
	var candidates []identity.Candidate
	if produced(fs) {
		candidates = identity.Candidates(fs, who).Candidates
	}
	finish(timings, started)
	model := report.Build(&report.Input{
		Kind: report.KindWeek, Tool: d.Version, Facts: fs, Policy: d.Policy, Who: who,
		Values: values, Landed: landed, History: window, Timings: timings, Candidates: candidates,
	})
	if !produced(fs) {
		return model, domain.ErrNoFacts
	}
	return model, nil
}

// finish closes a report's timings.
func finish(t *report.Timings, started time.Time) {
	t.Finished = time.Now().UTC()
	t.TotalMS = time.Since(started).Milliseconds()
}
