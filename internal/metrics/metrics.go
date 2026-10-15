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
	"errors"
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
	ID             domain.MetricID
	Question, Unit string
	Ranked         bool
	// Subjects are the kinds of subject (domain.SubjectPerson, SubjectRepo,
	// SubjectArea, SubjectGroup, SubjectChange) of the values a scan stores,
	// in the order the catalog entry names them: none for who_knows@1, which
	// who-knows computes per query, and no person for ai_assisted@1, whose
	// personal share only that person's page computes (PersonalAIShare). It
	// is the one declaration of it, as Forge is of GitHub data: explain,
	// which reads stored values, refuses a metric about any other kind of
	// subject, whatever a run holds, rather than answer with nothing (#69).
	Subjects []string
	// Forge is true for a metric that reads the GitHub data a run read
	// (FactSet.Forge): its numbers are as current as that data's sync, and
	// a report dates them by it and says where it is missing. It is the one
	// declaration of which metrics read GitHub data, so a new metric that
	// reads reviews is dated and caveated with no second list to forget.
	Forge bool
	// Ratio is true for a metric whose values are shares, Num of Den, as
	// durable@1's are, so a report prints them as a percent with both
	// parts, 1 of 1 included. It is the one declaration of it: a report
	// reads it, never the unit's words, which can name other shares
	// (surviving@1's counts carry a bot_share detail), and a ranked
	// metric's bootstrap follows from it (RankKind), so its rank table
	// cannot print its estimates in another unit than its values.
	Ratio                                             bool
	Eligibility, BlindSpots, Gaming, Oracle, Validity string
}

// RankKind is how rank.Intervals bootstraps a ranked metric
// (docs/metrics.md#ranking-rules), which follows from Ratio: a share
// resamples each person's clusters (rank.Ratio), and any other value is a
// total, which resamples the window's week blocks (rank.Sum). No metric
// declares a kind of its own, so the kind its intervals are drawn by and
// the unit its values and its rank table print in (RankSet.Percent, which
// reads Ratio) agree by construction. A metric's Clusters still shapes its
// data for its kind, Den for a share and Block and Options.Blocks for a
// total, so flipping Ratio is a new metric version, as any definition
// change is.
func (d Definition) RankKind() rank.Kind {
	if d.Ratio {
		return rank.Ratio
	}
	return rank.Sum
}

// Input is what every metric computes over. Facts is required, with its
// Now. Policy may be nil, which means no bot or AI patterns (as for
// identity.New) and no [week] days, so Days must then be set for
// landed@1; Compute checks that before any metric runs, so a bad Input
// fails with its own name rather than as one metric's error halfway
// through.
type Input struct {
	Facts  *domain.FactSet
	Policy *policy.Policy
	// Who is nil, and then built from Facts and Policy, or a directory
	// identity.New built over the same facts, FactSet.Forge included: the
	// GitHub data is what links a login to a person (Reviewer).
	Who  *identity.Directory
	Days int // landed@1 window, at least 1; 0: the policy's [week] days
}

// weekDays returns landed@1's window in days: Days, else the policy's
// [week] days, which policy.Parse requires to be at least 1.
func (in *Input) weekDays() (int, error) {
	switch {
	case in.Days > 0:
		return in.Days, nil
	case in.Days < 0:
		return 0, fmt.Errorf("metrics: Input.Days is %d: want at least 1, or 0 for the policy's [week] days", in.Days)
	case in.Policy != nil && in.Policy.Week.Days > 0:
		return in.Policy.Week.Days, nil
	case in.Policy != nil:
		return 0, fmt.Errorf("metrics: Input.Days is 0 and the policy's [week] days is %d: landed@1 needs a window of at least one day", in.Policy.Week.Days)
	default:
		return 0, errors.New("metrics: Input.Days is 0 and there is no policy to take [week] days from: landed@1 needs a window of at least one day")
	}
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
// scope, beside the kind its definition gives (Definition.RankKind):
//
//	def := m.Definition()
//	rank.Intervals(def.ID, scope, ri.Window, def.RankKind(), ri.Data, ri.Options)
type RankInput struct {
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
		reviewsGiven{},
	}
}

// Lookup returns the metric with id, such as "durable@1", or the latest
// version for a bare name such as "durable". An id that does not parse, or
// that names no registered metric, is a *domain.UsageError, because ids come
// from the command line and the MCP server. Its message names no command:
// each of those readers has its own, which the use case adds (#69).
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
		"unknown metric %q: the registry has %s", id, strings.Join(ids, ", "))}
}

// Compute runs every metric over in and returns all values, sorted by
// metric, scope, subject and window: what a scan stores with its run. The
// facts are projected once and the projection is shared by every metric
// that reads it. Two answers are never part of it, because they are asked
// per query: WhoKnows, for any path, and PersonalAIShare, which only the
// person it describes may see.
func Compute(in *Input) ([]domain.MetricValue, error) {
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	if v.daysErr != nil {
		return nil, v.daysErr
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
