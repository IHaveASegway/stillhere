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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/redact"
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
// a usage error; 130 when a command failed after ctx was cancelled, which is
// how cmd/stillhere reports a signal. It prints at most one error line to
// stderr and never calls os.Exit (cmd/stillhere turns off cobra's Windows
// mousetrap, which would).
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
	interrupted := ctx.Err() != nil
	_, _ = fmt.Fprintln(stderr, errorLine(cmd, e.userMessage(err, interrupted)))
	if interrupted {
		// Interrupted: whatever failed, it failed because the run was
		// stopped, which a shell loop must be able to tell apart from "no
		// repository produced facts" (exit 1).
		return exitInterrupted
	}
	return exitCode(err)
}

// exitInterrupted is the exit code of an interrupted run: 128 + SIGINT, the
// shell convention.
const exitInterrupted = 130

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
	all         bool // --all on report, scan, week, explain and who-knows: every row of every table (SR-14)

	log *slog.Logger // stderr, at --log-level; built by setup

	// database is the facts database the command opened, absolute, or ""
	// before it opens one: what the error line names when the database
	// holds no such run, or SQLite fails (userMessage).
	database string
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
		// --version (and -v), often the first command typed after an
		// install, prints the first line of 'stillhere version' (#71).
		Version: versionLine(e.build),
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetFlagErrorFunc(flagError)
	root.SetHelpCommand(helpCommand())

	f := root.PersistentFlags()
	f.StringVar(&e.dbPath, "db", "",
		"facts database `FILE` (default $STILLHERE_HOME/facts.db; STILLHERE_HOME defaults to ~/.stillhere)")
	f.StringVar(&e.policyPath, "policy", "",
		"policy `FILE` for week, scan and policy (default ./stillhere.toml when present, else the packaged policy); a stored run keeps its own")
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
		e.pathsCommand(),
		e.meCommand(),
		e.metricsCommand(),
		e.identitiesCommand(),
		e.labelCommand(),
		e.validateCommand(),
		e.syncCommand(),
		e.mcpCommand(),
		e.serveCommand(),
		e.egressCommand(),
		e.policyCommand(),
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
		ReplaceAttr: logAttr,
	}))
	return nil
}

// need lists what a command uses: report reads facts.db and runs without
// git, and only week and scan load a policy file. A command that reads a
// stored run uses the policy the run recorded (Run.PolicySource), never
// today's. store opens facts.db, which holds the forge replica too, so it
// hands the one store over as both ports: sync writes the replica, scan
// reads it, and the read commands date a run's GitHub numbers with it.
// write says the command writes facts.db (scan, sync): it is created when
// missing, and refused when it or its directory cannot be written, before
// any git work (sqlite.Open). A command that only reads it never creates it
// or its directory (sqlite.OpenExisting), so a mistyped --db is a usage
// error that names the path rather than a new, empty database (#71).
// forge, for sync only, builds the GitHub adapter, and needs store.
type need struct{ git, policy, store, write, forge bool }

// with builds the dependencies a command needs, runs fn with them, and
// releases them, joining a failure to close the store to fn's error.
func (e *env) with(ctx context.Context, n need, fn func(app.Deps) error) (err error) {
	d := app.Deps{Log: e.log, Version: e.build.Version}
	if !n.policy && e.policyPath != "" {
		e.log.Warn("--policy is ignored: only week, scan and policy read a policy file, and a stored run carries its own")
	}
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
		if e.database, err = filepath.Abs(path); err != nil {
			return usagef("--db %s: %v", path, err)
		}
		open := sqlite.OpenExisting
		if n.write {
			open = sqlite.Open
		}
		store, err := open(ctx, path)
		if err != nil {
			return err
		}
		if n.write {
			// Where the run goes, said once, as an absolute path (RB-12):
			// a relative --db names a file in the working directory.
			e.log.Info("facts database", "path", redact.Scrub(e.database))
		}
		d.Store, d.ForgeStore = store, store
		defer func() {
			if cerr := store.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("closing %s: %w", path, cerr))
			}
		}()
	}
	if n.forge {
		forge, err := e.forgeSource(ctx)
		if err != nil {
			return err
		}
		d.Forge = forge
	}
	return fn(d)
}

// defaultMailmapFile is read from the directory the command started in when
// --mailmap is not given.
const defaultMailmapFile = ".mailmap"

// gitRunner builds the git adapter with the organisation mailmap: --mailmap,
// else ./.mailmap when it exists in the directory the command started in
// (docs/spec/git-invocation.md#identity-mailmap-and-bots).
func (e *env) gitRunner(ctx context.Context) (*gitcli.Runner, error) {
	flag, mailmap := "--mailmap", e.mailmapPath
	if mailmap == "" {
		// A file the working directory supplied is named by its path
		// alone (fileLabel).
		flag, mailmap = "", "./"+defaultMailmapFile
		there, err := implicitFile(mailmap, defaultMailmapFile, "--mailmap")
		if err != nil {
			return nil, err
		}
		if !there {
			return gitcli.New(ctx, gitcli.Config{Logger: e.log})
		}
		e.log.Info("using the mailmap in the working directory (--mailmap names another)", "file", mailmap)
	}
	// git reads the file in every repository and silently ignores one it
	// cannot open, so check it here, once, and say which lines it will skip
	// or cut (RB-17).
	if err := readable(flag, mailmap); err != nil {
		return nil, err
	}
	checkMailmap(e.log, fileLabel(flag, mailmap), mailmap)
	return gitcli.New(ctx, gitcli.Config{MailmapFile: mailmap, Logger: e.log})
}

// maxPolicyBytes caps the policy file a command reads. The packaged policy
// written out in full ('stillhere policy') is about 23 KB, and a file
// without end, such as a link to /dev/zero, must not take the memory
// (#71).
const maxPolicyBytes = 1 << 20

// loadPolicy reads exactly one policy: --policy, else ./stillhere.toml when
// present, else the packaged default. A file replaces the packaged policy as
// a whole (docs/spec/classification.md#deny-list).
func (e *env) loadPolicy() (*policy.Policy, error) {
	return e.readPolicy(false)
}

// readPolicy is loadPolicy for week and scan, and for the policy command
// when printing is set. Then an empty ./stillhere.toml counts as absent: it
// is the file that 'stillhere policy > stillhere.toml' asks for, which the
// shell creates, empty, before stillhere starts, and no policy is empty.
// week and scan refuse it, since a policy file that says nothing is a
// mistake the user should see.
func (e *env) readPolicy(printing bool) (*policy.Policy, error) {
	if e.policyPath != "" {
		data, err := readPolicyFile("--policy", e.policyPath)
		if err != nil {
			return nil, err
		}
		// Parse's errors already name the file.
		return policy.Parse(e.policyPath, data)
	}
	shown := "./" + defaultPolicyFile
	there, err := implicitFile(shown, defaultPolicyFile, "--policy")
	if err != nil {
		return nil, err
	}
	if there {
		data, err := readPolicyFile("", shown)
		switch {
		case err != nil:
			return nil, err
		case len(data) > 0:
			e.log.Info("using the policy in the working directory (--policy names another)", "file", shown)
			return policy.Parse(defaultPolicyFile, data)
		case !printing:
			return nil, usagef("%s is empty: write a policy into it ('stillhere policy > %s' starts one), or remove it",
				shown, defaultPolicyFile)
		}
		e.log.Info("the policy in the working directory is empty, as a redirection to it leaves it before stillhere starts: "+
			"printing the packaged policy", "file", shown)
	}
	p, err := policy.Default()
	if err != nil {
		return nil, fmt.Errorf("packaged policy: %w", err)
	}
	return p, nil
}

// readPolicyFile reads a policy file that flag, or the working directory
// (flag ""), named: through readable, so that a FIFO or a device is refused
// rather than opened, and only up to maxPolicyBytes.
func readPolicyFile(flag, path string) ([]byte, error) {
	if err := readable(flag, path); err != nil {
		return nil, err
	}
	label := fileLabel(flag, path)
	f, err := os.Open(path)
	if err != nil {
		return nil, usagef("%s: %v", label, pathCause(err))
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil {
		return nil, usagef("%s: %v", label, pathCause(err))
	}
	if len(data) > maxPolicyBytes {
		return nil, usagef("%s: larger than %d bytes, far more than any policy holds", label, maxPolicyBytes)
	}
	return data, nil
}

// implicitFile reports whether name, a file a command reads from the
// working directory when no flag names one (./stillhere.toml, ./.mailmap),
// is there. The working directory is often a clone's root, so the file may
// be the clone's rather than the user's, and a clone can commit a symbolic
// link: one to /dev/zero would be read without end, and one to a FIFO
// would hang the command (#71). So a link there is refused, and the flag
// that would take it said; a file a flag names may be a link, since the
// user chose it.
func implicitFile(shown, name, flag string) (bool, error) {
	fi, err := os.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, usagef("%s: %v", shown, pathCause(err))
	case fi.Mode()&fs.ModeSymlink != 0:
		return false, usagef("%s is a symbolic link, which stillhere does not follow in the working directory, where a clone "+
			"can put one: pass the file with %s to use it", shown, flag)
	}
	return true, nil
}

// databasePath resolves --db, else $STILLHERE_HOME/facts.db.
func (e *env) databasePath() (string, error) {
	if e.dbPath != "" {
		if err := localPath("--db", e.dbPath); err != nil {
			return "", err
		}
		return e.dbPath, nil
	}
	home, err := stillhereHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "facts.db"), nil
}

// stillhereHome returns $STILLHERE_HOME, which defaults to ~/.stillhere: the
// directory for facts.db and the managed mirrors. A relative one is
// refused (RB-12): it would name another facts database in every directory
// a command runs in, so a report run elsewhere than its scan would find
// none, as the XDG Base Directory specification rules for its own
// variables ("All paths set in these environment variables must be
// absolute").
func stillhereHome() (string, error) {
	if home := os.Getenv("STILLHERE_HOME"); home != "" {
		if err := localPath("STILLHERE_HOME", home); err != nil {
			return "", err
		}
		if !filepath.IsAbs(home) {
			return "", usagef("STILLHERE_HOME %q is a relative path, which names another facts database in every directory "+
				"a command runs in: set it to an absolute path (such as \"$PWD/%s\"), or pass --db", home, home)
		}
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
	return errors.Join(err, e.render(m))
}

// printAnswer renders a read command's answer in the chosen --format, or
// returns the use case's error.
func (e *env) printAnswer(d report.Document, err error) error {
	if err != nil {
		return err
	}
	return e.render(d)
}

// render writes d to stdout in the chosen --format: text and Markdown the
// first rows of each table, or every row with --all; JSON every row.
func (e *env) render(d report.Document) error {
	var opts []report.Option
	if e.all {
		opts = append(opts, report.AllRows())
	}
	switch e.format {
	case formatJSON:
		return report.JSON(e.stdout, d)
	case formatMarkdown:
		return report.Markdown(e.stdout, d, opts...)
	default:
		return report.Text(e.stdout, d, opts...)
	}
}

// addAllFlag defines --all, which render reads.
func (e *env) addAllFlag(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&e.all, "all", false,
		"print every row of every table in text and Markdown (default: the first 10 of each, then how many more); JSON holds every row")
}

// pathsArgs is the argument check of week and scan: at least one PATH, said
// in stillhere's words rather than cobra's (RB-19).
func pathsArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return usagef("%s needs at least one PATH: a repository, or a group directory whose immediate children are "+
			"repositories, such as '%s .' (see '%s --help')", cmd.Name(), cmd.CommandPath(), cmd.CommandPath())
	}
	return nil
}

// timeFlag is a pflag.Value that parses an RFC 3339 time, or a date that
// means midnight UTC, and stores UTC.
type timeFlag struct{ t *time.Time }

func (f timeFlag) String() string {
	if f.t == nil || f.t.IsZero() {
		return ""
	}
	return f.t.Format(time.RFC3339)
}

func (f timeFlag) Set(s string) error {
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			*f.t = t.UTC()
			return nil
		}
	}
	return errors.New("want an RFC 3339 time, such as 2026-10-07T00:00:00Z, or a date, such as 2026-10-07, which means midnight UTC")
}

func (f timeFlag) Type() string { return "time" }

// addNowFlag defines --now; read it with nowOr. Its help says what the
// instant decides: every window is measured back from it (durable@1's
// cohort ends 90 days before it), and the commit each repository is read
// at, the chain-order rule of collect.SnapshotAt (ADR 0012).
func addNowFlag(cmd *cobra.Command, t *time.Time) {
	cmd.Flags().Var(timeFlag{t}, "now",
		"the instant the run describes, an RFC 3339 time or a date for midnight UTC (default the wall clock, UTC): "+
			"every window is measured back from it, and each repository is read at the first commit committed before it, walking "+
			"its default branch's first-parent chain back from the head (a head-after-now caveat when the head is "+
			"newer; a repository with none is left out); pass it for reproducible output")
}

// nowOr returns --now when it was given, else the wall clock Execute read
// once: the one instant the command's windows, snapshots and numbers read.
// Other code reads the clock only for what no number depends on: timings
// and log durations, the store's leases and finish times, GitHub's rate
// pacing and mirror cleanup (docs/spec/codebase.md#determinism).
func (e *env) nowOr(cmd *cobra.Command, t time.Time) time.Time {
	if cmd.Flags().Changed("now") {
		return t
	}
	return e.now
}

// addRunFlag defines --run; check it with checkRun.
func addRunFlag(cmd *cobra.Command, id *int64) {
	cmd.Flags().Int64Var(id, "run", 0, "run `ID` to read (default the latest complete or partial scan)")
}

func checkRun(id int64) error {
	if id < 0 {
		return usagef("invalid --run %d: want a run id of 1 or more", id)
	}
	return nil
}

// addExcludeFlag defines --exclude-repo; check it with checkGlobs.
func addExcludeFlag(cmd *cobra.Command, globs *[]string) {
	cmd.Flags().StringArrayVar(globs, "exclude-repo", nil,
		"leave out repositories whose name matches `GLOB` (path.Match syntax, such as 'legacy-*'); repeatable; the report lists them")
}

// checkGlobs rejects a malformed --exclude-repo pattern before any git
// process starts.
func checkGlobs(globs []string) error {
	for _, g := range globs {
		if _, err := path.Match(g, ""); err != nil {
			return usagef("invalid --exclude-repo %q: %v", g, err)
		}
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

// readable checks that path names a regular file stillhere can open. It
// checks the type before opening: opening a FIFO for reading blocks until a
// writer appears, and no context can cancel that. flag is the flag that
// named the file, or "" for one the working directory supplied.
func readable(flag, path string) error {
	label := fileLabel(flag, path)
	info, err := os.Stat(path)
	if err != nil {
		return usagef("%s: %v", label, pathCause(err))
	}
	if !info.Mode().IsRegular() {
		return usagef("%s: not a regular file", label)
	}
	f, err := os.Open(path)
	if err != nil {
		return usagef("%s: %v", label, pathCause(err))
	}
	return f.Close()
}

// fileLabel is how a message names a file a command reads: the flag that
// named it and its path ("--policy team.toml"), or, for a file the working
// directory supplied (flag ""), its path alone ("./stillhere.toml").
func fileLabel(flag, path string) string {
	if flag == "" {
		return path
	}
	return flag + " " + path
}

// pathCause is what went wrong with a file, without the operation and the
// path that an *fs.PathError adds ("stat team.toml: no such file or
// directory"): the message names the file already (fileLabel).
func pathCause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
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
		return "", usagef("%s: %v", fileLabel(flag, path), err)
	}
	return abs, nil
}

// urlScheme matches the start of a URL, such as https:// or ssh://. Its
// scheme has two characters or more, since one letter is a Windows drive.
var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+://`)

// localPath refuses value, given where a local file or directory belongs
// (what names it: a flag, or STILLHERE_HOME), when it is a URL or holds a
// credential, which redact.Scrub would remove. Either is a remote pasted
// into the wrong place. Taken as a path, it would make a directory named
// after the token, and a scan of that directory would store the name in
// facts.db and print it in every report (#71). The refusal quotes the
// value scrubbed, before anything is created or recorded.
func localPath(what, value string) error {
	if urlScheme.MatchString(value) || redact.Scrub(value) != value {
		return usagef("%s %q: want a local path, not a URL or a path that holds a credential", what, redact.Scrub(value))
	}
	return nil
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

// logAttr is the log handler's ReplaceAttr. It drops the timestamp, since
// stderr is read live and output without it is reproducible, and scrubs
// every other value of credentials (redact.Scrub). A log line can carry a
// path or a URL the user typed (--db, --mirrors, a PATH, a mirror under
// them), and a token in one must not reach stderr (#71). The call
// sites scrub what they know to be such a value; this is the one place
// every line passes, so a line that forgets cannot leak (defence in depth:
// a single egress filter, as a log pipeline's redaction stage is).
// redact.Scrub changes nothing but a credential, so a clean line is printed
// byte for byte.
func logAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	switch v := a.Value; v.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(redact.Scrub(v.String()))
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			a.Value = slog.StringValue(redact.Scrub(x.Error()))
		case []string:
			scrubbed := make([]string, len(x))
			for i, s := range x {
				scrubbed[i] = redact.Scrub(s)
			}
			a.Value = slog.AnyValue(scrubbed)
		case fmt.Stringer:
			a.Value = slog.StringValue(redact.Scrub(x.String()))
		}
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

// helpCommand replaces cobra's help command, which prints the root's help
// and exits 0 for an unknown topic (the root accepts arbitrary arguments,
// so cobra finds the root for "help nope") and calls os.Exit when printing
// fails.
func helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Long:  "help prints the help of any command, such as: stillhere help sync github",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			target, rest, err := c.Root().Find(args)
			if err != nil || len(rest) > 0 {
				return usagef("unknown help topic %q (see '%s --help')",
					strings.Join(args, " "), c.Root().CommandPath())
			}
			target.InitDefaultHelpFlag() // so the help lists --help, as cobra's does
			return target.Help()
		},
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

// userMessage returns what the error line says about err: its whole chain, as
// the operations that failed wrapped it, except for three kinds of failure
// whose chain is internal detail, which goes to the debug log instead
// (#71), as kubectl maps an API error to its standard message:
//
//   - a command a signal stopped: "interrupted", whatever failed on the way;
//   - a facts database that holds no such run or person (domain.ErrNotFound):
//     the database's path, then the use case's own sentence, which says what
//     to do, without the store operations below it;
//   - a SQLite failure the user can act on, such as a full disk or a
//     damaged file: the database's path, then sqlite.Advice.
//
// The exit code is chosen apart from the words (exitCode).
func (e *env) userMessage(err error, interrupted bool) string {
	short := ""
	switch {
	case interrupted:
		short = "interrupted"
	case e.database == "":
	case errors.Is(err, domain.ErrNotFound):
		short = e.database + ": " + outermost(err)
	default:
		if advice := sqlite.Advice(err); advice != "" {
			short = e.database + ": " + advice
		}
	}
	if short == "" {
		return err.Error()
	}
	e.log.Debug("the error in full", "error", err)
	return short
}

// outermost returns err's own words without those of the error it wraps:
// the context the outermost layer added, which a use case writes for the
// user, as Rust's anyhow prints an error's outermost context and leaves
// the chain to its alternate form. An error that wraps nothing, or not at
// the end of its message, is returned whole.
func outermost(err error) string {
	msg := err.Error()
	inner := errors.Unwrap(err)
	if inner == nil {
		return msg
	}
	if head, ok := strings.CutSuffix(msg, ": "+inner.Error()); ok && head != "" {
		return head
	}
	return msg
}

// errorLine is the one line printed for a failed command. Each line of the
// message, such as each error errors.Join collected, becomes one clause,
// and the clauses are joined with "; ". An error can quote an argument, and
// an argument can be a URL with a token in it, so every clause is scrubbed.
func errorLine(cmd *cobra.Command, msg string) string {
	var clauses []string
	for _, line := range strings.Split(msg, "\n") {
		if line = terminalSafe(redact.Scrub(line)); line != "" {
			clauses = append(clauses, line)
		}
	}
	return cmd.CommandPath() + ": " + strings.Join(clauses, "; ")
}

// terminalSafe returns s as one line a terminal prints literally: control
// and format characters, which include escape sequences and bidirectional
// overrides that a path or an argument may carry, are shown escaped the way
// Go quotes them (\x1b, ‮), and runs of whitespace become one space.
func terminalSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// exitCode maps an error to the exit code (docs/spec/report-json.md#exit-codes),
// from typed errors only, never from message text.
func exitCode(err error) int {
	var usage *domain.UsageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &usage):
		return 2
	default:
		// Includes domain.ErrNoFacts: no repository produced facts.
		return 1
	}
}
