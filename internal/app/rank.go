package app

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Ranking, one metric at a time (ADR 0001). The metric owns its window and
// its eligibility rule (metrics.RankInput), and rank.Intervals owns the
// bootstrap; this file only decides which scopes to rank in and keeps bots
// out, because bots are never ranked.

// rankedMetric is a metric that ranks people, resolved in the registry.
type rankedMetric struct {
	def    metrics.Definition
	ranked metrics.Ranked
}

// lookupMetric resolves a metric id from a request, such as "durable@1",
// or "durable" for the latest version. An id the registry does not know is
// a usage error: it came from the user.
func lookupMetric(id string) (metrics.Metric, error) {
	m, err := metrics.Lookup(id)
	if err == nil {
		return m, nil
	}
	var usage *domain.UsageError
	if errors.As(err, &usage) {
		return nil, err
	}
	return nil, &domain.UsageError{Msg: fmt.Sprintf("unknown metric %q (%v): 'stillhere metrics' lists them", id, err)}
}

// resolveRanked resolves the metrics named by --rank, in the order named,
// once each.
func resolveRanked(ids []domain.MetricID) ([]rankedMetric, error) {
	var out []rankedMetric
	seen := map[domain.MetricID]bool{}
	for _, id := range ids {
		m, err := lookupMetric(id.String())
		if err != nil {
			return nil, err
		}
		r, ok := m.(metrics.Ranked)
		if !ok {
			return nil, &domain.UsageError{Msg: fmt.Sprintf(
				"%s is context and never ranked: ranks are for %s ('stillhere metrics' lists them)", id, rankedNames())}
		}
		def := m.Definition()
		if !seen[def.ID] {
			seen[def.ID] = true
			out = append(out, rankedMetric{def: def, ranked: r})
		}
	}
	return out, nil
}

// allRanked returns every ranked metric in the registry, in its order.
func allRanked() []rankedMetric {
	var out []rankedMetric
	for _, m := range metrics.All() {
		if r, ok := m.(metrics.Ranked); ok {
			out = append(out, rankedMetric{def: m.Definition(), ranked: r})
		}
	}
	return out
}

func rankedNames() string {
	var names []string
	for _, m := range allRanked() {
		names = append(names, m.def.ID.String())
	}
	if len(names) == 0 {
		return "the ranked metrics"
	}
	return strings.Join(names, ", ")
}

// parseRankOption reads the --rank metrics a scan recorded in its options.
func parseRankOption(s string) []domain.MetricID {
	var out []domain.MetricID
	for _, part := range strings.Split(s, ",") {
		if id, err := domain.ParseMetricID(strings.TrimSpace(part)); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// rankScopes returns the scopes to rank within. "all" ranks everyone
// together, which only an explicit --scope all asks for; "group" ranks
// within each group, and within each repository named directly, because
// ranks never mix groups unless asked to (docs/metrics.md#ranking-rules).
func rankScopes(kind string, fs *domain.FactSet) []domain.Scope {
	if kind == domain.ScopeAll {
		return []domain.Scope{{Kind: domain.ScopeAll}}
	}
	var out []domain.Scope
	for _, g := range fs.Groups {
		out = append(out, domain.Scope{Kind: domain.ScopeGroup, Key: g.Name})
	}
	for _, rf := range fs.Repos {
		if rf.Repo.Group == "" {
			out = append(out, domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key})
		}
	}
	return out
}

// rankAll ranks each metric within each scope. Intervals the run already
// stored are used as they are, since the bootstrap is seeded from the
// metric, scope and window and would give the same ones; the rest are
// computed. A scope where nobody has a cluster still gets its table, empty,
// so that a rank the user asked for never disappears without a word (the
// report says why). With subject given, only the tables subject is in are
// kept: a person's own page needs only theirs. A stored table counts when it
// lists subject, whatever the clusters say: the stored intervals are the
// run's, and a run stored before it kept its GitHub data (ADR 0011) has no
// clusters of a metric that reads it. It returns the tables for the report
// and the intervals to store.
func rankAll(in *metrics.Input, ms []rankedMetric, scopes []domain.Scope, stored []domain.RankInterval,
	subject *domain.Subject) ([]report.RankSet, []domain.RankInterval, error) {
	type key struct {
		metric domain.MetricID
		scope  domain.Scope
	}
	prev := map[key][]domain.RankInterval{}
	for _, ri := range stored {
		k := key{ri.Metric, ri.Scope}
		prev[k] = append(prev[k], ri)
	}
	var sets []report.RankSet
	var all []domain.RankInterval
	for _, m := range ms {
		for _, s := range scopes {
			ri, err := m.ranked.Clusters(in, s)
			if err != nil {
				return nil, nil, fmt.Errorf("ranking %s in %s %s: %w", m.def.ID, s.Kind, s.Key, err)
			}
			data := people(ri.Data, in.Who)
			intervals := prev[key{m.def.ID, s}]
			if subject != nil && len(data[*subject]) == 0 &&
				!slices.ContainsFunc(intervals, func(x domain.RankInterval) bool { return x.Subject == *subject }) {
				continue
			}
			if len(intervals) == 0 && len(data) > 0 {
				intervals = rank.Intervals(m.def.ID, s, ri.Window, m.def.RankKind(), data, ri.Options)
			}
			// Percent reads the one declaration of a share, as explain
			// does, and so does the bootstrap's kind (RankKind).
			sets = append(sets, report.RankSet{
				Metric: metricInfo(m.def), Scope: s, Window: ri.Window,
				Percent: m.def.Ratio, MinN: ri.Options.MinN, Intervals: intervals,
			})
			all = append(all, intervals...)
		}
	}
	slices.SortFunc(all, func(a, b domain.RankInterval) int {
		return cmp.Or(
			cmp.Compare(a.Metric.Name, b.Metric.Name),
			cmp.Compare(a.Metric.Version, b.Metric.Version),
			cmp.Compare(a.Scope.Kind, b.Scope.Kind),
			cmp.Compare(a.Scope.Key, b.Scope.Key),
			cmp.Compare(a.Subject.Kind, b.Subject.Kind),
			cmp.Compare(a.Subject.Key, b.Subject.Key),
		)
	})
	return sets, all, nil
}

// people keeps the clusters of people who are not bots: bots are never
// ranked, and a bot in the data would push every person down a place.
func people(data map[domain.Subject][]rank.Cluster, who *identity.Directory) map[domain.Subject][]rank.Cluster {
	var out map[domain.Subject][]rank.Cluster
	for s, cs := range data {
		if s.Kind != domain.SubjectPerson || who.Bot(s.Key) || len(cs) == 0 {
			continue
		}
		if out == nil {
			out = map[domain.Subject][]rank.Cluster{}
		}
		out[s] = cs
	}
	return out
}

// metricInfo is a metric's published definition as reports print it.
func metricInfo(def metrics.Definition) report.MetricInfo {
	return report.MetricInfo{
		ID: def.ID.String(), Question: def.Question, Unit: def.Unit, Ranked: def.Ranked, Forge: def.Forge, Ratio: def.Ratio,
		Eligibility: def.Eligibility, BlindSpots: def.BlindSpots, Gaming: def.Gaming,
		Oracle: def.Oracle, Validity: def.Validity,
	}
}
