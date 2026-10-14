// Package metrics is the metric registry and every metric version
// (docs/metrics.md). Metrics are code: each version is a reviewed, tested
// definition with a fixture oracle and a validity note, and a definition
// change is a new version, never a silent edit (ADR 0003).
//
// It is part of the functional core: Compute is a plain function over a
// domain.FactSet, testable without git or a database. Each metric is a
// Strategy (Gamma et al., Design Patterns) behind the Metric interface, and
// All is the registry: a single list, so a new metric or version is one new
// file plus one line there.
//
// The values a metric returns are a published language (Evans,
// Domain-Driven Design): the report, the store, explain and the MCP server
// all read them, so each metric's subjects, scopes, windows and Detail keys
// are documented with its entry in docs/metrics.md.
package metrics

import (
	"fmt"
	"strings"

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
	Who    *identity.Directory // nil: built from Facts and Policy
	Days   int                 // landed@1 window; 0: the policy's [week] days
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
	// Data holds each person's clusters, sorted by Change. Bots are never
	// ranked, so they are not in it; nil when the scope has no clusters.
	Data map[domain.Subject][]rank.Cluster
}

// projected is implemented by the metrics that compute over a shared
// projection, so Compute builds the projection once for all of them. A
// metric that does not implement it, such as one added in its own file, is
// computed through its Compute method instead.
type projected interface {
	compute(v *view) ([]domain.MetricValue, error)
}

// All returns every registered metric version in a stable order: the order
// of the catalog in docs/metrics.md. It is the registry, a single list: a
// new metric or version registers with one line here.
func All() []Metric {
	return []Metric{
		landedV1{},
		survivingV1{},
		majorityOwnerV1{},
		knowledgeConcentrationV1{},
		knowledgeLossV1{},
		whoKnowsV1{},
		durableV1{},
		simplifyingV1{},
		aiAssistedV1{},
	}
}

// Lookup returns the metric with id, such as "durable@1", or the latest
// version for a bare name such as "durable". An id that does not parse, or
// that names no registered metric, is a *domain.UsageError, because ids come
// from the command line and the MCP server.
func Lookup(id string) (Metric, error) {
	want, err := domain.ParseMetricID(id)
	if err != nil {
		return nil, err
	}
	var latest Metric
	for _, m := range All() {
		got := m.Definition().ID
		switch {
		case got.Name != want.Name:
		case want.Version == got.Version:
			return m, nil
		case want.Version == 0 && (latest == nil || got.Version > latest.Definition().ID.Version):
			latest = m
		}
	}
	if latest != nil {
		return latest, nil
	}
	ids := make([]string, 0, len(All()))
	for _, m := range All() {
		ids = append(ids, m.Definition().ID.String())
	}
	return nil, &domain.UsageError{Msg: fmt.Sprintf(
		"unknown metric %q: the registry has %s (stillhere metrics describes each)", id, strings.Join(ids, ", "))}
}

// Compute runs every metric over in and returns all values, sorted by
// metric, scope, subject and window. The facts are projected once and the
// projection is shared by every metric that reads it.
func Compute(in *Input) ([]domain.MetricValue, error) {
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	var out []domain.MetricValue
	for _, m := range All() {
		var vals []domain.MetricValue
		if p, ok := m.(projected); ok {
			vals, err = p.compute(v)
		} else {
			vals, err = m.Compute(in)
		}
		if err != nil {
			return nil, fmt.Errorf("metrics: compute %s: %w", m.Definition().ID, err)
		}
		out = append(out, vals...)
	}
	return sorted(out), nil
}

// computeAlone is the Compute method of a projected metric: it projects
// the facts for that metric alone.
func computeAlone(m projected, in *Input) ([]domain.MetricValue, error) {
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	return m.compute(v)
}
