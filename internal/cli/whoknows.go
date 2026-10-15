package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) whoKnowsCommand() *cobra.Command {
	var (
		run  int64
		repo string
	)
	cmd := &cobra.Command{
		Use:   "who-knows PATH",
		Short: "Show who knows the code under a path",
		Long: `who-knows lists the people who know the code under PATH, a path prefix inside
a repository: majority owners, the top authors of surviving lines and recent
landers. It is a routing answer, not a rank. It reads a stored run, by
default the latest complete or partial scan.

PATH is relative to a repository's root, such as src or src/pay.go, as the
run stores it, and not a path on disk: an absolute path, or one relative to
the working directory or to a group, names nothing in the run, and the
answer then says so rather than "nobody".`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			req := app.WhoKnowsRequest{RunID: run, Path: args[0], Repo: repo}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.WhoKnows(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository `KEY` to search (default every repository in the run)")
	addRunFlag(cmd, &run)
	return cmd
}
