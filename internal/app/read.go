package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/redact"
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
//
// GitHub data is not per run: the forge replica outlives runs, and a sync
// replaces a pull request as a whole. So a run keeps what it read of it, the
// watermarks, with the names and the join they were read under, and its
// pull requests, whole (docs/spec/codebase.md#run-model, ADR 0011), and the
// read side never reads the replica. A stored run's FactSet.Forge is the
// run's own: what the read side computes from GitHub data (who_knows@1's
// recent reviewers for any path, a newer metric version) comes from the
// same snapshot as its git facts and stored numbers, and the watermarks say
// when it was synced. A report of a run from last month says last month's
// "as of" over last month's repositories and reviews, whatever a sync did
// since; computing over today's replica would put two snapshots in one
// answer.

// stored is a run read back: the run, its own policy, its facts and its
// values, and the GitHub data it read.
type stored struct {
	run    domain.Run
	policy *policy.Policy
	facts  *domain.FactSet
	who    *identity.Directory
	values []domain.MetricValue
	ranks  []domain.RankInterval
	forge  *report.ForgeRead // nil without the replica port: the reader cannot tell
	// unkept: the run read GitHub data but kept none of its pull requests,
	// as a run stored before schema version 4 did, so a number computed
	// from them when the run is read (who_knows@1's reviewers) is unknown.
	unkept bool
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
	values, ranks, split := canonicalKeys(values, ranks)
	if split {
		d.Log.Warn("the run was stored before stillhere compared GitHub logins without case, and holds one login in two "+
			"cases: they stay two reviewers in its pages, and no name or id finds the one in capitals: scan again", "run", run.ID)
	}
	s := &stored{run: run, policy: p, values: values, ranks: ranks}
	if s.facts, s.forge, s.unkept, err = withForge(ctx, d, run, fs); err != nil {
		return nil, err
	}
	// After the run's GitHub data: it links forge logins to the run's people.
	s.who = identity.New(s.facts, p)
	return s, nil
}

// withForge returns a stored run's facts with the GitHub data the run kept
// (FactSet.Forge, ADR 0011), how that data is dated, and whether the run
// read GitHub data but kept none of its pull requests (stored.unkept). The
// read use cases build the run's directory over what it returns, so a
// GitHub login links to a person, and GitHub's links group a person's
// addresses (identity's same-forge-account rule), as they did when the run
// was scanned.
func withForge(ctx context.Context, d Deps, run domain.Run, fs *domain.FactSet) (*domain.FactSet, *report.ForgeRead, bool, error) {
	forge, watermarks, err := readForgeMarks(ctx, d, run)
	if err != nil || len(watermarks) == 0 {
		return fs, forge, false, err
	}
	github, err := d.ForgeStore.RunForge(ctx, run.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("reading the GitHub data of run %d: %w", run.ID, err)
	}
	if github == nil {
		// A run stored before facts.db kept the pull requests a run
		// read (schema version 4). Its stored numbers stand, dated by
		// the watermarks it kept; what is computed from the pull
		// requests when it is read could only come from today's
		// replica, another snapshot than its git facts, so it is
		// unknown rather than half of it.
		d.Log.Warn("the run was stored before stillhere kept the pull requests a run reads: its stored GitHub numbers "+
			"stand, and who-knows lists no reviewers for it: scan again", "run", run.ID)
		return fs, forge, true, nil
	}
	// A copy: a store may hand every reader the same facts (the MCP
	// server caches them), which no use case changes.
	c := *fs
	c.Forge = github
	return &c, forge, false, nil
}

// canonicalKeys upcasts the person keys of a run stored before #69, whose
// GitHub logins kept their case ("@Zoe"), to the keys made since ("@zoe",
// identity.CanonicalKey), in the one place a stored run enters the read
// side. Its values and ranks then join, as a new run's do, the keys every
// reader is handed (lowercased) and those it computes from the run's facts
// when it reads it (who_knows@1's reviewers, explain's clusters). Nothing is
// migrated: the rows stay as they were written, and a store that hands
// every reader the same slices (the MCP server caches them) keeps its own,
// since a key is changed in a copy. A run that holds one login in two cases
// ("@Zoe" and "@zoe": one account it split in two) keeps both as stored
// and reports split: it ranked them as two people, and folding them would
// give one key two rows in a table, which only a new scan merges.
func canonicalKeys(values []domain.MetricValue, ranks []domain.RankInterval) ([]domain.MetricValue, []domain.RankInterval, bool) {
	keys := map[string]bool{} // every person key the run stored
	for _, v := range values {
		if v.Subject.Kind == domain.SubjectPerson {
			keys[v.Subject.Key] = true
		}
	}
	for _, r := range ranks {
		if r.Subject.Kind == domain.SubjectPerson {
			keys[r.Subject.Key] = true
		}
	}
	folded := map[string]int{} // canonical key → the stored keys of that form
	for key := range keys {
		folded[identity.CanonicalKey(key)]++
	}
	upcast := map[string]string{} // stored key → its canonical form, when it is another key and no other stored key has that form
	split := false
	for key := range keys {
		switch c := identity.CanonicalKey(key); {
		case c == key:
		case folded[c] > 1:
			split = true
		default:
			upcast[key] = c
		}
	}
	if len(upcast) == 0 {
		return values, ranks, split
	}
	values, ranks = slices.Clone(values), slices.Clone(ranks)
	for i := range values {
		if c, ok := upcast[values[i].Subject.Key]; ok && values[i].Subject.Kind == domain.SubjectPerson {
			values[i].Subject.Key = c
		}
	}
	for i := range ranks {
		if c, ok := upcast[ranks[i].Subject.Key]; ok && ranks[i].Subject.Kind == domain.SubjectPerson {
			ranks[i].Subject.Key = c
		}
	}
	return values, ranks, split
}

// readForgeMarks reads back the watermarks a stored run recorded
// (ForgeStore.RunWatermarks), each with the name and the join it was read
// under, and how they date the run's GitHub data. Nothing a later sync
// changed in the replica, a rename included, reaches the run's pages. The
// dating is nil without the replica port, when the reader cannot tell, and
// the watermarks are nil when the run read no GitHub data, or kept no join.
func readForgeMarks(ctx context.Context, d Deps, run domain.Run) (*report.ForgeRead, []domain.RunWatermark, error) {
	if d.ForgeStore == nil {
		return nil, nil, nil
	}
	watermarks, err := d.ForgeStore.RunWatermarks(ctx, run.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the forge watermarks of run %d: %w", run.ID, err)
	}
	for _, wm := range watermarks {
		if len(wm.Keys) == 0 {
			// Only a run stored before facts.db kept the join (schema
			// version 3) has such a watermark. Which of its repositories it
			// dates is unknown, and matching it against today's replica
			// could date the wrong ones, so the reader cannot tell, as
			// without the port.
			d.Log.Warn("the run recorded its GitHub data without the repositories it dates, so its pages date none: scan again",
				"run", run.ID)
			return nil, nil, nil
		}
	}
	return forgeRead(watermarks), watermarks, nil
}

// resolveRun finds the run a request names: its id, or the latest complete
// or partial scan for 0. Its not-found errors say what to do in their own
// words, which the command line prints alone, after the database's path
// (the chain of store operations below them goes to its debug log).
func resolveRun(ctx context.Context, s domain.FactStore, id int64) (domain.Run, error) {
	if id == 0 {
		run, err := s.LatestRun(ctx, domain.RunKindScan)
		if errors.Is(err, domain.ErrNotFound) {
			return run, fmt.Errorf("no complete scan in this facts database yet: run 'stillhere scan PATH...' first: %w", err)
		}
		if err != nil {
			return run, fmt.Errorf("finding the latest scan: %w", err)
		}
		return run, nil
	}
	run, err := s.Run(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		// The retention rule is the store's to state (codebase.md#run-model):
		// the sentence says only that it may be why.
		return run, fmt.Errorf("run %d: no complete or partial run has that id: it may have failed, or retention "+
			"may have deleted it; leave the run out to read the latest scan: %w", id, err)
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
	m, _, err := buildReport(ctx, d, r, true)
	return m, err
}

// buildReport is Report, with the stored run its model was built from, for
// an answer built on a report (Overview). scanRanks says whether a request
// that names no rank gets the tables the scan was asked to rank, as a
// report does; an overview holds a rank table only when it is asked for
// one, so an overview asked for none ranks nothing.
func buildReport(ctx context.Context, d Deps, r ReportRequest, scanRanks bool) (*report.Model, *stored, error) {
	d, err := d.ready("app.Report", false, false, true)
	if err != nil {
		return nil, nil, err
	}
	started := time.Now()
	ids := r.Rank
	ranked, err := resolveRanked(ids, d.Commands)
	if err != nil {
		return nil, nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, nil, err
	}
	loaded := time.Since(started).Milliseconds()
	if len(ids) == 0 && scanRanks {
		// The ranks the scan was asked for. One the registry no longer has
		// is left out rather than failing the report the user did not ask
		// to rank.
		for _, id := range parseRankOption(s.run.Options["rank"]) {
			m, err := resolveRanked([]domain.MetricID{id}, d.Commands)
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
		return nil, nil, err
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
		History: historyOf(s.facts), Timings: timings, Scope: s.scope(), Forge: s.forge, Commands: d.Commands,
	}), s, nil
}

// Explain returns one subject's values for one metric and, for a ranked
// metric and a person, each change behind them: the clusters the metric
// counted, which the bootstrap resamples.
func Explain(ctx context.Context, d Deps, r ExplainRequest) (*report.Explain, error) {
	d, err := d.ready("app.Explain", false, false, true)
	if err != nil {
		return nil, err
	}
	m, err := lookupMetric(r.Metric.String(), d.Commands)
	if err != nil {
		return nil, err
	}
	def := m.Definition()
	if r.Subject.Kind == domain.SubjectPerson && report.OwnPageOnly(def.ID) {
		// Explain is what a lead reads; a person's own AI-assisted share is
		// on their own page only. The command line cannot tell who asks, so
		// it refuses for everyone (ADR 0007).
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"%s about a person is shown only on their own page (ADR 0007): %s", def.ID, d.commands().OwnPage())}
	}
	if !slices.Contains(def.Subjects, r.Subject.Kind) {
		// No run holds an answer, so none is read.
		return nil, explainKindError(d.commands(), def, r.Subject.Kind)
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("%s %s has no values in run %d (a person is named by their canonical email, after .mailmap, "+
			"or by @login when the run links no email to that GitHub login): %w",
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
		Values: values, Clusters: clusters, Forge: s.forge, Commands: d.Commands,
	}), nil
}

// explainKindError refuses to explain a metric about a kind of subject no
// scan stores its values about (#69), such as durable@1, a person's, about
// a repository: an empty answer would read as "no data" when the question
// has no answer, and an agent could not tell the two apart. The kinds are
// the metric's own declaration (metrics.Definition.Subjects), not what one
// run happens to hold, so the same question is refused in every run, one
// with no value of the metric included. The error names them and the calls
// that answer instead, in the reader's words (cmds): explain about one of
// those kinds, where the reader can ask it, and the metric's rank tables,
// or its values per repository and group. The one metric a scan stores no
// value of is who_knows@1, which who-knows computes for any path, so a
// metric that declares no kind is sent there.
func explainKindError(cmds report.Commands, def metrics.Definition, kind string) error {
	id := def.ID.String()
	if len(def.Subjects) == 0 {
		return &domain.UsageError{Msg: fmt.Sprintf(
			"%s is computed per query, for any path, and never stored, so explain has no value of it. Ask instead: %s.",
			id, cmds.WhoKnows())}
	}
	var instead []string
	if how := cmds.Explain(id, def.Subjects); how != "" {
		instead = append(instead, how)
	}
	if def.Ranked {
		instead = append(instead, cmds.Ranks(id))
	} else {
		instead = append(instead, cmds.Report())
	}
	return &domain.UsageError{Msg: fmt.Sprintf("%s holds no value about %s: its values are about %s. Ask instead: %s.",
		id, explainKindOne(kind), explainKindsMany(def.Subjects), strings.Join(instead, "; "))}
}

// explainKindOne names one subject of a kind: "a person", "an area".
func explainKindOne(kind string) string {
	switch kind {
	case domain.SubjectPerson:
		return "a person"
	case domain.SubjectRepo:
		return "a repository"
	case domain.SubjectGroup:
		return "a group"
	case domain.SubjectArea:
		return "an area"
	case domain.SubjectChange:
		return "a change"
	}
	return "a " + kind
}

// explainKindsMany names subjects of several kinds, in their order:
// "people", "repositories, areas and groups".
func explainKindsMany(kinds []string) string {
	plural := map[string]string{domain.SubjectPerson: "people", domain.SubjectRepo: "repositories",
		domain.SubjectGroup: "groups", domain.SubjectArea: "areas", domain.SubjectChange: "changes"}
	var out []string
	for _, k := range kinds {
		out = append(out, cmp.Or(plural[k], k+"s"))
	}
	switch len(out) {
	case 0:
		return "nothing"
	case 1:
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

// WhoKnows returns who knows the code under a path: who_knows@1 computed
// over the run's facts under that path, so any prefix can be asked about,
// down to one file, without the run storing a value per directory.
func WhoKnows(ctx context.Context, d Deps, r WhoKnowsRequest) (*report.WhoKnows, error) {
	d, err := d.ready("app.WhoKnows", false, false, true)
	if err != nil {
		return nil, err
	}
	if _, err := lookupMetric(report.MetricWhoKnows, d.Commands); err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	repo := r.Repo
	if repo != "" && !hasRepo(s.facts, repo) {
		// A remote pasted with its credentials names its scrubbed key. The
		// error quotes only the scrubbed form: it is printed and may be
		// logged (docs/spec/git-invocation.md#url-scrubbing).
		repo = redact.URL(repo)
		if !hasRepo(s.facts, repo) {
			return nil, &domain.UsageError{Msg: fmt.Sprintf(
				"--repo %q is not a repository of run %d: it takes a repository key, as 'stillhere report --format json' lists them", repo, s.run.ID)}
		}
	}
	// The path is echoed in the answer, its header and its caveats, and a
	// URL pasted as PATH can carry a token: it is scrubbed once, here, and
	// the scrubbed form is the one asked about (#71).
	prefix := redact.Scrub(cleanPrefix(r.Path))
	// who_knows@1 is answered per query, for any prefix down to one file,
	// and never stored: a value for every directory of every repository
	// would outnumber the facts.
	values, err := metrics.WhoKnows(s.input(), repo, prefix)
	if err != nil {
		return nil, fmt.Errorf("computing %s: %w", report.MetricWhoKnows, err)
	}
	forge := s.forge
	if s.unkept {
		// The reviewers are computed from the pull requests, which this run
		// did not keep: the answer cannot tell, so it says nothing about
		// GitHub rather than list no reviewers as if there were none.
		forge = nil
	}
	run := s.run
	w := report.NewWhoKnows(&report.WhoKnowsInput{
		Run: &run, Facts: underPrefix(s.facts, prefix, repo), Who: s.who, Path: prefix, Repo: repo, Values: values,
		Forge: forge, Commands: d.Commands,
	})
	if c, ok := whoKnowsNoPath(s.facts, prefix, repo, run.ID); ok {
		w.Caveats = append(w.Caveats, c)
	}
	return w, nil
}

// whoKnowsNoPathNames is how many repositories that hold the path a
// whoKnowsNoPath caveat names before it counts the rest.
const whoKnowsNoPathNames = 3

// whoKnowsNoPath is the no-values caveat (report.NoValuesCaveat) of a
// who-knows answer about a path that no collected repository asked about
// has (repo, or every one for ""): none classified at its HEAD, blamed
// under the path or changed there by its history (holdsPath). The answer
// is then empty because the path names nothing there, and "nobody" would
// read as code no one knows (#71). It keeps exit 0: the question had an
// answer, and this is it.
//
// The caveat states what the run holds, and so suits every reader, who
// passes the path in their own form (the command line takes it relative to
// a repository's root, the MCP server an absolute path in a clone too): the
// repositories asked about lack it; when --repo or repo named one, the
// run's other repositories that have it, since then the choice of
// repository, not the form of the path, is what missed; and otherwise that
// the run knows each path relative to its repository's root, the usual miss
// being a path from the filesystem. A run none of whose repositories asked
// about was collected says so with its failed caveats instead, and the
// whole repository (prefix "") is never missing.
func whoKnowsNoPath(fs *domain.FactSet, prefix, repo string, runID int64) (report.Caveat, bool) {
	if prefix == "" {
		return report.Caveat{}, false
	}
	var asked, holding []string // collected repositories asked about, and others of the run that hold the path
	collected := 0
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		if rf.Status != domain.RepoStatusOK {
			continue
		}
		collected++
		has := holdsPath(rf, prefix)
		switch {
		case repo == "" || rf.Repo.Key == repo:
			if has {
				return report.Caveat{}, false
			}
			asked = append(asked, rf.Repo.Name)
		case has:
			holding = append(holding, rf.Repo.Name)
		}
	}
	if len(asked) == 0 {
		return report.Caveat{}, false
	}
	const missing = ", so this answer is missing, not a real \"nobody\": "
	if len(holding) > 0 {
		return report.NoValuesCaveat(fmt.Sprintf("Repository %s of run %d has no path under %q",
			asked[0], runID, prefix) + missing + whoKnowsHolding(holding) + "."), true
	}
	lead := fmt.Sprintf("No repository of run %d has a path under %q", runID, prefix)
	if collected == 1 {
		lead = fmt.Sprintf("Repository %s of run %d has no path under %q", asked[0], runID, prefix)
	}
	return report.NoValuesCaveat(lead + missing +
		"the run knows each path relative to its repository's root, such as src or src/pay.go."), true
}

// whoKnowsHolding names the repositories that hold a path another
// repository lacks, in the run's order, and counts those past
// whoKnowsNoPathNames when there are two or more of them: "repository
// fixture has one", "repositories a, b and c have one", "repositories a,
// b, c and 2 others have one".
func whoKnowsHolding(names []string) string {
	switch n := len(names); {
	case n == 1:
		return "repository " + names[0] + " has one"
	case n > whoKnowsNoPathNames+1:
		return fmt.Sprintf("repositories %s and %d others have one",
			strings.Join(names[:whoKnowsNoPathNames], ", "), n-whoKnowsNoPathNames)
	default:
		return "repositories " + strings.Join(names[:n-1], ", ") + " and " + names[n-1] + " have one"
	}
}

// holdsPath reports whether a repository's facts have a path under prefix:
// one classified at its HEAD, a line blamed under it, or a change its
// history made there, a rename's old path included. These are the facts
// underPrefix keeps.
func holdsPath(rf *domain.RepoFacts, prefix string) bool {
	for _, pc := range rf.Paths {
		if under(pc.Path, prefix) {
			return true
		}
	}
	for _, o := range rf.Owners {
		if under(o.Path, prefix) {
			return true
		}
	}
	for _, ch := range rf.Changes {
		if under(ch.Path, prefix) || (ch.OldPath != "" && under(ch.OldPath, prefix)) {
			return true
		}
	}
	return false
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
// scope. That is the facts a lead sees about them (ADR 0001), plus their
// own AI-assisted share, which only this page shows (ADR 0007). Who may
// open whose page is the web server's to enforce (M3): the command line
// has no viewer identity.
func Me(ctx context.Context, d Deps, r MeRequest) (*report.Me, error) {
	return personPage(ctx, d, "me", r.RunID, r.Email, true)
}

// personPage builds a person's page for Me (own) and Person (a lead's view
// of them, without their own AI-assisted share), so the two cannot drift:
// one reads what the other does, from the same run, with the same GitHub
// dating.
func personPage(ctx context.Context, d Deps, cmd string, runID int64, email string, own bool) (*report.Me, error) {
	op := "app.Me"
	if !own {
		op = "app.Person"
	}
	d, err := d.ready(op, false, false, true)
	if err != nil {
		return nil, err
	}
	// Every person key is lowercased where it is made, an email by the
	// collectors and a GitHub login by identity.Reviewer (#69), so the key
	// a reader passes is made canonical the same way, whatever its case.
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, &domain.UsageError{Msg: cmd + " needs the person's email"}
	}
	s, err := load(ctx, d, runID)
	if err != nil {
		return nil, err
	}
	subject := domain.Subject{Kind: domain.SubjectPerson, Key: email}
	var values []domain.MetricValue
	for _, v := range s.values {
		if v.Subject == subject && !report.OwnPageOnly(v.Metric) {
			values = append(values, v)
		}
	}
	if own {
		// A person's own AI-assisted share is never stored: it is computed
		// for this page alone (ADR 0007), and NewMe and the renderers label
		// it (report.OwnPageOnly).
		share, err := metrics.PersonalAIShare(s.input(), email)
		if err != nil {
			return nil, fmt.Errorf("computing the AI-assisted share: %w", err)
		}
		values = append(values, share...)
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
		Run: &run, Facts: s.facts, Who: s.who, Email: email, Values: values, Ranks: sets, Forge: s.forge,
		Metrics: Metrics().Metrics, Commands: d.Commands,
	}), nil
}
