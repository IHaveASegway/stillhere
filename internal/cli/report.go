package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) reportCommand() *cobra.Command {
	var (
		run   int64
		ranks []string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Print a stored run as a report",
		Long: `report prints a run stored in facts.db, by default the latest complete or
partial scan, with ranks for the metrics named by --rank, or, without --rank,
for those the scan was asked to rank. Every number comes from the run's own
facts and policy, so a report of the same run is the same report. It reads
only facts.db.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			ids, err := parseMetricIDs(ranks)
			if err != nil {
				return err
			}
			req := app.ReportRequest{RunID: run, Rank: ids}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printReport(app.Report(cmd.Context(), d, req))
			})
		},
	}
	addRunFlag(cmd, &run)
	addRankFlag(cmd, &ranks)
	e.addAllFlag(cmd)
	return cmd
}
