// Command stillhere is change analytics for git and GitHub: what landed, what
// lasted, who knows it. All behaviour lives in internal/cli; main only ties
// the process to it: signals, arguments, streams and the exit code.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
	// cobra would otherwise print a notice and call os.Exit(1) when Windows
	// Explorer starts stillhere, bypassing cli.Execute's exit codes.
	cobra.MousetrapHelpText = ""

	// SIGINT, SIGTERM or SIGHUP cancels ctx, which stops every running git
	// process. git runs in process groups of its own, so the terminal's
	// hangup never reaches it: without SIGHUP here, closing the terminal
	// would leave it running. Under nohup SIGHUP stays ignored.
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, signals...)
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan os.Signal, 1)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case sig := <-caught:
			// Restore the default handlers, so a second signal ends the
			// process at once.
			signal.Reset(signals...)
			received <- sig
			cancel()
		case <-ctx.Done():
		}
	}()

	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr,
		cli.Build{Version: version, Commit: commit, Date: date})
	cancel()
	<-watched
	select {
	case sig := <-received:
		reraise(sig)
	default:
	}
	os.Exit(code)
}

// reraise ends the process by sig, now that git has been stopped, so the
// shell or supervisor that sent it sees stillhere die by that signal: a
// shell stops a loop only then, not on an ordinary exit status (Martin
// Cracauer, "Proper handling of SIGINT/SIGQUIT", 2003). It returns only
// where a process cannot signal itself, such as on Windows; main then exits
// with Execute's code (130 for an interrupted run).
func reraise(sig os.Signal) {
	signal.Reset(sig)
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		return
	}
	if self.Signal(sig) != nil {
		return
	}
	// The Go runtime delivers the signal on another thread; wait for it.
	time.Sleep(time.Second)
}
