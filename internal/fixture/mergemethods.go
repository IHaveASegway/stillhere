package fixture

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// This file builds the merge-methods guard of testdata/fixture/SPEC.md
// (#38's review): the first-parent shapes GitHub's merge methods leave, which
// who_knows@1 maps pull requests to landings by. A rebase merge puts each of
// a pull request's commits on the branch with a new SHA and one committer
// second, and two squash merges that a merge queue or a bot lands can share
// a second too. The main fixture holds no two first-parent commits with one
// committer time, and adding them would move its oracles, so the guard is a
// repository of its own, as the regression guards in guards.go are.

// mergeMethodsName is the base name of the directory the guard builds into.
const mergeMethodsName = "merge-methods"

// MergeMethods builds the merge-methods guard of testdata/fixture/SPEC.md in
// a new temporary directory and returns its absolute path: a work tree named
// "merge-methods", on branch main, where a pull request's two rebased commits
// share a committer second, and two squash commits of two pull requests share
// another. It stops the test with t.Fatal if the build fails.
func MergeMethods(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), mergeMethodsName)
	if err := BuildMergeMethods(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// BuildMergeMethods builds the merge-methods guard in dir, which is created
// when missing and must otherwise be empty. Cancelling ctx kills the running
// git process and returns its error.
func BuildMergeMethods(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	mergeMethodsHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build merge-methods repository in %s: %w", dir, b.err)
	}
	return nil
}

// mergeMethodsHistory writes the merge-methods history table of SPEC: one
// row per commit, in table order, each commented with its row ID. GitHub
// commits every merge, as its merge button does: the author and the author
// date stay the pull request author's, and the committer time is the
// merge's, one second for all the commits a rebase merge writes.
func mergeMethodsHistory(b *builder) {
	ann := person{"Ann Base", "ann@example.com"}
	rob := person{"Rob Rebase", "rob@example.com"}
	ali := person{"Ali Squash", "ali@example.com"}
	bo := person{"Bo Squash", "bo@example.com"}
	github := person{"GitHub", "noreply@github.com"}

	b.initRepo()
	b.git("config", "commit.gpgsign", "false")

	b.row(noon(2026, time.September, 1), ann) // B0: pushed to main, no pull request
	b.write("base.py", numbered("base", 2))
	b.commit("feat: add base.py")

	// #1's rebase merge: GitHub writes both of its commits in one second.
	rebased := noon(2026, time.October, 1)
	b.rowAs(noon(2026, time.September, 28), rob, rebased, github) // R1
	b.write("one.py", numbered("one", 2))
	b.commit("feat: add one.py")

	b.rowAs(noon(2026, time.September, 29), rob, rebased, github) // R2: #1's merge commit
	b.write("two.py", numbered("two", 2))
	b.commit("feat: add two.py")

	// #2's and #3's squash merges, which a merge queue lands in one second.
	squashed := noon(2026, time.October, 2)
	b.rowAs(squashed, ali, squashed, github) // S1: #2's merge commit
	b.write("a.py", numbered("a", 2))
	b.commit("feat: add a.py")

	b.rowAs(squashed, bo, squashed, github) // S2: #3's merge commit
	b.write("b.py", numbered("b", 2))
	b.commit("feat: add b.py")
}
