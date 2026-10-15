package cli

import (
	"github.com/spf13/cobra"
)

// policyCommand prints the policy week and scan would use here as a
// complete policy file (#71): the way to start one's own, which every team
// needs to name its own bot accounts, without copying the packaged deny
// list's ~240 patterns by hand.
func (e *env) policyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "policy",
		Short: "Print the policy a scan would use, as a stillhere.toml to edit",
		Long: `policy prints the policy that week and scan would use here, as a complete
policy file: --policy FILE, else ./stillhere.toml, else the packaged policy,
with the packaged deny list written out in full from generated_paths.txt, so
that the file stands alone and its hash covers every pattern. To start a
policy of your own, such as one that names your team's bot accounts:

  stillhere policy > stillhere.toml

then edit it. A policy file replaces the packaged policy as a whole, so
every table and every key in it stays required. A stored run keeps the
policy it was scanned with, whatever the file says later. It reads neither
git nor facts.db.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			p, err := e.readPolicy(true)
			if err != nil {
				return err
			}
			file, err := p.File()
			if err != nil {
				return err
			}
			_, err = e.stdout.Write(file)
			return err
		},
	}
}
