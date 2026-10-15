package collect

import (
	"io"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
)

// TestBlamePorcelain checks that git blame's two porcelain forms parse to
// the same counts (docs/spec/blame-collector.md#porcelain-parsing):
// --line-porcelain, which Blame runs and which repeats a commit's author
// lines in every record, and --porcelain, which prints them only the first
// time the commit appears. It reads porcelain git printed, captured or
// live, never porcelain written by hand.
func TestBlamePorcelain(t *testing.T) {
	t.Parallel()

	t.Run("captured", func(t *testing.T) {
		t.Parallel()
		ada := domain.Person{Name: "Ada Example", Email: "ada@example.com"}
		ann := domain.Person{Name: "Ann", Email: "ann@example.com"}
		ben := domain.Person{Name: "Ben", Email: "ben@example.com"}
		for _, tc := range []struct {
			name  string
			forms []string // the same file's blame in each form git printed
			want  []domain.LineOwner
		}{{
			// The size-3 group of service.py in SPEC's measured blame: three
			// records, the middle one a header without a group size whose
			// content is a lone tab. Three records, two lines.
			name:  "spec",
			forms: []string{blameSpecGroup},
			want: []domain.LineOwner{{
				Path: "f", Commit: "e9db5aab2e60fc152a91b4249739fcb8d0c7cfc2",
				Author: ada, AuthorTime: time.Unix(1767268800, 0).UTC(), Lines: 2,
			}},
		}, {
			// Ann writes three lines and Ben rewrites the middle one. In
			// --porcelain, Ann's third line comes back without author lines,
			// so a parser that carries the previous record's author gives it
			// to Ben. Ann's commit spells her address Ann@Example.COM, and
			// git prints it so: the key must still be ann@example.com, as
			// the history collector lowercases %aE, or one person splits in
			// two between blame and history.
			name:  "ann-ben-ann",
			forms: []string{blameABALinePorcelain, blameABAPorcelain},
			want: []domain.LineOwner{{
				Path: "f", Commit: "10cb948814dba5f798813e5b31502392922c2766",
				Author: ann, AuthorTime: time.Unix(1767268800, 0).UTC(), Lines: 2,
			}, {
				Path: "f", Commit: "a5203000798708d126f950e9888a4e05421b70b7",
				Author: ben, AuthorTime: time.Unix(1767355200, 0).UTC(), Lines: 1,
			}},
		}} {
			for i, form := range tc.forms {
				got, err := blameParse(strings.NewReader(form), "f")
				if err != nil {
					t.Fatalf("%s, form %d: %v", tc.name, i, err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s, form %d:\n got %+v\nwant %+v", tc.name, i, got, tc.want)
				}
			}
		}
	})

	t.Run("fixture", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		git := newRunner(t, fixture.Mailmap(t))
		dir := fixture.Main(t)
		out, err := git.Run(ctx, domain.GitCmd{Dir: dir, Args: []string{"rev-parse", "--verify", "HEAD^{commit}"}})
		if err != nil {
			t.Fatal(err)
		}
		head := strings.TrimSpace(string(out))
		b := &blamer{git: git, repo: domain.Repo{Path: dir, Head: head}, timeout: blameTimeout}

		lines := make(map[string]int) // prod non-blank lines per email
		for _, path := range blameMainProdPaths {
			linePorcelain := b.file(ctx, path)
			if linePorcelain.err != nil {
				t.Fatalf("%s: --line-porcelain: %v", path, linePorcelain.err)
			}
			var porcelain []domain.LineOwner
			err := git.Stream(ctx, domain.GitCmd{
				Dir:  dir,
				Args: []string{"blame", "-w", "-M", "--porcelain", "--no-ignore-revs-file", head, "--", path},
			}, func(r io.Reader) error {
				var err error
				porcelain, err = blameParse(r, path)
				return err
			})
			if err != nil {
				t.Fatalf("%s: --porcelain: %v", path, err)
			}
			if !reflect.DeepEqual(linePorcelain.owners, porcelain) {
				t.Errorf("%s: the forms disagree:\n--line-porcelain %+v\n     --porcelain %+v", path, linePorcelain.owners, porcelain)
			}
			for _, o := range linePorcelain.owners {
				lines[o.Author.Email] += o.Lines
			}
			if path == "service.py" {
				blameCheckService(t, linePorcelain.owners)
			}
		}

		// The still-here oracle: every prod file blamed by hand
		// (testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle).
		want := map[string]int{
			"ada@example.com":     10,
			"cara@example.com":    9,
			"bob@example.com":     7,
			"frank@example.com":   2,
			"gina@example.com":    2,
			"hank@example.com":    2,
			"dana@example.com":    1,
			"erin@example.com":    1,
			"copilot@bot.example": 3,
		}
		if !maps.Equal(lines, want) {
			t.Errorf("prod non-blank lines per email:\n got %v\nwant %v", lines, want)
		}
	})
}

// blameCheckService checks service.py against SPEC's measured blame, by
// author time rather than by commit ID: the init commit keeps beta and
// alpha, 4 non-blank lines, because -M credits the moved block to it and
// the blank inside alpha does not count; gamma's 2 lines are Ada's through
// the mailmap; delta's 2 are Bob's.
func blameCheckService(t *testing.T, owners []domain.LineOwner) {
	t.Helper()
	type row struct {
		email string
		lines int
	}
	got := make(map[time.Time]row)
	for _, o := range owners {
		got[o.AuthorTime] = row{o.Author.Email, o.Lines}
	}
	want := map[time.Time]row{
		time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC): {"ada@example.com", 4},
		time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC): {"ada@example.com", 2},
		time.Date(2026, time.October, 6, 13, 0, 0, 0, time.UTC): {"bob@example.com", 2},
	}
	if !maps.Equal(got, want) {
		t.Errorf("service.py by author time:\n got %v\nwant %v", got, want)
	}
}

// blameMainProdPaths are the paths of the main fixture whose role is prod:
// the files_with_majority list of SPEC's still-here oracle.
var blameMainProdPaths = []string{
	"ada_week.py", "bot.py", "kept.py", "main_only.py", "new_name.py",
	"service.py", "side.py", "side_week.py", "spaced.py", "split.py",
}

// blamePorcelainText joins captured porcelain lines. Content lines are
// written with an explicit "\t", so the lone tab of a blank line stays
// visible.
func blamePorcelainText(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// blameSpecGroup is the --line-porcelain of service.py's size-3 group,
// copied from testdata/fixture/SPEC.md#measured-blame (git 2.50.1).
var blameSpecGroup = blamePorcelainText(
	"e9db5aab2e60fc152a91b4249739fcb8d0c7cfc2 1 3 3",
	"author Ada Example",
	"author-mail <ada@example.com>",
	"author-time 1767268800",
	"author-tz +0000",
	"committer Ada Example",
	"committer-mail <ada@example.com>",
	"committer-time 1767268800",
	"committer-tz +0000",
	"summary init",
	"boundary",
	"filename app.py",
	"\tdef alpha_moved_block():",
	"e9db5aab2e60fc152a91b4249739fcb8d0c7cfc2 2 4",
	"author Ada Example",
	"author-mail <ada@example.com>",
	"author-time 1767268800",
	"author-tz +0000",
	"committer Ada Example",
	"committer-mail <ada@example.com>",
	"committer-time 1767268800",
	"committer-tz +0000",
	"summary init",
	"boundary",
	"filename app.py",
	"\t",
	"e9db5aab2e60fc152a91b4249739fcb8d0c7cfc2 3 5",
	"author Ada Example",
	"author-mail <ada@example.com>",
	"author-time 1767268800",
	"author-tz +0000",
	"committer Ada Example",
	"committer-mail <ada@example.com>",
	"committer-time 1767268800",
	"committer-tz +0000",
	"summary init",
	"boundary",
	"filename app.py",
	"\t    return \"alpha moved block kept\"",
)

// blameABALinePorcelain and blameABAPorcelain are git 2.50.1's blame -w -M
// of f.py in a two-commit repository: Ann <Ann@Example.COM> (2026-01-01)
// writes ann_one, ann_two and ann_three; Ben <ben@example.com> (2026-01-02)
// replaces ann_two with ben_two. Captured on 2026-10-14.
var (
	blameABALinePorcelain = blamePorcelainText(
		"10cb948814dba5f798813e5b31502392922c2766 1 1 1",
		"author Ann",
		"author-mail <Ann@Example.COM>",
		"author-time 1767268800",
		"author-tz +0000",
		"committer Ann",
		"committer-mail <Ann@Example.COM>",
		"committer-time 1767268800",
		"committer-tz +0000",
		"summary ann writes f.py",
		"boundary",
		"filename f.py",
		"\tann_one = 1",
		"a5203000798708d126f950e9888a4e05421b70b7 2 2 1",
		"author Ben",
		"author-mail <ben@example.com>",
		"author-time 1767355200",
		"author-tz +0000",
		"committer Ben",
		"committer-mail <ben@example.com>",
		"committer-time 1767355200",
		"committer-tz +0000",
		"summary ben rewrites the middle line",
		"previous 10cb948814dba5f798813e5b31502392922c2766 f.py",
		"filename f.py",
		"\tben_two = 22",
		"10cb948814dba5f798813e5b31502392922c2766 3 3 1",
		"author Ann",
		"author-mail <Ann@Example.COM>",
		"author-time 1767268800",
		"author-tz +0000",
		"committer Ann",
		"committer-mail <Ann@Example.COM>",
		"committer-time 1767268800",
		"committer-tz +0000",
		"summary ann writes f.py",
		"boundary",
		"filename f.py",
		"\tann_three = 3",
	)
	blameABAPorcelain = blamePorcelainText(
		"10cb948814dba5f798813e5b31502392922c2766 1 1 1",
		"author Ann",
		"author-mail <Ann@Example.COM>",
		"author-time 1767268800",
		"author-tz +0000",
		"committer Ann",
		"committer-mail <Ann@Example.COM>",
		"committer-time 1767268800",
		"committer-tz +0000",
		"summary ann writes f.py",
		"boundary",
		"filename f.py",
		"\tann_one = 1",
		"a5203000798708d126f950e9888a4e05421b70b7 2 2 1",
		"author Ben",
		"author-mail <ben@example.com>",
		"author-time 1767355200",
		"author-tz +0000",
		"committer Ben",
		"committer-mail <ben@example.com>",
		"committer-time 1767355200",
		"committer-tz +0000",
		"summary ben rewrites the middle line",
		"previous 10cb948814dba5f798813e5b31502392922c2766 f.py",
		"filename f.py",
		"\tben_two = 22",
		"10cb948814dba5f798813e5b31502392922c2766 3 3 1",
		"\tann_three = 3",
	)
)
