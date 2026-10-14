package app

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The read side: each use case reads the latest complete or partial run
// unless a run id is given (FactStore.LatestRun or FactStore.Run), and never
// writes. It rebuilds the run's own policy with policy.FromSource on
// Run.PolicySource and checks its Ref against Run.Policy, so today's
// stillhere.toml never changes a stored run's numbers.

// ExplainResult is one person's values for one metric in one run, the
// starting point for tracing them to commits.
type ExplainResult struct {
	RunID  int64
	Metric domain.MetricID
	Person string
	Values []domain.MetricValue // one per scope and window, sorted
}

// WhoKnowsResult is the who_knows@1 answer for a path in one run.
type WhoKnowsResult struct {
	RunID  int64
	Repo   string // Repo.Key, or "" for every repository
	Path   string
	Values []domain.MetricValue // sorted
}

// MeResult is one person's own page: their values and ranks in one run.
type MeResult struct {
	RunID  int64
	Email  string
	Values []domain.MetricValue  // sorted
	Ranks  []domain.RankInterval // sorted
}

// ValidateResult is how well each ranked metric separates meaningful from
// routine changes in the labels.
type ValidateResult struct {
	RunID   int64
	Labels  int         // labelled changes read
	Metrics []MetricAUC // sorted by metric id
}

// MetricAUC is one metric's separation of the labelled changes.
type MetricAUC struct {
	Metric domain.MetricID
	AUC    float64 // 0.5 is chance; a ranked metric stays beta below 0.7
	N      int     // labelled changes the metric scored
}

// Report renders a stored run as a report, optionally with ranks.
func Report(ctx context.Context, d Deps, r ReportRequest) (*report.Model, error) {
	return nil, fmt.Errorf("app.Report: %w (#11)", domain.ErrNotImplemented)
}

// Explain returns one person's values for one metric.
func Explain(ctx context.Context, d Deps, r ExplainRequest) (*ExplainResult, error) {
	return nil, fmt.Errorf("app.Explain: %w (#11)", domain.ErrNotImplemented)
}

// WhoKnows returns who knows the code under a path.
func WhoKnows(ctx context.Context, d Deps, r WhoKnowsRequest) (*WhoKnowsResult, error) {
	return nil, fmt.Errorf("app.WhoKnows: %w (#11)", domain.ErrNotImplemented)
}

// Me returns one person's own page.
func Me(ctx context.Context, d Deps, r MeRequest) (*MeResult, error) {
	return nil, fmt.Errorf("app.Me: %w (#11)", domain.ErrNotImplemented)
}

// Validate scores each ranked metric against hand labels with the AUC.
func Validate(ctx context.Context, d Deps, r ValidateRequest) (*ValidateResult, error) {
	return nil, fmt.Errorf("app.Validate: %w (#11)", domain.ErrNotImplemented)
}
