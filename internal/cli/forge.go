package cli

import (
	"context"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/github"
)

// forgeSource builds the GitHub adapter that sync github uses. The adapter
// finds the token itself (GH_TOKEN, GITHUB_TOKEN, else gh's stored token)
// and keeps it, so the token never passes through this package
// (docs/spec/git-invocation.md#credentials). Mirrors get a git runner of
// their own, without the organisation mailmap, which clone and fetch never
// read. This file belongs to the GitHub sync (#14), so wiring the adapter
// touches no other issue's file.
func (e *env) forgeSource(ctx context.Context) (domain.ForgeSource, error) {
	git, err := gitcli.New(ctx, gitcli.Config{Logger: e.log})
	if err != nil {
		return nil, err
	}
	return github.New(ctx, github.Config{
		Git:       git,
		Logger:    e.log,
		UserAgent: "stillhere/" + e.build.Version,
	})
}
