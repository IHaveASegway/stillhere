package app_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The windows of the change oracles at --now 2026-10-07 (SPEC, "Change
// oracles"): half-open, from and to 00:00:00Z.
var (
	cohort     = window("2025-10-07T00:00:00Z", "2026-07-09T00:00:00Z") // durable@1
	recent     = window("2026-07-09T00:00:00Z", "2026-10-07T00:00:00Z") // simplifying@1 and who_knows@1's recent landers
	activity   = window("2026-04-10T00:00:00Z", "2026-10-07T00:00:00Z") // knowledge_loss@1
	scanWindow = window("2025-10-07T00:00:00Z", "2026-10-07T00:00:00Z") // the 365 days a scan collects, ai_assisted@1's
)

// durableRow is one person's row of a SPEC durable@1 table.
type durableRow struct {
	Name             string
	Surviving, Added float64 // Σ surviving / Σ added
	N                int     // clusters
	Reverted         int     // Detail reverted_landings: clusters in reverted landings
	Ranked           bool    // eligible: at least 10 clusters
}

// simplifyingRow is one person's or bot's row of a SPEC simplifying@1 table.
type simplifyingRow struct {
	Name        string
	Removed     float64
	Simplifying int // Detail simplifying_landings
	N           int // clusters
	Ranked, Bot bool
}

// cluster is one change behind a person's ranked value, as explain lists
// it by landing time: durable@1's surviving and added lines, or
// simplifying@1's removed lines (Den 0: a sum).
type cluster struct{ Num, Den float64 }

// TestChangeOracles checks the change oracles of testdata/fixture/SPEC.md
// through app.Scan and the read use cases: durable@1 and simplifying@1 per
// person and per landing, with their rank tables, on both fixtures; the
// revert links and their effect; AI-assisted tags per landing and the
// shares; knowledge_loss@1 and who_knows@1 on the main fixture; surviving@1
// credit on the sibling; the three regression guards (a backdated
// first-parent commit, a revert merged with --no-ff, and changes git takes
// for binary), on repositories of their own; and the identity candidates of
// each fixture, without its mailmap and with it. Every fixture is scanned
// alone.
func TestChangeOracles(t *testing.T) {
	t.Parallel()
	t.Run("main fixture", func(t *testing.T) {
		t.Parallel()
		mainChangeOracles(t)
	})
	t.Run("sibling", func(t *testing.T) {
		t.Parallel()
		siblingChangeOracles(t)
	})
	t.Run("backdated first-parent commit", func(t *testing.T) {
		t.Parallel()
		backdatedGuard(t)
	})
	t.Run("revert merged with --no-ff", func(t *testing.T) {
		t.Parallel()
		mergedRevertGuard(t)
	})
	t.Run("binary files", func(t *testing.T) { t.Parallel(); binaryGuard(t) })
	t.Run("identity candidates", func(t *testing.T) {
		t.Parallel()
		identityOracles(t)
	})
	t.Run("reviews", func(t *testing.T) { t.Parallel(); reviewOracles(t) })
}

// mainChangeOracles checks SPEC's change oracles on the main fixture.
func mainChangeOracles(t *testing.T) {
	s := scan(t, fixture.Mailmap(t), nil, scanRanked(fixture.Main(t)))
	r := onlyRepo(t, s.model)

	t.Run("durable@1", func(t *testing.T) {
		// SPEC, "durable@1 (main fixture)": no one else has a value.
		checkDurable(t, s, r.Key, "SPEC durable@1 (main fixture)", map[string]durableRow{
			"ada@example.com":  {"Ada Example", 9, 9, 2, 0, false},
			"bob@example.com":  {"Bob Builder", 5, 7, 2, 0, false},
			"gina@example.com": {"Gina Split", 2, 2, 1, 0, false},
			"hank@example.com": {"Hank Split", 2, 2, 1, 0, false},
		}, map[string]map[string]cluster{
			// SPEC, "Change facts per landing (main fixture)", rows 1 to 7:
			// surviving and added per landing. Row 4, Ada's git mv, adds
			// nothing and is no cluster.
			"ada@example.com":  {"2026-01-01T12:00:00Z": {7, 7}, "2026-01-02T12:00:00Z": {2, 2}},
			"bob@example.com":  {"2026-01-04T12:00:00Z": {0, 2}, "2026-03-01T12:00:00Z": {5, 5}},
			"gina@example.com": {"2026-01-06T12:00:00Z": {2, 2}},
			"hank@example.com": {"2026-01-07T12:00:00Z": {2, 2}},
		})
	})

	t.Run("simplifying@1", func(t *testing.T) {
		checkSimplifying(t, s, r.Key, "SPEC simplifying@1 (main fixture)", map[string]simplifyingRow{
			"ada@example.com":     {"Ada Example", 0, 0, 1, false, false},
			"bob@example.com":     {"Bob Builder", 0, 0, 1, false, false},
			"cara@example.com":    {"Cara Dev", 0, 0, 1, false, false},
			"dana@example.com":    {"Dana Dev", 0, 0, 1, false, false},
			"erin@example.com":    {"Erin Editor", 0, 0, 1, false, false},
			"frank@example.com":   {"Frank & Sons", 0, 0, 1, false, false},
			"ivy@example.com":     {"Ivy Tester", 0, 0, 1, false, false},
			"jules@example.com":   {"Jules Note", 0, 0, 1, false, false},
			"copilot@bot.example": {"GitHub Copilot", 0, 0, 1, false, true},
		}, map[string]map[string]cluster{
			// SPEC, "Change facts per landing (main fixture)", rows 8 to 16:
			// Cara's commits in Bob's merge (row 8), and Bob's own row 9.
			"cara@example.com":  {"2026-10-06T12:00:00Z": {}},
			"bob@example.com":   {"2026-10-06T13:00:00Z": {}},
			"dana@example.com":  {"2026-10-06T15:00:00Z": {}},
			"erin@example.com":  {"2026-10-06T16:00:00Z": {}},
			"frank@example.com": {"2026-10-06T17:00:00Z": {}},
			"ada@example.com":   {"2026-10-06T18:00:00Z": {}},
			"ivy@example.com":   {"2026-10-06T19:00:00Z": {}},
			"jules@example.com": {"2026-10-06T20:00:00Z": {}},
		})
	})

	t.Run("knowledge_loss@1", func(t *testing.T) {
		// SPEC, "knowledge_loss@1 (main fixture)": Gina's 2 and Hank's 2 of
		// the 34 lines people hold.
		checkShare(t, "SPEC knowledge_loss@1 (main fixture)", r.Card.KnowledgeLoss, 4, 34)
		e := s.explain(t, domain.MetricID{Name: "knowledge_loss", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
		if v, ok := repoValue(t, "SPEC knowledge_loss@1 (main fixture)", e, r.Key); ok {
			if v.Num != 4 || v.Den != 34 || !sameInterval(v.Window, activity) {
				t.Errorf("SPEC knowledge_loss@1 (main fixture): %s over [%s, %s), want 4 / 34 over the activity window [2026-04-10, 2026-10-07)",
					describe(v), rfc3339(v.Window.Since), rfc3339(v.Window.Until))
			}
		}
	})

	t.Run("who_knows@1", func(t *testing.T) {
		// SPEC, "who_knows@1 (main fixture)".
		for _, c := range []whoKnowsCase{
			{path: "service.py", lines: 8, owners: []string{"ada@example.com"},
				top: []knows{{"ada@example.com", 6}, {"bob@example.com", 2}}, landers: []string{"bob@example.com"}},
			{path: "split.py", lines: 4, owners: []string{"gina@example.com", "hank@example.com"},
				top: []knows{{"gina@example.com", 2}, {"hank@example.com", 2}}},
			{path: "side.py", lines: 2, owners: []string{"cara@example.com"},
				top: []knows{{"cara@example.com", 2}}, landers: []string{"cara@example.com"}},
		} {
			checkWhoKnows(t, "SPEC who_knows@1 (main fixture)", s, r.Key, c)
		}
	})

	t.Run("who-knows of a path no repository has", func(t *testing.T) {
		// A filesystem path, such as the fixture's path from its group, names
		// nothing in a repository: the answer is missing, with a no-values
		// caveat that says which form of path who-knows takes, never
		// "nobody" (#71). notes.csv is a path the run knows, which no one
		// holds lines of or landed lately: that one is a real "nobody".
		for _, c := range []struct {
			path    string
			missing bool
		}{{"fixture/service.py", true}, {"notes.csv", false}} {
			w := s.whoKnows(t, c.path)
			missing := slices.ContainsFunc(w.Caveats, func(cv report.Caveat) bool {
				return cv.Code == "no-values" && strings.Contains(cv.Message, "relative to a repository's root")
			})
			if len(w.Repos) != 0 || missing != c.missing {
				t.Errorf("who-knows %s: %d repositories, caveats %+v; want none, and the no-path caveat %v", c.path, len(w.Repos), w.Caveats, c.missing)
			}
		}
	})

	t.Run("AI-assisted tags and reverts", func(t *testing.T) {
		// SPEC, "AI-assisted tags and reverts (main fixture)": no landing is
		// AI-assisted, and the revert query returns nothing.
		checkShare(t, "SPEC AI-assisted tags (main fixture)", r.Card.AIAssisted, 0, 16)
		if links := revertLinks(t, onlyFacts(t, s)); len(links) > 0 {
			t.Errorf("SPEC reverts (main fixture): the revert query returns nothing, but the run links %v", links)
		}
	})
}

// siblingChangeOracles checks SPEC's change oracles on the sibling fixture.
func siblingChangeOracles(t *testing.T) {
	s := scan(t, fixture.SiblingMailmap(t), nil, scanRanked(fixture.Sibling(t)))
	r := onlyRepo(t, s.model)
	rf := onlyFacts(t, s)

	t.Run("durable@1", func(t *testing.T) {
		// SPEC, "durable@1 (sibling)": Copilot, Uma and Claude Dupont have no
		// value. L7, reverted by R1, keeps 0 of 3; R3, a revert landing, is
		// no cluster; O10 (whitespace only) and D1 (deletions only) add
		// nothing.
		checkDurable(t, s, r.Key, "SPEC durable@1 (sibling)", map[string]durableRow{
			"lena@example.com":  {"Lena Lead", 26, 30, 10, 1, true},
			"omar@example.com":  {"Omar Ops", 27, 27, 9, 0, false},
			"pia@example.com":   {"Pia Pair", 9, 9, 2, 0, false},
			"quinn@example.com": {"Quinn Squash", 6, 6, 1, 0, false},
		}, map[string]map[string]cluster{
			"lena@example.com": {
				"2025-11-03T12:00:00Z": {3, 3}, // L1
				"2025-11-17T12:00:00Z": {2, 3}, // L2: W2 replaced lena_2_2
				"2025-12-01T12:00:00Z": {3, 3}, // L3
				"2025-12-15T12:00:00Z": {3, 3}, // L4
				"2026-01-19T12:00:00Z": {3, 3}, // L5: blame looks through F
				"2026-02-09T12:00:00Z": {3, 3}, // L6
				"2026-03-02T12:00:00Z": {0, 3}, // L7: reverted by R1
				"2026-03-23T12:00:00Z": {3, 3}, // L8
				"2026-04-27T12:00:00Z": {3, 3}, // L9
				"2026-05-11T12:00:00Z": {3, 3}, // L10
			},
			"omar@example.com": {
				"2025-11-10T12:00:00Z": {3, 3}, // O1: blame -w leaves the re-indented lines with O1
				"2025-11-24T12:00:00Z": {3, 3}, // O2
				"2025-12-08T12:00:00Z": {3, 3}, // O3
				"2026-01-12T12:00:00Z": {3, 3}, // O4
				"2026-01-26T12:00:00Z": {3, 3}, // O5
				"2026-02-23T12:00:00Z": {3, 3}, // O6
				"2026-03-16T12:00:00Z": {3, 3}, // O7
				"2026-04-20T12:00:00Z": {3, 3}, // O8
				"2026-05-04T12:00:00Z": {3, 3}, // O9
			},
			"pia@example.com": {
				"2026-02-02T12:00:00Z": {4, 4}, // C1: Copilot is the author; Pia is credited
				"2026-02-20T12:00:00Z": {5, 5}, // M: P1 3 and P2 2; the merge commit itself is excluded
			},
			"quinn@example.com": {"2026-03-09T12:00:00Z": {6, 6}}, // Q1
		})
	})

	t.Run("simplifying@1", func(t *testing.T) {
		// SPEC, "simplifying@1 (sibling)": Quinn has no value, because both of
		// his window landings are excluded (D is reverted, R2 a revert); F
		// (ignore-revs) and R1 (a revert) are no clusters of Omar's.
		checkSimplifying(t, s, r.Key, "SPEC simplifying@1 (sibling)", map[string]simplifyingRow{
			"lena@example.com": {"Lena Lead", 23, 1, 10, true, false},
			"omar@example.com": {"Omar Ops", 21, 0, 2, false, false},
			"pia@example.com":  {"Pia Pair", 0, 0, 1, false, false},
		}, map[string]map[string]cluster{
			"lena@example.com": {
				"2026-07-20T12:00:00Z": {},        // W1
				"2026-07-27T12:00:00Z": {Num: 1},  // W2
				"2026-08-03T12:00:00Z": {},        // W3
				"2026-08-10T12:00:00Z": {},        // W4
				"2026-08-17T12:00:00Z": {},        // W5
				"2026-08-24T12:00:00Z": {},        // W6
				"2026-08-31T12:00:00Z": {},        // W7
				"2026-09-14T12:00:00Z": {Num: 22}, // S1: net -20, simplifying
				"2026-09-16T12:00:00Z": {},        // W8
				"2026-09-21T12:00:00Z": {},        // W9
			},
			"omar@example.com": {
				"2026-07-24T12:00:00Z": {},        // IR: its only path is unclassified
				"2026-08-12T12:00:00Z": {Num: 21}, // S2: net -16, not simplifying
			},
			"pia@example.com": {"2026-07-29T12:00:00Z": {}}, // T1: the 30 lines are test
		})
	})

	t.Run("eligibility on the person's own page", func(t *testing.T) {
		// SPEC, the Eligible columns: Lena at exactly 10 clusters in both
		// metrics; Omar at 9 in durable@1, listed and not ranked.
		checkMeRank(t, s, r.Key, "lena@example.com", durable, true, 10)
		checkMeRank(t, s, r.Key, "lena@example.com", simplifying, true, 10)
		checkMeRank(t, s, r.Key, "omar@example.com", durable, false, 9)
		checkMeRank(t, s, r.Key, "omar@example.com", simplifying, false, 2)
	})

	t.Run("reverts", func(t *testing.T) {
		// SPEC, "Reverts" (sibling): three links, by landing time.
		want := map[string]string{
			"2026-04-06T12:00:00Z": "2026-03-30T12:00:00Z", // R3 reverts D1
			"2026-08-07T12:00:00Z": "2026-08-05T12:00:00Z", // R2 reverts D
			"2026-08-19T12:00:00Z": "2026-03-02T12:00:00Z", // R1 reverts L7
		}
		sameRows(t, "SPEC reverts (sibling), revert landing → reverted landing", revertLinks(t, rf), want)
	})

	t.Run("ignore-revs", func(t *testing.T) {
		// SPEC, "Sibling history": IR lists F (2026-07-22) in the root
		// .git-blame-ignore-revs, which blame uses and the run records.
		f := landingAt(t, rf, "2026-07-22T12:00:00Z")
		if got := r.IgnoreRevs; got.Source != "root" || !slices.Equal(got.Revs, []string{f}) {
			t.Errorf("SPEC sibling history, IR: blame_ignore_revs is source %q, revs %v; want root, holding F's id %s", got.Source, got.Revs, f)
		}
	})

	t.Run("AI-assisted tags", func(t *testing.T) {
		// SPEC, "AI-assisted tags and credit": L3, O4, C1 and M are tagged and
		// Q1 is not; 4 of the 42 landings in the last 365 days. Who each one
		// credits is in the durable@1 clusters above.
		checkShare(t, "SPEC AI-assisted tags and credit", r.Card.AIAssisted, 4, 42)
		for _, c := range []struct {
			row, at string
			tagged  bool
		}{
			{"L3", "2025-12-01T12:00:00Z", true},
			{"O4", "2026-01-12T12:00:00Z", true},
			{"C1", "2026-02-02T12:00:00Z", true},
			{"M", "2026-02-20T12:00:00Z", true},
			{"Q1", "2026-03-09T12:00:00Z", false},
		} {
			checkTag(t, s, r.Key, c.row, landingAt(t, rf, c.at), c.tagged)
		}
	})

	t.Run("surviving@1 credit", func(t *testing.T) {
		// SPEC, "surviving@1 credit (sibling)": all 120 lines are people's,
		// Copilot and Claude Dupont have none, and C1's 4 lines are Pia's.
		checkSurviving(t, "SPEC surviving@1 credit (sibling)", r, map[string]int{
			"lena@example.com":  37,
			"omar@example.com":  36,
			"quinn@example.com": 31,
			"pia@example.com":   9,
			"uma@example.com":   7,
		})
		checkShare(t, "SPEC surviving@1 credit (sibling), the bot share", r.Card.BotShare, 0, 120)
		checkWhoKnows(t, "SPEC surviving@1 credit (sibling), C1's lines", s, r.Key, whoKnowsCase{
			path: "retry.py", lines: 4, owners: []string{"pia@example.com"}, top: []knows{{"pia@example.com", 4}},
		})
	})
}

// backdatedGuard checks SPEC's backdated-commit guard: X, a first-parent
// commit whose committer date is older than the scan window, hides none of
// the landings below it, and its own commit counts for no landing; A2, a
// rebased commit whose author date is older than the window, lands by its
// committer time and is credited to its author, not to its committer.
func backdatedGuard(t *testing.T) {
	s := scan(t, "", nil, scanRanked(fixture.Backdated(t)))
	r := onlyRepo(t, s.model)
	if got := r.Card.Landings.History; got != 4 {
		t.Errorf("SPEC regression guards, backdated commit: %d landings in the history window, want 4 (B1, B2, A1 and A2)", got)
	}
	checkDurable(t, s, r.Key, "SPEC regression guards, backdated commit, durable@1", map[string]durableRow{
		"bea@example.com": {"Bea Below", 4, 4, 1, 0, false},
	}, map[string]map[string]cluster{
		"bea@example.com": {"2026-02-02T12:00:00Z": {4, 4}}, // B1
	})
	checkSimplifying(t, s, r.Key, "SPEC regression guards, backdated commit, simplifying@1", map[string]simplifyingRow{
		"cal@example.com": {"Cal Below", 0, 0, 3, false, false},
	}, map[string]map[string]cluster{
		"cal@example.com": {
			"2026-08-03T12:00:00Z": {}, // B2
			"2026-08-17T12:00:00Z": {}, // S1, landed by A1
			"2026-08-24T12:00:00Z": {}, // A2, at its committer time, for its author; Bea committed it
		},
	})
}

// mergedRevertGuard checks SPEC's merged-revert guard: each revert sits on
// a side branch that a --no-ff merge lands, under a message that names no
// revert; the revert is linked to the merge landing, which neither change
// metric counts, and the reverted landing leaves simplifying@1; the lines a
// revert restored still count in surviving@1, for whoever reverted.
func mergedRevertGuard(t *testing.T) {
	s := scan(t, "", nil, scanRanked(fixture.MergedRevert(t)))
	r := onlyRepo(t, s.model)
	if got := r.Card.Landings.History; got != 6 {
		t.Errorf("SPEC regression guards, merged revert: %d landings in the history window, want 6 (D1, M1, N1, X2, M2 and N2)", got)
	}
	sameRows(t, "SPEC regression guards, merged revert, revert landing → reverted landing", revertLinks(t, onlyFacts(t, s)), map[string]string{
		"2026-01-26T12:00:00Z": "2026-01-12T12:00:00Z", // M1 lands R1, which reverts D1
		"2026-08-17T12:00:00Z": "2026-08-03T12:00:00Z", // M2 lands R2, which reverts X2
	})
	// Rex, who reverted, and Wes, whose landings were reverted, have no
	// value: Nia's control landings are the only clusters.
	checkDurable(t, s, r.Key, "SPEC regression guards, merged revert, durable@1", map[string]durableRow{
		"nia@example.com": {"Nia Normal", 3, 3, 1, 0, false},
	}, map[string]map[string]cluster{
		"nia@example.com": {"2026-02-09T12:00:00Z": {3, 3}}, // N1
	})
	checkSimplifying(t, s, r.Key, "SPEC regression guards, merged revert, simplifying@1", map[string]simplifyingRow{
		"nia@example.com": {"Nia Normal", 0, 0, 1, false, false},
	}, map[string]map[string]cluster{
		"nia@example.com": {"2026-08-24T12:00:00Z": {}}, // N2
	})
	// surviving@1 excludes no commit: blame gives R1's 4 restored lines to
	// Rex, and Nia holds legacy_5 and legacy_6 from U0, nia.py's 3 and
	// nia_w.py's 1.
	checkSurviving(t, "SPEC regression guards, merged revert, surviving@1", r, map[string]int{
		"rex@example.com": 4,
		"nia@example.com": 6,
	})
}

// checkSurviving compares surviving@1, a repository's still-here table, with
// a SPEC table of the prod lines each person holds at HEAD, and requires
// that no bot holds any.
func checkSurviving(t *testing.T, table string, r *report.Repo, want map[string]int) {
	t.Helper()
	if r.StillHere == nil {
		t.Errorf("%s: the repository has no still-here table", table)
		return
	}
	got := map[string]int{}
	for _, row := range r.StillHere.Humans {
		got[row.Email] = row.ProdNonblankLines
	}
	for _, row := range r.StillHere.Bots {
		t.Errorf("%s: bot %s holds %d lines, want none", table, row.Email, row.ProdNonblankLines)
	}
	sameRows(t, table, got, want)
}

// checkDurable compares durable@1 in repository key with a SPEC table: the
// rank table lists exactly its people, eligible ones ranked and the rest
// below the minimum, and explain gives each person's Σ surviving / Σ added,
// N, reverted landings and clusters by landing time.
func checkDurable(t *testing.T, s *scanned, key, table string, want map[string]durableRow,
	clusters map[string]map[string]cluster) {
	t.Helper()
	rows := map[string]rankRow{}
	for email, w := range want {
		rows[email] = rankRow{w.Name, listOf(w.Ranked, false), w.Surviving / w.Added, w.N}
	}
	checkRankTable(t, table, s.model, durable, key, cohort, rows)

	got := map[string]durableRow{}
	for email, w := range want {
		e := s.explain(t, durable, person(email))
		v, ok := repoValue(t, table, e, key)
		if !ok {
			continue
		}
		if !sameInterval(v.Window, cohort) {
			t.Errorf("%s: %s's value covers [%s, %s), want the cohort [2025-10-07, 2026-07-09)", table, email,
				rfc3339(v.Window.Since), rfc3339(v.Window.Until))
		}
		// Ranked is the rank table's to say, above.
		got[email] = durableRow{e.Subject.Name, v.Num, v.Den, v.N, int(v.Detail["reverted_landings"]), w.Ranked}
		sameRows(t, fmt.Sprintf("%s, %s's clusters by landing time", table, email), changes(e, key), clusters[email])
	}
	sameRows(t, table, got, want)
}

// checkSimplifying compares simplifying@1 in repository key with a SPEC
// table, as checkDurable does: the rank table, then each person's or bot's
// removed lines, simplifying landings and N, and each person's clusters.
func checkSimplifying(t *testing.T, s *scanned, key, table string, want map[string]simplifyingRow,
	clusters map[string]map[string]cluster) {
	t.Helper()
	rows := map[string]rankRow{}
	for email, w := range want {
		rows[email] = rankRow{w.Name, listOf(w.Ranked, w.Bot), w.Removed, w.N}
	}
	checkRankTable(t, table, s.model, simplifying, key, recent, rows)

	got := map[string]simplifyingRow{}
	for email, w := range want {
		e := s.explain(t, simplifying, person(email))
		v, ok := repoValue(t, table, e, key)
		if !ok {
			continue
		}
		if !sameInterval(v.Window, recent) {
			t.Errorf("%s: %s's value covers [%s, %s), want the 90 days [2026-07-09, 2026-10-07)", table, email,
				rfc3339(v.Window.Since), rfc3339(v.Window.Until))
		}
		// Ranked is the rank table's to say, above.
		got[email] = simplifyingRow{e.Subject.Name, v.Value, int(v.Detail["simplifying_landings"]), v.N, w.Ranked, e.Subject.Bot}
		if !w.Bot { // bots are never ranked, so explain lists no clusters of theirs
			sameRows(t, fmt.Sprintf("%s, %s's clusters by landing time", table, email), changes(e, key), clusters[email])
		}
	}
	sameRows(t, table, got, want)
}

// rankRow is where a rank table lists a person, and with what.
type rankRow struct {
	Name     string
	List     string // ranked, below_min or bots
	Estimate float64
	N        int
}

func listOf(ranked, bot bool) string {
	switch {
	case bot:
		return "bots"
	case ranked:
		return "ranked"
	}
	return "below_min"
}

// checkRankTable compares metric's rank table in the scope of repository
// key, which is how a run without groups ranks a repository named directly,
// with want: its window, its minimum of 10 clusters, and every row.
func checkRankTable(t *testing.T, table string, m *report.Model, metric domain.MetricID, key string,
	w report.Interval, want map[string]rankRow) {
	t.Helper()
	var rt *report.RankTable
	for i := range m.Ranks {
		if m.Ranks[i].Metric == metric.String() && m.Ranks[i].Scope == (report.Scope{Kind: domain.ScopeRepo, Key: key}) {
			rt = &m.Ranks[i]
		}
	}
	if rt == nil {
		t.Errorf("%s: the scan has no %s rank table for repository %s", table, metric, key)
		return
	}
	if !sameInterval(rt.Window, w) || rt.MinN != 10 {
		t.Errorf("%s: the rank table covers [%s, %s) with min_n %d, want [%s, %s) and 10", table,
			rfc3339(rt.Window.Since), rfc3339(rt.Window.Until), rt.MinN, rfc3339(w.Since), rfc3339(w.Until))
	}
	got := map[string]rankRow{}
	for _, l := range []struct {
		name string
		rows []report.RankRow
	}{{"ranked", rt.Ranked}, {"below_min", rt.BelowMin}, {"bots", rt.Bots}} {
		for _, row := range l.rows {
			got[row.Email] = rankRow{row.Name, l.name, row.Estimate, row.N}
		}
	}
	sameRows(t, table+", rank table", got, want)
}

// repoValue returns e's one value in the scope of repository key.
func repoValue(t *testing.T, table string, e *report.Explain, key string) (report.Value, bool) {
	t.Helper()
	var found []report.Value
	for _, v := range e.Values {
		if v.Scope == (report.Scope{Kind: domain.ScopeRepo, Key: key}) {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		t.Errorf("%s: %s %s has %d %s values in repository %s, want 1", table, e.Subject.Kind, e.Subject.Key, len(found), e.Metric.ID, key)
		return report.Value{}, false
	}
	return found[0], true
}

// changes returns the clusters explain lists in repository key, by landing
// time.
func changes(e *report.Explain, key string) map[string]cluster {
	out := map[string]cluster{}
	for _, c := range e.Changes {
		if c.RepoKey == key {
			out[rfc3339(c.Time)] = cluster{c.Num, c.Den}
		}
	}
	return out
}

// checkTag asks explain whether ai_assisted@1 tags landing, as the MCP
// server's explain(metric, subject) does for a change: a tagged landing has
// one value, 1, over the 365 days a scan collects, and an untagged one has
// none at all.
func checkTag(t *testing.T, s *scanned, key, row, landing string, tagged bool) {
	t.Helper()
	oracle := "SPEC AI-assisted tags and credit, " + row
	e, err := app.Explain(t.Context(), s.read, app.ExplainRequest{
		RunID: s.run, Metric: domain.MetricID{Name: "ai_assisted", Version: 1},
		Subject: domain.Subject{Kind: domain.SubjectChange, Key: landing},
	})
	switch {
	case !tagged && errNotFound(err):
	case !tagged && err == nil:
		t.Errorf("%s: not AI-assisted, but explain gives %d values", oracle, len(e.Values))
	case err != nil:
		t.Errorf("%s: explain: %v", oracle, err)
	default:
		v, ok := repoValue(t, oracle, e, key)
		if ok && (v.Value != 1 || !sameInterval(v.Window, scanWindow)) {
			t.Errorf("%s: AI-assisted, but its tag is %s over [%s, %s), want 1 over [2025-10-07, 2026-10-07)", oracle, describe(v),
				rfc3339(v.Window.Since), rfc3339(v.Window.Until))
		}
	}
}

// checkMeRank checks a person's place in metric's rank table of repository
// key on their own page.
func checkMeRank(t *testing.T, s *scanned, key, email string, metric domain.MetricID, eligible bool, n int) {
	t.Helper()
	oracle := fmt.Sprintf("SPEC %s (sibling), %s's Eligible column", metric, email)
	for _, r := range s.me(t, email).Ranks {
		if r.Metric == metric.String() && r.Scope == (report.Scope{Kind: domain.ScopeRepo, Key: key}) {
			if r.Eligible != eligible || r.N != n {
				t.Errorf("%s: me gives eligible %t at %d clusters, want %t at %d", oracle, r.Eligible, r.N, eligible, n)
			}
			return
		}
	}
	t.Errorf("%s: me lists no %s rank in repository %s", oracle, metric, key)
}

// checkShare compares a report's share with SPEC's num of den.
func checkShare(t *testing.T, oracle string, s *report.Share, num, den float64) {
	t.Helper()
	switch {
	case s == nil:
		t.Errorf("%s: the card has no share, want %v of %v", oracle, num, den)
	case s.Num != num || s.Den != den || s.Value != num/den:
		t.Errorf("%s: %v of %v (%v), want %v of %v", oracle, s.Num, s.Den, s.Value, num, den)
	}
}

// knows is a person and their surviving prod lines under a path.
type knows struct {
	Email string
	Lines int
}

// whoKnowsCase is one row of a SPEC who_knows@1 table.
type whoKnowsCase struct {
	path    string
	lines   int      // surviving prod lines under the path
	owners  []string // majority owners, by email, sorted
	top     []knows  // top surviving-line authors, in order
	landers []string // recent landers (90 days), by email, sorted, one landing each
}

// checkWhoKnows asks app.WhoKnows about c.path and compares the answer in
// repository key with c: majority owners, top authors, recent landers, and
// no bots.
func checkWhoKnows(t *testing.T, table string, s *scanned, key string, c whoKnowsCase) {
	t.Helper()
	oracle := table + ", " + c.path
	w := s.whoKnows(t, c.path)
	var r *report.WhoKnowsRepo
	for i := range w.Repos {
		if w.Repos[i].Key == key {
			r = &w.Repos[i]
		}
	}
	if r == nil {
		t.Errorf("%s: who-knows has no answer in repository %s", oracle, key)
		return
	}
	emails := func(ks []report.Knower) []string {
		var out []string
		for _, k := range ks {
			out = append(out, k.Email)
		}
		slices.Sort(out)
		return out
	}
	var top []knows
	for _, k := range r.TopAuthors {
		top = append(top, knows{k.Email, k.Lines})
	}
	if r.Lines != c.lines {
		t.Errorf("%s: %d surviving prod lines, want %d", oracle, r.Lines, c.lines)
	}
	if got := emails(r.MajorityOwners); !slices.Equal(got, c.owners) {
		t.Errorf("%s: majority owners %v, want %v", oracle, got, c.owners)
	}
	if !slices.Equal(top, c.top) {
		t.Errorf("%s: top authors %+v, want %+v", oracle, top, c.top)
	}
	if got := emails(r.RecentLanders); !slices.Equal(got, c.landers) {
		t.Errorf("%s: recent landers %v, want %v", oracle, got, c.landers)
	}
	for _, k := range r.RecentLanders {
		if k.RecentLandings != 1 {
			t.Errorf("%s: %s landed %d recent changes, want 1", oracle, k.Email, k.RecentLandings)
		}
	}
	if len(r.Bots) > 0 {
		t.Errorf("%s: bots %v, want none", oracle, emails(r.Bots))
	}
}

// onlyFacts returns the stored facts of the run's one repository.
func onlyFacts(t *testing.T, s *scanned) *domain.RepoFacts {
	t.Helper()
	fs := s.facts(t)
	if len(fs.Repos) != 1 {
		t.Fatalf("the run stored %d repositories, want 1", len(fs.Repos))
	}
	return &fs.Repos[0]
}

// landingAt returns the SHA of rf's landing at time at.
func landingAt(t *testing.T, rf *domain.RepoFacts, at string) string {
	t.Helper()
	for _, l := range rf.Landings {
		if rfc3339(l.Time) == at {
			return l.SHA
		}
	}
	t.Fatalf("%s has no landing at %s", rf.Repo.Name, at)
	return ""
}

// revertLinks returns rf's revert links as landing times: the revert
// landing's, then the reverted commit's landing's.
func revertLinks(t *testing.T, rf *domain.RepoFacts) map[string]string {
	t.Helper()
	when := map[string]string{}
	for _, l := range rf.Landings {
		when[l.SHA] = rfc3339(l.Time)
	}
	name := func(sha string) string {
		if at, ok := when[sha]; ok {
			return at
		}
		return "commit " + sha // not a landing of the run
	}
	out := map[string]string{}
	for _, rv := range rf.Reverts {
		if _, dup := out[name(rv.SHA)]; dup {
			t.Errorf("the landing at %s reverts more than one commit", name(rv.SHA))
		}
		out[name(rv.SHA)] = name(rv.Reverted)
	}
	return out
}
