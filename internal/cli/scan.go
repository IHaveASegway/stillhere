package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
)

func (e *env) scanCommand() *cobra.Command {
	var (
		now        time.Time
		jobs       int
		ignoreRevs string
		ranks      []string
		scope      string
		exclude    []string
	)
	cmd := &cobra.Command{
		Use:   "scan PATH...",
		Short: "Record a run in facts.db: history, blame, metrics and ranks (offline)",
		Long: `scan records a run in facts.db: what landed in the last year, whose lines
blame still attributes at each HEAD, and every metric computed from those
facts. It ranks every ranked metric and stores the ranks with the run; --rank
names the rank tables to print, and 'stillhere report --rank' prints any of
them later. A repository that fails is recorded as failed and the run
continues.

Each PATH is a repository or a group, as for week. scan never fetches and
never calls GitHub: the pull requests and reviews it counts are the ones
'stillhere sync github' last copied into facts.db, and the run records when
that was, which every report of it shows.`,
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, paths []string) error {
			ids, err := parseMetricIDs(ranks)
			if err != nil {
				return err
			}
			if jobs < 0 {
				return usagef("invalid --jobs %d: want 1 or more, or 0 for min(12, CPUs)", jobs)
			}
			if scope != domain.ScopeGroup && scope != domain.ScopeAll {
				return usagef("invalid --scope %q: want group or all", scope)
			}
			if err := checkGlobs(exclude); err != nil {
				return err
			}
			req := app.ScanRequest{
				Paths:   paths,
				Exclude: exclude,
				Now:     e.nowOr(cmd, now),
				Jobs:    jobs,
				Rank:    ids,
				Scope:   scope,
			}
			if ignoreRevs != "" {
				if req.IgnoreRevsFile, err = absFile("--ignore-revs-file", ignoreRevs); err != nil {
					return err
				}
			}
			return e.with(cmd.Context(), need{git: true, policy: true, store: true, write: true}, func(d app.Deps) error {
				return e.printReport(app.Scan(cmd.Context(), d, req))
			})
		},
	}
	f := cmd.Flags()
	addNowFlag(cmd, &now)
	f.IntVar(&jobs, "jobs", 0, "concurrent git blame processes (default min(12, CPUs))")
	f.StringVar(&ignoreRevs, "ignore-revs-file", "",
		"blame ignore-revs `FILE` that replaces the repository's own; single-repository runs only")
	addRankFlag(cmd, &ranks)
	f.StringVar(&scope, "scope", domain.ScopeGroup, "rank within each group, or across everything: group or all")
	addExcludeFlag(cmd, &exclude)
	return cmd
}
