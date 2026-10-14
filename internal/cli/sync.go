package cli

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) syncCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Bring mirrors and forge facts up to date (the only command that uses the network)",
		Args:  cobra.ArbitraryArgs,
		RunE:  subcommandRequired,
	}
	cmd.AddCommand(e.syncGitHubCommand())
	return cmd
}

func (e *env) syncGitHubCommand() *cobra.Command {
	var req app.SyncRequest
	cmd := &cobra.Command{
		Use:   "github --org ORG",
		Short: "Mirror an organization's repositories and sync pull requests and reviews",
		Long: `sync github lists an organization's repositories, keeps a bare mirror of
each one current, and copies pull requests, reviews and review comments into
facts.db. It is the only command that uses the network. The token is passed
to git per command and is never written to a remote URL, config, log or
report.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if req.Org == "" {
				return usagef("sync github needs --org ORG (see '%s --help')", cmd.CommandPath())
			}
			if req.Mirrors == "" {
				home, err := stillhereHome()
				if err != nil {
					return err
				}
				req.Mirrors = filepath.Join(home, "mirrors")
			}
			return e.with(cmd.Context(), need{git: true, store: true}, func(d app.Deps) error {
				return app.Sync(cmd.Context(), d, req)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Org, "org", "", "GitHub organization `ORG` (required)")
	f.StringVar(&req.Mirrors, "mirrors", "", "mirror `DIR` (default $STILLHERE_HOME/mirrors)")
	f.BoolVar(&req.IncludeArchived, "include-archived", false, "also mirror archived repositories")
	f.BoolVar(&req.IncludeForks, "include-forks", false, "also mirror forks")
	return cmd
}
