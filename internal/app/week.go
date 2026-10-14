package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/report"
)

// Week reports what landed on each repository's default branch in the
// window, without storing anything. It returns domain.ErrNoFacts, alongside
// the report, when no repository produced facts.
func Week(ctx context.Context, d Deps, r WeekRequest) (*report.Model, error) {
	return nil, fmt.Errorf("app.Week: %w (#11)", errors.ErrUnsupported)
}
