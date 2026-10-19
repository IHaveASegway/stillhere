package report

import (
	"strconv"
	"strings"
	"time"
)

// Egress is the egress record as stillhere egress prints it (#85, ADR 0015):
// each run's decision about which AI-assisted shares the MCP server may let
// leave the machine, recorded before the first of them left. It lists what
// may leave, scope by scope, with the numbers as they would leave: a share
// is recorded with its run's whole decision once the first share of the run
// is about to leave, so a share listed here need not have been sent. It
// holds no person, only how many people each share is made of.
type Egress struct {
	Record    string           `json:"record"`    // the record's file
	Decisions []EgressDecision `json:"decisions"` // oldest first
}

// EgressDecision is one run's recorded decision.
type EgressDecision struct {
	Run        int64     `json:"run"`         // the run's id in its facts.db
	RunStarted time.Time `json:"run_started"` // with the id, which facts.db's run it is
	AsOf       time.Time `json:"as_of"`       // the run's --now: the end of the shares' window
	Window     Interval  `json:"window"`
	Metric     string    `json:"metric"`
	Inputs     string    `json:"inputs"`  // hex SHA-256 of the numbers the decision was made over
	Version    string    `json:"version"` // the stillhere that decided it
	Decided    time.Time `json:"decided"`
	// CheckedAgainst is every earlier decision it was decided against:
	// those recorded before it for a window that overlaps its own.
	CheckedAgainst []EgressRun   `json:"checked_against"`
	Shares         []EgressShare `json:"shares"` // what may leave, by kind and key; empty: every share of the run is withheld
}

// EgressRun names a run of the record, as a decision does.
type EgressRun struct {
	Run        int64     `json:"run"`
	RunStarted time.Time `json:"run_started"`
}

// EgressShare is one share a decision lets leave, as it would leave.
type EgressShare struct {
	Scope  Scope    `json:"scope"`
	Name   string   `json:"name"`  // the scope as the MCP answers name it: a repository's name, a group's, "" for every repository
	Repos  []string `json:"repos"` // Repo.Key of the repositories its landings come from
	Num    float64  `json:"num"`
	Den    float64  `json:"den"`
	Value  float64  `json:"value"`
	People int      `json:"people"` // the fewest people its landings are made of, as the floor counts them
}

// egressScope names a recorded share's scope on a page: by the name the
// answers use, and a repository's key beside it when it says more.
func egressScope(s EgressShare) string {
	switch s.Scope.Kind {
	case "group":
		return "group " + s.Scope.Key
	case "repo":
		if s.Name == "" || s.Name == s.Scope.Key {
			return "repository " + s.Scope.Key
		}
		return "repository " + s.Name + " (" + s.Scope.Key + ")"
	}
	return "every repository"
}

// instant formats an instant of the record in UTC to the second, and to the
// nanosecond when it has a fraction, with no zone: two decisions are checked
// against each other only when their windows overlap, and a page that
// rounded them to the minute would hide which did. A midnight is its date.
func instant(t time.Time) string {
	t = t.UTC()
	if t.Equal(t.Truncate(24 * time.Hour)) {
		return t.Format(time.DateOnly)
	}
	return t.Format("2006-01-02 15:04:05.999999999")
}

// checked says which earlier decisions d was decided against.
func (d *EgressDecision) checked() string {
	if len(d.CheckedAgainst) == 0 {
		return "no earlier decision of an overlapping window"
	}
	var runs []string
	for _, r := range d.CheckedAgainst {
		runs = append(runs, "run "+strconv.FormatInt(r.Run, 10)+" (started "+instant(r.RunStarted)+" UTC)")
	}
	return "checked against " + strings.Join(runs, ", ")
}

func (e *Egress) writeText(p *printer) {
	p.linef("Egress record %s: %s, oldest first", clean(e.Record), plural(len(e.Decisions), "decision", "decisions"))
	if len(e.Decisions) == 0 {
		p.line("No AI-assisted share may leave from this record yet.")
		return
	}
	p.line("Each run's shares that may leave the machine, recorded with its whole decision once its first share was about to.")
	cols := []column{{head: "Scope"}, {head: "Share", right: true}, {head: "People", right: true}, {head: "Repositories", right: true}}
	for _, d := range e.Decisions {
		p.blank()
		p.linef("Run %d (started %s UTC), as of %s UTC: %s over %s to %s UTC", d.Run, instant(d.RunStarted), instant(d.AsOf),
			d.Metric, instant(d.Window.Since), instant(d.Window.Until))
		p.linef("  decided %s UTC by stillhere %s; %s", instant(d.Decided), clean(d.Version), clean(d.checked()))
		if len(d.Shares) == 0 {
			p.line("  No share may leave: every AI-assisted share of the run is withheld.")
			continue
		}
		var rows [][]string
		for _, s := range d.Shares {
			rows = append(rows, []string{clean(egressScope(s)), percent(s.Value) + " (" + number(s.Num) + " of " + number(s.Den) + ")",
				strconv.Itoa(s.People), strconv.Itoa(len(s.Repos))})
		}
		p.textTable("  ", cols, rows)
	}
}

func (e *Egress) writeMarkdown(p *printer) {
	p.line("# Egress record: the AI-assisted shares that may leave")
	p.blank()
	p.line(mdCode(e.Record) + ": " + plural(len(e.Decisions), "decision", "decisions") + ", oldest first. Each run's shares " +
		"that may leave the machine, recorded with its whole decision once its first share was about to.")
	if len(e.Decisions) == 0 {
		p.blank()
		p.line("No AI-assisted share may leave from this record yet.")
		return
	}
	cols := []column{{head: "Scope"}, {head: "Share", right: true}, {head: "People", right: true}, {head: "Repositories", right: true}}
	for _, d := range e.Decisions {
		p.blank()
		p.linef("## Run %d, as of %s UTC", d.Run, instant(d.AsOf))
		p.blank()
		p.linef("Started %s UTC; %s over %s to %s UTC; decided %s UTC by stillhere %s; %s.", instant(d.RunStarted), mdID(d.Metric),
			instant(d.Window.Since), instant(d.Window.Until), instant(d.Decided), mdText(d.Version), mdText(d.checked()))
		p.blank()
		if len(d.Shares) == 0 {
			p.line("No share may leave: every AI-assisted share of the run is withheld.")
			continue
		}
		var rows [][]string
		for _, s := range d.Shares {
			rows = append(rows, []string{mdText(egressScope(s)), percent(s.Value) + " (" + number(s.Num) + " of " + number(s.Den) + ")",
				strconv.Itoa(s.People), strconv.Itoa(len(s.Repos))})
		}
		p.mdTable(cols, rows)
	}
}
