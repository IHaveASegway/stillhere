package collect

import (
	"context"
	"errors"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// HistoryMode selects how much history collects.
type HistoryMode int

// The history modes.
const (
	// HistoryWeek collects the first-parent landings in the window with
	// numstat: what week prints.
	HistoryWeek HistoryMode = iota
	// HistoryScan adds the landing map, each commit's own non-blank added
	// and removed lines, canonical co-authors and reverts: what scan stores.
	HistoryScan
)

// HistoryOptions says which landings to collect and how much about them.
type HistoryOptions struct {
	Window domain.Window
	Mode   HistoryMode
}

// HistoryFacts is what History learned about one repository.
type HistoryFacts struct {
	Commits  []domain.Commit
	Landings []domain.Landing
	Changes  []domain.FileChange
	Reverts  []domain.Revert
	Warnings []domain.Warning
}

// History walks the first-parent history of repo.Head over opt.Window and
// streams git's output into facts (docs/spec/week-collector.md).
func History(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt HistoryOptions) (*HistoryFacts, error) {
	return nil, fmt.Errorf("collect.History: %w (#6)", errors.ErrUnsupported)
}
