package report

import (
	"fmt"
	"strings"
)

// The Markdown renderer: GitHub-flavoured Markdown, the same sections as the
// text renderer in the same order, for pasting into an issue or a pull
// request. Untrusted text goes through mdText, and paths, SHAs and remotes
// through mdCode, so a name such as "<img src=x>" renders as text. A page
// that gets pasted is shared, so it names people without their emails, or a
// name that gives one away (printer.person), and leaves out each
// repository's local path (a warning about a path still names it).

// mdTable prints a GitHub table; numbers are right-aligned.
func (p *printer) mdTable(cols []column, rows [][]string) {
	head, sep := "|", "|"
	for _, c := range cols {
		head += " " + c.head + " |"
		if c.right {
			sep += " ---: |"
		} else {
			sep += " --- |"
		}
	}
	p.line(head)
	p.line(sep)
	for _, r := range rows {
		line := "|"
		for _, cell := range r {
			line += " " + cell + " |"
		}
		p.line(line)
	}
}

// mdID renders a stillhere identifier (a metric id, a warning code) as code,
// so an underscore is never read as emphasis.
func mdID(s string) string { return mdCode(s) }

func (m *Model) writeMarkdown(p *printer) {
	title := "# stillhere " + m.Kind
	if m.Run != nil {
		title += fmt.Sprintf(" · run %d (%s)", m.Run.ID, mdText(m.Run.Status))
	}
	p.line(title)
	p.blank()
	p.line("- As of " + dayUTC(m.Now) + " · " + policyText(m, mdText))
	p.line("- " + windowSummary(m, mdText))
	if m.Kind == KindScan {
		p.line("- " + historySummary(m))
	}
	if s := forgeSummary(m); s != "" {
		p.line("- " + s)
	}
	p.line("- " + repoSummary(m, mdText))
	if len(m.Metrics) > 0 {
		var ids []string
		for _, id := range m.Metrics {
			ids = append(ids, mdID(id))
		}
		p.line("- Metrics " + strings.Join(ids, ", "))
	}
	mdCaveats(p, "##", m.Caveats, false)
	for i := range m.Overviews {
		if !m.repeats(&m.Overviews[i]) {
			mdOverview(p, m, &m.Overviews[i])
		}
	}
	for i := range m.Ranks {
		mdRanks(p, "##", &m.Ranks[i])
	}
	for i := range m.Repos {
		mdRepo(p, m, &m.Repos[i])
	}
	if len(m.Warnings) > 0 {
		p.blank()
		p.line("## Warnings")
		p.blank()
		for _, w := range m.Warnings {
			p.line("- " + mdID(w.Code) + ": " + mdText(w.Message))
		}
	}
}

func mdCaveats(p *printer, level string, cs []Caveat, nested bool) {
	var shown []Caveat
	for _, c := range cs {
		if !nested || !c.Standing() {
			shown = append(shown, c)
		}
	}
	if len(shown) == 0 {
		return
	}
	p.blank()
	p.line(level + " What could make this wrong")
	p.blank()
	for _, c := range shown {
		msg := mdText(c.Message)
		if len(c.Repos) > 0 {
			msg += " (" + repoList(c.Repos, mdText) + ")"
		}
		p.line("- " + msg)
	}
}

func mdOverview(p *printer, m *Model, o *Overview) {
	t := o.Totals
	p.blank()
	p.line("## " + overviewTitle(o.Scope, mdText))
	p.blank()
	repos := plural(t.Repos, "repository", "repositories") + fmt.Sprintf(" (%d ok", t.ReposOK)
	if t.ReposFailed > 0 {
		repos += fmt.Sprintf(", %d failed", t.ReposFailed)
	}
	p.line("- " + repos + ") · " + peopleSummary(t))
	p.line("- " + sizeText(t.ProdLines, t.ProdFiles, t.ProdPaths) + " · " + landingsText(m, t.LandingsWindow, t.LandingsHistory))
	if line := shareLine(o.KnowledgeLoss, o.AIAssisted, o.BotShare); line != "" {
		p.line("- " + mdText(line))
	}
	if line := goingQuietText(p, o.KnowledgeLoss, mdText); line != "" {
		p.line("- Going quiet within 30 days: " + line)
	}
	if o.ReviewCoverage != nil {
		p.line("- " + mdText(coverageText(o.ReviewCoverage, t.Repos)))
	}
	if o.Concentration != nil {
		p.line("- " + mdText(concentrationText(o.Concentration)))
	}
	if len(o.Repos) > 1 {
		p.blank()
		p.line("### Repositories")
		p.blank()
		legend := repoLegend(m, o.Repos)
		p.line(mdText(strings.ToUpper(legend[:1]) + legend[1:] + "."))
		p.blank()
		cols, rows := repoTable(m, o.Repos, mdText)
		for i := range cols {
			cols[i].head = strings.ToUpper(cols[i].head[:1]) + cols[i].head[1:]
		}
		p.mdTable(cols, rows)
	}
	if o.People != nil {
		p.blank()
		p.line("### Still here")
		p.blank()
		p.line(fmt.Sprintf("Surviving prod non-blank lines across %s (%s blamed).",
			across(o.Scope), plural(o.People.FilesBlamed, "file", "files")))
		mdPeople(p, o.People)
	}
	if o.Week != nil || o.Totals.ReposOK > 0 {
		mdWeek(p, "###", m, o.Week)
	}
	mdCaveats(p, "###", o.Caveats, true)
}

func mdPeople(p *printer, t *StillHere) {
	p.blank()
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.line("Nobody: no surviving lines.")
		return
	}
	cols := []column{{head: "Person"}, {head: "Prod lines", right: true}, {head: "Majority files", right: true},
		{head: "Repos", right: true}, {head: "Landings 365d", right: true}}
	activity := activityShown(t.Humans)
	if activity {
		cols = append(cols, column{head: "Last active"})
	}
	row := func(r StillHereRow) []string {
		cells := []string{p.person(r.Email, r.Name, r.NameWithheld, mdText), grouped(r.ProdNonblankLines),
			grouped(r.MajorityFiles), grouped(r.Repos), grouped(r.Landings365d)}
		if activity {
			cells = append(cells, mdText(activityCell(r.LastActive, r.LastActivity, r.QuietInDays, r.GoingQuiet, false)))
		}
		return cells
	}
	mdWithBots(p, cols, t.Humans, t.Bots, row)
}

func mdStillHere(p *printer, t *StillHere) {
	p.blank()
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.line("Nobody: no surviving lines.")
		return
	}
	cols := []column{{head: "Person"}, {head: "Prod lines", right: true}, {head: "Majority files", right: true},
		{head: "Landings 365d", right: true}}
	activity := activityShown(t.Humans)
	if activity {
		cols = append(cols, column{head: "Last active"})
	}
	cols = append(cols, column{head: "Other roles"})
	row := func(r StillHereRow) []string {
		cells := []string{p.person(r.Email, r.Name, r.NameWithheld, mdText), grouped(r.ProdNonblankLines),
			grouped(r.MajorityFiles), grouped(r.Landings365d)}
		if activity {
			cells = append(cells, mdText(activityCell(r.LastActive, r.LastActivity, r.QuietInDays, r.GoingQuiet, false)))
		}
		return append(cells, mdText(otherRoles(r.LinesByRole)))
	}
	mdWithBots(p, cols, t.Humans, t.Bots, row)
}

func mdWeek(p *printer, level string, m *Model, t *WeekTable) {
	w := Interval{Since: m.Window.Since, Until: m.Window.Until}
	p.blank()
	p.line(level + " What landed, " + interval(w))
	p.blank()
	if t == nil {
		p.line(mdText(strings.ToUpper(weekUnknown[:1])+weekUnknown[1:]) + ".")
		return
	}
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.line("Nothing landed in the window.")
		return
	}
	if t.Total != nil {
		p.line(weekTotalText(t.Total) + ".")
		p.blank()
	}
	p.line("Sorted by " + mdID(m.Window.Sort) + ". A landing counts for each person who wrote one of its commits, so a " +
		"merged branch counts for its authors. Lines added count their commits' numstat insertions, blank lines included, " +
		"on paths that received a role; they are activity, not comparable to surviving lines.")
	p.blank()
	cols, row := weekColumns(p, t, mdText)
	for i := range cols {
		cols[i].head = strings.ToUpper(cols[i].head[:1]) + cols[i].head[1:]
	}
	mdWithBots(p, cols, t.Humans, t.Bots, row)
}

// mdWithBots prints the humans' table, then the bots' under a label of
// their own, the first pageRows of each, and counts the rest (SR-14).
func mdWithBots[R any](p *printer, cols []column, humans, bots []R, row func(R) []string) {
	humans, hiddenH := shown(p, humans, pageRows)
	bots, hiddenB := shown(p, bots, pageRows)
	var rows [][]string
	for _, r := range humans {
		rows = append(rows, row(r))
	}
	if len(rows) > 0 {
		p.mdTable(cols, rows)
	}
	if s := moreText(hiddenH, "person", "people"); s != "" {
		p.blank()
		p.line(strings.ToUpper(s[:1]) + s[1:] + ".")
	}
	if len(bots) == 0 {
		return
	}
	if len(rows) > 0 {
		p.blank()
	}
	p.line("Bots, listed apart:")
	p.blank()
	rows = rows[:0]
	for _, r := range bots {
		rows = append(rows, row(r))
	}
	p.mdTable(cols, rows)
	if s := moreText(hiddenB, "bot", "bots"); s != "" {
		p.blank()
		p.line(strings.ToUpper(s[:1]) + s[1:] + ".")
	}
}

func mdRanks(p *printer, level string, t *RankTable) {
	p.blank()
	p.line(level + " Ranks: " + mdID(t.Metric) + ", " + scopeText(t.Scope, mdText, t.Provenance) + ", " + interval(t.Window) +
		rankForgeAsOf(t))
	p.blank()
	if t.Question != "" {
		p.line(mdText(t.Question))
		p.blank()
	}
	if len(t.Ranked) == 0 && len(t.BelowMin) == 0 && len(t.Bots) == 0 {
		p.line("Nobody has a value.")
	} else {
		cols, rows, notes := rankRows(p, t, mdText, true)
		cols[1].head, cols[2].head = "Person", mdText(cols[2].head)
		if len(rows) > 0 {
			p.mdTable(cols, rows)
		}
		for _, n := range notes {
			p.blank()
			p.line(mdText(strings.ToUpper(n[:1]) + n[1:] + "."))
		}
	}
	p.blank()
	p.line(mdText(rankFooter(p, t)))
	mdCaveats(p, level+"#", t.Caveats, true)
}

func mdRepo(p *printer, m *Model, r *Repo) {
	p.blank()
	p.line("## Repository " + mdText(r.Name))
	p.blank()
	var where []string
	if r.Group != "" {
		where = append(where, "group "+mdText(r.Group))
	}
	if r.Head != "" {
		where = append(where, mdCode(r.Branch)+" @ "+mdCode(short(r.Head))+" "+branchNote(r, mdCode))
	}
	where = append(where, "status "+mdText(r.Status))
	if r.Remote != "" {
		where = append(where, "remote "+mdCode(r.Remote))
	}
	if r.Bare {
		where = append(where, "bare")
	}
	p.line("- " + strings.Join(where, " · "))
	if r.Status != "ok" {
		p.line("- Error: " + mdText(r.Error))
		mdCaveats(p, "###", r.Card.Caveats, true)
		return
	}
	mdCard(p, m, r)
	mdWeek(p, "###", m, r.Week)
	if r.StillHere != nil {
		p.blank()
		p.line("### Still here at " + mdCode(short(r.Head)))
		p.blank()
		p.line(fmt.Sprintf("Surviving non-blank lines blame attributes at HEAD (%s blamed).",
			plural(r.StillHere.FilesBlamed, "file", "files")))
		mdStillHere(p, r.StillHere)
	}
	if len(r.Errors.Blame) > 0 {
		p.blank()
		p.line("### Blame errors")
		p.blank()
		for _, e := range r.Errors.Blame {
			p.line("- " + mdCode(e.Path) + ": " + mdText(e.Message))
		}
	}
	mdCaveats(p, "###", r.Card.Caveats, true)
}

func mdCard(p *printer, m *Model, r *Repo) {
	c := &r.Card
	p.line("- " + cardFacts(m, r))
	if len(c.MadeOf) > 0 {
		var who []string
		for _, a := range c.MadeOf {
			who = append(who, fmt.Sprintf("%s %s (%s)", p.person(a.Email, a.Name, a.NameWithheld, mdText),
				grouped(a.ProdLines), mdText(madeOfDetail(a))))
		}
		p.line("- Made of " + strings.Join(who, ", "))
	}
	if line := shareLine(c.KnowledgeLoss, c.AIAssisted, c.BotShare); line != "" {
		p.line("- " + mdText(line))
	}
	if line := goingQuietText(p, c.KnowledgeLoss, mdText); line != "" {
		p.line("- Going quiet within 30 days: " + line)
	}
	if c.ReviewCoverage != nil {
		p.line("- " + mdText(coverageText(c.ReviewCoverage, 1)))
	}
	if c.Concentration != nil {
		p.line("- " + mdText(concentrationText(c.Concentration)))
	}
	p.line("- " + skippedText(m, r, mdCode))
	if len(c.Areas) > 0 {
		p.blank()
		p.line("### Areas")
		p.blank()
		p.line("Top-level directories, by the prod lines people hold there.")
		p.blank()
		cols, rows, more := areaTable(p, c.Areas, mdCode)
		for i := range cols {
			cols[i].head = strings.ToUpper(cols[i].head[:1]) + cols[i].head[1:]
		}
		p.mdTable(cols, rows)
		if more != "" {
			p.blank()
			p.line(strings.ToUpper(more[:1]) + more[1:] + ".")
		}
	}
}
