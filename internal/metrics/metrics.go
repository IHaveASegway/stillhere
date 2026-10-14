// Package metrics is the metric registry and every metric version
// (docs/metrics.md). Metrics are code: each version is a reviewed, tested
// definition with a fixture oracle and a validity note, and a definition
// change is a new version, never a silent edit (ADR 0003).
//
// It is part of the functional core: Compute is a plain function over a
// domain.FactSet, testable without git or a database.
package metrics

import (
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// Definition is a metric's published contract: the catalog entry of
// docs/metrics.md, in code.
type Definition struct {
	ID                                                domain.MetricID
	Question, Unit                                    string
	Ranked                                            bool
	Eligibility, BlindSpots, Gaming, Oracle, Validity string
}

// Input is what every metric computes over.
type Input struct {
	Facts  *domain.FactSet
	Policy *policy.Policy
	Who    *identity.Directory
	Days   int // landed@1 window
}

// Metric is one version of one metric.
type Metric interface {
	Definition() Definition
	Compute(in *Input) ([]domain.MetricValue, error)
}

// Ranked is a metric people can be ranked on, one metric at a time. It
// yields the per-change clusters the bootstrap in internal/rank resamples,
// framed by the metric itself: its window and its eligibility rule are part
// of its versioned definition (docs/metrics.md), so the use case that ranks
// never restates them.
type Ranked interface {
	Metric
	Clusters(in *Input, scope domain.Scope) (RankInput, error)
}

// RankInput is everything rank.Intervals needs to rank one metric in one
// scope:
//
//	rank.Intervals(m.Definition().ID, scope, ri.Window, ri.Kind, ri.Data, ri.Options)
type RankInput struct {
	Kind rank.Kind
	// Window is the metric's own window for in.Facts.Now, such as
	// durable@1's cohort [now-365d, now-90d). It seeds the bootstrap and
	// is the Window of the matching MetricValues, which is how ranks join
	// to values.
	Window domain.Window
	// Options carries MinN from the metric's eligibility rule (such as 10
	// clusters) and, for Sum, the number of week Blocks in Window.
	// Replicates 0 means rank's default, B = 1000.
	Options rank.Options
	Data    map[domain.Subject][]rank.Cluster
}

// All returns every registered metric version in a stable order.
func All() []Metric {
	return []Metric{reviewsGiven{}} // #9 adds the rest of the registry
}

// Lookup returns the metric with id, such as "durable@1", or the latest
// version for a bare name such as "durable".
func Lookup(id string) (Metric, error) {
	return nil, fmt.Errorf("metrics.Lookup: %w (#9)", domain.ErrNotImplemented)
}

// Compute runs every metric over in and returns all values, sorted.
func Compute(in *Input) ([]domain.MetricValue, error) {
	return nil, fmt.Errorf("metrics.Compute: %w (#9)", domain.ErrNotImplemented)
}
