package cli

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// forgeSource builds the GitHub adapter that sync github uses: a token from
// GITHUB_TOKEN or GH_TOKEN, else gh's stored token, never written anywhere
// (docs/spec/git-invocation.md#credentials). This file belongs to the
// GitHub sync (#14), so wiring the adapter touches no other issue's file.
func (e *env) forgeSource(ctx context.Context) (domain.ForgeSource, error) {
	return nil, fmt.Errorf("GitHub forge source: %w (#14)", domain.ErrNotImplemented)
}
