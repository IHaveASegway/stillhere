package app_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// identityAddress is one row of a SPEC identity candidate's address table.
type identityAddress struct {
	Email, Name, Domain string
	Commits, CoAuthored int
	First, Last         string
	Canonical           bool
}

// identityCandidate is one SPEC identity candidate.
type identityCandidate struct {
	Confidence string
	Guess      bool
	Rules      string // comma-separated, in the order of the rules
	Name       string
	Addresses  []identityAddress
}

// The SPEC candidates (SPEC, "Identity candidates (M2, #26)").
var (
	identAda = identityCandidate{"medium", false, "same-name,same-local-part", "Ada Example", []identityAddress{
		{"ada@example.com", "Ada Example", "main", 2, 0, "2026-01-01T12:00:00Z", "2026-01-05T12:00:00Z", true},
		{"ada@other.example", "Ada Example", "other", 2, 0, "2026-01-02T12:00:00Z", "2026-10-06T18:00:00Z", false},
	}}
	identGinaHank = identityCandidate{"low", true, "hand-off", "Hank Split", []identityAddress{
		{"gina@example.com", "Gina Split", "main", 1, 0, "2026-01-06T12:00:00Z", "2026-01-06T12:00:00Z", false},
		{"hank@example.com", "Hank Split", "main", 1, 0, "2026-01-07T12:00:00Z", "2026-01-07T12:00:00Z", true},
	}}
	identPia = identityCandidate{"medium", false, "same-name", "Pia Pair", []identityAddress{
		{"81234567+piapair@users.noreply.github.com", "Pia Pair", "noreply", 0, 1, "2026-02-02T12:00:00Z", "2026-02-02T12:00:00Z", false},
		{"pia@example.com", "Pia Pair", "main", 3, 0, "2026-02-16T12:00:00Z", "2026-07-29T12:00:00Z", true},
	}}
)

// identityOracles checks SPEC's identity candidates: app.Identities over a
// scan of each fixture, without its mailmap and with it, and the suggested
// mailmap lines through git check-mailmap.
func identityOracles(t *testing.T) {
	const table = "SPEC Identity candidates"
	main, sibling := fixture.Main(t), fixture.Sibling(t)
	identities := func(repo, mailmap string) *report.Identities {
		t.Helper()
		s := scan(t, mailmap, nil, app.ScanRequest{Paths: []string{repo}, Now: fixture.Now})
		id, err := app.Identities(t.Context(), s.read, app.IdentitiesRequest{RunID: s.run})
		if err != nil {
			t.Fatalf("app.Identities: %v", err)
		}
		return id
	}
	mainBare, siblingBare := identities(main, ""), identities(sibling, "")
	for _, c := range []struct {
		scan       string
		id         *report.Identities
		addresses  int
		bots       int
		candidates []identityCandidate
	}{
		{"main fixture, no --mailmap", mainBare, 11, 1, []identityCandidate{identAda, identGinaHank}},
		{"main fixture, MailmapText", identities(main, fixture.Mailmap(t)), 10, 1, []identityCandidate{identGinaHank}},
		{"sibling, no --mailmap", siblingBare, 7, 3, []identityCandidate{identPia}},
		{"sibling, SiblingMailmapText", identities(sibling, fixture.SiblingMailmap(t)), 6, 3, nil},
	} {
		id := c.id
		if id.Addresses != c.addresses || id.Bots != c.bots || id.Shared != 0 || id.Oversized != 0 {
			t.Errorf("%s, %s: addresses %d, bots %d, shared %d, oversized %d; want %d, %d, 0, 0",
				table, c.scan, id.Addresses, id.Bots, id.Shared, id.Oversized, c.addresses, c.bots)
		}
		if got, want := describeCandidates(id.Candidates), describeWant(c.candidates); got != want {
			t.Errorf("%s, %s: candidates\n%s\nwant\n%s", table, c.scan, got, want)
		}
	}
	for _, cand := range mainBare.Candidates {
		for _, l := range cand.Links {
			switch {
			case l.Rule == "same-local-part" && !l.FromName:
				t.Errorf("%s, Ada: the same-local-part link is not from_name: %+v", table, l)
			case l.Rule == "hand-off" && (l.HandOff == nil || l.HandOff.GapDays != 1 || l.HandOff.SharedFiles != 1 ||
				len(l.HandOff.Files) != 1 || l.HandOff.Files[0].Path != "split.py"):
				t.Errorf("%s, Gina → Hank: hand-off %+v, want a 1-day gap and one shared file, split.py", table, l.HandOff)
			}
		}
	}

	// The mailmap form of the scans without a mailmap holds the three lines
	// commented out, so adding it unread merges nobody; uncommented, each
	// maps its alias to the suggested person.
	var out bytes.Buffer
	for _, id := range []*report.Identities{mainBare, siblingBare} {
		if err := report.Mailmap(&out, id); err != nil {
			t.Fatal(err)
		}
	}
	lines := []string{
		"# Ada Example <ada@example.com> <ada@other.example>",
		"# Hank Split <hank@example.com> <gina@example.com>",
		"# Pia Pair <pia@example.com> <81234567+piapair@users.noreply.github.com>",
	}
	for _, l := range lines {
		if !strings.Contains(out.String(), "\n"+l+"\n") {
			t.Errorf("%s: the mailmap form has no line %q:\n%s", table, l, out.String())
		}
	}
	for _, l := range strings.Split(out.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			t.Errorf("%s: the mailmap form has a line that is not a comment: %q", table, l)
		}
	}
	uncommented := out.String()
	for _, l := range lines {
		uncommented = strings.Replace(uncommented, l, strings.TrimPrefix(l, "# "), 1)
	}
	for _, c := range []struct{ dir, alias, printed, uncommented string }{
		{main, "<ada@other.example>", "<ada@other.example>", "Ada Example <ada@example.com>"},
		{sibling, "<81234567+piapair@users.noreply.github.com>", "<81234567+piapair@users.noreply.github.com>", "Pia Pair <pia@example.com>"},
		{main, "<gina@example.com>", "<gina@example.com>", "Hank Split <hank@example.com>"},
	} {
		if got := checkMailmap(t, c.dir, out.String(), c.alias); got != c.printed {
			t.Errorf("%s: git check-mailmap %s with the mailmap form as printed = %q, want it unchanged", table, c.alias, got)
		}
		if got := checkMailmap(t, c.dir, uncommented, c.alias); got != c.uncommented {
			t.Errorf("%s: git check-mailmap %s with the lines uncommented = %q, want %q", table, c.alias, got, c.uncommented)
		}
	}
}

// describeCandidates writes the candidates' fields that SPEC's tables give,
// one line per address, so a failure shows the whole difference.
func describeCandidates(cs []report.IdentityCandidate) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s guess=%t commented=%t rules=%s name=%q\n", c.Confidence, c.Guess, c.Commented, strings.Join(c.Rules, ","), c.Name)
		for _, a := range c.Addresses {
			fmt.Fprintf(&b, "  %s %q %s %d/%d %s %s canonical=%t %v\n", a.Email, a.Name, a.DomainKind, a.Commits, a.CoAuthored,
				rfc3339(a.First), rfc3339(a.Last), a.Canonical, a.Repos)
		}
	}
	return b.String()
}

// describeWant writes SPEC's candidates as describeCandidates writes the
// answer's: every one is commented out, and each address is in the one
// repository of its fixture.
func describeWant(cs []identityCandidate) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s guess=%t commented=true rules=%s name=%q\n", c.Confidence, c.Guess, c.Rules, c.Name)
		repo := "local:fixture"
		if c.Name == "Pia Pair" {
			repo = "local:sibling"
		}
		for _, a := range c.Addresses {
			fmt.Fprintf(&b, "  %s %q %s %d/%d %s %s canonical=%t [%s]\n", a.Email, a.Name, a.Domain, a.Commits, a.CoAuthored,
				a.First, a.Last, a.Canonical, repo)
		}
	}
	return b.String()
}

// checkMailmap runs git check-mailmap for contact in dir through the
// runner, with text as the organisation mailmap file.
func checkMailmap(t *testing.T, dir, text, contact string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailmap")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	git, err := gitcli.New(t.Context(), gitcli.Config{MailmapFile: path})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	out, err := git.Run(t.Context(), domain.GitCmd{Dir: dir, Args: []string{"check-mailmap", contact}})
	if err != nil {
		t.Fatalf("git check-mailmap %s: %v", contact, err)
	}
	return strings.TrimSpace(string(out))
}
