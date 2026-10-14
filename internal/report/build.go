package report

import (
	"cmp"
	"slices"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// Input is what Build arranges into a Model.
type Input struct {
	Kind    string               // KindWeek or KindScan
	Tool    string               // the version of the binary building the report
	Run     *domain.Run          // the stored run, with its final status; nil for week, and for a scan that stored nothing
	Facts   *domain.FactSet      // the facts the values were computed from
	Policy  *policy.Policy       // the policy the facts were collected under
	Who     People               // display names and bots, over Facts
	Values  []domain.MetricValue // every value computed over Facts
	Ranks   []RankSet            // rank intervals for the metrics named by --rank, in the order named
	Landed  Landed               // how the landed@1 window was set
	History domain.Window        // the landing window the history covers
	Timings *Timings
}

// Landed is how the landed@1 window was set: by the command line, or by the
// policy's [week] table.
type Landed struct {
	Days        int
	DaysFromCLI bool
	Sort        string // "lines_added" or "commits"
	SortFromCLI bool
}

// RankSet is one ranked metric's intervals within one scope, as the ranking
// use case computed them with rank.Intervals.
type RankSet struct {
	Metric    MetricInfo
	Scope     domain.Scope
	Window    domain.Window         // the metric's own window, which its values share
	Percent   bool                  // a ratio metric: estimates print as percentages
	MinN      int                   // the metric's eligibility threshold
	Intervals []domain.RankInterval // people only: bots are never ranked
}

// The values of Window.DaysFrom and Window.SortFrom.
const (
	fromCLI    = "cli"
	fromPolicy = "policy"
)

// Build arranges a run's facts and metric values into a report. It computes
// no metric: every number about a person is read from in.Values, as
// values.go lays out, and the counts it takes from facts (files, landings,
// sizes) need no identity or policy rule. A section whose metric produced
// no values is left out, and the report's caveats say so.
func Build(in *Input) *Model {
	b := newBuilder(in)
	fs := in.Facts
	m := &Model{
		Schema:  Schema,
		Kind:    in.Kind,
		Tool:    Tool{Name: "stillhere", Version: in.Tool},
		Now:     fs.Now,
		Policy:  Policy{Version: fs.Policy.Version, SHA256: fs.Policy.SHA256},
		Window:  b.landedWindow(),
		History: intervalOf(in.History),
		Timings: in.Timings,
	}
	if in.Policy != nil {
		m.Policy.Headline = in.Policy.Headline.Metric
	}
	if r := in.Run; r != nil {
		m.Run = &Run{ID: r.ID, Status: string(r.Status), ToolVersion: r.ToolVersion, Options: copyOptions(r.Options)}
	}
	for _, g := range b.groups {
		m.Groups = append(m.Groups, Group{Name: g.Name, Path: g.Path, Repos: slices.Clone(g.Repos)})
	}
	m.Warnings = warningsOf(fs.Warnings)
	for _, rf := range b.repos {
		m.Repos = append(m.Repos, b.repo(rf))
	}
	for _, g := range b.groups {
		m.Overviews = append(m.Overviews, b.overview(g))
	}
	for _, rs := range in.Ranks {
		m.Ranks = append(m.Ranks, b.rankTable(rs))
	}
	m.Caveats = b.runCaveats(m)
	m.Metrics = b.metricIDs()
	return m
}

// builder holds what every section of one report needs.
type builder struct {
	in     *Input
	ix     valueIndex
	repos  []*domain.RepoFacts // sorted by group, name and key
	byKey  map[string]*domain.RepoFacts
	groups []domain.Group // sorted by name, members limited to the run's repositories
	landed Interval       // the landed window
	used   map[string]bool
}

func newBuilder(in *Input) *builder {
	fs := in.Facts
	b := &builder{in: in, ix: newValueIndex(in.Values), byKey: map[string]*domain.RepoFacts{}, used: map[string]bool{}}
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		b.repos = append(b.repos, rf)
		b.byKey[rf.Repo.Key] = rf
	}
	slices.SortFunc(b.repos, func(x, y *domain.RepoFacts) int {
		return cmp.Or(
			cmp.Compare(x.Repo.Group, y.Repo.Group),
			cmp.Compare(x.Repo.Name, y.Repo.Name),
			cmp.Compare(x.Repo.Key, y.Repo.Key),
		)
	})
	for _, g := range fs.Groups {
		var keys []string
		for _, k := range g.Repos {
			if b.byKey[k] != nil {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		g.Repos = keys
		b.groups = append(b.groups, g)
	}
	slices.SortFunc(b.groups, func(x, y domain.Group) int { return cmp.Compare(x.Name, y.Name) })
	days := time.Duration(in.Landed.Days) * 24 * time.Hour
	b.landed = Interval{Since: fs.Now.Add(-days), Until: fs.Now}
	return b
}

func (b *builder) landedWindow() Window {
	l := b.in.Landed
	w := Window{
		Days: l.Days, DaysFrom: fromPolicy, Sort: l.Sort, SortFrom: fromPolicy,
		Since: b.landed.Since, Until: b.landed.Until,
		DateField: "committer", Walk: "first-parent", Diff: "first-parent",
	}
	if l.DaysFromCLI {
		w.DaysFrom = fromCLI
	}
	if l.SortFromCLI {
		w.SortFrom = fromCLI
	}
	return w
}

// use records that a section printed a metric's values, for the report's
// list of metric versions.
func (b *builder) use(ids ...string) {
	for _, id := range ids {
		b.used[id] = true
	}
}

func (b *builder) metricIDs() []string {
	ids := make(map[string]bool, len(b.used))
	for id := range b.used {
		ids[id] = true
	}
	for _, rs := range b.in.Ranks {
		ids[rs.Metric.ID] = true
	}
	var out []string
	for id := range ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func (b *builder) name(email string) string { return displayName(b.in.Who, email) }

func (b *builder) bot(email string) bool { return isBot(b.in.Who, email) }

// repo builds one repository's part of the report.
func (b *builder) repo(rf *domain.RepoFacts) Repo {
	r := Repo{
		Name: rf.Repo.Name, Key: rf.Repo.Key, Group: rf.Repo.Group, Path: rf.Repo.Path,
		Remote: rf.Repo.Remote, Branch: rf.Repo.Branch, Head: rf.Repo.Head,
		Bare: rf.Repo.Bare, Shallow: rf.Repo.Shallow, Partial: rf.Repo.Partial,
		Status: rf.Status, Error: rf.Error, LineFacts: rf.LineFacts,
		Warnings: warningsOf(rf.Warnings),
		Skipped:  skippedOf(rf.Paths),
		IgnoreRevs: IgnoreRevs{
			Source: rf.IgnoreRevs.Source, SHA256: rf.IgnoreRevs.SHA256, Revs: slices.Clone(rf.IgnoreRevs.Revs),
		},
	}
	for _, e := range rf.BlameErrors {
		r.Errors.Blame = append(r.Errors.Blame, FileError{Path: e.Path, Message: e.Message})
	}
	slices.SortFunc(r.Errors.Blame, func(x, y FileError) int {
		return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.Message, y.Message))
	})
	scope := domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}
	if rf.Status == domain.RepoStatusOK {
		r.Week = b.weekTable(scope)
		if rf.LineFacts {
			r.StillHere = b.stillHere(scope, filesBlamed(rf))
		}
	}
	r.Card = b.card(rf, r.StillHere)
	return r
}

// card builds a repository's card.
func (b *builder) card(rf *domain.RepoFacts, still *StillHere) RepoCard {
	scope := domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}
	var used []string
	c := RepoCard{Size: sizeOf(rf), Landings: b.landingsOf(rf)}
	if !rf.FirstCommit.IsZero() {
		c.Age = &Age{FirstCommit: rf.FirstCommit, Days: int(b.in.Facts.Now.Sub(rf.FirstCommit) / (24 * time.Hour))}
	}
	if still != nil {
		used = append(used, MetricSurviving)
		total := 0
		for _, r := range still.Humans {
			total += r.ProdNonblankLines
		}
		for _, r := range still.Bots {
			total += r.ProdNonblankLines
		}
		for _, r := range still.Humans {
			if r.ProdNonblankLines == 0 {
				continue
			}
			c.People++
			if len(c.MadeOf) < 5 {
				c.MadeOf = append(c.MadeOf, Author{
					Email: r.Email, Name: r.Name, ProdLines: r.ProdNonblankLines,
					Share: float64(r.ProdNonblankLines) / float64(total),
				})
			}
		}
		for _, r := range still.Bots {
			if r.ProdNonblankLines > 0 {
				c.Bots++
			}
		}
		c.BotShare = botShare(still)
	}
	if c.Concentration = b.concentration(scope); c.Concentration != nil {
		used = append(used, MetricConcentration)
	}
	if c.KnowledgeLoss = b.share(MetricKnowledgeLoss, scope, nil); c.KnowledgeLoss != nil {
		used = append(used, MetricKnowledgeLoss)
	}
	if c.AIAssisted = b.share(MetricAIAssisted, scope, nil); c.AIAssisted != nil {
		used = append(used, MetricAIAssisted)
	}
	b.use(used...)
	c.Provenance = b.provenance([]*domain.RepoFacts{rf}, used)
	// A card is read on its own (the MCP server hands one to an agent), so
	// it carries the standing caveats too; the text and Markdown renderers
	// print those once, at the top of the report.
	c.Caveats = append([]Caveat{notFetched()}, stripRepos(repoCaveats(rf))...)
	if c.AIAssisted != nil {
		c.Caveats = append(c.Caveats, aiLowerBound())
	}
	return c
}

// overview builds one group's platform view.
func (b *builder) overview(g domain.Group) Overview {
	scope := domain.Scope{Kind: domain.ScopeGroup, Key: g.Name}
	members := b.members(g.Repos)
	o := Overview{Scope: scopeOf(scope), Totals: b.totals(members)}
	var used []string
	lineFacts, ok := false, false
	blamed := 0
	for _, rf := range members {
		lineFacts = lineFacts || rf.LineFacts
		ok = ok || rf.Status == domain.RepoStatusOK
		blamed += filesBlamed(rf)
	}
	if ok {
		o.Week = b.weekTable(scope)
		used = append(used, MetricLanded)
	}
	if lineFacts {
		o.People = b.stillHere(scope, blamed)
		o.BotShare = botShare(o.People)
		used = append(used, MetricSurviving)
	}
	if o.Concentration = b.concentration(scope); o.Concentration != nil {
		used = append(used, MetricConcentration)
	}
	if o.KnowledgeLoss = b.share(MetricKnowledgeLoss, scope, members); o.KnowledgeLoss != nil {
		used = append(used, MetricKnowledgeLoss)
	}
	if o.AIAssisted = b.share(MetricAIAssisted, scope, members); o.AIAssisted != nil {
		used = append(used, MetricAIAssisted)
	}
	bots := map[string]bool{}
	if o.People != nil {
		for _, r := range o.People.Humans {
			if r.ProdNonblankLines > 0 {
				o.Totals.People++
			}
		}
		for _, r := range o.People.Bots {
			if r.ProdNonblankLines > 0 {
				bots[r.Email] = true
			}
		}
	}
	if o.Week != nil {
		o.Totals.Landers = len(o.Week.Humans)
		for _, r := range o.Week.Bots {
			bots[r.Email] = true
		}
	}
	o.Totals.Bots = len(bots)
	b.use(used...)
	o.Provenance = b.provenance(members, used)
	o.Caveats = b.scopeCaveats(members, scope, o.AIAssisted != nil)
	return o
}

// members returns the run's repositories with the given keys, in report
// order.
func (b *builder) members(keys []string) []*domain.RepoFacts {
	in := make(map[string]bool, len(keys))
	for _, k := range keys {
		in[k] = true
	}
	var out []*domain.RepoFacts
	for _, rf := range b.repos {
		if in[rf.Repo.Key] {
			out = append(out, rf)
		}
	}
	return out
}

// scopeMembers returns the repositories a scope covers.
func (b *builder) scopeMembers(s domain.Scope) []*domain.RepoFacts {
	switch s.Kind {
	case domain.ScopeRepo:
		return b.members([]string{s.Key})
	case domain.ScopeGroup:
		for _, g := range b.groups {
			if g.Name == s.Key {
				return b.members(g.Repos)
			}
		}
		return nil
	default:
		return b.repos
	}
}

func (b *builder) totals(members []*domain.RepoFacts) Totals {
	t := Totals{Repos: len(members)}
	for _, rf := range members {
		if rf.Status == domain.RepoStatusOK {
			t.ReposOK++
		} else {
			t.ReposFailed++
		}
		s := sizeOf(rf)
		t.ProdFiles += s.ProdFiles
		t.ProdLines += s.ProdLines
		l := b.landingsOf(rf)
		t.LandingsWindow += l.Window
		t.LandingsHistory += l.History
	}
	return t
}

// weekTable reads landed@1 in a scope.
func (b *builder) weekTable(scope domain.Scope) *WeekTable {
	t := &WeekTable{}
	for _, v := range b.ix.people(MetricLanded, scope) {
		email := v.Subject.Key
		row := WeekRow{
			Email: email, Name: b.name(email), Bot: b.bot(email),
			Commits:        count(v.Value),
			LinesAdded:     count(v.Detail[detailLinesAdded]),
			ProdLinesAdded: count(v.Detail[detailProdLinesAdded]),
		}
		if row.Bot {
			t.Bots = append(t.Bots, row)
		} else {
			t.Humans = append(t.Humans, row)
		}
	}
	byCommits := b.in.Landed.Sort == "commits"
	order := func(x, y WeekRow) int {
		if byCommits {
			return cmp.Or(cmp.Compare(y.Commits, x.Commits), cmp.Compare(x.Email, y.Email))
		}
		return cmp.Or(cmp.Compare(y.LinesAdded, x.LinesAdded), cmp.Compare(x.Email, y.Email))
	}
	slices.SortFunc(t.Humans, order)
	slices.SortFunc(t.Bots, order)
	b.use(MetricLanded)
	return t
}

// stillHere reads surviving@1 in a scope.
func (b *builder) stillHere(scope domain.Scope, blamed int) *StillHere {
	t := &StillHere{FilesBlamed: blamed}
	for _, v := range b.ix.people(MetricSurviving, scope) {
		email := v.Subject.Key
		majority, fromMajority := b.ix.majorityFiles(v)
		if fromMajority {
			b.use(MetricMajority)
		}
		row := StillHereRow{
			Email: email, Name: b.name(email), Bot: b.bot(email),
			ProdNonblankLines: count(v.Value),
			MajorityFiles:     majority,
			LinesByRole:       linesByRole(v.Detail),
			Repos:             count(v.Detail[detailRepos]),
			Landings365d:      count(v.Detail[detailLandings365d]),
		}
		if row.Bot {
			t.Bots = append(t.Bots, row)
		} else {
			t.Humans = append(t.Humans, row)
		}
	}
	order := func(x, y StillHereRow) int {
		return cmp.Or(
			cmp.Compare(y.ProdNonblankLines, x.ProdNonblankLines),
			cmp.Compare(y.MajorityFiles, x.MajorityFiles),
			cmp.Compare(x.Email, y.Email),
		)
	}
	slices.SortFunc(t.Humans, order)
	slices.SortFunc(t.Bots, order)
	b.use(MetricSurviving)
	return t
}

// concentration reads knowledge_concentration@1 about a scope as a whole.
func (b *builder) concentration(scope domain.Scope) *Concentration {
	v, ok := b.ix.aggregate(MetricConcentration, scope)
	if !ok {
		return nil
	}
	return &Concentration{
		Metric:            MetricConcentration,
		FilesWithMajority: count(v.Detail[detailFilesWithMajority]),
		AuthorsInPrefix:   count(v.Value),
		BotsInPrefix:      count(v.Detail[detailBotsInPrefix]),
		Label:             ConcentrationLabel,
	}
}

// share reads a ratio metric about a scope as a whole. When the metric has
// no value about a group itself, the group's share is the ratio of its
// members' summed parts, which is exact for these metrics: their parts are
// counts (lines, landings) that add across repositories.
func (b *builder) share(metric string, scope domain.Scope, members []*domain.RepoFacts) *Share {
	if v, ok := b.ix.aggregate(metric, scope); ok {
		return &Share{Metric: metric, Num: v.Num, Den: v.Den, Value: v.Value}
	}
	if scope.Kind == domain.ScopeRepo {
		return nil
	}
	s := Share{Metric: metric}
	found := false
	for _, rf := range members {
		if v, ok := b.ix.aggregate(metric, domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}); ok {
			s.Num += v.Num
			s.Den += v.Den
			found = true
		}
	}
	if !found || s.Den == 0 {
		return nil
	}
	s.Value = s.Num / s.Den
	return &s
}

// botShare is the share of a scope's surviving prod lines held by bots.
func botShare(t *StillHere) *Share {
	s := Share{Metric: MetricSurviving}
	for _, r := range t.Humans {
		s.Den += float64(r.ProdNonblankLines)
	}
	for _, r := range t.Bots {
		s.Num += float64(r.ProdNonblankLines)
		s.Den += float64(r.ProdNonblankLines)
	}
	if s.Den == 0 {
		return nil
	}
	s.Value = s.Num / s.Den
	return &s
}

// rankTable lays out one ranked metric in one scope.
func (b *builder) rankTable(rs RankSet) RankTable {
	t := RankTable{
		Metric: rs.Metric.ID, Question: rs.Metric.Question,
		Scope: scopeOf(rs.Scope), Window: intervalOf(rs.Window),
		Percent: rs.Percent, MinN: rs.MinN,
	}
	for _, ri := range rs.Intervals {
		if ri.Subject.Kind != domain.SubjectPerson {
			continue
		}
		email := ri.Subject.Key
		row := RankRow{
			Email: email, Name: b.name(email),
			RankLo: ri.RankLo, RankHi: ri.RankHi,
			Estimate: ri.Estimate, Lo: ri.Lo, Hi: ri.Hi, N: ri.N,
		}
		switch {
		case b.bot(email):
			row.RankLo, row.RankHi = 0, 0
			t.Bots = append(t.Bots, row)
		case ri.Eligible:
			t.Ranked = append(t.Ranked, row)
		default:
			row.RankLo, row.RankHi = 0, 0
			t.BelowMin = append(t.BelowMin, row)
		}
	}
	listed := map[string]bool{}
	for _, r := range t.Bots {
		listed[r.Email] = true
	}
	for _, v := range b.ix.people(rs.Metric.ID, rs.Scope) {
		if email := v.Subject.Key; b.bot(email) && !listed[email] && sameWindow(v.Window, rs.Window) {
			t.Bots = append(t.Bots, RankRow{Email: email, Name: b.name(email), Estimate: v.Value, Lo: v.Value, Hi: v.Value, N: v.N})
		}
	}
	slices.SortFunc(t.Ranked, func(x, y RankRow) int {
		return cmp.Or(
			cmp.Compare(x.RankLo, y.RankLo),
			cmp.Compare(x.RankHi, y.RankHi),
			cmp.Compare(y.Estimate, x.Estimate),
			cmp.Compare(x.Email, y.Email),
		)
	})
	byN := func(x, y RankRow) int {
		return cmp.Or(cmp.Compare(y.N, x.N), cmp.Compare(y.Estimate, x.Estimate), cmp.Compare(x.Email, y.Email))
	}
	slices.SortFunc(t.BelowMin, byN)
	slices.SortFunc(t.Bots, byN)
	members := b.scopeMembers(rs.Scope)
	t.Provenance = b.provenance(members, []string{rs.Metric.ID})
	t.Caveats = b.scopeCaveats(members, rs.Scope, false)
	if rs.Metric.Validity != "" {
		t.Caveats = append(t.Caveats, Caveat{Code: codeValidity, Message: "Validity: " + rs.Metric.Validity})
	}
	return t
}

// sameWindow compares windows by instant: a time read back from the store
// may carry another location than the one computed.
func sameWindow(a, b domain.Window) bool {
	return a.Since.Equal(b.Since) && a.Until.Equal(b.Until)
}

// provenance says where a section that covers repos and reads metrics comes
// from.
func (b *builder) provenance(repos []*domain.RepoFacts, metrics []string) Provenance {
	p := Provenance{AsOf: b.in.Facts.Now, Policy: b.in.Facts.Policy.SHA256}
	if b.in.Run != nil {
		p.RunID = b.in.Run.ID
	}
	for _, rf := range repos {
		if rf.Repo.Head != "" {
			p.Heads = append(p.Heads, Head{Repo: rf.Repo.Name, Key: rf.Repo.Key, Branch: rf.Repo.Branch, SHA: rf.Repo.Head})
		}
	}
	ids := slices.Clone(metrics)
	slices.Sort(ids)
	p.Metrics = slices.Compact(ids)
	return p
}

// filesBlamed counts the paths blame read without an error: every path at
// HEAD that received a role, less those with a blame error.
func filesBlamed(rf *domain.RepoFacts) int {
	if !rf.LineFacts {
		return 0
	}
	failed := map[string]bool{}
	for _, e := range rf.BlameErrors {
		failed[e.Path] = true
	}
	n := 0
	for _, pc := range rf.Paths {
		if pc.AtHead && pc.Role != "" && !failed[pc.Path] {
			n++
		}
	}
	return n
}

// sizeOf measures a repository at HEAD from its facts.
func sizeOf(rf *domain.RepoFacts) Size {
	var s Size
	prod := map[string]bool{}
	for _, pc := range rf.Paths {
		if !pc.AtHead {
			continue
		}
		s.Files++
		if pc.Role == domain.RoleProd {
			s.ProdFiles++
			prod[pc.Path] = true
		}
	}
	for _, o := range rf.Owners {
		if prod[o.Path] {
			s.ProdLines += o.Lines
		}
	}
	return s
}

// landingsOf counts a repository's landings in the landed window and in the
// history window.
func (b *builder) landingsOf(rf *domain.RepoFacts) Landings {
	w := domain.Window{Since: b.landed.Since, Until: b.landed.Until}
	l := Landings{History: len(rf.Landings)}
	for _, ld := range rf.Landings {
		if w.Contains(ld.Time) {
			l.Window++
		}
	}
	return l
}

func skippedOf(paths []domain.PathClass) Skipped {
	var s Skipped
	for _, pc := range paths {
		switch pc.Skip {
		case domain.SkipDeniedPath:
			s.DeniedPath++
		case domain.SkipTooBig:
			s.TooBig++
		case domain.SkipGeneratedHeader:
			s.GeneratedHeader++
		case domain.SkipUnclassified:
			s.Unclassified++
		}
	}
	return s
}

func warningsOf(ws []domain.Warning) []Warning {
	var out []Warning
	for _, w := range ws {
		out = append(out, Warning{Code: w.Code, Message: w.Message})
	}
	slices.SortFunc(out, func(x, y Warning) int {
		return cmp.Or(cmp.Compare(x.Code, y.Code), cmp.Compare(x.Message, y.Message))
	})
	return out
}

func copyOptions(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
