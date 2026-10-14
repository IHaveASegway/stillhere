package sqlite

import "github.com/IHaveASegway/stillhere/internal/domain"

// This file belongs to the GitHub sync (#14). It implements
// domain.ForgeStore on Store: pull requests, reviews and login links,
// upserted by node id, in the tables that migration 2 creates (migration 1
// is #8's schema). Keeping them here leaves store.go to #8.

var _ domain.ForgeStore = (*Store)(nil)
