package app

import (
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Metrics returns the metric registry: every metric version with its
// question, unit, eligibility, blind spots, how it can be gamed, its oracle
// and its validity (docs/metrics.md). Both inbound adapters list it: the
// command line's metrics command and the MCP server's metrics() tool.
func Metrics() *report.Catalog {
	c := &report.Catalog{}
	for _, m := range metrics.All() {
		c.Metrics = append(c.Metrics, metricInfo(m.Definition()))
	}
	return c
}
