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
		Use:   "me --email EMAIL",
		Short: "Show your own page: what your changes did",
		Long: `me shows one person's own page from a stored run, by default the latest
complete or partial scan: their values and ranks for every metric. That is the
facts their lead sees about them, plus their own AI-assisted share, which no
report, rank table or explain shows.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			req := app.MeRequest{RunID: run, Email: canonicalEmail(email)}
			if req.Email == "" {
				// A default would come from the user's git identity, which
				// the hardened runner cannot see (it ignores global config)
				// and which a facts.db reader does not run git for; until
				// that is decided, the email is required.
				return usagef("me needs --email EMAIL (see '%s --help')", cmd.CommandPath())
			}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.Me(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "your canonical `EMAIL` (required)")
	addRunFlag(cmd, &run)
	return cmd
}
