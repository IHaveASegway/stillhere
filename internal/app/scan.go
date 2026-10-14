package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/report"
)

// Scan runs discovery, then classification, history and blame per repository
// (each one a bulkhead), stores the facts, computes metrics and ranks, and
// publishes the run. It returns domain.ErrNoFacts, alongside the report, when
// no repository produced facts.
func Scan(ctx context.Context, d Deps, r ScanRequest) (*report.Model, error) {
	return nil, fmt.Errorf("app.Scan: %w (#11)", errors.ErrUnsupported)
}
