// Package app holds stillhere's use cases: week, scan, report, explain,
// who-knows, me, validate, metrics, identities and sync, and the MCP
// server's from M4. Each one orchestrates the core and the collectors, and
// reaches git, facts.db and GitHub only through the ports it is handed in
// Deps, so it never imports an adapter, os/exec, database/sql or cobra, and
// never opens a file itself: the files a run touches outside git are
// collect's, the directories discovery reads and blame's ignore-revs lists
// (docs/spec/codebase.md#the-dependency-rule).
//
// Requests arrive already parsed and validated by an inbound adapter (the
// command line, or the MCP server): a use case never sees a flag, and the
// adapter, not the use case, picks the output format. Answers are report
// types, so both adapters render and serve the same shapes.
//
// The write side is week (collect, compute, print) and scan (collect,
// compute, store as one run). The read side loads a stored run with the
// policy the run recorded and computes nothing that changes it (CQRS: Martin
// Fowler, "CQRS").
package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// finishTimeout bounds the FinishRun that records a run's outcome after its
// context was cancelled (a scan's failed, a sync's whatever it reached): the
// write must outlive the cancelled context, but only briefly. A store that
// waits for another process's write lock would otherwise keep a stopped run
// alive for minutes, until a service manager kills it; a run left running is
// invisible to readers and deleted once its lease lapses.
const finishTimeout = 10 * time.Second

// Deps are the ports and settings a use case runs with, built by the
// composition root in internal/cli.
type Deps struct {
	Git   domain.GitRunner   // nil for commands that only read facts.db
	Store domain.FactStore   // nil for week
	Forge domain.ForgeSource // sync only: the forge's API
	// ForgeStore is the forge replica in facts.db, the same store as Store:
	// sync writes it; scan reads it (LoadForge) and records what it read,
	// the watermarks (PutRunWatermarks) and the repositories and pull
	// requests (PutRunForge); the read use cases read those back
	// (RunWatermarks, RunForge) to say when a run's GitHub data was synced
	// and to compute over the run's own GitHub data (ADR 0011). With nil,
	// a scan reads no GitHub data, and neither a scan nor a reader says
	// anything about it, provenance or caveat: they cannot speak for a
	// replica they were not given, and so a stored run renders as its scan
	// did.
	ForgeStore domain.ForgeStore
	Policy     *policy.Policy // week and scan only; a read use case rebuilds the run's own (policy.FromSource on Run.PolicySource)
	Log        *slog.Logger   // progress and warnings, on stderr; nil discards them
	Version    string         // the tool version recorded on every run
	// Commands is how the answers and errors of the use cases name the
	// commands they send their reader to: the inbound adapter's own
	// vocabulary, as the MCP server names its tools (#69). nil: the
	// command line's.
	Commands report.Commands
}

// commands is the vocabulary the use cases' answers and errors name
// commands in: Deps.Commands, or the command line's.
func (d Deps) commands() report.Commands { return report.CommandsOr(d.Commands) }

// ready returns d with a logger, after checking that it holds the ports op
// needs: a missing port is the composition root's mistake, not the user's.
func (d Deps) ready(op string, git, pol, store bool) (Deps, error) {
	switch {
	case git && d.Git == nil:
		return d, fmt.Errorf("%s: no git runner", op)
	case pol && d.Policy == nil:
		return d, fmt.Errorf("%s: no policy", op)
	case store && d.Store == nil:
		return d, fmt.Errorf("%s: no facts store", op)
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return d, nil
}

// WeekRequest asks what landed on the default branch of each repository in
// the window ending at Now (docs/spec/week-collector.md#window).
type WeekRequest struct {
	Paths       []string  // repositories or groups, as given on the command line
	Exclude     []string  // --exclude-repo globs; see ScanRequest.Exclude
	Now         time.Time // the window's end, UTC
	Days        int       // window length when DaysFromCLI; else the policy's [week] days
	DaysFromCLI bool      // recorded in the report as days_from: cli or policy
	Sort        string    // "lines_added" or "commits" when SortFromCLI; else the policy's [week] sort
	SortFromCLI bool      // recorded in the report as sort_from: cli or policy
}

// ScanRequest asks for a full run: history, blame, metrics and ranks, stored
// in facts.db.
type ScanRequest struct {
	Paths []string
	// Exclude holds the --exclude-repo globs (path.Match syntax, already
	// checked), matched against Repo.Name after discovery. Each repository
	// left out becomes a run-level warning with code "excluded-repo", so the
	// report lists it and the store keeps it without a table of its own.
	Exclude        []string
	Now            time.Time
	Jobs           int               // concurrent git blame processes; 0 means min(12, NumCPU)
	IgnoreRevsFile string            // absolute path; "" uses each repo's own file; single-repo runs only
	Rank           []domain.MetricID // metrics to rank, in the order given
	Scope          string            // rank scope: domain.ScopeGroup or domain.ScopeAll
}

// ReportRequest asks for a stored run rendered as a report.
type ReportRequest struct {
	RunID int64             // 0: the latest complete or partial scan run
	Rank  []domain.MetricID // metrics to rank, in the order given
}

// ExplainRequest asks which facts produced one subject's value for one
// metric: a person on the command line; a person, area, repository or
// change for the MCP server's explain(metric, subject).
type ExplainRequest struct {
	RunID   int64           // 0: the latest complete or partial scan run
	Metric  domain.MetricID // Version 0 means the latest version
	Subject domain.Subject  // a person's Key is the canonical email, lowercased
}

// WhoKnowsRequest asks who knows the code under a path.
type WhoKnowsRequest struct {
	RunID int64  // 0: the latest complete or partial scan run
	Path  string // repo-relative path prefix
	Repo  string // Repo.Key; "" searches every repository in the run
}

// MeRequest asks for one person's own page.
type MeRequest struct {
	RunID int64  // 0: the latest complete or partial scan run
	Email string // canonical email, lowercased; required: the command line has no default (me needs --email)
}

// ValidateRequest asks how well each ranked metric separates changes a person
// labelled meaningful from those labelled routine (docs/metrics.md#validity-checks).
type ValidateRequest struct {
	RunID      int64  // 0: the latest complete or partial scan run
	Labels     []byte // the labels CSV (change id, label, labeller), read by the command line
	LabelsName string // where Labels came from, for messages
}
