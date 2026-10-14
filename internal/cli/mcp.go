package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

func (e *env) mcpCommand() *cobra.Command {
	var allowSource bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve facts.db to an AI agent over MCP (stdio)",
		Long: `mcp runs a local Model Context Protocol server on stdin and stdout that
answers an agent's questions from facts.db. It only reads. Diffs are exposed
only with --allow-source.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("mcp command: %w (#15)", domain.ErrNotImplemented)
		},
	}
	cmd.Flags().BoolVar(&allowSource, "allow-source", false, "let the agent read diffs and source")
	return cmd
}
