package cli

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/github"
	"github.com/IHaveASegway/stillhere/internal/redact"
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
each one current, and copies pull requests, their reviews, labels and commits
into facts.db. It is the only command that uses the network. Each later sync
reads only what changed since the last one. A repository that fails is
reported and the others continue.

Scan the mirrors with: stillhere scan <mirrors>/<org>

The token comes from GH_TOKEN or GITHUB_TOKEN, else from gh auth login. It is
passed to git per command, in the environment, and is never written to a
remote URL, config, log or report.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if req.Org == "" {
				return usagef("sync github needs --org ORG (see '%s --help')", cmd.CommandPath())
			}
			// GitHub's own name rule, the adapter's, before a run is begun:
			// the run records its options, and a URL pasted as an
			// organization carries a token as often as not (#71).
			if !github.ValidName(req.Org) {
				return usagef("invalid --org %q: want the login of a GitHub organization or user, such as acme",
					redact.Scrub(req.Org))
			}
			// The mirrors are cloned under --mirrors, and a scan stores
			// their paths: a URL there would put its token in both (#71).
			if err := localPath("--mirrors", req.Mirrors); err != nil {
				return err
			}
			if req.Mirrors == "" {
				home, err := stillhereHome()
				if err != nil {
					return err
				}
				req.Mirrors = filepath.Join(home, "mirrors")
			}
			mirrors, err := filepath.Abs(req.Mirrors)
			if err != nil {
				return usagef("--mirrors %s: %v", req.Mirrors, err)
			}
			req.Mirrors = mirrors
			req.Now = e.now
			// The forge adapter runs the mirror commands with a git runner
			// of its own, so the use case needs no git runner.
			return e.with(cmd.Context(), need{store: true, write: true, forge: true}, func(d app.Deps) error {
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
