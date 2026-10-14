package app

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// SyncRequest asks to bring an organization's mirrors and forge facts up to
// date: the only use case that touches the network (ADR 0004).
type SyncRequest struct {
	Org             string // GitHub organization login
	Mirrors         string // directory of the bare mirrors, one <owner>/<repo>.git each
	IncludeArchived bool
	IncludeForks    bool
}

// Sync lists the organization's repositories, fetches their bare mirrors and
// upserts pull requests and reviews into facts.db.
func Sync(ctx context.Context, d Deps, r SyncRequest) error {
	return fmt.Errorf("app.Sync: %w (#14)", domain.ErrNotImplemented)
}
