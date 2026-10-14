// Package fixture builds the git repositories that testdata/fixture/SPEC.md
// specifies, with the real git binary, so that every oracle test reads real
// porcelain rather than a recording of it.
//
// fixture is test support. It sits outside the hexagon in
// docs/spec/codebase.md: it runs git directly through os/exec instead of
// domain.GitRunner, and no production package imports it. Oracle tests call
// Main and Mailmap for the main fixture, and Sibling and SiblingMailmap for the
// sibling: a separate repository that holds the change cases (AI-assisted
// trailers, reverts, simplifying deletions, rank eligibility), so they never
// move the main oracles. Build and BuildSibling build the same repositories
// for inspection by hand.
//
// The build is hermetic. Every git process runs without the machine's global
// and system configuration, the installation's templates and hooks, the
// per-user ignore file, the per-user and system attribute files, and any
// inherited GIT_ variable, and every commit names its author, committer and
// date. The repository therefore has the same commit IDs on every machine:
// an oracle that depended on a developer's ~/.gitconfig would not be an
// oracle.
package fixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Now is the --now instant of every oracle in SPEC: 2026-10-07T00:00:00Z.
// The default seven-day week is [2026-09-30T00:00:00Z, Now), and no fixture
// commit sits on either boundary. Now is a variable only because Go has no
// time.Time constants; never assign to it.
var Now = time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC)

// MailmapText is the mailmap the oracles pass with --mailmap. It folds Ada's
// second address into her first. It is deliberately not committed: the
// repository also sets log.mailmap=false, so a collector sees one Ada only
// through -c mailmap.file= and git's mailmapped placeholders (%aN, %aE).
const MailmapText = "Ada Example <ada@example.com> <ada@other.example>\n"

// repoName is the base name of the directory Main builds into. A fixed name
// keeps the repository's name and discovery key ("local:fixture") identical
// in every test, whatever order the test creates temporary directories in.
const repoName = "fixture"

// Main builds the main fixture repository in a new temporary directory and
// returns its absolute path: a work tree named "fixture", on branch main. It
// stops the test with t.Fatal if the build fails.
func Main(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), repoName)
	if err := BuildContext(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Mailmap writes MailmapText to a new temporary file and returns the file's
// path, for --mailmap or git -c mailmap.file=.
func Mailmap(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailmap")
	if err := os.WriteFile(path, []byte(MailmapText), 0o600); err != nil {
		t.Fatalf("fixture: write mailmap: %v", err)
	}
	return path
}

// Build is BuildContext with context.Background, for callers that have no
// context to pass, such as a person building the fixture to inspect it.
func Build(dir string) error {
	return BuildContext(context.Background(), dir)
}

// BuildContext builds the main fixture repository in dir: a work tree on
// branch main whose history is the History table of
// testdata/fixture/SPEC.md, with the feature branch kept. dir is created
// when missing and must otherwise be empty. Cancelling ctx kills the running
// git process and returns its error.
func BuildContext(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	mainHistory(b)
	if b.err != nil {
		return fmt.Errorf("fixture: build main repository in %s: %w", dir, b.err)
	}
	return nil
}

// The four blocks of service.py, which is app.py until 2026-01-05. Alpha and
// beta each hold 43 alphanumeric characters, above the 20 that git blame -M
// needs before it credits a moved block to the commit that wrote it. The
// empty line inside alpha is the blank that blame counters must skip.
const (
	alphaBlock = "def alpha_moved_block():\n" +
		"\n" +
		"    return \"alpha moved block kept\"\n"
	betaBlock = "def beta_stable_block():\n" +
		"    return \"beta stable block here\"\n"
	gammaBlock = "def gamma_added_later():\n" +
		"    return \"gamma added by second mail\"\n"
	deltaBlock = "def delta_bob_added():\n" +
		"    return \"delta added on the branch\"\n"
)

// mainHistory writes the History table of testdata/fixture/SPEC.md: one
// b.row per table row, in table order, then that row's changes. Where SPEC
// gives only a line count, the content is plain non-blank lines, and every
// non-blank line is unique in the repository, so no diff can pair a line
// with a twin.
func mainHistory(b *builder) {
	ada := person{"Ada Example", "ada@example.com"}
	adaOther := person{"Ada Example", "ada@other.example"}
	bob := person{"Bob Builder", "bob@example.com"}
	cara := person{"Cara Dev", "cara@example.com"}
	copilot := person{"GitHub Copilot", "copilot@bot.example"}
	dana := person{"Dana Dev", "dana@example.com"}
	erin := person{"Erin Editor", "erin@example.com"}
	frank := person{"Frank & Sons", "frank@example.com"}
	gina := person{"Gina Split", "gina@example.com"}
	hank := person{"Hank Split", "hank@example.com"}
	ivy := person{"Ivy Tester", "ivy@example.com"}
	jules := person{"Jules Note", "jules@example.com"}

	b.initRepo()
	b.git("config", "log.mailmap", "false") // SPEC Setup: the oracles pass the mailmap themselves
	b.git("config", "commit.gpgsign", "false")

	// app.py, plus one file for each skip and role the oracles exercise.
	b.row(at(time.January, 1, 12), ada)
	b.write("app.py", alphaBlock+betaBlock)
	b.write("tests/test_service.py", lines(
		"def test_service_one():",
		"    assert 1 == 1",
	))
	b.write("go.sum", lines(
		"example.com/dep v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"example.com/dep v1.0.0/go.mod h1:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		"example.com/other v0.1.0/go.mod h1:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=",
	))
	b.write("vendor/lib.py", numbered("vendor_lib", 10))
	b.write("gen_out.py", lines("// Code generated by tool. DO NOT EDIT.")+numbered("gen_out", 5))
	b.write("bootstrap/cache/config.php", lines(
		"<?php",
		"return [",
		"    'debug' => false,",
		"];",
	))
	b.write("old_name.py", lines("x = 1", "y = 2", "z = 3"))
	b.commit("init") // the summary in SPEC's measured porcelain

	b.row(at(time.January, 2, 12), adaOther)
	b.extend("app.py", gammaBlock)
	b.commit("add gamma_added_later")

	// The -M case: the same blocks with alpha below beta. The diff keeps
	// alpha and re-adds beta at the top, so blame without -M credits beta
	// to Bob, and blame with -M credits it to the init commit.
	b.row(at(time.January, 4, 12), bob)
	b.write("app.py", betaBlock+alphaBlock+gammaBlock)
	b.commit("move alpha_moved_block below beta_stable_block")

	b.row(at(time.January, 5, 12), ada)
	b.git("mv", "app.py", "service.py")
	b.commit("rename app.py to service.py")

	b.row(at(time.January, 6, 12), gina)
	b.write("split.py", lines("gina = 1", "gina = 2"))
	b.commit("add split.py")

	b.row(at(time.January, 7, 12), hank)
	b.extend("split.py", lines("hank = 1", "hank = 2"))
	b.commit("extend split.py")

	b.row(at(time.February, 1, 12), cara)
	b.git("switch", "--quiet", "--create", "feature")
	b.write("side.py", numbered("side", 2))
	b.commit("add side.py")

	b.row(at(time.March, 1, 12), bob)
	b.git("switch", "--quiet", "main")
	b.write("main_only.py", numbered("main_only", 5))
	b.commit("add main_only.py")

	b.row(at(time.October, 5, 12), cara)
	b.git("switch", "--quiet", "feature")
	b.write("side_week.py", numbered("side_week", 7))
	b.commit("add side_week.py")

	b.row(at(time.October, 6, 12), bob)
	b.git("switch", "--quiet", "main")
	b.git("merge", "--quiet", "--no-ff", "--no-edit", "--message", "Merge branch 'feature'", "feature")

	b.row(at(time.October, 6, 13), bob)
	b.extend("service.py", deltaBlock)
	b.commit("add delta_bob_added")

	b.row(at(time.October, 6, 14), copilot)
	b.write("bot.py", numbered("bot", 3))
	b.commit("add bot.py")

	b.row(at(time.October, 6, 15), dana)
	b.write("vendor/new.py", numbered("vendor_new", 9))
	b.write("kept.py", lines("kept = 1"))
	b.commit("add vendor/new.py and kept.py")

	b.row(at(time.October, 6, 16), erin)
	b.git("mv", "old_name.py", "new_name.py")
	b.extend("new_name.py", lines("w = 4"))
	b.commit("rename old_name.py to new_name.py")

	b.row(at(time.October, 6, 17), frank)
	b.write("spaced.py", lines("a = 1", "", "b = 2"))
	b.commit("add spaced.py")

	b.row(at(time.October, 6, 18), adaOther)
	b.write("ada_week.py", lines("ada = 1"))
	b.commit("add ada_week.py")

	b.row(at(time.October, 6, 19), ivy)
	b.extend("tests/test_service.py", lines(
		"def test_service_two():",
		"    assert 2 == 2",
		"def test_service_three():",
		"    assert 3 == 3",
	))
	b.write("notes.csv", lines(
		"id,note",
		"1,first",
		"2,second",
		"3,third",
		"4,fourth",
		"5,fifth",
		"6,sixth",
		"7,seventh",
	))
	b.commit("extend tests and add notes.csv")

	b.row(at(time.October, 6, 20), jules)
	b.write("extra.csv", lines(
		"key,value",
		"a,1",
		"b,2",
		"c,3",
	))
	b.commit("add extra.csv")
}
