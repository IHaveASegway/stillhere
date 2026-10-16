package cli

import (
	"strings"

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
		Use:   "explain METRIC --person WHO",
		Short: "Trace one person's value for a metric to the changes behind it",
		Long: `explain shows the facts behind one person's value for one metric in a stored
run, by default the latest complete or partial scan: the changes a ranked
metric counted, the landings landed@2 counts, the commits whose lines
surviving@1 sums, the files majority_owner@1 counts. METRIC is an id such as
durable@1, or a bare name such as durable for its latest version.

WHO names the person as a page does: a display name as the reports print it
("Ada Example", or the start of each word, "ada ex"), a GitHub login
(@ada), an id from an MCP answer (p-…), or their canonical email from
--format json. Two people of one name are refused, listed with their ids.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := domain.ParseMetricID(args[0])
			if err != nil {
				return err
			}
			if err := checkRun(run); err != nil {
				return err
			}
			if strings.TrimSpace(person) == "" {
				return usagef("explain needs --person WHO: a display name, @login, id or email (see '%s --help')", cmd.CommandPath())
			}
			req := app.ExplainRequest{RunID: run, Metric: id, Person: person}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.Explain(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "the person: a display name, @login, id (p-…) or canonical email (required)")
	addRunFlag(cmd, &run)
	e.addAllFlag(cmd)
	return cmd
}
