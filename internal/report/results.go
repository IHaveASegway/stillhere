package report

import "github.com/IHaveASegway/stillhere/internal/domain"

// The answers of the read commands. They live here, beside Model, so that
// the text, Markdown and JSON renderers and the MCP server share one shape
// for each answer; internal/app returns them.

// Explain is the explain answer: one subject's values for one metric in one
// run, the starting point for tracing them to the changes behind them.
type Explain struct {
	RunID   int64
	Metric  domain.MetricID
	Subject domain.Subject
	Values  []domain.MetricValue // one per scope and window, sorted
}

// WhoKnows is the who_knows@1 answer for a path in one run.
type WhoKnows struct {
	RunID  int64
	Repo   string // Repo.Key, or "" for every repository
	Path   string
	Values []domain.MetricValue // sorted
}

// Me is one person's own page: their values and ranks in one run.
type Me struct {
	RunID  int64
	Email  string
	Values []domain.MetricValue  // sorted
	Ranks  []domain.RankInterval // sorted
}

// Validation is how well each ranked metric separates the changes a person
// labelled meaningful from those labelled routine
// (docs/metrics.md#validity-checks).
type Validation struct {
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
