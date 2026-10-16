package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

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
		Use:   "github (--org ORG | --repo OWNER/NAME...)",
		Short: "Mirror repositories and sync their pull requests and reviews",
		Long: `sync github keeps a bare mirror of each repository of an organization (--org),
or of the repositories you name (--repo, repeatable), and copies their pull
requests, with reviews, labels and commits, into facts.db. It is the only
command that uses the network. A repository that fails is reported and the
others continue, and the last line says how the sync went.

A first sync reads the pull requests updated in the last 365 days, the window
every metric reads (--since reaches further back), newest first; a later one
reads what changed since the last. Then it checks the copy against GitHub's
own count, and reads the window again at once if pull requests are missing.
An interrupted sync loses nothing: the next one reads the same range again.
A first sync of the cli organization (cli/cli: 1,300 pull requests updated in
a year) took 2 minutes; of go-chi's 20 repositories, 28 seconds; a sync with
nothing new, a few seconds per repository. Three repositories sync at once.

Archived repositories and forks of an organization are left out, and their
mirrors moved to <mirrors>/.stale, unless --include-archived or
--include-forks; a repository --repo names is synced whatever its flags.

Scan the mirrors with: stillhere scan <mirrors>/<owner>

The token comes from GH_TOKEN or GITHUB_TOKEN, else from gh auth login. It is
passed to git per command, in the environment, and is never written to a
remote URL, config, log or report.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkSyncTargets(cmd, req); err != nil {
				return err
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
			if !req.Since.IsZero() && !req.Since.Before(req.Now) {
				return usagef("--since %s is not in the past", req.Since.Format(time.DateOnly))
			}
			// The forge adapter runs the mirror commands with a git runner
			// of its own, so the use case needs no git runner.
			return e.with(cmd.Context(), need{store: true, write: true, forge: true}, func(d app.Deps) error {
				sum, err := app.Sync(cmd.Context(), d, req)
				if sum != nil {
					printSyncSummary(e.stdout, sum, cmd.Context().Err() != nil)
				}
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Org, "org", "", "GitHub organization or user `ORG`: its repositories")
	f.StringArrayVar(&req.Repos, "repo", nil, "repository `OWNER/NAME` to sync, instead of --org (repeatable)")
	f.StringVar(&req.Mirrors, "mirrors", "", "mirror `DIR` (default $STILLHERE_HOME/mirrors)")
	f.BoolVar(&req.IncludeArchived, "include-archived", false, "also mirror an organization's archived repositories")
	f.BoolVar(&req.IncludeForks, "include-forks", false, "also mirror an organization's forks")
	f.Var(timeFlag{&req.Since}, "since",
		"also read the pull requests updated since `DATE`, an RFC 3339 time or a date for midnight UTC, "+
			"when it is before the 365 days a sync reads anyway")
	return cmd
}

// checkSyncTargets checks --org and --repo before the sync begins its run:
// exactly one of them, each name by GitHub's own name rule (the adapter's),
// since the run records its options and a URL pasted as a name carries a
// token as often as not (#71).
func checkSyncTargets(cmd *cobra.Command, req app.SyncRequest) error {
	switch {
	case req.Org == "" && len(req.Repos) == 0:
		return usagef("sync github needs --org ORG or --repo OWNER/NAME (see '%s --help')", cmd.CommandPath())
	case req.Org != "" && len(req.Repos) > 0:
		return usagef("sync github takes --org or --repo, not both: --repo syncs the repositories it names, " +
			"--org every repository of an organization")
	case req.Org != "" && !github.ValidName(req.Org):
		return usagef("invalid --org %q: want the login of a GitHub organization or user, such as acme",
			redact.Scrub(req.Org))
	}
	for _, r := range req.Repos {
		owner, name, ok := strings.Cut(r, "/")
		if !ok || !github.ValidName(owner) || !github.ValidName(name) {
			return usagef("invalid --repo %q: want OWNER/NAME, such as cli/cli", redact.Scrub(r))
		}
	}
	return nil
}

// printSyncSummary prints a sync's closing line to stdout, for every sync
// that began its run: what a scheduled sync's log needs to tell success
// from a partial or an incomplete sync, whose exit code is 0 too
// (docs/spec/report-json.md#exit-codes).
func printSyncSummary(w io.Writer, s *app.SyncSummary, interrupted bool) {
	var b strings.Builder
	b.WriteString("sync github: ")
	if interrupted {
		b.WriteString("interrupted; ")
	}
	if s.Repos == 0 {
		b.WriteString("no repository to sync")
	} else {
		fmt.Fprintf(&b, "%d of %s synced", s.Synced, plural(s.Repos, "repository", "repositories"))
	}
	if s.Failed > 0 {
		fmt.Fprintf(&b, ", %d failed", s.Failed)
	}
	if s.Incomplete > 0 {
		fmt.Fprintf(&b, ", %d with pull requests missing (the next sync reads them again)", s.Incomplete)
	}
	if s.Unchecked > 0 {
		fmt.Fprintf(&b, ", %d not checked against GitHub's count", s.Unchecked)
	}
	fmt.Fprintf(&b, "; %s read in %s", plural(s.Pulls, "pull request", "pull requests"), s.Took.Round(time.Second))
	_, _ = fmt.Fprintln(w, b.String())
}

// plural is "1 repository", "2 repositories".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
