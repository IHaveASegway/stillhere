// Package cli is stillhere's command line: the cobra commands and flags, the
// exit codes, and the composition root, the one place where adapters are
// built and handed to the use cases in internal/app (Mark Seemann,
// "Composition Root"). Commands parse and validate input at this boundary
// and pass typed requests inward, so a use case never sees a flag.
//
// Each command lives in its own file. Shared plumbing lives here: the global
// flags, building dependencies on demand, rendering, and mapping errors to
// exit codes.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// The report formats --format accepts.
const (
	formatText     = "text"
	formatMarkdown = "md"
	formatJSON     = "json"
)

// defaultPolicyFile is read from the directory the command started in when
// --policy is not given.
const defaultPolicyFile = "stillhere.toml"

// Execute runs the command line with args, which exclude the program name,
// and returns the exit code (docs/spec/report-json.md#exit-codes): 0 on
// success; 1 when the command failed or no repository produced facts; 2 for
// a usage error, including a command whose use case is not built yet. It
// prints at most one error line to stderr and never calls os.Exit.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, build Build) int {
	e := &env{
		stdout: stdout,
		stderr: stderr,
		build:  build.resolved(),
		now:    time.Now().UTC().Truncate(time.Second),
		log:    slog.New(slog.DiscardHandler),
	}
	if args == nil {
		args = []string{} // cobra reads os.Args for nil; Execute uses only its inputs
	}
	root := e.rootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if cmd == nil {
		cmd = root
	}
	_, _ = fmt.Fprintln(stderr, errorLine(cmd, err))
	return exitCode(err)
}

// env is one invocation's composition root: the streams, the global flags,
// the clock read once, and the logger. It builds adapters on demand, so a
// command never builds, or fails on, an adapter it does not use.
type env struct {
	stdout, stderr io.Writer
	build          Build
	now            time.Time // the wall clock in UTC, read once: the default --now

	// Global flags.
	dbPath      string
	policyPath  string
	mailmapPath string
	logLevel    string
	format      string

	log *slog.Logger // stderr, at --log-level; built by setup
}

func (e *env) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "stillhere",
		Short: "Change analytics for git and GitHub: what landed, what lasted, who knows it",
		Long: `stillhere answers what landed, what lasted and who knows the code, across many
repositories, with every number traceable to the commits behind it. Analysis
is offline and deterministic; only "sync github" touches the network.

stillhere measures specific properties of code changes: how long they lasted,
how much they simplified, how much review they got. It does not measure a
person's value, and it must not be the basis for performance or pay decisions.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              cobra.ArbitraryArgs,
		RunE:              subcommandRequired,
		PersistentPreRunE: func(*cobra.Command, []string) error { return e.setup() },
	}
	root.SetFlagErrorFunc(flagError)

	f := root.PersistentFlags()
	f.StringVar(&e.dbPath, "db", "",
		"facts database `FILE` (default $STILLHERE_HOME/facts.db; STILLHERE_HOME defaults to ~/.stillhere)")
	f.StringVar(&e.policyPath, "policy", "",
		"policy `FILE` (default ./stillhere.toml when present, else the packaged policy)")
	f.StringVar(&e.mailmapPath, "mailmap", "",
		"organisation mailmap `FILE` passed to git (default ./.mailmap when present)")
	f.StringVar(&e.logLevel, "log-level", "info", "stderr log level: debug, info, warn or error")
	f.StringVar(&e.format, "format", formatText, "report format: text, md or json")

	root.AddCommand(
		e.weekCommand(),
		e.scanCommand(),
		e.reportCommand(),
		e.explainCommand(),
		e.whoKnowsCommand(),
		e.meCommand(),
		e.metricsCommand(),
		e.validateCommand(),
		e.syncCommand(),
		e.mcpCommand(),
		e.versionCommand(),
	)
	return root
}

// setup validates the global flags and builds the logger. It runs before
// every command.
func (e *env) setup() error {
	switch e.format {
	case formatText, formatMarkdown, formatJSON:
	default:
		return usagef("invalid --format %q: want text, md or json", e.format)
	}
	level, err := parseLevel(e.logLevel)
	if err != nil {
		return err
	}
	e.log = slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: withoutTime,
	}))
	return nil
}

// need lists what a command uses: report reads facts.db and runs without
// git, and sync needs no policy.
type need struct{ git, policy, store bool }

// with builds the dependencies a command needs, runs fn with them, and
// releases them, joining a failure to close the store to fn's error.
func (e *env) with(ctx context.Context, n need, fn func(app.Deps) error) (err error) {
	d := app.Deps{Log: e.log, Version: e.build.Version}
	if n.git {
		git, err := e.gitRunner(ctx)
		if err != nil {
			return err
		}
		d.Git = git
	}
	if n.policy {
		p, err := e.loadPolicy()
		if err != nil {
			return err
		}
		d.Policy = p
	}
	if n.store {
		path, err := e.databasePath()
		if err != nil {
			return err
		}
		store, err := sqlite.Open(ctx, path)
		if err != nil {
			return err
		}
		d.Store = store
		defer func() {
			if cerr := store.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("closing %s: %w", path, cerr))
			}
		}()
	}
	return fn(d)
}

// gitRunner builds the git adapter with the organisation mailmap: --mailmap,
// else ./.mailmap when it exists in the directory the command started in
// (docs/spec/git-invocation.md#identity-mailmap-and-bots).
func (e *env) gitRunner(ctx context.Context) (*gitcli.Runner, error) {
	mailmap := e.mailmapPath
	if mailmap != "" {
		if err := readable("--mailmap", mailmap); err != nil {
			return nil, err
		}
	} else {
		switch _, err := os.Stat(".mailmap"); {
		case err == nil:
			mailmap = ".mailmap"
		case !errors.Is(err, fs.ErrNotExist):
			return nil, usagef("./.mailmap: %v", err)
		}
	}
	return gitcli.New(ctx, gitcli.Config{MailmapFile: mailmap, Logger: e.log})
}

// loadPolicy reads exactly one policy: --policy, else ./stillhere.toml when
// present, else the packaged default. A file replaces the packaged policy as
// a whole (docs/spec/classification.md#deny-list).
func (e *env) loadPolicy() (*policy.Policy, error) {
	name := e.policyPath
	if name == "" {
		name = defaultPolicyFile
	}
	data, err := os.ReadFile(name)
	switch {
	case err == nil:
		p, err := policy.Parse(name, data)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", name, err)
		}
		return p, nil
	case e.policyPath == "" && errors.Is(err, fs.ErrNotExist):
		p, err := policy.Default()
		if err != nil {
			return nil, fmt.Errorf("packaged policy: %w", err)
		}
		return p, nil
	default:
		return nil, usagef("cannot read the policy file: %v", err)
	}
}

// databasePath resolves --db, else $STILLHERE_HOME/facts.db.
func (e *env) databasePath() (string, error) {
	if e.dbPath != "" {
		return e.dbPath, nil
	}
	home, err := stillhereHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "facts.db"), nil
}

// stillhereHome returns $STILLHERE_HOME, which defaults to ~/.stillhere: the
// directory for facts.db and the managed mirrors.
func stillhereHome() (string, error) {
	if home := os.Getenv("STILLHERE_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", usagef("cannot find the home directory (%v): set STILLHERE_HOME or --db", err)
	}
	return filepath.Join(home, ".stillhere"), nil
}

// printReport renders m in the chosen --format when the use case produced
// one, and then returns err, so a run in which no repository produced facts
// still prints each repository's error before it exits 1.
func (e *env) printReport(m *report.Model, err error) error {
	if m == nil {
		return err
	}
	var rerr error
	switch e.format {
	case formatJSON:
		rerr = report.JSON(e.stdout, m)
	case formatMarkdown:
		rerr = report.Markdown(e.stdout, m)
	default:
		rerr = report.Text(e.stdout, m)
	}
	return errors.Join(err, rerr)
}

// printJSON writes a read-side result as indented JSON.
// TODO(#11): text and Markdown renderers for the read-side results; until
// then they print as JSON whatever --format says.
func (e *env) printJSON(v any) error {
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// timeFlag is a pflag.Value that parses RFC 3339 and stores UTC.
type timeFlag struct{ t *time.Time }

func (f timeFlag) String() string {
	if f.t == nil || f.t.IsZero() {
		return ""
	}
	return f.t.Format(time.RFC3339)
}

func (f timeFlag) Set(s string) error {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return errors.New("want an RFC 3339 time, such as 2026-10-07T00:00:00Z")
	}
	*f.t = t.UTC()
	return nil
}

func (f timeFlag) Type() string { return "time" }

// addNowFlag defines --now; read it with nowOr.
func addNowFlag(cmd *cobra.Command, t *time.Time) {
	cmd.Flags().Var(timeFlag{t}, "now",
		"end of the window, RFC 3339 (default the wall clock, UTC); pass it for reproducible output")
}

// nowOr returns --now when it was given, else the wall clock Execute read
// once. No other code reads the clock (docs/spec/codebase.md#determinism).
func (e *env) nowOr(cmd *cobra.Command, t time.Time) time.Time {
	if cmd.Flags().Changed("now") {
		return t
	}
	return e.now
}

// addRunFlag defines --run; check it with checkRun.
func addRunFlag(cmd *cobra.Command, id *int64) {
	cmd.Flags().Int64Var(id, "run", 0, "run `ID` to read (default the latest complete run)")
}

func checkRun(id int64) error {
	if id < 0 {
		return usagef("invalid --run %d: want a run id of 1 or more", id)
	}
	return nil
}

// addRankFlag defines --rank; parse it with parseMetricIDs.
func addRankFlag(cmd *cobra.Command, ids *[]string) {
	cmd.Flags().StringSliceVar(ids, "rank", nil,
		"rank people on `METRIC`, such as durable@1; repeat it or separate metrics with commas")
}

// parseMetricIDs parses metric ids given on the command line. A malformed id
// is a *domain.UsageError.
func parseMetricIDs(ids []string) ([]domain.MetricID, error) {
	out := make([]domain.MetricID, 0, len(ids))
	for _, s := range ids {
		id, err := domain.ParseMetricID(s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// readable checks that path names a regular file stillhere can open.
func readable(flag, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return usagef("%s: %v", flag, err)
	}
	info, err := f.Stat()
	_ = f.Close()
	switch {
	case err != nil:
		return usagef("%s: %v", flag, err)
	case !info.Mode().IsRegular():
		return usagef("%s %s: not a regular file", flag, path)
	}
	return nil
}

// absFile checks that path is readable and returns it made absolute: git
// runs with -C in each repository, which would otherwise resolve a relative
// path there.
func absFile(flag, path string) (string, error) {
	if err := readable(flag, path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", usagef("%s %s: %v", flag, path, err)
	}
	return abs, nil
}

// canonicalEmail normalises an email given on the command line to the
// canonical key: trimmed and lowercased.
func canonicalEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, usagef("invalid --log-level %q: want debug, info, warn or error", s)
}

// withoutTime drops the timestamp from log lines: stderr is read live, and
// output without it is reproducible.
func withoutTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// usagef returns a *domain.UsageError, which exits 2.
func usagef(format string, args ...any) error {
	return &domain.UsageError{Msg: fmt.Sprintf(format, args...)}
}

// flagError makes every flag parsing error a usage error. cobra would
// otherwise exit 1 for it.
func flagError(cmd *cobra.Command, err error) error {
	if errors.Is(err, pflag.ErrHelp) {
		return err
	}
	return usagef("%v (see '%s --help')", err, cmd.CommandPath())
}

// usageArgs makes a positional-argument check fail with a usage error.
func usageArgs(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := check(cmd, args); err != nil {
			return usagef("%v (see '%s --help')", err, cmd.CommandPath())
		}
		return nil
	}
}

// subcommandRequired runs when a command that only groups subcommands is
// called on its own: without arguments it prints help, and with an unknown
// subcommand it fails with a usage error, where cobra would exit 1.
func subcommandRequired(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if cmd.SuggestionsMinimumDistance <= 0 {
		// cobra sets this default only on the code path this function replaces.
		cmd.SuggestionsMinimumDistance = 2
	}
	if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
		msg += "; did you mean " + strings.Join(suggestions, " or ") + "?"
	}
	return usagef("%s (see '%s --help')", msg, cmd.CommandPath())
}

// errorLine is the one line printed for a failed command.
func errorLine(cmd *cobra.Command, err error) string {
	return cmd.CommandPath() + ": " + strings.Join(strings.Fields(err.Error()), " ")
}

// exitCode maps an error to the exit code (docs/spec/report-json.md#exit-codes),
// from typed errors only, never from message text.
func exitCode(err error) int {
	var usage *domain.UsageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage), errors.Is(err, domain.ErrNotImplemented):
		// A command whose use case is not built yet is a usage error: the
		// user cannot get a result from it today. The second case goes with
		// domain.ErrNotImplemented when the last stub does (#13).
		return 2
	default:
		// Includes domain.ErrNoFacts: no repository produced facts.
		return 1
	}
}
