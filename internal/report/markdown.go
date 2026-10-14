package report

import (
	"fmt"
	"strings"
)

// The Markdown renderer: GitHub-flavoured Markdown, the same sections as the
// text renderer in the same order, for pasting into an issue or a pull
// request. Untrusted text goes through mdText, and paths, SHAs and remotes
// through mdCode, so a name such as "<img src=x>" renders as text. A page
// that gets pasted is shared, so it names people without their emails and
// leaves out each repository's local path (a warning about a path still
// names it).

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
	p.line("- As of " + day(m.Now) + " · " + policyText(m, mdText))
	p.line("- " + windowSummary(m, mdText))
	if m.Kind == KindScan {
		p.line("- " + historySummary(m))
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
		mdOverview(p, m, &m.Overviews[i])
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
		if !nested || !standing(c) {
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
	p.line("## Group " + mdText(o.Scope.Key))
	p.blank()
	repos := plural(t.Repos, "repository", "repositories") + fmt.Sprintf(" (%d ok", t.ReposOK)
	if t.ReposFailed > 0 {
		repos += fmt.Sprintf(", %d failed", t.ReposFailed)
	}
	p.line("- " + repos + ") · " + peopleSummary(t, o.People != nil))
	size := grouped(t.ProdFiles) + " prod files"
	if o.People != nil {
		size = grouped(t.ProdLines) + " surviving prod lines in " + size
	}
	p.line("- " + size + " · " + landingsText(m, t.LandingsWindow, t.LandingsHistory))
	if line := shareLine(o.KnowledgeLoss, o.AIAssisted, o.BotShare); line != "" {
		p.line("- " + mdText(line))
	}
	if o.Concentration != nil {
		p.line("- " + mdText(concentrationText(o.Concentration)))
	}
	if o.People != nil {
		p.blank()
		p.line("### Still here")
		p.blank()
		p.line(fmt.Sprintf("Surviving prod non-blank lines across the group (%s blamed).",
			plural(o.People.FilesBlamed, "file", "files")))
		mdPeople(p, o.People)
	}
	if o.Week != nil {
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
	row := func(r StillHereRow) []string {
		return []string{mdText(r.Name), grouped(r.ProdNonblankLines), grouped(r.MajorityFiles), grouped(r.Repos), grouped(r.Landings365d)}
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
		{head: "Other roles"}}
	row := func(r StillHereRow) []string {
		return []string{mdText(r.Name), grouped(r.ProdNonblankLines), grouped(r.MajorityFiles), mdText(otherRoles(r.LinesByRole))}
	}
	mdWithBots(p, cols, t.Humans, t.Bots, row)
}

func mdWeek(p *printer, level string, m *Model, t *WeekTable) {
	w := Interval{Since: m.Window.Since, Until: m.Window.Until}
	p.blank()
	p.line(level + " What landed, " + interval(w))
	p.blank()
	if len(t.Humans) == 0 && len(t.Bots) == 0 {
		p.line("Nothing landed in the window.")
		return
	}
	p.line("Sorted by " + mdID(m.Window.Sort) + ". Lines added count numstat insertions, blank lines included, " +
		"on paths that received a role; they are activity, not comparable to surviving lines.")
	p.blank()
	cols := []column{{head: "Person"}, {head: "Commits", right: true}, {head: "Lines added", right: true},
		{head: "Prod lines added", right: true}}
	row := func(r WeekRow) []string {
		return []string{mdText(r.Name), grouped(r.Commits), grouped(r.LinesAdded), grouped(r.ProdLinesAdded)}
	}
	mdWithBots(p, cols, t.Humans, t.Bots, row)
}

// mdWithBots prints the humans' table, then the bots' under a label of
// their own.
func mdWithBots[R any](p *printer, cols []column, humans, bots []R, row func(R) []string) {
	var rows [][]string
	for _, r := range humans {
		rows = append(rows, row(r))
	}
	if len(rows) > 0 {
		p.mdTable(cols, rows)
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
}

func mdRanks(p *printer, level string, t *RankTable) {
	p.blank()
	p.line(level + " Ranks: " + mdID(t.Metric) + ", " + scopeText(t.Scope, mdText, t.Provenance) + ", " + interval(t.Window))
	p.blank()
	if t.Question != "" {
		p.line(mdText(t.Question))
		p.blank()
	}
	if len(t.Ranked) == 0 && len(t.BelowMin) == 0 && len(t.Bots) == 0 {
		p.line("Nobody has a value.")
	} else {
		cols := []column{{head: "#"}, {head: "Person"}, {head: mdText(metricName(t.Metric)), right: true},
			{head: "95% interval", right: true}, {head: "n", right: true}}
		var rows [][]string
		for _, r := range t.Ranked {
			rows = append(rows, []string{rankText(r.RankLo, r.RankHi), mdText(r.Name), estimateText(r.Estimate, t.Percent),
				intervalText(r.Lo, r.Hi, t.Percent), grouped(r.N)})
		}
		for _, r := range t.BelowMin {
			rows = append(rows, []string{"—", mdText(r.Name), "", "", grouped(r.N)})
		}
		for _, r := range t.Bots {
			rows = append(rows, []string{"bot", mdText(r.Name), estimateText(r.Estimate, t.Percent), "", grouped(r.N)})
		}
		p.mdTable(cols, rows)
	}
	p.blank()
	p.line(mdText(rankFooter(t)))
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
		where = append(where, mdCode(r.Branch)+" @ "+mdCode(short(r.Head)))
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
	if r.Week != nil {
		mdWeek(p, "###", m, r.Week)
	}
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
			who = append(who, fmt.Sprintf("%s %s (%s)", mdText(a.Name), grouped(a.ProdLines), percent(a.Share)))
		}
		p.line("- Made of " + strings.Join(who, ", "))
	}
	if line := shareLine(c.KnowledgeLoss, c.AIAssisted, c.BotShare); line != "" {
		p.line("- " + mdText(line))
	}
	if c.Concentration != nil {
		p.line("- " + mdText(concentrationText(c.Concentration)))
	}
	p.line("- " + skippedText(r, mdCode))
}
