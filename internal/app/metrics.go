package app

import (
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// AboutPeople reports whether a scan stores values of the metric id about
// people (metrics.Definition.Subjects, the one declaration Explain refuses
// every other kind of subject by); an unknown metric is a
// *domain.UsageError in the reader's words (cmds). An inbound adapter that
// reads a subject argument whose kind it was not told asks it before it
// resolves the subject as a person (round 3's KT-8: the MCP server read
// "repo:NAME" as a person's name for review_coverage@1, which is never
// about one, and its miss hid the refusal that names what to pass).
func AboutPeople(id domain.MetricID, cmds report.Commands) (bool, error) {
	m, err := lookupMetric(id.String(), cmds)
	if err != nil {
		return false, err
	}
	return slices.Contains(m.Definition().Subjects, domain.SubjectPerson), nil
}

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
