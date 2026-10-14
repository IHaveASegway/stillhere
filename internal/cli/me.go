package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) meCommand() *cobra.Command {
	var (
		run   int64
		email string
	)
	cmd := &cobra.Command{
		Use:   "me",
		Short: "Show your own page: what your changes did",
		Long: `me shows one person's own page from a stored run, by default the latest
complete one: their values and ranks for every metric. Each person sees
exactly what their lead sees about them.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			req := app.MeRequest{RunID: run, Email: canonicalEmail(email)}
			return e.with(cmd.Context(), need{policy: true, store: true}, func(d app.Deps) error {
				res, err := app.Me(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				return e.printJSON(res)
			})
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "your canonical `EMAIL`")
	addRunFlag(cmd, &run)
	return cmd
}
