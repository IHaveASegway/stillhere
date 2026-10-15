package cli

import (
	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// identitiesFormatMailmap is the --format only identities accepts: the
// suggested .mailmap lines, with their evidence as comments.
const identitiesFormatMailmap = "mailmap"

func (e *env) identitiesCommand() *cobra.Command {
	var (
		run    int64
		format string
	)
	cmd := &cobra.Command{
		Use:   "identities",
		Short: "Suggest .mailmap lines for addresses that may be one person",
		Long: `identities lists the addresses of a stored run, by default the latest complete
one, that may belong to one person, with the evidence for each: a shared
display name, a shared local part, one GitHub account, or a hand-off (one
address stopped as another started in the same files, which is only ever a
guess). stillhere applies none of them: git's .mailmap is the only identity
store, and a wrong line merges two people into one who does not exist.

--format mailmap prints the suggested .mailmap lines with the evidence as
comments, for a person to review; a weak match or a guess is commented out.
The text and Markdown forms name no email, and --format json gives the
addresses. A line added to a .mailmap takes effect at the next scan.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			switch format {
			case identitiesFormatMailmap, formatText, formatMarkdown, formatJSON:
			default:
				return usagef("invalid --format %q: want mailmap, text, md or json", format)
			}
			if e.mailmapPath != "" {
				e.log.Warn("--mailmap is ignored: identities reads a stored run, which git mailmapped when it was scanned; scan again to apply a mailmap")
			}
			req := app.IdentitiesRequest{RunID: run}
			return e.with(cmd.Context(), need{store: true}, func(d app.Deps) error {
				id, err := app.Identities(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				if format == identitiesFormatMailmap {
					return report.Mailmap(e.stdout, id)
				}
				// This command's --format shadows the global flag, which
				// knows no mailmap form, so render reads the choice here.
				e.format = format
				return e.render(id)
			})
		},
	}
	cmd.Flags().StringVar(&format, "format", formatText, "output format: mailmap (suggested .mailmap lines), text, md or json")
	addRunFlag(cmd, &run)
	return cmd
}
