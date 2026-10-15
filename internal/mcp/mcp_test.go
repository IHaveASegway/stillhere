package mcp

import (
	"bufio"
	"context"
	"encoding/json/v2"
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
				Scope  report.Scope  `json:"scope"`
				Totals report.Totals `json:"totals"`
			} `json:"overviews"`
			Repos []struct {
				Name string `json:"name"`
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
			Repos []report.Repo `json:"repos"`
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "fixture"}), &repo)
		if len(repo.Repos) != 1 || repo.Repos[0].StillHere == nil {
			t.Fatalf("overview(fixture) gave %d repositories, want the fixture's card and tables", len(repo.Repos))
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
		if lena.Name != "Lena Lead" || lena.RankLo != 1 || lena.RankHi != 1 || lena.N != 10 || !near(lena.Estimate, 26.0/30) {
			t.Errorf("platform durable@1 rank %+v, want Lena Lead 1-1, 26/30, n 10", lena)
		}
		if lena.Email != pseudonym("lena@example.com") {
			t.Errorf("Lena's key %q, want her id in place of her email", lena.Email)
		}

		text := refused(t, "overview", map[string]any{"scope": "nowhere"})
		if !strings.Contains(text, `"platform"`) || !strings.Contains(text, `"sibling"`) {
			t.Errorf("an unknown scope's error does not list the run's scopes, quoted: %s", text)
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
		if durable == nil || !durable.Eligible || durable.RankLo != 1 || durable.RankHi != 1 || durable.N != 10 ||
			!near(durable.Estimate, 26.0/30) {
			t.Errorf("Lena's durable@1 rank %+v, want eligible at 1-1, 26/30, n 10", durable)
		}
		if !slices.ContainsFunc(p.Values, func(v valueOut) bool { return v.Metric == "ai_assisted@1" }) {
			t.Error("Lena's own page lacks her own AI-assisted share")
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
	})

	t.Run("changes", func(t *testing.T) {
		var c struct {
			Changes []changeBrief `json:"changes"`
			Page    page          `json:"page"`
		}
		decode(t, ask(t, "changes", map[string]any{"scope": "sibling", "since": "2026-09-01"}), &c)
		if len(c.Changes) != 3 {
			t.Fatalf("sibling changes since 2026-09-01: %d, want W9, W8 and S1", len(c.Changes))
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
		refused(t, "explain", map[string]any{"metric": "ai_assisted", "subject": "Omar Ops"})
		decode(t, ask(t, "explain", map[string]any{"metric": "knowledge_loss", "subject": "fixture", "subject_kind": "repo"}), &e)
		if len(e.Values) != 1 || e.Values[0].Num != 4 || e.Values[0].Den != 34 {
			t.Errorf("explain(knowledge_loss, fixture) %+v, want 4 of 34", e.Values)
		}
		// A repository of many people shows its AI-assisted share.
		decode(t, ask(t, "explain", map[string]any{"metric": "ai_assisted", "subject": "sibling", "subject_kind": "repo"}), &e)
		if len(e.Values) != 1 {
			t.Errorf("explain(ai_assisted, sibling) %+v, want the sibling's share", e.Values)
		}
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
		if isErr && strings.Contains(text, domain.ErrNotImplemented.Error()) {
			t.Skip("#26")
		}
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
		text, isErr := callTool(t, small, "overview", map[string]any{"scope": "platform"})
		if isErr || len(text) > budget {
			t.Fatalf("overview(platform) under a %d-character budget: %d characters, error %v: %s", budget, len(text), isErr, text)
		}
		decode(t, text, &o)
		if o.Page.Limit >= overviewLimit || len(o.Repos) != 1 || o.Repos[0].Name != "fixture" || o.Page.NextCursor == nil {
			t.Fatalf("overview(platform) under the budget: %d repositories, page %+v; want fixture alone on a halved page with a cursor",
				len(o.Repos), o.Page)
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
		_ = inW.Close()
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
	})
	buildRepo(t, solo, []testCommit{
		{"Solo Dev", "solo@corp.example", "2026-05-02", "init", "main.py", numbered("solo", 10)},
		{"Solo Dev", "solo@corp.example", "2026-08-02", "feat: more", "more.py", numbered("more", 10)},
	})
	scan(t, db, mailmap, platform)
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
		// id is the one person resolves.
		var repo struct {
			Repos []report.Repo `json:"repos"`
		}
		decode(t, ask(t, "overview", map[string]any{"scope": "odd"}), &repo)
		if len(repo.Repos) != 1 || len(repo.Repos[0].Card.MadeOf) == 0 {
			t.Fatalf("overview(odd) %+v, want its card", repo.Repos)
		}
		for _, a := range repo.Repos[0].Card.MadeOf {
			var p personOut
			decode(t, ask(t, "person", map[string]any{"name": a.Email}), &p)
			if p.Person.Name != a.Name || p.Person.Key != a.Email {
				t.Errorf("person(%s), the id overview gave %s, is %+v", a.Email, a.Name, p.Person)
			}
		}
		for _, args := range []map[string]any{
			{"scope": "odd", "response_format": "detailed"}, {"scope": "platform", "rank": "durable"},
		} {
			ask(t, "overview", args)
		}
		for _, name := range []string{"dev", "Ip Literal", "Jürgen Müller", "Root", "Pat O'Brien"} {
			ask(t, "person", map[string]any{"name": name, "response_format": "detailed"})
			ask(t, "explain", map[string]any{"metric": "surviving", "subject": name, "response_format": "detailed"})
		}
		ask(t, "changes", map[string]any{"scope": "odd", "since": "365d", "response_format": "detailed"})
		ask(t, "highlights", map[string]any{"period": "quarter", "response_format": "detailed"})
		ask(t, "who_knows", map[string]any{"path": "src", "repo": "odd", "response_format": "detailed"})
		if text, isErr := callTool(t, cs, "identity_candidates", nil); !isErr || !strings.Contains(text, domain.ErrNotImplemented.Error()) {
			// Once #26 lands: its addresses are emails too.
			if isErr {
				t.Errorf("identity_candidates failed: %s", text)
			}
			if e := leak(text); e != "" {
				t.Errorf("identity_candidates holds %q without --allow-emails: %s", e, text)
			}
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
	return db, scan(t, db, mailmap, platform), platform, mailmap
}

// scan records one scan of platform in db through app.Scan, with the
// adapters internal/cli builds: a gitcli.Runner with the mailmap, the
// packaged policy and a sqlite.Store. It returns the runner.
func scan(t *testing.T, db, mailmap, platform string) *gitcli.Runner {
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
	_, err = app.Scan(ctx, app.Deps{Git: git, Store: store, Policy: p, Version: "test"},
		app.ScanRequest{Paths: []string{platform}, Now: fixture.Now, Rank: ranks, Scope: domain.ScopeGroup})
	if cerr := store.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return git
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

// withLines lists knowers by name and surviving lines under the path.
func withLines(ks []report.Knower) string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Name+" "+strconv.Itoa(k.Lines))
	}
	return strings.Join(out, ", ")
}
