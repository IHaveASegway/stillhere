package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) validateCommand() *cobra.Command {
	var (
		run    int64
		labels string
	)
	cmd := &cobra.Command{
		Use:   "validate --labels FILE",
		Short: "Check how well each ranked metric separates meaningful from routine changes",
		Long: `validate reads hand labels (change id, meaningful, routine or noise, labeller)
and reports, per metric version, how well the metric separates meaningful from
routine changes as an AUC, where 0.5 is chance. The labels judge colleagues'
work, so keep the file outside the repository.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			if labels == "" {
				return usagef("validate needs --labels FILE (see '%s --help')", cmd.CommandPath())
			}
			path, err := absFile("--labels", labels)
			if err != nil {
				return err
			}
			req := app.ValidateRequest{RunID: run, Labels: path}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				res, err := app.Validate(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				return e.printJSON(res)
			})
		},
	}
	cmd.Flags().StringVar(&labels, "labels", "", "labels CSV `FILE` (required)")
	addRunFlag(cmd, &run)
	return cmd
}
