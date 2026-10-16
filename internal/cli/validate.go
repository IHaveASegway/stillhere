package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) validateCommand() *cobra.Command {
	var (
		run      int64
		labels   string
		labeller string
	)
	cmd := &cobra.Command{
		Use:   "validate [--labels FILE] [--labeller NAME]",
		Short: "Check how well each ranked metric separates meaningful from routine changes",
		Long: `validate reads hand labels (change, meaningful, routine or noise, labeller)
and reports, per metric version, how well the metric separates meaningful from
routine changes as an AUC, where 0.5 is chance, with its 95% interval. A change
is a landing's SHA (or 7 or more of its first hex digits) or a pull request,
written #950, pr:950 or owner/name#950; every other line that starts with #
is a comment. Each metric scores a change at its own grain: durable@1 and
simplifying@1 by its landings, reviews_given@1 by its pull request. A change
counts once: each labeller's last row for it, and over several labellers the
label most of them gave; --labeller NAME scores one labeller's labels alone.

The bar is an AUC of at least 0.7 on at least 50 scored labels. Labels that
clear it are evidence about the code they judge; the beta mark on stillhere's
ranks changes only when a metric's published Validity line does.

'stillhere label' makes the file. The labels judge colleagues' work, so it is
kept beside the facts database ($STILLHERE_HOME/labels.csv), outside every
repository, unless --labels names another.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			// The command line reads the file, so the use case does no I/O
			// outside its ports.
			flag, path, err := e.labelsPath(labels)
			if err != nil {
				return err
			}
			e.warnInRepository(flag, path)
			data, err := readLabelsFile(flag, path, false)
			if err != nil {
				return err
			}
			req := app.ValidateRequest{RunID: run, Labels: data, LabelsName: path, Labeller: labeller}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.Validate(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&labels, "labels", "", "labels CSV `FILE` (default labels.csv beside the facts database, as 'stillhere label' writes it)")
	cmd.Flags().StringVar(&labeller, "labeller", "", "score only the rows of the labeller `NAME`")
	addRunFlag(cmd, &run)
	return cmd
}
