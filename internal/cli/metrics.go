package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
)

func (e *env) metricsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "List every metric version with its definition",
		Long: `metrics lists the metric registry: each metric@version with its question,
unit, eligibility rule, blind spots, how it can be gamed, its fixture oracle
and its validity status (docs/metrics.md). It reads neither git nor
facts.db.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			if e.policyPath != "" {
				e.log.Warn("--policy is ignored: metrics lists definitions, which no policy changes")
			}
			return e.render(app.Metrics())
		},
	}
}
