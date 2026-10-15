package report

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Text and Markdown for the read commands' answers: explain, who-knows,
// me, validate and metrics. Each starts with its provenance and ends with
// its caveats.

// provenanceText is "run 3 · as of 2026-10-07 · policy 1a2b3c4d5e6f", and
// when the answer shows a number computed from GitHub data, when that data
// was synced: "· GitHub data as of 2026-10-06".
func provenanceText(pv Provenance) string {
	s := "as of " + day(pv.AsOf)
	if pv.RunID != 0 {
		s = fmt.Sprintf("run %d · %s", pv.RunID, s)
	}
	if pv.Policy != "" {
		s += " · policy " + short12(pv.Policy)
	}
	if forgeDated(pv) {
		s += forgeAsOf(pv.Forge)
	}
	return s
}

// ratioUnit reports whether a metric's unit names a ratio, which prints as
// a percentage.
func ratioUnit(unit string) bool {
	u := strings.ToLower(unit)
	for _, w := range []string{"share", "ratio", "fraction", "percent", "%"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

// valueText prints a value: a ratio as "86.7% (26 of 30)", anything else as
// its number.
func valueText(v Value, ratio bool) string {
	if ratio || (v.Den != 1 && v.Den != 0) {
		return percent(v.Value) + " (" + number(v.Num) + " of " + number(v.Den) + ")"
	}
	return number(v.Value)
}

// detailText is "majority_files 3 · repos 1", keys sorted.
func detailText(d map[string]float64, esc func(string) string) string {
	var keys []string
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, esc(k)+" "+number(d[k]))
	}
	return strings.Join(parts, " · ")
}

// windowText is the window a value covers, or "at HEAD" for a value that
// has none.
func windowText(w Interval) string {
	if w.Since.IsZero() && w.Until.IsZero() {
		return "at HEAD"
	}
	return interval(w)
}

// subject names what an answer is about: a person as the page names them
// (printer.person), anything else by its kind and key.
func (p *printer) subject(s Subject, esc func(string) string) string {
	if s.Kind == domain.SubjectPerson {
		name := p.person(s.Key, s.Name, s.NameWithheld, esc)
		if s.Bot {
			name += " (bot)"
		}
		return name
	}
	return s.Kind + " " + esc(s.Key)
}

// Explain.

func (e *Explain) writeText(p *printer) {
	p.wrapped("", "  ", "explain "+clean(e.Metric.ID)+" · "+p.subject(e.Subject, clean)+" · "+provenanceText(e.Provenance))
	if e.Metric.Question != "" {
		p.wrapped("  ", "  ", clean(e.Metric.Question))
	}
	p.blank()
	ratio := ratioUnit(e.Metric.Unit)
	if len(e.Values) == 0 {
		p.line("  no value in this run")
	} else {
		cols := []column{{head: "scope"}, {head: "window"}, {head: "value", right: true}, {head: "n", right: true}, {head: "detail"}}
		var rows [][]string
		for _, v := range e.Values {
			rows = append(rows, []string{scopeText(v.Scope, clean, e.Provenance), windowText(v.Window), valueText(v, ratio), grouped(v.N), detailText(v.Detail, clean)})
		}
		p.textTable("  ", cols, rows)
	}
	if len(e.Changes) > 0 {
		p.blank()
		p.linef("  The %s behind it, by repository and time", plural(len(e.Changes), "change", "changes"))
		cols := []column{{head: "landing"}, {head: "repo"}, {head: "landed"}, {head: "num", right: true}, {head: "den", right: true}, {head: "title"}}
		var rows [][]string
		for _, c := range e.Changes {
			den := number(c.Den)
			if c.Den == 0 {
				den = ""
			}
			rows = append(rows, []string{short(c.Landing), clean(c.Repo), day(c.Time), number(c.Num), den, clean(c.Title)})
		}
		p.textTable("    ", cols, rows)
	}
	textDefinition(p, e.Metric)
	textCaveats(p, "", "What could make this wrong", e.Caveats, false)
}

func textDefinition(p *printer, d MetricInfo) {
	var lines [][2]string
	for _, f := range [][2]string{{"unit", d.Unit}, {"eligibility", d.Eligibility}, {"blind spots", d.BlindSpots},
		{"how it can be gamed", d.Gaming}, {"validity", d.Validity}} {
		if f[1] != "" {
			lines = append(lines, f)
		}
	}
	if len(lines) == 0 {
		return
	}
	p.blank()
	for _, f := range lines {
		p.wrapped("  "+f[0]+": ", "    ", clean(f[1]))
	}
}

func (e *Explain) writeMarkdown(p *printer) {
	p.line("# explain " + mdID(e.Metric.ID) + " · " + p.subject(e.Subject, mdText))
	p.blank()
	p.line("- " + provenanceText(e.Provenance))
	if e.Metric.Question != "" {
		p.line("- " + mdText(e.Metric.Question))
	}
	p.blank()
	ratio := ratioUnit(e.Metric.Unit)
	if len(e.Values) == 0 {
		p.line("No value in this run.")
	} else {
		cols := []column{{head: "Scope"}, {head: "Window"}, {head: "Value", right: true}, {head: "n", right: true}, {head: "Detail"}}
		var rows [][]string
		for _, v := range e.Values {
			rows = append(rows, []string{scopeText(v.Scope, mdText, e.Provenance), windowText(v.Window), valueText(v, ratio), grouped(v.N), detailText(v.Detail, mdID)})
		}
		p.mdTable(cols, rows)
	}
	if len(e.Changes) > 0 {
		p.blank()
		p.line("## The " + plural(len(e.Changes), "change", "changes") + " behind it")
		p.blank()
		cols := []column{{head: "Landing"}, {head: "Repo"}, {head: "Landed"}, {head: "Num", right: true}, {head: "Den", right: true}, {head: "Title"}}
		var rows [][]string
		for _, c := range e.Changes {
			den := number(c.Den)
			if c.Den == 0 {
				den = ""
			}
			rows = append(rows, []string{mdCode(short(c.Landing)), mdText(c.Repo), day(c.Time), number(c.Num), den, mdText(c.Title)})
		}
		p.mdTable(cols, rows)
	}
	mdDefinition(p, "##", e.Metric)
	mdCaveats(p, "##", e.Caveats, false)
}

func mdDefinition(p *printer, level string, d MetricInfo) {
	var lines []string
	for _, f := range [][2]string{{"Unit", d.Unit}, {"Eligibility", d.Eligibility}, {"Blind spots", d.BlindSpots},
		{"How it can be gamed", d.Gaming}, {"Oracle", d.Oracle}, {"Validity", d.Validity}} {
		if f[1] != "" {
			lines = append(lines, "- "+f[0]+": "+mdText(f[1]))
		}
	}
	if len(lines) == 0 {
		return
	}
	p.blank()
	p.line(level + " Definition")
	p.blank()
	for _, l := range lines {
		p.line(l)
	}
}

// WhoKnows.

// knowers lists people who know a path, each as the page names them
// (printer.person), followed by what what says about them.
func (p *printer) knowers(ks []Knower, esc func(string) string, what func(Knower) string) string {
	if len(ks) == 0 {
		return "none"
	}
	var parts []string
	for _, k := range ks {
		parts = append(parts, p.person(k.Email, k.Name, k.NameWithheld, esc)+" "+what(k))
	}
	return strings.Join(parts, " · ")
}

// pathTotals is "37 surviving prod lines in 12 prod files, 10 with a
// majority owner".
func pathTotals(r WhoKnowsRepo) string {
	return fmt.Sprintf("%s surviving prod lines in %s, %d with a majority owner",
		grouped(r.Lines), plural(r.ProdFiles, "prod file", "prod files"), r.FilesWithMajority)
}

func ownerCount(k Knower) string { return "(" + plural(k.MajorityFiles, "file", "files") + ")" }
func lineCount(k Knower) string  { return grouped(k.Lines) }
func landingCount(k Knower) string {
	return "(" + plural(k.RecentLandings, "landing", "landings") + ")"
}
func reviewCount(k Knower) string {
	return "(" + plural(k.RecentReviews, "pull request", "pull requests") + ")"
}

// reviewsShown reports whether a who-knows answer lists recent reviewers
// for the repository: it has some, or the run read GitHub data for it
// (the answer's provenance.forge lists it), so "none" is a real none. A
// repository without GitHub data lists no reviewers line at all, so a
// git-only answer says nothing about GitHub.
func (w *WhoKnows) reviewsShown(r WhoKnowsRepo) bool {
	return len(r.RecentReviewers) > 0 ||
		w.Provenance.Forge != nil && slices.ContainsFunc(w.Provenance.Forge.Repos, func(s ForgeSync) bool { return s.Key == r.Key })
}

// whoKnowsIntro says what the answer lists, reviewers included where it
// shows them.
func (w *WhoKnows) whoKnowsIntro() string {
	lists := "majority owners, the top authors of surviving prod lines, and recent landers (90 days)"
	if slices.ContainsFunc(w.Repos, w.reviewsShown) {
		lists = "majority owners, the top authors of surviving prod lines, and recent landers and reviewers (90 days)"
	}
	return "a routing answer, not a rank: " + lists
}

// noAnswer reports whether an answer's caveats say a metric it reads gave
// no values although the run has their facts: the answer is then missing,
// and must not read as an empty one.
func noAnswer(cs []Caveat) bool {
	return slices.ContainsFunc(cs, func(c Caveat) bool { return c.Code == codeNoValues })
}

func (w *WhoKnows) writeText(p *printer) {
	path := w.Path
	if path == "" {
		path = "the whole repository"
	}
	p.wrapped("", "  ", "who knows "+clean(path)+" · "+provenanceText(w.Provenance))
	p.wrapped("  ", "  ", w.whoKnowsIntro())
	switch {
	case len(w.Repos) == 0 && noAnswer(w.Caveats):
		p.blank()
		p.line("  no answer: " + MetricWhoKnows + " produced no values for this path (see what could make this wrong)")
	case len(w.Repos) == 0:
		p.blank()
		p.line("  nobody: no surviving lines or recent landings under this path")
	}
	for _, r := range w.Repos {
		p.blank()
		p.wrapped("", "  ", "Repository "+clean(r.Repo)+" · "+pathTotals(r))
		p.wrapped("  majority owners  ", "                   ", p.knowers(r.MajorityOwners, clean, ownerCount))
		p.wrapped("  top authors      ", "                   ", p.knowers(r.TopAuthors, clean, lineCount))
		p.wrapped("  recent landers   ", "                   ", p.knowers(r.RecentLanders, clean, landingCount))
		if w.reviewsShown(r) {
			p.wrapped("  recent reviewers ", "                   ", p.knowers(r.RecentReviewers, clean, reviewCount))
		}
		if len(r.Bots) > 0 {
			p.wrapped("  bots             ", "                   ", p.knowers(r.Bots, clean, lineCount))
		}
	}
	textCaveats(p, "", "What could make this wrong", w.Caveats, false)
}

func (w *WhoKnows) writeMarkdown(p *printer) {
	path := mdCode(w.Path)
	if w.Path == "" {
		path = "the whole repository"
	}
	p.line("# Who knows " + path)
	p.blank()
	p.line("- " + provenanceText(w.Provenance))
	intro := w.whoKnowsIntro()
	p.line("- " + strings.ToUpper(intro[:1]) + intro[1:] + ".")
	switch {
	case len(w.Repos) == 0 && noAnswer(w.Caveats):
		p.blank()
		p.line("No answer: " + mdID(MetricWhoKnows) + " produced no values for this path (see what could make this wrong).")
	case len(w.Repos) == 0:
		p.blank()
		p.line("Nobody: no surviving lines or recent landings under this path.")
	}
	for _, r := range w.Repos {
		p.blank()
		p.line("## Repository " + mdText(r.Repo))
		p.blank()
		p.line("- " + pathTotals(r))
		p.line("- Majority owners: " + p.knowers(r.MajorityOwners, mdText, ownerCount))
		p.line("- Top authors: " + p.knowers(r.TopAuthors, mdText, lineCount))
		p.line("- Recent landers: " + p.knowers(r.RecentLanders, mdText, landingCount))
		if w.reviewsShown(r) {
			p.line("- Recent reviewers: " + p.knowers(r.RecentReviewers, mdText, reviewCount))
		}
		if len(r.Bots) > 0 {
			p.line("- Bots: " + p.knowers(r.Bots, mdText, lineCount))
		}
	}
	mdCaveats(p, "##", w.Caveats, false)
}

// Me.

// byMetric groups values by metric, in order.
func byMetric(vs []Value) [][]Value {
	var out [][]Value
	for _, v := range vs {
		if n := len(out); n > 0 && out[n-1][0].Metric == v.Metric {
			out[n-1] = append(out[n-1], v)
			continue
		}
		out = append(out, []Value{v})
	}
	return out
}

func meRankText(r MeRank) (rank, estimate string) {
	if !r.Eligible {
		return "—", fmt.Sprintf("listed, not ranked: %s, below %d", plural(r.N, "change", "changes"), r.MinN)
	}
	return rankText(r.RankLo, r.RankHi) + " of " + grouped(r.Of),
		estimateText(r.Estimate, r.Percent) + " (" + intervalText(r.Lo, r.Hi, r.Percent) + ")"
}

// meIntro says what a person's own page holds, and that their AI-assisted
// share is on no other page (ADR 0007).
const meIntro = "every value and rank interval this run holds about you, including your own AI-assisted share, " +
	"which no report, rank table or explanation shows (ADR 0007)"

// ownPageLabel marks a metric whose values about a person only their own
// page shows.
const ownPageLabel = "shown only on your own page (ADR 0007)"

// metricHeading is a metric's heading on the me page, labelled when only
// that page shows it.
func metricHeading(id string, esc func(string) string) string {
	if OwnPageOnly(domain.MetricID{Name: metricName(id)}) {
		return esc(id) + " · " + ownPageLabel
	}
	return esc(id)
}

func (m *Me) writeText(p *printer) {
	p.wrapped("", "  ", "me · "+p.subject(m.Person, clean)+" · "+provenanceText(m.Provenance))
	p.wrapped("  ", "  ", meIntro)
	if len(m.Values) == 0 {
		p.blank()
		p.line("  no values in this run")
	}
	for _, group := range byMetric(m.Values) {
		p.blank()
		p.line(metricHeading(group[0].Metric, clean))
		for _, v := range group {
			line := scopeText(v.Scope, clean, m.Provenance) + " · " + windowText(v.Window) + ": " + valueText(v, false)
			if v.N > 0 {
				line += ", n " + grouped(v.N)
			}
			if d := detailText(v.Detail, clean); d != "" {
				line += " · " + d
			}
			p.wrapped("  ", "    ", line)
		}
	}
	if len(m.Ranks) > 0 {
		p.blank()
		p.line("Ranks")
		cols := []column{{head: "metric"}, {head: "scope"}, {head: "rank"}, {head: "estimate (95% interval)"}, {head: "n", right: true}}
		var rows [][]string
		for _, r := range m.Ranks {
			rank, est := meRankText(r)
			rows = append(rows, []string{r.Metric, scopeText(r.Scope, clean, m.Provenance), rank, est, grouped(r.N)})
		}
		p.textTable("  ", cols, rows)
	}
	textCaveats(p, "", "What could make this wrong", m.Caveats, false)
}

func (m *Me) writeMarkdown(p *printer) {
	p.line("# " + p.subject(m.Person, mdText))
	p.blank()
	p.line("- " + provenanceText(m.Provenance))
	p.line("- " + strings.ToUpper(meIntro[:1]) + meIntro[1:] + ".")
	if len(m.Values) == 0 {
		p.blank()
		p.line("No values in this run.")
	}
	for _, group := range byMetric(m.Values) {
		p.blank()
		p.line("## " + metricHeading(group[0].Metric, mdID))
		p.blank()
		cols := []column{{head: "Scope"}, {head: "Window"}, {head: "Value", right: true}, {head: "n", right: true}, {head: "Detail"}}
		var rows [][]string
		for _, v := range group {
			rows = append(rows, []string{scopeText(v.Scope, mdText, m.Provenance), windowText(v.Window), valueText(v, false), grouped(v.N), detailText(v.Detail, mdID)})
		}
		p.mdTable(cols, rows)
	}
	if len(m.Ranks) > 0 {
		p.blank()
		p.line("## Ranks")
		p.blank()
		cols := []column{{head: "Metric"}, {head: "Scope"}, {head: "Rank"}, {head: "Estimate (95% interval)"}, {head: "n", right: true}}
		var rows [][]string
		for _, r := range m.Ranks {
			rank, est := meRankText(r)
			rows = append(rows, []string{mdID(r.Metric), scopeText(r.Scope, mdText, m.Provenance), rank, est, grouped(r.N)})
		}
		p.mdTable(cols, rows)
	}
	mdCaveats(p, "##", m.Caveats, false)
}

// Validation.

func aucRow(a MetricAUC) []string {
	auc := "—"
	if a.AUC != nil {
		auc = fmt.Sprintf("%.2f", *a.AUC)
	}
	status := "meets the bar (AUC ≥ 0.7 on ≥ 50 labels)"
	if a.Beta {
		status = "beta"
	}
	return []string{a.Metric, auc, grouped(a.N), grouped(a.Meaningful), grouped(a.Routine), status}
}

func (v *Validation) summary() string {
	return fmt.Sprintf("%s from %s: %d meaningful, %d routine, %d noise · %d not matched to a landing of the run",
		plural(v.Labels, "labelled row", "labelled rows"), plural(v.Labellers, "labeller", "labellers"),
		v.Meaningful, v.Routine, v.Noise, v.Unmatched)
}

func (v *Validation) writeText(p *printer) {
	p.line("validate · " + provenanceText(v.Provenance))
	p.wrapped("  ", "  ", v.summary())
	p.line("  AUC: the chance a meaningful change scores above a routine one; 0.5 is chance")
	p.blank()
	if len(v.Metrics) == 0 {
		p.line("  no ranked metric scored a labelled change")
	} else {
		cols := []column{{head: "metric"}, {head: "AUC", right: true}, {head: "n", right: true},
			{head: "meaningful", right: true}, {head: "routine", right: true}, {head: "status"}}
		var rows [][]string
		for _, a := range v.Metrics {
			rows = append(rows, aucRow(a))
		}
		p.textTable("  ", cols, rows)
	}
	textCaveats(p, "", "What could make this wrong", v.Caveats, false)
}

func (v *Validation) writeMarkdown(p *printer) {
	p.line("# Validity check")
	p.blank()
	p.line("- " + provenanceText(v.Provenance))
	p.line("- " + v.summary())
	p.line("- AUC: the chance a meaningful change scores above a routine one; 0.5 is chance.")
	p.blank()
	if len(v.Metrics) == 0 {
		p.line("No ranked metric scored a labelled change.")
	} else {
		cols := []column{{head: "Metric"}, {head: "AUC", right: true}, {head: "n", right: true},
			{head: "Meaningful", right: true}, {head: "Routine", right: true}, {head: "Status"}}
		var rows [][]string
		for _, a := range v.Metrics {
			row := aucRow(a)
			row[0] = mdID(row[0])
			rows = append(rows, row)
		}
		p.mdTable(cols, rows)
	}
	mdCaveats(p, "##", v.Caveats, false)
}

// Catalog.

func (c *Catalog) writeText(p *printer) {
	if len(c.Metrics) == 0 {
		p.line("no metrics registered")
		return
	}
	for i, d := range c.Metrics {
		if i > 0 {
			p.blank()
		}
		kind := "context, never ranked"
		if d.Ranked {
			kind = "ranked"
		}
		p.linef("%s (%s)", d.ID, kind)
		if d.Question != "" {
			p.wrapped("  ", "  ", clean(d.Question))
		}
		for _, f := range [][2]string{{"unit", d.Unit}, {"eligibility", d.Eligibility}, {"blind spots", d.BlindSpots},
			{"how it can be gamed", d.Gaming}, {"oracle", d.Oracle}, {"validity", d.Validity}} {
			if f[1] != "" {
				p.wrapped("  "+f[0]+": ", "    ", clean(f[1]))
			}
		}
	}
}

func (c *Catalog) writeMarkdown(p *printer) {
	p.line("# Metrics")
	if len(c.Metrics) == 0 {
		p.blank()
		p.line("No metrics registered.")
		return
	}
	p.blank()
	cols := []column{{head: "Metric"}, {head: "Ranked"}, {head: "Question"}, {head: "Unit"}}
	var rows [][]string
	for _, d := range c.Metrics {
		ranked := "no"
		if d.Ranked {
			ranked = "yes"
		}
		rows = append(rows, []string{mdID(d.ID), ranked, mdText(d.Question), mdText(d.Unit)})
	}
	p.mdTable(cols, rows)
	for _, d := range c.Metrics {
		p.blank()
		p.line("## " + mdID(d.ID))
		if d.Question != "" {
			p.blank()
			p.line(mdText(d.Question))
		}
		var lines []string
		for _, f := range [][2]string{{"Unit", d.Unit}, {"Eligibility", d.Eligibility}, {"Blind spots", d.BlindSpots},
			{"How it can be gamed", d.Gaming}, {"Oracle", d.Oracle}, {"Validity", d.Validity}} {
			if f[1] != "" {
				lines = append(lines, "- "+f[0]+": "+mdText(f[1]))
			}
		}
		if len(lines) > 0 {
			p.blank()
			for _, l := range lines {
				p.line(l)
			}
		}
	}
}
