package mcp

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
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
// "concise" (the default), and in either form a page of the answer's main
// list and the first rows of every other list, with its total (Anthropic,
// "Writing effective tools for agents", 2025: names rather than ids,
// concise or detailed, paging under the client's token cap). No list in an
// answer grows with the platform unbounded: a list is paged, cut to rows
// with a *_total, or bounded by the registry. It computes no number: it
// only selects and trims.

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
	// off by half and still fit. An answer over it is cut until it fits.
	answerBudget = 40000
	maxLimit     = 100  // the largest page a call may ask for
	minRows      = 3    // the shortest list an answer cuts a table to
	minPatch     = 1000 // the smallest patch the budget cuts a diff to, in bytes
	// headsAlways is how many repositories an answer covers before its
	// provenance lists only the HEADs of those it names.
	headsAlways = 10
)

// format is how much of an answer to give: the page of the answer's main
// list, the rows of every other list inside it, and the parts the budget
// can cut.
type format struct {
	detailed bool
	offset   int  // the first item of the page
	limit    int  // items in the page
	asked    int  // the limit the agent asked for, 0 for none: a page cut below it says so (page.Note)
	rows     int  // rows of each other list, such as an overview's people
	allHeads bool // provenance lists every repository's HEAD (detailed), until the budget needs the room
	patch    int  // diff: the bytes of patch the answer holds; 0 for the use case's own cut
}

// shrink cuts f for an answer over the budget, the least useful part
// first: the HEADs of repositories the answer does not name, then the
// patch, then the page, then the rows of each list. It reports false when
// nothing is left to cut.
func (f *format) shrink() bool {
	switch {
	case f.allHeads:
		f.allHeads = false
	case f.patch > minPatch:
		f.patch = max(minPatch, f.patch/2)
	case f.limit > 1:
		f.limit = (f.limit + 1) / 2
	case f.rows > minRows:
		f.rows = max(minRows, f.rows/2)
	default:
		return false
	}
	return true
}

// provenanceOut is a section's provenance as an answer gives it: the
// report's, with the HEADs cut to the repositories the answer names once
// it covers more than headsAlways, and their count and digest, so the list
// never grows with the platform and the HEAD set stays checkable.
type provenanceOut struct {
	report.Provenance
	// HeadsTotal counts every repository the section's numbers cover;
	// heads lists them all in a detailed answer, and in any answer that
	// covers headsAlways or fewer.
	HeadsTotal int `json:"heads_total"`
	// HeadsSHA256 digests all of them, as "repo key branch sha" lines in
	// heads order, so two answers can be checked against one HEAD set.
	HeadsSHA256 string `json:"heads_sha256"`
	// ForgeReposTotal counts forge.repos before they were cut with heads;
	// absent when they were not cut. omitzero, not omitempty:
	// encoding/json/v2 omits only empty JSON values (a string, a list, an
	// object), never a 0 or a false.
	ForgeReposTotal int `json:"forge_repos_total,omitzero"`
}

// named is the set of repositories an answer names, by key or by name.
type named struct{ keys, names map[string]bool }

func newNamed() named { return named{keys: map[string]bool{}, names: map[string]bool{}} }

func (n named) key(k string)  { n.keys[k] = true }
func (n named) name(s string) { n.names[s] = true }

// provenance shapes a section's provenance for an answer that names the
// repositories in n.
func (f format) provenance(pv report.Provenance, n named) provenanceOut {
	out := provenanceOut{Provenance: pv, HeadsTotal: len(pv.Heads)}
	var lines bytes.Buffer
	for _, h := range pv.Heads {
		fmt.Fprintf(&lines, "%s %s %s %s\n", h.Repo, h.Key, h.Branch, h.SHA)
	}
	sum := sha256.Sum256(lines.Bytes())
	out.HeadsSHA256 = hex.EncodeToString(sum[:])
	if f.allHeads || len(pv.Heads) <= headsAlways {
		return out
	}
	out.Heads = nil
	for _, h := range pv.Heads {
		if n.keys[h.Key] || n.names[h.Repo] {
			out.Heads = append(out.Heads, h)
		}
	}
	if pv.Forge != nil {
		// The GitHub repositories are cut with the heads they join, and as_of
		// stays the oldest sync over all of them, which dates the numbers.
		forge := *pv.Forge
		out.ForgeReposTotal, forge.Repos = len(forge.Repos), nil
		for _, s := range pv.Forge.Repos {
			if n.keys[s.Key] {
				forge.Repos = append(forge.Repos, s)
			}
		}
		out.Forge = &forge
	}
	return out
}

// head opens every answer about a run: where its numbers come from.
type head struct {
	Provenance provenanceOut `json:"provenance"`
}

func (h *head) provenance() *report.Provenance { return &h.Provenance.Provenance }

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
	// Note says why Limit is below the limit the agent asked for: the size
	// every answer is cut to; absent when the page holds what was asked
	// (round 2: identity_candidates(limit: 100) answered 25 a page without
	// saying why).
	Note string `json:"note,omitzero"`
}

// paginate cuts the page f asks for out of items, in a run.
func paginate[T any](items []T, f format, run int64) ([]T, *page) {
	p := &page{Offset: min(f.offset, len(items)), Limit: f.limit, Total: len(items)}
	if f.asked > f.limit {
		// The schema refuses a limit above maxLimit before a call gets
		// here, so the only cut below the limit asked is the budget's.
		p.Note = fmt.Sprintf("limit %d, not the %d asked: the answer is cut to fit the size every answer is held to; "+
			"next_cursor gives the rest, a page at a time", f.limit, f.asked)
	}
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

// quietCut is a knowledge-loss share as an answer sends it: its going-quiet
// people (report.ShareGoingQuiet.Holders, the soonest first) cut to n, a
// list's rows, in a copy, since the report's model is shared; going_quiet's
// people counts them all.
func quietCut(sh *report.Share, n int) *report.Share {
	if sh == nil || sh.GoingQuiet == nil || sh.GoingQuiet.Holders == nil || n <= 0 || len(*sh.GoingQuiet.Holders) <= n {
		return sh
	}
	c, q := *sh, *sh.GoingQuiet
	holders := (*q.Holders)[:n]
	q.Holders = &holders
	c.GoingQuiet = &q
	return &c
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

// The AI-assisted shares of the whole run, groups and repositories. A share
// over the landings of one or two people is close to one person's own
// share, which ADR 0007 shows only to that person, and shares add up over
// the scopes, so a share is withheld where it is small and wherever,
// beside the shares that are shown, it would give a small one away. The
// run's one decision (app.AIShares, read with its vocabulary) says which:
// every answer reads it, so no two answers disagree and none can be
// subtracted from another. The egress is stricter than the local report,
// as it is for emails.

// withheldWhy is why a share is withheld, as a caveat and a refusal say it,
// without pointing at a share it could be worked out from.
func withheldWhy() string {
	return fmt.Sprintf("fewer than %d people landed in its window, or it would let a share that is withheld be "+
		"worked out from those shown. A share of one or two people is close to one person's own, which only that "+
		"person sees (ADR 0007)", app.AIShareMinPeople)
}

// aiCaveat says that an answer withheld the AI-assisted share of scopes.
func aiCaveat(scopes []string) report.Caveat {
	scopes = slices.Compact(slices.Sorted(slices.Values(scopes)))
	if len(scopes) > maxCandidates {
		scopes = append(scopes[:maxCandidates], fmt.Sprintf("%d more", len(scopes)-maxCandidates))
	}
	return report.Caveat{Code: "ai-few-people", Message: fmt.Sprintf("The AI-assisted share of %s is withheld: %s.",
		strings.Join(scopes, ", "), withheldWhy())}
}

// showsAI reports whether the run's decision lets a scope's AI-assisted
// share leave; none does without the run's vocabulary.
func showsAI(names *app.Names, sc report.Scope) bool {
	return names != nil && names.AIShares.Show(domain.Scope{Kind: sc.Kind, Key: sc.Key})
}

// The overview tool.

// areasOut is a repository's top-level directories with their knowledge
// numbers, largest first (report.Areas, #106): Areas the first rows of
// them and AreasTotal their count. Both are null for a repository without
// line facts (a shallow or partial clone, whose caveat and line_facts say
// why), whose areas are unknown rather than none (MET-4: no value, never
// 0).
type areasOut struct {
	Areas      *[]report.Area `json:"areas"`
	AreasTotal *int           `json:"areas_total"`
}

type overviewOut struct {
	head
	Scope     scopeOut       `json:"scope"`
	Run       runOut         `json:"run"`
	Overviews []overviewItem `json:"overviews"`
	Repos     any            `json:"repos"` // concise: []repoSummary; detailed: []repoOut
	// areasOut is one repository's areas; nil, and so absent, for any
	// other scope.
	*areasOut
	Ranks    []rankTableOut   `json:"ranks"`
	Warnings []report.Warning `json:"warnings"`
	// WarningsTotal counts the run's warnings, of which warnings holds the
	// first rows.
	WarningsTotal int `json:"warnings_total"`
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
	Scope report.Scope `json:"scope"`
	// ScopeArg is the exact string to pass as scope for this group
	// (resolver.scopeArg); absent for the whole run.
	ScopeArg      string                `json:"scope_arg,omitempty"`
	Provenance    *provenanceOut        `json:"provenance,omitzero"` // detailed only: the answer's covers it
	Totals        report.Totals         `json:"totals"`
	People        *stillHereOut         `json:"people"`
	Week          *weekOut              `json:"week"`
	Concentration *report.Concentration `json:"concentration"`
	KnowledgeLoss *report.Share         `json:"knowledge_loss"`
	AIAssisted    *report.Share         `json:"ai_assisted"`
	BotShare      *report.Share         `json:"bot_share"`
	Caveats       []report.Caveat       `json:"caveats,omitzero"` // detailed only
	// ReviewCoverage is review_coverage@1 about the scope (#107); null
	// without GitHub data.
	ReviewCoverage *report.ReviewCoverage `json:"review_coverage"`
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

// weekOut is landed@2's table for a scope (#108): its total, each landing
// once, and the first rows of each list. null, as the report's week, when
// the scope's history does not cover the window (#74).
type weekOut struct {
	Total       *report.WeekTotal `json:"total"`
	Humans      []report.WeekRow  `json:"humans"`
	HumansTotal int               `json:"humans_total"`
	Bots        []report.WeekRow  `json:"bots"`
	BotsTotal   int               `json:"bots_total"`
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
	out := &weekOut{Total: t.Total}
	out.Humans, out.HumansTotal = cut(t.Humans, rows)
	out.Bots, out.BotsTotal = cut(t.Bots, rows)
	return out
}

// shaper shapes the answers of one call: its format, the run's vocabulary
// (nil for an answer that reads no run) and what the answer withheld.
type shaper struct {
	f          format
	names      *app.Names
	w          *withholder // the egress filter the answer will pass; nil when emails may leave
	aiWithheld []string    // the scopes whose AI-assisted share was withheld
	// aiDetailWithheld: a shown AI-assisted share left without its detail
	// (withholdAI)
	aiDetailWithheld bool
}

// share returns a scope's AI-assisted share as it may leave: nil, its num
// and den with it, noted for the caveat, when the run's decision withholds
// it.
func (s *shaper) share(sh *report.Share, sc report.Scope, name string) *report.Share {
	if sh == nil || showsAI(s.names, sc) {
		return sh
	}
	s.aiWithheld = append(s.aiWithheld, name)
	return nil
}

// scopeName names a scope in a caveat.
func scopeName(sc report.Scope, repoName string) string {
	switch sc.Kind {
	case domain.ScopeGroup:
		return "group " + strconv.Quote(sc.Key)
	case domain.ScopeRepo:
		return "repository " + strconv.Quote(repoName)
	}
	return "the whole run"
}

func (s *shaper) overview(o report.Overview, n named) overviewItem {
	f := s.f
	item := overviewItem{
		Scope: o.Scope, ScopeArg: s.scopeArg(o.Scope), Totals: o.Totals,
		People: stillHereOf(o.People, f.rows), Week: weekOf(o.Week, f.rows),
		Concentration: o.Concentration, KnowledgeLoss: quietCut(o.KnowledgeLoss, f.rows), BotShare: o.BotShare,
		AIAssisted:     s.share(o.AIAssisted, o.Scope, scopeName(o.Scope, "")),
		ReviewCoverage: o.ReviewCoverage,
	}
	if f.detailed {
		pv := f.provenance(o.Provenance, n)
		item.Provenance, item.Caveats = &pv, o.Caveats
	}
	return item
}

// repoSummary is one repository in a list: its card's numbers on one row.
type repoSummary struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Group string `json:"group"`
	// ScopeArg is the exact string to pass as scope or repo for this
	// repository: its name unless a group or another repository shares it
	// (resolver.scopeArg).
	ScopeArg      string          `json:"scope_arg"`
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
	// ReviewCoverage is review_coverage@1 about the repository (#107);
	// null without GitHub data for it.
	ReviewCoverage *report.ReviewCoverage `json:"review_coverage"`
}

// madeOf is one of the people a repository is made of, with their last
// activity, as the card's (report.Author): when, what, and the days left
// before knowledge_loss@1 counts them as gone quiet.
type madeOf struct {
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	Share        float64    `json:"share"` // of the repository's surviving prod lines
	LastActive   *time.Time `json:"last_active"`
	LastActivity string     `json:"last_activity"`
	QuietInDays  *int       `json:"quiet_in_days"`
	GoingQuiet   bool       `json:"going_quiet"`
}

func (s *shaper) summary(r report.Repo) repoSummary {
	c := r.Card
	sc := report.Scope{Kind: domain.ScopeRepo, Key: r.Key}
	out := repoSummary{
		Name: r.Name, Key: r.Key, Group: r.Group, ScopeArg: s.scopeArg(report.Scope{Kind: domain.ScopeRepo, Key: r.Key}),
		Status: r.Status, Error: r.Error, LineFacts: r.LineFacts,
		Size: c.Size, People: c.People, Bots: c.Bots,
		KnowledgeLoss: quietCut(c.KnowledgeLoss, s.f.rows), AIAssisted: s.share(c.AIAssisted, sc, scopeName(sc, r.Name)), BotShare: c.BotShare,
		ReviewCoverage: c.ReviewCoverage, Landings: c.Landings,
	}
	if c.Age != nil {
		days := c.Age.Days
		out.AgeDays = &days
	}
	if c.Concentration != nil {
		n := c.Concentration.AuthorsInPrefix
		out.Concentration = &n
	}
	authors, _ := cut(c.MadeOf, min(s.f.rows, 3))
	for _, a := range authors {
		out.MadeOf = append(out.MadeOf, madeOf{Name: a.Name, Email: a.Email, Share: a.Share, LastActive: a.LastActive,
			LastActivity: a.LastActivity, QuietInDays: a.QuietInDays, GoingQuiet: a.GoingQuiet})
	}
	for _, cv := range c.Caveats {
		if !cv.Standing() {
			out.Caveats = append(out.Caveats, cv.Code)
		}
	}
	return out
}

// scopeArg is the exact string an agent passes to name a scope of the
// answer's run (resolver.scopeArg).
func (s *shaper) scopeArg(sc report.Scope) string {
	if s.names == nil {
		return ""
	}
	return (&resolver{names: s.names}).scopeArg(domain.Scope{Kind: sc.Kind, Key: sc.Key})
}

// repoOut is one repository whole in an answer: the report's, and the
// string that names it as a scope.
type repoOut struct {
	ScopeArg string `json:"scope_arg"`
	report.Repo
}

// repoDetail is one repository whole, as the report has it, with its
// lists cut to the answer's rows.
func (s *shaper) repoDetail(r report.Repo) report.Repo {
	rows := s.f.rows
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
	sc := report.Scope{Kind: domain.ScopeRepo, Key: r.Key}
	r.Card.AIAssisted = s.share(r.Card.AIAssisted, sc, scopeName(sc, r.Name))
	r.Card.MadeOf, _ = cut(r.Card.MadeOf, rows)
	r.Card.Areas, _ = cut(r.Card.Areas, rows)
	r.Warnings, _ = cut(r.Warnings, rows)
	r.Errors.Blame, _ = cut(r.Errors.Blame, rows)
	r.IgnoreRevs.Revs, _ = cut(r.IgnoreRevs.Revs, rows)
	return r
}

// rankTableOut is one rank table: everyone eligible with their 95% rank
// interval, the people below the minimum sample, and bots apart, each list
// cut to the answer's rows, with its metric's validity and reference result
// (report.RankTable's), so the verdict travels with the order it qualifies
// (wave 12).
type rankTableOut struct {
	Metric        string            `json:"metric"`
	Question      string            `json:"question"`
	Scope         report.Scope      `json:"scope"`
	Window        report.Interval   `json:"window"`
	Percent       bool              `json:"percent"`
	MinN          int               `json:"min_n"`
	Validity      string            `json:"validity"`
	Reference     *report.Reference `json:"reference"`
	Provenance    *provenanceOut    `json:"provenance,omitzero"` // detailed only
	Ranked        []report.RankRow  `json:"ranked"`
	RankedTotal   int               `json:"ranked_total"`
	BelowMin      []report.RankRow  `json:"below_min"`
	BelowMinTotal int               `json:"below_min_total"`
	Bots          []report.RankRow  `json:"bots"`
	BotsTotal     int               `json:"bots_total"`
	Caveats       []report.Caveat   `json:"caveats"`
}

func (s *shaper) rankTable(t report.RankTable, n named) rankTableOut {
	out := rankTableOut{
		Metric: t.Metric, Question: t.Question, Scope: t.Scope, Window: t.Window, Percent: t.Percent, MinN: t.MinN,
		Validity: t.Validity, Reference: t.Reference, Caveats: t.Caveats,
	}
	if s.f.detailed {
		pv := s.f.provenance(t.Provenance, n)
		out.Provenance = &pv
	}
	out.Ranked, out.RankedTotal = cut(t.Ranked, s.f.rows)
	out.BelowMin, out.BelowMinTotal = cut(t.BelowMin, s.f.rows)
	out.Bots, out.BotsTotal = cut(t.Bots, s.f.rows)
	return out
}

// overviewAnswer shapes an overview. One repository is answered whole; a
// group pages its repositories; the whole run is an index that pages its
// groups, then its repositories, as one list, with the rank tables of the
// groups on the page.
func (s *shaper) overviewAnswer(a *app.OverviewAnswer, sc scopeOut) *overviewOut {
	f := s.f
	out := &overviewOut{
		Scope: sc,
		Run:   runOut{ID: a.Run.ID, Status: a.Run.Status, ToolVersion: a.Run.ToolVersion},
	}
	out.Warnings, out.WarningsTotal = cut(a.Warnings, f.rows)
	if f.detailed {
		out.Run.Options = a.Run.Options
	}
	var repos []report.Repo
	var overviews []report.Overview // the overviews the answer holds, in the report's order
	var groups map[string]bool      // the groups an index page holds; nil when the scope is not the whole run
	switch sc.Kind {
	case domain.ScopeAll:
		// One list of the run's groups, then its repositories; its overview
		// of every repository (with --scope all, or without groups) is on
		// every page.
		type entry struct {
			group *report.Overview
			repo  *report.Repo
		}
		var entries []entry
		for i := range a.Overviews {
			if a.Overviews[i].Scope.Kind == domain.ScopeGroup {
				entries = append(entries, entry{group: &a.Overviews[i]})
			} else {
				overviews = append(overviews, a.Overviews[i])
			}
		}
		for i := range a.Repos {
			entries = append(entries, entry{repo: &a.Repos[i]})
		}
		pageEntries, pg := paginate(entries, f, a.Run.ID)
		out.Page = pg
		groups = map[string]bool{}
		for _, e := range pageEntries {
			if e.repo != nil {
				repos = append(repos, *e.repo)
				continue
			}
			groups[e.group.Scope.Key] = true
			overviews = append(overviews, *e.group)
		}
	case domain.ScopeRepo:
		repos = a.Repos
		out.areasOut = &areasOut{}
		if len(repos) == 1 && repos[0].LineFacts {
			// Measured: [] when no top-level directory holds a line people
			// hold, as the fixture's prod lines all sit at its root.
			rows, total := cut(a.Areas, f.rows)
			if rows == nil {
				rows = []report.Area{}
			}
			out.Areas, out.AreasTotal = &rows, &total
		}
	default:
		var pg *page
		repos, pg = paginate(a.Repos, f, a.Run.ID)
		out.Page = pg
		overviews = a.Overviews
	}
	n := newNamed()
	for _, r := range repos {
		n.key(r.Key)
	}
	for _, o := range overviews {
		item := s.overview(o, n)
		if groups != nil && o.Scope.Kind == domain.ScopeGroup && !f.detailed {
			// The index of the platforms: each group's totals and shares,
			// and its people in overview of the group.
			item.People, item.Week = nil, nil
		}
		out.Overviews = append(out.Overviews, item)
	}
	for _, t := range a.Ranks {
		if groups != nil && t.Scope.Kind == domain.ScopeGroup && !groups[t.Scope.Key] {
			continue // the index holds the tables of its page's groups
		}
		out.Ranks = append(out.Ranks, s.rankTable(t, n))
	}
	if f.detailed || sc.Kind == domain.ScopeRepo {
		// A repository is answered whole: its card and its tables.
		var detail []repoOut
		for _, r := range repos {
			detail = append(detail, repoOut{ScopeArg: s.scopeArg(report.Scope{Kind: domain.ScopeRepo, Key: r.Key}), Repo: s.repoDetail(r)})
		}
		out.Repos = detail
	} else {
		var summary []repoSummary
		for _, r := range repos {
			summary = append(summary, s.summary(r))
		}
		out.Repos = summary
	}
	out.head = head{Provenance: f.provenance(a.Provenance, n)}
	out.tail = tail{Page: out.Page, Caveats: s.withheldAI(a.Caveats)}
	return out
}

// The values of an answer about one subject.

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

// values selects the values an answer shows about its subject, in the
// report's order: those about the whole run, unless they repeat the one
// group's (the subject holds code in one group and nowhere else); per
// metric, the rows group values with the largest numbers; and with repos,
// as many repository values. It returns how many there were in all, and
// names the repositories it shows in n.
func (s *shaper) values(vs []report.Value, repos bool, n named) ([]valueOut, int) {
	vs = s.withholdAI(vs)
	byKind := map[[2]string][]int{} // metric and scope kind → indexes into vs
	for i, v := range vs {
		k := [2]string{v.Metric, v.Scope.Kind}
		byKind[k] = append(byKind[k], i)
	}
	keep := map[int]bool{}
	for k, idx := range byKind {
		switch k[1] {
		case domain.ScopeAll:
			for _, i := range idx {
				if g := byKind[[2]string{k[0], domain.ScopeGroup}]; len(g) != 1 || !sameNumbers(vs[g[0]], vs[i]) {
					keep[i] = true
				}
			}
		case domain.ScopeGroup, domain.ScopeRepo:
			if k[1] == domain.ScopeRepo && !repos {
				continue
			}
			// The scopes that carry most of the number: the largest
			// denominator (the lines or changes a ratio is over), then
			// the largest numerator.
			ranked := slices.Clone(idx)
			slices.SortFunc(ranked, func(a, b int) int {
				return cmp.Or(cmp.Compare(vs[b].Den, vs[a].Den), cmp.Compare(vs[b].Num, vs[a].Num),
					cmp.Compare(vs[b].N, vs[a].N), cmp.Compare(vs[a].Scope.Key, vs[b].Scope.Key))
			})
			ranked, _ = cut(ranked, s.f.rows)
			for _, i := range ranked {
				keep[i] = true
			}
		}
	}
	var out []valueOut
	for i, v := range vs {
		if !keep[i] {
			continue
		}
		if v.Scope.Kind == domain.ScopeRepo {
			n.key(v.Scope.Key)
		}
		out = append(out, valueOut{Metric: v.Metric, Scope: v.Scope, Window: v.Window, Value: v.Value, Num: v.Num,
			Den: v.Den, N: v.N, Detail: v.Detail})
	}
	return out, len(vs)
}

// withholdAI withholds what the run's decision withholds of the AI-assisted
// shares among an answer's values, wherever report.AIShareIn says they are,
// noted for the caveat. A value that is a share leaves only when it is a
// repository's or a group's whose scope the decision shows, or a person's
// own, which only their own page holds (app.Me). A copy of its scope's
// share in another metric's value (surviving@1's ai_assisted_share about a
// repository or a group) leaves only when that share does: otherwise the
// value leaves without the copy. explain refuses a withheld share before it
// reads any; this keeps every answer that lists values on the same
// decision, whichever metric carries the share.
//
// A share that leaves carries its value, num and den alone, never its
// detail (#87): ai_assisted@1's detail is its cohort comparisons
// (durable_ai and durable_other, their _n counts, reverted_ai and
// reverted_other), which count narrower populations than the share and can
// be one or two people's however many the share covers. The rule keeps
// none of the detail rather than delete the keys known today, so a key a
// later version adds stays on the machine too (fail closed, an allow list
// of nothing), and the answer says what it held back.
func (s *shaper) withholdAI(vs []report.Value) []report.Value {
	var out []report.Value
	for _, v := range vs {
		whole, key := report.AIShareIn(v)
		scopeShare := v.Subject.Kind == domain.SubjectRepo || v.Subject.Kind == domain.SubjectGroup
		shown := scopeShare && showsAI(s.names, v.Scope)
		_, copied := v.Detail[key]
		switch {
		case whole && (v.Subject.Kind == domain.SubjectPerson || shown):
			if len(v.Detail) > 0 {
				v.Detail, s.aiDetailWithheld = map[string]float64{}, true
			}
		case whole:
			s.aiWithheld = append(s.aiWithheld, scopeName(v.Scope, s.repoName(v.Scope.Key)))
			continue
		case key != "" && copied && !shown:
			v.Detail = maps.Clone(v.Detail)
			delete(v.Detail, key)
			s.aiWithheld = append(s.aiWithheld, scopeName(v.Scope, s.repoName(v.Scope.Key)))
		}
		out = append(out, v)
	}
	return out
}

// repoName is a repository's name in the run's vocabulary; its key without
// one.
func (s *shaper) repoName(key string) string {
	if s.names != nil {
		for _, r := range s.names.Repos {
			if r.Key == key {
				return r.Name
			}
		}
	}
	return key
}

// withheldAI adds to an answer's caveats what it withheld of the
// AI-assisted shares: the shares themselves (ai-few-people), and the detail
// of those it shows (ai-detail-withheld), so an agent reads a hole as held
// back rather than as no data.
func (s *shaper) withheldAI(caveats []report.Caveat) []report.Caveat {
	if len(s.aiWithheld) > 0 {
		caveats = append(slices.Clip(caveats), aiCaveat(s.aiWithheld))
	}
	if s.aiDetailWithheld {
		caveats = append(slices.Clip(caveats), report.Caveat{Code: "ai-detail-withheld", Message: "An AI-assisted share " +
			"leaves this server without its detail: how AI-assisted landings held up against the rest (ai_assisted@1's " +
			"cohort comparisons) counts narrower populations than the share, changes 90 to 365 days old and reverted " +
			"landings, which can be one or two people's however many the share covers. The share itself is shown."})
	}
	return caveats
}

// sameNumbers reports whether two values of one metric say the same.
func sameNumbers(a, b report.Value) bool {
	return a.Value == b.Value && a.Num == b.Num && a.Den == b.Den && a.N == b.N &&
		a.Window.Since.Equal(b.Window.Since) && a.Window.Until.Equal(b.Window.Until)
}

// The person tool.

type personOut struct {
	head
	Person personRef `json:"person"`
	// OwnPage is true when the person is the user the server runs for
	// (--me): the answer is their own page and holds their own
	// AI-assisted share, which no one else's answer shows (ADR 0007).
	OwnPage bool       `json:"own_page"`
	Values  []valueOut `json:"values"`
	// ValuesTotal counts every value the run holds about the person:
	// values holds those about the whole run and the largest per group,
	// and in a detailed answer the largest per repository too.
	ValuesTotal int             `json:"values_total"`
	Ranks       []report.MeRank `json:"ranks"`
	RanksTotal  int             `json:"ranks_total"`
	tail
}

// personRef names the person an answer is about.
type personRef struct {
	Name       string   `json:"name"`
	Key        string   `json:"key"` // the canonical email (an id, p-…, unless --allow-emails), or "@login"
	Bot        bool     `json:"bot"`
	Repos      []string `json:"repos"` // the repositories they wrote in or hold a value about, the first rows
	ReposTotal int      `json:"repos_total"`
}

func (s *shaper) personAnswer(me *report.Me, p app.NamedPerson, own bool) *personOut {
	n := newNamed()
	out := &personOut{
		Person:  personRef{Name: me.Person.Name, Key: me.Person.Key, Bot: me.Person.Bot},
		OwnPage: own,
		tail:    tail{Caveats: me.Caveats},
	}
	out.Person.Repos, out.Person.ReposTotal = cut(p.Repos, s.f.rows)
	for _, r := range out.Person.Repos {
		n.name(r)
	}
	out.Values, out.ValuesTotal = s.values(me.Values, s.f.detailed, n)
	out.Ranks, out.RanksTotal = s.ranks(me.Ranks)
	out.head = head{Provenance: s.f.provenance(me.Provenance, n)}
	out.Caveats = s.withheldAI(out.Caveats)
	return out
}

// ranks keeps, per metric, the person's places in the rows tables with the
// most eligible people, in the report's order.
func (s *shaper) ranks(rs []report.MeRank) ([]report.MeRank, int) {
	byMetric := map[string][]int{}
	for i, r := range rs {
		byMetric[r.Metric] = append(byMetric[r.Metric], i)
	}
	keep := map[int]bool{}
	for _, idx := range byMetric {
		ranked := slices.Clone(idx)
		slices.SortFunc(ranked, func(a, b int) int {
			x, y := rs[a], rs[b]
			return cmp.Or(compareBool(y.Eligible, x.Eligible), cmp.Compare(y.Of, x.Of), cmp.Compare(x.Scope.Key, y.Scope.Key))
		})
		ranked, _ = cut(ranked, s.f.rows)
		for _, i := range ranked {
			keep[i] = true
		}
	}
	var out []report.MeRank
	for i, r := range rs {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out, len(rs)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
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
	MajorityOwnersTotal  int `json:"majority_owners_total"`
	RecentLandersTotal   int `json:"recent_landers_total"`
	RecentReviewersTotal int `json:"recent_reviewers_total"`
	BotsTotal            int `json:"bots_total"`
}

func (s *shaper) whoKnowsAnswer(w *report.WhoKnows, repoName string) *whoKnowsOut {
	out := &whoKnowsOut{Path: w.Path, Repo: repoName, tail: tail{Caveats: w.Caveats}}
	repos, pg := paginate(w.Repos, s.f, w.Provenance.RunID)
	if pg.Total > 1 || pg.NextCursor != nil {
		out.Page = pg
	}
	n := newNamed()
	for _, r := range repos {
		o := whoKnowsRepo{WhoKnowsRepo: r}
		o.MajorityOwners, o.MajorityOwnersTotal = cut(r.MajorityOwners, s.f.rows)
		o.RecentLanders, o.RecentLandersTotal = cut(r.RecentLanders, s.f.rows)
		// Every list but the top authors, which are five at most, grows with
		// the code's history: a hot file reviewed by hundreds of people must
		// still fit the budget once its lists are cut to one row.
		o.RecentReviewers, o.RecentReviewersTotal = cut(r.RecentReviewers, s.f.rows)
		o.Bots, o.BotsTotal = cut(r.Bots, s.f.rows)
		out.Repos = append(out.Repos, o)
		n.key(r.Key)
	}
	out.head = head{Provenance: s.f.provenance(w.Provenance, n)}
	return out
}

// The changes tool.

type changesOut struct {
	head
	Scope   scopeOut        `json:"scope"`
	Window  report.Interval `json:"window"` // [since, the run's as-of)
	Person  string          `json:"person,omitempty"`
	Changes any             `json:"changes"` // concise: []changeBrief; detailed: []changeDetail
	tail
}

// changeBrief is one landing in a concise list.
type changeBrief struct {
	Repo        string        `json:"repo"`
	Landing     string        `json:"landing"` // its first 12 hex digits
	Time        time.Time     `json:"time"`
	Merge       bool          `json:"merge,omitzero"`
	Title       string        `json:"title,omitempty"` // only with --allow-source
	People      []personBrief `json:"people"`
	PeopleTotal int           `json:"people_total"`
	MergedBy    string        `json:"merged_by,omitempty"` // name
	Reverts     []string      `json:"reverts,omitzero"`
	RevertedBy  []string      `json:"reverted_by,omitzero"`
}

// changeDetail is one landing in a detailed list: the use case's whole,
// with its people cut to the answer's rows.
type changeDetail struct {
	app.Change
	PeopleTotal int `json:"people_total"`
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
	// Absent outside its 90-day window, and when every commit of theirs in
	// the change is a revert or a commit a revert names.
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

func (s *shaper) changesAnswer(a *app.ChangesAnswer, sc scopeOut, person string) *changesOut {
	f := s.f
	out := &changesOut{Scope: sc, Window: a.Window, Person: person, tail: tail{Caveats: a.Caveats}}
	changes, pg := paginate(a.Changes, f, a.Provenance.RunID)
	out.Page = pg
	n := newNamed()
	for _, c := range changes {
		n.key(c.RepoKey)
	}
	out.head = head{Provenance: f.provenance(a.Provenance, n)}
	if f.detailed {
		detail := []changeDetail{}
		for _, c := range changes {
			d := changeDetail{Change: c}
			d.People, d.PeopleTotal = cut(c.People, f.rows)
			detail = append(detail, d)
		}
		out.Changes = detail
		return out
	}
	brief := []changeBrief{}
	for _, c := range changes {
		b := changeBrief{Repo: c.Repo, Landing: shortSHA(c.Landing), Time: c.Time, Merge: c.Merge, Title: c.Title,
			Reverts: shortSHAs(c.Reverts), RevertedBy: shortSHAs(c.RevertedBy)}
		if c.MergedBy != nil {
			b.MergedBy = c.MergedBy.Name
		}
		people, total := cut(c.People, f.rows)
		b.PeopleTotal = total
		for _, p := range people {
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
	Metric  any            `json:"metric"` // concise: metricBrief; detailed: report.MetricInfo
	Subject report.Subject `json:"subject"`
	Values  []valueOut     `json:"values"`
	// ValuesTotal counts the subject's values of the metric: values holds
	// those about the whole run and, per kind of scope, the largest groups'
	// and repositories'.
	ValuesTotal int             `json:"values_total"`
	Changes     []report.Change `json:"changes"` // a ranked metric's counted changes for a person, newest last
	// Landings, Commits and Files are what a person's value of a metric
	// that is not ranked is made of (#109), paged as changes are: the
	// landings landed@1 and landed@2 count, the blamed commits surviving@1
	// sums, the files majority_owner@1 counts. Absent for other metrics.
	Landings []report.ExplainLanding `json:"landings,omitempty"`
	Commits  []report.ExplainCommit  `json:"commits,omitempty"`
	Files    []report.ExplainFile    `json:"files,omitempty"`
	// Coverage is what review_coverage@1's value about a repository, a
	// group or the whole run is made of: each change of its window with
	// its outcome, the unreviewed in Den first, paged as changes are.
	// Absent for other metrics.
	Coverage []report.CoverageChange `json:"coverage,omitempty"`
	tail
}

// metricBrief is a metric's definition in a concise answer.
type metricBrief struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	Unit        string `json:"unit,omitempty"`
	Ranked      bool   `json:"ranked"`
	Ratio       bool   `json:"ratio,omitzero"` // its values are shares, num of den; absent when false
	Eligibility string `json:"eligibility,omitempty"`
}

func briefOf(m report.MetricInfo, unit bool) metricBrief {
	b := metricBrief{ID: m.ID, Question: m.Question, Ranked: m.Ranked, Ratio: m.Ratio}
	if unit {
		b.Unit, b.Eligibility = m.Unit, m.Eligibility
	}
	return b
}

func (s *shaper) explainAnswer(e *report.Explain, allowSource bool) *explainOut {
	f := s.f
	out := &explainOut{Subject: e.Subject, tail: tail{Caveats: e.Caveats}}
	n := newNamed()
	out.Values, out.ValuesTotal = s.values(e.Values, true, n)
	if f.detailed {
		out.Metric = e.Metric
	} else {
		out.Metric = briefOf(e.Metric, true)
	}
	// One list is filled for any metric, so one page and cursor serve it.
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
		n.key(c.RepoKey)
	}
	landings, pg := paginate(e.Landings, f, e.Provenance.RunID)
	if pg.Total > 0 {
		out.Page = pg
	}
	for _, l := range landings {
		if !allowSource {
			l.Title = ""
		}
		if !f.detailed {
			l.Landing = shortSHA(l.Landing)
		}
		out.Landings = append(out.Landings, l)
		n.key(l.RepoKey)
	}
	commits, pg := paginate(e.Commits, f, e.Provenance.RunID)
	if pg.Total > 0 {
		out.Page = pg
	}
	for _, c := range commits {
		if !allowSource {
			c.Title = ""
		}
		if !f.detailed {
			c.Commit = shortSHA(c.Commit)
		}
		out.Commits = append(out.Commits, c)
		n.key(c.RepoKey)
	}
	files, pg := paginate(e.Files, f, e.Provenance.RunID)
	if pg.Total > 0 {
		out.Page = pg
	}
	for _, x := range files {
		out.Files = append(out.Files, x)
		n.key(x.RepoKey)
	}
	coverage, pg := paginate(e.Coverage, f, e.Provenance.RunID)
	if pg.Total > 0 {
		out.Page = pg
	}
	for _, c := range coverage {
		if !allowSource {
			c.Title = "" // a commit subject or a pull request's title is source: local output only
		}
		if !f.detailed {
			c.Landing = shortSHA(c.Landing)
		}
		out.Coverage = append(out.Coverage, c) // the author's key leaves as an id, as every person subject's (privacy.go)
		n.key(c.RepoKey)
	}
	out.head = head{Provenance: f.provenance(e.Provenance, n)}
	out.Caveats = s.withheldAI(out.Caveats)
	return out
}

// The highlights tool.

type highlightsOut struct {
	head
	Scope  scopeOut        `json:"scope"`
	Period report.Interval `json:"period"`
	// TopTier holds the first rows of the tables' top tiers; TopTierTotal
	// counts them all, one per ranked metric and rank scope.
	TopTier      []app.TopTier         `json:"top_tier"`
	TopTierTotal int                   `json:"top_tier_total"`
	Lasted       []app.HighlightChange `json:"lasted"`
	Removed      []app.HighlightChange `json:"removed"`
	tail
}

func (s *shaper) highlightsAnswer(h *app.HighlightsAnswer, sc scopeOut) *highlightsOut {
	f := s.f
	out := &highlightsOut{Scope: sc, Period: h.Period, tail: tail{Caveats: h.Caveats}}
	n := newNamed()
	shorten := func(cs []app.HighlightChange) []app.HighlightChange {
		var o []app.HighlightChange
		for _, c := range cs {
			if !f.detailed {
				c.Landing = shortSHA(c.Landing)
			}
			o = append(o, c)
			n.key(c.RepoKey)
		}
		return o
	}
	out.Lasted, out.Removed = shorten(h.Lasted), shorten(h.Removed)
	tiers, total := cut(h.TopTier, f.rows)
	out.TopTierTotal = total
	for _, t := range tiers {
		t.People, _ = cut(t.People, f.rows)
		out.TopTier = append(out.TopTier, t)
	}
	out.head = head{Provenance: f.provenance(h.Provenance, n)}
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

func (s *shaper) diffAnswer(d *app.DiffAnswer) *diffOut {
	f := s.f
	n := newNamed()
	n.key(d.RepoKey)
	out := &diffOut{head: head{Provenance: f.provenance(d.Provenance, n)}, Repo: d.Repo, RepoKey: d.RepoKey,
		Landing: d.Landing, Time: d.Time, Merge: d.Merge, Title: d.Title, Base: d.Base, Path: d.Path, Patch: d.Patch,
		Truncated: d.Truncated, tail: tail{Caveats: d.Caveats}}
	out.Files, out.FilesTotal = cut(d.Files, f.limit)
	if f.patch > 0 && len(out.Patch) > f.patch {
		// The patch's JSON escapes made the answer too large: cut it at
		// the last whole line inside the room the budget leaves.
		p := out.Patch[:f.patch]
		if i := strings.LastIndexByte(p, '\n'); i >= 0 {
			p = p[:i+1]
		}
		out.Patch, out.Truncated = p, true
		out.Caveats = append(slices.Clip(out.Caveats), report.Caveat{Code: "truncated", Message: fmt.Sprintf(
			"The patch was cut at %d bytes so the answer fits: ask for a path under the change to see the rest of it.", len(p))})
	}
	if n := s.w.count(out.Patch); n > 0 {
		// The egress filter will replace them, as in every string: say so
		// where the code is, so an id is not read as the code's own (MC-18).
		out.Caveats = append(slices.Clip(out.Caveats), report.Caveat{Code: "patch-emails", Message: fmt.Sprintf(
			"%d email address(es) in the patch are replaced by ids (p-…), as everywhere without --allow-emails: the code "+
				"shown differs from the repository's there, and an id in a patch, such as a test's made-up address, is not "+
				"necessarily anyone in the run.", n)})
	}
	return out
}

// The identity_candidates tool serves report.Identities as #26 builds it,
// its candidates paged as every list tool pages its main list (#58): a
// repository of 12,000 commits holds some 80 candidates, about 75,000
// characters whole, which no single answer may carry. Concise rows say who
// may be one person, how sure the rules are, and the lines to apply;
// detailed adds each rule's evidence (links, hand-offs) and every
// address's repositories, up to the answer's rows.
type identitiesOut struct {
	head
	Scope      report.Scope `json:"scope"`
	Addresses  int          `json:"addresses"`
	Bots       int          `json:"bots"`
	Shared     int          `json:"shared"`
	Oversized  int          `json:"oversized"`
	Candidates any          `json:"candidates"` // concise: []identityBrief; detailed: []identityDetail
	// Guesses is how many candidates of the whole list (page.total) are
	// hand-off guesses, which come last (report.Identities.Guesses).
	Guesses int `json:"guesses"`
	// Rules is every rule's definition, how it links and how it can be
	// wrong: bounded by the rules, not the run.
	Rules []report.IdentityRule `json:"rules"`
	tail
}

// identityBrief is one candidate in a concise answer: report's candidate
// without its links, whose evidence detailed carries.
type identityBrief struct {
	Confidence string            `json:"confidence"`
	Guess      bool              `json:"guess"`
	Commented  bool              `json:"commented"`
	Chained    bool              `json:"chained"`
	Rules      []string          `json:"rules"`
	Name       string            `json:"name"`
	Addresses  []identityAddress `json:"addresses"`
	Mailmap    *[]string         `json:"mailmap"` // null unless emails may leave (--allow-emails)
}

// identityDetail is one candidate in a detailed answer: the report's whole,
// its addresses' repositories cut to the answer's rows with their totals.
type identityDetail struct {
	report.IdentityCandidate
	Addresses []identityAddress `json:"addresses"` // in place of the report's
}

// identityAddress is one address of a candidate, its repositories cut to
// the answer's rows.
type identityAddress struct {
	report.IdentityAddress
	ReposTotal int `json:"repos_total"`
}

func (s *shaper) identitiesAnswer(ids *report.Identities) *identitiesOut {
	f := s.f
	out := &identitiesOut{Scope: ids.Scope, Addresses: ids.Addresses, Bots: ids.Bots, Shared: ids.Shared,
		Oversized: ids.Oversized, Guesses: ids.Guesses, Rules: ids.Rules, tail: tail{Caveats: ids.Caveats}}
	cands, pg := paginate(ids.Candidates, f, ids.Provenance.RunID)
	out.Page = pg
	n := newNamed()
	addresses := func(as []report.IdentityAddress) []identityAddress {
		var o []identityAddress
		for _, a := range as {
			ia := identityAddress{IdentityAddress: a}
			ia.Repos, ia.ReposTotal = cut(a.Repos, f.rows)
			for _, k := range ia.Repos {
				n.key(k)
			}
			o = append(o, ia)
		}
		return o
	}
	if f.detailed {
		detail := []identityDetail{}
		for _, c := range cands {
			detail = append(detail, identityDetail{IdentityCandidate: c, Addresses: addresses(c.Addresses)})
		}
		out.Candidates = detail
	} else {
		brief := []identityBrief{}
		for _, c := range cands {
			brief = append(brief, identityBrief{Confidence: c.Confidence, Guess: c.Guess, Commented: c.Commented,
				Chained: c.Chained, Rules: c.Rules, Name: c.Name, Addresses: addresses(c.Addresses), Mailmap: c.Mailmap})
		}
		out.Candidates = brief
	}
	out.head = head{Provenance: f.provenance(ids.Provenance, n)}
	return out
}
