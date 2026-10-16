package app_test

import (
	"testing"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// weekRow is one row of SPEC's week table: landings, commits, merged,
// lines_added and prod_lines_added (-1 when null: unknown).
type weekRow struct {
	Name                                                  string
	Landings, Commits, Merged, LinesAdded, ProdLinesAdded int
}

// The week table of testdata/fixture/SPEC.md ("Week oracle"), by email:
// the people, then the bot table, as landed@2 credits them (#108): Cara
// wrote the branch Bob merged, so the merge is her landing with both of her
// commits' lines, and Bob's row counts the one landing he wrote and the
// merge. Gina and Hank have no row. The window's totals are each landing
// once (weekTotal).
var (
	weekHumans = map[string]weekRow{
		"cara@example.com":  {"Cara Dev", 1, 2, 0, 9, 9},
		"bob@example.com":   {"Bob Builder", 1, 1, 1, 2, 2},
		"ivy@example.com":   {"Ivy Tester", 1, 1, 0, 4, 0},
		"frank@example.com": {"Frank & Sons", 1, 1, 0, 3, 3},
		"ada@example.com":   {"Ada Example", 1, 1, 0, 1, 1},
		"dana@example.com":  {"Dana Dev", 1, 1, 0, 1, 1},
		"erin@example.com":  {"Erin Editor", 1, 1, 0, 1, 1},
		"jules@example.com": {"Jules Note", 1, 1, 0, 0, 0},
	}
	weekBots = map[string]weekRow{
		"copilot@bot.example": {"GitHub Copilot", 1, 1, 0, 3, 3},
	}
	weekTotal = weekRow{"", 9, 10, 1, 24, 20}
)

// TestWeekOracle checks the week oracle of testdata/fixture/SPEC.md through
// app.Week on the main fixture at --now 2026-10-07 with the packaged policy:
// the week table, people and bots apart, in the repository's table and in
// the overview of every repository; and the --days 1 window against the
// policy's [week] days = 7, as the report states it and as the run collected
// it. SPEC asserts the set of rows: their display order is not a rank.
func TestWeekOracle(t *testing.T) {
	t.Parallel()
	repo := fixture.Main(t)
	d := writeDeps(t, fixture.Mailmap(t), nil)
	if d.Policy.Week.Days != 7 {
		t.Fatalf("the packaged policy's [week] days is %d: SPEC's --days 1 oracle is against 7", d.Policy.Week.Days)
	}

	t.Run("week table", func(t *testing.T) {
		t.Parallel()
		m, err := app.Week(t.Context(), d, app.WeekRequest{Paths: []string{repo}, Now: fixture.Now})
		if err != nil {
			t.Fatalf("app.Week: %v", err)
		}
		// SPEC Setup: --now 2026-10-07T00:00:00Z, so the default week starts
		// at 2026-09-30T00:00:00Z.
		checkWindow(t, "SPEC Setup, the default week", m, 7, "policy",
			window("2026-09-30T00:00:00Z", "2026-10-07T00:00:00Z"))
		r := onlyRepo(t, m)
		checkWeekTable(t, "repository "+r.Name, r.Week)
		if len(m.Overviews) != 1 {
			t.Fatalf("a week report of one repository has %d overviews, want 1, of every repository", len(m.Overviews))
		}
		checkWeekTable(t, "the overview of every repository", m.Overviews[0].Week)
	})

	t.Run("--days 1", func(t *testing.T) {
		t.Parallel()
		m, err := app.Week(t.Context(), d, app.WeekRequest{Paths: []string{repo}, Now: fixture.Now, Days: 1, DaysFromCLI: true})
		if err != nil {
			t.Fatalf("app.Week: %v", err)
		}
		checkWindow(t, "SPEC week oracle, --days 1 against [week] days = 7", m, 1, "cli",
			window("2026-10-06T00:00:00Z", "2026-10-07T00:00:00Z"))
		// Every week landing is on 2026-10-06, so the rows do not move.
		checkWeekTable(t, "repository "+onlyRepo(t, m).Name+", --days 1", onlyRepo(t, m).Week)
	})
}

// checkWindow compares the landed window a week report records with SPEC,
// and the window the run collected with it. report.Build derives the landed
// window from the days and --now alone, so only history, which a week
// report records as the window it collected and which equals its window
// (docs/spec/report-json.md), shows that app.Week collected that window.
func checkWindow(t *testing.T, oracle string, m *report.Model, days int, from string, want report.Interval) {
	t.Helper()
	w := m.Window
	if w.Days != days || w.DaysFrom != from {
		t.Errorf("%s: window days %d from %q, want %d from %q", oracle, w.Days, w.DaysFrom, days, from)
	}
	if got := (report.Interval{Since: w.Since, Until: w.Until}); !sameInterval(got, want) {
		t.Errorf("%s: window [%s, %s), want [%s, %s)", oracle,
			rfc3339(w.Since), rfc3339(w.Until), rfc3339(want.Since), rfc3339(want.Until))
	}
	if !sameInterval(m.History, want) {
		t.Errorf("%s: history, the window the run collected, is [%s, %s), want [%s, %s): a week report's history equals its window",
			oracle, rfc3339(m.History.Since), rfc3339(m.History.Until), rfc3339(want.Since), rfc3339(want.Until))
	}
}

// checkWeekTable compares a week table with SPEC's: every person and bot
// row, in its own list.
func checkWeekTable(t *testing.T, where string, wt *report.WeekTable) {
	t.Helper()
	if wt == nil {
		t.Errorf("SPEC week oracle: %s has no week table", where)
		return
	}
	lines := func(n *int) int {
		if n == nil {
			return -1
		}
		return *n
	}
	rows := func(list string, rs []report.WeekRow, bot bool) map[string]weekRow {
		out := map[string]weekRow{}
		for _, r := range rs {
			if r.Bot != bot {
				t.Errorf("SPEC week oracle: %s lists %s among the %s with bot %t", where, r.Email, list, r.Bot)
			}
			out[r.Email] = weekRow{r.Name, r.Landings, r.Commits, r.Merged, lines(r.LinesAdded), lines(r.ProdLinesAdded)}
		}
		return out
	}
	sameRows(t, "SPEC week oracle, people ("+where+")", rows("people", wt.Humans, false), weekHumans)
	sameRows(t, "SPEC week oracle, bot table ("+where+")", rows("bots", wt.Bots, true), weekBots)
	if tt := wt.Total; tt == nil {
		t.Errorf("SPEC week oracle: %s has no totals", where)
	} else if got := (weekRow{"", tt.Landings, tt.Commits, tt.Merged, lines(tt.LinesAdded), lines(tt.ProdLinesAdded)}); got != weekTotal {
		t.Errorf("SPEC week oracle: %s totals %+v, want %+v", where, got, weekTotal)
	}
}
