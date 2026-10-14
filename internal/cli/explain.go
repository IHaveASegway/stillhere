package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
)

func (e *env) explainCommand() *cobra.Command {
	var (
		run    int64
		person string
	)
	cmd := &cobra.Command{
		Use:   "explain METRIC --person EMAIL",
		Short: "Trace one person's value for a metric to the changes behind it",
		Long: `explain shows the facts behind one person's value for one metric in a stored
run, by default the latest complete one. METRIC is an id such as durable@1, or
a bare name such as durable for its latest version.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := domain.ParseMetricID(args[0])
			if err != nil {
				return err
			}
			if err := checkRun(run); err != nil {
				return err
			}
			req := app.ExplainRequest{RunID: run, Metric: id, Person: canonicalEmail(person)}
			if req.Person == "" {
				return usagef("explain needs --person EMAIL (see '%s --help')", cmd.CommandPath())
			}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				res, err := app.Explain(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				return e.printJSON(res)
			})
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "the person's canonical `EMAIL` (required)")
	addRunFlag(cmd, &run)
	return cmd
}
