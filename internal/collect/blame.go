package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// BlameOptions configures one repository's blame.
type BlameOptions struct {
	Paths          []string      // classified paths that received a role
	IgnoreRevsFile string        // CLI override; single-repo runs only
	Slots          chan struct{} // shared by every repo: one slot per running git blame
	Timeout        time.Duration // per file; 120 s in the spec
	Progress       func(done, total int)
}

// BlameFacts is what Blame learned about one repository.
type BlameFacts struct {
	Owners     []domain.LineOwner
	IgnoreRevs domain.IgnoreRevs
	Errors     []domain.FileError
}

// Blame attributes the non-blank lines of every path at repo.Head to the
// commits that wrote them (docs/spec/blame-collector.md). A failure on one
// file is recorded and the repository continues.
func Blame(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt BlameOptions) (*BlameFacts, error) {
	return nil, fmt.Errorf("collect.Blame: %w (#7)", domain.ErrNotImplemented)
}
