package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

func (e *env) weekCommand() *cobra.Command {
	var (
		now        time.Time
		days       int
		sort       string
		ignoreRevs string
		exclude    []string
	)
	cmd := &cobra.Command{
		Use:   "week PATH...",
		Short: "Print what landed on each default branch in the last week (offline)",
		Long: `week prints what landed on the default branch of each repository in the
window that ends at --now (landed@2): the scope's total, each landing once,
then one row per person who wrote what landed, with the landings that hold
their commits, those commits, the lines those commits added (all, and on
prod paths), and the merges they landed, and bots in a table of their own. A
merged branch counts for the people who wrote its commits, not for whoever
merged it.

Each PATH is a repository (a work tree or a bare mirror) or a group, a
directory whose immediate children are repositories. week never fetches, so a
stale clone gives a stale answer, and it stores nothing.`,
		Args: pathsArgs,
		RunE: func(cmd *cobra.Command, paths []string) error {
			flags := cmd.Flags()
			req := app.WeekRequest{
				Paths:       paths,
				Exclude:     exclude,
				Now:         e.nowOr(cmd, now),
				Days:        days,
				DaysFromCLI: flags.Changed("days"),
				Sort:        sort,
				SortFromCLI: flags.Changed("week-sort"),
			}
			if req.DaysFromCLI && (days < 1 || days > policy.MaxWeekDays) {
				return usagef("invalid --days %d: want 1 to %d", days, policy.MaxWeekDays)
			}
			if req.SortFromCLI && sort != "lines_added" && sort != "commits" {
				return usagef("invalid --week-sort %q: want lines_added or commits", sort)
			}
			if err := checkGlobs(exclude); err != nil {
				return err
			}
			if flags.Changed("ignore-revs-file") {
				e.log.Warn("--ignore-revs-file is ignored by week: git log has no such option")
			}
			return e.with(cmd.Context(), need{git: true, policy: true}, func(d app.Deps) error {
				return e.printReport(app.Week(cmd.Context(), d, req))
			})
		},
	}
	f := cmd.Flags()
	addNowFlag(cmd, &now)
	f.IntVar(&days, "days", 0, "window length in days (default the policy's [week] days)")
	f.StringVar(&sort, "week-sort", "", "row order: lines_added or commits (default the policy's [week] sort)")
	addExcludeFlag(cmd, &exclude)
	f.StringVar(&ignoreRevs, "ignore-revs-file", "", "accepted for symmetry with scan and ignored: git log has no such option")
	e.addAllFlag(cmd)
	return cmd
}
