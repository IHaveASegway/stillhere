package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The --now of SPEC's "A run reads each repository as it stood at --now",
// and the knowledge_loss@1 activity window it gives: 180 days back.
var (
	asOfNow      = time.Date(2026, time.March, 15, 0, 0, 0, 0, time.UTC)
	asOfActivity = window("2025-09-16T00:00:00Z", "2026-03-15T00:00:00Z")
)

// The warning codes of the snapshot step (#70), as report-json.md lists them.
const (
	codeHeadAfterNow      = "head-after-now"
	codeDatedAfterNow     = "dated-after-now"
	codeNoCommitBeforeNow = "no-commit-before-now"
)

// snapshotOracle checks SPEC's "A run reads each repository as it stood at
// --now" (#70) on the main fixture, scanned with --now 2026-03-15: the run
// reads row 7, the newest first-parent commit before then, records it as
// the repository's head and says so where the numbers are; its numbers are
// those of the repository as it stood then (knowledge_loss@1 0 / 18, where
// reading the fetched head gives 13 / 34); and, metamorphically, the same
// repository with its branch reset to that commit gives the same report in
// every format, apart from the head-after-now warning and caveats. At a
// --now before the root commit the repository yields no facts and a
// run-level warning that says why. On the backdated guard, whose commit
// read sits above a landing dated after --now, the run says that too.
func snapshotOracle(t *testing.T) {
	dir := fixture.Main(t)
	mailmap := fixture.Mailmap(t)
	req := scanRanked(dir)
	req.Now = asOfNow

	t.Run("no commit before --now", func(t *testing.T) {
		// Not parallel: the scans below reset the repository's branch.
		d := writeDeps(t, mailmap, nil)
		s, _ := openStore(t)
		d.Store, d.ForgeStore = s, s
		early := req
		early.Now = time.Date(2025, time.December, 1, 0, 0, 0, 0, time.UTC)
		m, err := app.Scan(t.Context(), d, early)
		if !errors.Is(err, domain.ErrNoFacts) {
			t.Fatalf("a scan at --now 2025-12-01 returned %v, want domain.ErrNoFacts: the branch has no commit before then", err)
		}
		if m == nil || m.Run != nil || len(m.Repos) != 0 {
			t.Fatalf("a scan at --now 2025-12-01 gave report %+v, want one with no run and no repository", m)
		}
		var found []report.Warning
		for _, w := range m.Warnings {
			if w.Code == codeNoCommitBeforeNow {
				found = append(found, w)
			}
		}
		if len(found) != 1 || !strings.Contains(found[0].Message, "only 16 landings at or after it") {
			t.Errorf("SPEC, a branch with no commit before --now: run warnings %+v, want one %s warning with 16 landings at or after --now",
				m.Warnings, codeNoCommitBeforeNow)
		}
	})

	const table = "SPEC, a run reads each repository as it stood at --now"
	read := scan(t, mailmap, nil, req)
	rf := onlyFacts(t, read)
	row7 := landingAt(t, rf, "2026-03-01T12:00:00Z")
	r := onlyRepo(t, read.model)
	heads := r.Card.Provenance.Heads
	if rf.Repo.Head != row7 || r.Head != row7 || len(heads) != 1 || heads[0].SHA != row7 {
		t.Errorf("%s: the run read and recorded %s (report %s, provenance %+v), want row 7's commit %s",
			table, rf.Repo.Head, r.Head, heads, row7)
	}
	saysSo := func(msg string) bool {
		return strings.Contains(msg, "commit "+row7[:12]) && strings.Contains(msg, "9 landings later")
	}
	if !slices.ContainsFunc(r.Warnings, func(w report.Warning) bool { return w.Code == codeHeadAfterNow && saysSo(w.Message) }) {
		t.Errorf("%s: the repository's warnings %+v hold no %s warning naming commit %s and 9 landings after --now",
			table, r.Warnings, codeHeadAfterNow, row7[:12])
	}
	for where, cs := range map[string][]report.Caveat{"the card": r.Card.Caveats, "the report": read.model.Caveats} {
		if !slices.ContainsFunc(cs, func(c report.Caveat) bool { return c.Code == codeHeadAfterNow && saysSo(c.Message) }) {
			t.Errorf("%s: %s's caveats %+v hold no %s caveat naming commit %s and 9 landings", table, where, cs, codeHeadAfterNow, row7[:12])
		}
	}

	// The numbers of the repository as it stood then.
	checkShare(t, table+", knowledge_loss@1", r.Card.KnowledgeLoss, 0, 18)
	e := read.explain(t, domain.MetricID{Name: "knowledge_loss", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
	if v, ok := repoValue(t, table, e, r.Key); ok {
		if v.Num != 0 || v.Den != 18 || v.Detail["people"] != 4 || v.Detail["people_inactive"] != 0 || !sameInterval(v.Window, asOfActivity) {
			t.Errorf("%s: %s, want 0 / 18 with people 4 and people_inactive 0, over [2025-09-16, 2026-03-15)", table, describe(v))
		}
	}
	type held struct{ Lines, Landings365d int }
	got := map[string]held{}
	if len(read.model.Overviews) == 1 && read.model.Overviews[0].People != nil {
		for _, p := range read.model.Overviews[0].People.Humans {
			got[p.Email] = held{p.ProdNonblankLines, p.Landings365d}
		}
	}
	sameRows(t, table+", surviving@1 and landings_365d", got, map[string]held{
		"ada@example.com":  {9, 3},
		"bob@example.com":  {5, 2},
		"gina@example.com": {2, 1},
		"hank@example.com": {2, 1},
	})

	// Metamorphic: the same repository cut back to the commit the run read.
	git := writeDeps(t, "", nil).Git
	if _, err := git.Run(t.Context(), domain.GitCmd{Dir: dir, Args: []string{"update-ref", "refs/heads/main", row7}}); err != nil {
		t.Fatalf("resetting main to row 7: %v", err)
	}
	cut := scan(t, mailmap, nil, req)
	if c := onlyRepo(t, cut.model); len(c.Warnings) != 0 {
		t.Errorf("%s: the repository cut back to row 7 has warnings %+v, want none: its head is before --now", table, c.Warnings)
	}
	a := render(t, withoutTimings(withoutCode(read.model, codeHeadAfterNow)))
	b := render(t, withoutTimings(cut.model))
	sameBytes(t, table+": the JSON of the fetched head and of the repository cut back", a.json, b.json)
	sameBytes(t, table+": the text of the fetched head and of the repository cut back", a.text, b.text)
	sameBytes(t, table+": the Markdown of the fetched head and of the repository cut back", a.markdown, b.markdown)

	t.Run("landings dated after --now below the commit read", func(t *testing.T) {
		const table = "SPEC, landings dated after --now below the commit read"
		dir := fixture.Backdated(t)
		req := scanRanked(dir)
		req.Now = time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
		s := scan(t, "", nil, req)
		rf := onlyFacts(t, s)
		r := onlyRepo(t, s.model)
		// X is main~2: A2's first parent is A1, and A1's is X.
		out, err := writeDeps(t, "", nil).Git.Run(t.Context(), domain.GitCmd{Dir: dir, Args: []string{"rev-parse", "--verify", "main~2^{commit}"}})
		if err != nil {
			t.Fatalf("resolving X: %v", err)
		}
		x := strings.TrimSpace(string(out))
		if rf.Repo.Head != x || r.Head != x {
			t.Errorf("%s: the run read %s (report %s), want X, %s", table, rf.Repo.Head, r.Head, x)
		}
		says := map[string]func(string) bool{
			codeHeadAfterNow: func(msg string) bool {
				return strings.Contains(msg, "commit "+x[:12]) && strings.Contains(msg, "2 landings later")
			},
			codeDatedAfterNow: func(msg string) bool {
				return strings.HasPrefix(msg, "1 landing on main's first-parent chain below commit "+x[:12]+",")
			},
		}
		for code, ok := range says {
			if !slices.ContainsFunc(r.Warnings, func(w report.Warning) bool { return w.Code == code && ok(w.Message) }) {
				t.Errorf("%s: the repository's warnings %+v hold no %s warning as SPEC words it", table, r.Warnings, code)
			}
			for where, cs := range map[string][]report.Caveat{"the card": r.Card.Caveats, "the report": s.model.Caveats} {
				if !slices.ContainsFunc(cs, func(c report.Caveat) bool { return c.Code == code && ok(c.Message) }) {
					t.Errorf("%s: %s's caveats %+v hold no %s caveat as SPEC words it", table, where, cs, code)
				}
			}
		}
		// The numbers of the commit read, which the warnings explain: Cal's
		// lines are B2's, dated after --now, so no window holds that landing.
		checkShare(t, table+", knowledge_loss@1", r.Card.KnowledgeLoss, 3, 7)
		e := s.explain(t, domain.MetricID{Name: "knowledge_loss", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
		if v, ok := repoValue(t, table, e, r.Key); ok && (v.Detail["people"] != 3 || v.Detail["people_inactive"] != 2) {
			t.Errorf("%s: %s, want people 3 and people_inactive 2", table, describe(v))
		}
	})
}

// withoutCode returns a copy of m without the warnings and caveats that
// carry code, in every section that holds them.
func withoutCode(m *report.Model, code string) *report.Model {
	caveats := func(cs []report.Caveat) []report.Caveat {
		return slices.DeleteFunc(slices.Clone(cs), func(c report.Caveat) bool { return c.Code == code })
	}
	warnings := func(ws []report.Warning) []report.Warning {
		return slices.DeleteFunc(slices.Clone(ws), func(w report.Warning) bool { return w.Code == code })
	}
	c := *m
	c.Warnings = warnings(m.Warnings)
	c.Caveats = caveats(m.Caveats)
	c.Overviews = slices.Clone(m.Overviews)
	for i := range c.Overviews {
		c.Overviews[i].Caveats = caveats(c.Overviews[i].Caveats)
	}
	c.Ranks = slices.Clone(m.Ranks)
	for i := range c.Ranks {
		c.Ranks[i].Caveats = caveats(c.Ranks[i].Caveats)
	}
	c.Repos = slices.Clone(m.Repos)
	for i := range c.Repos {
		c.Repos[i].Warnings = warnings(c.Repos[i].Warnings)
		c.Repos[i].Card.Caveats = caveats(c.Repos[i].Card.Caveats)
	}
	return &c
}
