package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

func (e *env) egressCommand() *cobra.Command {
	var egress string
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Print the AI-assisted shares the MCP server let leave the machine",
		Long: `egress prints the egress record (ADR 0015): for each run an MCP server answered
about, the AI-assisted shares it decided may leave the machine, recorded
before the first of them did, with the numbers as they left, how many people
each share is made of, and the stillhere version that decided. A run's
decision is made once and kept: every later answer about the run, by any
version, sends the same shares, and a later run's shares leave only when they
cannot be subtracted from these.

It reads the record and never writes it; the record holds no email, only
scopes, numbers and salted hashes of the people behind each share.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			path, err := egressPath(egress)
			if err != nil {
				return err
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return usagef("--egress %s: %v", path, err)
			}
			ledger, err := sqlite.OpenEgressReadOnly(cmd.Context(), abs)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := ledger.Close(); cerr != nil {
					err = errors.Join(err, fmt.Errorf("closing %s: %w", abs, cerr))
				}
			}()
			return e.printAnswer(app.Egress(cmd.Context(), app.Deps{Egress: ledger, Log: e.log}, app.EgressRequest{Record: abs}))
		},
	}
	addEgressFlag(cmd.Flags(), &egress)
	return cmd
}
