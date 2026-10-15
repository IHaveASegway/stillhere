package fixture

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// This file builds the small repositories of the regression guards in
// testdata/fixture/SPEC.md: history shapes that broke the history collector
// once (#37's review) or a metric (#54), and that the main and sibling
// fixtures must not hold, because each would move their oracles. Each
// repository is its own, so a guard that fails names its own cause.

// The base names of the directories the guards build into, so their
// discovery keys are "local:backdated", "local:merged-revert" and
// "local:binary".
const (
	backdatedName    = "backdated"
	mergedRevertName = "merged-revert"
	binaryName       = "binary"
)

// nul is the byte that makes git take a file for binary when it is in the
// file's first 8,000 bytes: the binary guard's files hold it at the end of
// a line.
const nul = "\x00"

// Backdated builds the backdated-commit guard of testdata/fixture/SPEC.md in
// a new temporary directory and returns its absolute path: a work tree named
// "backdated", on branch main, whose first-parent chain holds a commit with a
// committer date older than the scan window, above landings inside it, and
// on top a rebased landing, whose author date is older than the window and
// whose committer is not its author. It stops the test with t.Fatal if the
// build fails.
func Backdated(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), backdatedName)
	if err := BuildBackdated(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// BuildBackdated builds the backdated-commit guard in dir, which is created
// when missing and must otherwise be empty. Cancelling ctx kills the running
// git process and returns its error.
func BuildBackdated(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	backdatedHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build backdated repository in %s: %w", dir, b.err)
	}
	return nil
}

// MergedRevert builds the merged-revert guard of testdata/fixture/SPEC.md in
// a new temporary directory and returns its absolute path: a work tree named
// "merged-revert", on branch main, where two reverts sit on side branches
// that --no-ff merges land, and neither merge's message names a revert. It
// stops the test with t.Fatal if the build fails.
func MergedRevert(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), mergedRevertName)
	if err := BuildMergedRevert(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// BuildMergedRevert builds the merged-revert guard in dir, which is created
// when missing and must otherwise be empty. Cancelling ctx kills the running
// git process and returns its error.
func BuildMergedRevert(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	mergedRevertHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build merged-revert repository in %s: %w", dir, b.err)
	}
	return nil
}

// Binary builds the binary-file guard of testdata/fixture/SPEC.md in a new
// temporary directory and returns its absolute path: a work tree named
// "binary", on branch main, where git takes some prod files for binary at
// some commits, so those commits' added lines are unknown while blame still
// attributes every line: a file added with a NUL byte beside a text file and
// renamed later, and a file that is text, then binary, then text again. It
// stops the test with t.Fatal if the build fails.
func Binary(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), binaryName)
	if err := BuildBinary(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// BuildBinary builds the binary-file guard in dir, which is created when
// missing and must otherwise be empty. Cancelling ctx kills the running git
// process and returns its error.
func BuildBinary(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	binaryHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build binary repository in %s: %w", dir, b.err)
	}
	return nil
}

// backdatedHistory writes the Backdated history table of SPEC: one row per
// commit, in table order, each commented with its row ID.
//
// X is committed on top of B2 with a committer date of 2025-06-01, as git
// rebase --committer-date-is-author-date leaves a commit written long ago.
// Plain --since stops a --first-parent walk at the first commit older than
// the window, so it would print A2 and A1 and stop at X, hiding B2 and B1.
// A1 is a merge, so the landing map is built in one walk through X, whose
// own claim must be discarded rather than given to A1.
//
// A2 is the other way round: Cal's commit of 2025-04-14, rebased onto main
// by Bea on 2026-08-24, keeps its author and author date. It lands by its
// committer time, inside the window, and is credited to its author, so a
// collector that chooses or dates landings by author time, or credits the
// committer, moves an oracle.
func backdatedHistory(b *builder) {
	bea := person{"Bea Below", "bea@example.com"}
	cal := person{"Cal Below", "cal@example.com"}
	dot := person{"Dot Dated", "dot@example.com"}

	b.initRepo()
	b.git("config", "commit.gpgsign", "false")

	b.row(noon(2026, time.February, 2), bea) // B1: the root, a durable@1 cohort landing
	b.write("bea.py", numbered("bea", 4))
	b.commit("feat: add bea.py")

	b.row(noon(2026, time.August, 3), cal) // B2: a 90-day window landing
	b.write("cal.py", numbered("cal", 2))
	b.commit("feat: add cal.py")

	b.row(noon(2025, time.June, 1), dot) // X: older than the scan window, above B1 and B2
	b.write("dot.py", numbered("dot", 1))
	b.commit("feat: add dot.py")

	b.git("switch", "--quiet", "--create", "side")
	b.row(noon(2026, time.August, 10), cal) // S1: on side, landed by A1
	b.write("side.py", numbered("side", 2))
	b.commit("feat: add side.py")

	b.row(noon(2026, time.August, 17), bea) // A1: a window landing above X
	b.git("switch", "--quiet", "main")
	b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge branch 'side'", "side")

	b.rowAs(noon(2025, time.April, 14), cal, noon(2026, time.August, 24), bea) // A2: Cal's, rebased onto main by Bea
	b.write("rebased.py", numbered("rebased", 2))
	b.commit("feat: add rebased.py")
}

// mergedRevertHistory writes the Merged revert history table of SPEC: one
// row per commit, in table order, each commented with its row ID.
//
// Each revert is made on a side branch and landed by a --no-ff merge whose
// message names no revert, as a pull request merged with GitHub's default
// merge method lands: only a commit the merge brought in says "This reverts
// commit <sha>.". M1 lands a delete-then-revert in the durable@1 cohort,
// where R1's restored lines would earn durable lines if the revert went
// unseen; M2 lands an add-then-revert in the 90-day window, where R2's
// removed lines would earn simplifying@1 lines and a simplifying landing.
func mergedRevertHistory(b *builder) {
	nia := person{"Nia Normal", "nia@example.com"}
	wes := person{"Wes Reverted", "wes@example.com"}
	rex := person{"Rex Revert", "rex@example.com"}
	mia := person{"Mia Merge", "mia@example.com"}

	// revertOn records a revert of sha, by who at when, on a new branch
	// named branch, and its --no-ff merge into main by mia at merged.
	revertOn := func(branch, sha string, when, merged time.Time, who person) {
		b.git("switch", "--quiet", "--create", branch)
		b.row(when, who)
		b.git("revert", "--no-edit", sha)
		b.row(merged, mia)
		b.git("switch", "--quiet", "main")
		b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge branch '"+branch+"'", branch)
	}

	b.initRepo()
	b.git("config", "commit.gpgsign", "false")

	b.row(noon(2025, time.September, 15), nia) // U0: more than 365 days old, not a landing
	b.write("legacy.py", numbered("legacy", 6))
	b.commit("chore: import legacy.py")

	b.row(noon(2026, time.January, 12), wes) // D1: R1 reverts it
	b.write("legacy.py", span("legacy", 5, 6))
	b.commit("refactor: drop legacy_1 to legacy_4")
	d1 := b.head()
	revertOn("undo-d1", d1, noon(2026, time.January, 19), noon(2026, time.January, 26), rex) // R1, then M1

	b.row(noon(2026, time.February, 9), nia) // N1
	b.write("nia.py", numbered("nia", 3))
	b.commit("feat: add nia.py")

	b.row(noon(2026, time.August, 3), wes) // X2: R2 reverts it
	b.write("wes.py", numbered("wes", 20))
	b.commit("feat: add wes.py")
	x2 := b.head()
	revertOn("undo-x2", x2, noon(2026, time.August, 10), noon(2026, time.August, 17), rex) // R2, then M2

	b.row(noon(2026, time.August, 24), nia) // N2
	b.write("nia_w.py", numbered("nia_w", 1))
	b.commit("feat: add nia_w.py")
}

// binaryHistory writes the Binary history table of SPEC: one row per commit,
// in table order, each commented with its row ID. Every row is in durable@1's
// cohort.
//
// A NUL byte in a file's first 8,000 bytes makes git take it for binary in a
// diff whose old or new side holds one: numstat prints "-" "-" and the own
// patch has no hunks, so History gives the change no lines. Blame reads no
// such check and attributes every line. B1 is #54's case: bin.py is binary
// from its first commit, and R1 renames it, so its lines are blamed to B1
// in moved.py at HEAD. flip.py is text at T1, binary at T2, which changes
// two of its lines, and text again at T3, which takes the NUL byte out: at
// HEAD it is text, and T1, T2 and T3 each hold one of its lines or more.
func binaryHistory(b *builder) {
	ann := person{"Ann Binary", "ann@example.com"}
	flo := person{"Flo Flip", "flo@example.com"}

	b.initRepo()
	b.git("config", "commit.gpgsign", "false")

	b.row(noon(2026, time.February, 2), ann) // B1: a binary prod file beside a text one
	b.write("bin.py", lines("bin_1 = 1", "bin_2 = 2"+nul, "bin_3 = 3"))
	b.write("core.py", numbered("core", 2))
	b.commit("feat: add bin.py and core.py")

	b.row(noon(2026, time.March, 2), flo) // T1: flip.py is text
	b.write("flip.py", numbered("flip", 4))
	b.commit("feat: add flip.py")

	b.row(noon(2026, time.April, 6), flo) // T2: flip.py turns binary
	b.write("flip.py", lines("flip_1 = 1", "flip_2 = 2"+nul, "flip_3 = 3", "flip_4 = 40"))
	b.write("flo.py", numbered("flo", 1))
	b.commit("feat: change flip.py and add flo.py")

	b.row(noon(2026, time.May, 4), flo) // T3: flip.py is text again
	b.write("flip.py", lines("flip_1 = 1", "flip_2 = 20", "flip_3 = 3", "flip_4 = 40"))
	b.commit("fix: take the NUL byte out of flip.py")

	b.row(noon(2026, time.June, 1), ann) // R1: B1's binary file, renamed
	b.git("mv", "bin.py", "moved.py")
	b.commit("refactor: rename bin.py to moved.py")
}
