package fixture

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// This file builds the merge-back guard of testdata/fixture/SPEC.md (wave
// 10): a maintenance branch, stable, that pull requests merge into and that
// carries cherry-picks of fixes already on main, merged back into main. Its
// merges bring changes that are copies of earlier landings' (copies by
// patch id), pull requests merged into another branch, and a merge whose
// every commit is one of those; and a cherry-pick onto main names a pull
// request merged into stable. None of the other fixtures holds a merge-back,
// and adding one would move their oracles, so the guard is a repository of
// its own, as the regression guards in guards.go are.

// mergeBackName is the base name of the directory the guard builds into.
const mergeBackName = "merge-back"

// MergeBack builds the merge-back guard of testdata/fixture/SPEC.md in a new
// temporary directory and returns its absolute path: a work tree named
// "merge-back", on branch main, whose branch stable carries cherry-picks of
// two of main's fixes, a push of its own and three pull requests' squashes,
// and is merged into main three times. It stops the test with t.Fatal if
// the build fails.
func MergeBack(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), mergeBackName)
	if err := BuildMergeBack(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// BuildMergeBack builds the merge-back guard in dir, which is created when
// missing and must otherwise be empty. Cancelling ctx kills the running git
// process and returns its error.
func BuildMergeBack(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	mergeBackHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build merge-back repository in %s: %w", dir, b.err)
	}
	return nil
}

// mergeBackHistory writes the merge-back history table of SPEC: one row per
// commit, in table order, each commented with its row ID. GitHub commits a
// squash and a merge, as its merge button does; a cherry-pick keeps its
// original's author, date and message, and whoever picks it commits it.
// Rows B0 to M1 are in durable@1's cohort, the rest in the 90-day window.
func mergeBackHistory(b *builder) {
	ann := person{"Ann Maint", "ann@example.com"}
	bea := person{"Bea Fix", "bea@example.com"}
	dan := person{"Dan Stable", "dan@example.com"}
	cal := person{"Cal Release", "cal@example.com"}
	github := person{"GitHub", "noreply@github.com"}

	b.initRepo()
	b.git("config", "commit.gpgsign", "false")

	b.row(noon(2026, time.January, 5), ann) // B0: the root, pushed to main
	b.write("lib.py", numbered("lib", 10))
	b.commit("feat: add lib.py")
	b.git("branch", "stable")

	b.rowAs(noon(2026, time.January, 12), bea, noon(2026, time.January, 12), github) // C1: #1's squash on main
	b.write("guard.py", numbered("guard", 4))
	b.commit("fix: add guard.py (#1)")
	c1 := b.head()

	b.git("switch", "--quiet", "stable")
	b.rowAs(noon(2026, time.January, 12), bea, noon(2026, time.January, 19), cal) // K1: C1 picked onto stable, a copy
	b.git("cherry-pick", c1)

	b.row(noon(2026, time.January, 20), dan) // S1: pushed to stable
	b.write("stable.py", numbered("stable", 3))
	b.commit("fix: add stable.py")

	b.git("switch", "--quiet", "main")
	b.row(noon(2026, time.January, 26), ann) // M1: stable merged into main
	b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge branch 'stable'", "stable")

	b.rowAs(noon(2026, time.August, 3), bea, noon(2026, time.August, 3), github) // C2: #9's squash on main
	b.write("lib.py", numbered("lib", 7))
	b.commit("fix: drop lib_8 to lib_10 (#9)")
	c2 := b.head()

	b.git("switch", "--quiet", "stable")
	b.rowAs(noon(2026, time.August, 3), bea, noon(2026, time.August, 5), cal) // K2: C2 picked onto stable, a copy
	b.git("cherry-pick", c2)

	b.rowAs(noon(2026, time.August, 10), dan, noon(2026, time.August, 10), github) // P2: #2's squash into stable
	b.write("tidy.py", numbered("tidy", 2))
	b.commit("fix: add tidy.py (#2)")

	b.rowAs(noon(2026, time.August, 11), dan, noon(2026, time.August, 11), github) // P3: #3's squash into stable
	b.write("more.py", numbered("more", 2))
	b.commit("fix: add more.py (#3)")

	b.row(noon(2026, time.August, 12), cal) // R1: pushed to stable
	b.write("version.py", lines("VERSION = '1.0.1'"))
	b.commit("Release 1.0.1")

	b.git("switch", "--quiet", "main")
	b.rowAs(noon(2026, time.August, 17), ann, noon(2026, time.August, 17), github) // M2: #4, stable merged into main
	b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge pull request #4 from acme/stable", "stable")

	b.git("switch", "--quiet", "stable")
	b.rowAs(noon(2026, time.August, 24), dan, noon(2026, time.August, 24), github) // P5: #5's squash into stable
	b.write("five.py", numbered("five", 2))
	b.commit("fix: add five.py (#5)")

	b.git("switch", "--quiet", "main")
	b.rowAs(noon(2026, time.August, 31), ann, noon(2026, time.August, 31), github) // M3: #6, stable merged into main
	b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge pull request #6 from acme/stable", "stable")

	b.git("switch", "--quiet", "stable")
	b.rowAs(noon(2026, time.September, 7), cal, noon(2026, time.September, 7), github) // P7: #7's squash into stable
	b.write("version.py", lines("VERSION = '1.0.2'"))
	b.commit("Release 1.0.2 (#7)")
	p7 := b.head()

	b.git("switch", "--quiet", "main")
	b.rowAs(noon(2026, time.September, 7), cal, noon(2026, time.September, 8), cal) // C7: P7 picked onto main
	b.git("cherry-pick", p7)

	b.rowAs(noon(2026, time.September, 14), bea, noon(2026, time.September, 14), github) // C8: #8's squash on main
	b.write("extra.py", numbered("extra", 3))
	b.commit("feat: add extra.py (#8)")
}
