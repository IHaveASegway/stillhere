package fixture

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// SiblingMailmapText is the mailmap the sibling oracles pass with --mailmap.
// It folds the GitHub noreply address that the Copilot commit's
// Co-authored-by trailer names into Pia's canonical address, so the oracles
// also check that co-authors are canonicalised with git check-mailmap. Like
// MailmapText it is not committed, and the repository sets log.mailmap=false.
// Neither text names a person of the other repository, so one run over both
// repositories can pass the two texts concatenated.
const SiblingMailmapText = "Pia Pair <pia@example.com> <81234567+piapair@users.noreply.github.com>\n"

// siblingName is the base name of the directory Sibling builds into, so the
// repository's discovery key is always "local:sibling".
const siblingName = "sibling"

// Sibling builds the sibling fixture repository in a new temporary directory
// and returns its absolute path: a work tree named "sibling", on branch main.
// It stops the test with t.Fatal if the build fails. The sibling is a separate
// repository so that the change cases it holds (AI-assisted trailers, reverts,
// simplifying deletions, rank eligibility) never move the main oracles.
func Sibling(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), siblingName)
	if err := BuildSiblingContext(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// SiblingMailmap writes SiblingMailmapText to a new temporary file and
// returns the file's path, for --mailmap or git -c mailmap.file=.
func SiblingMailmap(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailmap")
	if err := os.WriteFile(path, []byte(SiblingMailmapText), 0o600); err != nil {
		t.Fatalf("fixture: write sibling mailmap: %v", err)
	}
	return path
}

// BuildSibling is BuildSiblingContext with context.Background, for callers
// that have no context to pass, such as a person building the sibling to
// inspect it.
func BuildSibling(dir string) error {
	return BuildSiblingContext(context.Background(), dir)
}

// BuildSiblingContext builds the sibling fixture repository in dir: a work
// tree on branch main whose history is the Sibling history table of
// testdata/fixture/SPEC.md, with branch pia-feature kept. dir is created when
// missing and must otherwise be empty. Cancelling ctx kills the running git
// process and returns its error.
func BuildSiblingContext(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	siblingHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build sibling repository in %s: %w", dir, b.err)
	}
	return nil
}

// siblingHistory writes the Sibling history table of testdata/fixture/SPEC.md:
// one row per commit, in table order, each commented with the row ID that
// SPEC's sibling oracles use.
//
// Every row is at noon UTC, and no row is within a day of a boundary the
// oracles use: the durable@1 cohort [2025-10-07, 2026-07-09), the 90-day
// window [2026-07-09, 2026-10-07), the 180-day activity window from
// 2026-04-10, the week from 2026-09-30 and the 30-day highlight window from
// 2026-09-07. As in the main fixture, every non-blank line is unique in the
// repository, so no diff can pair a line with an identical twin.
func siblingHistory(b *builder) {
	uma := person{"Uma Legacy", "uma@example.com"}
	lena := person{"Lena Lead", "lena@example.com"}
	omar := person{"Omar Ops", "omar@example.com"}
	pia := person{"Pia Pair", "pia@example.com"}
	quinn := person{"Quinn Squash", "quinn@example.com"}
	copilot := person{"Copilot", "copilot@bot.example"}

	// edit records one commit that creates or replaces the file at path.
	edit := func(when time.Time, who person, path, content, msg string) {
		b.row(when, who)
		b.write(path, content)
		b.commit(msg)
	}
	// file records one commit that creates <name>.py with n numbered lines,
	// under the subject "feat: add <name>.py" and then the trailer, if any.
	file := func(when time.Time, who person, name string, n int, trailer string) {
		msg := "feat: add " + name + ".py"
		if trailer != "" {
			msg += "\n\n" + trailer
		}
		edit(when, who, name+".py", numbered(name, n), msg)
	}
	// revert records a landing made by git revert --no-edit. Its message
	// carries the line "This reverts commit <sha>." that revert detection reads.
	revert := func(when time.Time, who person, sha string) {
		b.row(when, who)
		b.git("revert", "--no-edit", sha)
	}

	b.initRepo()
	b.git("config", "log.mailmap", "false") // the oracles pass SiblingMailmapText themselves
	b.git("config", "commit.gpgsign", "false")

	// U0, more than 365 days before Now: the code that later rows delete.
	// legacy_b.py has blank lines inside the block S2 deletes.
	b.row(noon(2025, time.September, 15), uma)
	b.write("legacy_a.py", numbered("legacy_a", 24))
	b.write("legacy_b.py", span("legacy_b", 1, 5)+"\n"+
		span("legacy_b", 6, 9)+"\n"+
		span("legacy_b", 10, 13)+"\n"+
		span("legacy_b", 14, 17)+"\n"+
		span("legacy_b", 18, 23))
	b.write("legacy_c.py", numbered("legacy_c", 26))
	b.write("legacy_d.py", numbered("legacy_d", 6))
	b.write("tests/test_legacy.py", numbered("test_legacy", 30))
	b.commit("chore: import the legacy modules")

	// The durable@1 cohort, [2025-10-07, 2026-07-09).
	file(noon(2025, time.November, 3), lena, "lena_1", 3, "") // L1
	// O1: O10 later re-indents the body.
	edit(noon(2025, time.November, 10), omar, "omar_1.py", lines(
		"def omar_job():",
		"    omar_1_1 = 1",
		"    omar_1_2 = 2",
	), "feat: add omar_1.py")
	file(noon(2025, time.November, 17), lena, "lena_2", 3, "") // L2
	file(noon(2025, time.November, 24), omar, "omar_2", 3, "") // O2
	// L3: Claude Code's trailer, with the key capitalised as Claude Code
	// writes it. Git matches trailer keys case-insensitively.
	file(noon(2025, time.December, 1), lena, "lena_3", 3, "Co-Authored-By: Claude <noreply@anthropic.com>")
	file(noon(2025, time.December, 8), omar, "omar_3", 3, "")  // O3
	file(noon(2025, time.December, 15), lena, "lena_4", 3, "") // L4
	file(noon(2026, time.January, 12), omar, "omar_4", 3,
		"Co-authored-by: aider (gpt-4o) <aider@aider.chat>") // O4: Aider's trailer
	// L5: F later requotes it.
	edit(noon(2026, time.January, 19), lena, "lena_5.py", lines(
		"lena_5_1 = 'one'",
		"lena_5_2 = 'two'",
		"lena_5_3 = 'three'",
	), "feat: add lena_5.py")
	file(noon(2026, time.January, 26), omar, "omar_5", 3, "") // O5
	// C1: the Copilot coding agent as author, and the person it worked for as
	// co-author, under a GitHub noreply address that SiblingMailmapText folds
	// into pia@example.com.
	file(noon(2026, time.February, 2), copilot, "retry", 4,
		"Co-authored-by: Pia Pair <81234567+piapair@users.noreply.github.com>")
	file(noon(2026, time.February, 9), lena, "lena_6", 3, "") // L6

	// P1 and P2 on branch pia-feature, landed by Quinn's merge M. P2 carries
	// a Claude trailer under a model name, so M is an AI-assisted landing only
	// through a commit it brought in.
	b.git("switch", "--quiet", "--create", "pia-feature")
	file(noon(2026, time.February, 16), pia, "pia_feature", 3, "") // P1
	b.row(noon(2026, time.February, 18), pia)                      // P2
	b.extend("pia_feature.py", span("pia_feature", 4, 5))
	b.commit("feat: extend pia_feature.py\n\nCo-authored-by: Claude Sonnet 4.5 <noreply@anthropic.com>")
	b.row(noon(2026, time.February, 20), quinn) // M
	b.git("switch", "--quiet", "main")
	b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge branch 'pia-feature'", "pia-feature")

	file(noon(2026, time.February, 23), omar, "omar_6", 3, "") // O6
	file(noon(2026, time.March, 2), lena, "lena_7", 3, "")     // L7: R1 reverts it
	l7 := b.head()
	// Q1: a GitHub squash merge, which lists the pull request's other authors
	// as Co-authored-by trailers. This one is a person named Claude with a
	// noreply address, so neither the name nor the address may tag it.
	edit(noon(2026, time.March, 9), quinn, "quota.py", numbered("quota", 6),
		"feat: add quota checks (#7)\n\n"+
			"* feat: add quota checks\n\n"+
			"* fix: handle zero quota\n\n"+
			"---------\n\n"+
			"Co-authored-by: Claude Dupont <92345678+cdupont@users.noreply.github.com>")
	file(noon(2026, time.March, 16), omar, "omar_7", 3, "") // O7
	file(noon(2026, time.March, 23), lena, "lena_8", 3, "") // L8
	// D1 deletes four legacy lines and R3 reverts it. The revert re-adds
	// lines, and blame then credits them to R3.
	edit(noon(2026, time.March, 30), omar, "legacy_d.py", span("legacy_d", 5, 6),
		"refactor: drop legacy_d_1 to legacy_d_4") // D1
	revert(noon(2026, time.April, 6), omar, b.head())       // R3
	file(noon(2026, time.April, 20), omar, "omar_8", 3, "") // O8
	file(noon(2026, time.April, 27), lena, "lena_9", 3, "") // L9
	file(noon(2026, time.May, 4), omar, "omar_9", 3, "")    // O9
	file(noon(2026, time.May, 11), lena, "lena_10", 3, "")  // L10
	// O10: whitespace only, which a -w patch reduces to nothing.
	edit(noon(2026, time.May, 18), omar, "omar_1.py", lines(
		"def omar_job():",
		"  omar_1_1 = 1",
		"  omar_1_2 = 2",
	), "style: reindent omar_job")

	// The simplifying@1 window, [2026-07-09, 2026-10-07).
	file(noon(2026, time.July, 20), lena, "lena_w1", 1, "") // W1
	// F requotes L5's lines, and IR lists F in .git-blame-ignore-revs, so
	// blame at HEAD gives the lines back to L5.
	edit(noon(2026, time.July, 22), omar, "lena_5.py", lines(
		`lena_5_1 = "one"`,
		`lena_5_2 = "two"`,
		`lena_5_3 = "three"`,
	), "style: use double quotes in lena_5.py") // F
	f := b.head()
	edit(noon(2026, time.July, 24), omar, ".git-blame-ignore-revs",
		lines("# style: use double quotes in lena_5.py", f),
		"chore: ignore the quote-style commit in blame") // IR
	edit(noon(2026, time.July, 27), lena, "lena_2.py", lines(
		"lena_2_1 = 1",
		"lena_2_2 = 22",
		"lena_2_3 = 3",
	), "fix: set lena_2_2 to 22") // W2: replaces one of L2's lines
	// T1: a large deletion under tests/, which no prod metric counts.
	b.row(noon(2026, time.July, 29), pia)
	b.git("rm", "--quiet", "--", "tests/test_legacy.py")
	b.commit("test: remove the legacy tests")
	file(noon(2026, time.August, 3), lena, "lena_w3", 1, "") // W3
	// D would be a simplifying landing, but R2 reverts it.
	edit(noon(2026, time.August, 5), quinn, "legacy_c.py", span("legacy_c", 26, 26),
		"refactor: drop legacy_c_1 to legacy_c_25") // D
	revert(noon(2026, time.August, 7), quinn, b.head())       // R2
	file(noon(2026, time.August, 10), lena, "lena_w4", 1, "") // W4
	// S2 removes 21 non-blank and 4 blank lines and adds 5: net -16.
	edit(noon(2026, time.August, 12), omar, "legacy_b.py", lines(
		"legacy_b_total = 231",
		"legacy_b_first = 1",
		"legacy_b_last = 21",
		"legacy_b_count = 21",
		"legacy_b_mean = 11",
	)+span("legacy_b", 22, 23), "refactor: fold legacy_b_1 to legacy_b_21")
	file(noon(2026, time.August, 17), lena, "lena_w5", 1, "") // W5
	revert(noon(2026, time.August, 19), omar, l7)             // R1
	file(noon(2026, time.August, 24), lena, "lena_w6", 1, "") // W6
	file(noon(2026, time.August, 31), lena, "lena_w7", 1, "") // W7
	// S1 removes 22 non-blank lines and adds 2 non-blank and 1 blank: net -20.
	edit(noon(2026, time.September, 14), lena, "legacy_a.py", lines(
		"legacy_a_total = 253",
		"",
		"legacy_a_note = 'folded 1 to 22'",
	)+span("legacy_a", 23, 24), "refactor: fold legacy_a_1 to legacy_a_22")
	file(noon(2026, time.September, 16), lena, "lena_w8", 1, "") // W8
	file(noon(2026, time.September, 21), lena, "lena_w9", 1, "") // W9
}

// head returns the full ID of the commit HEAD names, or "" once the build has
// failed. The sibling needs real IDs twice: git revert names the commit it
// reverts, and .git-blame-ignore-revs lists the commit blame skips. The
// fixed argv reads no ignore or attribute file, so it does not need the -c
// overrides that builder.git adds.
func (b *builder) head() string {
	if b.err != nil {
		return ""
	}
	cmd := exec.CommandContext(b.ctx, "git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir = b.dir
	cmd.Env = slices.Concat(b.env, b.who)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		b.err = fmt.Errorf("git rev-parse HEAD: %w", err)
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			b.err = fmt.Errorf("%w: %s", b.err, msg)
		}
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// noon is year-month-day at 12:00:00 UTC. The sibling spans two years, so it
// cannot use at, which is fixed to 2026.
func noon(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// span returns the lines "<name>_<i> = <i>" for i from first to last: the
// part of numbered(name, last) that starts at line first.
func span(name string, first, last int) string {
	var s strings.Builder
	for i := first; i <= last; i++ {
		fmt.Fprintf(&s, "%s_%d = %d\n", name, i, i)
	}
	return s.String()
}
