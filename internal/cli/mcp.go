package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

func (e *env) mcpCommand() *cobra.Command {
	var allowSource, allowEmails bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve facts.db to an AI agent over MCP (stdio)",
		Long: `mcp runs a local Model Context Protocol server on stdin and stdout that
answers an agent's questions from facts.db. It only reads. Diffs are exposed
only with --allow-source, and emails only with --allow-emails, because every
answer goes on to the agent's model provider.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("mcp command: %w (#15)", domain.ErrNotImplemented)
		},
	}
	cmd.Flags().BoolVar(&allowSource, "allow-source", false, "let the agent read diffs and source")
	cmd.Flags().BoolVar(&allowEmails, "allow-emails", false, "include people's emails in answers (default: display names only)")
	return cmd
}
