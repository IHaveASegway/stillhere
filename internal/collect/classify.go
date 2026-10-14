package collect

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// PathAt names a path at a commit: a week path that is absent at Head and is
// sized at the landing that touched it.
type PathAt struct{ Commit, Path string }

// ClassifyPaths classifies every path in the tree at repo.Head, plus the
// absent paths, with one batched call per git question: ls-tree for sizes,
// check-attr --source for linguist attributes, grep for generated headers,
// and cat-file --batch-check for absent paths
// (docs/spec/classification.md).
func ClassifyPaths(ctx context.Context, git domain.GitRunner, repo domain.Repo, p *policy.Policy, absent []PathAt) ([]domain.PathClass, error) {
	return nil, fmt.Errorf("collect.ClassifyPaths: %w (#5)", domain.ErrNotImplemented)
}
