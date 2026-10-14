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
default the latest complete one.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			req := app.WhoKnowsRequest{RunID: run, Path: args[0], Repo: repo}
			return e.with(cmd.Context(), need{policy: true, store: true}, func(d app.Deps) error {
				res, err := app.WhoKnows(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				return e.printJSON(res)
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository `KEY` to search (default every repository in the run)")
	addRunFlag(cmd, &run)
	return cmd
}
