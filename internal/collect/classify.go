package collect

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// PathAt names a path in the tree of a commit that touched it.
type PathAt struct{ Commit, Path string }

// ClassifyPaths classifies every path in the tree at repo.Head, plus each
// path in touched that Head no longer has, with one batched call per git
// question: ls-tree -l for the paths and sizes at Head, check-attr --source
// for linguist attributes, grep for generated headers, and cat-file
// --batch-check for the sizes of touched paths absent at Head
// (docs/spec/classification.md).
//
// touched lists the paths the history touched, in week and scan runs
// alike: scan needs the paths deleted since too, because durable@1 counts
// the prod lines they added. It holds one entry per path, at the newest
// commit that added lines to it, so the path is in that commit's tree; a
// later entry for the same path is ignored. A touched path that exists at
// Head is classified at Head. The result has one PathClass per distinct
// path, sorted by Path.
func ClassifyPaths(ctx context.Context, git domain.GitRunner, repo domain.Repo, p *policy.Policy, touched []PathAt) ([]domain.PathClass, error) {
	return nil, fmt.Errorf("collect.ClassifyPaths: %w (#5)", domain.ErrNotImplemented)
}
