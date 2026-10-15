package collect

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// historyRenameStream is a git fast-import stream: a root commit on
// 2026-01-01 that creates every source path, then one commit on 2026-01-02
// that renames them in each form git's numstat can print, and modifies two
// files whose names look like what a parser must not split. Each comment
// shows the path field git prints without -z. fast-import takes a path
// either raw to the end of the line or C-quoted.
const historyRenameStream = `commit refs/heads/main
author Ren Root <ren@example.com> 1767268800 +0000
committer Ren Root <ren@example.com> 1767268800 +0000
data <<EOF
create the sources
EOF
M 100644 inline dir/old/file.py
data <<EOF
file_1 = 1
file_2 = 2
EOF
M 100644 inline old/file.py
data <<EOF
brace_1 = 1
EOF
M 100644 inline x/y/z.py
data <<EOF
z_1 = 1
EOF
M 100644 inline lib/a.py
data <<EOF
lib_a_1 = 1
EOF
M 100644 inline old_name.py
data <<EOF
name_1 = 1
name_2 = 2
EOF
M 100644 inline a
data <<EOF
arrow_1 = 1
EOF
M 100644 inline "sp ace/f\"q.py"
data <<EOF
quote_1 = 1
quote_2 = 2
quote_3 = 3
EOF
M 100644 inline "\303\274/na\303\257ve.py"
data <<EOF
utf8_1 = 1
utf8_2 = 2
utf8_3 = 3
EOF
M 100644 inline {x => y}.py
data <<EOF
looks_1 = 1
EOF
M 100644 inline "tab\there.py"
data <<EOF
tab_1 = 1
EOF

commit refs/heads/main
author Ren Mover <mover@example.com> 1767355200 +0000
committer Ren Mover <mover@example.com> 1767355200 +0000
data <<EOF
rename in every form
EOF
D dir/old/file.py
M 100644 inline dir/new/file.py
data <<EOF
file_1 = 1
file_2 = 2

file_3 = 3
EOF
D old/file.py
M 100644 inline new/file.py
data <<EOF
brace_1 = 1
EOF
D x/y/z.py
M 100644 inline x/z.py
data <<EOF
z_1 = 1
EOF
D lib/a.py
M 100644 inline lib/deep/a.py
data <<EOF
lib_a_1 = 1
EOF
D old_name.py
M 100644 inline new_name.py
data <<EOF
name_1 = 1
name_2 = 2
name_3 = 3
EOF
D a
M 100644 inline b => c
data <<EOF
arrow_1 = 1
EOF
D "sp ace/f\"q.py"
M 100644 inline "sp ace/f\"q2.py"
data <<EOF
quote_1 = 1
quote_2 = 2
quote_3 = 3
quote_4 = 4
EOF
D "\303\274/na\303\257ve.py"
M 100644 inline "\303\274/na\303\257ve2.py"
data <<EOF
utf8_1 = 1
utf8_2 = 2
utf8_3 = 3
utf8_4 = 4
EOF
M 100644 inline {x => y}.py
data <<EOF
looks_1 = 1
looks_2 = 2
EOF
M 100644 inline "tab\there.py"
data <<EOF
tab_1 = 1
tab_2 = 2
EOF

`

// TestNumstatRename checks that every form in which git prints a path in
// numstat reaches the facts as the destination path, the rename source,
// and the lines the commit's own diff added, in week and scan runs alike
// (docs/spec/week-collector.md#parsing). History reads numstat with -z, so
// git prints each path exactly as stored; without -z it would print the
// forms in the comments below, and each comment names the parser of that
// form that this test catches.
func TestNumstatRename(t *testing.T) {
	git := newRunner(t, "")
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--template=", "--initial-branch=main", "--object-format=sha1"},
		{"fast-import", "--quiet"},
	} {
		cmd := domain.GitCmd{Dir: dir, Args: args}
		if args[0] == "fast-import" {
			cmd.Stdin = strings.NewReader(historyRenameStream)
		}
		if _, err := git.Run(t.Context(), cmd); err != nil {
			t.Fatalf("git %s: %v", args[0], err)
		}
	}
	out, err := git.Run(t.Context(), domain.GitCmd{Dir: dir, Args: []string{"rev-parse", "refs/heads/main"}})
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	head := strings.TrimSpace(string(out))
	repo := domain.Repo{Key: "local:renames", Name: "renames", Path: dir, Branch: "main", Head: head}
	window := domain.Window{
		Since: time.Date(2025, time.December, 31, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC),
	}

	// change is one expected row of the rename commit: numstat, then the
	// non-blank lines of the commit's own patch (a scan fact).
	type change struct {
		path, oldPath          string
		added, removed, nonBlk int
	}
	want := []change{
		// "dir/{old => new}/file.py": the brace form, destination
		// prefix + new + suffix. Two lines added, one of them blank.
		{"dir/new/file.py", "dir/old/file.py", 2, 0, 1},
		// "{old => new}/file.py": braces at the start of the path.
		{"new/file.py", "old/file.py", 0, 0, 0},
		// "x/{y => }/z.py": prefix + new + suffix gives "x//z.py".
		{"x/z.py", "x/y/z.py", 0, 0, 0},
		// "lib/{ => deep}/a.py": an empty source between the braces.
		{"lib/deep/a.py", "lib/a.py", 0, 0, 0},
		// "old_name.py => new_name.py": no common prefix or suffix.
		{"new_name.py", "old_name.py", 1, 0, 1},
		// "a => b => c": a destination that contains " => " is
		// ambiguous; it is not "a => b" renamed to "c".
		{"b => c", "a", 0, 0, 0},
		// "\"sp ace/f\\\"q.py\" => \"sp ace/f\\\"q2.py\"": quoted names,
		// which no brace rule covers. The patch quotes the path too and,
		// because it holds a space, ends its "+++" line with a tab.
		{"sp ace/f\"q2.py", "sp ace/f\"q.py", 1, 0, 1},
		// "\"\\303\\274/na\\303\\257ve.py\" => ...": octal escapes,
		// which must become the path's own bytes, in numstat and patch.
		{"ü/naïve2.py", "ü/naïve.py", 1, 0, 1},
		// "{x => y}.py": not a rename at all, but a modified file whose
		// name looks like one; a brace parser credits "y.py".
		{"{x => y}.py", "", 1, 0, 1},
		// "\"tab\\there.py\"": a quoted name that is not a rename.
		{"tab\there.py", "", 1, 0, 1},
	}
	slices.SortFunc(want, func(a, b change) int { return strings.Compare(a.path, b.path) })

	for _, mode := range []HistoryMode{HistoryWeek, HistoryScan} {
		facts, err := History(t.Context(), git, repo, HistoryOptions{Window: window, Mode: mode})
		if err != nil {
			t.Fatalf("mode %d: History: %v", mode, err)
		}
		if len(facts.Landings) != 2 || facts.Warnings != nil {
			t.Errorf("mode %d: %d landings and warnings %v; want 2 landings and no warning",
				mode, len(facts.Landings), facts.Warnings)
		}
		var got []domain.FileChange
		for _, fc := range facts.Changes {
			if fc.Commit == head {
				got = append(got, fc)
			}
		}
		if len(got) != len(want) {
			t.Errorf("mode %d: the rename commit has %d rows, want %d: %+v", mode, len(got), len(want), got)
			continue
		}
		for i, w := range want {
			nonBlank, removedNonBlank := -1, -1 // not collected in week runs
			if mode == HistoryScan {
				nonBlank, removedNonBlank = w.nonBlk, 0
			}
			wantRow := domain.FileChange{
				Commit: head, Path: w.path, OldPath: w.oldPath,
				Added: w.added, Removed: w.removed,
				AddedNonBlank: nonBlank, RemovedNonBlank: removedNonBlank,
			}
			if got[i] != wantRow {
				t.Errorf("mode %d: row %d\n got %+v\nwant %+v", mode, i, got[i], wantRow)
			}
		}
	}
}
