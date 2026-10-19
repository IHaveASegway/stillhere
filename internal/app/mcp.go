package app

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The MCP server's read use cases (#15, ADR 0006): the questions an agent
// asks about a platform (a group of repositories) or one repository. Like
// the command line's read use cases in read.go, each one reads the latest
// complete or partial run unless a run id is given, with the run's own
// policy, and computes nothing that changes the run. Every number comes
// from the stored values, or from a ranked metric's own clusters as explain
// reads them, so an answer here never disagrees with a report, a rank
// table or an explanation of the same run.
//
// The answers are report types, composed for an agent's questions, and a
// few types of their own for what no report shows (changes, highlights, a
// diff), which carry their own JSON names: docs/spec/mcp.md is their
// published shape. Paging, the concise and detailed forms, and keeping
// emails, commit subjects and source away from the agent's model provider
// are the adapter's (internal/mcp): a use case here returns the whole
// answer, with commit subjects only when asked (Titles), except Diff, which
// only a user who let source leave the machine gets.

// The metric versions the change answers read: a move to a new version is
// a reviewed change, as in report/values.go.
const (
	mcpDurable     = "durable@1"
	mcpSimplifying = "simplifying@1"
)

// mcpSimplifyingDays is simplifying@1's window, which a caveat names: the
// metric owns the number (docs/metrics.md#simplifying1).
const mcpSimplifyingDays = 90

// mcpHighlightsTop is how many changes each list of the highlights holds.
const mcpHighlightsTop = 5

// NamesRequest asks for the vocabulary of a stored run.
type NamesRequest struct {
	RunID int64 // 0: the latest complete or partial scan run
}

// Names is a stored run's vocabulary: the groups, repositories and people
// an inbound adapter resolves an agent's names against, so that an agent
// can ask about "Lena Lead" or "payments" rather than an email or a key,
// and what it needs to decide what may leave the machine.
type Names struct {
	Run    domain.Run
	Groups []NamedGroup  // by name
	Repos  []NamedRepo   // by group, name and key, as reports list them
	People []NamedPerson // by name, then key
	// AIShares is the run's one decision about which AI-assisted shares may
	// leave the machine (aishare.go), which every answer about the run reads.
	AIShares AIShares
}

// NamedGroup is one group of a run.
type NamedGroup struct {
	Name  string
	Repos []string // Repo.Key of its repositories, sorted
}

// NamedRepo is one repository of a run.
type NamedRepo struct {
	Key, Name string
	Group     string // "" when the repository was named directly
	Path      string // the absolute path the run read it at, for resolving an absolute path inside it
	// Forge (wave 11, round 3's KT-2) are the names, "host/owner/name" as
	// domain.RemoteRepo gives them, that the run's GitHub data gives the
	// repository: the forge repository joined to it and the names GitHub
	// resolves to that one (domain.ForgeRepo.Aliases), which MatchRepos
	// matches OWNER/NAME by when the origin does not; nil without GitHub
	// data, and in a StoredRun, which reads no facts.
	Forge []string
}

// withForgeNames sets each repository's Forge names from the fact set's
// GitHub data, by the join the run counts by (domain.ForgeFacts.RunJoin).
func withForgeNames(repos []NamedRepo, fs *domain.FactSet) {
	if fs.Forge == nil {
		return
	}
	join := fs.Forge.RunJoin(fs.Repos)
	names := map[string][]string{} // Repo.Key → its forge names
	for _, r := range fs.Forge.Repos {
		name, ok := domain.RemoteRepo(r.Key)
		if !ok {
			continue
		}
		for _, key := range join[r.ID] {
			names[key] = append(names[key], name)
			names[key] = append(names[key], domain.ForgeAliasNames(r)...)
		}
	}
	for i := range repos {
		repos[i].Forge = names[repos[i].Key]
	}
}

// NamedPerson is someone a run knows: an author, a co-author, or anyone a
// metric holds a value about, such as a reviewer the forge could not link
// to an email.
type NamedPerson struct {
	Key   string   // the canonical email, or "@login", lowercased, for a reviewer the forge could not link
	Name  string   // as the reports' JSON gives it (report.SubjectOf): the display name, the login as GitHub shows it, or "" when none may be shown, so only an id or an email finds them
	Bot   bool     // a bot or an agent: listed apart, never ranked
	Repos []string // names of the repositories they wrote in or hold a value about, sorted
	Lines int      // surviving@1: their surviving prod lines across the run; 0 without a value
	// Logins are the GitHub logins the run links to the person
	// (identity.Directory.Logins, the links every metric that reads
	// reviews counts by), as GitHub shows them: an adapter resolves
	// "@login" to the person by them, without case. nil for none, and for a
	// reviewer keyed by login.
	Logins []string
	// ShownLogins are those of Logins an answer may print beside the
	// person: none that gives an address of the run away
	// (identity.Directory.LoginWithheld), as a display name that does is
	// printed nowhere.
	ShownLogins []string
	// aliases are the emails git's mailmap rewrote to the person's key in
	// the run's commits (domain.RepoFacts.Aliases), sorted: an id (or an
	// email) an earlier answer gave before a .mailmap change names them by
	// one (FindPerson). Unexported: no adapter sends them anywhere.
	aliases []string
}

// RunNames returns a stored run's vocabulary: its groups, repositories and
// people with their display names.
func RunNames(ctx context.Context, d Deps, r NamesRequest) (*Names, error) {
	d, err := d.ready("app.RunNames", false, false, true)
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	return runNames(s)
}

// runNames is a stored run's vocabulary, for RunNames and for a use case
// that resolves a reader's name for a person in the run it loaded
// (FindPerson: explain and me on the command line, PF-7).
func runNames(s *stored) (*Names, error) {
	n := &Names{Run: s.run}
	repos := make([]domain.Repo, 0, len(s.facts.Repos))
	repoName := make(map[string]string, len(s.facts.Repos))
	for _, rf := range s.facts.Repos {
		repos = append(repos, rf.Repo)
		repoName[rf.Repo.Key] = rf.Repo.Name
	}
	n.Groups, n.Repos = mcpNamedScopes(s.facts.Groups, repos)
	withForgeNames(n.Repos, s.facts)

	type acc struct {
		p     NamedPerson
		repos map[string]bool // repository names
	}
	people := map[string]*acc{}
	// Keys are taken as load hands them, never made canonical again here:
	// every key is lowercased where it is made, an email by the collectors
	// and a login by identity.Reviewer (#69), and load upcasts those of a
	// run stored before (canonicalKeys), so a key this vocabulary hands an
	// adapter always joins the run's values.
	add := func(key string) *acc {
		if key == "" {
			return nil
		}
		a := people[key]
		if a == nil {
			a = &acc{p: mcpPersonNamed(key, s), repos: map[string]bool{}}
			people[key] = a
		}
		return a
	}
	for _, v := range s.values {
		if v.Subject.Kind != domain.SubjectPerson {
			continue
		}
		a := add(v.Subject.Key)
		if a == nil {
			continue
		}
		if v.Scope.Kind == domain.ScopeRepo && repoName[v.Scope.Key] != "" {
			a.repos[repoName[v.Scope.Key]] = true
		}
		if v.Metric.String() == report.MetricSurviving && v.Scope.Kind == domain.ScopeAll {
			a.p.Lines = int(v.Value)
		}
	}
	for _, rf := range s.facts.Repos {
		for _, c := range rf.Commits {
			for _, p := range append([]domain.Person{c.Author}, c.CoAuthors...) {
				if a := add(p.Email); a != nil {
					a.repos[rf.Repo.Name] = true
				}
			}
		}
	}
	for _, rf := range s.facts.Repos {
		for _, al := range rf.Aliases {
			if a := people[al.Canonical]; a != nil && al.Email != al.Canonical && !slices.Contains(a.p.aliases, al.Email) {
				a.p.aliases = append(a.p.aliases, al.Email)
			}
		}
	}
	for _, a := range people {
		if len(a.repos) > 0 {
			a.p.Repos = slices.Sorted(maps.Keys(a.repos))
		}
		slices.Sort(a.p.aliases)
		n.People = append(n.People, a.p)
	}
	slices.SortFunc(n.People, func(a, b NamedPerson) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Key, b.Key)) })

	n.AIShares = aiShares(s)
	return n, nil
}

// mcpNamedScopes is a run's groups and repositories as an adapter names
// them, sorted as Names lists them.
func mcpNamedScopes(groups []domain.Group, repos []domain.Repo) ([]NamedGroup, []NamedRepo) {
	var ng []NamedGroup
	for _, g := range groups {
		ng = append(ng, NamedGroup{Name: g.Name, Repos: slices.Sorted(slices.Values(g.Repos))})
	}
	slices.SortFunc(ng, func(a, b NamedGroup) int { return cmp.Compare(a.Name, b.Name) })
	var nr []NamedRepo
	for _, r := range repos {
		nr = append(nr, NamedRepo{Key: r.Key, Name: r.Name, Group: r.Group, Path: r.Path})
	}
	slices.SortFunc(nr, func(a, b NamedRepo) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Key, b.Key))
	})
	return ng, nr
}

// StoredRun is one stored scan run as an adapter names its scopes: its
// groups and repositories, without its people or facts.
type StoredRun struct {
	Run    domain.Run
	Groups []NamedGroup // as Names lists them
	Repos  []NamedRepo  // as Names lists them
}

// StoredRuns returns every complete or partial scan run that facts.db
// holds, newest first, by its scopes (MC-17): so an adapter whose agent
// names a group or a repository that the run it reads does not hold can
// say which run does, and how the user serves it. It reads no facts, so it
// costs a few rows per run, which retention bounds (about 20 runs).
func StoredRuns(ctx context.Context, d Deps) ([]StoredRun, error) {
	d, err := d.ready("app.StoredRuns", false, false, true)
	if err != nil {
		return nil, err
	}
	runs, err := d.Store.Runs(ctx, domain.RunKindScan)
	if err != nil {
		return nil, err
	}
	out := make([]StoredRun, 0, len(runs))
	for _, r := range runs {
		sr := StoredRun{Run: r.Run}
		sr.Groups, sr.Repos = mcpNamedScopes(r.Groups, r.Repos)
		out = append(out, sr)
	}
	return out, nil
}

// mcpAIPeople returns, per repository with an ai_assisted@1 value, the
// humans credited with a non-merge commit of a landing in that value's
// window: the people its AI-assisted share is made of. The window is the
// stored value's, so this restates none of the metric's definition.
func mcpAIPeople(s *stored) map[string]map[string]bool {
	windows := map[string]domain.Window{}
	for _, v := range s.values {
		if v.Metric.String() == report.MetricAIAssisted && v.Subject.Kind == domain.SubjectRepo {
			windows[v.Subject.Key] = v.Window
		}
	}
	out := map[string]map[string]bool{}
	for i := range s.facts.Repos {
		rf := &s.facts.Repos[i]
		w, ok := windows[rf.Repo.Key]
		if !ok {
			continue
		}
		commits := make(map[string]*domain.Commit, len(rf.Commits))
		for j := range rf.Commits {
			commits[rf.Commits[j].SHA] = &rf.Commits[j]
		}
		people := map[string]bool{}
		for _, l := range rf.Landings {
			if !w.Contains(l.Time) {
				continue
			}
			for _, sha := range mcpLandingCommits(l) {
				c := commits[sha]
				if c == nil || len(c.Parents) > 1 {
					continue
				}
				if p := s.who.Credit(c.Author, c.CoAuthors); !s.who.Bot(p) {
					people[p] = true
				}
			}
		}
		out[rf.Repo.Key] = people
	}
	return out
}

// mcpPersonNamed names one person of a stored run exactly as the reports
// do (report.SubjectOf), which owns the rule: a reviewer the forge could
// not link to an email by login, as reviews_given@1 keys them, and a
// person whose name no page may show with "". It adds the GitHub logins the
// run links to the person, read from the directory that links them for
// every metric (identity.Directory.Logins), never linked again here.
func mcpPersonNamed(key string, s *stored) NamedPerson {
	sub := report.SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: key}, s.who)
	p := NamedPerson{Key: key, Name: sub.Name, Bot: sub.Bot, Logins: s.who.Logins(key)}
	for _, l := range p.Logins {
		if !s.who.LoginWithheld(l) {
			p.ShownLogins = append(p.ShownLogins, l)
		}
	}
	return p
}

// OverviewRequest asks for the platform view of one scope of a stored run.
type OverviewRequest struct {
	RunID int64 // 0: the latest complete or partial scan run
	// Scope is the zero Scope for the whole run, a group (Kind group, Key
	// its name) or one repository (Kind repo, Key its Repo.Key).
	Scope domain.Scope
	// Rank names ranked metrics whose rank tables to add: those of the
	// run's rank scopes that hold the asked scope, since ranks are kept
	// within groups unless the scan asked for one table of everything.
	// With none named, the answer holds no rank table, not even those of
	// the metrics the scan was asked to rank, which a report adds.
	Rank []domain.MetricID
}

// OverviewAnswer is the platform view of one scope: the report's overviews
// and repository cards for it, which say how old and how big the code is,
// who it is made of, where its knowledge is concentrated or has gone quiet,
// and its AI and bot share, each section with its own provenance and
// caveats.
type OverviewAnswer struct {
	// Provenance is a group's overview's or a repository's card's, with the
	// metrics of the repositories and rank tables the answer holds, or the
	// whole run's, which names every metric of the run's report: one index
	// page of the whole run holds the numbers of only some of them.
	Provenance report.Provenance
	Run        report.Run
	Scope      domain.Scope // as asked
	// Overviews holds a group's overview; for the whole run, every overview
	// the report has (every repository, with --scope all or without groups,
	// then one per group); for one repository, none: its card is the view.
	Overviews []report.Overview
	Repos     []report.Repo // the repositories in the scope, in report order
	// Areas holds one repository's top-level directories with their stored
	// knowledge numbers, largest first (report.Areas, #106); nil for any
	// other scope.
	Areas    []report.Area
	Ranks    []report.RankTable // the tables of the metrics asked, in the rank scopes that hold the asked scope
	Warnings []report.Warning   // the run's own: repositories --exclude-repo left out, paths that gave no repository
	Caveats  []report.Caveat    // what could make the scope's numbers wrong
}

// Overview returns the platform view of a scope of a stored run, built by
// the same report.Build as stillhere report, so its numbers are the
// report's.
func Overview(ctx context.Context, d Deps, r OverviewRequest) (*OverviewAnswer, error) {
	d, err := d.ready("app.Overview", false, false, true)
	if err != nil {
		return nil, err
	}
	m, s, err := buildReport(ctx, d, ReportRequest{RunID: r.RunID, Rank: r.Rank}, false)
	if err != nil {
		return nil, err
	}
	a := &OverviewAnswer{Scope: r.Scope, Warnings: m.Warnings}
	if m.Run != nil {
		a.Run = *m.Run
	}
	var group string // the group whose rank tables hold the scope
	switch r.Scope.Kind {
	case "":
		// Where the whole report's numbers come from, as each of its
		// sections says for itself, over every repository of the run.
		a.Overviews, a.Repos, a.Ranks, a.Caveats = m.Overviews, m.Repos, m.Ranks, m.Caveats
		run := s.run
		a.Provenance = report.AnswerProvenance(&run, s.facts, s.forge, m.Metrics)
		return a, nil
	case domain.ScopeGroup:
		i := slices.IndexFunc(m.Overviews, func(o report.Overview) bool {
			return o.Scope.Kind == domain.ScopeGroup && o.Scope.Key == r.Scope.Key
		})
		if i < 0 {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("group %q is not a group of run %d", r.Scope.Key, a.Run.ID)}
		}
		a.Overviews = []report.Overview{m.Overviews[i]}
		a.Provenance, a.Caveats = m.Overviews[i].Provenance, m.Overviews[i].Caveats
		for _, rp := range m.Repos {
			if rp.Group == r.Scope.Key {
				a.Repos = append(a.Repos, rp)
			}
		}
		group = r.Scope.Key
	case domain.ScopeRepo:
		i := slices.IndexFunc(m.Repos, func(rp report.Repo) bool { return rp.Key == r.Scope.Key })
		if i < 0 {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("repository %q is not a repository of run %d", r.Scope.Key, a.Run.ID)}
		}
		rp := m.Repos[i]
		a.Repos = []report.Repo{rp}
		a.Provenance, a.Caveats = rp.Card.Provenance, rp.Card.Caveats
		a.Areas = report.Areas(s.values, rp.Key)
		group = rp.Group
	default:
		return nil, fmt.Errorf("app.Overview: unknown scope kind %q", r.Scope.Kind)
	}
	for _, t := range m.Ranks {
		switch {
		case t.Scope.Kind == domain.ScopeAll,
			group != "" && t.Scope.Kind == domain.ScopeGroup && t.Scope.Key == group,
			r.Scope.Kind == domain.ScopeRepo && t.Scope.Kind == domain.ScopeRepo && t.Scope.Key == r.Scope.Key:
			a.Ranks = append(a.Ranks, t)
		}
	}
	// metrics lists every number's (docs/spec/mcp.md), as the whole run's
	// above does by listing every metric of its report. The scope's own provenance names only its overview's or
	// its card's, so add those of the repositories the answer holds, each
	// with its week table (report.MetricLanded: landed@2) beside its card, and the rank tables'.
	metrics := slices.Clone(a.Provenance.Metrics)
	for _, rp := range a.Repos {
		metrics = append(metrics, rp.Card.Provenance.Metrics...)
		if rp.Week != nil {
			metrics = append(metrics, report.MetricLanded)
		}
	}
	for _, t := range a.Ranks {
		metrics = append(metrics, t.Metric)
	}
	for _, ar := range a.Areas {
		if ar.KnowledgeLoss != nil {
			metrics = append(metrics, report.MetricKnowledgeLoss)
		}
		if ar.Concentration != nil {
			metrics = append(metrics, report.MetricConcentration)
		}
	}
	slices.Sort(metrics)
	a.Provenance.Metrics = slices.Compact(metrics)
	return a, nil
}

// PersonRequest asks what a stored run holds about one person, for someone
// other than that person.
type PersonRequest struct {
	RunID int64  // 0: the latest complete or partial scan run
	Email string // the canonical email, lowercased, or "@login" for a reviewer the forge could not link
}

// Person returns what a lead sees about one person (ADR 0001): every value
// the run holds about them and their place in each ranked metric's table
// within the run's rank scope. It is Me without the person's own
// AI-assisted share, which only their own page shows (ADR 0007), so the
// share is never computed here; the MCP server answers the person the
// server was started for (--me) through Me instead.
func Person(ctx context.Context, d Deps, r PersonRequest) (*report.Me, error) {
	return personPage(ctx, d, "person", r.RunID, r.Email, false)
}

// ChangesRequest asks which changes landed in a scope of a stored run, at
// or after Since.
type ChangesRequest struct {
	RunID  int64        // 0: the latest complete or partial scan run
	Scope  domain.Scope // the zero Scope: every repository; else a group or one repository
	Since  time.Time    // landings at or after it; zero: the run's landed window (the policy's [week] days)
	Person string       // a canonical email: only the landings that credit them; "" for everyone
	// Me is the canonical email of the person asking, if known. Only their
	// own entries carry the AI-assisted tag, which shows only to that
	// person (ADR 0007).
	Me string
	// Titles adds each landing's commit subject, which is local output and
	// never sent to an AI unless the user allows it (domain.Commit).
	Titles bool
}

// ChangesAnswer is what landed in a scope and window, newest first, with
// each change's contribution to the change metrics.
type ChangesAnswer struct {
	Provenance report.Provenance `json:"provenance"`
	Scope      report.Scope      `json:"scope"`
	Window     report.Interval   `json:"window"` // [since, the run's as-of)
	Changes    []Change          `json:"changes"`
	Caveats    []report.Caveat   `json:"caveats"`
}

// Change is one landing: a first-parent commit of a repository's default
// branch, with the commits it brought in.
type Change struct {
	Repo       string          `json:"repo"` // name
	RepoKey    string          `json:"repo_key"`
	Landing    string          `json:"landing"` // the landing's full SHA
	Time       time.Time       `json:"time"`    // its committer time, when it landed
	Merge      bool            `json:"merge"`
	Commits    int             `json:"commits"`         // the commits it brought in, itself included
	Title      string          `json:"title,omitempty"` // the landing commit's subject: ChangesRequest.Titles only
	People     []ChangePerson  `json:"people"`          // credited with its non-merge commits, by name
	MergedBy   *report.Subject `json:"merged_by"`       // who is credited with the merge commit; nil for a non-merge
	Reverts    []string        `json:"reverts"`         // landings of the run whose commits this one reverts
	RevertedBy []string        `json:"reverted_by"`     // landings of the run that revert a commit of this one
}

// ChangePerson is one person's part in a change.
type ChangePerson struct {
	Person report.Subject `json:"person"`
	// Durable is durable@1's cluster for the person and landing: Num lines
	// still blamed to their commits, Den non-blank prod lines they added on
	// files blame read. Nil outside the cohort window (changes 90 to 365
	// days old) or when the landing added no such line.
	Durable *Contribution `json:"durable"`
	// Simplifying is simplifying@1's cluster: Num non-blank prod lines
	// their commits removed. Nil outside its 90-day window, and for a
	// reverted landing or a revert.
	Simplifying *Contribution `json:"simplifying"`
	// AIAssisted says whether one of their own commits in the landing is
	// AI-assisted; nil unless the person is the one asking (ADR 0007).
	AIAssisted *bool `json:"ai_assisted"`
}

// Contribution is one person's cluster in one landing for a ranked metric:
// the unit its eligibility counts and its bootstrap resamples.
type Contribution struct {
	Metric string  `json:"metric"`
	Num    float64 `json:"num"`
	Den    float64 `json:"den"` // a ratio's denominator; 0 for a sum
}

// Changes returns the landings of a scope at or after Since, newest first,
// each with the people its commits credit and their clusters in durable@1
// and simplifying@1: the same clusters explain lists and the ranks resample.
func Changes(ctx context.Context, d Deps, r ChangesRequest) (*ChangesAnswer, error) {
	d, err := d.ready("app.Changes", false, false, true)
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	repos, err := mcpScopeRepos(s, r.Scope)
	if err != nil {
		return nil, err
	}
	now := s.facts.Now
	since := r.Since
	if since.IsZero() {
		since = now.Add(-time.Duration(s.policy.Week.Days) * 24 * time.Hour)
	}
	var caveats []report.Caveat
	if h := historyOf(s.facts); since.Before(h.Since) {
		caveats = append(caveats, report.Caveat{Code: "history-window", Message: fmt.Sprintf(
			"The run's history starts at %s, so nothing earlier is listed: a scan collects the 365 days before its --now.",
			h.Since.Format(time.DateOnly))})
		since = h.Since
	}
	window := domain.Window{Since: since, Until: now}
	clusters, err := mcpChangeClusters(s, repos)
	if err != nil {
		return nil, err
	}
	opt := mcpChangeOptions{
		window: window, clusters: clusters, titles: r.Titles,
		me: strings.ToLower(strings.TrimSpace(r.Me)),
	}
	person := strings.ToLower(strings.TrimSpace(r.Person))
	a := &ChangesAnswer{Scope: mcpReportScope(r.Scope), Window: report.Interval{Since: window.Since, Until: window.Until}}
	for _, rf := range repos {
		for _, ch := range mcpRepoChanges(s, rf, opt) {
			if person == "" || ch.credits(person) {
				a.Changes = append(a.Changes, ch)
			}
		}
	}
	slices.SortFunc(a.Changes, func(x, y Change) int {
		return cmp.Or(y.Time.Compare(x.Time), cmp.Compare(x.Repo, y.Repo), cmp.Compare(x.RepoKey, y.RepoKey), cmp.Compare(x.Landing, y.Landing))
	})
	// The window always ends at the run's as-of, so its newest 90 days
	// are always too young for durable@1.
	caveats = append(caveats, report.Caveat{Code: "durable-window",
		Message: "durable@1 scores changes 90 to 365 days old only: a younger change has no durable cluster yet, which is not a score of 0."})
	if since.Before(now.Add(-mcpSimplifyingDays * 24 * time.Hour)) {
		caveats = append(caveats, report.Caveat{Code: "simplifying-window",
			Message: "simplifying@1 counts the last 90 days only, so an older change carries no simplifying cluster."})
	}
	scoped := mcpScopedFacts(s.facts, repos)
	run := s.run
	a.Provenance = report.AnswerProvenance(&run, scoped, s.forge, []string{mcpDurable, mcpSimplifying})
	validity, err := mcpValidity(mcpDurable, mcpSimplifying)
	if err != nil {
		return nil, err
	}
	a.Caveats = append(append(report.AnswerCaveats(scoped), caveats...), validity...)
	return a, nil
}

// mcpValidity is the validity caveats of the ranked metrics among ids
// (report.ValidityCaveats), for an answer that shows their clusters or
// ranks: a caveat goes where the number it qualifies is (#48), so the
// changes and the highlights say how far their ranked numbers are
// validated, as a rank table does (#69).
func mcpValidity(ids ...string) ([]report.Caveat, error) {
	var infos []report.MetricInfo
	for _, id := range ids {
		m, err := lookupMetric(id, nil)
		if err != nil {
			return nil, err
		}
		infos = append(infos, metricInfo(m.Definition()))
	}
	return report.ValidityCaveats(infos...), nil
}

// credits reports whether the change credits email: one of its commits is
// theirs, or they are credited with its merge.
func (c *Change) credits(email string) bool {
	if c.MergedBy != nil && c.MergedBy.Key == email {
		return true
	}
	return slices.ContainsFunc(c.People, func(p ChangePerson) bool { return p.Person.Key == email })
}

// mcpClusterKey finds one person's cluster in one landing of one repository.
type mcpClusterKey struct{ repo, landing, person string }

// mcpClusters holds the clusters of the two change metrics.
type mcpClusters struct {
	durable, simplifying map[mcpClusterKey]rank.Cluster
}

// mcpChangeClusters computes durable@1's and simplifying@1's clusters in each
// repository of repos, as explain does for one person: the metric owns its
// window, its exclusions and its credit rule, so this restates none of
// them. Bots' clusters are not in RankInput.Data, as rank leaves them out:
// changes and highlights credit people.
func mcpChangeClusters(s *stored, repos []*domain.RepoFacts) (mcpClusters, error) {
	ix := mcpClusters{durable: map[mcpClusterKey]rank.Cluster{}, simplifying: map[mcpClusterKey]rank.Cluster{}}
	in := s.input()
	for _, m := range []struct {
		id  string
		out map[mcpClusterKey]rank.Cluster
	}{{mcpDurable, ix.durable}, {mcpSimplifying, ix.simplifying}} {
		found, err := lookupMetric(m.id, nil)
		if err != nil {
			return ix, err
		}
		ranked, ok := found.(metrics.Ranked)
		if !ok {
			return ix, fmt.Errorf("%s is not a ranked metric", m.id)
		}
		for _, rf := range repos {
			if rf.Status != domain.RepoStatusOK {
				continue
			}
			ri, err := ranked.Clusters(in, domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key})
			if err != nil {
				return ix, fmt.Errorf("reading %s's clusters in %q: %w", m.id, rf.Repo.Name, err)
			}
			for sub, cs := range ri.Data {
				for _, c := range cs {
					m.out[mcpClusterKey{rf.Repo.Key, c.Change, sub.Key}] = c
				}
			}
		}
	}
	return ix, nil
}

// mcpChangeOptions says which landings mcpRepoChanges lists, and what with.
type mcpChangeOptions struct {
	window   domain.Window
	clusters mcpClusters
	titles   bool   // ChangesRequest.Titles
	me       string // ChangesRequest.Me: whose own entries carry the AI-assisted tag
}

// mcpRepoChanges returns one repository's landings in the window as changes.
func mcpRepoChanges(s *stored, rf *domain.RepoFacts, opt mcpChangeOptions) []Change {
	commits := make(map[string]*domain.Commit, len(rf.Commits))
	for i := range rf.Commits {
		commits[rf.Commits[i].SHA] = &rf.Commits[i]
	}
	landingOf := map[string]string{} // commit SHA → the landing that brought it in
	for _, l := range rf.Landings {
		for _, sha := range mcpLandingCommits(l) {
			landingOf[sha] = l.SHA
		}
	}
	reverts, revertedBy := map[string][]string{}, map[string][]string{}
	for _, rv := range rf.Reverts {
		if target, ok := landingOf[rv.Reverted]; ok {
			reverts[rv.SHA] = append(reverts[rv.SHA], target)
			revertedBy[target] = append(revertedBy[target], rv.SHA)
		}
	}
	var out []Change
	for _, l := range rf.Landings {
		if !opt.window.Contains(l.Time) {
			continue
		}
		shas := mcpLandingCommits(l)
		ch := Change{
			Repo: rf.Repo.Name, RepoKey: rf.Repo.Key, Landing: l.SHA, Time: l.Time, Merge: l.Merge, Commits: len(shas),
			Reverts:    mcpSortedUnique(reverts[l.SHA]),
			RevertedBy: mcpSortedUnique(revertedBy[l.SHA]),
		}
		if c := commits[l.SHA]; c != nil {
			if opt.titles {
				ch.Title = c.Subject
			}
			if l.Merge {
				sub := report.SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: s.who.Credit(c.Author, c.CoAuthors)}, s.who)
				ch.MergedBy = &sub
			}
		}
		// One entry per person credited with a non-merge commit of the
		// landing: a merge commit itself never counts (docs/metrics.md).
		mine := false // the asker's own commits, of which one is AI-assisted
		for _, sha := range shas {
			c := commits[sha]
			if c == nil || len(c.Parents) > 1 {
				continue
			}
			email := s.who.Credit(c.Author, c.CoAuthors)
			if email == opt.me && opt.me != "" {
				mine = mine || s.who.AIAssisted(c)
			}
			if slices.ContainsFunc(ch.People, func(p ChangePerson) bool { return p.Person.Key == email }) {
				continue
			}
			p := ChangePerson{Person: report.SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: email}, s.who)}
			if k, ok := opt.clusters.durable[mcpClusterKey{rf.Repo.Key, l.SHA, email}]; ok {
				p.Durable = &Contribution{Metric: mcpDurable, Num: k.Num, Den: k.Den}
			}
			if k, ok := opt.clusters.simplifying[mcpClusterKey{rf.Repo.Key, l.SHA, email}]; ok {
				p.Simplifying = &Contribution{Metric: mcpSimplifying, Num: k.Num}
			}
			ch.People = append(ch.People, p)
		}
		for i := range ch.People {
			if opt.me != "" && ch.People[i].Person.Key == opt.me {
				ai := mine
				ch.People[i].AIAssisted = &ai
			}
		}
		slices.SortFunc(ch.People, func(a, b ChangePerson) int {
			return cmp.Or(cmp.Compare(a.Person.Name, b.Person.Name), cmp.Compare(a.Person.Key, b.Person.Key))
		})
		out = append(out, ch)
	}
	return out
}

// mcpLandingCommits is a landing's commit map: the landing itself for a week
// run's empty map, as the metrics read it.
func mcpLandingCommits(l domain.Landing) []string {
	if len(l.Commits) == 0 {
		return []string{l.SHA}
	}
	return l.Commits
}

func mcpSortedUnique(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := slices.Clone(ss)
	slices.Sort(out)
	return slices.Compact(out)
}

// mcpScopeRepos returns the repositories of a stored run in scope, in key
// order: every one for the zero Scope.
func mcpScopeRepos(s *stored, scope domain.Scope) ([]*domain.RepoFacts, error) {
	var out []*domain.RepoFacts
	switch scope.Kind {
	case "", domain.ScopeAll:
		for i := range s.facts.Repos {
			out = append(out, &s.facts.Repos[i])
		}
		return out, nil
	case domain.ScopeGroup:
		if !slices.ContainsFunc(s.facts.Groups, func(g domain.Group) bool { return g.Name == scope.Key }) {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("group %q is not a group of run %d", scope.Key, s.run.ID)}
		}
		for i := range s.facts.Repos {
			if s.facts.Repos[i].Repo.Group == scope.Key {
				out = append(out, &s.facts.Repos[i])
			}
		}
		return out, nil
	case domain.ScopeRepo:
		for i := range s.facts.Repos {
			if s.facts.Repos[i].Repo.Key == scope.Key {
				return []*domain.RepoFacts{&s.facts.Repos[i]}, nil
			}
		}
		return nil, &domain.UsageError{Msg: fmt.Sprintf("repository %q is not a repository of run %d", scope.Key, s.run.ID)}
	}
	return nil, fmt.Errorf("unknown scope kind %q", scope.Kind)
}

// mcpScopedFacts is a run's fact set limited to repos, for an answer's
// provenance and caveats: the repositories it covers, and no other.
func mcpScopedFacts(fs *domain.FactSet, repos []*domain.RepoFacts) *domain.FactSet {
	out := *fs
	out.Repos = nil
	for _, rf := range repos {
		out.Repos = append(out.Repos, *rf)
	}
	return &out
}

func mcpReportScope(s domain.Scope) report.Scope {
	if s.Kind == "" {
		return report.Scope{Kind: domain.ScopeAll}
	}
	return report.Scope{Kind: s.Kind, Key: s.Key}
}

// HighlightsRequest asks for the highlights of a scope of a stored run.
type HighlightsRequest struct {
	RunID int64        // 0: the latest complete or partial scan run
	Scope domain.Scope // the zero Scope: every repository; else a group or one repository
	Since time.Time    // the start of the period; zero: 30 days before the run's as-of
	// Titles adds the landing commits' subjects; see ChangesRequest.Titles.
	Titles bool
}

// HighlightsAnswer is what deserves a mention in a scope: rule-based picks
// from the metrics' own values, ranks and clusters, never a ranking of
// people by a point estimate (ADR 0001).
type HighlightsAnswer struct {
	Provenance report.Provenance `json:"provenance"`
	Scope      report.Scope      `json:"scope"`
	Period     report.Interval   `json:"period"`
	// TopTier holds, per ranked metric and rank scope, the people whose 95%
	// rank interval reaches first place: everyone who could be first.
	// Their intervals overlap, so they are not ordered among themselves,
	// and a tier that holds more than a third of a table's eligible people
	// singles no one out, unless the intervals set it above everyone else:
	// otherwise it is not listed, and a caveat says so.
	TopTier []TopTier `json:"top_tier"`
	// Lasted holds durable@1's cohort clusters that kept the most lines:
	// changes 90 to 365 days old whose lines blame still attributes to
	// them. The period does not apply: the cohort window is the metric's.
	Lasted []HighlightChange `json:"lasted"`
	// Removed holds simplifying@1's clusters in the period that removed the
	// most prod lines and were not reverted. A removal is not always a
	// simplification: a simplifying landing nets -20 lines or fewer, which
	// a person's simplifying_landings detail counts.
	Removed []HighlightChange `json:"removed"`
	Caveats []report.Caveat   `json:"caveats"`
}

// TopTier is one rank table's top: the people whose rank interval includes
// first place.
type TopTier struct {
	Metric   string          `json:"metric"`
	Question string          `json:"question"`
	Scope    report.Scope    `json:"scope"`
	Window   report.Interval `json:"window"`
	Percent  bool            `json:"percent"` // a ratio: estimates are shares
	MinN     int             `json:"min_n"`   // the eligibility threshold
	Of       int             `json:"of"`      // eligible people in the table
	// PeopleTotal counts the eligible people whose 95% rank interval
	// reaches first place.
	PeopleTotal int `json:"people_total"`
	// People lists them by name, unless they are more than a third of Of
	// and do not stand apart: then no one stands out, People is empty and
	// a caveat says so. A tier stands apart when someone is outside it and
	// the best place anyone outside can take lies below the worst place any
	// of the tier reaches, so it is named in a table of any size, such as
	// one person at 1-1 of two; a tier of everyone never stands apart.
	People []report.RankRow `json:"people"`
}

// mcpTierShare is the share of a table's eligible people above which its top
// tier singles no one out: more than one in mcpTierShare reach first place.
const mcpTierShare = 3

// HighlightChange is one person's cluster in one landing, picked for a
// highlight.
type HighlightChange struct {
	Metric  string         `json:"metric"`
	Repo    string         `json:"repo"`
	RepoKey string         `json:"repo_key"`
	Landing string         `json:"landing"`
	Time    time.Time      `json:"time"`
	Person  report.Subject `json:"person"`
	Num     float64        `json:"num"` // durable@1: lines still blamed to it; simplifying@1: lines removed
	Den     float64        `json:"den"` // durable@1: lines added; 0 for simplifying@1
	Title   string         `json:"title,omitempty"`
}

// Highlights returns a scope's highlights: the top tier of every ranked
// metric's table, the cohort changes that lasted, and the period's largest
// removals that stuck.
func Highlights(ctx context.Context, d Deps, r HighlightsRequest) (*HighlightsAnswer, error) {
	d, err := d.ready("app.Highlights", false, false, true)
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	repos, err := mcpScopeRepos(s, r.Scope)
	if err != nil {
		return nil, err
	}
	now := s.facts.Now
	since := r.Since
	if since.IsZero() {
		since = now.Add(-30 * 24 * time.Hour)
	}
	a := &HighlightsAnswer{Scope: mcpReportScope(r.Scope), Period: report.Interval{Since: since, Until: now}}

	// The top tier, in the run's rank scopes that hold the asked scope.
	var scopes []domain.Scope
	group := ""
	if r.Scope.Kind == domain.ScopeRepo && len(repos) == 1 {
		group = repos[0].Repo.Group
	}
	for _, sc := range rankScopes(s.scope(), s.facts) {
		switch {
		case r.Scope.Kind == "", sc.Kind == domain.ScopeAll, sc == r.Scope,
			group != "" && sc.Kind == domain.ScopeGroup && sc.Key == group:
			scopes = append(scopes, sc)
		}
	}
	sets, _, err := rankAll(s.input(), allRanked(), scopes, s.ranks, nil)
	if err != nil {
		return nil, err
	}
	used := []string{mcpDurable, mcpSimplifying}
	var wide []string // the tiers that single no one out
	for _, set := range sets {
		t := TopTier{
			Metric: set.Metric.ID, Question: set.Metric.Question, Scope: report.Scope{Kind: set.Scope.Kind, Key: set.Scope.Key},
			Window: report.Interval{Since: set.Window.Since, Until: set.Window.Until}, Percent: set.Percent, MinN: set.MinN,
		}
		tierHi, restLo := 0, 0 // the tier's lowest place, and the best place anyone else could take; 0: none
		for _, ri := range set.Intervals {
			if !ri.Eligible || ri.Subject.Kind != domain.SubjectPerson || s.who.Bot(ri.Subject.Key) {
				continue
			}
			t.Of++
			if ri.RankLo != 1 {
				if restLo == 0 || ri.RankLo < restLo {
					restLo = ri.RankLo
				}
				continue
			}
			tierHi = max(tierHi, ri.RankHi)
			sub := report.SubjectOf(ri.Subject, s.who)
			t.People = append(t.People, report.RankedRow(sub, ri))
		}
		if len(t.People) == 0 {
			continue
		}
		t.PeopleTotal = len(t.People)
		// The tier stands apart when someone is outside it and no one
		// outside could take a place its people's intervals reach: then
		// the intervals order it above the rest, however small the table
		// (MC-16: 1-1 against 2-2 of two). A tier of everyone (restLo 0)
		// is above no one. Each interval is the person's own, so the check
		// reads both sides rather than trust them to agree.
		apart := restLo > tierHi
		if mcpTierShare*t.PeopleTotal > t.Of && !apart {
			// When most of the table could be first, listing them would
			// read as a podium the intervals do not support (ADR 0001).
			t.People = nil
			wide = append(wide, fmt.Sprintf("%s in %s: %d of %d", t.Metric, mcpScopeText(set.Scope), t.PeopleTotal, t.Of))
		}
		// Not ordered among themselves: by name only.
		slices.SortFunc(t.People, func(x, y report.RankRow) int {
			return cmp.Or(cmp.Compare(x.Name, y.Name), cmp.Compare(x.Email, y.Email))
		})
		a.TopTier = append(a.TopTier, t)
		used = append(used, set.Metric.ID)
	}

	ix, err := mcpChangeClusters(s, repos)
	if err != nil {
		return nil, err
	}
	period := domain.Window{Since: since, Until: now}
	a.Lasted = mcpHighlightChanges(s, repos, ix.durable, mcpDurable, nil, r.Titles)
	a.Removed = mcpHighlightChanges(s, repos, ix.simplifying, mcpSimplifying, &period, r.Titles)

	scoped := mcpScopedFacts(s.facts, repos)
	run := s.run
	a.Provenance = report.AnswerProvenance(&run, scoped, s.forge, used)
	// The top tier can hold reviews_given@1, whose GitHub numbers carry
	// their caveats as its rank tables do.
	a.Caveats = append(report.AnswerCaveats(scoped), report.ForgeCaveats(&run, scoped, s.forge, used)...)
	a.Caveats = append(a.Caveats, report.Caveat{Code: "highlights",
		Message: "Highlights are rule-based picks for recognition, not a ranking of people: the top tier counts everyone " +
			"whose 95% rank interval reaches first place and names them when they are a third of the table or fewer, or " +
			"when their intervals lie above everyone else's, and the change lists are single changes with their numbers."})
	validity, err := mcpValidity(used...)
	if err != nil {
		return nil, err
	}
	a.Caveats = append(a.Caveats, validity...)
	if len(wide) > 0 {
		if len(wide) > mcpHighlightsTop {
			wide = append(wide[:mcpHighlightsTop], fmt.Sprintf("and %d more tables", len(wide)-mcpHighlightsTop))
		}
		a.Caveats = append(a.Caveats, report.Caveat{Code: "no-standout", Message: "No one stands out where more than a " +
			"third of a table's eligible people have a 95% rank interval that reaches first place and the intervals do not " +
			"set them above everyone else, so these top tiers are not listed (could be first, of eligible): " +
			strings.Join(wide, "; ") + "."})
	}
	return a, nil
}

// mcpScopeText names a rank scope in a sentence.
func mcpScopeText(s domain.Scope) string {
	if s.Kind == domain.ScopeAll || s.Kind == "" {
		return "the whole run"
	}
	return s.Kind + " " + strconv.Quote(s.Key)
}

// mcpHighlightChanges picks a metric's clusters with the largest Num, at most
// mcpHighlightsTop, among landings in period (every landing when nil).
func mcpHighlightChanges(s *stored, repos []*domain.RepoFacts, clusters map[mcpClusterKey]rank.Cluster, metric string,
	period *domain.Window, titles bool) []HighlightChange {
	type landing struct {
		repo *domain.RepoFacts
		at   time.Time
		sub  string
	}
	landings := map[[2]string]landing{} // repo key and SHA → the landing
	for _, rf := range repos {
		subjects := make(map[string]string, len(rf.Commits))
		for _, c := range rf.Commits {
			subjects[c.SHA] = c.Subject
		}
		for _, l := range rf.Landings {
			landings[[2]string{rf.Repo.Key, l.SHA}] = landing{repo: rf, at: l.Time, sub: subjects[l.SHA]}
		}
	}
	var out []HighlightChange
	for k, c := range clusters {
		l, ok := landings[[2]string{k.repo, k.landing}]
		if !ok || c.Num <= 0 || (period != nil && !period.Contains(l.at)) {
			continue
		}
		h := HighlightChange{
			Metric: metric, Repo: l.repo.Repo.Name, RepoKey: k.repo, Landing: k.landing, Time: l.at,
			Person: report.SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: k.person}, s.who),
			Num:    c.Num, Den: c.Den,
		}
		if titles {
			h.Title = l.sub
		}
		out = append(out, h)
	}
	slices.SortFunc(out, func(x, y HighlightChange) int {
		return cmp.Or(cmp.Compare(y.Num, x.Num), y.Time.Compare(x.Time), cmp.Compare(x.Repo, y.Repo),
			cmp.Compare(x.Landing, y.Landing), cmp.Compare(x.Person.Key, y.Person.Key))
	})
	if len(out) > mcpHighlightsTop {
		out = out[:mcpHighlightsTop]
	}
	return out
}

// The limits of a diff answer.
const (
	mcpDiffMaxBytes  = 24000 // the default cut of a patch: room, in an MCP answer, for the JSON escapes of its tabs and newlines
	mcpDiffMaxFiles  = 1000  // paths named to git for one patch, so its command line stays short
	mcpDiffMinPrefix = 7     // hex digits a change id needs, as git's short ids
	// mcpDiffTimeout bounds each git command of a diff. An agent waits for
	// the answer, and the server answers one call at a time, so a diff
	// that git cannot produce quickly (a huge change, a cold clone on a
	// slow disk) fails with what to narrow rather than holding every
	// other call for git's default of minutes.
	mcpDiffTimeout = 30 * time.Second
)

// errMCPPatchFull stops git once a patch has reached its cut.
var errMCPPatchFull = errors.New("the patch reached its cut")

// DiffRequest asks for the patch of one landing of a stored run. A diff is
// source code: only an inbound adapter whose user lets source leave the
// machine asks for one (stillhere mcp --allow-source).
type DiffRequest struct {
	RunID    int64  // 0: the latest complete or partial scan run
	Repo     string // Repo.Key; "" searches every repository of the run
	Change   string // a landing's SHA, or a prefix of at least 7 hex digits
	Path     string // a repo-relative path prefix that limits the diff; "" for the whole change
	MaxBytes int    // the patch is cut at about this many bytes; 0 means 24,000
	// Earlier are the other landings of Change's change, newest first, as
	// metrics.Change.Landings lists them after the first: a rebase merge's.
	// Each must be the first parent of the one before it, Change's first,
	// so the diff runs from the last one's first parent to Change: the
	// whole change, not its newest commit. nil for Change alone.
	Earlier []string
}

// DiffAnswer is one landing's first-parent diff: what it changed on the
// default branch, read from the repository with git at query time.
type DiffAnswer struct {
	Provenance report.Provenance `json:"provenance"`
	Repo       string            `json:"repo"`
	RepoKey    string            `json:"repo_key"`
	Landing    string            `json:"landing"`
	Time       time.Time         `json:"time"`
	Merge      bool              `json:"merge"`
	Title      string            `json:"title"` // the landing commit's subject
	Base       string            `json:"base"`  // the first parent the diff is taken against (of the oldest of Earlier, when asked); "" for a root commit
	Path       string            `json:"path"`  // the prefix asked for; "" for the whole change
	Files      []DiffFile        `json:"files"` // every path the diff touches under Path, by path
	Patch      string            `json:"patch"` // the unified diff of the files with a role, cut at MaxBytes
	Truncated  bool              `json:"truncated"`
	Caveats    []report.Caveat   `json:"caveats"`
}

// DiffFile is one path a diff touches.
type DiffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // the rename's source
	Added   int    `json:"added"`              // numstat lines; 0 for a binary file
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
	// Class is the run's verdict on the path: its role (prod, test, ...),
	// or why it adds nothing (denied_path, too_big, generated_header,
	// unclassified); "" when the run did not classify it.
	Class   string `json:"class"`
	InPatch bool   `json:"in_patch"` // the patch shows it: the path has a role, as the metrics count
}

// Diff returns the first-parent diff of one landing of a stored run: the
// files it touched, with the run's class of each, and the patch of those
// that have a role, so a lockfile or generated code does not crowd out the
// change. It runs git on the repository the run read, with the analysis
// profile, so nothing is fetched and no program the repository names
// runs: external diff and textconv are off. The commit must still be in
// the clone.
func Diff(ctx context.Context, d Deps, r DiffRequest) (*DiffAnswer, error) {
	d, err := d.ready("app.Diff", true, false, true)
	if err != nil {
		return nil, err
	}
	change := strings.ToLower(strings.TrimSpace(r.Change))
	if len(change) < mcpDiffMinPrefix || len(change) > 40 || strings.Trim(change, "0123456789abcdef") != "" {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"change %q is not a commit id: want a landing's SHA, or its first %d or more hex digits", r.Change, mcpDiffMinPrefix)}
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	rf, l, err := mcpFindLanding(s, r.Repo, change)
	if err != nil {
		return nil, err
	}
	a := &DiffAnswer{Repo: rf.Repo.Name, RepoKey: rf.Repo.Key, Landing: l.SHA, Time: l.Time, Merge: l.Merge, Path: cleanPrefix(r.Path)}
	boundary := false
	for i := range rf.Commits {
		if c := &rf.Commits[i]; c.SHA == l.SHA {
			a.Title = c.Subject
			if len(c.Parents) > 0 {
				a.Base = c.Parents[0]
			}
			boundary = rf.Repo.Boundary(c)
		}
	}
	from := l // the oldest landing the diff covers: l, or the oldest of r.Earlier
	if !boundary && len(r.Earlier) > 0 {
		if from, boundary, err = diffEarlier(rf, l, a, r.Earlier); err != nil {
			return nil, err
		}
		if a.Base == "" && !boundary {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("landing %s, the oldest of the change, is %q's root commit: "+
				"the diff of a change that starts there would be its whole tree", mcpShort(from.SHA), rf.Repo.Name)}
		}
	}
	if boundary {
		// In a shallow clone a parentless commit is where the clone's
		// history ends, by the one rule the history collector reads it by
		// (domain.Repo.Boundary: its boundary gets no change rows). Its
		// parent is not in the clone, so its diff against nothing would be
		// its whole tree, not its change (RB-5).
		return nil, &domain.UsageError{Msg: fmt.Sprintf("landing %s is where %q's shallow clone ends: the clone does not "+
			"hold its parent, so its diff would be its whole tree, not its change. Its change shows once the user deepens "+
			"the clone (git fetch --unshallow) and scans again.", mcpShort(from.SHA), rf.Repo.Name)}
	}
	// The two-tree form diffs the landing against its first parent, a
	// merge included, on every git the floor admits; a root commit has no
	// parent and is diffed as all added.
	trees := []string{"--root", l.SHA}
	if a.Base != "" {
		trees = []string{a.Base, l.SHA}
	}
	var spec []string
	if a.Path != "" {
		spec = []string{"--", ":(literal)" + a.Path}
	}
	args := append(append([]string{"diff-tree", "-r", "-M", "--no-commit-id", "--numstat", "-z"}, trees...), spec...)
	out, err := d.Git.Run(ctx, domain.GitCmd{Dir: rf.Repo.Path, Args: args, Timeout: mcpDiffTimeout, Profile: domain.Analysis})
	if err != nil {
		switch {
		case mcpGitTimedOut(ctx, err):
			return nil, mcpDiffTooSlow(l, rf)
		case rf.Repo.Partial && collect.LacksObjects(err):
			return nil, mcpDiffPartial(l, rf)
		}
		return nil, fmt.Errorf("git could not read landing %s in %q, which run %d scanned: the clone may have moved or "+
			"rewritten its history since; scan again: %w", mcpShort(l.SHA), rf.Repo.Name, s.run.ID, err)
	}
	if a.Files, err = mcpParseNumstat(out); err != nil {
		return nil, fmt.Errorf("reading the files of landing %s in %q: %w", mcpShort(l.SHA), rf.Repo.Name, err)
	}
	classes := make(map[string]domain.PathClass, len(rf.Paths))
	for _, pc := range rf.Paths {
		classes[pc.Path] = pc
	}
	var paths []string
	shown, left := 0, 0 // files with a role in the patch, and beyond mcpDiffMaxFiles
	for i := range a.Files {
		f := &a.Files[i]
		pc, ok := classes[f.Path]
		switch {
		case !ok:
		case pc.Role != "":
			f.Class = string(pc.Role)
			if shown == mcpDiffMaxFiles {
				left++
				break
			}
			f.InPatch = true
			shown++
			paths = append(paths, ":(literal)"+f.Path)
			if f.OldPath != "" {
				// git pairs a rename only when both of its paths are in
				// the pathspec; without the source, the patch would show
				// the whole file as added.
				paths = append(paths, ":(literal)"+f.OldPath)
			}
		default:
			f.Class = string(pc.Skip)
		}
	}
	limit := r.MaxBytes
	if limit <= 0 {
		limit = mcpDiffMaxBytes
	}
	if len(paths) > 0 {
		args := append(append([]string{"diff-tree", "-r", "-M", "-p", "--no-commit-id", "--no-color", "--no-ext-diff",
			"--no-textconv", "-U3"}, trees...), append([]string{"--"}, paths...)...)
		cmd := domain.GitCmd{Dir: rf.Repo.Path, Args: args, Timeout: mcpDiffTimeout, Profile: domain.Analysis}
		err = d.Git.Stream(ctx, cmd, func(rd io.Reader) error {
			b, err := io.ReadAll(io.LimitReader(rd, int64(limit)+1))
			if err != nil {
				return err
			}
			if len(b) > limit {
				// Cut at the last whole line inside the limit.
				b = b[:limit]
				if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
					b = b[:i+1]
				}
				a.Patch, a.Truncated = string(b), true
				return errMCPPatchFull
			}
			a.Patch = string(b)
			return nil
		})
		switch {
		case err == nil, errors.Is(err, errMCPPatchFull):
		case mcpGitTimedOut(ctx, err):
			return nil, mcpDiffTooSlow(l, rf)
		case rf.Repo.Partial && collect.LacksObjects(err):
			return nil, mcpDiffPartial(l, rf)
		default:
			return nil, fmt.Errorf("git could not diff landing %s in %q: %w", mcpShort(l.SHA), rf.Repo.Name, err)
		}
	}
	scoped := mcpScopedFacts(s.facts, []*domain.RepoFacts{rf})
	run := s.run
	a.Provenance = report.AnswerProvenance(&run, scoped, s.forge, nil)
	a.Caveats = report.AnswerCaveats(scoped)
	switch {
	case left > 0:
		a.Truncated = true
		a.Caveats = append(a.Caveats, report.Caveat{Code: "truncated", Message: fmt.Sprintf(
			"The patch shows the first %d files with a role and leaves out %d more: ask for a path under the change.",
			mcpDiffMaxFiles, left)})
	case a.Truncated:
		a.Caveats = append(a.Caveats, report.Caveat{Code: "truncated", Message: fmt.Sprintf(
			"The patch was cut at %s bytes: ask for a path under the change to see the rest of it.", strconv.Itoa(limit))})
	}
	return a, nil
}

// diffEarlier moves a's base below the earlier landings of l's change
// (DiffRequest.Earlier), so the diff is the whole change: each must be a
// landing of the run and the first parent of the one before it, so the
// change is one unbroken stretch of the first-parent history and no other
// change's commit is in its diff. It returns the oldest landing, whose
// first parent is the base, and whether it is the shallow clone's boundary.
func diffEarlier(rf *domain.RepoFacts, l domain.Landing, a *DiffAnswer, earlier []string) (domain.Landing, bool, error) {
	commits := make(map[string]*domain.Commit, len(rf.Commits))
	for i := range rf.Commits {
		commits[rf.Commits[i].SHA] = &rf.Commits[i]
	}
	landings := make(map[string]domain.Landing, len(rf.Landings))
	for _, x := range rf.Landings {
		landings[x.SHA] = x
	}
	for _, sha := range earlier {
		sha = strings.ToLower(strings.TrimSpace(sha))
		next, ok := landings[sha]
		c := commits[sha]
		if !ok || c == nil || sha != a.Base {
			return l, false, &domain.UsageError{Msg: fmt.Sprintf("landing %s is not the first parent of landing %s in %q: "+
				"a change's earlier landings follow one another on the first-parent history, newest first",
				mcpShort(sha), mcpShort(l.SHA), rf.Repo.Name)}
		}
		l, a.Base = next, "" // the newer of the next pair, for its message
		if len(c.Parents) > 0 {
			a.Base = c.Parents[0]
		}
		if rf.Repo.Boundary(c) {
			return l, true, nil
		}
	}
	return l, false, nil
}

// mcpGitTimedOut reports whether a git command failed on its own timeout,
// rather than on the caller's context ending.
func mcpGitTimedOut(ctx context.Context, err error) bool {
	var ge *domain.GitError
	return ctx.Err() == nil && errors.As(err, &ge) && errors.Is(ge.Err, context.DeadlineExceeded)
}

// mcpDiffTooSlow is the error of a diff git could not produce in time.
func mcpDiffTooSlow(l domain.Landing, rf *domain.RepoFacts) error {
	return &domain.UsageError{Msg: fmt.Sprintf("git took more than %d seconds to diff landing %s in %q: ask for a path "+
		"under the change, or a smaller max_chars", int(mcpDiffTimeout/time.Second), mcpShort(l.SHA), rf.Repo.Name)}
}

// mcpDiffPartial is the error of a diff git could not read in a partial
// clone because the clone lacks the contents (RB-8, collect.LacksObjects):
// they were never fetched, and analysis fetches nothing, so no rescan helps
// until the clone has them. A refetch alone does not: git fetch --refetch
// applies the clone's partialclonefilter again, and the clone stays a
// promisor, which discovery reads as partial (verified on git 2.50.1: 1,804
// objects missing before and after; none but one after the filter was
// unset, and a scan blamed the clone once promisor was unset too).
func mcpDiffPartial(l domain.Landing, rf *domain.RepoFacts) error {
	return &domain.UsageError{Msg: fmt.Sprintf("git could not read landing %s in %q, a partial clone (made with --filter): "+
		"the clone lacks the file contents the diff needs, and stillhere never fetches. The diff works once the user "+
		"clones the repository whole (git clone without --filter) and scans that clone; in this clone, git config "+
		"--unset remote.origin.partialclonefilter, then git fetch --refetch origin, then git config --unset "+
		"remote.origin.promisor does the same, and a scan then also gives it line facts.", mcpShort(l.SHA), rf.Repo.Name)}
}

// mcpFindLanding finds the landing a change id names: one of the run's
// landings, in repo when it is not "", whose SHA starts with change.
func mcpFindLanding(s *stored, repo, change string) (*domain.RepoFacts, domain.Landing, error) {
	type match struct {
		rf *domain.RepoFacts
		l  domain.Landing
	}
	find := func(repo string) []match {
		var found []match
		for i := range s.facts.Repos {
			rf := &s.facts.Repos[i]
			if repo != "" && rf.Repo.Key != repo {
				continue
			}
			for _, l := range rf.Landings {
				if strings.HasPrefix(l.SHA, change) {
					found = append(found, match{rf, l})
				}
			}
		}
		return found
	}
	found := find(repo)
	switch len(found) {
	case 1:
		if found[0].rf.Status != domain.RepoStatusOK {
			return nil, domain.Landing{}, &domain.UsageError{Msg: fmt.Sprintf(
				"repository %q failed in run %d, so its landings cannot be read", found[0].rf.Repo.Name, s.run.ID)}
		}
		return found[0].rf, found[0].l, nil
	case 0:
		if repo != "" {
			// The landing may be another repository's (MC-15): say whose.
			var others []string
			for _, m := range find("") {
				others = append(others, strconv.Quote(m.rf.Repo.Name))
			}
			if len(others) > 0 {
				slices.Sort(others)
				name := repo
				if rfs, err := mcpScopeRepos(s, domain.Scope{Kind: domain.ScopeRepo, Key: repo}); err == nil {
					name = rfs[0].Repo.Name
				}
				return nil, domain.Landing{}, &domain.UsageError{Msg: fmt.Sprintf("no landing of %q starts with %s, but "+
					"a landing of %s does: pass that repository, or no repository", name, change,
					strings.Join(slices.Compact(others), ", "))}
			}
		}
		return nil, domain.Landing{}, fmt.Errorf("no landing of run %d starts with %s: a change is a first-parent commit "+
			"of a default branch in the run's 365-day history, as changes lists them: %w", s.run.ID, change, domain.ErrNotFound)
	}
	var names []string
	for _, m := range found {
		names = append(names, strconv.Quote(m.rf.Repo.Name)+" "+mcpShort(m.l.SHA))
	}
	slices.Sort(names)
	return nil, domain.Landing{}, &domain.UsageError{Msg: fmt.Sprintf(
		"change %s names %d landings (%s): give more of its digits, or the repository", change, len(found), strings.Join(names, ", "))}
}

// mcpParseNumstat reads git diff-tree --numstat -z: "<added>\t<removed>\t<path>\0",
// or for a rename "<added>\t<removed>\t\0<source>\0<destination>\0", with
// "-" for both counts of a binary file. Files are sorted by path.
func mcpParseNumstat(out []byte) ([]DiffFile, error) {
	fields := strings.Split(string(out), "\x00")
	var files []DiffFile
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected numstat field %q", fields[i])
		}
		f := DiffFile{Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			f.Binary = true
		} else {
			var err error
			if f.Added, err = strconv.Atoi(parts[0]); err != nil {
				return nil, fmt.Errorf("unexpected numstat count %q", parts[0])
			}
			if f.Removed, err = strconv.Atoi(parts[1]); err != nil {
				return nil, fmt.Errorf("unexpected numstat count %q", parts[1])
			}
		}
		if f.Path == "" { // a rename: the source and the destination follow
			if i+2 >= len(fields) {
				return nil, errors.New("a numstat rename without its paths")
			}
			f.OldPath, f.Path = fields[i+1], fields[i+2]
			i += 2
		}
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b DiffFile) int { return cmp.Compare(a.Path, b.Path) })
	return files, nil
}

// short is a SHA's first 12 hex digits, as answers name a change.
func mcpShort(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
