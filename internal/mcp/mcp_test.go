package mcp

import (
	"context"
	"encoding/json/v2"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

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
// of both fixtures as one platform (testdata/fixture/SPEC.md). It checks
// the oracle numbers an answer shows, that names resolve as the reports
// print them and an ambiguous one lists its candidates, and the privacy
// rules: no email without --allow-emails, no commit subject or diff
// without --allow-source, and the AI-assisted share only on the --me
// person's own page. Every answer must fit Claude Code's 25,000-token cap
// at 4 characters a token.
func TestMCPTools(t *testing.T) {
	ctx := t.Context()
	db, git := scanPlatform(t)
	// The default server: no emails, no source, and the user is Lena.
	cs := connect(t, Config{Open: openDB(db), Me: "lena@example.com", Version: "test"})
	ask := func(t *testing.T, tool string, args map[string]any) string {
		t.Helper()
		text, isErr := callTool(t, cs, tool, args)
		if isErr {
			t.Fatalf("%s(%v) failed: %s", tool, args, text)
		}
		if loc := emailPattern.FindStringIndex(text); loc != nil {
			t.Errorf("%s(%v) holds an email without --allow-emails: %q", tool, args, text[loc[0]:loc[1]])
		}
		return text
	}
	refused := func(t *testing.T, tool string, args map[string]any) string {
		t.Helper()
		text, isErr := callTool(t, cs, tool, args)
		if !isErr {
			t.Fatalf("%s(%v) answered, want an error: %s", tool, args, text)
		}
		if emailPattern.MatchString(text) {
			t.Errorf("%s(%v)'s error holds an email: %s", tool, args, text)
		}
		return text
	}

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
		decode(t, ask(t, "overview", map[string]any{"limit": 1}), &run)
		if len(run.Overviews) != 1 || run.Overviews[0].Scope != (report.Scope{Kind: "group", Key: "platform"}) ||
			run.Overviews[0].Totals.Repos != 2 {
			t.Errorf("overviews %+v, want the platform group with its 2 repositories", run.Overviews)
		}
		if len(run.Repos) != 1 || run.Repos[0].Name != "fixture" || run.Page.Total != 2 || run.Page.NextCursor == nil {
			t.Fatalf("first page %+v of %+v, want fixture and a cursor to the second of 2", run.Repos, run.Page)
		}
		if !slices.Equal(run.Withheld, []string{"emails", "source"}) {
			t.Errorf("withheld %v, want emails and source", run.Withheld)
		}
		decode(t, ask(t, "overview", map[string]any{"limit": 1, "cursor": *run.Page.NextCursor}), &run)
		if len(run.Repos) != 1 || run.Repos[0].Name != "sibling" || run.Page.NextCursor != nil {
			t.Errorf("second page %+v of %+v, want sibling and no cursor", run.Repos, run.Page)
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
		if !mapsEqual(got, want) {
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
		if repo.Provenance.RunID != 1 || len(repo.Provenance.Heads) != 1 || len(repo.Provenance.Heads[0].SHA) != 40 ||
			!slices.Contains(repo.Provenance.Metrics, "surviving@1") {
			t.Errorf("fixture provenance %+v, want run 1, its HEAD and the metric versions", repo.Provenance)
		}

		// The sibling's surviving@1 credit, and the platform's durable@1
		// table, where Lena alone is eligible at exactly 10 clusters.
		decode(t, ask(t, "overview", map[string]any{"scope": "repo:sibling"}), &repo)
		got = map[string][2]int{}
		for _, row := range repo.Repos[0].StillHere.Humans {
			got[row.Name] = [2]int{row.ProdNonblankLines}
		}
		if want := map[string][2]int{"Lena Lead": {37}, "Omar Ops": {36}, "Quinn Squash": {31}, "Pia Pair": {9}, "Uma Legacy": {7}}; !mapsEqual(got, want) {
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
		if strings.HasPrefix(lena.Email, "p-") == false {
			t.Errorf("Lena's key %q, want an id in place of her email", lena.Email)
		}

		text := refused(t, "overview", map[string]any{"scope": "nowhere"})
		if !strings.Contains(text, "platform") || !strings.Contains(text, "sibling") {
			t.Errorf("an unknown scope's error does not list the run's scopes: %s", text)
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
		if text := refused(t, "person", map[string]any{"name": "Split"}); !strings.Contains(text, "Gina Split") ||
			!strings.Contains(text, "Hank Split") || !strings.Contains(text, "id p-") {
			t.Errorf("an ambiguous name's error does not list both candidates with their ids: %s", text)
		}
		if text := refused(t, "person", map[string]any{"name": "Lenna Lead"}); !strings.Contains(text, "Lena Lead") {
			t.Errorf("a misspelt name's error does not suggest Lena Lead: %s", text)
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
	})

	t.Run("highlights", func(t *testing.T) {
		var h highlightsOut
		decode(t, ask(t, "highlights", map[string]any{"period": "quarter"}), &h)
		tiers := map[string]string{}
		for _, tt := range h.TopTier {
			var ns []string
			for _, p := range tt.People {
				ns = append(ns, p.Name)
			}
			tiers[tt.Metric] = strings.Join(ns, ", ")
		}
		if tiers["durable@1"] != "Lena Lead" || tiers["simplifying@1"] != "Lena Lead" {
			t.Errorf("top tiers %v, want Lena Lead alone for durable@1 and simplifying@1", tiers)
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
		if emailPattern.MatchString(text) {
			t.Errorf("identity_candidates holds an email without --allow-emails: %s", text)
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

	// A server the user lets show emails and source: the diff tool runs
	// git, and subjects and emails reach the agent.
	t.Run("allow source and emails", func(t *testing.T) {
		ss := connect(t, Config{Open: openDB(db), Git: git, AllowSource: true, AllowEmails: true, Version: "test"})
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
}

// scanPlatform builds both fixtures in one directory, a platform of two
// repositories, and records one scan of it in a new facts.db through
// app.Scan, with the adapters internal/cli builds: a gitcli.Runner with
// both fixtures' mailmap lines, the packaged policy and a sqlite.Store. It
// returns the database and the runner.
func scanPlatform(t *testing.T) (string, *gitcli.Runner) {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	platform := filepath.Join(root, "platform")
	if err := fixture.BuildContext(ctx, filepath.Join(platform, "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.BuildSiblingContext(ctx, filepath.Join(platform, "sibling")); err != nil {
		t.Fatal(err)
	}
	mailmap := filepath.Join(root, "mailmap")
	if err := os.WriteFile(mailmap, []byte(fixture.MailmapText+fixture.SiblingMailmapText), 0o600); err != nil {
		t.Fatal(err)
	}
	git, err := gitcli.New(ctx, gitcli.Config{MailmapFile: mailmap})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Default()
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(root, "facts.db")
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
	return db, git
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
// SDK client session on the other.
func connect(t *testing.T, cfg Config) *sdk.ClientSession {
	t.Helper()
	ctx := t.Context()
	s := newServer(cfg)
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

func mapsEqual(a, b map[string][2]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

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
