package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
)

func (e *env) pathsCommand() *cobra.Command {
	var (
		run     int64
		repo    string
		role    string
		skipped bool
	)
	cmd := &cobra.Command{
		Use:   "paths [PATH...]",
		Short: "List each path of a run with its role and the rule that decided it",
		Long: `paths lists every path a stored run classified, at HEAD or touched by its
history, with the class the classifier gave it (a role such as prod, test,
doc or iac, or why it was skipped: denied, too big, generated header,
unclassified) and the rule that decided it: a deny pattern and its line,
a [[roles.path]] rule and its reason, an extension or a name, a size bound,
a linguist attribute, a generated header. The rules come from the run's own
policy, which the run keeps, so the answer is what the scan counted, whatever
stillhere.toml says today. It reads a stored run, by default the latest
complete or partial scan, and lists every path; JSON holds the same.

Each PATH is a prefix relative to a repository's root, such as src or
examples/hello.rs, as who-knows takes one; with none, every path is listed.
--role keeps the paths of one role and --skipped the skipped ones, which a
report's "skipped paths" counts.`,
		Example: `  stillhere paths --skipped
  stillhere paths --role prod examples benches
  stillhere paths --repo https://github.com/OWNER/NAME.git --format json`,
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			req := app.PathsRequest{RunID: run, Prefixes: args, Repo: repo, Role: role, Skipped: skipped}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.Paths(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository `KEY` to list (default every repository in the run)")
	cmd.Flags().StringVar(&role, "role", "", "list only the paths with this `ROLE`, one of "+domain.RoleList())
	cmd.Flags().BoolVar(&skipped, "skipped", false, "list only the paths the classifier skipped: denied, too big, generated header, unclassified")
	addRunFlag(cmd, &run)
	return cmd
}
