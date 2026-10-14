package domain

import "context"

// FactStore is the port to the facts database: the write side records runs,
// and the read side sees only complete and partial runs
// (docs/spec/codebase.md#run-model). Every read method returns ErrNotFound
// for a run that is unknown, still running or failed.
type FactStore interface {
	// BeginRun records r as running and returns its id.
	BeginRun(ctx context.Context, r Run) (int64, error)
	// PutRepoFacts stores one repository's facts for a running run.
	PutRepoFacts(ctx context.Context, runID int64, f *RepoFacts) error
	// PutRunFacts stores the run's groups and run-level warnings.
	PutRunFacts(ctx context.Context, runID int64, groups []Group, warnings []Warning) error
	// PutMetrics stores the metric values and rank intervals computed for a run.
	PutMetrics(ctx context.Context, runID int64, values []MetricValue, ranks []RankInterval) error
	// FinishRun publishes a run by setting its final status.
	FinishRun(ctx context.Context, runID int64, status RunStatus) error
	// Run returns a complete or partial run by id, with its status, tool
	// version, times, options and policy source, or ErrNotFound.
	Run(ctx context.Context, id int64) (Run, error)
	// LatestRun returns the newest complete or partial run of the kind, or
	// ErrNotFound.
	LatestRun(ctx context.Context, kind RunKind) (Run, error)
	// LoadFacts returns a complete or partial run's facts exactly as they
	// were put, or ErrNotFound.
	LoadFacts(ctx context.Context, runID int64) (*FactSet, error)
	// LoadMetrics returns a complete or partial run's metric values and
	// rank intervals, or ErrNotFound.
	LoadMetrics(ctx context.Context, runID int64) ([]MetricValue, []RankInterval, error)
	// Close releases the database.
	Close() error
}
