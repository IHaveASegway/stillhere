// Package app holds stillhere's use cases: week, scan, report, explain,
// who-knows, me, validate and sync. Each one orchestrates the core and the
// collectors and reaches the outside world only through the ports it is
// handed in Deps, so it never imports an adapter, os/exec, database/sql or
// cobra (docs/spec/codebase.md#the-dependency-rule).
//
// Requests arrive already parsed and validated by the command line: a use
// case never sees a flag, and the command line, not the use case, picks the
// output format.
package app

import (
	"log/slog"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"

	// errgroup bounds the per-repository fan-out of Week and Scan (#11).
	// Importing it here pins the version in go.mod for every branch.
	_ "golang.org/x/sync/errgroup"
)

// Deps are the ports and settings a use case runs with, built by the
// composition root in internal/cli.
type Deps struct {
	Git        domain.GitRunner   // nil for commands that only read facts.db
	Store      domain.FactStore   // nil for week
	Forge      domain.ForgeSource // sync only: the forge's API
	ForgeStore domain.ForgeStore  // sync only: the forge replica in facts.db
	Policy     *policy.Policy     // week and scan only; a read use case rebuilds the run's own (policy.FromSource on Run.PolicySource)
	Log        *slog.Logger
	Version    string // the tool version recorded on every run
}

// WeekRequest asks what landed on the default branch of each repository in
// the window ending at Now (docs/spec/week-collector.md#window).
type WeekRequest struct {
	Paths       []string  // repositories or groups, as given on the command line
	Now         time.Time // the window's end, UTC
	Days        int       // window length when DaysFromCLI; else the policy's [week] days
	DaysFromCLI bool      // recorded in the report as days_from: cli or policy
	Sort        string    // "lines_added" or "commits" when SortFromCLI; else the policy's [week] sort
	SortFromCLI bool      // recorded in the report as sort_from: cli or policy
}

// ScanRequest asks for a full run: history, blame, metrics and ranks, stored
// in facts.db.
type ScanRequest struct {
	Paths          []string
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

// ExplainRequest asks which facts produced one person's value for one metric.
type ExplainRequest struct {
	RunID  int64           // 0: the latest complete or partial scan run
	Metric domain.MetricID // Version 0 means the latest version
	Person string          // canonical email, lowercased
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
	Email string // canonical email, lowercased; "" lets the use case choose a default
}

// ValidateRequest asks how well each ranked metric separates changes a person
// labelled meaningful from those labelled routine (docs/metrics.md#validity-checks).
type ValidateRequest struct {
	RunID  int64  // 0: the latest complete or partial scan run
	Labels string // absolute path of the labels CSV: change id, label, labeller
}
