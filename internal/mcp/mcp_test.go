package mcp

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// TestMCPTools asks every tool what an agent would, through the SDK's own
// client over its in-memory transport, of a facts.db that holds one scan
// of both fixtures as one platform (testdata/fixture/SPEC.md), and then a
// second scan that adds a repository whose people have emails of every odd
// shape. It checks the oracle numbers an answer shows, that names resolve
// as the reports print them and an ambiguous one lists its candidates,
// paging, cursors and the size budget, and the privacy rules: no email of
// the run without --allow-emails, whatever its shape, no commit subject or
// diff without --allow-source, the AI-assisted share only on the --me
// person's own page and never for a repository of fewer than three people.
// Every answer must fit Claude Code's 25,000-token cap at 4 characters a
// token.
func TestMCPTools(t *testing.T) {
	ctx := t.Context()
	db, git, platform, mailmap := scanPlatform(t)
	// Every email of every run in facts.db: no answer may hold one.
	emails := runEmails(t, db, 1)
	leak := func(text string) string {
		lower := strings.ToLower(text)
		for _, e := range emails {
			if strings.Contains(lower, e) {
				return e
			}
		}
		return ""
	}
	// The default server: no emails, no source, and the user is Lena.
	cs := connect(t, Config{Open: openDB(db), Me: "lena@example.com", Version: "test"}, 0)
	ask := func(t *testing.T, tool string, args map[string]any) string {
		t.Helper()
		text, isErr := callTool(t, cs, tool, args)
		if isErr {
			t.Fatalf("%s(%v) failed: %s", tool, args, text)
		}
		if e := leak(text); e != "" {
			t.Errorf("%s(%v) holds %q without --allow-emails: %s", tool, args, e, text)
		}
		return text
	}
	refused := func(t *testing.T, tool string, args map[string]any) string {
		t.Helper()
		text, isErr := callTool(t, cs, tool, args)
		if !isErr {
			t.Fatalf("%s(%v) answered, want an error: %s", tool, args, text)
		}
		if e := leak(text); e != "" {
			t.Errorf("%s(%v)'s error holds %q: %s", tool, args, e, text)
		}
		return text
	}
	var runOneCursor string // a cursor of run 1, which the second run must refuse

	t.Run("tools", func(t *testing.T) {
		res, err := cs.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
			if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
				t.Errorf("%s is not marked read-only", tl.Name)
			}
			if !strings.Contains(tl.Description, "untrusted") && tl.Name != "metrics" {
				t.Errorf("%s's description does not say repository text is untrusted", tl.Name)
			}
		}
		slices.Sort(names)
		want := []string{"changes", "diff", "explain", "highlights", "identity_candidates", "metrics", "overview", "person", "who_knows"}
		if !slices.Equal(names, want) {
			t.Errorf("tools %v, want %v", names, want)
		}
	})

	t.Run("overview", func(t *testing.T) {
		// The whole run is an index: its groups, then its repositories, as
		// one paged list.
		var run struct {
			Overviews []struct {
				Scope    report.Scope  `json:"scope"`
				ScopeArg string        `json:"scope_arg"`
				Totals   report.Totals `json:"totals"`
			} `json:"overviews"`
			Repos []struct {
				Name     string `json:"name"`
				ScopeArg string `json:"scope_arg"`
			} `json:"repos"`
			Page     page     `json:"page"`
			Withheld []string `json:"withheld"`
		}
		decode(t, ask(t, "overview", map[string]any{"limit": 2}), &run)
		if len(run.Overviews) != 1 || run.Overviews[0].Scope != (report.Scope{Kind: "group", Key: "platform"}) ||
			run.Overviews[0].Totals.Repos != 2 {
			t.Errorf("overviews %+v, want the platform group with its 2 repositories", run.Overviews)
		}
		if len(run.Repos) != 1 || run.Repos[0].Name != "fixture" || run.Page.Total != 3 || run.Page.NextCursor == nil {
			t.Fatalf("first page %+v of %+v, want the group, fixture and a cursor to the third of 3", run.Repos, run.Page)
		}
		// Each scope of the index carries the exact string that names it
		// (MC-4), which the next call can pass as it is.
		if run.Overviews[0].ScopeArg != "group:platform" || run.Repos[0].ScopeArg != "fixture" {
			t.Errorf("the index's scope_arg: group %q, fixture %q; want group:platform and fixture",
				run.Overviews[0].ScopeArg, run.Repos[0].ScopeArg)
		}
		ask(t, "overview", map[string]any{"scope": run.Overviews[0].ScopeArg})
		// So does explain's group subject, which takes the group's scope_arg
		// as overview gives it.
		ask(t, "explain", map[string]any{"metric": "knowledge_loss", "subject": run.Overviews[0].ScopeArg, "subject_kind": "group"})
		if !slices.Equal(run.Withheld, []string{"emails", "source"}) {
			t.Errorf("withheld %v, want emails and source", run.Withheld)
		}
		runOneCursor = *run.Page.NextCursor
		run.Overviews, run.Repos = nil, nil
		decode(t, ask(t, "overview", map[string]any{"limit": 2, "cursor": runOneCursor}), &run)
		if len(run.Overviews) != 0 || len(run.Repos) != 1 || run.Repos[0].Name != "sibling" || run.Page.NextCursor != nil {
			t.Errorf("second page %+v %+v of %+v, want sibling alone and no cursor", run.Overviews, run.Repos, run.Page)
		}

		// The main fixture's card and still-here table (SPEC: still-here
		// oracle, knowledge_loss@1).
		var repo struct {
			head
			Repos      []report.Repo  `json:"repos"`
			Areas      *[]report.Area `json:"areas"`
			AreasTotal *int           `json:"areas_total"`
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "fixture"}), &repo)
		if len(repo.Repos) != 1 || repo.Repos[0].StillHere == nil {
			t.Fatalf("overview(fixture) gave %d repositories, want the fixture's card and tables", len(repo.Repos))
		}
		// The fixture's prod lines are all at its root, which is no area
		// (SPEC, knowledge_loss@1): a repository answer lists its areas,
		// here none (#106).
		if repo.Areas == nil || len(*repo.Areas) != 0 || repo.AreasTotal == nil || *repo.AreasTotal != 0 {
			t.Errorf("overview(fixture) areas %v of %v, want [] of 0", repo.Areas, repo.AreasTotal)
		}
		r := repo.Repos[0]
		got := map[string][2]int{}
		for _, row := range append(r.StillHere.Humans, r.StillHere.Bots...) {
			got[row.Name] = [2]int{row.ProdNonblankLines, row.MajorityFiles}
		}
		want := map[string][2]int{
			"Ada Example": {10, 3}, "Cara Dev": {9, 2}, "Bob Builder": {7, 1}, "Frank & Sons": {2, 1}, "Gina Split": {2, 1},
			"Hank Split": {2, 1}, "Dana Dev": {1, 1}, "Erin Editor": {1, 0}, "GitHub Copilot": {3, 1},
		}
		if !maps.Equal(got, want) {
			t.Errorf("fixture still-here rows (lines, majority files) %v, want %v", got, want)
		}
		if len(r.StillHere.Bots) != 1 || r.StillHere.Bots[0].Name != "GitHub Copilot" {
			t.Errorf("fixture bots %+v, want GitHub Copilot alone", r.StillHere.Bots)
		}
		if c := r.Card.Concentration; c == nil || c.FilesWithMajority != 10 || c.AuthorsInPrefix != 2 || c.BotsInPrefix != 0 ||
			c.Label != report.ConcentrationLabel {
			t.Errorf("fixture concentration %+v, want 10 files, 2 authors, 0 bots, not a truck factor", c)
		}
		if l := r.Card.KnowledgeLoss; l == nil || l.Num != 4 || l.Den != 34 {
			t.Errorf("fixture knowledge_loss %+v, want 4 of 34", l)
		}
		pv := repo.Provenance
		if pv.RunID != 1 || len(pv.Heads) != 1 || len(pv.Heads[0].SHA) != 40 || pv.HeadsTotal != 1 || len(pv.HeadsSHA256) != 64 ||
			!slices.Contains(pv.Metrics, "surviving@1") {
			t.Errorf("fixture provenance %+v, want run 1, its HEAD, the digest and the metric versions", pv)
		}
		// metrics names every number's metric: the week table's landed@2
		// beside the card's, and none the scan ranked, since no rank table
		// was asked for.
		if !slices.Contains(pv.Metrics, "landed@2") || slices.Contains(pv.Metrics, "durable@1") {
			t.Errorf("fixture provenance metrics %v, want landed@2 and no durable@1", pv.Metrics)
		}

		// The sibling's surviving@1 credit, and the platform's durable@1
		// table, where Lena alone is eligible at exactly 10 clusters.
		decode(t, ask(t, "overview", map[string]any{"scope": "repo:sibling"}), &repo)
		got = map[string][2]int{}
		for _, row := range repo.Repos[0].StillHere.Humans {
			got[row.Name] = [2]int{row.ProdNonblankLines}
		}
		if want := map[string][2]int{"Lena Lead": {37}, "Omar Ops": {36}, "Quinn Squash": {31}, "Pia Pair": {9}, "Uma Legacy": {7}}; !maps.Equal(got, want) {
			t.Errorf("sibling surviving@1 %v, want %v", got, want)
		}
		if b := repo.Repos[0].Card.BotShare; b == nil || b.Num != 0 || b.Den != 120 {
			t.Errorf("sibling bot share %+v, want 0 of 120", b)
		}
		var ranks struct {
			Ranks []rankTableOut `json:"ranks"`
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "platform", "rank": "durable"}), &ranks)
		if len(ranks.Ranks) != 1 || len(ranks.Ranks[0].Ranked) != 1 {
			t.Fatalf("platform durable ranks %+v, want one table with one ranked person", ranks.Ranks)
		}
		lena := ranks.Ranks[0].Ranked[0]
		if lena.Name != "Lena Lead" || lena.RankLo == nil || *lena.RankLo != 1 || *lena.RankHi != 1 || lena.N != 10 || !near(lena.Estimate, 26.0/30) {
			t.Errorf("platform durable@1 rank %+v, want Lena Lead 1-1, 26/30, n 10", lena)
		}
		if lena.Email != pseudonym("lena@example.com") {
			t.Errorf("Lena's key %q, want her id in place of her email", lena.Email)
		}

		text := refused(t, "overview", map[string]any{"scope": "nowhere"})
		if !strings.Contains(text, `"platform"`) || !strings.Contains(text, `"sibling"`) {
			t.Errorf("an unknown scope's error does not list the run's scopes, quoted: %s", text)
		}
		// An argument the tool's schema refuses is refused with what to
		// pass, not the schema validator's words (MC-9).
		for _, c := range []struct {
			tool string
			args map[string]any
			want string
		}{
			{"overview", map[string]any{"limit": 1000}, "limit is a whole number from 1 to 100, not 1000; pass limit: 100"},
			{"overview", map[string]any{"bogus": 1}, `has no argument "bogus": it takes cursor, limit, rank, response_format, scope`},
			{"explain", map[string]any{"metric": "durable", "subject_kind": "bogus"}, `"bogus" is not one of person, repo, group, area`},
			{"explain", map[string]any{}, "explain needs metric: A metric id"},
		} {
			if text := refused(t, c.tool, c.args); !strings.Contains(text, c.want) || strings.Contains(text, "validating") {
				t.Errorf("%s(%v) was refused with %q, want %q", c.tool, c.args, text, c.want)
			}
		}
		// A hint in a use case's error names this server's tools, never a
		// command the agent cannot run (#69).
		text = refused(t, "overview", map[string]any{"scope": "platform", "rank": "landed"})
		if !strings.Contains(text, "(metrics() lists them)") || strings.Contains(text, "stillhere metrics") {
			t.Errorf("overview(rank: landed) was refused with %q, want the metrics tool named, not the command", text)
		}
	})

	t.Run("person", func(t *testing.T) {
		var p personOut
		decode(t, ask(t, "person", map[string]any{"name": "Lena Lead"}), &p)
		if p.Person.Name != "Lena Lead" || !p.OwnPage || !slices.Equal(p.Person.Repos, []string{"sibling"}) {
			t.Errorf("person %+v own page %v, want Lena Lead's own page, in sibling", p.Person, p.OwnPage)
		}
		var durable *report.MeRank
		for i, r := range p.Ranks {
			if r.Metric == "durable@1" {
				durable = &p.Ranks[i]
			}
		}
		if durable == nil || !durable.Eligible || durable.RankLo == nil || *durable.RankLo != 1 || *durable.RankHi != 1 || durable.N != 10 ||
			!near(durable.Estimate, 26.0/30) {
			t.Errorf("Lena's durable@1 rank %+v, want eligible at 1-1, 26/30, n 10", durable)
		}
		if !slices.ContainsFunc(p.Values, func(v valueOut) bool { return v.Metric == "ai_assisted@1" }) {
			t.Error("Lena's own page lacks her own AI-assisted share")
		}
		// Each caveat goes where its number is (#69): the page shows ranks of
		// unvalidated metrics, and her own AI-assisted share.
		if !hasCaveat(p.Caveats, "validity", "durable@1") || !hasCaveat(p.Caveats, "validity", "simplifying@1") ||
			!hasCaveat(p.Caveats, "ai-lower-bound", "") {
			t.Errorf("Lena's own page carries %v, want the validity of durable@1 and simplifying@1, and ai-lower-bound", p.Caveats)
		}
		// Someone else's answer never holds their share (ADR 0007), and a
		// first name is enough when it is unique.
		decode(t, ask(t, "person", map[string]any{"name": "omar", "response_format": "detailed"}), &p)
		if p.Person.Name != "Omar Ops" || p.OwnPage || !slices.ContainsFunc(p.Caveats, func(c report.Caveat) bool { return c.Code == "name-match" }) {
			t.Errorf("person(omar) %+v own page %v caveats %v, want Omar Ops matched by name", p.Person, p.OwnPage, p.Caveats)
		}
		if slices.ContainsFunc(p.Values, func(v valueOut) bool { return v.Metric == "ai_assisted@1" }) {
			t.Error("Omar's answer holds his AI-assisted share, which only his own page may show")
		}
		if !hasCaveat(p.Caveats, "validity", "simplifying@1") || hasCaveat(p.Caveats, "ai-lower-bound", "") {
			t.Errorf("Omar's page carries %v, want simplifying@1's validity and no ai-lower-bound, as it shows no share", p.Caveats)
		}
		// Names from the repositories are quoted in the server's sentences.
		if text := refused(t, "person", map[string]any{"name": "Split"}); !strings.Contains(text, `"Gina Split" (id p-`) ||
			!strings.Contains(text, `"Hank Split" (id p-`) {
			t.Errorf("an ambiguous name's error does not list both candidates, quoted, with their ids: %s", text)
		}
		if text := refused(t, "person", map[string]any{"name": "Lenna Lead"}); !strings.Contains(text, `"Lena Lead"`) {
			t.Errorf("a misspelt name's error does not suggest Lena Lead: %s", text)
		}
	})

	// A lead's view of a person and the person's own page differ only by
	// the person's own AI-assisted share: app.Person and app.Me must not
	// drift apart (provenance, caveats, ranks and every other value).
	t.Run("person and own page", func(t *testing.T) {
		lead := connect(t, Config{Open: openDB(db), Version: "test"}, 0)
		args := map[string]any{"name": "Lena Lead", "response_format": "detailed"}
		var own, theirs personOut
		decode(t, ask(t, "person", args), &own)
		text, isErr := callTool(t, lead, "person", args)
		if isErr {
			t.Fatalf("person(Lena Lead) without --me failed: %s", text)
		}
		decode(t, text, &theirs)
		notAI := func(v valueOut) bool { return v.Metric != "ai_assisted@1" }
		own.Values = slices.DeleteFunc(own.Values, func(v valueOut) bool { return !notAI(v) })
		own.Provenance.Metrics = slices.DeleteFunc(own.Provenance.Metrics, func(m string) bool { return m == "ai_assisted@1" })
		// The share's own caveat goes with it.
		own.Caveats = slices.DeleteFunc(own.Caveats, func(c report.Caveat) bool { return c.Code == "ai-lower-bound" })
		own.OwnPage, own.ValuesTotal, theirs.ValuesTotal = false, 0, 0
		if !reflect.DeepEqual(own, theirs) {
			t.Errorf("Lena's own page, without her AI-assisted share,\n%+v\ndiffers from a lead's view of her\n%+v", own, theirs)
		}
	})

	t.Run("who_knows", func(t *testing.T) {
		var w whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": "service.py", "repo": "fixture"}), &w)
		if len(w.Repos) != 1 {
			t.Fatalf("who_knows(service.py) gave %d repositories, want 1", len(w.Repos))
		}
		r := w.Repos[0]
		if names(r.MajorityOwners) != "Ada Example" || withLines(r.TopAuthors) != "Ada Example 6, Bob Builder 2" ||
			names(r.RecentLanders) != "Bob Builder" {
			t.Errorf("who_knows(service.py): owners %q, top authors %q, recent landers %q; want Ada; Ada 6, Bob 2; Bob",
				names(r.MajorityOwners), withLines(r.TopAuthors), names(r.RecentLanders))
		}
		// An absolute path inside a scanned clone names its repository.
		var abs whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": filepath.Join(platform, "fixture", "service.py")}), &abs)
		if abs.Repo != "fixture" || abs.Path != "service.py" || !reflect.DeepEqual(abs.Repos, w.Repos) {
			t.Errorf("who_knows(absolute service.py) answered %q in %q with %+v, want the fixture's service.py as above",
				abs.Path, abs.Repo, abs.Repos)
		}
		decode(t, ask(t, "who_knows", map[string]any{"path": "split.py"}), &w)
		if len(w.Repos) != 1 || names(w.Repos[0].MajorityOwners) != "Gina Split, Hank Split" || len(w.Repos[0].RecentLanders) != 0 {
			t.Errorf("who_knows(split.py) %+v, want Gina and Hank as owners and no recent lander", w.Repos)
		}
		// A window of the agent's (wave 11, Ruby F10): split.py's January
		// landers are in 300 days, and the answer says its window; one past
		// the run's 365 days of history is refused, not answered as if
		// nothing landed before it.
		var long whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": "split.py", "days": 300}), &long)
		if w.Days != 90 || long.Days != 300 || len(long.Repos) != 1 || names(long.Repos[0].RecentLanders) != "Gina Split, Hank Split" {
			t.Errorf("who_knows(split.py, days 300): days %d (and %d without), %+v; want 300 (90), Gina and Hank as recent landers",
				long.Days, w.Days, long.Repos)
		}
		if text := refused(t, "who_knows", map[string]any{"path": "split.py", "days": 400}); !strings.Contains(text, "365") {
			t.Errorf("who_knows(split.py, days 400) was refused with %q, want the run's 365 days named", text)
		}
	})

	t.Run("changes", func(t *testing.T) {
		var c struct {
			Changes []changeBrief   `json:"changes"`
			Page    page            `json:"page"`
			Caveats []report.Caveat `json:"caveats"`
		}
		decode(t, ask(t, "changes", map[string]any{"scope": "sibling", "since": "2026-09-01"}), &c)
		if len(c.Changes) != 3 {
			t.Fatalf("sibling changes since 2026-09-01: %d, want W9, W8 and S1", len(c.Changes))
		}
		// The two metrics share their beta mark and their reference's
		// source, so one caveat says them once, then each metric's verdict
		// by name, and what a model panel is.
		if !hasCaveat(c.Caveats, "validity", "Validity of durable@1 and simplifying@1: Beta") ||
			!hasCaveat(c.Caveats, "validity", "Reference (model panel, 4 public repositories, 2026-10-15): durable@1 does not separate") ||
			!hasCaveat(c.Caveats, "validity", "; simplifying@1 separates, but no better than change size") ||
			!hasCaveat(c.Caveats, "validity", "The model panel is three AI models that labelled public changes without seeing any metric value") ||
			countCode(c.Caveats, "validity") != 1 {
			t.Errorf("changes carries %v, want one validity caveat for the two metrics whose clusters it shows, with each one's reference", c.Caveats)
		}
		s1 := c.Changes[2]
		if len(s1.People) != 1 || s1.People[0].Name != "Lena Lead" || s1.People[0].Removed == nil ||
			*s1.People[0].Removed != 22 || s1.Title != "" {
			t.Errorf("S1 %+v, want Lena Lead removing 22 lines, without its subject", s1)
		}
		if a := s1.People[0].AIAssisted; a == nil || *a {
			t.Errorf("S1's tag for Lena, the user, is %v, want false", a)
		}
		decode(t, ask(t, "changes", map[string]any{"scope": "fixture", "person": "Bob Builder", "since": "7d"}), &c)
		var got []string
		for _, ch := range c.Changes {
			got = append(got, ch.MergedBy+"|"+ch.People[0].Name)
			if ch.People[0].AIAssisted != nil {
				t.Errorf("%s carries %s's AI-assisted tag, which only that person may see", ch.Landing, ch.People[0].Name)
			}
		}
		if !slices.Equal(got, []string{"|Bob Builder", "Bob Builder|Cara Dev"}) {
			t.Errorf("Bob's changes this week (merged by|person) %v, want delta, then the merge of Cara's feature", got)
		}
		// An empty person is the user the server runs for, Lena, as
		// everywhere a person is named (MC-5); left out, it is everyone.
		var all, mine struct {
			Person  string        `json:"person"`
			Changes []changeBrief `json:"changes"`
		}
		decode(t, ask(t, "changes", map[string]any{"scope": "sibling", "since": "365d", "limit": 100}), &all)
		decode(t, ask(t, "changes", map[string]any{"scope": "sibling", "since": "365d", "limit": 100, "person": ""}), &mine)
		lenas := slices.DeleteFunc(slices.Clone(all.Changes), func(ch changeBrief) bool {
			return ch.MergedBy != "Lena Lead" && !slices.ContainsFunc(ch.People, func(p personBrief) bool { return p.Name == "Lena Lead" })
		})
		if mine.Person != "Lena Lead" || len(mine.Changes) == 0 || len(mine.Changes) != len(lenas) || len(lenas) == len(all.Changes) {
			t.Errorf("changes(person: \"\") gave %d changes for %q, want Lena Lead's %d of the sibling's %d",
				len(mine.Changes), mine.Person, len(lenas), len(all.Changes))
		}
		lead := connect(t, Config{Open: openDB(db), Version: "test"}, 0)
		if text, isErr := callTool(t, lead, "changes", map[string]any{"person": ""}); !isErr || !strings.Contains(text, "leave person out") {
			t.Errorf("changes(person: \"\") without --me answered %q, want to leave person out", text)
		}
	})

	t.Run("explain", func(t *testing.T) {
		var e explainOut
		decode(t, ask(t, "explain", map[string]any{"metric": "durable", "subject": "Lena Lead"}), &e)
		if len(e.Values) == 0 || len(e.Changes) != 10 {
			t.Fatalf("explain(durable, Lena) gave %d values and %d changes, want her 10 clusters", len(e.Values), len(e.Changes))
		}
		for _, v := range e.Values {
			if v.Num != 26 || v.Den != 30 || v.N != 10 {
				t.Errorf("Lena's durable@1 in %v: %v of %v, n %d; want 26 of 30, n 10", v.Scope, v.Num, v.Den, v.N)
			}
		}
		for _, c := range e.Changes {
			if c.Title != "" {
				t.Errorf("explain shows the subject of %s without --allow-source", c.Landing)
			}
		}
		// What a value that is not ranked is made of (#109): Bob's one
		// landing of the week that holds a commit of his, delta; the merge
		// of Cara's branch, his only by its merge commit, holds none.
		var l explainOut
		decode(t, ask(t, "explain", map[string]any{"metric": "landed", "subject": "Bob Builder"}), &l)
		if len(l.Landings) != 1 || l.Landings[0].Commits != 1 || l.Landings[0].Title != "" || l.Landings[0].Merge {
			t.Errorf("explain(landed, Bob) lists the landings %+v, want delta's alone, 1 commit, with no subject", l.Landings)
		}
		refused(t, "explain", map[string]any{"metric": "ai_assisted", "subject": "Omar Ops"})
		decode(t, ask(t, "explain", map[string]any{"metric": "knowledge_loss", "subject": "fixture", "subject_kind": "repo"}), &e)
		if len(e.Values) != 1 || e.Values[0].Num != 4 || e.Values[0].Den != 34 {
			t.Errorf("explain(knowledge_loss, fixture) %+v, want 4 of 34", e.Values)
		}
		// A repository of many people shows its AI-assisted share, a lower
		// bound.
		decode(t, ask(t, "explain", map[string]any{"metric": "ai_assisted", "subject": "sibling", "subject_kind": "repo"}), &e)
		if len(e.Values) != 1 || !hasCaveat(e.Caveats, "ai-lower-bound", "") {
			t.Errorf("explain(ai_assisted, sibling) %+v with caveats %v, want the sibling's share, a lower bound", e.Values, e.Caveats)
		}
		// The share leaves without its detail, the cohort comparisons the
		// metric computes beside it, which can be one or two people's (#87),
		// and says so. The sibling's stored value holds all six, so nothing
		// here passes because the metric left them out.
		st, err := sqlite.OpenReadOnly(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		le, err := app.Explain(ctx, app.Deps{Store: st}, app.ExplainRequest{Metric: domain.MetricID{Name: "ai_assisted", Version: 1},
			Subject: domain.Subject{Kind: domain.SubjectRepo, Key: e.Values[0].Scope.Key}})
		if cerr := st.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"durable_ai", "durable_other", "durable_ai_n", "durable_other_n", "reverted_ai", "reverted_other"} {
			if _, ok := le.Values[0].Detail[k]; !ok {
				t.Errorf("the sibling's stored ai_assisted@1 detail %v lacks %s", le.Values[0].Detail, k)
			}
		}
		for _, v := range e.Values {
			if len(v.Detail) != 0 {
				t.Errorf("explain(ai_assisted, sibling) sends the share's detail %v, want none", v.Detail)
			}
		}
		if !hasCaveat(e.Caveats, "ai-detail-withheld", "cohort comparisons") {
			t.Errorf("explain(ai_assisted, sibling) caveats %v, want ai-detail-withheld", e.Caveats)
		}
		// A metric about a kind of subject it holds no value about is no
		// empty answer but a refusal that says which calls answer instead,
		// as this server's tools (#69).
		text := refused(t, "explain", map[string]any{"metric": "durable", "subject": "fixture", "subject_kind": "repo"})
		for _, want := range []string{"durable@1 holds no value about a repository", "its values are about people",
			"explain(metric: durable@1, subject_kind: person)", "overview(scope, rank: durable@1)"} {
			if !strings.Contains(text, want) || strings.Contains(text, "stillhere") {
				t.Errorf("explain(durable, fixture as a repository) was refused with %q, want %q and no command", text, want)
			}
		}
		// The kinds are the metric's own (metrics.Definition.Subjects), not
		// what the run holds: this run read no GitHub data, so it has no
		// value of reviews_given@1 at all, and the question is refused all
		// the same. who_knows@1, which no scan stores, is sent to its tool.
		for _, c := range []struct {
			args map[string]any
			want string
		}{
			{map[string]any{"metric": "reviews_given", "subject": "fixture", "subject_kind": "repo"},
				"overview(scope, rank: reviews_given@1)"},
			{map[string]any{"metric": "who_knows", "subject": "platform", "subject_kind": "group"}, "who_knows(path, repo)"},
		} {
			if text := refused(t, "explain", c.args); !strings.Contains(text, c.want) || strings.Contains(text, "stillhere") {
				t.Errorf("explain(%v) was refused with %q, want %q and no command", c.args, text, c.want)
			}
		}
		if text := refused(t, "explain", map[string]any{"metric": "durable@2", "subject": "Lena Lead"}); !strings.Contains(text, "(metrics() describes each)") ||
			strings.Contains(text, "stillhere metrics") {
			t.Errorf("explain(durable@2) was refused with %q, want the metrics tool named, not the command", text)
		}
		// Wave 10's dead ends: days on a metric with a window of its own is
		// refused in words that name no command-line flag; a window past the
		// 365 days the run's history covers is refused by the schema, not
		// answered as if nothing landed then; an area that holds nothing
		// points to who_knows; and the whole run's own numbers (subject_kind
		// all) are explained like a repository's.
		if text := refused(t, "explain", map[string]any{"metric": "knowledge_loss", "subject": "fixture", "subject_kind": "repo",
			"days": 30}); !strings.Contains(text, "a window in days is landed@1's and landed@2's alone") || strings.Contains(text, "--") {
			t.Errorf("explain(knowledge_loss, days 30) was refused with %q, want the window explained and no flag named", text)
		}
		if text := refused(t, "explain", map[string]any{"metric": "landed", "subject": "Bob Builder", "days": 400}); !strings.Contains(text, "days") ||
			!strings.Contains(text, "365") {
			t.Errorf("explain(landed, Bob, days 400) was refused with %q, want days and its maximum, 365, named", text)
		}
		if text := refused(t, "explain", map[string]any{"metric": "knowledge_loss", "subject": "fixture", "subject_kind": "area",
			"path": "nosuchdir"}); !strings.Contains(text, "who_knows(path, repo) answers any path") || !strings.Contains(text, `"fixture"`) ||
			strings.Contains(text, "stillhere") || strings.Contains(text, "git.example") {
			t.Errorf("explain(knowledge_loss, area fixture:nosuchdir) was refused with %q, want the repository by name and who_knows", text)
		}
		var all explainOut
		decode(t, ask(t, "explain", map[string]any{"metric": "landed", "subject_kind": "all"}), &all)
		if len(all.Values) == 0 || all.Subject.Kind != domain.SubjectAll || len(all.Landings) == 0 {
			t.Errorf("explain(landed, all) gave subject %+v, %d values and %d landings, want the whole run's totals and their landings",
				all.Subject, len(all.Values), len(all.Landings))
		}
		refused(t, "explain", map[string]any{"metric": "landed", "subject": "fixture", "subject_kind": "all"})
	})

	t.Run("highlights", func(t *testing.T) {
		var h highlightsOut
		decode(t, ask(t, "highlights", map[string]any{"period": "quarter"}), &h)
		// Lena is the only eligible person in both tables, so first place
		// singles no one out: each tier is counted, not listed.
		tiers := map[string]string{}
		for _, tt := range h.TopTier {
			tiers[tt.Metric] = strconv.Itoa(tt.PeopleTotal) + " of " + strconv.Itoa(tt.Of) + ", " + strconv.Itoa(len(tt.People)) + " listed"
		}
		if want := map[string]string{"durable@1": "1 of 1, 0 listed", "simplifying@1": "1 of 1, 0 listed"}; !maps.Equal(tiers, want) {
			t.Errorf("top tiers %v, want %v", tiers, want)
		}
		if !slices.ContainsFunc(h.Caveats, func(c report.Caveat) bool { return c.Code == "no-standout" }) {
			t.Errorf("highlights caveats %v, want no-standout for tiers of everyone eligible", h.Caveats)
		}
		if !hasCaveat(h.Caveats, "validity", "Validity of durable@1 and simplifying@1: Beta") || countCode(h.Caveats, "validity") != 1 {
			t.Errorf("highlights caveats %v, want one validity caveat for the ranked metrics it picks from", h.Caveats)
		}
		if len(h.Removed) < 2 || h.Removed[0].Person.Name != "Lena Lead" || h.Removed[0].Num != 22 ||
			h.Removed[1].Person.Name != "Omar Ops" || h.Removed[1].Num != 21 {
			t.Errorf("largest removals %+v, want S1 (Lena, 22) then S2 (Omar, 21)", h.Removed)
		}
		if len(h.Lasted) == 0 || h.Lasted[0].Person.Name != "Ada Example" || h.Lasted[0].Num != 7 || h.Lasted[0].Den != 7 {
			t.Errorf("lasted %+v, want Ada's init first, 7 of 7 lines kept", h.Lasted)
		}
	})

	t.Run("identity_candidates", func(t *testing.T) {
		text, isErr := callTool(t, cs, "identity_candidates", nil)
		if isErr {
			t.Fatalf("identity_candidates failed: %s", text)
		}
		if e := leak(text); e != "" {
			t.Errorf("identity_candidates holds %q without --allow-emails: %s", e, text)
		}
		var ids struct {
			head
			Withheld []string `json:"withheld"`
		}
		decode(t, text, &ids)
		if ids.Provenance.RunID != 1 || !slices.Contains(ids.Withheld, "emails") {
			t.Errorf("identity_candidates provenance %+v, withheld %v; want run 1 and emails withheld", ids.Provenance, ids.Withheld)
		}
		// The suggested lines are made of emails: without --allow-emails
		// they are left out (null), not sent with ids in them.
		if strings.Contains(text, `"mailmap":[`) || !strings.Contains(text, `"mailmap":null`) {
			t.Errorf("identity_candidates without --allow-emails carries mailmap lines, or no null for them: %s", text)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		var m struct {
			Provenance registryOut         `json:"provenance"`
			Metrics    []report.MetricInfo `json:"metrics"`
		}
		decode(t, ask(t, "metrics", nil), &m)
		if !slices.Contains(m.Provenance.Metrics, "durable@1") || len(m.Metrics) != len(m.Provenance.Metrics) {
			t.Errorf("metrics %v, want the whole registry", m.Provenance.Metrics)
		}
		decode(t, ask(t, "metrics", map[string]any{"metric": "durable"}), &m)
		if len(m.Metrics) != 1 || m.Metrics[0].ID != "durable@1" || !strings.Contains(m.Metrics[0].Eligibility, "10 clusters") {
			t.Errorf("metrics(durable) %+v, want durable@1's whole definition", m.Metrics)
		}
	})

	t.Run("diff", func(t *testing.T) {
		text := refused(t, "diff", map[string]any{"change": "d656020"})
		if !strings.Contains(text, "--allow-source") {
			t.Errorf("diff's refusal does not say how to allow it: %s", text)
		}
	})

	// An answer over the budget is cut to a smaller page, and the cursor
	// still leads to the rest.
	t.Run("budget", func(t *testing.T) {
		// A budget a hundred characters short of the whole answer: room
		// for one repository's summary, not two.
		whole := ask(t, "overview", map[string]any{"scope": "platform"})
		budget := len(whole) - 100
		small := connect(t, Config{Open: openDB(db), Me: "lena@example.com", Version: "test"}, budget)
		var o struct {
			Repos []repoSummary `json:"repos"`
			Page  page          `json:"page"`
		}
		text, isErr := callTool(t, small, "overview", map[string]any{"scope": "platform", "limit": overviewLimit})
		if isErr || len(text) > budget {
			t.Fatalf("overview(platform) under a %d-character budget: %d characters, error %v: %s", budget, len(text), isErr, text)
		}
		decode(t, text, &o)
		if o.Page.Limit >= overviewLimit || len(o.Repos) != 1 || o.Repos[0].Name != "fixture" || o.Page.NextCursor == nil {
			t.Fatalf("overview(platform) under the budget: %d repositories, page %+v; want fixture alone on a halved page with a cursor",
				len(o.Repos), o.Page)
		}
		// A page cut below the limit the agent asked for says so (wave 10).
		if want := fmt.Sprintf("limit %d, not the %d asked: the answer is cut to fit", o.Page.Limit, overviewLimit); !strings.HasPrefix(o.Page.Note, want) {
			t.Errorf("overview(platform, limit %d) under the budget: page note %q, want it to start %q", overviewLimit, o.Page.Note, want)
		}
		text, _ = callTool(t, small, "overview", map[string]any{"scope": "platform", "cursor": *o.Page.NextCursor})
		decode(t, text, &o)
		if len(o.Repos) != 1 || o.Repos[0].Name != "sibling" || o.Page.NextCursor != nil {
			t.Errorf("the next page under the budget %+v, page %+v; want sibling and the end", o.Repos, o.Page)
		}
	})

	// A server the user lets show emails and source: the diff tool runs
	// git, and subjects and emails reach the agent.
	t.Run("allow source and emails", func(t *testing.T) {
		ss := connect(t, Config{Open: openDB(db), Git: git, AllowSource: true, AllowEmails: true, Version: "test"}, 0)
		var c struct {
			Changes []changeBrief `json:"changes"`
		}
		text, _ := callTool(t, ss, "changes", map[string]any{"scope": "sibling", "since": "2026-09-14"})
		decode(t, text, &c)
		if len(c.Changes) != 3 || c.Changes[2].Title != "refactor: fold legacy_a_1 to legacy_a_22" ||
			c.Changes[2].People[0].Key != "lena@example.com" {
			t.Fatalf("changes with source and emails %+v, want S1's subject and Lena's email", c.Changes)
		}
		var d diffOut
		text, isErr := callTool(t, ss, "diff", map[string]any{"change": c.Changes[2].Landing})
		if isErr {
			t.Fatalf("diff failed: %s", text)
		}
		decode(t, text, &d)
		i := slices.IndexFunc(d.Files, func(f app.DiffFile) bool { return f.Path == "legacy_a.py" })
		if i < 0 || d.Files[i].Class != "prod" || !d.Files[i].InPatch || d.Files[i].Removed != 22 ||
			!strings.Contains(d.Patch, "-legacy_a_1 = 1") || d.Truncated {
			t.Errorf("diff of S1: files %+v, patch %q; want legacy_a.py (prod, 22 removed) in the patch", d.Files, d.Patch)
		}
		// With emails allowed, each candidate carries its suggested .mailmap
		// lines, as 'stillhere identities --format mailmap' writes them:
		// Gina → Hank is a guess, so its line is commented out (SPEC,
		// identity candidates).
		var ids struct {
			Candidates []struct {
				Mailmap []string `json:"mailmap"`
			} `json:"candidates"`
		}
		text, isErr = callTool(t, ss, "identity_candidates", nil)
		if isErr {
			t.Fatalf("identity_candidates failed: %s", text)
		}
		decode(t, text, &ids)
		if len(ids.Candidates) != 1 ||
			!slices.Equal(ids.Candidates[0].Mailmap, []string{"# Hank Split <hank@example.com> <gina@example.com>"}) {
			t.Errorf("identity_candidates with --allow-emails: %+v, want Gina → Hank's commented line", ids.Candidates)
		}
	})

	// A flag the agent cannot change, set wrong, is an error that says what
	// the user does about it.
	t.Run("flags", func(t *testing.T) {
		pinned := connect(t, Config{Open: openDB(db), RunID: 99, Version: "test"}, 0)
		if text, isErr := callTool(t, pinned, "overview", nil); !isErr || !strings.Contains(text, "pinned to run 99 (--run)") ||
			!strings.Contains(text, "the latest scan is run 1") {
			t.Errorf("a server pinned to a run facts.db lacks answered %q, want what the user restarts it with", text)
		}
		alias := connect(t, Config{Open: openDB(db), Me: "ada@other.example", Version: "test"}, 0)
		if text, isErr := callTool(t, alias, "person", nil); !isErr || !strings.Contains(text, "canonical email") {
			t.Errorf("person() for a --me no one has answered %q, want that --me must be the canonical email", text)
		}
		var p personOut
		text, _ := callTool(t, alias, "person", map[string]any{"name": "Ada Example"})
		decode(t, text, &p)
		if p.OwnPage || !slices.ContainsFunc(p.Caveats, func(c report.Caveat) bool { return c.Code == "me-not-in-run" }) {
			t.Errorf("person(Ada Example) for a --me no one has: own page %v, caveats %v; want a me-not-in-run caveat",
				p.OwnPage, p.Caveats)
		}
	})

	// A bug in one answer is that call's error, as net/http keeps serving
	// after a handler panics: the next call is answered.
	t.Run("panic", func(t *testing.T) {
		s := newServer(Config{Version: "test"})
		res := s.answer(ctx, "boom", format{}, "", func(context.Context, *call) (render, error) { panic("boom") })
		if text := res.Content[0].(*sdk.TextContent).Text; !res.IsError || !strings.Contains(text, "stillhere's bug") {
			t.Errorf("a panicking call answered %q, want an error that says it is stillhere's bug", text)
		}
		if res := s.metrics(ctx, metricsIn{}); res.IsError {
			t.Errorf("the call after a panic failed: %v", res.Content)
		}
	})

	// One line on stdin that is not a message is answered as JSON-RPC
	// asks, and the session goes on.
	t.Run("stdio", func(t *testing.T) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, Config{Version: "test"}, inR, outW)
			_ = outW.Close()
		}()
		lines := bufio.NewScanner(outR)
		lines.Buffer(nil, 1<<20)
		send := func(line string) {
			t.Helper()
			if _, err := io.WriteString(inW, line+"\n"); err != nil {
				t.Fatal(err)
			}
		}
		next := func() map[string]any {
			t.Helper()
			if !lines.Scan() {
				t.Fatalf("the server wrote nothing more: %v", lines.Err())
			}
			var m map[string]any
			decode(t, lines.Text(), &m)
			return m
		}
		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
		if m := next(); m["id"] != 1.0 || m["result"] == nil {
			t.Fatalf("initialize answered %v", m)
		}
		send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		send(`this is not json`)
		if m := next(); m["id"] != nil || m["error"] == nil || m["error"].(map[string]any)["code"] != -32700.0 {
			t.Errorf("a line that is not JSON was answered %v, want a parse error (-32700) with a null id", m)
		}
		send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"metrics","arguments":{}}}`)
		if m := next(); m["id"] != 2.0 || m["result"] == nil {
			t.Errorf("the call after the stray line was answered %v, want metrics' answer", m)
		}
		// An id with neither a method nor a result or an error is no message
		// (MC-10): the SDK alone would drop it without a word.
		send(`{"jsonrpc":"2.0","id":"x"}`)
		if m := next(); m["id"] != nil || m["error"] == nil || m["error"].(map[string]any)["code"] != -32600.0 {
			t.Errorf("an id with no method was answered %v, want an invalid request (-32600) with a null id", m)
		}
		// A call read before stdin closes is answered before the server
		// ends (#58), as a client that pipes its requests expects.
		send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"metrics","arguments":{"metric":"durable"}}}`)
		_ = inW.Close()
		if m := next(); m["id"] != 3.0 || m["result"] == nil {
			t.Errorf("the call read before stdin closed was answered %v, want metrics' answer", m)
		}
		if err := <-done; err != nil {
			t.Errorf("Serve ended with %v after stdin closed, want nil", err)
		}
	})

	// A second scan adds two repositories: one whose people have emails of
	// every odd shape, a version pin in a path and an email in a commit
	// subject, and one of a single person.
	odd, solo := filepath.Join(platform, "odd"), filepath.Join(platform, "solo")
	buildRepo(t, odd, []testCommit{
		{"dev", "dev@build-box", "2026-03-02", "init by dev", "src/app.py", numbered("dev", 20)},
		{"Ip Literal", "ip@[10.0.0.1]", "2026-03-05", "feat: ip", "src/ip.py", numbered("ip", 12)},
		{"Jürgen Müller", "jürgen@firma.de", "2026-03-09", "feat: jürgen", "src/jurgen.py", numbered("jurgen", 15)},
		{"Root", "root@localhost", "2026-04-01", "feat: root", "src/root.py", numbered("root", 9)},
		{"Colon Person", "colon@corp.example", "2026-08-20", "feat: g by colon@corp.example: fixed", "src/g.py", numbered("g", 8)},
		{"Pat O'Brien", "o'brien@corp.example", "2026-08-25", "pins: lodash@4.17.21 from bar@v1.2.3",
			"src/pins/lodash@4.17.21/index.py", "pin = 'github.com/foo/bar@v1.2.3'\n" + numbered("pins", 5)},
		// Every byte of it is escaped in JSON, so its patch is twice as long
		// in an answer as in the file.
		{"dev", "dev@build-box", "2026-09-10", "feat: quotes", "src/quotes.py", strings.Repeat(`q = "\"\"\"\"\"\"\"\"\"\"\"\""`+"\n", 1000)},
		// One name on two addresses, written two ways, which no .mailmap
		// merged: identities compares names without case or punctuation.
		{"Max Mustermann", "max@corp.example", "2026-03-12", "feat: max", "src/max.py", numbered("max", 2)},
		{"MAX MUSTERMANN.", "max@mail.example", "2026-03-13", "feat: max again", "src/max2.py", numbered("maxi", 2)},
	})
	buildRepo(t, solo, []testCommit{
		{"Solo Dev", "solo@corp.example", "2026-05-02", "init", "main.py", numbered("solo", 10)},
		{"Solo Dev", "solo@corp.example", "2026-08-02", "feat: more", "more.py", numbered("more", 10)},
	})
	seedSolo(t, git, db, solo)
	scan(t, db, mailmap, domain.ScopeGroup, platform)
	emails = append(emails, runEmails(t, db, 2)...)
	for _, e := range []string{"dev@build-box", "ip@[10.0.0.1]", "jürgen@firma.de", "root@localhost", "o'brien@corp.example"} {
		if !slices.Contains(emails, e) {
			t.Fatalf("run 2 does not know %s: its people are %v", e, emails)
		}
	}

	t.Run("second run", func(t *testing.T) {
		// The first answer that reads the new run says so, and a cursor of
		// the old run is refused rather than paging another list.
		var o struct {
			head
			tail
		}
		decode(t, ask(t, "overview", nil), &o)
		if o.Provenance.RunID != 2 || !slices.ContainsFunc(o.Caveats, func(c report.Caveat) bool { return c.Code == "run-changed" }) {
			t.Errorf("the first answer of run 2: run %d, caveats %v; want a run-changed caveat", o.Provenance.RunID, o.Caveats)
		}
		if text := refused(t, "overview", map[string]any{"limit": 2, "cursor": runOneCursor}); !strings.Contains(text, "belongs to run 1") {
			t.Errorf("a cursor of run 1 was refused with %q, want one that names its run", text)
		}

		// Every odd email is withheld (ask checks every answer), and each
		// id is the one person resolves. dev's and Root's names are their
		// addresses' local parts, so they are withheld too: no answer names
		// them, they are asked for by id, and their name matches no one.
		dev, root := pseudonym("dev@build-box"), pseudonym("root@localhost")
		var repo struct {
			Repos []report.Repo `json:"repos"`
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "odd"}), &repo)
		if len(repo.Repos) != 1 || len(repo.Repos[0].Card.MadeOf) == 0 {
			t.Fatalf("overview(odd) %+v, want its card", repo.Repos)
		}
		for _, a := range repo.Repos[0].Card.MadeOf {
			if withheld := a.Email == dev || a.Email == root; a.NameWithheld != withheld || (a.Name == "") != withheld {
				t.Errorf("overview(odd) names %s %q (withheld %v), want the name withheld exactly for dev and Root", a.Email, a.Name, a.NameWithheld)
			}
			var p personOut
			decode(t, ask(t, "person", map[string]any{"name": a.Email}), &p)
			if p.Person.Name != a.Name || p.Person.Key != a.Email {
				t.Errorf("person(%s), the id overview gave %s, is %+v", a.Email, a.Name, p.Person)
			}
		}
		if text := refused(t, "person", map[string]any{"name": "Root"}); strings.Contains(text, root) {
			t.Errorf("person(Root) was refused with %q, which points at the person whose name is withheld", text)
		}
		for _, args := range []map[string]any{
			{"scope": "odd", "response_format": "detailed"}, {"scope": "platform", "rank": "durable"},
		} {
			ask(t, "overview", args)
		}
		for _, name := range []string{dev, "Ip Literal", "Jürgen Müller", root, "Pat O'Brien"} {
			ask(t, "person", map[string]any{"name": name, "response_format": "detailed"})
			ask(t, "explain", map[string]any{"metric": "surviving", "subject": name, "response_format": "detailed"})
		}
		ask(t, "changes", map[string]any{"scope": "odd", "since": "365d", "response_format": "detailed"})
		ask(t, "highlights", map[string]any{"period": "quarter", "response_format": "detailed"})
		ask(t, "who_knows", map[string]any{"path": "src", "repo": "odd", "response_format": "detailed"})
		// identity_candidates' addresses are emails too.
		if text, isErr := callTool(t, cs, "identity_candidates", nil); isErr {
			t.Errorf("identity_candidates failed: %s", text)
		} else if e := leak(text); e != "" {
			t.Errorf("identity_candidates holds %q without --allow-emails: %s", e, text)
		}
		// The candidates are paged, as every list tool's main list is
		// (#58): concise rows without the rules' evidence, which detailed
		// adds, and a cursor that leads to the rest.
		type idsPage struct {
			Candidates []map[string]any `json:"candidates"`
			Page       page             `json:"page"`
		}
		var p1, p2, det idsPage
		decode(t, ask(t, "identity_candidates", map[string]any{"limit": 1}), &p1)
		if len(p1.Candidates) != 1 || p1.Page.Total < 2 || p1.Page.NextCursor == nil || p1.Candidates[0]["links"] != nil {
			t.Fatalf("identity_candidates(limit: 1) %+v, want one concise candidate of two or more, and a cursor", p1)
		}
		decode(t, ask(t, "identity_candidates", map[string]any{"limit": 1, "cursor": *p1.Page.NextCursor}), &p2)
		if len(p2.Candidates) != 1 || p2.Page.Offset != 1 || reflect.DeepEqual(p1.Candidates, p2.Candidates) {
			t.Errorf("identity_candidates' second page %+v, want the next candidate", p2)
		}
		decode(t, ask(t, "identity_candidates", map[string]any{"limit": 1, "response_format": "detailed"}), &det)
		if len(det.Candidates) != 1 || det.Candidates[0]["links"] == nil {
			t.Errorf("identity_candidates detailed %+v, want the candidate with its links", det)
		}

		// A repository's areas (#106): odd's prod lines are all under src/,
		// so its one area holds the repository's own knowledge loss.
		var oa struct {
			Repos      []report.Repo `json:"repos"`
			Areas      []report.Area `json:"areas"`
			AreasTotal int           `json:"areas_total"`
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "odd"}), &oa)
		if kl := oa.Repos[0].Card.KnowledgeLoss; len(oa.Areas) != 1 || oa.AreasTotal != 1 || oa.Areas[0].Path != "src/" ||
			kl == nil || !reflect.DeepEqual(oa.Areas[0].KnowledgeLoss, kl) ||
			oa.Areas[0].Lines != int(kl.Den) || oa.Areas[0].Concentration == nil {
			t.Errorf("overview(odd) areas %+v of %d, want src/ alone, with the repository's knowledge loss %+v and a concentration",
				oa.Areas, oa.AreasTotal, kl)
		}
		// who_knows of that directory gives the same knowledge numbers, by
		// the one computation a scan stores (metrics.PathKnowledge), and of
		// a deeper path, which no scan stores, its own (#106).
		var wa whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": "src/", "repo": "odd"}), &wa)
		if len(wa.Repos) != 1 || len(oa.Areas) == 0 || wa.Repos[0].KnowledgeLoss == nil ||
			!reflect.DeepEqual(wa.Repos[0].KnowledgeLoss, oa.Areas[0].KnowledgeLoss) ||
			!reflect.DeepEqual(wa.Repos[0].People, oa.Areas[0].People) ||
			!reflect.DeepEqual(wa.Repos[0].PeopleInactive, oa.Areas[0].PeopleInactive) ||
			!reflect.DeepEqual(wa.Repos[0].Concentration, oa.Areas[0].Concentration) {
			t.Errorf("who_knows(src/, odd) %+v, want overview's area numbers %+v", wa.Repos, oa.Areas)
		}
		var wd whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": "src/pins", "repo": "odd"}), &wd)
		if len(wd.Repos) != 1 || wd.Repos[0].KnowledgeLoss == nil || len(oa.Areas) == 0 ||
			wd.Repos[0].KnowledgeLoss.Den <= 0 || wd.Repos[0].KnowledgeLoss.Den > float64(oa.Areas[0].Lines) {
			t.Errorf("who_knows(src/pins, odd) %+v, want its own knowledge loss over at most src/'s %d lines", wd.Repos, oa.Areas[0].Lines)
		}

		// A path with a version pin in it is the repository's path, which
		// the agent can pass back.
		const pin = "src/pins/lodash@4.17.21/index.py"
		var w whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": pin, "repo": "odd"}), &w)
		if w.Path != pin || len(w.Repos) != 1 || names(w.Repos[0].TopAuthors) != "Pat O'Brien" {
			t.Errorf("who_knows(%s) answered %q with %+v, want the path as it is and Pat O'Brien", pin, w.Path, w.Repos)
		}
		decode(t, ask(t, "who_knows", map[string]any{"path": w.Path, "repo": "odd"}), &w)
		if w.Path != pin || len(w.Repos) != 1 {
			t.Errorf("who_knows of the path an answer gave, %q, answered %q", pin, w.Path)
		}

		// The share of a repository one person landed in is that person's
		// own: it does not leave (ADR 0007).
		var s struct {
			Repos []report.Repo `json:"repos"`
			tail
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "solo"}), &s)
		if len(s.Repos) != 1 || s.Repos[0].Card.AIAssisted != nil ||
			!slices.ContainsFunc(s.Caveats, func(c report.Caveat) bool { return c.Code == "ai-few-people" }) {
			t.Errorf("overview(solo) shows the share %+v with caveats %v, want it withheld and said so", s.Repos[0].Card.AIAssisted, s.Caveats)
		}
		if text := refused(t, "explain", map[string]any{"metric": "ai_assisted", "subject": "solo", "subject_kind": "repo"}); !strings.Contains(text, "withheld") {
			t.Errorf("explain(ai_assisted, solo) was refused with %q, want the share withheld", text)
		}

		// GitHub logins (#69, seedSolo). Zed, whom no commit links to an
		// email, is keyed by his login lowercased, as GitHub compares
		// logins, and named as GitHub shows it, so every case of it finds
		// him and his review; SoloDev, whose commit is Solo Dev's, finds
		// Solo Dev in any case, and a candidate list shows the login.
		for _, name := range []string{"@Zed", "@zed", "@ZED"} {
			var z personOut
			decode(t, ask(t, "person", map[string]any{"name": name}), &z)
			if z.Person.Key != "@zed" || z.Person.Name != "@Zed" ||
				!slices.ContainsFunc(z.Values, func(v valueOut) bool { return v.Metric == "reviews_given@1" && v.Value == 1 }) {
				t.Errorf("person(%s) is %+v with values %+v, want @zed named @Zed, 1 pull request reviewed", name, z.Person, z.Values)
			}
		}
		var ze explainOut
		decode(t, ask(t, "explain", map[string]any{"metric": "reviews_given", "subject": "@Zed"}), &ze)
		if len(ze.Values) == 0 || slices.ContainsFunc(ze.Values, func(v valueOut) bool { return v.Value != 1 }) {
			t.Errorf("explain(reviews_given, @Zed) %+v, want 1 in every scope", ze.Values)
		}
		var sd personOut
		decode(t, ask(t, "person", map[string]any{"name": "@soloDEV"}), &sd)
		if sd.Person.Name != "Solo Dev" {
			t.Errorf("person(@soloDEV) is %+v, want Solo Dev, whose commit GitHub attributes to SoloDev", sd.Person)
		}
		if text := refused(t, "person", map[string]any{"name": "Solo Deb"}); !strings.Contains(text, `"Solo Dev" (id p-`) ||
			!strings.Contains(text, "login @SoloDev") {
			t.Errorf("a misspelt name's suggestion %q does not show Solo Dev's login", text)
		}
		var sw whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": "more.py", "repo": "solo"}), &sw)
		if len(sw.Repos) != 1 || len(sw.Repos[0].RecentReviewers) != 1 || sw.Repos[0].RecentReviewers[0].Email != "@zed" ||
			sw.Repos[0].RecentReviewers[0].Name != "@Zed" {
			t.Errorf("who_knows(more.py in solo) %+v, want Zed as its recent reviewer, keyed @zed", sw.Repos)
		}

		// One name written two ways on two addresses is one name to the
		// identities rules, and so to the caveats, which send the agent to
		// the tool that suggests the line (#69): in the platform's overview
		// and in a who_knows answer that lists both.
		var g struct{ tail }
		decode(t, ask(t, "overview", map[string]any{"scope": "platform"}), &g)
		if !hasCaveat(g.Caveats, "unmerged-identities", `"MAX MUSTERMANN."`) ||
			!hasCaveat(g.Caveats, "unmerged-identities", "identity_candidates(scope) suggests the lines") ||
			hasCaveat(g.Caveats, "unmerged-identities", "stillhere") {
			t.Errorf("overview(platform) caveats %v, want MAX MUSTERMANN. as unmerged, with identity_candidates", g.Caveats)
		}
		var mw whoKnowsOut
		decode(t, ask(t, "who_knows", map[string]any{"path": "src", "repo": "odd"}), &mw)
		if !hasCaveat(mw.Caveats, "unmerged-identities", `"MAX MUSTERMANN."`) {
			t.Errorf("who_knows(src in odd) caveats %v, want the two Max Mustermanns it lists as unmerged", mw.Caveats)
		}

		// With source allowed and emails not: a subject keeps its version
		// pins and loses its email, and a diff keeps its paths and code.
		ss := connect(t, Config{Open: openDB(db), Git: git, AllowSource: true, Version: "test"}, 0)
		var c struct {
			Changes []changeBrief `json:"changes"`
		}
		text, _ := callTool(t, ss, "changes", map[string]any{"scope": "odd", "since": "365d"})
		if e := leak(text); e != "" {
			t.Errorf("changes with source holds %q: %s", e, text)
		}
		decode(t, text, &c)
		titles := map[string]string{}
		for _, ch := range c.Changes {
			titles[ch.Title] = ch.Landing
		}
		pinned, ok := titles["pins: lodash@4.17.21 from bar@v1.2.3"]
		if _, colon := titles["feat: g by "+pseudonym("colon@corp.example")+": fixed"]; !ok || !colon {
			t.Errorf("subjects %v, want the pins one whole and colon's email as its id", slices.Sorted(maps.Keys(titles)))
		}
		var d diffOut
		text, isErr := callTool(t, ss, "diff", map[string]any{"change": pinned})
		if isErr {
			t.Fatalf("diff(%s) failed: %s", pinned, text)
		}
		decode(t, text, &d)
		if len(d.Files) != 1 || d.Files[0].Path != pin || !strings.Contains(d.Patch, "+++ b/"+pin) ||
			!strings.Contains(d.Patch, "github.com/foo/bar@v1.2.3") {
			t.Errorf("diff of the pins change: files %+v, patch %q; want the path and the code as they are", d.Files, d.Patch)
		}
		// A patch whose JSON escapes put the answer over the budget is cut
		// further, on a line, and the answer says so.
		d = diffOut{}
		text, isErr = callTool(t, ss, "diff", map[string]any{"change": titles["feat: quotes"]})
		if isErr {
			t.Fatalf("diff of the quotes change failed: %s", text)
		}
		decode(t, text, &d)
		if !d.Truncated || len(d.Patch) >= 24000 || !strings.HasSuffix(d.Patch, "\n") ||
			!slices.ContainsFunc(d.Caveats, func(c report.Caveat) bool { return strings.Contains(c.Message, "so the answer fits") }) {
			t.Errorf("diff of the quotes change: %d bytes of patch, truncated %v, caveats %v; want it cut on a line to fit",
				len(d.Patch), d.Truncated, d.Caveats)
		}
	})

	// The egress filter, case by case: an email the run does not know is
	// withheld by where it is (a member named email, a person's key) or by
	// how it looks in free text, and nothing else that has an @ changes.
	t.Run("egress", func(t *testing.T) {
		type doc struct {
			Rows    []report.StillHereRow `json:"rows"`
			Subject report.Subject        `json:"subject"`
			Scope   report.Scope          `json:"scope"`
			Path    string                `json:"path"`
			Texts   []string              `json:"texts"`
		}
		in := doc{
			Rows:    []report.StillHereRow{{Email: "who@intranet", Name: "Who"}},
			Subject: report.Subject{Kind: domain.SubjectPerson, Key: "who@[192.168.0.1]"},
			Scope:   report.Scope{Kind: domain.ScopeRepo, Key: "git@github.com:o/r.git"},
			Path:    "deps/bob@corp.example/x.py",
			Texts: []string{
				"mail 'bob@corp.example' or Alice@Corp.Example.",
				"feat: g by colon@corp.example: fixed",
				"lodash@4.17.21, actions/setup-go@v5.0.1, durable@1, x@latest and github.com/foo/bar@v1.2.3",
				"git@github.com:o/r.git and jürgen@firma.de",
			},
		}
		want := doc{
			Rows:    []report.StillHereRow{{Email: pseudonym("who@intranet"), Name: "Who"}},
			Subject: report.Subject{Kind: domain.SubjectPerson, Key: pseudonym("who@[192.168.0.1]")},
			Scope:   in.Scope,
			Path:    in.Path,
			Texts: []string{
				"mail '" + pseudonym("bob@corp.example") + "' or " + pseudonym("alice@corp.example") + ".",
				"feat: g by " + pseudonym("colon@corp.example") + ": fixed",
				in.Texts[2],
				"git@github.com:o/r.git and " + pseudonym("jürgen@firma.de"),
			},
		}
		b, err := encode(in, newWithholder(nil))
		if err != nil {
			t.Fatal(err)
		}
		w, err := encode(want, nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != string(w) {
			t.Errorf("the egress filter gave\n%s\nwant\n%s", b, w)
		}
		// A diff whose patch the filter will rewrite says so where the code
		// is (MC-18); a version pin is no address, and a server that lets
		// emails leave rewrites nothing.
		patch := &app.DiffAnswer{Patch: "+author = 'Jane Smith <jane@example.org>'\n+pin = 'lodash@4.17.21'\n"}
		f := format{limit: diffFiles, rows: minRows}
		if d := (&shaper{f: f, w: newWithholder(nil)}).diffAnswer(patch); !hasCaveat(d.Caveats, "patch-emails", "1 email address") {
			t.Errorf("a diff of a patch with one address carries %v, want patch-emails counting it", d.Caveats)
		}
		if d := (&shaper{f: f}).diffAnswer(patch); hasCaveat(d.Caveats, "patch-emails", "") {
			t.Errorf("a diff with emails allowed carries %v, want no patch-emails", d.Caveats)
		}
	})

	// AI-assisted shares add up over the scopes, so a share withheld on its
	// own could be worked out from those shown around it: a group's less its
	// shown repositories' (ADR 0007's amendment of 2026-10-15). Group ai holds
	// team, four people with one AI-assisted landing of 12, and solo, one
	// person with three of 4; group solos holds three repositories of one
	// person each; group alias holds max, one person who committed from three
	// addresses, under three spellings of a name, that no .mailmap merges;
	// group defaults holds doe, one person who committed as John Doe, a
	// default name, from three addresses with nothing else in common; group
	// blanks holds blank, two people, one of whom also committed with an
	// empty email; group nomail holds crew, three people, and ghost, whose
	// one person never set an email. The local report shows every share, which
	// is how the test knows what no answer may let the agent work out, and
	// surviving@1's population makeup copies each share (ai_assisted_share),
	// which explain must withhold as the decision does.
	t.Run("AI-assisted shares that add up", func(t *testing.T) {
		root := t.TempDir()
		group, solos, lone := filepath.Join(root, "ai"), filepath.Join(root, "solos"), filepath.Join(root, "lone")
		alias, defaults, blanks := filepath.Join(root, "alias"), filepath.Join(root, "defaults"), filepath.Join(root, "blanks")
		nomail := filepath.Join(root, "nomail")
		const trailer = "\n\nCo-Authored-By: Claude <noreply@anthropic.com>"
		commit := func(name, email string, day int, ai bool) testCommit {
			subject := "feat: " + strings.ToLower(name) + " " + strconv.Itoa(day)
			if ai {
				subject += trailer
			}
			file := strings.ToLower(strings.ReplaceAll(name, " ", "_")) + "_" + strconv.Itoa(day) + ".py"
			return testCommit{name, email, "2026-09-" + fmt.Sprintf("%02d", day), subject, file, numbered("f"+strconv.Itoa(day), 3)}
		}
		var team []testCommit
		for i, who := range []string{"Ann", "Ben", "Cat", "Dan"} {
			for j := range 3 {
				day := 1 + 3*i + j
				team = append(team, commit(who+" Team", strings.ToLower(who)+"@team.example", day, day == 5))
			}
		}
		buildRepo(t, filepath.Join(group, "team"), team)
		buildRepo(t, filepath.Join(group, "solo"), []testCommit{
			commit("Sam Solo", "sam@solo.example", 20, true), commit("Sam Solo", "sam@solo.example", 21, true),
			commit("Sam Solo", "sam@solo.example", 22, false), commit("Sam Solo", "sam@solo.example", 23, true),
		})
		for i, who := range []string{"One", "Two", "Three"} {
			buildRepo(t, filepath.Join(solos, "s"+strconv.Itoa(i+1)), []testCommit{
				commit("Pat "+who, strings.ToLower(who)+"@solos.example", 24+i, i == 0)})
		}
		buildRepo(t, lone, []testCommit{commit("Lee Lone", "lee@lone.example", 28, true)})
		// One person, three addresses: a work one, a laptop's made-up one and
		// GitHub's noreply one, under three spellings of their name, which
		// identities leaves apart (the names disagree) and the floor's blocks
		// join by the local part max (identity.Blocks).
		buildRepo(t, filepath.Join(alias, "max"), []testCommit{
			commit("Max Multi", "max@multi.example", 13, true), commit("M. Multi", "max@maxs-laptop.local", 14, false),
			commit("Maximilian Multi", "4242+max@users.noreply.github.com", 15, false),
		})
		// One person under a default name, which identities never compares
		// and the floor's blocks still join.
		buildRepo(t, filepath.Join(defaults, "doe"), []testCommit{
			commit("John Doe", "jdoe@work.example", 16, true), commit("John Doe", "johnny@laptop.local", 17, false),
			commit("John Doe", "jd.home@home.example", 18, false),
		})
		// An empty email is a missing fact: beside Sam's other address and
		// Bob, it adds no one.
		buildRepo(t, filepath.Join(blanks, "blank"), []testCommit{
			commit("Sam Blank", "", 19, true), commit("Sam Blank", "sam@blank.example", 27, false),
			commit("Bob Blank", "bob@blank.example", 29, true),
		})
		// An email no one set is still someone when no one else is there:
		// ghost's share is withheld, and so is crew's, which beside the
		// group's would give ghost's away.
		buildRepo(t, filepath.Join(nomail, "crew"), []testCommit{
			commit("Iris Crew", "iris@crew.example", 2, true), commit("Joel Crew", "joel@crew.example", 3, false),
			commit("Kira Crew", "kira@crew.example", 4, false),
		})
		buildRepo(t, filepath.Join(nomail, "ghost"), []testCommit{
			commit("Sam Ghost", "", 5, true), commit("Sam Ghost", "", 6, true), commit("Sam Ghost", "", 7, false),
		})

		// overview answers each scope's share as the agent sees it, keyed
		// "all:", "group:NAME" or "repo:NAME"; nil is withheld.
		type answer struct {
			Overviews []struct {
				Scope      report.Scope  `json:"scope"`
				AIAssisted *report.Share `json:"ai_assisted"`
			} `json:"overviews"`
			Repos []struct {
				Name       string        `json:"name"`
				AIAssisted *report.Share `json:"ai_assisted"`
				Card       *struct {
					AIAssisted *report.Share `json:"ai_assisted"`
				} `json:"card"`
			} `json:"repos"`
			Caveats []report.Caveat `json:"caveats"`
		}
		overview := func(t *testing.T, cs *sdk.ClientSession, scope string) (map[string]*report.Share, answer) {
			t.Helper()
			text, isErr := callTool(t, cs, "overview", map[string]any{"scope": scope})
			if isErr {
				t.Fatalf("overview(%q) failed: %s", scope, text)
			}
			var a answer
			decode(t, text, &a)
			got := map[string]*report.Share{}
			for _, o := range a.Overviews {
				got[o.Scope.Kind+":"+o.Scope.Key] = o.AIAssisted
			}
			for _, r := range a.Repos {
				if r.Card != nil {
					r.AIAssisted = r.Card.AIAssisted
				}
				got["repo:"+r.Name] = r.AIAssisted
			}
			return got, a
		}
		// local is every share as the local report shows it.
		local := func(t *testing.T, db string) map[string]*report.Share {
			t.Helper()
			st, err := sqlite.OpenReadOnly(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			a, err := app.Overview(ctx, app.Deps{Store: st}, app.OverviewRequest{})
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]*report.Share{}
			for _, o := range a.Overviews {
				out[o.Scope.Kind+":"+o.Scope.Key] = o.AIAssisted
			}
			for _, r := range a.Repos {
				out["repo:"+r.Name] = r.Card.AIAssisted
			}
			return out
		}
		// shown checks which shares an answer holds, each as the local
		// report has it.
		shown := func(t *testing.T, what string, got, truth map[string]*report.Share, want ...string) {
			t.Helper()
			for k, sh := range got {
				switch {
				case slices.Contains(want, k) && (sh == nil || truth[k] == nil || *sh != *truth[k]):
					t.Errorf("%s: %s's share is %+v, want the local report's %+v", what, k, sh, truth[k])
				case !slices.Contains(want, k) && sh != nil:
					t.Errorf("%s: %s's share %+v is shown, want it withheld", what, k, sh)
				}
			}
		}
		// residual is a scope's share less the shares shown below it: what
		// subtracting gives.
		residual := func(whole *report.Share, parts ...*report.Share) [2]float64 {
			r := [2]float64{whole.Num, whole.Den}
			for _, p := range parts {
				if p != nil {
					r[0], r[1] = r[0]-p.Num, r[1]-p.Den
				}
			}
			return r
		}
		withheld := func(t *testing.T, cs *sdk.ClientSession, subject, kind string) {
			t.Helper()
			text, isErr := callTool(t, cs, "explain", map[string]any{"metric": "ai_assisted", "subject": subject, "subject_kind": kind})
			if !isErr || !strings.Contains(text, "withheld") || strings.Contains(text, "instead") {
				t.Errorf("explain(ai_assisted, %s %s) answered %q, want it refused without pointing at another share", kind, subject, text)
			}
		}
		// copied checks the share surviving@1's population makeup copies
		// (ai_assisted_share) in explain's answer about a repository or a
		// group: the local report's share when want holds it, and otherwise
		// none, with the caveat that names the scope.
		copied := func(t *testing.T, cs *sdk.ClientSession, subject, kind string, want *report.Share) {
			t.Helper()
			text, isErr := callTool(t, cs, "explain", map[string]any{"metric": "surviving", "subject": subject, "subject_kind": kind})
			if isErr {
				t.Fatalf("explain(surviving, %s %s) failed: %s", kind, subject, text)
			}
			var e explainOut
			decode(t, text, &e)
			if len(e.Values) == 0 {
				t.Fatalf("explain(surviving, %s %s) holds no value: %s", kind, subject, text)
			}
			for _, v := range e.Values {
				share, ok := v.Detail["ai_assisted_share"]
				switch {
				case want != nil && (!ok || !near(share, want.Value)):
					t.Errorf("explain(surviving, %s %s): ai_assisted_share %v (%v), want the local report's %v", kind, subject, share, ok, want.Value)
				case want == nil && ok:
					t.Errorf("explain(surviving, %s %s) holds the withheld AI-assisted share as ai_assisted_share %v", kind, subject, share)
				}
			}
			if want == nil && !slices.ContainsFunc(e.Caveats, func(c report.Caveat) bool {
				return c.Code == "ai-few-people" && strings.Contains(c.Message, strconv.Quote(subject))
			}) {
				t.Errorf("explain(surviving, %s %s) caveats %v, want ai-few-people naming it", kind, subject, e.Caveats)
			}
		}

		db := filepath.Join(root, "group.db")
		scan(t, db, mailmap, domain.ScopeGroup, group, solos, alias, defaults, blanks, nomail)
		truth := local(t, db)
		cs := connect(t, Config{Open: openDB(db), Version: "test"}, 0)

		// Team's share, shown beside the group's, would give solo's away:
		// both are withheld, and the group's share alone is what is left.
		got, a := overview(t, cs, "ai")
		shown(t, "overview(ai)", got, truth, "group:ai")
		if sh := got["group:ai"]; sh == nil || residual(sh, got["repo:team"], got["repo:solo"]) == residual(truth["repo:solo"]) {
			t.Errorf("overview(ai): the group's share less its repositories' shown gives solo's own share %+v", truth["repo:solo"])
		}
		if !slices.ContainsFunc(a.Caveats, func(c report.Caveat) bool {
			return c.Code == "ai-few-people" && strings.Contains(c.Message, `"team"`) && strings.Contains(c.Message, `"solo"`)
		}) {
			t.Errorf("overview(ai) caveats %v, want ai-few-people naming team and solo", a.Caveats)
		}
		// A second call, about one repository, reads the same decision, and
		// so does surviving@1's copy of the share.
		for _, repo := range []string{"team", "solo"} {
			got, _ = overview(t, cs, repo)
			shown(t, "overview("+repo+")", got, truth)
			withheld(t, cs, repo, "repo")
			copied(t, cs, repo, "repo", nil)
		}
		copied(t, cs, "ai", "group", truth["group:ai"])
		// Three people of one repository each make a group's share: it is
		// shown, theirs are not.
		got, _ = overview(t, cs, "solos")
		shown(t, "overview(solos)", got, truth, "group:solos")
		text, isErr := callTool(t, cs, "explain", map[string]any{"metric": "ai_assisted", "subject": "solos", "subject_kind": "group"})
		var e explainOut
		if !isErr {
			decode(t, text, &e)
		}
		if len(e.Values) != 1 {
			t.Errorf("explain(ai_assisted, group solos) answered %s, want the group's share", text)
		}
		copied(t, cs, "solos", "group", truth["group:solos"])
		copied(t, cs, "s1", "repo", nil)
		// Three addresses of one block are one person, so neither max's share
		// nor its group's leaves: the count errs low, never high.
		got, _ = overview(t, cs, "alias")
		shown(t, "overview(alias)", got, truth)
		withheld(t, cs, "max", "repo")
		withheld(t, cs, "alias", "group")
		copied(t, cs, "max", "repo", nil)
		got, _ = overview(t, cs, "defaults")
		shown(t, "overview(defaults)", got, truth)
		withheld(t, cs, "doe", "repo")
		got, _ = overview(t, cs, "blanks")
		shown(t, "overview(blanks)", got, truth)
		withheld(t, cs, "blank", "repo")
		got, _ = overview(t, cs, "nomail")
		shown(t, "overview(nomail)", got, truth, "group:nomail")
		withheld(t, cs, "ghost", "repo")

		// With --scope all the whole run's share is shown too, and a
		// repository of one person named on its own hangs from the run: the
		// run's share less group ai's would be lone's own. Group ai is
		// withheld on the run's account, and only the run's share is shown.
		db = filepath.Join(root, "all.db")
		scan(t, db, mailmap, domain.ScopeAll, group, lone)
		truth = local(t, db)
		cs = connect(t, Config{Open: openDB(db), Version: "test"}, 0)
		got, _ = overview(t, cs, "")
		shown(t, "overview() of the whole run", got, truth, "all:")
		if sh := got["all:"]; sh == nil || residual(sh, got["group:ai"], got["repo:lone"]) == residual(truth["repo:lone"]) {
			t.Errorf("overview(): the whole run's share less those shown gives lone's own share %+v", truth["repo:lone"])
		}
		copied(t, cs, "ai", "group", nil)
		copied(t, cs, "lone", "repo", nil)
	})

	// A third scan, of one repository of two people in a group of its name
	// (as go-git/go-git is), becomes the latest run. A scope only an earlier
	// run holds is refused with the run that holds it and how the user
	// serves it (MC-17), and a bare name that run holds twice with each
	// scope_arg there. Ann's removal of 50 lines and Bo's of one line in
	// each of ten weeks give simplifying@1 a table of two whose intervals do
	// not meet, Ann 1-1 and Bo 2-2 (the bootstrap resamples weeks, and Ann
	// removes more in every one), so its top tier stands out although it is
	// half the table (MC-16).
	t.Run("a third run", func(t *testing.T) {
		org := filepath.Join(t.TempDir(), "pair")
		pair := filepath.Join(org, "pair")
		commits := []testCommit{
			{"Ann Apart", "ann@pair.example", "2026-07-10", "add a", "a.py", numbered("a", 600)},
			{"Bo Behind", "bo@pair.example", "2026-07-10", "add b", "b.py", numbered("b", 40)},
		}
		for i := range 10 {
			day := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 7*i).Format(time.DateOnly)
			commits = append(commits,
				testCommit{"Ann Apart", "ann@pair.example", day, "trim a", "a.py", numbered("a", 550-50*i)},
				testCommit{"Bo Behind", "bo@pair.example", day, "trim b", "b.py", numbered("b", 39-i)})
		}
		buildRepo(t, pair, commits)
		scan(t, db, mailmap, domain.ScopeGroup, org)
		emails = append(emails, runEmails(t, db, 3)...)

		text := refused(t, "overview", map[string]any{"scope": "platform"})
		for _, want := range []string{`no group or repository of run 3`, `Run 2 (as of 2026-10-07T00:00:00Z) holds group "platform"`,
			"this server reads the latest scan, run 3", "--run 2"} {
			if !strings.Contains(text, want) {
				t.Errorf("overview(platform) of run 3 was refused with %q, want %q", text, want)
			}
		}
		if text := refused(t, "who_knows", map[string]any{"path": "", "repo": "odd"}); !strings.Contains(text, `Run 2 (as of`) ||
			!strings.Contains(text, `holds repository "odd"`) {
			t.Errorf("who_knows(repo: odd) of run 3 was refused with %q, want run 2 named as holding it", text)
		}
		pinned := connect(t, Config{Open: openDB(db), RunID: 2, Version: "test"}, 0)
		text, isErr := callTool(t, pinned, "overview", map[string]any{"scope": "pair"})
		for _, want := range []string{`Run 3 (as of 2026-10-07T00:00:00Z) holds 2 scopes of that name`,
			`group "pair", passed as "group:pair"`, `passed as "repo:pair"`, "this server is pinned to run 2 (--run)", "--run 3"} {
			if !isErr || !strings.Contains(text, want) {
				t.Errorf("overview(pair) pinned to run 2 answered %q, want a refusal with %q", text, want)
			}
		}

		var h highlightsOut
		decode(t, ask(t, "highlights", map[string]any{"period": "quarter"}), &h)
		i := slices.IndexFunc(h.TopTier, func(tt app.TopTier) bool { return tt.Metric == "simplifying@1" })
		if i < 0 || h.TopTier[i].Of != 2 || h.TopTier[i].PeopleTotal != 1 || len(h.TopTier[i].People) != 1 ||
			h.TopTier[i].People[0].Name != "Ann Apart" || h.TopTier[i].People[0].RankHi == nil || *h.TopTier[i].People[0].RankHi != 1 {
			t.Errorf("highlights' simplifying@1 tiers %+v, want Ann Apart named, 1-1 of 2", h.TopTier)
		}
		if hasCaveat(h.Caveats, "no-standout", "simplifying@1") {
			t.Errorf("highlights say no one stands out in simplifying@1, where Ann's interval lies above Bo's: %v", h.Caveats)
		}
	})

	// A shallow clone's boundary and a partial clone's landings cannot be
	// diffed offline, and diff says why rather than send a whole tree as a
	// change (RB-5) or blame a rewritten history (RB-8).
	t.Run("shallow and partial clones", func(t *testing.T) {
		clones := fixture.CloneHealth(t, "https://github.com/example/partial.git")
		cdb := filepath.Join(t.TempDir(), "clones.db")
		cgit := scan(t, cdb, "", domain.ScopeGroup, clones.Shallow, clones.Partial)
		ss := connect(t, Config{Open: openDB(cdb), Git: cgit, AllowSource: true, AllowEmails: true, Version: "test"}, 0)
		for _, c := range []struct{ repo, want string }{
			{filepath.Base(clones.Shallow), "shallow clone ends: the clone does not hold its parent"},
			{filepath.Base(clones.Partial), "a partial clone (made with --filter)"},
		} {
			var ch struct {
				Changes []changeBrief `json:"changes"`
			}
			text, _ := callTool(t, ss, "changes", map[string]any{"scope": c.repo, "since": "365d"})
			decode(t, text, &ch)
			if len(ch.Changes) == 0 {
				t.Fatalf("changes(%s) is empty: %s", c.repo, text)
			}
			text, isErr := callTool(t, ss, "diff", map[string]any{"change": ch.Changes[0].Landing, "repo": c.repo})
			if !isErr || !strings.Contains(text, c.want) || strings.Contains(text, "rewritten") {
				t.Errorf("diff of %s's newest landing answered %q, want a refusal saying %q", c.repo, text, c.want)
			}
			// Without line facts its areas are unknown, not none (MET-4).
			var o map[string]any
			text, _ = callTool(t, ss, "overview", map[string]any{"scope": c.repo})
			decode(t, text, &o)
			if a, ok := o["areas"]; !ok || a != nil || o["areas_total"] != nil {
				t.Errorf("overview(%s) areas %v (present %v) of %v, want null of null", c.repo, a, ok, o["areas_total"])
			}
		}
		// A partial clone moved since its scan fails for that reason, which
		// no fetch fixes, so diff says so rather than blame its contents.
		if err := os.Rename(clones.Partial, clones.Partial+"-moved"); err != nil {
			t.Fatal(err)
		}
		var ch struct {
			Changes []changeBrief `json:"changes"`
		}
		text, _ := callTool(t, ss, "changes", map[string]any{"scope": filepath.Base(clones.Partial), "since": "365d"})
		decode(t, text, &ch)
		if len(ch.Changes) == 0 {
			t.Fatalf("changes of the partial clone is empty: %s", text)
		}
		text, isErr := callTool(t, ss, "diff", map[string]any{"change": ch.Changes[0].Landing, "repo": filepath.Base(clones.Partial)})
		if !isErr || !strings.Contains(text, "may have moved") || strings.Contains(text, "partial clone") {
			t.Errorf("diff in a partial clone moved since its scan answered %q, want that it may have moved", text)
		}
	})
}

// scanPlatform builds both fixtures in one directory, a platform of two
// repositories, and records one scan of it in a new facts.db. It returns
// the database, the runner, the platform's directory and the mailmap.
func scanPlatform(t *testing.T) (db string, git *gitcli.Runner, platform, mailmap string) {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	platform = filepath.Join(root, "platform")
	if err := fixture.BuildContext(ctx, filepath.Join(platform, "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.BuildSiblingContext(ctx, filepath.Join(platform, "sibling")); err != nil {
		t.Fatal(err)
	}
	mailmap = filepath.Join(root, "mailmap")
	if err := os.WriteFile(mailmap, []byte(fixture.MailmapText+fixture.SiblingMailmapText), 0o600); err != nil {
		t.Fatal(err)
	}
	db = filepath.Join(root, "facts.db")
	return db, scan(t, db, mailmap, domain.ScopeGroup, platform), platform, mailmap
}

// scan records one scan of paths in db through app.Scan, with the adapters
// internal/cli builds: a gitcli.Runner with the mailmap, the packaged
// policy and a sqlite.Store, and the rank scope given (--scope). It returns
// the runner.
func scan(t *testing.T, db, mailmap, scope string, paths ...string) *gitcli.Runner {
	t.Helper()
	ctx := t.Context()
	git, err := gitcli.New(ctx, gitcli.Config{MailmapFile: mailmap})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Default()
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ranks := []domain.MetricID{{Name: "durable", Version: 1}, {Name: "simplifying", Version: 1}}
	// The store is both ports, as internal/cli hands it over: the scan reads
	// the GitHub data a sync left in the replica (seedSolo).
	_, err = app.Scan(ctx, app.Deps{Git: git, Store: store, ForgeStore: store, Policy: p, Version: "test"},
		app.ScanRequest{Paths: paths, Now: fixture.Now, Rank: ranks, Scope: scope})
	if cerr := store.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return git
}

// seedSolo gives the solo repository an origin, by writing its local
// config, and the replica in db its GitHub data, as a sync stores it: one
// pull request by SoloDev, merged as solo's second commit, which links the
// login SoloDev to Solo Dev, and approved by Zed, whom no commit of the
// run links to an email.
func seedSolo(t *testing.T, git *gitcli.Runner, db, solo string) {
	t.Helper()
	ctx := t.Context()
	repo := domain.ForgeRepo{ID: "R_solo", Key: "https://git.example.test/acme/solo.git", Owner: "acme", Name: "solo",
		DefaultBranch: "main"}
	f, err := os.OpenFile(filepath.Join(solo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[remote \"origin\"]\n\turl = " + repo.Key + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := git.Run(ctx, domain.GitCmd{Dir: solo, Args: []string{"rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	merged := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC) // the second commit's time
	commit := domain.ForgeCommit{SHA: strings.TrimSpace(string(out)), AuthorEmail: "solo@corp.example", AuthorLogin: "SoloDev"}
	page := domain.ForgePage{Repo: repo.ID, Updated: merged, PullRequests: []domain.PullRequest{{
		ID: "PR_s1", Repo: repo.ID, Number: 1, Title: "more", Author: domain.ForgeUser{Login: "SoloDev"},
		State: domain.PullMerged, BaseBranch: "main", Created: merged.Add(-48 * time.Hour), Updated: merged, Closed: merged,
		Merged: merged, MergeCommit: commit, Commits: []domain.ForgeCommit{commit},
		Reviews: []domain.Review{{ID: "PRR_s1", Author: domain.ForgeUser{Login: "Zed"}, State: domain.ReviewApproved,
			Submitted: merged.Add(-3 * time.Hour)}},
	}}}
	st, err := sqlite.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutForgeRepos(ctx, []domain.ForgeRepo{repo}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutForgePage(ctx, &page); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkForgeSynced(ctx, repo.ID, time.Date(2026, time.October, 6, 22, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// runEmails returns every email one run of db knows, as app.RunNames
// reads them from the run: what no answer may hold without --allow-emails.
func runEmails(t *testing.T, db string, run int64) []string {
	t.Helper()
	st, err := sqlite.OpenReadOnly(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	n, err := app.RunNames(t.Context(), app.Deps{Store: st}, app.NamesRequest{RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range n.People {
		if strings.Contains(p.Key, "@") && !strings.HasPrefix(p.Key, "@") {
			out = append(out, p.Key)
		}
	}
	return out
}

// testCommit is one commit of a repository buildRepo makes: one file.
type testCommit struct {
	name, email, day, subject, path, content string
}

// buildRepo makes a git repository in dir from commits, with git run as
// internal/fixture runs it: without the machine's global and system
// configuration, the installation's template, the per-user ignore and
// attribute files and any inherited GIT_ variable, so the history is the
// same on every machine. Each commit lands at noon UTC on its day.
func buildRepo(t *testing.T, dir string, commits []testCommit) {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1")
	git := func(who []string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git",
			append([]string{"-c", "core.excludesFile=/dev/null", "-c", "core.attributesFile=/dev/null"}, args...)...)
		cmd.Dir, cmd.Env = dir, append(slices.Clone(env), who...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git(nil, "init", "--quiet", "--template=", "--initial-branch=main", "--object-format=sha1")
	for _, c := range commits {
		when, err := time.Parse(time.DateOnly, c.day)
		if err != nil {
			t.Fatal(err)
		}
		date := strconv.FormatInt(when.Add(12*time.Hour).Unix(), 10) + " +0000"
		full := filepath.Join(dir, filepath.FromSlash(c.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		git(nil, "add", "--", c.path)
		git([]string{"GIT_AUTHOR_NAME=" + c.name, "GIT_AUTHOR_EMAIL=" + c.email, "GIT_AUTHOR_DATE=" + date,
			"GIT_COMMITTER_NAME=" + c.name, "GIT_COMMITTER_EMAIL=" + c.email, "GIT_COMMITTER_DATE=" + date},
			"commit", "--quiet", "--message", c.subject)
	}
}

// numbered returns n distinct non-blank lines, as the fixture's files are.
func numbered(name string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(name + "_" + strconv.Itoa(i) + " = " + strconv.Itoa(i) + "\n")
	}
	return b.String()
}

// openDB opens db as cli's mcp command does: read-only.
func openDB(db string) func(context.Context) (domain.FactStore, error) {
	return func(ctx context.Context) (domain.FactStore, error) {
		st, err := sqlite.OpenReadOnly(ctx, db)
		if err != nil {
			return nil, err
		}
		return st, nil
	}
}

// connect serves cfg on one end of an in-memory transport and returns an
// SDK client session on the other. A budget other than 0 replaces the
// server's answer budget.
func connect(t *testing.T, cfg Config, budget int) *sdk.ClientSession {
	t.Helper()
	ctx := t.Context()
	s := newServer(cfg)
	if budget != 0 {
		s.budget = budget
	}
	st, ct := sdk.NewInMemoryTransports()
	ss, err := s.mcpServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Close()
		s.close()
	})
	return cs
}

// callTool calls a tool and returns its text, and whether it is an error. An
// answer of either kind must fit the client's token cap.
func callTool(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s(%v): %v", tool, args, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s(%v) gave %d contents, want 1", tool, args, len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("%s(%v) gave %T, want text", tool, args, res.Content[0])
	}
	const capChars = 25000 * 4 // Claude Code's default cap, at 4 characters a token
	if n := len(text.Text); n >= capChars || n > answerBudget {
		t.Errorf("%s(%v) is %d characters, over the server's budget of %d or the %d-token cap", tool, args, n, answerBudget, capChars/4)
	}
	return text.Text, res.IsError
}

func decode(t *testing.T, text string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatalf("decoding %s: %v", text, err)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// names lists knowers by name.
func names(ks []report.Knower) string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Name)
	}
	return strings.Join(out, ", ")
}

// hasCaveat reports whether an answer's caveats hold one with code whose
// message holds text.
func hasCaveat(cs []report.Caveat, code, text string) bool {
	return slices.ContainsFunc(cs, func(c report.Caveat) bool { return c.Code == code && strings.Contains(c.Message, text) })
}

// countCode counts the caveats of code.
func countCode(cs []report.Caveat, code string) int {
	n := 0
	for _, c := range cs {
		if c.Code == code {
			n++
		}
	}
	return n
}

// withLines lists knowers by name and surviving lines under the path.
func withLines(ks []report.Knower) string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Name+" "+strconv.Itoa(k.Lines))
	}
	return strings.Join(out, ", ")
}
