// Package sqlite is the outbound adapter that implements domain.FactStore on
// SQLite through modernc.org/sqlite, a pure-Go translation of SQLite, so the
// binary needs no cgo and stays static (ADR 0002, ADR 0003).
//
// facts.db is a derived, disposable replica: git, GitHub and .mailmap stay
// the sources of truth. Runs are written as running and published by a
// status flip, so readers never see half a run
// (docs/spec/codebase.md#run-model).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"

	// Registers the "sqlite" database/sql driver.
	_ "modernc.org/sqlite"
)

// Store is one open facts database.
type Store struct {
	db *sql.DB
}

var _ domain.FactStore = (*Store)(nil)

// Open opens the facts database at path, creating it and its directory
// (0700, file 0600) when missing, migrating its schema, and deleting runs a
// crash left running.
func Open(ctx context.Context, path string) (*Store, error) {
	return nil, fmt.Errorf("sqlite.Open: %w (#8)", domain.ErrNotImplemented)
}

// BeginRun records r as running and returns its id.
func (s *Store) BeginRun(ctx context.Context, r domain.Run) (int64, error) {
	return 0, fmt.Errorf("sqlite.Store.BeginRun: %w (#8)", domain.ErrNotImplemented)
}

// PutRepoFacts stores one repository's facts for a running run.
func (s *Store) PutRepoFacts(ctx context.Context, runID int64, f *domain.RepoFacts) error {
	return fmt.Errorf("sqlite.Store.PutRepoFacts: %w (#8)", domain.ErrNotImplemented)
}

// PutRunFacts stores the run's groups and run-level warnings.
func (s *Store) PutRunFacts(ctx context.Context, runID int64, groups []domain.Group, warnings []domain.Warning) error {
	return fmt.Errorf("sqlite.Store.PutRunFacts: %w (#8)", domain.ErrNotImplemented)
}

// PutMetrics stores the metric values and rank intervals computed for a run.
func (s *Store) PutMetrics(ctx context.Context, runID int64, values []domain.MetricValue, ranks []domain.RankInterval) error {
	return fmt.Errorf("sqlite.Store.PutMetrics: %w (#8)", domain.ErrNotImplemented)
}

// FinishRun publishes a run by setting its final status.
func (s *Store) FinishRun(ctx context.Context, runID int64, status domain.RunStatus) error {
	return fmt.Errorf("sqlite.Store.FinishRun: %w (#8)", domain.ErrNotImplemented)
}

// Run returns a complete or partial run by id, or domain.ErrNotFound.
func (s *Store) Run(ctx context.Context, id int64) (domain.Run, error) {
	return domain.Run{}, fmt.Errorf("sqlite.Store.Run: %w (#8)", domain.ErrNotImplemented)
}

// LatestRun returns the newest complete or partial run of the kind, or
// domain.ErrNotFound.
func (s *Store) LatestRun(ctx context.Context, kind domain.RunKind) (domain.Run, error) {
	return domain.Run{}, fmt.Errorf("sqlite.Store.LatestRun: %w (#8)", domain.ErrNotImplemented)
}

// LoadFacts returns a complete or partial run's facts exactly as they were
// put, or domain.ErrNotFound.
func (s *Store) LoadFacts(ctx context.Context, runID int64) (*domain.FactSet, error) {
	return nil, fmt.Errorf("sqlite.Store.LoadFacts: %w (#8)", domain.ErrNotImplemented)
}

// LoadMetrics returns a complete or partial run's metric values and rank
// intervals, or domain.ErrNotFound.
func (s *Store) LoadMetrics(ctx context.Context, runID int64) ([]domain.MetricValue, []domain.RankInterval, error) {
	return nil, nil, fmt.Errorf("sqlite.Store.LoadMetrics: %w (#8)", domain.ErrNotImplemented)
}

// Close releases the database. It is safe on a Store whose Open failed.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
