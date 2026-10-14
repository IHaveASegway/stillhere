package collect

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Discovery is what Discover found on the paths it was given.
type Discovery struct {
	Repos    []domain.Repo      // health-checked, duplicates removed, sorted by Key
	Groups   []domain.Group     // sorted by Name
	Warnings []domain.Warning   // duplicate-repo, shallow-clone, partial-clone, ...
	Failed   []domain.FileError // PATH slots that were neither a repo nor a group
}

// Discover expands command-line paths into repositories and groups, removes
// duplicates by scrubbed origin URL, resolves each default branch to a SHA,
// and checks clone health (docs/spec/discovery.md).
func Discover(ctx context.Context, git domain.GitRunner, paths []string) (*Discovery, error) {
	return nil, fmt.Errorf("collect.Discover: %w (#4)", domain.ErrNotImplemented)
}
