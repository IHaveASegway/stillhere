package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/mcp"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

func (e *env) mcpCommand() *cobra.Command {
	var (
		run         int64
		allowSource bool
		allowEmails bool
		me          string
		egress      string
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve facts.db to an AI agent over MCP (stdio)",
		Long: `mcp runs a local Model Context Protocol server on stdin and stdout that
answers an agent's questions about a platform (a group of repositories) or one
repository from facts.db: overview, person, who_knows, changes, explain,
highlights, identity_candidates, metrics and diff. It reads the latest complete
or partial scan at each call, or the run --run names, and opens facts.db so
that it cannot write to it. Only protocol messages go to stdout; the log goes
to stderr.

Every answer goes on to the agent's model provider, so the server withholds
by default what the provider does not need: emails become stable ids unless
--allow-emails, commit subjects and diffs stay local unless --allow-source,
and a person's AI-assisted share is shown only to that person, the one --me
names (ADR 0007). A repository's or a group's AI-assisted share leaves only
once the server has recorded it in its egress record, --egress (ADR 0015),
and only when it cannot be subtracted from shares sent before; while the
record cannot be written, every such share is withheld. Keep the record:
unlike facts.db it cannot be made again, and a new one forgets what left.
'stillhere egress' prints what it holds.

To use it from Claude Code, register stillhere's absolute path: Claude Code
starts the server with its own PATH, which need not hold stillhere. command -v
stillhere must print that path; nothing printed means stillhere is not on your
PATH, and the line below would register an empty command.

  claude mcp add --scope user stillhere -- "$(command -v stillhere)" mcp --me you@example.com`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			if e.policyPath != "" {
				e.log.Warn("--policy is ignored: mcp reads stored runs, and a stored run carries its own policy")
			}
			email := canonicalEmail(me)
			if me != "" && !strings.Contains(email, "@") {
				return usagef("invalid --me %q: want your canonical email, as the reports key you (after .mailmap)", me)
			}
			path, err := e.databasePath()
			if err != nil {
				return err
			}
			record, err := egressPath(egress, true)
			if err != nil {
				return err
			}
			cfg := mcp.Config{
				// Opened on the first call that needs it, and read-only:
				// the server never creates, migrates or cleans up facts.db.
				Open: func(ctx context.Context) (domain.FactStore, error) {
					st, err := sqlite.OpenReadOnly(ctx, path)
					if err != nil {
						return nil, err
					}
					return st, nil
				},
				// The egress record: the one file this server writes,
				// opened the first time an answer would hold a share.
				Egress: func(ctx context.Context) (domain.EgressLedger, error) {
					ledger, err := sqlite.OpenEgress(ctx, record)
					if err != nil {
						return nil, err
					}
					return ledger, nil
				},
				RunID:       run,
				AllowSource: allowSource,
				AllowEmails: allowEmails,
				Me:          email,
				Version:     e.build.Version,
				Log:         e.log,
			}
			if allowSource {
				// Only the diff tool runs git, and only with the analysis
				// profile: nothing is fetched or written.
				git, err := e.gitRunner(cmd.Context())
				if err != nil {
					return err
				}
				cfg.Git = git
			}
			// --db is typed by the user and may be anything, a URL with a
			// token included (#71).
			e.log.Info("serving MCP on stdio", "db", redact.Scrub(path), "egress", redact.Scrub(record), "run", run,
				"allow_source", allowSource, "allow_emails", allowEmails, "me", email != "")
			return mcp.Serve(cmd.Context(), cfg, os.Stdin, e.stdout)
		},
	}
	f := cmd.Flags()
	f.Int64Var(&run, "run", 0, "serve run `ID` only (default the latest complete or partial scan, read at each call)")
	f.BoolVar(&allowSource, "allow-source", false, "let the agent read commit subjects and diffs (source leaves the machine)")
	f.BoolVar(&allowEmails, "allow-emails", false, "include people's emails in answers (default: stable ids in their place)")
	f.StringVar(&me, "me", "", "your canonical `EMAIL`: your own answers include your own AI-assisted share")
	addEgressFlag(f, &egress)
	return cmd
}

// addEgressFlag defines --egress, the egress record's file, on mcp and on
// egress.
func addEgressFlag(f *pflag.FlagSet, egress *string) {
	f.StringVar(egress, "egress", "",
		"egress record `FILE`: the AI-assisted shares the MCP server lets leave (default $STILLHERE_HOME/egress.db; an absolute path for mcp)")
}

// egressPath resolves --egress, else $STILLHERE_HOME/egress.db: one record
// per home, whichever facts.db --db names, since a record keys each run by
// its id and start time and checks a new run against every run whose
// window overlaps its own that any server of the home answered about. The
// server's (server) must be absolute, as STILLHERE_HOME must: a relative
// one would give every directory the server starts in a record of its own
// (Claude Code starts a server registered with --scope user in each
// project's), and each would forget what the others let leave. The
// reader's is resolved against the working directory.
func egressPath(flag string, server bool) (string, error) {
	if flag != "" {
		if err := localPath("--egress", flag); err != nil {
			return "", err
		}
		if !filepath.IsAbs(flag) {
			if server {
				return "", usagef("--egress %q is a relative path, which names another egress record in every directory "+
					"the server starts in, each forgetting what the others let leave: pass an absolute path (such as \"$PWD/%s\")",
					flag, flag)
			}
			abs, err := filepath.Abs(flag)
			if err != nil {
				return "", usagef("--egress %s: %v", flag, err)
			}
			return abs, nil
		}
		return flag, nil
	}
	home, err := stillhereHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "egress.db"), nil
}
