package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) whoKnowsCommand() *cobra.Command {
	var (
		run  int64
		repo string
		days int
	)
	cmd := &cobra.Command{
		Use:   "who-knows PATH",
		Short: "Show who knows the code under a path",
		Long: `who-knows lists the people who know the code under PATH, a path prefix inside
a repository: majority owners, the top authors of surviving lines and recent
landers, and recent reviewers when the run read GitHub data. It is a routing
answer, not a rank. For the path it also prints its knowledge loss (the share
of its human-held lines whose people have gone quiet) and its majority-owner
concentration, as the report prints them for a repository. It reads a stored
run, by default the latest complete or partial scan. Each list shows its
first 10 people, and --all every one, each with their last activity (a
landing, a review, a pull request, a commit on any branch) and how soon they
go quiet.

Recent landers and reviewers are those of the last 90 days. --days N sets
another window, at most the history the run read (365 days for a scan), as
explain's --days does for landed: on a repository that lands rarely, the
people who changed a path a few months ago are often the ones to ask.

It counts prod lines; under a path that holds none, such as tests or docs,
it counts that path's own lines (test, doc, ...) and says so. A path the
policy leaves out (unclassified, denied, generated or too big) gets an empty
answer that says why and what would make it count.

PATH is relative to a repository's root, such as src or src/pay.go, as the
run stores it, and not a path on disk: an absolute path, or one relative to
the working directory or to a group, names nothing in the run, and the
answer then says so rather than "nobody". --repo takes a repository's name
as a card heads it, GROUP/NAME, OWNER/NAME, its remote URL or its key.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			if cmd.Flags().Changed("days") && days < 1 {
				return usagef("--days %d: want at least 1", days)
			}
			req := app.WhoKnowsRequest{RunID: run, Path: args[0], Repo: repo, Days: days}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.WhoKnows(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "the repository to search, by `NAME`, OWNER/NAME, remote URL or key (default every repository in the run)")
	cmd.Flags().IntVar(&days, "days", 0, "the window of recent landers and reviewers in days, at most the run's history (default 90)")
	addRunFlag(cmd, &run)
	e.addAllFlag(cmd)
	return cmd
}
