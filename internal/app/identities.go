package app

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// IdentitiesRequest asks for the identity candidates of a stored run.
type IdentitiesRequest struct {
	RunID int64        // 0: the latest complete or partial scan run
	Scope domain.Scope // the zero Scope: every repository of the run; else a group or one repository
}

// Identities suggests .mailmap merges for the people of a stored run, each
// with its evidence, and applies none (#26). The identities command and the
// MCP server's identity_candidates tool both answer through it.
func Identities(ctx context.Context, d Deps, r IdentitiesRequest) (*report.Identities, error) {
	return nil, fmt.Errorf("app.Identities: %w (#26)", domain.ErrNotImplemented)
}
