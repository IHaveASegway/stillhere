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
	day     time.Time
	author  string // as a commit's author line holds it: "Name <email>", or "<email>" without a name
	file    string
	lines   int
	subject string // "" for "feat: add <file>"
}{
	// A login used as a name, and the same name on another address,
	// whose local part it is not.
	{namesDay(time.March, 2), "jdoe <jdoe@corp.example>", "login.py", 6, ""},
	{namesDay(time.March, 3), "jdoe <john.doe@mail.example>", "cross.py", 5, ""},
	// An address typed as a name.
	{namesDay(time.March, 4), "mary.major@corp.example <mary.major@corp.example>", "typed.py", 4, ""},
	// Two people without a name, and a committer named like a label.
	{namesDay(time.March, 5), "<anon@corp.example>", "anon.py", 3, ""},
	{namesDay(time.March, 6), "<ghost@corp.example>", "ghost.py", 2, ""},
	{namesDay(time.March, 9), "(no name 1) <tricky@corp.example>", "tricky.py", 2, ""},
	// A name two addresses share, written two ways, which is shown, and
	// which the identities rules read as one name (#69); and a default name
	// two addresses share, which they read as no name at all.
	{namesDay(time.March, 10), "Sam Same <sam@corp.example>", "sam.py", 3, ""},
	{namesDay(time.March, 11), "SAM SAME. <sam@other.example>", "same.py", 2, ""},
	// A subject that names an address no one in the run has, one a person
	// of the run has, and one in a dotless domain of a person of the run
	// who is on no page with it, which no page prints (SEC-3, #90).
	{namesDay(time.March, 12), "Sam Same <sam@corp.example>", "thanks.py", 2,
		"thanks dave.private@hidden.example, jdoe@corp.example, root@buildbox"},
	{namesDay(time.March, 15), "Root Builder <root@buildbox>", "root.py", 2, ""},
	{namesDay(time.March, 13), "Your Name <you@corp.example>", "yours.py", 2, ""},
	{namesDay(time.March, 14), "Your Name <you@mail.example>", "yours2.py", 2, ""},
	// Wave 11: a person who changed user.name from a handle to their full
	// name, whose newest name is theirs though the handle has more commits
	// (round 3's org F11: week, which reads a week, showed the new name and
	// scan, which reads a year, the old one); and a person a later
	// Co-authored-by trailer spells otherwise, whose own name stays (Ruby
	// F11). A name counts by its commits, not its lines, so they hold one
	// line each, below every row the text table's first 10 must show.
	{namesDay(time.March, 16), "klane42 <kit@corp.example>", "kit1.py", 0, ""},
	{namesDay(time.March, 17), "klane42 <kit@corp.example>", "kit2.py", 0, ""},
	{namesDay(time.March, 18), "Kit Lane <kit@corp.example>", "kit3.py", 1, ""},
	{namesDay(time.March, 19), "Pat Doe <pat@corp.example>", "pat.py", 1, ""},
	{namesDay(time.March, 20), "Sam Same <sam@corp.example>", "pair.py", 0,
		"feat: add pair.py\n\nCo-authored-by: pd <pat@corp.example>"},
	// A name a person and a team's info mailbox share, which no identity
	// rule links, so the caveat counts it apart (round 3's TS F10).
	{namesDay(time.March, 21), "Lee Role <lee@corp.example>", "lee.py", 1, ""},
	{namesDay(time.March, 22), "Lee Role <info@corp.example>", "info.py", 1, ""},
	// A bot whose name is an address, and one whose name is its own
	// noreply login, which is shown.
	{namesDay(time.October, 2), "renovate@corp.example <renovate@corp.example>", "renovate.py", 1, ""},
	{namesDay(time.October, 5), "dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>", "deps.py", 1, ""},
}

// namesDay is noon UTC on a day of 2026, the fixtures' year.
func namesDay(month time.Month, day int) time.Time {
	return time.Date(2026, month, day, 12, 0, 0, 0, time.UTC)
}

// namesSecrets are what no text or Markdown page of the names repository
// may hold: the local parts its display names would give away, and every
// domain, since no page prints an address.
var namesSecrets = []string{"jdoe", "mary.major", "corp.example", "mail.example", "other.example", "noreply", "buildbox"}

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
	// Sam's subject names a customer's address, jdoe's and root's: explain
	// prints it in every format, each address as the label, root's too,
	// whom the page does not show and whose dotless domain looks like no
	// address: the page withholds every address of the run (#90). JSON
	// keeps an email only where a person's email goes, Sam's own in the
	// subject's key.
	sam, err := app.Explain(t.Context(), first.read, app.ExplainRequest{RunID: first.run, Metric: durable, Subject: person("sam@corp.example")})
	if err != nil {
		t.Fatalf("app.Explain(durable@1, sam@corp.example): %v", err)
	}
	explained := render(t, sam)
	for _, page := range []struct {
		name string
		data []byte
	}{{"text", explained.text}, {"Markdown", explained.markdown}, {"JSON", explained.json}} {
		lower := bytes.ToLower(page.data)
		// The text table cuts the title after the second label.
		if !bytes.Contains(page.data, []byte("thanks (address withheld), (address withheld)")) ||
			bytes.Contains(lower, []byte("dave.private")) || bytes.Contains(lower, []byte("hidden.example")) ||
			bytes.Contains(lower, []byte("jdoe")) || bytes.Contains(lower, []byte("buildbox")) {
			t.Errorf("explain of Sam's durable@1 as %s does not print Sam's subject with both addresses withheld:\n%s", page.name, page.data)
		}
	}

	for _, page := range []struct {
		name string
		data []byte
	}{{"report text", a.text}, {"report Markdown", a.markdown}, {"who-knows text", who.text}, {"who-knows Markdown", who.markdown},
		{"explain text", explained.text}, {"explain Markdown", explained.markdown}} {
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
		"jdoe@corp.example":       {"", true},
		"john.doe@mail.example":   {"", true},
		"mary.major@corp.example": {"", true},
		"anon@corp.example":       {"", false},
		"ghost@corp.example":      {"", false},
		"tricky@corp.example":     {"(no name 1)", false},
		"sam@corp.example":        {"Sam Same", false},
		"sam@other.example":       {"SAM SAME.", false},
		"you@corp.example":        {"Your Name", false},
		"you@mail.example":        {"Your Name", false},
		"root@buildbox":           {"Root Builder", false},
		"kit@corp.example":        {"Kit Lane", false},
		"pat@corp.example":        {"Pat Doe", false},
		"lee@corp.example":        {"Lee Role", false},
		"info@corp.example":       {"Lee Role", false},
		"renovate@corp.example":   {"", true},
		"49699333+dependabot[bot]@users.noreply.github.com": {"dependabot[bot]", false},
	})

	// The unmerged-identities caveat reads names as the identities command
	// does (identity.Directory.NameKey, #69): Sam's two ways are one name,
	// "Your Name" is a default and no name, and jdoe's, shared and withheld,
	// is counted. who-knows, which lists both Sams as owners, says so too,
	// and of the whole repository it gives the card's knowledge numbers, so
	// it says, in the same caveat, what the card's says of the identity
	// candidates who hold lines there (#104). Lee's name, which a team's
	// info mailbox shares, is counted apart: identities compares no role
	// address, so it suggests no line (wave 11, round 3's TS F10).
	const unmerged = `2 display names belong to more than one email and were not merged: "SAM SAME." and 1 name withheld ` +
		`because it gives an address away. If the emails of a name are one person, a .mailmap line merges them: ` +
		`'stillhere identities' suggests the lines, with the evidence for each. 1 more display name belongs to more ` +
		`than one email, but no identity rule links their emails (an address several people use, such as a team or ` +
		`info mailbox, two GitHub accounts, or a name more than 10 addresses carry), so 'stillhere identities' ` +
		`suggests no line for them: their numbers stay apart, as two people's would.`
	messages := func(caveats []report.Caveat) []string {
		var got []string
		for _, c := range caveats {
			if c.Code == "unmerged-identities" {
				got = append(got, c.Message)
			}
		}
		return got
	}
	if got := messages(first.model.Caveats); len(got) != 1 || got[0] != unmerged {
		t.Errorf("the names repository's report says %q of unmerged identities, want %q", got, unmerged)
	}
	card := messages(first.model.Repos[0].Card.Caveats)
	if got := messages(first.whoKnows(t, "").Caveats); len(card) != 1 || !strings.HasPrefix(card[0], "2 identity candidates hold lines here: ") ||
		len(got) != 1 || got[0] != unmerged+" "+card[0] {
		t.Errorf("the names repository's who-knows says %q of unmerged identities, want %q and the card's %q", got, unmerged, card)
	}
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
		if c.subject != "" {
			msg = c.subject + "\n"
		}
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
