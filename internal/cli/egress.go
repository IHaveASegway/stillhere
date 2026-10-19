package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

func (e *env) egressCommand() *cobra.Command {
	var egress string
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Print the AI-assisted shares the MCP server may let leave the machine",
		Long: `egress prints the egress record (ADR 0015): each run's decision about the
AI-assisted shares the MCP server may let leave the machine, recorded with
the run's whole decision before the first of them was about to leave, so a
share listed need not have been sent. Each comes with the numbers as they
would leave, how many people it is made of, the repository's name beside its
key, the stillhere version that decided, and the earlier decisions it was
decided against: every one recorded before it for a window that overlaps its
own. A run's decision is made once and kept: every later answer about the
run, by any version, sends the same shares, and a later run's shares leave
only when they cannot be subtracted from these.

It reads the record and never writes it, so it reads a copy or a record on a
read-only mount too; beside a record it can write, SQLite may make the -wal
and -shm files a reader needs. The record holds no email, only scopes,
numbers, a digest of each repository's landings, and the people behind each
share numbered within their decision, which names no one outside it.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			abs, err := egressPath(egress, false)
			if err != nil {
				return err
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
