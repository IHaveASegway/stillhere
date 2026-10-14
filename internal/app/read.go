package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The read side: each use case reads the latest complete or partial run
// unless a run id is given (FactStore.LatestRun or FactStore.Run), and never
// writes. It rebuilds the run's own policy with policy.FromSource on
// Run.PolicySource and checks its Ref against Run.Policy, so today's
// stillhere.toml never changes a stored run's numbers. What it computes
// (ranks the scan was not asked for, who_knows@1 under a path, a ranked
// metric's clusters for explain) is computed over the stored facts with the
// run's policy and seeded from the inputs, so it is the same every time.
// The answers are report types, which the command line renders and the MCP
// server (#15) returns.

// stored is a run read back: the run, its own policy, its facts and its
// values.
type stored struct {
	run    domain.Run
	policy *policy.Policy
	facts  *domain.FactSet
	who    *identity.Directory
	values []domain.MetricValue
	ranks  []domain.RankInterval
}

// input is the metrics input over a stored run's facts.
func (s *stored) input() *metrics.Input {
	return &metrics.Input{Facts: s.facts, Policy: s.policy, Who: s.who, Days: s.policy.Week.Days}
}

// scope is the rank scope the run's scan was given: group unless it said
// all.
func (s *stored) scope() string {
	if s.run.Options["scope"] == domain.ScopeAll {
		return domain.ScopeAll
	}
	return domain.ScopeGroup
}

// load reads a stored run with its own policy.
func load(ctx context.Context, d Deps, runID int64) (*stored, error) {
	run, err := resolveRun(ctx, d.Store, runID)
	if err != nil {
		return nil, err
	}
	p, err := runPolicy(run)
	if err != nil {
		return nil, err
	}
	fs, err := d.Store.LoadFacts(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("reading run %d's facts: %w", run.ID, err)
	}
	values, ranks, err := d.Store.LoadMetrics(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("reading run %d's metrics: %w", run.ID, err)
	}
	return &stored{run: run, policy: p, facts: fs, who: identity.New(fs, p), values: values, ranks: ranks}, nil
}

// resolveRun finds the run a request names: its id, or the latest complete
// or partial scan for 0.
func resolveRun(ctx context.Context, s domain.FactStore, id int64) (domain.Run, error) {
	if id == 0 {
		run, err := s.LatestRun(ctx, domain.RunKindScan)
		if errors.Is(err, domain.ErrNotFound) {
			return run, fmt.Errorf("no complete scan in facts.db yet: run 'stillhere scan PATH...' first: %w", err)
		}
		if err != nil {
			return run, fmt.Errorf("finding the latest scan: %w", err)
		}
		return run, nil
	}
	run, err := s.Run(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return run, fmt.Errorf("run %d: no complete or partial run has that id: %w", id, err)
	}
	if err != nil {
		return run, fmt.Errorf("reading run %d: %w", id, err)
	}
	if run.Kind != domain.RunKindScan {
		return run, &domain.UsageError{Msg: fmt.Sprintf("run %d is a %s run, which holds no git facts: name a scan run", id, run.Kind)}
	}
	return run, nil
}

// runPolicy rebuilds the policy a run recorded and checks it is the one the
// run's hash names.
func runPolicy(run domain.Run) (*policy.Policy, error) {
	p, err := policy.FromSource(fmt.Sprintf("run %d", run.ID), run.PolicySource)
	if err != nil {
		return nil, fmt.Errorf("run %d: rebuilding its policy: %w", run.ID, err)
	}
	if got := p.Ref(); got != run.Policy {
		return nil, fmt.Errorf("run %d: its stored policy is version %d with hash %s, not the version %d with hash %s it recorded",
			run.ID, got.Version, got.SHA256, run.Policy.Version, run.Policy.SHA256)
	}
	return p, nil
}

// historyOf is the landing window a stored run's history covers.
func historyOf(fs *domain.FactSet) domain.Window {
	for _, rf := range fs.Repos {
		if !rf.History.Since.IsZero() || !rf.History.Until.IsZero() {
			return rf.History
		}
	}
	return domain.Window{Since: fs.Now.Add(-scanHistoryDays * 24 * time.Hour), Until: fs.Now}
}

// Report renders a stored run as a report, with ranks for the metrics
// named, or, when none is named, for those the scan was asked to rank.
func Report(ctx context.Context, d Deps, r ReportRequest) (*report.Model, error) {
	d, err := d.ready("app.Report", false, false, true)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	ids := r.Rank
	ranked, err := resolveRanked(ids)
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	loaded := time.Since(started).Milliseconds()
	if len(ids) == 0 {
		// The ranks the scan was asked for. One the registry no longer has
		// is left out rather than failing the report the user did not ask
		// to rank.
		for _, id := range parseRankOption(s.run.Options["rank"]) {
			m, err := resolveRanked([]domain.MetricID{id})
			if err != nil {
				d.Log.Warn("leaving out a rank the scan recorded", "metric", id.String(), "error", err)
				continue
			}
			ranked = append(ranked, m...)
		}
	}
	start := time.Now()
	sets, _, err := rankAll(s.input(), ranked, rankScopes(s.scope(), s.facts), s.ranks, nil)
	if err != nil {
		return nil, err
	}
	timings := &report.Timings{Started: s.run.Started, Finished: s.run.Finished, LoadMS: loaded,
		RankMS: time.Since(start).Milliseconds()}
	if !s.run.Started.IsZero() && !s.run.Finished.IsZero() {
		timings.TotalMS = s.run.Finished.Sub(s.run.Started).Milliseconds()
	}
	run := s.run
	return report.Build(&report.Input{
		Kind: report.KindScan, Tool: d.Version, Run: &run, Facts: s.facts, Policy: s.policy, Who: s.who,
		Values: s.values, Ranks: sets,
		Landed:  report.Landed{Days: s.policy.Week.Days, Sort: s.policy.Week.Sort},
		History: historyOf(s.facts), Timings: timings,
	}), nil
}

// Explain returns one subject's values for one metric and, for a ranked
// metric and a person, each change behind them: the clusters the metric
// counted, which the bootstrap resamples.
func Explain(ctx context.Context, d Deps, r ExplainRequest) (*report.Explain, error) {
	d, err := d.ready("app.Explain", false, false, true)
	if err != nil {
		return nil, err
	}
	m, err := lookupMetric(r.Metric.String())
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	def := m.Definition()
	var values []domain.MetricValue
	known := false
	for _, v := range s.values {
		if v.Subject != r.Subject {
			continue
		}
		known = true
		if v.Metric == def.ID {
			values = append(values, v)
		}
	}
	if !known {
		return nil, fmt.Errorf("%s %s has no values in run %d (a person is named by their canonical email, after .mailmap): %w",
			r.Subject.Kind, r.Subject.Key, s.run.ID, domain.ErrNotFound)
	}
	clusters := map[string][]rank.Cluster{}
	if rm, ok := m.(metrics.Ranked); ok && r.Subject.Kind == domain.SubjectPerson {
		in := s.input()
		for _, rf := range s.facts.Repos {
			if rf.Status != domain.RepoStatusOK {
				continue
			}
			ri, err := rm.Clusters(in, domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key})
			if err != nil {
				return nil, fmt.Errorf("explaining %s: %w", def.ID, err)
			}
			if cs := ri.Data[r.Subject]; len(cs) > 0 {
				clusters[rf.Repo.Key] = cs
			}
		}
	}
	run := s.run
	return report.NewExplain(&report.ExplainInput{
		Run: &run, Facts: s.facts, Who: s.who, Metric: metricInfo(def), Subject: r.Subject,
		Values: values, Clusters: clusters,
	}), nil
}

// WhoKnows returns who knows the code under a path: who_knows@1 computed
// over the run's facts under that path, so any prefix can be asked about,
// down to one file, without the run storing a value per directory.
func WhoKnows(ctx context.Context, d Deps, r WhoKnowsRequest) (*report.WhoKnows, error) {
	d, err := d.ready("app.WhoKnows", false, false, true)
	if err != nil {
		return nil, err
	}
	if _, err := lookupMetric(report.MetricWhoKnows); err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	if r.Repo != "" && !hasRepo(s.facts, r.Repo) {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"--repo %q is not a repository of run %d: it takes a repository key, as 'stillhere report --format json' lists them", r.Repo, s.run.ID)}
	}
	prefix := cleanPrefix(r.Path)
	values, err := whoKnowsValues(s, r.Repo, prefix)
	if err != nil {
		return nil, fmt.Errorf("computing %s: %w", report.MetricWhoKnows, err)
	}
	run := s.run
	return report.NewWhoKnows(&report.WhoKnowsInput{
		Run: &run, Facts: underPrefix(s.facts, prefix, r.Repo), Who: s.who, Path: prefix, Repo: r.Repo, Values: values,
	}), nil
}

// whoKnowsValues computes who_knows@1 for the code under prefix: per
// query, because a value for every directory of every repository would
// outnumber the facts. With the metric contract on main, that is the
// metric's Compute over the run's facts under the prefix.
//
// TODO(#13): the metrics branch (#9, PR #35) answers a prefix itself with
// metrics.WhoKnows(in, repo, prefix), and its who_knows@1 Compute returns
// no values. Once both are merged, this body is
// metrics.WhoKnows(s.input(), repo, prefix); NewWhoKnows already reads that
// layout (docs/spec/report-json.md#where-the-numbers-come-from).
func whoKnowsValues(s *stored, repo, prefix string) ([]domain.MetricValue, error) {
	m, err := lookupMetric(report.MetricWhoKnows)
	if err != nil {
		return nil, err
	}
	return m.Compute(&metrics.Input{Facts: underPrefix(s.facts, prefix, repo), Policy: s.policy, Who: s.who, Days: s.policy.Week.Days})
}

func hasRepo(fs *domain.FactSet, key string) bool {
	for _, rf := range fs.Repos {
		if rf.Repo.Key == key {
			return true
		}
	}
	return false
}

// cleanPrefix turns a path from the command line into a repo-relative
// prefix, cleaned as if the repository were the root: "./src/" is "src",
// "/src/a.go" is "src/a.go", and "." or "" is the whole repository.
func cleanPrefix(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	return path.Clean("/" + p)[1:]
}

// under reports whether path p is prefix or inside it.
func under(p, prefix string) bool {
	return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

// underPrefix returns the facts of the repositories asked about (repo, or
// every one), with their paths, line owners, file changes and blame errors
// kept only under prefix. Commits and landings stay whole: who landed what
// under the path is decided by the file changes that remain.
func underPrefix(fs *domain.FactSet, prefix, repo string) *domain.FactSet {
	out := *fs
	out.Repos = nil
	for _, rf := range fs.Repos {
		if repo != "" && rf.Repo.Key != repo {
			continue
		}
		c := rf
		c.Paths, c.Owners, c.Changes, c.BlameErrors = nil, nil, nil, nil
		for _, pc := range rf.Paths {
			if under(pc.Path, prefix) {
				c.Paths = append(c.Paths, pc)
			}
		}
		for _, o := range rf.Owners {
			if under(o.Path, prefix) {
				c.Owners = append(c.Owners, o)
			}
		}
		for _, ch := range rf.Changes {
			if under(ch.Path, prefix) || (ch.OldPath != "" && under(ch.OldPath, prefix)) {
				c.Changes = append(c.Changes, ch)
			}
		}
		for _, e := range rf.BlameErrors {
			if under(e.Path, prefix) {
				c.BlameErrors = append(c.BlameErrors, e)
			}
		}
		out.Repos = append(out.Repos, c)
	}
	return &out
}

// Me returns one person's own page: every value the run holds about them,
// and their place in each ranked metric's table within the run's rank
// scope, which is exactly what a lead sees about them (ADR 0001).
func Me(ctx context.Context, d Deps, r MeRequest) (*report.Me, error) {
	d, err := d.ready("app.Me", false, false, true)
	if err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(r.Email))
	if email == "" {
		return nil, &domain.UsageError{Msg: "me needs the person's email"}
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	subject := domain.Subject{Kind: domain.SubjectPerson, Key: email}
	var values []domain.MetricValue
	for _, v := range s.values {
		if v.Subject == subject {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s has no values in run %d (a person is named by their canonical email, after .mailmap): %w",
			email, s.run.ID, domain.ErrNotFound)
	}
	sets, _, err := rankAll(s.input(), allRanked(), rankScopes(s.scope(), s.facts), s.ranks, &subject)
	if err != nil {
		return nil, err
	}
	run := s.run
	return report.NewMe(&report.MeInput{
		Run: &run, Facts: s.facts, Who: s.who, Email: email, Values: values, Ranks: sets,
	}), nil
}
