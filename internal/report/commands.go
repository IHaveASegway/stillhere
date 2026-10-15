package report

import (
	"slices"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Commands is how an answer's sentences name the commands they send their
// reader to (#69). A person at a terminal runs stillhere's commands; an
// agent calls the MCP server's tools and can run no command, so a hint
// that names a command its reader does not have is a dead end. Every such
// hint, in a use case's error or in a caveat a report builds, asks its
// reader's Commands: the command line's (CLI) unless the inbound adapter
// passes its own, as the MCP server passes its tools (app.Deps.Commands).
// One vocabulary per reader, decided where the reader is known, rather
// than a rewrite of finished sentences: the producer says what it points
// to, and the reader's adapter says how that is called.
type Commands interface {
	// Metrics names where every metric's definition is: the registry.
	Metrics() string
	// Identities names where the suggested .mailmap lines are, with the
	// evidence for each.
	Identities() string
	// Report names where a run's numbers per repository and group are.
	Report() string
	// Ranks says how to read the rank tables of a ranked metric.
	Ranks(metric string) string
	// Explain says how to explain metric about a subject of one of kinds
	// (domain.Subject kinds), or "" when the reader cannot ask about any of
	// them.
	Explain(metric string, kinds []string) string
	// WhoKnows names where who knows the code under a path is asked:
	// who_knows@1, which a scan never stores, so explain has none of it.
	WhoKnows() string
	// OwnPage names where a person's own page is, which alone shows their
	// own AI-assisted share (ADR 0007).
	OwnPage() string
}

// CLI is the command line's Commands: every answer's, unless its reader
// is another.
type CLI struct{}

// Metrics is the metrics command.
func (CLI) Metrics() string { return "'stillhere metrics'" }

// Identities is the identities command.
func (CLI) Identities() string { return "'stillhere identities'" }

// Report is the report command.
func (CLI) Report() string { return "'stillhere report'" }

// Ranks is the report command with --rank.
func (CLI) Ranks(metric string) string { return "'stillhere report --rank " + metric + "'" }

// Explain is the explain command, which names a person only.
func (CLI) Explain(metric string, kinds []string) string {
	if slices.Contains(kinds, domain.SubjectPerson) {
		return "'stillhere explain " + metric + " --person EMAIL'"
	}
	return ""
}

// WhoKnows is the who-knows command.
func (CLI) WhoKnows() string { return "'stillhere who-knows PATH'" }

// OwnPage is the me command.
func (CLI) OwnPage() string { return "'stillhere me --email EMAIL'" }

// CommandsOr returns c, or the command line's when c is nil: what an
// answer's hints name when the inbound adapter said nothing.
func CommandsOr(c Commands) Commands {
	if c == nil {
		return CLI{}
	}
	return c
}

// identitiesHint says where the .mailmap lines an unmerged-identities
// caveat calls for come from: the identities command, or tool, suggests
// them, with the evidence for each, and applies none.
func identitiesHint(c Commands) string {
	return CommandsOr(c).Identities() + " suggests the lines, with the evidence for each."
}
