package app_test

import (
	"testing"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// stillRow is one row of SPEC's still-here table.
type stillRow struct {
	Name                             string
	ProdNonblankLines, MajorityFiles int
}

// The still-here table of testdata/fixture/SPEC.md ("Still-here oracle"),
// by email: the people, then the bot.
var (
	stillHumans = map[string]stillRow{
		"ada@example.com":   {"Ada Example", 10, 3},
		"cara@example.com":  {"Cara Dev", 9, 2},
		"bob@example.com":   {"Bob Builder", 7, 1},
		"frank@example.com": {"Frank & Sons", 2, 1},
		"gina@example.com":  {"Gina Split", 2, 1},
		"hank@example.com":  {"Hank Split", 2, 1},
		"dana@example.com":  {"Dana Dev", 1, 1},
		"erin@example.com":  {"Erin Editor", 1, 0},
	}
	stillBots = map[string]stillRow{
		"copilot@bot.example": {"GitHub Copilot", 3, 1},
	}
)

// TestStillHereOracle checks the still-here oracle of
// testdata/fixture/SPEC.md through app.Scan on the main fixture: whose prod
// lines blame still attributes at HEAD and the files each person holds most
// of, people and bots apart, in the repository's table and in the overview
// of every repository; and the concentration walk on the repository's card.
func TestStillHereOracle(t *testing.T) {
	t.Parallel()
	s := scan(t, fixture.Mailmap(t), nil, app.ScanRequest{Paths: []string{fixture.Main(t)}, Now: fixture.Now})
	r := onlyRepo(t, s.model)
	checkStillHere(t, "repository "+r.Name, r.StillHere)
	if len(s.model.Overviews) != 1 {
		t.Fatalf("a scan of one repository has %d overviews, want 1, of every repository", len(s.model.Overviews))
	}
	checkStillHere(t, "the overview of every repository", s.model.Overviews[0].People)

	// SPEC, "The concentration walk": 10 files have a majority owner, and Ada
	// (3) and Cara (2) reach half of them.
	c := r.Card.Concentration
	switch {
	case c == nil:
		t.Error("SPEC still-here oracle, the concentration walk: the card has no concentration")
	case c.FilesWithMajority != 10 || c.AuthorsInPrefix != 2 || c.BotsInPrefix != 0:
		t.Errorf("SPEC still-here oracle, the concentration walk: files_with_majority %d, authors_in_prefix %d, bots_in_prefix %d; want 10, 2 and 0",
			c.FilesWithMajority, c.AuthorsInPrefix, c.BotsInPrefix)
	}
}

// checkStillHere compares a still-here table with SPEC's: every person and
// bot row, in its own list.
func checkStillHere(t *testing.T, where string, sh *report.StillHere) {
	t.Helper()
	if sh == nil {
		t.Errorf("SPEC still-here oracle: %s has no still-here table", where)
		return
	}
	rows := func(list string, rs []report.StillHereRow, bot bool) map[string]stillRow {
		out := map[string]stillRow{}
		for _, r := range rs {
			if r.Bot != bot {
				t.Errorf("SPEC still-here oracle: %s lists %s among the %s with bot %t", where, r.Email, list, r.Bot)
			}
			out[r.Email] = stillRow{r.Name, r.ProdNonblankLines, r.MajorityFiles}
		}
		return out
	}
	sameRows(t, "SPEC still-here oracle, people ("+where+")", rows("people", sh.Humans, false), stillHumans)
	sameRows(t, "SPEC still-here oracle, bots ("+where+")", rows("bots", sh.Bots, true), stillBots)
}
