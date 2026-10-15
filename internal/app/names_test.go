package app_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// namesHistory is the history of the repository TestDeterminism's names
// subtest scans: one commit per row, each adding a prod file of the given
// number of lines, by an author whose display name the reports may not
// print as git recorded it (#52). git commit refuses an empty name, so the
// repository is written with git fast-import, which records any.
var namesHistory = []struct {
	day    time.Time
	author string // as a commit's author line holds it: "Name <email>", or "<email>" without a name
	file   string
	lines  int
}{
	// A login used as a name, and the same name on another address,
	// whose local part it is not.
	{namesDay(time.March, 2), "jdoe <jdoe@corp.example>", "login.py", 6},
	{namesDay(time.March, 3), "jdoe <john.doe@mail.example>", "cross.py", 5},
	// An address typed as a name.
	{namesDay(time.March, 4), "mary.major@corp.example <mary.major@corp.example>", "typed.py", 4},
	// Two people without a name, and a committer named like a label.
	{namesDay(time.March, 5), "<anon@corp.example>", "anon.py", 3},
	{namesDay(time.March, 6), "<ghost@corp.example>", "ghost.py", 2},
	{namesDay(time.March, 9), "(no name 1) <tricky@corp.example>", "tricky.py", 2},
	// A name two addresses share, which is shown.
	{namesDay(time.March, 10), "Sam Same <sam@corp.example>", "sam.py", 3},
	{namesDay(time.March, 11), "Sam Same <sam@other.example>", "same.py", 2},
	// A bot whose name is an address, and one whose name is its own
	// noreply login, which is shown.
	{namesDay(time.October, 2), "renovate@corp.example <renovate@corp.example>", "renovate.py", 1},
	{namesDay(time.October, 5), "dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>", "deps.py", 1},
}

// namesDay is noon UTC on a day of 2026, the fixtures' year.
func namesDay(month time.Month, day int) time.Time {
	return time.Date(2026, month, day, 12, 0, 0, 0, time.UTC)
}

// namesSecrets are what no text or Markdown page of the names repository
// may hold: the local parts its display names would give away, and every
// domain, since no page prints an address.
var namesSecrets = []string{"jdoe", "mary.major", "corp.example", "mail.example", "other.example", "noreply"}

// namesLabel finds a label a page gives a person it cannot name, and not a
// name printed quoted because it looks like one.
var namesLabel = regexp.MustCompile(`(?:^|[^"])\(((no name|name withheld) [0-9]+)\)`)

// displayNames scans the names repository twice, into separate facts.db
// files, and checks how every page names its people (#52,
// docs/spec/report-json.md, "Where emails go"): the two scans, and report
// on the stored run, render byte for byte alike, labels included; no text
// or Markdown page, who-knows included, holds a local part a display name
// would give away, or any domain; the two people without a name get two
// labels and the four withheld names four, while the committer named
// "(no name 1)" is printed quoted, never as a label; and the JSON withholds
// a name on every address that carries it, and a bot's name that is an
// address, but not dependabot[bot], which is only its own noreply login.
func displayNames(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "names")
	buildNames(t, dir)
	req := app.ScanRequest{Paths: []string{dir}, Now: fixture.Now, Rank: []domain.MetricID{durable}}
	first, second := scan(t, "", nil, req), scan(t, "", nil, req)
	a, b := render(t, withoutTimings(first.model)), render(t, withoutTimings(second.model))
	sameBytes(t, "the names repository's two scans' JSON without timings", a.json, b.json)
	sameBytes(t, "the names repository's two scans' text", a.text, b.text)
	sameBytes(t, "the names repository's two scans' Markdown", a.markdown, b.markdown)
	m, err := app.Report(t.Context(), first.read, app.ReportRequest{RunID: first.run})
	if err != nil {
		t.Fatalf("app.Report: %v", err)
	}
	rep := render(t, withoutTimings(m))
	sameBytes(t, "the names repository's report text and its scan's", rep.text, a.text)
	sameBytes(t, "the names repository's report Markdown and its scan's", rep.markdown, a.markdown)
	who := render(t, first.whoKnows(t, ""))

	for _, page := range []struct {
		name string
		data []byte
	}{{"report text", a.text}, {"report Markdown", a.markdown}, {"who-knows text", who.text}, {"who-knows Markdown", who.markdown}} {
		lower := bytes.ToLower(page.data)
		for _, s := range namesSecrets {
			if i := bytes.Index(lower, []byte(s)); i >= 0 {
				t.Errorf("the names repository's %s holds %q: %q", page.name, s, page.data[max(0, i-40):min(len(page.data), i+40)])
			}
		}
	}
	// jdoe's two addresses, Mary's and the renovate bot's names are
	// withheld, and anon and ghost have none: one label each, by kind.
	byKind := map[string]map[string]bool{}
	for _, l := range namesLabel.FindAllSubmatch(a.text, -1) {
		kind := string(l[2])
		if byKind[kind] == nil {
			byKind[kind] = map[string]bool{}
		}
		byKind[kind][string(l[1])] = true
	}
	labels := map[string]int{}
	for kind, ls := range byKind {
		labels[kind] = len(ls)
	}
	if want := map[string]int{"no name": 2, "name withheld": 4}; fmt.Sprint(labels) != fmt.Sprint(want) {
		t.Errorf("the names repository's report gives %v labels, want %v:\n%s", labels, want, a.text)
	}
	if !bytes.Contains(a.text, []byte(`"(no name 1)"`)) || !bytes.Contains(a.markdown, []byte(`"(no name 1)"`)) {
		t.Errorf("the committer named (no name 1) is not printed quoted:\n%s", a.text)
	}

	type shown struct {
		name     string
		withheld bool
	}
	got := map[string]shown{}
	for _, ov := range first.model.Overviews {
		for _, rows := range [][]report.StillHereRow{ov.People.Humans, ov.People.Bots} {
			for _, r := range rows {
				got[r.Email] = shown{r.Name, r.NameWithheld}
			}
		}
	}
	sameRows(t, "the names repository's still-here names", got, map[string]shown{
		"jdoe@corp.example":                                 {"", true},
		"john.doe@mail.example":                             {"", true},
		"mary.major@corp.example":                           {"", true},
		"anon@corp.example":                                 {"", false},
		"ghost@corp.example":                                {"", false},
		"tricky@corp.example":                               {"(no name 1)", false},
		"sam@corp.example":                                  {"Sam Same", false},
		"sam@other.example":                                 {"Sam Same", false},
		"renovate@corp.example":                             {"", true},
		"49699333+dependabot[bot]@users.noreply.github.com": {"dependabot[bot]", false},
	})
}

// buildNames writes the names repository in dir with git fast-import,
// through the runner the scans use, as TestNumstatRename writes its
// repository: on branch main, with SHA-1 object IDs and no checkout, which
// a scan does not read.
func buildNames(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git, err := gitcli.New(t.Context(), gitcli.Config{})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	var stream strings.Builder
	for _, c := range namesHistory {
		when := fmt.Sprintf("%d +0000", c.day.Unix())
		msg := "feat: add " + c.file + "\n"
		var body strings.Builder
		for i := 1; i <= c.lines; i++ {
			fmt.Fprintf(&body, "%s_%d = %d\n", strings.TrimSuffix(c.file, ".py"), i, i)
		}
		fmt.Fprintf(&stream, "commit refs/heads/main\nauthor %s %s\ncommitter %s %s\ndata %d\n%s",
			c.author, when, c.author, when, len(msg), msg)
		fmt.Fprintf(&stream, "M 100644 inline %s\ndata %d\n%s\n", c.file, body.Len(), body.String())
	}
	for _, args := range [][]string{
		{"init", "--quiet", "--template=", "--initial-branch=main", "--object-format=sha1"},
		{"fast-import", "--quiet"},
	} {
		cmd := domain.GitCmd{Dir: dir, Args: args}
		if args[0] == "fast-import" {
			cmd.Stdin = strings.NewReader(stream.String())
		}
		if _, err := git.Run(t.Context(), cmd); err != nil {
			t.Fatalf("git %s: %v", args[0], err)
		}
	}
}
