package report

import (
	"strconv"
	"time"
)

// Egress is the egress record as stillhere egress prints it (#85, ADR 0015):
// each run's decision about which AI-assisted shares the MCP server may let
// leave the machine, recorded before the first of them left. It shows what
// may have left, scope by scope, with the numbers as they left; it holds no
// person, only how many people each share is made of.
type Egress struct {
	Record    string           `json:"record"`    // the record's file
	Decisions []EgressDecision `json:"decisions"` // oldest first
}

// EgressDecision is one run's recorded decision.
type EgressDecision struct {
	Run        int64         `json:"run"`         // the run's id in its facts.db
	RunStarted time.Time     `json:"run_started"` // with the id, which facts.db's run it is
	AsOf       time.Time     `json:"as_of"`       // the run's --now: the end of the shares' window
	Window     Interval      `json:"window"`
	Metric     string        `json:"metric"`
	Inputs     string        `json:"inputs"`  // hex SHA-256 of the numbers the decision was made over
	Version    string        `json:"version"` // the stillhere that decided it
	Decided    time.Time     `json:"decided"`
	Shares     []EgressShare `json:"shares"` // what may leave, by kind and key; empty: every share of the run is withheld
}

// EgressShare is one share a decision lets leave, as it leaves.
type EgressShare struct {
	Scope  Scope    `json:"scope"`
	Repos  []string `json:"repos"` // Repo.Key of the repositories its landings come from
	Num    float64  `json:"num"`
	Den    float64  `json:"den"`
	Value  float64  `json:"value"`
	People int      `json:"people"` // the fewest people its landings are made of, as the floor counts them
}

// egressScope names a recorded share's scope on a page.
func egressScope(sc Scope) string {
	switch sc.Kind {
	case "group":
		return "group " + sc.Key
	case "repo":
		return "repository " + sc.Key
	}
	return "every repository"
}

func (e *Egress) writeText(p *printer) {
	p.linef("Egress record %s: %s, oldest first", clean(e.Record), plural(len(e.Decisions), "decision", "decisions"))
	if len(e.Decisions) == 0 {
		p.line("No AI-assisted share has been let out from this record.")
		return
	}
	cols := []column{{head: "Scope"}, {head: "Share", right: true}, {head: "People", right: true}, {head: "Repositories", right: true}}
	for _, d := range e.Decisions {
		p.blank()
		p.linef("Run %d (started %s), as of %s: %s over %s to %s", d.Run, dayUTC(d.RunStarted), dayUTC(d.AsOf), d.Metric,
			day(d.Window.Since), day(d.Window.Until))
		p.linef("  decided %s by stillhere %s", dayUTC(d.Decided), clean(d.Version))
		if len(d.Shares) == 0 {
			p.line("  No share may leave: every AI-assisted share of the run is withheld.")
			continue
		}
		var rows [][]string
		for _, s := range d.Shares {
			rows = append(rows, []string{clean(egressScope(s.Scope)), percent(s.Value) + " (" + number(s.Num) + " of " + number(s.Den) + ")",
				strconv.Itoa(s.People), strconv.Itoa(len(s.Repos))})
		}
		p.textTable("  ", cols, rows)
	}
}

func (e *Egress) writeMarkdown(p *printer) {
	p.line("# Egress record")
	p.blank()
	p.line(mdCode(e.Record) + ": " + plural(len(e.Decisions), "decision", "decisions") + ", oldest first.")
	if len(e.Decisions) == 0 {
		p.blank()
		p.line("No AI-assisted share has been let out from this record.")
		return
	}
	cols := []column{{head: "Scope"}, {head: "Share", right: true}, {head: "People", right: true}, {head: "Repositories", right: true}}
	for _, d := range e.Decisions {
		p.blank()
		p.linef("## Run %d, as of %s", d.Run, dayUTC(d.AsOf))
		p.blank()
		p.linef("Started %s; %s over %s to %s; decided %s by stillhere %s.", dayUTC(d.RunStarted), mdID(d.Metric),
			day(d.Window.Since), day(d.Window.Until), dayUTC(d.Decided), mdText(d.Version))
		p.blank()
		if len(d.Shares) == 0 {
			p.line("No share may leave: every AI-assisted share of the run is withheld.")
			continue
		}
		var rows [][]string
		for _, s := range d.Shares {
			rows = append(rows, []string{mdText(egressScope(s.Scope)), percent(s.Value) + " (" + number(s.Num) + " of " + number(s.Den) + ")",
				strconv.Itoa(s.People), strconv.Itoa(len(s.Repos))})
		}
		p.mdTable(cols, rows)
	}
}
