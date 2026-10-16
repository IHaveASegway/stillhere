package app_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
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
	// #103: GitHub attributes both of Ada's addresses to the account ada.
	identAdaForge = identityCandidate{"high", false, "same-name,same-local-part,same-forge-account", "Ada Example", identAda.Addresses}
	// #53: with a .mailmap line that maps jules@ to ada@other.example
	// beside MailmapText, git gives ada@example.com everything Ada wrote,
	// and ada@other.example only the commit written as jules@.
	identAdaChained = identityCandidate{"medium", false, "same-name,same-local-part", "Ada Example", []identityAddress{
		{"ada@example.com", "Ada Example", "main", 4, 0, "2026-01-01T12:00:00Z", "2026-10-06T18:00:00Z", true},
		{"ada@other.example", "Ada Example", "other", 1, 0, "2026-10-06T20:00:00Z", "2026-10-06T20:00:00Z", false},
	}}
)

// identChainMailmap is the #53 case's organisation mailmap: MailmapText,
// and a line that folds jules@ into ada@other.example, the address
// MailmapText folds into ada@example.com. git follows no chain, so the
// commit written as jules@ stays at ada@other.example.
const identChainMailmap = fixture.MailmapText + "Ada Example <ada@other.example> <jules@example.com>\n"

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
		if got, want := describeCandidates(id.Candidates), describeWant(c.candidates, ""); got != want {
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
	identityForge(t, table)
	identityChain(t, table)
}

// identityForge checks SPEC's same-forge-account case (#103) and what it
// does to the two metrics that count people holding lines (#104): the main
// fixture without its mailmap, with an origin and a replica of one pull
// request, squashed before the history window, whose two commits GitHub
// attributes to the account ada (one as Ada), written as Ada's two
// addresses. The run keeps the pull request's links only (its window holds
// no pull request), and Ada's candidate is high: its line, written
// uncommented, is MailmapText.
func identityForge(t *testing.T, table string) {
	repo := fixture.Main(t)
	setOrigin(t, repo, reviewRepo.Key)
	old := time.Date(2025, time.June, 2, 12, 0, 0, 0, time.UTC)
	pull := domain.PullRequest{ID: "PR_9", Repo: reviewRepo.ID, Number: 9, Title: "t9", Author: domain.ForgeUser{Login: "ada"},
		State: domain.PullMerged, BaseBranch: "main", Created: old.Add(-24 * time.Hour), Updated: old, Closed: old, Merged: old,
		MergeCommit: domain.ForgeCommit{SHA: strings.Repeat("9", 40), AuthorEmail: "ada@example.com", AuthorLogin: "ada"},
		Commits:     []domain.ForgeCommit{{SHA: strings.Repeat("8", 40), AuthorEmail: "ada@other.example", AuthorLogin: "Ada"}}}
	s := scan(t, "", nil, app.ScanRequest{Paths: []string{repo}, Now: fixture.Now}, func(st *sqlite.Store) {
		reviewSeed(t, st, reviewRepo, []domain.PullRequest{pull}, old)
	})
	id, err := app.Identities(t.Context(), s.read, app.IdentitiesRequest{RunID: s.run})
	if err != nil {
		t.Fatalf("app.Identities: %v", err)
	}
	if got, want := describeCandidates(id.Candidates), describeWant([]identityCandidate{identAdaForge, identGinaHank}, reviewRepo.Key); got != want {
		t.Errorf("%s, same-forge-account: candidates\n%s\nwant\n%s", table, got, want)
	}
	var out bytes.Buffer
	if err := report.Mailmap(&out, id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n"+strings.TrimSuffix(fixture.MailmapText, "\n")+"\n") {
		t.Errorf("%s, same-forge-account: the mailmap form does not write MailmapText as a line:\n%s", table, out.String())
	}

	// #104: 11 / 34 as keyed (SPEC's no-mailmap case), 4 / 34 with Ada
	// merged, as with her mailmap, and 4 / 34 with Gina and Hank merged
	// too, both quiet; concentration 3 authors as keyed, 2 merged.
	r := onlyRepo(t, s.model)
	loss, conc := r.Card.KnowledgeLoss, r.Card.Concentration
	wantLoss := &report.ShareIfMerged{Candidates: 2, High: 1, HighNum: 4, HighValue: 4.0 / 34, AllNum: 4, AllValue: 4.0 / 34}
	if loss == nil || loss.Num != 11 || loss.Den != 34 || loss.IfMerged == nil || *loss.IfMerged != *wantLoss {
		t.Errorf("%s, same-forge-account: knowledge_loss@1 is %+v, want 11 / 34 with if_merged %+v", table, loss, wantLoss)
	}
	wantConc := report.ConcentrationIfMerged{Candidates: 2, High: 1, HighAuthors: 2, AllAuthors: 2}
	if conc == nil || conc.AuthorsInPrefix != 3 || conc.IfMerged == nil || *conc.IfMerged != wantConc {
		t.Errorf("%s, same-forge-account: knowledge_concentration@1 is %+v, want 3 authors with if_merged %+v", table, conc, wantConc)
	}
	e := s.explain(t, domain.MetricID{Name: "knowledge_loss", Version: 1}, domain.Subject{Kind: domain.SubjectRepo, Key: r.Key})
	for what, cs := range map[string][]report.Caveat{"the card": r.Card.Caveats, "explain": e.Caveats} {
		if !slices.ContainsFunc(cs, func(c report.Caveat) bool {
			return c.Code == "unmerged-identities" && strings.Contains(c.Message, "2 identity candidates hold lines here") &&
				strings.Contains(c.Message, "Knowledge loss is 32.4% as the run keys people, 11.8% with the 1 high-confidence one merged")
		}) {
			t.Errorf("%s, same-forge-account: %s has no unmerged-identities caveat with the range: %+v", table, what, cs)
		}
	}
}

// identityChain checks SPEC's mailmap-chain case (#53): the main fixture
// with identChainMailmap. The run records what git rewrote, so Ada's
// suggestion maps jules@ to ada@example.com, where git does not follow
// MailmapText's line, and writes no line for ada@other.example, which
// MailmapText already maps there.
func identityChain(t *testing.T, table string) {
	repo := fixture.Main(t)
	s := scan(t, mailmapFile(t, identChainMailmap), nil, app.ScanRequest{Paths: []string{repo}, Now: fixture.Now})
	id, err := app.Identities(t.Context(), s.read, app.IdentitiesRequest{RunID: s.run, Mailmap: true})
	if err != nil {
		t.Fatalf("app.Identities: %v", err)
	}
	if id.Addresses != 10 || id.Bots != 1 {
		t.Errorf("%s, mailmap chain: addresses %d, bots %d; want 10, 1", table, id.Addresses, id.Bots)
	}
	if got, want := describeCandidates(id.Candidates), describeWant([]identityCandidate{identAdaChained, identGinaHank}, ""); got != want {
		t.Errorf("%s, mailmap chain: candidates\n%s\nwant\n%s", table, got, want)
	}
	if len(id.Candidates) == 0 {
		return
	}
	ada := id.Candidates[0]
	aliases := map[string][]string{}
	for _, a := range ada.Addresses {
		for _, al := range a.Aliases {
			aliases[a.Email] = append(aliases[a.Email], al.Email)
		}
	}
	if !reflect.DeepEqual(aliases, map[string][]string{
		"ada@example.com": {"ada@other.example"}, "ada@other.example": {"jules@example.com"},
	}) {
		t.Errorf("%s, mailmap chain: aliases %v, want ada@other.example on ada@example.com and jules@ on ada@other.example", table, aliases)
	}
	line := "# Ada Example <ada@example.com> <jules@example.com>"
	if ada.Mailmap == nil || !reflect.DeepEqual(*ada.Mailmap, []string{line}) {
		t.Errorf("%s, mailmap chain: Ada's lines %v, want [%s]", table, ada.Mailmap, line)
	}
	for _, c := range []struct{ text, want string }{
		{identChainMailmap, "Ada Example <ada@other.example>"},
		{identChainMailmap + strings.TrimPrefix(line, "# ") + "\n", "Ada Example <ada@example.com>"},
	} {
		if got := checkMailmap(t, repo, c.text, "<jules@example.com>"); got != c.want {
			t.Errorf("%s, mailmap chain: git check-mailmap <jules@example.com> = %q, want %q", table, got, c.want)
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
// answer's: every one but a high one is commented out, and each address is
// in the one repository of its fixture, or in key when it is not "".
func describeWant(cs []identityCandidate, key string) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s guess=%t commented=%t rules=%s name=%q\n", c.Confidence, c.Guess, c.Confidence != "high", c.Rules, c.Name)
		repo := "local:fixture"
		if c.Name == "Pia Pair" {
			repo = "local:sibling"
		}
		if key != "" {
			repo = key
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
