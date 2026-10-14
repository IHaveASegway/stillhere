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
// stillhere.toml never changes a stored run's numbers. The answers are
// report types, which the command line renders and the MCP server (#15)
// returns.

// Report renders a stored run as a report, optionally with ranks.
func Report(ctx context.Context, d Deps, r ReportRequest) (*report.Model, error) {
	return nil, fmt.Errorf("app.Report: %w (#11)", domain.ErrNotImplemented)
}

// Explain returns one subject's values for one metric.
func Explain(ctx context.Context, d Deps, r ExplainRequest) (*report.Explain, error) {
	return nil, fmt.Errorf("app.Explain: %w (#11)", domain.ErrNotImplemented)
}

// WhoKnows returns who knows the code under a path.
func WhoKnows(ctx context.Context, d Deps, r WhoKnowsRequest) (*report.WhoKnows, error) {
	return nil, fmt.Errorf("app.WhoKnows: %w (#11)", domain.ErrNotImplemented)
}

// Me returns one person's own page.
func Me(ctx context.Context, d Deps, r MeRequest) (*report.Me, error) {
	return nil, fmt.Errorf("app.Me: %w (#11)", domain.ErrNotImplemented)
}

// Validate scores each ranked metric against hand labels with the AUC.
func Validate(ctx context.Context, d Deps, r ValidateRequest) (*report.Validation, error) {
	return nil, fmt.Errorf("app.Validate: %w (#11)", domain.ErrNotImplemented)
}
