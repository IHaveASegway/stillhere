package app_test

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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
	High       []int // the addresses high evidence links (IdentityAddress.High); none: every line is commented out
}

// The SPEC candidates (SPEC, "Identity candidates (M2, #26)").
var (
	identAda = identityCandidate{"medium", false, "same-name,same-local-part", "Ada Example", []identityAddress{
		{"ada@example.com", "Ada Example", "main", 2, 0, "2026-01-01T12:00:00Z", "2026-01-05T12:00:00Z", true},
		{"ada@other.example", "Ada Example", "other", 2, 0, "2026-01-02T12:00:00Z", "2026-10-06T18:00:00Z", false},
	}, nil}
	identGinaHank = identityCandidate{"low", true, "hand-off", "Hank Split", []identityAddress{
		{"gina@example.com", "Gina Split", "main", 1, 0, "2026-01-06T12:00:00Z", "2026-01-06T12:00:00Z", false},
		{"hank@example.com", "Hank Split", "main", 1, 0, "2026-01-07T12:00:00Z", "2026-01-07T12:00:00Z", true},
	}, nil}
	identPia = identityCandidate{"medium", false, "same-name", "Pia Pair", []identityAddress{
		{"81234567+piapair@users.noreply.github.com", "Pia Pair", "noreply", 0, 1, "2026-02-02T12:00:00Z", "2026-02-02T12:00:00Z", false},
		{"pia@example.com", "Pia Pair", "main", 3, 0, "2026-02-16T12:00:00Z", "2026-07-29T12:00:00Z", true},
	}, nil}
	// #103: GitHub attributes both of Ada's addresses to the account ada.
	identAdaForge = identityCandidate{"high", false, "same-name,same-local-part,same-forge-account", "Ada Example",
		identAda.Addresses, []int{0, 1}}
	// #53: with a .mailmap line that maps jules@ to ada@other.example
	// beside MailmapText, git gives ada@example.com everything Ada wrote,
	// and ada@other.example only the commit written as jules@.
	identAdaChained = identityCandidate{"medium", false, "same-name,same-local-part", "Ada Example", []identityAddress{
		{"ada@example.com", "Ada Example", "main", 4, 0, "2026-01-01T12:00:00Z", "2026-10-06T18:00:00Z", true},
		{"ada@other.example", "Ada Example", "other", 1, 0, "2026-10-06T20:00:00Z", "2026-10-06T20:00:00Z", false},
	}, nil}
)

// identChainMailmap is the #53 case's organisation mailmap: MailmapText,
// and a line that folds Jules's commit, by its name and email, into
// ada@other.example, the address MailmapText folds into ada@example.com.
// git follows no chain, so the commit written as jules@ stays at
// ada@other.example; and git prefers a line that matches a name and an
// email over one that matches the email alone, so only a line that names
// Jules Note too replaces it (#53, the review's ID-4).
const identChainMailmap = fixture.MailmapText + "Ada Example <ada@other.example> Jules Note <jules@example.com>\n"

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
	identityRepos(t, table)
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
	// Gina → Hank is a guess, last and counted apart, in JSON too (wave 10).
	if id.Guesses != 1 {
		t.Errorf("%s, same-forge-account: guesses %d, want 1, Gina → Hank", table, id.Guesses)
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
				strings.Contains(c.Message, "Knowledge loss is 32.4% as the run keys people, 11.8% with the high-confidence lines merged")
		}) {
			t.Errorf("%s, same-forge-account: %s has no unmerged-identities caveat with the range: %+v", table, what, cs)
		}
	}
}

// identityChain checks SPEC's mailmap-chain case (#53): the main fixture
// with identChainMailmap. The run records what git rewrote, so Ada's
// suggestion maps Jules Note <jules@> to ada@example.com, where git does not
// follow MailmapText's line, and writes no line for ada@other.example,
// which MailmapText already maps there.
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
	aliases := map[string]int{}
	for _, a := range ada.Addresses {
		aliases[a.Email] = a.Aliases
	}
	if !reflect.DeepEqual(aliases, map[string]int{"ada@example.com": 1, "ada@other.example": 1}) {
		t.Errorf("%s, mailmap chain: aliases %v, want one on each of Ada's addresses", table, aliases)
	}
	line := "# Ada Example <ada@example.com> Jules Note <jules@example.com>"
	if ada.Mailmap == nil || !reflect.DeepEqual(*ada.Mailmap, []string{line}) {
		t.Errorf("%s, mailmap chain: Ada's lines %v, want [%s]", table, ada.Mailmap, line)
	}
	// The suggestion replaces the name-keyed line it follows, and an
	// email-only one as well; a line of the email alone replaces neither
	// a name-keyed line, which git prefers whatever the order.
	emailOnly := fixture.MailmapText + "Ada Example <ada@other.example> <jules@example.com>\n"
	for _, c := range []struct{ text, want string }{
		{identChainMailmap, "Ada Example <ada@other.example>"},
		{identChainMailmap + strings.TrimPrefix(line, "# ") + "\n", "Ada Example <ada@example.com>"},
		{emailOnly + strings.TrimPrefix(line, "# ") + "\n", "Ada Example <ada@example.com>"},
		{identChainMailmap + "Ada Example <ada@example.com> <jules@example.com>\n", "Ada Example <ada@other.example>"},
	} {
		if got := checkMailmap(t, repo, c.text, "Jules Note <jules@example.com>"); got != c.want {
			t.Errorf("%s, mailmap chain: git check-mailmap Jules Note <jules@example.com> = %q, want %q", table, got, c.want)
		}
	}
}

// identRepoCommit is one commit of the two-repository case's histories: an
// author as the commit writes it, the file it adds and its lines, and a
// Co-authored-by trailer when coAuthor is not "".
type identRepoCommit struct {
	day      time.Time
	author   string // "Name <email>"
	file     string
	lines    int
	coAuthor string // "Name <email>"
}

func identDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// The two-repository case (SPEC, "Pat, Kim and Alice: two repositories
// and a replica"). In one, a .mailmap folds pat@old into pat@new; in two,
// which has none, pat@old wrote Pat's older code, and the replica links
// Kim's, Alice's and Zed's addresses to their accounts.
var (
	identOneHistory = []identRepoCommit{
		{identDay(2025, time.November, 3), "Pat Doe <pat@old.example>", "old.py", 3, ""},
		{identDay(2026, time.September, 1), "Pat Doe <pat@new.example>", "new.py", 3, ""},
		{identDay(2026, time.September, 2), "Pat Doe <pat@new.example>", "new2.py", 3, ""},
	}
	identTwoHistory = []identRepoCommit{
		{identDay(2025, time.November, 10), "Pat Doe <pat@old.example>", "pat.py", 4, ""},
		{identDay(2025, time.November, 11), "Pat Doe <pat@old.example>", "pat2.py", 4, ""},
		{identDay(2026, time.August, 1), "kimlee <kim@home.example>", "kim1.py", 2, ""},
		{identDay(2026, time.August, 2), "Kim Lee <kim@home.example>", "kim2.py", 2, ""},
		{identDay(2026, time.August, 3), "Kim Lee <kim@home.example>", "kim3.py", 2, ""},
		{identDay(2026, time.August, 4), "Kim Lee <kim@corp.example>", "corp1.py", 2, "Kim Lee <4242+kimlee@users.noreply.github.com>"},
		{identDay(2026, time.August, 5), "Kim Lee <kim@corp.example>", "corp2.py", 2, ""},
		{identDay(2026, time.August, 6), "Kim Lee <kim@corp.example>", "corp3.py", 2, ""},
		{identDay(2026, time.August, 7), "Kim Lee <kim@corp.example>", "corp4.py", 2, ""},
		{identDay(2026, time.August, 10), "Kim Lee <k.lee@legacy.example>", "klee.py", 2, ""},
		{identDay(2026, time.August, 20), "Alice Ng <alice@x.example>", "alice.py", 2, ""},
		{identDay(2026, time.August, 21), "Alice Ng <ngroe@shared.example>", "box1.py", 2, ""},
		{identDay(2026, time.August, 22), "Bob Roe <ngroe@shared.example>", "box2.py", 2, ""},
		{identDay(2026, time.August, 25), "Zed Ash <111+zed@users.noreply.github.com>", "z1.py", 2, ""},
		{identDay(2026, time.August, 26), "Zed Birch <222+zed@users.noreply.github.com>", "z2.py", 2, ""},
	}
	identTwoRepo = domain.ForgeRepo{ID: "R_two", Key: "https://git.example.test/acme/two.git",
		Owner: "acme", Name: "two", DefaultBranch: "main"}
)

// identityRepos checks SPEC's two-repository case: a line one repository's
// .mailmap applies is no line the other has (#53, the review's ID-1); a
// scoped answer lists the run's candidates with an address in the scope,
// as the card's caveat counts them (#104, ID-C1); GitHub's links tie a
// noreply address by its login and keep the lines they back as lines in a
// candidate that is medium (#103, ID-2), never two account ids (ID-3), and
// a mailbox two people commit as no higher than medium (ID-5); the run
// keeps one link for each address and account (ADR 0011's amendment); and
// a card where merging moves nothing carries no caveat (ID-8).
func identityRepos(t *testing.T, table string) {
	table += ", two repositories"
	group := filepath.Join(t.TempDir(), "ids")
	one, two := filepath.Join(group, "one"), filepath.Join(group, "two")
	buildIdentRepo(t, one, identOneHistory)
	buildIdentRepo(t, two, identTwoHistory)
	// git reads a work tree's .mailmap, tracked or not, in that repository
	// only.
	if err := os.WriteFile(filepath.Join(one, ".mailmap"), []byte("Pat Doe <pat@new.example> <pat@old.example>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setOrigin(t, two, identTwoRepo.Key)
	old := time.Date(2025, time.June, 1, 12, 0, 0, 0, time.UTC)
	link := func(n int, email, login string) domain.ForgeCommit {
		return domain.ForgeCommit{SHA: strings.Repeat("a", 39) + strconv.Itoa(n), AuthorEmail: email, AuthorLogin: login}
	}
	pull := domain.PullRequest{ID: "PR_31", Repo: identTwoRepo.ID, Number: 31, Title: "t31", Author: domain.ForgeUser{Login: "kimlee"},
		State: domain.PullMerged, BaseBranch: "main", Created: old.Add(-24 * time.Hour), Updated: old, Closed: old, Merged: old,
		MergeCommit: link(9, "kim@corp.example", "kimlee"),
		Commits: []domain.ForgeCommit{
			link(1, "kim@home.example", "KimLee"), link(2, "kim@corp.example", "kimlee"),
			link(3, "alice@x.example", "alice"), link(4, "ngroe@shared.example", "alice"),
			link(5, "111+zed@users.noreply.github.com", "zed"), link(6, "222+zed@users.noreply.github.com", "zed"),
			link(7, "kim@home.example", "kimlee"), link(8, "nobody@else.example", "ghost"),
		}}
	s := scan(t, "", nil, app.ScanRequest{Paths: []string{group}, Now: fixture.Now}, func(st *sqlite.Store) {
		reviewSeed(t, st, identTwoRepo, []domain.PullRequest{pull}, old)
	})
	id, err := app.Identities(t.Context(), s.read, app.IdentitiesRequest{RunID: s.run, Mailmap: true})
	if err != nil {
		t.Fatalf("app.Identities: %v", err)
	}
	const repoOne, repoTwo = "local:one", "https://git.example.test/acme/two.git"
	// Wave 10: GitHub's link is high for an address whose names differ too
	// (a name change looks like a mailbox two people share), so Alice's
	// candidate is high, its line written, and its evidence says the names
	// differ and names the account.
	want := `high guess=false commented=false rules=same-forge-account name="Alice Ng"
  alice@x.example "Alice Ng" other 1/0 2026-08-20T12:00:00Z 2026-08-20T12:00:00Z canonical=true high=true [` + repoTwo + `]
  ngroe@shared.example "Bob Roe" other 2/0 2026-08-21T12:00:00Z 2026-08-22T12:00:00Z canonical=false high=true [` + repoTwo + `]
medium guess=false commented=false rules=same-name,same-local-part,same-forge-account name="Kim Lee"
  kim@home.example "Kim Lee" other 3/0 2026-08-01T12:00:00Z 2026-08-03T12:00:00Z canonical=false high=true [` + repoTwo + `]
  4242+kimlee@users.noreply.github.com "Kim Lee" noreply 0/1 2026-08-04T12:00:00Z 2026-08-04T12:00:00Z canonical=false high=true [` + repoTwo + `]
  kim@corp.example "Kim Lee" other 4/0 2026-08-04T12:00:00Z 2026-08-07T12:00:00Z canonical=true high=true [` + repoTwo + `]
  k.lee@legacy.example "Kim Lee" other 1/0 2026-08-10T12:00:00Z 2026-08-10T12:00:00Z canonical=false high=false [` + repoTwo + `]
medium guess=false commented=true rules=same-name,same-local-part name="Pat Doe"
  pat@new.example "Pat Doe" other 3/0 2025-11-03T12:00:00Z 2026-09-02T12:00:00Z canonical=true high=false [` + repoOne + `]
  pat@old.example "Pat Doe" other 2/0 2025-11-10T12:00:00Z 2025-11-11T12:00:00Z canonical=false high=false [` + repoTwo + `]
`
	if got := describeCandidates(id.Candidates); got != want {
		t.Errorf("%s: candidates\n%s\nwant\n%s", table, got, want)
	}
	if id.Addresses != 10 || id.Bots != 0 || id.Shared != 0 || id.Oversized != 0 {
		t.Errorf("%s: addresses %d, bots %d, shared %d, oversized %d; want 10, 0, 0, 0", table, id.Addresses, id.Bots, id.Shared, id.Oversized)
	}
	lines := map[string][]string{}
	for _, c := range id.Candidates {
		if c.Mailmap != nil {
			lines[c.Name] = *c.Mailmap
		}
	}
	wantLines := map[string][]string{
		"Alice Ng": {"Alice Ng <alice@x.example> <ngroe@shared.example>"},
		"Kim Lee": {
			"Kim Lee <kim@corp.example> <kim@home.example>",
			"Kim Lee <kim@corp.example> <4242+kimlee@users.noreply.github.com>",
			"# Kim Lee <kim@corp.example> <k.lee@legacy.example>",
		},
		"Pat Doe": {"# Pat Doe <pat@new.example> <pat@old.example>"},
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Errorf("%s: mailmap lines %q, want %q", table, lines, wantLines)
	}
	// The link names its account only where the login gives no address
	// away: alice is alice@x.example's local part, so it is withheld.
	if alice := id.Candidates[0]; len(alice.Links) != 1 || alice.Links[0].Login != "" || !alice.Links[0].LoginWithheld ||
		alice.Addresses[0].NamesDiffer || alice.Addresses[0].Names != nil || !alice.Addresses[1].NamesDiffer ||
		!slices.Equal(alice.Addresses[1].Names, []string{"Alice Ng", "Bob Roe"}) || alice.Addresses[1].NamesWithheld != 0 {
		t.Errorf("%s: Alice's link %+v and addresses %+v, want the login withheld and said to be, and the second address's "+
			"names differing, Alice Ng and Bob Roe", table, alice.Links, alice.Addresses)
	}
	// The evidence names what it asks the reviewer to check, and why the
	// login is not printed (wave 10's review); the mailmap form, made of
	// the addresses, prints the login; no candidate here is a guess.
	text := strings.Join(strings.Fields(string(render(t, id).text)), " ")
	var mm bytes.Buffer
	if err := report.Mailmap(&mm, id); err != nil {
		t.Fatal(err)
	}
	mailmap := strings.Join(strings.Fields(mm.String()), " ")
	for _, want := range []string{"address 2 is written with several names: Alice Ng and Bob Roe",
		"one account, whose login is withheld as the local part of an address"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: the text answer has no %q:\n%s", table, want, text)
		}
	}
	if !strings.Contains(mailmap, "one account, @alice") || id.Guesses != 0 {
		t.Errorf("%s: guesses %d, mailmap form:\n%s\nwant no guess and @alice in the evidence", table, id.Guesses, mailmap)
	}
	if got := checkMailmap(t, two, "Pat Doe <pat@new.example> <pat@old.example>\n", "<pat@old.example>"); got != "Pat Doe <pat@new.example>" {
		t.Errorf("%s: git check-mailmap <pat@old.example> in two with Pat's line = %q, want Pat Doe <pat@new.example>", table, got)
	}

	// A scoped answer: the run's candidates with an address in the scope,
	// whole, and the scope's own population.
	for _, c := range []struct {
		repo      string
		addresses int
		names     []string
	}{
		{repoOne, 1, []string{"Pat Doe"}},
		{repoTwo, 9, []string{"Alice Ng", "Kim Lee", "Pat Doe"}},
	} {
		id, err := app.Identities(t.Context(), s.read, app.IdentitiesRequest{RunID: s.run,
			Scope: domain.Scope{Kind: domain.ScopeRepo, Key: c.repo}})
		if err != nil {
			t.Fatalf("app.Identities(%s): %v", c.repo, err)
		}
		var names []string
		for _, cand := range id.Candidates {
			names = append(names, cand.Name)
			if cand.Name == "Pat Doe" && len(cand.Addresses) != 2 {
				t.Errorf("%s, scope %s: Pat's candidate has %d addresses, want both", table, c.repo, len(cand.Addresses))
			}
		}
		if id.Addresses != c.addresses || !slices.Equal(names, c.names) {
			t.Errorf("%s, scope %s: %d addresses, candidates %q; want %d, %q", table, c.repo, id.Addresses, names, c.addresses, c.names)
		}
	}

	// The cards: two's knowledge loss moves with Pat merged, and its
	// concentration with Kim's high lines and with every candidate; one's
	// moves with nothing, so it carries if_merged and no caveat.
	cards := map[string]*report.RepoCard{}
	for i := range s.model.Repos {
		cards[s.model.Repos[i].Key] = &s.model.Repos[i].Card
	}
	c1, c2 := cards[repoOne], cards[repoTwo]
	if c1 == nil || c2 == nil {
		t.Fatalf("%s: the report has cards for %v", table, slices.Collect(maps.Keys(cards)))
	}
	wantLoss := report.ShareIfMerged{Candidates: 3, High: 2, HighNum: 8, HighValue: 8.0 / 34, AllNum: 0, AllValue: 0}
	if l := c2.KnowledgeLoss; l == nil || l.Num != 8 || l.Den != 34 || l.IfMerged == nil || *l.IfMerged != wantLoss {
		t.Errorf("%s: two's knowledge_loss@1 is %+v, want 8 / 34 with if_merged %+v", table, l, wantLoss)
	}
	wantConc := report.ConcentrationIfMerged{Candidates: 3, High: 2, HighAuthors: 2, AllAuthors: 1}
	if c := c2.Concentration; c == nil || c.AuthorsInPrefix != 3 || c.IfMerged == nil || *c.IfMerged != wantConc {
		t.Errorf("%s: two's knowledge_concentration@1 is %+v, want 3 authors with if_merged %+v", table, c, wantConc)
	}
	if !slices.ContainsFunc(c2.Caveats, func(c report.Caveat) bool {
		return c.Code == "unmerged-identities" && strings.Contains(c.Message, "3 identity candidates hold lines here") &&
			strings.Contains(c.Message, "Knowledge loss is 23.5% as the run keys people, 23.5% with the high-confidence "+
				"lines merged and 0.0% with every one merged.") &&
			strings.Contains(c.Message, "Majority-owner concentration is 3 authors as keyed, 2 with the high-confidence "+
				"lines merged and 1 with every one merged.")
	}) {
		t.Errorf("%s: two's card has no unmerged-identities caveat with the range: %+v", table, c2.Caveats)
	}
	wantOne := report.ShareIfMerged{Candidates: 1}
	if l := c1.KnowledgeLoss; l == nil || l.Num != 0 || l.Den != 9 || l.IfMerged == nil || *l.IfMerged != wantOne {
		t.Errorf("%s: one's knowledge_loss@1 is %+v, want 0 / 9 with if_merged %+v", table, l, wantOne)
	}
	if slices.ContainsFunc(c1.Caveats, func(c report.Caveat) bool { return c.Code == "unmerged-identities" }) {
		t.Errorf("%s: one's card carries an unmerged-identities caveat although merging moves nothing: %+v", table, c1.Caveats)
	}

	// The run keeps one link for each repository, address and account:
	// not 7, a second link of kim@home's, 8, an address the run never
	// shows, or 9, a second of kim@corp's.
	forge, err := s.store.RunForge(t.Context(), s.run)
	if err != nil {
		t.Fatalf("RunForge: %v", err)
	}
	var kept []string
	for _, l := range forge.Links {
		kept = append(kept, l.SHA[39:])
	}
	if !slices.Equal(kept, []string{"1", "2", "3", "4", "5", "6"}) {
		t.Errorf("%s: the run keeps links %v, want 1 to 6", table, kept)
	}
}

// buildIdentRepo writes one repository of the two-repository case in dir
// with git fast-import, through the runner the scans use, as buildNames
// does: on branch main, with SHA-1 object IDs and no checkout.
func buildIdentRepo(t *testing.T, dir string, history []identRepoCommit) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git, err := gitcli.New(t.Context(), gitcli.Config{})
	if err != nil {
		t.Fatalf("gitcli.New: %v", err)
	}
	var stream strings.Builder
	for _, c := range history {
		when := fmt.Sprintf("%d +0000", c.day.Unix())
		msg := "feat: add " + c.file + "\n"
		if c.coAuthor != "" {
			msg += "\nCo-authored-by: " + c.coAuthor + "\n"
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

// describeCandidates writes the candidates' fields that SPEC's tables give,
// one line per address, so a failure shows the whole difference.
func describeCandidates(cs []report.IdentityCandidate) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s guess=%t commented=%t rules=%s name=%q\n", c.Confidence, c.Guess, c.Commented, strings.Join(c.Rules, ","), c.Name)
		for _, a := range c.Addresses {
			fmt.Fprintf(&b, "  %s %q %s %d/%d %s %s canonical=%t high=%t %v\n", a.Email, a.Name, a.DomainKind, a.Commits,
				a.CoAuthored, rfc3339(a.First), rfc3339(a.Last), a.Canonical, a.High, a.Repos)
		}
	}
	return b.String()
}

// describeWant writes SPEC's candidates as describeCandidates writes the
// answer's: one with no high address is commented out whole, and each
// address is in the one repository of its fixture, or in key when it is
// not "".
func describeWant(cs []identityCandidate, key string) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s guess=%t commented=%t rules=%s name=%q\n", c.Confidence, c.Guess, len(c.High) == 0, c.Rules, c.Name)
		repo := "local:fixture"
		if c.Name == "Pia Pair" {
			repo = "local:sibling"
		}
		if key != "" {
			repo = key
		}
		for i, a := range c.Addresses {
			fmt.Fprintf(&b, "  %s %q %s %d/%d %s %s canonical=%t high=%t [%s]\n", a.Email, a.Name, a.Domain, a.Commits,
				a.CoAuthored, a.First, a.Last, a.Canonical, slices.Contains(c.High, i), repo)
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
