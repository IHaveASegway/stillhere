// Package rank turns per-change measurements into honest ranks: a cluster
// bootstrap over a subject's changes, 95% intervals for the value and the
// rank, eligibility by sample size, and the AUC the validity check reports
// (docs/metrics.md#ranking-rules, ADR 0001).
//
// It is part of the functional core. Every random draw derives from a seed
// built from the metric, scope and window, so the same inputs always give the
// same intervals.
package rank

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Kind says how a ranked metric aggregates its clusters.
type Kind int

// The kinds of ranked metric.
const (
	Ratio Kind = iota // Σ Num / Σ Den over a subject's clusters, such as durable@1
	Sum               // Σ Num, resampled by week block, such as simplifying@1
)

// Cluster is one change's contribution to one subject's value. Changes, not
// lines, are resampled, because lines inside a change are not independent.
type Cluster struct {
	Change string  // landing SHA or PR id
	Num    float64 // the change's numerator
	Den    float64 // Ratio only
	Block  int     // Sum only: the week (counted back from Now) the change landed in
}

// Options tunes the bootstrap.
type Options struct {
	Replicates int // B; default 1000
	MinN       int // eligibility: clusters per subject
	Blocks     int // Sum only: week blocks in the window
}

// Intervals returns one rank interval per subject in data, sorted.
// Subjects with fewer than opt.MinN clusters are listed and not ranked.
func Intervals(id domain.MetricID, scope domain.Scope, w domain.Window, kind Kind, data map[domain.Subject][]Cluster, opt Options) []domain.RankInterval {
	return nil // #10
}

// Seed derives the bootstrap seed from the metric, scope and window.
func Seed(id domain.MetricID, scope domain.Scope, w domain.Window) uint64 {
	return 0 // #10
}

// AUC returns the Mann-Whitney area under the ROC curve: the probability that
// a value from pos exceeds one from neg, counting ties as one half.
func AUC(pos, neg []float64) float64 {
	return 0 // #10
}
