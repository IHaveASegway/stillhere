package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
	"github.com/IHaveASegway/stillhere/internal/web"
)

func (e *env) serveCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Browse facts.db in a web browser, on this machine only",
		Long: `serve runs a web server on this machine that shows facts.db's runs as pages:
each run's report, one page per repository, the rank tables, every person's
own page, who knows a path, and the changes behind any number (explain).
People, repositories, areas and ranked estimates link to their pages; every
table of a report sorts by a click on its header and downloads as CSV, and a
long one filters as you type. The pages say what the command line says, from
the same use cases.

It is the team web UI's local mode (ADR 0005), for the person who scanned:
it listens on a loopback address only, and every page's address starts
with a random token, which only the address it prints holds. That address
is the key until the server stops: anyone who has it and can reach this
machine's loopback can read every page, so do not paste it anywhere. It
opens facts.db so that it cannot write to it, and reads the latest scan
each time the address itself is opened, so a scan published while it runs
is the next one shown. Ctrl-C stops it. Sign-in for a team is not built
yet: there is no way to serve these pages to other machines.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := web.CheckAddr(addr); err != nil {
				return usagef("--addr %v", err)
			}
			if e.policyPath != "" {
				e.log.Warn("--policy is ignored: serve reads stored runs, and a stored run carries its own policy")
			}
			path, err := e.databasePath()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			store, err := sqlite.OpenReadOnly(ctx, path)
			if err != nil {
				return err
			}
			ln, err := web.Listen(ctx, addr)
			if err != nil {
				_ = store.Close()
				return fmt.Errorf("%w (--addr names another address; port 0 picks a free one)", err)
			}
			srv, err := web.New(web.Config{Store: store, Version: e.build.Version, Log: e.log}, ln)
			if err != nil {
				_ = ln.Close()
				_ = store.Close()
				return err
			}
			e.log.Info("serving facts.db on this machine", "db", redact.Scrub(path), "addr", ln.Addr().String())
			// The address is the one line on stdout, for a script to read;
			// it holds the token, the server's only credential.
			_, _ = fmt.Fprintln(e.stdout, srv.URL())
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:0",
		"loopback `HOST:PORT` to listen on (port 0 picks a free one; only 127.0.0.1, ::1 or localhost)")
	return cmd
}
