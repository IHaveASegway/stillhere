package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The shapes of the answers, and how much of each an answer holds. The use
// cases in internal/app return whole answers in the report types; this
// file decides what an agent gets of them: everything with
// response_format "detailed", the numbers an agent needs to answer with
// "concise" (the default), and in either form a page of each long list
// (Anthropic, "Writing effective tools for agents", 2025: names rather than
// ids, concise or detailed, paging under the client's token cap). It
// computes no number: it only selects and trims.

// formatDetailed is the response_format that asks for every field; the
// other one, "concise", is the default.
const formatDetailed = "detailed"

// The sizes of an answer.
const (
	// answerBudget keeps every answer under 40,000 characters, about
	// 10,000 tokens at 4 characters a token: under Claude Code's warning
	// at 10,000 tokens, under the 50,000 characters past which it saves a
	// result to a file instead of showing it, and far under its default
	// cap of 25,000 tokens (MAX_MCP_OUTPUT_TOKENS), so the estimate may be
	// off by half and still fit. An answer over it is cut to a smaller
	// page.
	answerBudget = 40000
	maxLimit     = 100 // the largest page a call may ask for
	minRows      = 3   // the shortest list an answer cuts a table to
)

// format is how much of an answer to give: the page of the answer's main
// list and the rows of each table inside it.
type format struct {
	detailed bool
	offset   int // the first item of the page
	limit    int // items in the page
	rows     int // rows of each table inside an item, such as an overview's people
}

// head opens every answer about a run: where its numbers come from.
type head struct {
	Provenance report.Provenance `json:"provenance"`
}

func (h *head) provenance() *report.Provenance { return &h.Provenance }

// tail closes every answer: the page, what could make it wrong, and what
// the server withheld.
type tail struct {
	Page    *page           `json:"page,omitzero"`
	Caveats []report.Caveat `json:"caveats"`
	// Withheld lists what this server keeps from the agent: "emails"
	// (each replaced by a stable id, p-…, unless --allow-emails) and
	// "source" (commit subjects and diffs, unless --allow-source).
	Withheld []string `json:"withheld"`
}

func (t *tail) end() *tail { return t }

// page says which part of a list an answer holds and how to get the next.
type page struct {
	Offset     int     `json:"offset"`
	Limit      int     `json:"limit"`
	Total      int     `json:"total"`
	NextCursor *string `json:"next_cursor"` // pass it as cursor for the next page; null on the last
}

// paginate cuts the page f asks for out of items, in a run.
func paginate[T any](items []T, f format, run int64) ([]T, *page) {
	p := &page{Offset: min(f.offset, len(items)), Limit: f.limit, Total: len(items)}
	end := min(p.Offset+f.limit, len(items))
	if end < len(items) {
		c := cursor(run, end)
		p.NextCursor = &c
	}
	return items[p.Offset:end], p
}

// cursor is the opaque cursor of a page: the run and the offset, so a
// cursor from another run is refused rather than served stale.
func cursor(run int64, offset int) string { return fmt.Sprintf("%d:%d", run, offset) }

// parseCursor reads a cursor; "" is the first page of any run (run 0).
func parseCursor(c string) (run int64, offset int, err error) {
	if c == "" {
		return 0, 0, nil
	}
	r, o, ok := strings.Cut(c, ":")
	run, err1 := strconv.ParseInt(r, 10, 64)
	offset, err2 := strconv.Atoi(o)
	if !ok || err1 != nil || err2 != nil || run <= 0 || offset < 0 {
		return 0, 0, &domain.UsageError{Msg: fmt.Sprintf(
			"cursor %q is not one this server gave: pass next_cursor from the previous page as it is, or no cursor for the first page", c)}
	}
	return run, offset, nil
}

// checkCursor refuses a cursor from another run than the one an answer
// read: its offsets index another list.
func checkCursor(cursorRun, run int64) error {
	if cursorRun != 0 && cursorRun != run {
		return &domain.UsageError{Msg: fmt.Sprintf(
			"the cursor belongs to run %d, but the server now reads run %d (a scan was published): start again without a cursor",
			cursorRun, run)}
	}
	return nil
}

// cut keeps the first n items, every one when n is 0, and says how many
// there were.
func cut[T any](items []T, n int) ([]T, int) {
	if n > 0 && len(items) > n {
		return items[:n], len(items)
	}
	return items, len(items)
}

// shortSHA is how a concise answer names a commit: 12 hex digits, which
// every tool that takes a change accepts.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// scopeOut names the scope an answer covers.
type scopeOut struct {
	Kind string `json:"kind"` // all, group or repo
	Key  string `json:"key"`  // the group's name or the repository's key; "" for all
	Name string `json:"name"` // the group's or repository's name; "" for all
}

func scopeOf(sc domain.Scope, name string) scopeOut {
	if sc.Kind == "" {
		return scopeOut{Kind: domain.ScopeAll}
	}
	return scopeOut{Kind: sc.Kind, Key: sc.Key, Name: name}
}

// The overview tool.

type overviewOut struct {
	head
	Scope     scopeOut         `json:"scope"`
	Run       runOut           `json:"run"`
	Overviews []overviewItem   `json:"overviews"`
	Repos     any              `json:"repos"` // concise: []repoSummary; detailed: []report.Repo
	Ranks     []rankTableOut   `json:"ranks"`
	Warnings  []report.Warning `json:"warnings"`
	tail
}

// runOut is the run an answer read.
type runOut struct {
	ID          int64             `json:"id"`
	Status      string            `json:"status"` // complete, or partial when a repository or a path failed
	ToolVersion string            `json:"tool_version"`
	Options     map[string]string `json:"options,omitzero"` // detailed only: the scan's effective options
}

// overviewItem is one overview: a group's, or every repository's.
type overviewItem struct {
	Scope         report.Scope          `json:"scope"`
	Provenance    *report.Provenance    `json:"provenance,omitzero"` // detailed only: the answer's covers it
	Totals        report.Totals         `json:"totals"`
	People        *stillHereOut         `json:"people"`
	Week          *weekOut              `json:"week"`
	Concentration *report.Concentration `json:"concentration"`
	KnowledgeLoss *report.Share         `json:"knowledge_loss"`
	AIAssisted    *report.Share         `json:"ai_assisted"`
	BotShare      *report.Share         `json:"bot_share"`
	Caveats       []report.Caveat       `json:"caveats,omitzero"` // detailed only
}

// stillHereOut is surviving@1's rows for a scope, the first rows of each
// list.
type stillHereOut struct {
	FilesBlamed int                   `json:"files_blamed"`
	Humans      []report.StillHereRow `json:"humans"`
	HumansTotal int                   `json:"humans_total"`
	Bots        []report.StillHereRow `json:"bots"`
	BotsTotal   int                   `json:"bots_total"`
}

// weekOut is landed@1's rows for a scope, the first rows of each list.
type weekOut struct {
	Humans      []report.WeekRow `json:"humans"`
	HumansTotal int              `json:"humans_total"`
	Bots        []report.WeekRow `json:"bots"`
	BotsTotal   int              `json:"bots_total"`
}

func stillHereOf(t *report.StillHere, rows int) *stillHereOut {
	if t == nil {
		return nil
	}
	out := &stillHereOut{FilesBlamed: t.FilesBlamed}
	out.Humans, out.HumansTotal = cut(t.Humans, rows)
	out.Bots, out.BotsTotal = cut(t.Bots, rows)
	return out
}

func weekOf(t *report.WeekTable, rows int) *weekOut {
	if t == nil {
		return nil
	}
	out := &weekOut{}
	out.Humans, out.HumansTotal = cut(t.Humans, rows)
	out.Bots, out.BotsTotal = cut(t.Bots, rows)
	return out
}

func overviewOf(o report.Overview, f format) overviewItem {
	item := overviewItem{
		Scope: o.Scope, Totals: o.Totals,
		People: stillHereOf(o.People, f.rows), Week: weekOf(o.Week, f.rows),
		Concentration: o.Concentration, KnowledgeLoss: o.KnowledgeLoss, AIAssisted: o.AIAssisted, BotShare: o.BotShare,
	}
	if f.detailed {
		pv := o.Provenance
		item.Provenance, item.Caveats = &pv, o.Caveats
	}
	return item
}

// repoSummary is one repository in a list: its card's numbers on one row.
type repoSummary struct {
	Name          string          `json:"name"`
	Key           string          `json:"key"`
	Group         string          `json:"group"`
	Status        string          `json:"status"`          // ok, or error when the repository failed
	Error         string          `json:"error,omitempty"` // why it failed
	LineFacts     bool            `json:"line_facts"`      // blame ran: false for shallow and partial clones
	AgeDays       *int            `json:"age_days"`
	Size          report.Size     `json:"size"`
	People        *int            `json:"people"` // humans with surviving prod lines
	Bots          *int            `json:"bots"`
	MadeOf        []madeOf        `json:"made_of"` // the top humans by surviving prod lines
	Concentration *int            `json:"authors_in_prefix"`
	KnowledgeLoss *report.Share   `json:"knowledge_loss"`
	AIAssisted    *report.Share   `json:"ai_assisted"`
	BotShare      *report.Share   `json:"bot_share"`
	Landings      report.Landings `json:"landings"`
	Caveats       []string        `json:"caveats"` // the codes of its card's caveats beyond the standing ones; overview(scope=repo) says them in full
}

// madeOf is one of the people a repository is made of.
type madeOf struct {
	Name  string  `json:"name"`
	Email string  `json:"email"`
	Share float64 `json:"share"` // of the repository's surviving prod lines
}

func summaryOf(r report.Repo, rows int) repoSummary {
	c := r.Card
	s := repoSummary{
		Name: r.Name, Key: r.Key, Group: r.Group, Status: r.Status, Error: r.Error, LineFacts: r.LineFacts,
		Size: c.Size, People: c.People, Bots: c.Bots,
		KnowledgeLoss: c.KnowledgeLoss, AIAssisted: c.AIAssisted, BotShare: c.BotShare, Landings: c.Landings,
	}
	if c.Age != nil {
		days := c.Age.Days
		s.AgeDays = &days
	}
	if c.Concentration != nil {
		n := c.Concentration.AuthorsInPrefix
		s.Concentration = &n
	}
	authors, _ := cut(c.MadeOf, min(rows, 3))
	for _, a := range authors {
		s.MadeOf = append(s.MadeOf, madeOf{Name: a.Name, Email: a.Email, Share: a.Share})
	}
	for _, cv := range c.Caveats {
		if cv.Code != "not-fetched" && cv.Code != "ai-lower-bound" {
			s.Caveats = append(s.Caveats, cv.Code)
		}
	}
	return s
}

// repoDetail is one repository whole, as the report has it, with its
// tables cut to the answer's rows.
func repoDetail(r report.Repo, rows int) report.Repo {
	if r.StillHere != nil {
		t := *r.StillHere
		t.Humans, _ = cut(t.Humans, rows)
		t.Bots, _ = cut(t.Bots, rows)
		r.StillHere = &t
	}
	if r.Week != nil {
		t := *r.Week
		t.Humans, _ = cut(t.Humans, rows)
		t.Bots, _ = cut(t.Bots, rows)
		r.Week = &t
	}
	r.Errors.Blame, _ = cut(r.Errors.Blame, rows)
	r.IgnoreRevs.Revs, _ = cut(r.IgnoreRevs.Revs, rows)
	return r
}

// rankTableOut is one rank table: everyone eligible with their 95% rank
// interval, the people below the minimum sample, and bots apart, each list
// cut to the answer's rows.
type rankTableOut struct {
	Metric        string             `json:"metric"`
	Question      string             `json:"question"`
	Scope         report.Scope       `json:"scope"`
	Window        report.Interval    `json:"window"`
	Percent       bool               `json:"percent"`
	MinN          int                `json:"min_n"`
	Provenance    *report.Provenance `json:"provenance,omitzero"` // detailed only
	Ranked        []report.RankRow   `json:"ranked"`
	RankedTotal   int                `json:"ranked_total"`
	BelowMin      []report.RankRow   `json:"below_min"`
	BelowMinTotal int                `json:"below_min_total"`
	Bots          []report.RankRow   `json:"bots"`
	BotsTotal     int                `json:"bots_total"`
	Caveats       []report.Caveat    `json:"caveats"`
}

func rankTableOf(t report.RankTable, f format) rankTableOut {
	out := rankTableOut{
		Metric: t.Metric, Question: t.Question, Scope: t.Scope, Window: t.Window, Percent: t.Percent, MinN: t.MinN,
		Caveats: t.Caveats,
	}
	if f.detailed {
		pv := t.Provenance
		out.Provenance = &pv
	}
	out.Ranked, out.RankedTotal = cut(t.Ranked, f.rows)
	out.BelowMin, out.BelowMinTotal = cut(t.BelowMin, f.rows)
	out.Bots, out.BotsTotal = cut(t.Bots, f.rows)
	return out
}

func overviewAnswer(a *app.OverviewAnswer, sc scopeOut, f format) *overviewOut {
	out := &overviewOut{
		head:     head{Provenance: a.Provenance},
		Scope:    sc,
		Run:      runOut{ID: a.Run.ID, Status: a.Run.Status, ToolVersion: a.Run.ToolVersion},
		Warnings: a.Warnings,
		tail:     tail{Caveats: a.Caveats},
	}
	if f.detailed {
		out.Run.Options = a.Run.Options
	}
	for _, o := range a.Overviews {
		item := overviewOf(o, f)
		if sc.Kind == domain.ScopeAll && o.Scope.Kind == domain.ScopeGroup && !f.detailed {
			// The whole run is an index of its platforms: each group's
			// totals and shares, and its people in overview of the group.
			item.People, item.Week = nil, nil
		}
		out.Overviews = append(out.Overviews, item)
	}
	for _, t := range a.Ranks {
		out.Ranks = append(out.Ranks, rankTableOf(t, f))
	}
	repos, pg := paginate(a.Repos, f, a.Run.ID)
	out.Page = pg
	if f.detailed || sc.Kind == domain.ScopeRepo {
		// One repository is answered whole: its card and its tables.
		var detail []report.Repo
		for _, r := range repos {
			detail = append(detail, repoDetail(r, f.rows))
		}
		out.Repos = detail
		if sc.Kind == domain.ScopeRepo {
			out.Page = nil
		}
	} else {
		var summary []repoSummary
		for _, r := range repos {
			summary = append(summary, summaryOf(r, f.rows))
		}
		out.Repos = summary
	}
	return out
}

// The person tool.

type personOut struct {
	head
	Person personRef `json:"person"`
	// OwnPage is true when the person is the user the server runs for
	// (--me): the answer is their own page and holds their own
	// AI-assisted share, which no one else's answer shows (ADR 0007).
	OwnPage bool            `json:"own_page"`
	Values  []valueOut      `json:"values"`
	Ranks   []report.MeRank `json:"ranks"`
	tail
}

// personRef names the person an answer is about.
type personRef struct {
	Name  string   `json:"name"`
	Key   string   `json:"key"` // the canonical email (an id, p-…, unless --allow-emails), or "@login"
	Bot   bool     `json:"bot"`
	Repos []string `json:"repos"` // the repositories they wrote in or hold a value about
}

// valueOut is one metric value about the answer's subject.
type valueOut struct {
	Metric string             `json:"metric"`
	Scope  report.Scope       `json:"scope"`
	Window report.Interval    `json:"window,omitzero"` // absent for a value at HEAD, such as surviving@1's
	Value  float64            `json:"value"`
	Num    float64            `json:"num"`
	Den    float64            `json:"den"`
	N      int                `json:"n"`
	Detail map[string]float64 `json:"detail"`
}

func valuesOf(vs []report.Value) []valueOut {
	var out []valueOut
	for _, v := range vs {
		out = append(out, valueOut{Metric: v.Metric, Scope: v.Scope, Window: v.Window, Value: v.Value, Num: v.Num,
			Den: v.Den, N: v.N, Detail: v.Detail})
	}
	return out
}

// wideValues keeps the values a concise person answer needs: those about
// the run's groups, and those about the whole run unless they repeat the
// one group's (the person holds code in one group and nowhere else).
// Per-repository values are the detailed answer's.
func wideValues(vs []report.Value) []report.Value {
	inGroups := map[string][]report.Value{} // metric → its values in group scopes
	for _, v := range vs {
		if v.Scope.Kind == domain.ScopeGroup {
			inGroups[v.Metric] = append(inGroups[v.Metric], v)
		}
	}
	var out []report.Value
	for _, v := range vs {
		switch v.Scope.Kind {
		case domain.ScopeGroup:
			out = append(out, v)
		case domain.ScopeAll:
			if g := inGroups[v.Metric]; len(g) != 1 || !sameNumbers(g[0], v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// sameNumbers reports whether two values of one metric say the same.
func sameNumbers(a, b report.Value) bool {
	return a.Value == b.Value && a.Num == b.Num && a.Den == b.Den && a.N == b.N &&
		a.Window.Since.Equal(b.Window.Since) && a.Window.Until.Equal(b.Window.Until)
}

func personAnswer(me *report.Me, p app.NamedPerson, own bool, f format) *personOut {
	out := &personOut{
		head:    head{Provenance: me.Provenance},
		Person:  personRef{Name: me.Person.Name, Key: me.Person.Key, Bot: me.Person.Bot, Repos: p.Repos},
		OwnPage: own,
		Ranks:   me.Ranks,
		tail:    tail{Caveats: me.Caveats},
	}
	values := me.Values
	if !f.detailed {
		values = wideValues(values)
	}
	out.Values = valuesOf(values)
	return out
}

// The who_knows tool.

type whoKnowsOut struct {
	head
	Path  string         `json:"path"` // the repo-relative prefix; "" for the whole repository
	Repo  string         `json:"repo"` // the repository searched, or "" for every one
	Repos []whoKnowsRepo `json:"repos"`
	tail
}

// whoKnowsRepo is who knows the path in one repository, each list cut to
// the answer's rows.
type whoKnowsRepo struct {
	report.WhoKnowsRepo
	MajorityOwnersTotal int `json:"majority_owners_total"`
	RecentLandersTotal  int `json:"recent_landers_total"`
	BotsTotal           int `json:"bots_total"`
}

func whoKnowsAnswer(w *report.WhoKnows, repoName string, f format) *whoKnowsOut {
	out := &whoKnowsOut{head: head{Provenance: w.Provenance}, Path: w.Path, Repo: repoName, tail: tail{Caveats: w.Caveats}}
	repos, pg := paginate(w.Repos, f, w.Provenance.RunID)
	if pg.Total > 1 || pg.NextCursor != nil {
		out.Page = pg
	}
	for _, r := range repos {
		o := whoKnowsRepo{WhoKnowsRepo: r}
		o.MajorityOwners, o.MajorityOwnersTotal = cut(r.MajorityOwners, f.rows)
		o.RecentLanders, o.RecentLandersTotal = cut(r.RecentLanders, f.rows)
		o.Bots, o.BotsTotal = cut(r.Bots, f.rows)
		out.Repos = append(out.Repos, o)
	}
	return out
}

// The changes tool.

type changesOut struct {
	head
	Scope   scopeOut        `json:"scope"`
	Window  report.Interval `json:"window"` // [since, the run's as-of)
	Person  string          `json:"person,omitempty"`
	Changes any             `json:"changes"` // concise: []changeBrief; detailed: []app.Change
	tail
}

// changeBrief is one landing in a concise list.
type changeBrief struct {
	Repo       string        `json:"repo"`
	Landing    string        `json:"landing"` // its first 12 hex digits
	Time       time.Time     `json:"time"`
	Merge      bool          `json:"merge,omitzero"`
	Title      string        `json:"title,omitempty"` // only with --allow-source
	People     []personBrief `json:"people"`
	MergedBy   string        `json:"merged_by,omitempty"` // name
	Reverts    []string      `json:"reverts,omitzero"`
	RevertedBy []string      `json:"reverted_by,omitzero"`
}

// personBrief is one person's part in a change, in a concise list.
type personBrief struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	Bot  bool   `json:"bot,omitzero"`
	// Durable is durable@1's cluster: of the prod lines the person added,
	// those blame still gives them. Absent outside its cohort (changes 90
	// to 365 days old).
	Durable *kept `json:"durable,omitzero"`
	// Removed is simplifying@1's cluster: the prod lines they removed.
	// Absent outside its 90-day window, and for a revert or a reverted
	// landing.
	Removed    *float64 `json:"removed,omitzero"`
	AIAssisted *bool    `json:"ai_assisted,omitzero"` // only on the entry of the user the server runs for
}

// kept is a durable@1 cluster in a concise answer.
type kept struct {
	Kept  float64 `json:"kept"`
	Added float64 `json:"added"`
}

func shortSHAs(shas []string) []string {
	var out []string
	for _, s := range shas {
		out = append(out, shortSHA(s))
	}
	return out
}

func changesAnswer(a *app.ChangesAnswer, sc scopeOut, person string, f format) *changesOut {
	out := &changesOut{head: head{Provenance: a.Provenance}, Scope: sc, Window: a.Window, Person: person,
		tail: tail{Caveats: a.Caveats}}
	changes, pg := paginate(a.Changes, f, a.Provenance.RunID)
	out.Page = pg
	if f.detailed {
		out.Changes = changes
		if changes == nil {
			out.Changes = []app.Change{}
		}
		return out
	}
	brief := []changeBrief{}
	for _, c := range changes {
		b := changeBrief{Repo: c.Repo, Landing: shortSHA(c.Landing), Time: c.Time, Merge: c.Merge, Title: c.Title,
			Reverts: shortSHAs(c.Reverts), RevertedBy: shortSHAs(c.RevertedBy)}
		if c.MergedBy != nil {
			b.MergedBy = c.MergedBy.Name
		}
		for _, p := range c.People {
			pb := personBrief{Name: p.Person.Name, Key: p.Person.Key, Bot: p.Person.Bot, AIAssisted: p.AIAssisted}
			if p.Durable != nil {
				pb.Durable = &kept{Kept: p.Durable.Num, Added: p.Durable.Den}
			}
			if p.Simplifying != nil {
				removed := p.Simplifying.Num
				pb.Removed = &removed
			}
			b.People = append(b.People, pb)
		}
		brief = append(brief, b)
	}
	out.Changes = brief
	return out
}

// The explain tool.

type explainOut struct {
	head
	Metric  any             `json:"metric"` // concise: metricBrief; detailed: report.MetricInfo
	Subject report.Subject  `json:"subject"`
	Values  []valueOut      `json:"values"`
	Changes []report.Change `json:"changes"` // a ranked metric's counted changes for a person, newest last
	tail
}

// metricBrief is a metric's definition in a concise answer.
type metricBrief struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	Unit        string `json:"unit,omitempty"`
	Ranked      bool   `json:"ranked"`
	Eligibility string `json:"eligibility,omitempty"`
}

func briefOf(m report.MetricInfo, unit bool) metricBrief {
	b := metricBrief{ID: m.ID, Question: m.Question, Ranked: m.Ranked}
	if unit {
		b.Unit, b.Eligibility = m.Unit, m.Eligibility
	}
	return b
}

func explainAnswer(e *report.Explain, allowSource bool, f format) *explainOut {
	out := &explainOut{head: head{Provenance: e.Provenance}, Subject: e.Subject, Values: valuesOf(e.Values),
		tail: tail{Caveats: e.Caveats}}
	if f.detailed {
		out.Metric = e.Metric
	} else {
		out.Metric = briefOf(e.Metric, true)
	}
	changes, pg := paginate(e.Changes, f, e.Provenance.RunID)
	if pg.Total > 0 {
		out.Page = pg
	}
	for _, c := range changes {
		if !allowSource {
			c.Title = "" // a commit subject is source: local output only (domain.Commit)
		}
		if !f.detailed {
			c.Landing = shortSHA(c.Landing)
		}
		out.Changes = append(out.Changes, c)
	}
	return out
}

// The highlights tool.

type highlightsOut struct {
	head
	Scope   scopeOut              `json:"scope"`
	Period  report.Interval       `json:"period"`
	TopTier []app.TopTier         `json:"top_tier"`
	Lasted  []app.HighlightChange `json:"lasted"`
	Removed []app.HighlightChange `json:"removed"`
	tail
}

func highlightsAnswer(h *app.HighlightsAnswer, sc scopeOut, f format) *highlightsOut {
	out := &highlightsOut{head: head{Provenance: h.Provenance}, Scope: sc, Period: h.Period, TopTier: h.TopTier,
		tail: tail{Caveats: h.Caveats}}
	shorten := func(cs []app.HighlightChange) []app.HighlightChange {
		var o []app.HighlightChange
		for _, c := range cs {
			if !f.detailed {
				c.Landing = shortSHA(c.Landing)
			}
			o = append(o, c)
		}
		return o
	}
	out.Lasted, out.Removed = shorten(h.Lasted), shorten(h.Removed)
	for i := range out.TopTier {
		out.TopTier[i].People, _ = cut(out.TopTier[i].People, f.rows)
	}
	return out
}

// The metrics tool.

type metricsOut struct {
	Provenance registryOut     `json:"provenance"`
	Metrics    any             `json:"metrics"` // concise: []metricBrief; detailed, or one metric: []report.MetricInfo
	Caveats    []report.Caveat `json:"caveats"`
}

// registryOut is where the metric definitions come from: this binary's
// registry, not a run.
type registryOut struct {
	Source  string   `json:"source"`  // "registry": definitions in code, the same in every run of this version
	Version string   `json:"version"` // this stillhere's version
	Metrics []string `json:"metrics"` // every metric@version the registry holds
}

func metricsAnswer(c *report.Catalog, version, one string, f format) (*metricsOut, error) {
	out := &metricsOut{Provenance: registryOut{Source: "registry", Version: version}, Caveats: []report.Caveat{}}
	for _, m := range c.Metrics {
		out.Provenance.Metrics = append(out.Provenance.Metrics, m.ID)
	}
	if one != "" {
		id, err := domain.ParseMetricID(strings.TrimSpace(one))
		if err != nil {
			return nil, err
		}
		var match []report.MetricInfo
		latest := -1
		for i, m := range c.Metrics {
			name, version, _ := strings.Cut(m.ID, "@")
			v, _ := strconv.Atoi(version)
			switch {
			case name != id.Name:
			case id.Version == v:
				match = []report.MetricInfo{m}
			case id.Version == 0 && (latest < 0 || v > latest):
				latest, match = v, []report.MetricInfo{c.Metrics[i]}
			}
		}
		if len(match) == 0 {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("unknown metric %q: the registry has %s",
				one, strings.Join(out.Provenance.Metrics, ", "))}
		}
		out.Metrics = match
		return out, nil
	}
	if f.detailed {
		out.Metrics = c.Metrics
		return out, nil
	}
	var brief []metricBrief
	for _, m := range c.Metrics {
		brief = append(brief, briefOf(m, false))
	}
	out.Metrics = brief
	return out, nil
}

// The diff tool.

type diffOut struct {
	head
	Repo       string         `json:"repo"`
	RepoKey    string         `json:"repo_key"`
	Landing    string         `json:"landing"`
	Time       time.Time      `json:"time"`
	Merge      bool           `json:"merge"`
	Title      string         `json:"title"`
	Base       string         `json:"base"`
	Path       string         `json:"path"`
	Files      []app.DiffFile `json:"files"`
	FilesTotal int            `json:"files_total"`
	Patch      string         `json:"patch"`
	Truncated  bool           `json:"truncated"`
	tail
}

func diffAnswer(d *app.DiffAnswer, f format) *diffOut {
	out := &diffOut{head: head{Provenance: d.Provenance}, Repo: d.Repo, RepoKey: d.RepoKey, Landing: d.Landing,
		Time: d.Time, Merge: d.Merge, Title: d.Title, Base: d.Base, Path: d.Path, Patch: d.Patch, Truncated: d.Truncated,
		tail: tail{Caveats: d.Caveats}}
	out.Files, out.FilesTotal = cut(d.Files, f.limit)
	return out
}

// The identity_candidates tool serves report.Identities as #26 builds it,
// whole: its shape is #26's, so this server needs no change when the
// candidates and their evidence land.
type identitiesOut struct {
	*report.Identities
	Withheld []string `json:"withheld"`
}

func (o *identitiesOut) provenance() *report.Provenance { return &o.Provenance }
