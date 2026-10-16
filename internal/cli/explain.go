package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
)

func (e *env) explainCommand() *cobra.Command {
	var (
		run                       int64
		person, repo, group, area string
		days                      int
	)
	cmd := &cobra.Command{
		Use:   "explain METRIC (--person WHO | --repo NAME | --group NAME | --area REPO:DIR)",
		Short: "Trace a value of a metric to the changes behind it",
		Long: `explain shows the facts behind one subject's value of one metric in a stored
run, by default the latest complete or partial scan: the changes a ranked
metric counted, the landings landed@2 counts, the commits whose lines
surviving@1 sums, the files majority_owner@1 counts, and the changes
review_coverage@1 counts, each with whether someone else reviewed it and
why one was left out. METRIC is an id such as durable@1, or a bare name
such as durable for its latest version.

The subject is one of:
  --person WHO       a person, named as a page names them: a display name
                     ("Ada Example", or the start of each word, "ada ex"),
                     a GitHub login (@ada), an id from an MCP answer (p-…),
                     or their canonical email from --format json; two
                     people of one name are refused, listed with their ids
  --repo NAME        a repository's own numbers, named as the report names
                     it, by its key, or as GROUP/NAME (review_coverage@1,
                     landed@2, knowledge_loss@1, knowledge_concentration@1)
  --group NAME       a group's own numbers
  --area REPO:DIR    a top-level directory of a repository, such as
                     click:src (knowledge_loss@1, knowledge_concentration@1;
                     who-knows answers any path)

--days N sets landed@1's and landed@2's window, for the values and the
landings listed alike: --days 365 traces a still-here table's
"landings 365d".`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := domain.ParseMetricID(args[0])
			if err != nil {
				return err
			}
			if err := checkRun(run); err != nil {
				return err
			}
			named := 0
			for _, s := range []string{person, repo, group, area} {
				if strings.TrimSpace(s) != "" {
					named++
				}
			}
			switch {
			case named == 0:
				return usagef("explain needs a subject: --person WHO, --repo NAME, --group NAME or --area REPO:DIR "+
					"(see '%s --help')", cmd.CommandPath())
			case named > 1:
				return usagef("explain answers about one subject: pass one of --person, --repo, --group and --area")
			case cmd.Flags().Changed("days") && days < 1:
				return usagef("--days %d: want at least 1", days)
			}
			req := app.ExplainRequest{RunID: run, Metric: id, Person: person, Repo: repo, Group: group, Area: area,
				Days: days}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				return e.printAnswer(app.Explain(cmd.Context(), d, req))
			})
		},
	}
	cmd.Flags().StringVar(&person, "person", "", "a person: a display name, @login, id (p-…) or canonical email")
	cmd.Flags().StringVar(&repo, "repo", "", "a repository's own numbers: its name, key, or GROUP/NAME")
	cmd.Flags().StringVar(&group, "group", "", "a group's own numbers: its name")
	cmd.Flags().StringVar(&area, "area", "", "a top-level directory of a repository: REPO:DIR")
	cmd.Flags().IntVar(&days, "days", 0, "landed@1's and landed@2's window in days (default: the run's policy's [week] days)")
	addRunFlag(cmd, &run)
	e.addAllFlag(cmd)
	return cmd
}
