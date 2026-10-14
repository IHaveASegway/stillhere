package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func (e *env) metricsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "List every metric version with its definition",
		Long: `metrics lists the metric registry: each metric@version with its question,
unit, eligibility rule, blind spots, how it can be gamed, and its validity
status (docs/metrics.md).`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("metrics command: %w (#11)", errors.ErrUnsupported)
		},
	}
}
