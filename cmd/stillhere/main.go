// Command stillhere is change analytics for git and GitHub: what landed, what
// lasted, who knows it. All behaviour lives in internal/cli; main only ties
// the process to it: signals, arguments, streams and the exit code.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/IHaveASegway/stillhere/internal/cli"
)

// Set by release builds with -ldflags "-X main.version=... -X main.commit=...
// -X main.date=...". Empty values fall back to the build information the Go
// toolchain embeds.
var (
	version string
	commit  string
	date    string
)

func main() {
	// SIGINT or SIGTERM cancels ctx, which stops every running git process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		// After the first signal, restore the default handlers, so a second
		// Ctrl-C ends the process at once.
		<-ctx.Done()
		stop()
	}()
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr,
		cli.Build{Version: version, Commit: commit, Date: date})
	stop()
	os.Exit(code)
}
