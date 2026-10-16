package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/report"
)

func (e *env) validateCommand() *cobra.Command {
	var (
		run      int64
		labels   string
		labeller string
		scores   bool
	)
	cmd := &cobra.Command{
		Use:   "validate [--labels FILE] [--labeller NAME] [--scores]",
		Short: "Check how well each ranked metric separates meaningful from routine changes",
		Long: `validate reads hand labels (change, meaningful, routine or noise, labeller)
and reports, per metric version, how well the metric separates meaningful from
routine changes as an AUC, where 0.5 is chance, with its 95% interval. A change
is a landing's SHA (or 7 or more of its first hex digits) or a pull request,
written #950, pr:950 or owner/name#950; every other line that starts with #
is a comment. Each metric scores a change at its own grain: durable@1 and
simplifying@1 by its landings, reviews_given@1 by its pull request. A change
counts once: each labeller's last row for it, and over several labellers the
label most of them gave; --labeller NAME scores one labeller's labels alone.
Beside each AUC, "size alone" is the AUC of change size (lines added and
removed) over the same changes: a metric that does not beat it says no more
than that big changes matter. Under the table, each metric's verdict says what
its numbers mean, in a Validity line's words (docs/metrics.md#validity-checks):
whether it separates meaningful from routine changes, and whether it beats
change size, by a paired comparison of the two AUCs on the same changes.

The bar is an AUC of at least 0.7 on at least 50 scored labels; it asks nothing
of change size, which the verdict answers. Labels that clear it are evidence
about the code they judge; the beta mark on stillhere's ranks changes only when
a metric's published Validity line does.

--scores writes each labelled change's label, size and score by each ranked
metric as a CSV on stdout (the answer goes to stderr), or adds them to the JSON
with --format json: the numbers each AUC is computed from, so the AUC can be
recomputed, or set against the change's size on exactly the changes each
metric scored. Keep the file where the labels are, outside your repositories:
it judges the same work.

'stillhere label' makes the file. The labels judge colleagues' work, so it is
kept beside the facts database ($STILLHERE_HOME/labels.csv), outside every
repository, unless --labels names another.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			if scores && e.format == formatMarkdown {
				return usagef("--scores writes a CSV, or JSON with --format json, not Markdown")
			}
			// The command line reads the file, so the use case does no I/O
			// outside its ports.
			flag, path, err := e.labelsPath(labels)
			if err != nil {
				return err
			}
			e.warnInRepository(flag, path)
			data, err := readLabelsFile(flag, path, false)
			if err != nil {
				return err
			}
			req := app.ValidateRequest{RunID: run, Labels: data, LabelsName: path, Labeller: labeller, Scores: scores}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				v, err := app.Validate(cmd.Context(), d, req)
				if err != nil || !scores || e.format == formatJSON {
					return e.printAnswer(v, err)
				}
				// The CSV is the output; the answer, its AUCs and caveats, is
				// what the person reads meanwhile, as label --sheet's header.
				if err := report.Text(e.stderr, v); err != nil {
					return err
				}
				return report.Scores(e.stdout, v)
			})
		},
	}
	cmd.Flags().StringVar(&labels, "labels", "", "labels CSV `FILE` (default labels.csv beside the facts database, as 'stillhere label' writes it)")
	cmd.Flags().StringVar(&labeller, "labeller", "", "score only the rows of the labeller `NAME`")
	cmd.Flags().BoolVar(&scores, "scores", false, "write each labelled change's label, size and scores as a CSV (JSON with --format json)")
	addRunFlag(cmd, &run)
	return cmd
}
