package report

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The text renderer: a report for a terminal, read top to bottom. What
// could make the numbers wrong comes first, then the overviews (every
// repository, then each group), then the ranks asked for, then every
// repository's card and detail.
// Every untrusted string goes through clean, and no line is wider than
// maxWidth, except a single word (a path, a URL) that cannot be broken.

func (m *Model) writeText(p *printer) {
	textHeader(p, m)
	textCaveats(p, "", "What could make this wrong", m.Caveats, false)
	for i := range m.Overviews {
		p.blank()
		textOverview(p, m, &m.Overviews[i])
	}
	for i := range m.Ranks {
		p.blank()
		textRanks(p, &m.Ranks[i])
	}
	for i := range m.Repos {
		p.blank()
		textRepo(p, m, &m.Repos[i])
	}
	if len(m.Warnings) > 0 {
		p.blank()
		p.line("Warnings")
		for _, w := range m.Warnings {
			p.wrapped("  - "+clean(w.Code)+": ", "    ", clean(w.Message))
		}
	}
}

func textHeader(p *printer, m *Model) {
	title := "stillhere " + m.Kind
	if m.Run != nil {
		title += fmt.Sprintf(" · run %d (%s)", m.Run.ID, clean(m.Run.Status))
	}
	p.line(title + " · as of " + day(m.Now))
	p.wrapped("", "  ", policyText(m, clean))
	p.wrapped("", "  ", windowSummary(m, clean))
	if m.Kind == KindScan {
		p.line(historySummary(m))
	}
	if s := forgeSummary(m); s != "" {
		p.wrapped("", "  ", s)
	}
	p.wrapped("", "  ", repoSummary(m, clean))
	if len(m.Metrics) > 0 {
		p.wrapped("metrics ", "  ", strings.Join(m.Metrics, ", "))
	}
}

// policyText is "policy v1 0123456789ab · headline prod_nonblank_lines ·
// stillhere 0.1.0".
func policyText(m *Model, esc func(string) string) string {
	s := fmt.Sprintf("policy v%d %s", m.Policy.Version, short12(m.Policy.SHA256))
	if m.Policy.Headline != "" {
		s += " · headline " + esc(m.Policy.Headline)
	}
	return s + " · stillhere " + esc(m.Tool.Version)
}

// windowSummary is "landed window 2026-09-30 → 2026-10-07: 7 days (policy),
// sorted by lines_added (policy)".
func windowSummary(m *Model, esc func(string) string) string {
	w := m.Window
	return fmt.Sprintf("landed window %s: %s (%s), sorted by %s (%s)", interval(Interval{Since: w.Since, Until: w.Until}),
		plural(w.Days, "day", "days"), fromText(w.DaysFrom, "--days"), esc(w.Sort), fromText(w.SortFrom, "--week-sort"))
}

// historySummary is "history 2025-10-07 → 2026-10-07: the landings scan
// collected".
func historySummary(m *Model) string {
	return "history " + interval(m.History) + ": the landings the scan collected"
}

// fromText says where a window setting came from: the flag that set it, or
// the policy.
func fromText(from, flag string) string {
	if from == fromCLI {
		return flag
	}
	return "policy"
}

// short12 abbreviates a hash for reading.
func short12(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// repoSummary is "3 repositories: 2 ok, 1 failed · 2 groups: acme and beta".
func repoSummary(m *Model, esc func(string) string) string {
	ok, failed := 0, 0
	for _, r := range m.Repos {
		if r.Status == "ok" {
			ok++
		} else {
			failed++
		}
	}
	s := plural(len(m.Repos), "repository", "repositories") + fmt.Sprintf(": %d ok", ok)
	if failed > 0 {
		s += fmt.Sprintf(", %d failed", failed)
	}
	if len(m.Groups) > 0 {
		var names []string
		for _, g := range m.Groups {
			names = append(names, esc(g.Name))
		}
		s += " · " + plural(len(m.Groups), "group", "groups") + ": " + joinNames(names)
	}
	return s
}

// standing reports whether a caveat is one the top of a report already
// states for every section: nested sections leave it out of text and
// Markdown, while JSON keeps it so each section reads on its own.
func standing(c Caveat) bool { return c.Code == codeNotFetched || c.Code == codeAILowerBound }

func textCaveats(p *printer, indent, title string, cs []Caveat, nested bool) {
	var shown []Caveat
	for _, c := range cs {
		if !nested || !standing(c) {
			shown = append(shown, c)
		}
	}
	if len(shown) == 0 {
		return
	}
	p.blank()
	p.line(indent + title)
	for _, c := range shown {
		msg := clean(c.Message)
		if len(c.Repos) > 0 {
			msg += " (" + repoList(c.Repos, clean) + ")"
		}
		p.wrapped(indent+"  - ", indent+"    ", msg)
	}
}

// repoList names up to ten repositories, then says how many more.
func repoList(names []string, esc func(string) string) string {
	var out []string
	for i, n := range names {
		if i == 10 {
			out = append(out, fmt.Sprintf("%d more", len(names)-10))
			break
		}
		out = append(out, esc(n))
	}
	return joinNames(out)
}

// overviewTitle names an overview's scope: "Group acme", or "Every
// repository".
func overviewTitle(s Scope, esc func(string) string) string {
	if s.Kind == domain.ScopeAll {
		return "Every repository"
	}
	return "Group " + esc(s.Key)
}

// across says what an overview's people rows add up: "the group", or
// "every repository".
func across(s Scope) string {
	if s.Kind == domain.ScopeAll {
		return "every repository"
	}
	return "the group"
}

// sizeText is "37 surviving prod lines in 10 prod files · 12 prod paths at
// HEAD": the surviving part only when surviving@1 has its value.
func sizeText(prodLines, prodFiles *int, prodPaths int) string {
	paths := plural(prodPaths, "prod path", "prod paths") + " at HEAD"
	if prodLines == nil {
		return paths
	}
	s := grouped(*prodLines) + " surviving prod lines"
	if prodFiles != nil {
		s += " in " + plural(*prodFiles, "prod file", "prod files")
	}
	return s + " · " + paths
}

func textOverview(p *printer, m *Model, o *Overview) {
	t := o.Totals
	head := fmt.Sprintf("%s · %s (%d ok", overviewTitle(o.Scope, clean), plural(t.Repos, "repository", "repositories"), t.ReposOK)
	if t.ReposFailed > 0 {
		head += fmt.Sprintf(", %d failed", t.ReposFailed)
	}
	p.wrapped("", "  ", head+")")
	p.wrapped("  ", "  ", peopleSummary(t))
	p.wrapped("  ", "  ", sizeText(t.ProdLines, t.ProdFiles, t.ProdPaths)+" · "+landingsText(m, t.LandingsWindow, t.LandingsHistory))
	if line := shareLine(o.KnowledgeLoss, o.AIAssisted, o.BotShare); line != "" {
		p.wrapped("  ", "  ", line)
	}
	if o.Concentration != nil {
		p.wrapped("  ", "  ", concentrationText(o.Concentration))
	}
	if o.People != nil {
		p.blank()
		p.linef("  Still here: surviving prod non-blank lines, across %s (%s blamed)",
			across(o.Scope), plural(o.People.FilesBlamed, "file", "files"))
		textPeople(p, "    ", o.People)
	}
	if o.Week != nil {
		p.blank()
		textWeek(p, "  ", m, o.Week)
	}
	textCaveats(p, "  ", "What could make this wrong", o.Caveats, true)
}

// peopleSummary is "8 people with surviving prod lines · 7 landed in the
// window · 1 bot"; the first part only when surviving@1 has its value.
func peopleSummary(t Totals) string {
	var parts []string
	if t.People != nil {
		parts = append(parts, plural(*t.People, "person", "people")+" with surviving prod lines")
	}
	parts = append(parts, plural(t.Landers, "person", "people")+" landed in the window", plural(t.Bots, "bot", "bots"))
	return strings.Join(parts, " · ")
}

// shareLine is "knowledge loss 11.8% (4 of 34 human-held lines) · ...".
func shareLine(loss, ai, bots *Share) string {
	var parts []string
	if loss != nil {
		parts = append(parts, "knowledge loss "+share(loss, "human-held lines"))
	}
	if ai != nil {
		parts = append(parts, "AI-assisted "+share(ai, "landings"))
	}
	if bots != nil {
		parts = append(parts, "bot share "+share(bots, "lines"))
	}
	return strings.Join(parts, " · ")
}

// concentrationText is "majority-owner concentration: 2 authors (0 bots)
// hold the majority in half of the 10 files with a majority owner (not a
// truck factor)".
func concentrationText(c *Concentration) string {
	verb := "hold"
	if c.AuthorsInPrefix == 1 {
		verb = "holds"
	}
	return fmt.Sprintf("majority-owner concentration: %s (%s) %s the majority in half of the %s with a majority owner (%s)",
		plural(c.AuthorsInPrefix, "author", "authors"), plural(c.BotsInPrefix, "bot", "bots"), verb,
		plural(c.FilesWithMajority, "file", "files"), c.Label)
}

func textPeople(p *printer, indent string, t *StillHere) {
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.line(indent + "nobody: no surviving lines")
		return
	}
	cols := []column{{head: "person"}, {head: "prod lines", right: true}, {head: "majority files", right: true},
		{head: "repos", right: true}, {head: "landings 365d", right: true}}
	row := func(r StillHereRow) []string {
		return []string{clean(r.Name), grouped(r.ProdNonblankLines), grouped(r.MajorityFiles), grouped(r.Repos), grouped(r.Landings365d)}
	}
	p.textTable(indent, cols, withBots(t.Humans, t.Bots, row, len(cols)))
}

func textStillHere(p *printer, indent string, t *StillHere) {
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.line(indent + "nobody: no surviving lines")
		return
	}
	cols := []column{{head: "person"}, {head: "prod lines", right: true}, {head: "majority files", right: true},
		{head: "other roles"}}
	row := func(r StillHereRow) []string {
		return []string{clean(r.Name), grouped(r.ProdNonblankLines), grouped(r.MajorityFiles), otherRoles(r.LinesByRole)}
	}
	p.textTable(indent, cols, withBots(t.Humans, t.Bots, row, len(cols)))
}

// otherRoles is "test 6 · iac 2": the lines a person holds outside prod.
func otherRoles(byRole map[string]int) string {
	var roles []string
	for role := range byRole {
		if role != "prod" && byRole[role] > 0 {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	var parts []string
	for _, role := range roles {
		parts = append(parts, clean(role)+" "+grouped(byRole[role]))
	}
	return strings.Join(parts, " · ")
}

func textWeek(p *printer, indent string, m *Model, t *WeekTable) {
	w := Interval{Since: m.Window.Since, Until: m.Window.Until}
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.linef("%sWhat landed, %s: nothing", indent, interval(w))
		return
	}
	p.linef("%sWhat landed, %s, by %s", indent, interval(w), strings.ReplaceAll(clean(m.Window.Sort), "_", " "))
	cols := []column{{head: "person"}, {head: "commits", right: true}, {head: "lines added", right: true},
		{head: "prod lines added", right: true}}
	row := func(r WeekRow) []string {
		return []string{clean(r.Name), grouped(r.Commits), grouped(r.LinesAdded), grouped(r.ProdLinesAdded)}
	}
	p.textTable(indent+"  ", cols, withBots(t.Humans, t.Bots, row, len(cols)))
}

// withBots lays out a table's human rows, then a "bots" row, then the bot
// rows, so both sets share one alignment.
func withBots[R any](humans, bots []R, row func(R) []string, width int) [][]string {
	var rows [][]string
	for _, r := range humans {
		rows = append(rows, row(r))
	}
	if len(bots) > 0 {
		label := make([]string, width)
		label[0] = "bots"
		rows = append(rows, label)
		for _, r := range bots {
			rows = append(rows, row(r))
		}
	}
	return rows
}

func textRanks(p *printer, t *RankTable) {
	p.wrapped("", "  ", "Ranks · "+clean(t.Metric)+" · "+scopeText(t.Scope, clean, t.Provenance)+" · "+interval(t.Window)+
		rankForgeAsOf(t))
	if t.Question != "" {
		p.wrapped("  ", "  ", clean(t.Question))
	}
	if len(t.Ranked) == 0 && len(t.BelowMin) == 0 && len(t.Bots) == 0 {
		p.line("  nobody has a value")
	} else {
		cols := []column{{head: "#"}, {head: "person"}, {head: metricName(t.Metric), right: true},
			{head: "95% interval", right: true}, {head: "n", right: true}}
		var rows [][]string
		for _, r := range t.Ranked {
			rows = append(rows, []string{rankText(r.RankLo, r.RankHi), clean(r.Name), estimateText(r.Estimate, t.Percent),
				intervalText(r.Lo, r.Hi, t.Percent), grouped(r.N)})
		}
		for _, r := range t.BelowMin {
			rows = append(rows, []string{"—", clean(r.Name), "", "", grouped(r.N)})
		}
		if len(t.Bots) > 0 {
			rows = append(rows, []string{"", "bots", "", "", ""})
			for _, r := range t.Bots {
				rows = append(rows, []string{"—", clean(r.Name), estimateText(r.Estimate, t.Percent), "", grouped(r.N)})
			}
		}
		p.textTable("  ", cols, rows)
	}
	p.wrapped("  ", "  ", rankFooter(t))
	textCaveats(p, "  ", "What could make this wrong", t.Caveats, true)
}

// rankForgeAsOf dates a rank table of a metric that reads GitHub data, and
// is "" for any other.
func rankForgeAsOf(t *RankTable) string {
	if !forgeDated(t.Provenance) {
		return ""
	}
	return forgeAsOf(t.Provenance.Forge)
}

// rankFooter explains how to read a rank table, as product.md shows it.
func rankFooter(t *RankTable) string {
	return fmt.Sprintf("— is listed, not ranked: fewer than %d changes · intervals are 95%% · "+
		"ranks overlap when intervals do · bots are never ranked", t.MinN)
}

// metricName is the column head for a metric: "durable" for durable@1.
func metricName(id string) string {
	name, _, _ := strings.Cut(id, "@")
	return name
}

func rankText(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return strconv.Itoa(lo) + "–" + strconv.Itoa(hi)
}

func estimateText(f float64, pct bool) string {
	if pct {
		return percent0(f)
	}
	return number(f)
}

func intervalText(lo, hi float64, pct bool) string {
	if pct {
		return strconv.FormatFloat(100*lo, 'f', 0, 64) + "–" + percent0(hi)
	}
	return number(lo) + "–" + number(hi)
}

// scopeText names a scope for reading: a repository by its name, which the
// section's provenance pairs with its key, rather than by the key, which is
// usually a remote URL.
func scopeText(s Scope, esc func(string) string, pv Provenance) string {
	switch s.Kind {
	case "all":
		return "every repository"
	case "group":
		return "group " + esc(s.Key)
	}
	for _, h := range pv.Heads {
		if h.Key == s.Key {
			return "repository " + esc(h.Repo)
		}
	}
	return "repository " + esc(s.Key)
}

func textRepo(p *printer, m *Model, r *Repo) {
	head := "Repository " + clean(r.Name)
	if r.Group != "" {
		head += " · group " + clean(r.Group)
	}
	if r.Head != "" {
		head += " · " + clean(r.Branch) + " @ " + short(r.Head)
	}
	head += " · " + clean(r.Status)
	p.wrapped("", "  ", head)
	where := "path " + clean(r.Path)
	if r.Remote != "" {
		where += " · remote " + clean(r.Remote)
	} else {
		where += " · no remote"
	}
	if r.Bare {
		where += " · bare"
	}
	p.wrapped("  ", "    ", where)
	if r.Status != "ok" {
		p.wrapped("  error: ", "    ", clean(r.Error))
		textCaveats(p, "  ", "What could make this wrong", r.Card.Caveats, true)
		return
	}
	textCard(p, m, r)
	if r.Week != nil {
		p.blank()
		textWeek(p, "  ", m, r.Week)
	}
	if r.StillHere != nil {
		p.blank()
		p.linef("  Still here at %s: surviving non-blank lines (%s blamed)", short(r.Head),
			plural(r.StillHere.FilesBlamed, "file", "files"))
		textStillHere(p, "    ", r.StillHere)
	}
	if len(r.Errors.Blame) > 0 {
		p.blank()
		p.line("  Blame errors")
		for _, e := range r.Errors.Blame {
			p.wrapped("    - "+clean(e.Path)+": ", "      ", clean(e.Message))
		}
	}
	textCaveats(p, "  ", "What could make this wrong", r.Card.Caveats, true)
}

// landingsText is "landings: 9 in the window, 16 in the history", or just
// the window for a week report, whose history is the window.
func landingsText(m *Model, window, history int) string {
	s := "landings: " + grouped(window) + " in the window"
	if m.Kind == KindScan {
		s += ", " + grouped(history) + " in the history"
	}
	return s
}

// cardFacts is a card's first line: age, size, people and landings.
func cardFacts(m *Model, r *Repo) string {
	c := &r.Card
	var facts []string
	if c.Age != nil {
		facts = append(facts, fmt.Sprintf("age %s (first commit %s)", plural(c.Age.Days, "day", "days"), date(c.Age.FirstCommit)))
	}
	facts = append(facts, fmt.Sprintf("%s at HEAD, %s", plural(c.Size.Files, "file", "files"),
		plural(c.Size.ProdPaths, "prod path", "prod paths")))
	if c.Size.ProdLines != nil {
		lines := grouped(*c.Size.ProdLines) + " surviving prod lines"
		if c.Size.ProdFiles != nil {
			lines += " in " + plural(*c.Size.ProdFiles, "prod file", "prod files")
		}
		facts = append(facts, lines)
	}
	if c.People != nil || c.Bots != nil {
		var who []string
		if c.People != nil {
			who = append(who, plural(*c.People, "person", "people"))
		}
		if c.Bots != nil {
			who = append(who, plural(*c.Bots, "bot", "bots"))
		}
		facts = append(facts, strings.Join(who, ", "))
	}
	facts = append(facts, landingsText(m, c.Landings.Window, c.Landings.History))
	return strings.Join(facts, " · ")
}

// skippedText is "skipped paths: 4 denied, ... · blame ignore-revs: root
// (feedfacefeed, 1 commit)"; the ignore-revs part only when blame ran.
func skippedText(r *Repo, code func(string) string) string {
	s := r.Skipped
	out := fmt.Sprintf("skipped paths: %d denied, %d too big, %d generated header, %d unclassified",
		s.DeniedPath, s.TooBig, s.GeneratedHeader, s.Unclassified)
	if !r.LineFacts {
		return out
	}
	ignore := "none"
	if r.IgnoreRevs.Source != "" {
		ignore = fmt.Sprintf("%s (%s, %s)", code(r.IgnoreRevs.Source), code(short12(r.IgnoreRevs.SHA256)),
			plural(len(r.IgnoreRevs.Revs), "commit", "commits"))
	}
	return out + " · blame ignore-revs: " + ignore
}

func textCard(p *printer, m *Model, r *Repo) {
	c := &r.Card
	p.wrapped("  ", "  ", cardFacts(m, r))
	if len(c.MadeOf) > 0 {
		var who []string
		for _, a := range c.MadeOf {
			who = append(who, fmt.Sprintf("%s %s (%s)", clean(a.Name), grouped(a.ProdLines), percent(a.Share)))
		}
		p.wrapped("  made of: ", "    ", strings.Join(who, ", "))
	}
	if line := shareLine(c.KnowledgeLoss, c.AIAssisted, c.BotShare); line != "" {
		p.wrapped("  ", "  ", line)
	}
	if c.Concentration != nil {
		p.wrapped("  ", "  ", concentrationText(c.Concentration))
	}
	p.wrapped("  ", "  ", skippedText(r, clean))
}
