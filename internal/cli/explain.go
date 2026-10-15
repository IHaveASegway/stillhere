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
run, by default the latest complete or partial scan. METRIC is an id such as
durable@1, or a bare name such as durable for its latest version.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := domain.ParseMetricID(args[0])
			if err != nil {
				return err
			}
			if err := checkRun(run); err != nil {
				return err
			}
			email := canonicalEmail(person)
			if email == "" {
				return usagef("explain needs --person EMAIL (see '%s --help')", cmd.CommandPath())
			}
			req := app.ExplainRequest{
				RunID:   run,
				Metric:  id,
				Subject: domain.Subject{Kind: domain.SubjectPerson, Key: email},
			}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.Explain(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "the person's canonical `EMAIL` (required)")
	addRunFlag(cmd, &run)
	return cmd
}
