package collect

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file holds the co-author reader that the history (#6) and blame (#7)
// collectors share, so trailers are parsed and canonicalised in one place
// (docs/spec/codebase.md#working-in-parallel). #6 implements it; #7 calls it
// for the commits blame attributes lines to, which can be older than any
// history window.

// coAuthors returns the Co-authored-by trailers of each of shas, keyed by
// full SHA, in trailer order and canonicalised with git's mailmap. A commit
// without co-authors is absent from the map. It reads every commit in one
// batched call (git log --no-walk --stdin with
// %(trailers:key=Co-authored-by,valueonly)), matching the trailer key in any
// case, and canonicalises the identities with one git check-mailmap --stdin.
func coAuthors(ctx context.Context, git domain.GitRunner, repo domain.Repo, shas []string) (map[string][]domain.Person, error) {
	return nil, fmt.Errorf("collect.coAuthors: %w (#6)", domain.ErrNotImplemented)
}
