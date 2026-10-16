package cli

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// maxLabelsBytes caps the labels file label and validate read: 16 MiB, some
// 300,000 rows, where a lead's sample is 100.
const maxLabelsBytes = 16 << 20

// defaultLabelsFile is the labels file beside the facts database when no
// --labels names one: outside every repository, as labels judge colleagues'
// work.
const defaultLabelsFile = "labels.csv"

// labelKeys is the interactive session's prompt.
const labelKeys = "m meaningful · r routine · n noise · s skip · d diff · ? rubric · q quit"

// labelRubric is docs/validation.md's rubric, as short as a prompt allows.
var labelRubric = []string{
	"meaningful: a change a lead would want to know shipped: a feature, a real fix, a substantive",
	"            refactor, tests that protect behaviour",
	"routine:    necessary but unremarkable: dependency or version bumps, small configuration or",
	"            documentation tweaks, mechanical renames",
	"noise:      should not count at all: formatting, generated code, a revert of one's own mistake",
	"            within days, a merge with nothing of its own",
	"skip:       you cannot tell; the change is not asked about again",
	"Judge the change, not its author, and not what you expect stillhere to make of it.",
}

func (e *env) labelCommand() *cobra.Command {
	var (
		run      int64
		size     int
		seed     uint64
		labels   string
		labeller string
		sheet    bool
		plan     bool
	)
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Label a sample of a run's changes meaningful, routine or noise, for validate",
		Long: `label draws a sample of a stored run's changes and asks, one change at a time,
whether each is meaningful, routine or noise, with single keys:

  ` + labelKeys + `

Each answer is appended to the labels file at once, so quitting loses nothing,
and running label again resumes where it stopped (after a newer scan, --run ID
keeps the run the labels began on). By default it asks until each ranked
metric has 50 labels meaningful or routine among the changes it scores, the
bar validate checks: about 100 changes, more for each one answered noise or
skip; --n N asks about exactly N, and a larger --n extends a smaller one.
'stillhere validate' then scores every ranked metric against the labels.
Piped input answers with the first letter of each line, d and ? included.

The sample comes from the changes a ranked metric scores, people's only, from
each window the metrics read (durable@1: 90 to 365 days ago; simplifying@1
and reviews_given@1: the last 90 days), in proportion to each repository's
small, medium and large changes. It is blinded: no metric value, rank or author
is shown. docs/validation.md has the rubric and worked examples.

--labeller NAME writes NAME in each row and resumes only NAME's rows, so two
leads can label the same sample into one file. --sheet writes the sample as a
CSV instead, to label in a spreadsheet; validate reads the filled-in sheet as
it is. --plan prints the sample and its strata.

The labels judge colleagues' work: the file is kept beside the facts database
($STILLHERE_HOME/labels.csv), outside every repository, unless --labels names
another. Keep a sheet there too, out of every repository.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			if sheet && plan {
				return usagef("--sheet and --plan each print the sample: give one")
			}
			if cmd.Flags().Changed("n") && (size < 1 || size > app.MaxLabelSample) {
				return usagef("--n %d: want 1 to %d", size, app.MaxLabelSample)
			}
			interactive := !sheet && !plan
			if interactive && e.format != formatText {
				return usagef("label asks about changes as text: --format applies to --plan")
			}
			flag, path, err := e.labelsPath(labels)
			if err != nil {
				return err
			}
			e.warnInRepository(flag, path)
			data, err := readLabelsFile(flag, path, true) // a new file is made at the session's start
			if err != nil {
				return err
			}
			req := app.LabelRequest{RunID: run, N: size, Seed: seed, Labels: data, LabelsName: path, Labeller: labeller}
			return e.with(cmd.Context(), need{store: true, git: interactive}, func(d app.Deps) error {
				sample, err := app.Label(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				switch {
				case sheet:
					if err := report.LabelHeader(e.stderr, sample); err != nil {
						return err
					}
					if sample.Pool == 0 {
						return errNothingToLabel(sample)
					}
					return report.Sheet(e.stdout, sample)
				case plan:
					if err := e.render(sample); err != nil {
						return err
					}
					if sample.Pool == 0 {
						return errNothingToLabel(sample)
					}
					return nil
				}
				session := &labelSession{e: e, d: d, req: req, flag: flag, path: path, in: cmd.InOrStdin()}
				return session.run(cmd.Context(), sample)
			})
		},
	}
	cmd.Flags().IntVar(&size, "n", 0, "the sample's size (default: until each ranked metric has the 50 labels its bar needs)")
	cmd.Flags().Uint64Var(&seed, "seed", 1, "the sample's seed: the same run, --n, --seed and labels give the same sample")
	cmd.Flags().StringVar(&labels, "labels", "", "labels CSV `FILE` to append to (default labels.csv beside the facts database)")
	cmd.Flags().StringVar(&labeller, "labeller", "", "a `NAME` for the labeller: written in each row, and only its rows are resumed")
	cmd.Flags().BoolVar(&sheet, "sheet", false, "write the sample as a CSV to label in a spreadsheet, and label nothing here")
	cmd.Flags().BoolVar(&plan, "plan", false, "print the sample and its strata in --format, and label nothing")
	addRunFlag(cmd, &run)
	return cmd
}

// errNothingToLabel is label's error when the run holds no change to label:
// exit 1, as a read command's that cannot give an answer, with the reason
// the caveats printed above it give.
func errNothingToLabel(s *report.LabelSample) error {
	return fmt.Errorf("run %d holds no change to label (the caveats say why)", s.Provenance.RunID)
}

// labelsPath is the labels file a command reads: --labels, else labels.csv
// beside the facts database. flag names it in messages.
func (e *env) labelsPath(labels string) (flag, path string, err error) {
	if labels != "" {
		return "--labels", labels, nil
	}
	db, err := e.databasePath()
	if err != nil {
		return "", "", err
	}
	return "", filepath.Join(filepath.Dir(db), defaultLabelsFile), nil
}

// warnInRepository warns when a labels file is inside a git work tree,
// where 'git add .' would commit the judgments it holds: the labels are
// kept outside every repository by default (ADR 0014).
func (e *env) warnInRepository(flag, path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	for dir := filepath.Dir(abs); ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			e.log.Warn("the labels file is inside a git work tree, where 'git add' would commit its judgments of colleagues' "+
				"work: keep it outside every repository, as labels.csv beside the facts database is", "file", terminalSafe(fileLabel(flag, path)),
				"work_tree", terminalSafe(dir))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// readLabelsFile reads a labels file through readable, so that a FIFO or a
// device is refused rather than opened, and only up to maxLabelsBytes. A
// missing file is nil when optional: label's, which its session makes;
// validate's must exist.
func readLabelsFile(flag, path string, optional bool) ([]byte, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		switch {
		case optional:
			return nil, nil
		case flag == "":
			return nil, usagef("%s: no labels yet: 'stillhere label' writes them there, or --labels FILE names a labels file", path)
		}
	}
	if err := readable(flag, path); err != nil {
		return nil, err
	}
	label := fileLabel(flag, path)
	f, err := os.Open(path)
	if err != nil {
		return nil, usagef("%s: %v", label, pathCause(err))
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxLabelsBytes+1))
	if err != nil {
		return nil, usagef("%s: %v", label, pathCause(err))
	}
	if len(data) > maxLabelsBytes {
		return nil, usagef("%s: larger than %d bytes, far more than any labels file holds", label, maxLabelsBytes)
	}
	return data, nil
}

// labelSession is one interactive session of stillhere label: it asks about
// each sampled change its labeller has not labelled, in the sample's order,
// appends each answer to the labels file at once, and, when the sample was
// not given a size, draws again after the last one, since a noise or skip
// answer makes a window's part of the sample longer.
type labelSession struct {
	e          *env
	d          app.Deps
	req        app.LabelRequest
	flag, path string
	in         io.Reader

	out           *labelScreen
	keys          *keyReader
	w             *labelsWriter
	done, skipped int
}

// run asks about sample's changes, and those a fresh draw adds, until none
// is left or the labeller quits.
func (ls *labelSession) run(ctx context.Context, sample *report.LabelSample) error {
	ls.req.RunID = sample.Provenance.RunID // the run the session began on, even if a scan stores a newer one meanwhile
	ls.out = &labelScreen{w: ls.e.stdout}
	ls.out.header(sample)
	if sample.Pool == 0 {
		if ls.out.err != nil {
			return ls.out.err
		}
		return errNothingToLabel(sample)
	}
	defer func() {
		if ls.w != nil {
			_ = ls.w.f.Close()
		}
	}()
	asked := map[string]bool{}
	for {
		var todo []int
		for i, c := range sample.Changes {
			if c.Label == "" && !asked[c.Change] {
				todo = append(todo, i)
			}
		}
		if len(todo) == 0 {
			return ls.finish(sample)
		}
		if ls.w == nil {
			// The file is opened before the first card, so a labels file
			// that cannot be written is said before any answer is lost.
			w, err := openLabels(ls.path, ls.req.Labels)
			if err != nil {
				return err
			}
			ls.w = w
			ls.out.say("\nAnswers go to %s.\n", terminalSafe(ls.path))
			ls.out.say("Blinded: no metric value, rank or author is shown. Judge the change; press ? for the rubric.\n")
			ls.keys = newKeyReader(ctx, ls.in)
		}
		for _, i := range todo {
			asked[sample.Changes[i].Change] = true
			quit, err := ls.ask(ctx, &report.LabelCard{Index: i + 1, Of: len(sample.Changes), Change: sample.Changes[i]})
			if err != nil {
				return err
			}
			if quit {
				return ls.finish(ls.redraw(ctx, sample))
			}
		}
		if ls.req.N > 0 {
			return ls.finish(ls.redraw(ctx, sample))
		}
		// Noise and skip answers lengthen a window's part: draw again.
		sample = ls.redraw(ctx, sample)
	}
}

// ask shows one card and reads keys until it has an answer, which it
// appends to the labels file. quit is true when the labeller quit.
func (ls *labelSession) ask(ctx context.Context, card *report.LabelCard) (quit bool, err error) {
	out := ls.out
	out.say("\n")
	out.card(card)
	value := ""
	for value == "" {
		if out.err != nil {
			return false, out.err
		}
		out.say("%s > ", labelKeys)
		key, err := ls.keys.next(ctx)
		if err != nil {
			out.say("\n")
			if errors.Is(err, io.EOF) {
				return true, nil
			}
			return false, err
		}
		if '!' <= key && key <= '~' {
			out.say("%c\n", key)
		} else {
			out.say("\n")
		}
		switch key {
		case 'm':
			value = "meaningful"
		case 'r':
			value = "routine"
		case 'n':
			value = "noise"
		case 's':
			value = "skip"
		case 'd':
			diff, err := app.Diff(ctx, ls.d, app.DiffRequest{RunID: ls.req.RunID, Repo: card.Change.RepoKey, Change: card.Change.Change})
			if err != nil {
				out.say("  no diff: %s\n", terminalSafe(ls.e.userMessage(err, false)))
				continue
			}
			card.Patch, card.Truncated = diff.Patch, diff.Truncated
			if card.Patch == "" {
				card.Patch = "(no file with a role changed)"
			}
			out.card(card)
		case '?':
			for _, l := range labelRubric {
				out.say("  %s\n", l)
			}
		case 'q', 3, 4: // q, and ctrl-c and ctrl-d in raw mode
			return true, nil
		default:
			out.say("  not a key here: press m, r, n, s, d, ? or q\n")
		}
	}
	if err := ls.w.add(card.Change.Change, value, ls.req.Labeller); err != nil {
		return false, err
	}
	if value == "skip" {
		ls.skipped++
	} else {
		ls.done++
	}
	return false, nil
}

// redraw is the sample drawn again over the labels file as it now stands,
// for the labels toward the bar and the changes a noise or skip answer
// added; the sample as it was when the draw fails, such as after an
// interrupt.
func (ls *labelSession) redraw(ctx context.Context, sample *report.LabelSample) *report.LabelSample {
	data, err := readLabelsFile(ls.flag, ls.path, true)
	if err != nil {
		return sample
	}
	req := ls.req
	req.Labels = data
	next, err := app.Label(ctx, ls.d, req)
	if err != nil {
		return sample
	}
	return next
}

// finish closes the labels file and says what the session did, how far
// each metric is toward its bar, and what to run next.
func (ls *labelSession) finish(sample *report.LabelSample) error {
	out := ls.out
	what := fmt.Sprintf("Labelled %d this session", ls.done)
	if ls.skipped > 0 {
		what += fmt.Sprintf(", skipped %d", ls.skipped)
	}
	labelled := 0
	for _, c := range sample.Changes {
		if c.Label != "" {
			labelled++
		}
	}
	out.say("\n%s; %d of %d sampled have an answer.\n", what, labelled, len(sample.Changes))
	if t := sample.ProgressText(); t != "" {
		out.say("%s\n", t)
	}
	validate := "stillhere validate"
	if ls.flag != "" {
		validate += " --labels " + terminalSafe(ls.path)
	}
	if ls.req.Labeller != "" {
		validate += " --labeller " + terminalSafe(ls.req.Labeller)
	}
	switch {
	case sample.Ready():
		out.say("Next: %s\n", validate)
	case labelled < len(sample.Changes):
		out.say("Run stillhere label again to continue; %s shows the AUCs so far.\n", validate)
	case len(sample.Changes) < sample.Pool:
		out.say("A larger --n draws more changes to label; %s shows the AUCs so far.\n", validate)
	default:
		out.say("The run holds no more changes to label; %s shows the AUCs so far.\n", validate)
	}
	if ls.w != nil {
		err := ls.w.f.Close()
		ls.w = nil
		if err != nil {
			return err
		}
	}
	return out.err
}

// labelScreen writes the session to the terminal and keeps the first write
// error, so the session stops at the next prompt rather than ask a terminal
// that is gone.
type labelScreen struct {
	w   io.Writer
	err error
}

func (o *labelScreen) say(format string, args ...any) {
	if o.err == nil {
		_, o.err = fmt.Fprintf(o.w, format, args...)
	}
}

func (o *labelScreen) card(c *report.LabelCard) {
	if o.err == nil {
		o.err = report.Text(o.w, c)
	}
}

func (o *labelScreen) header(s *report.LabelSample) {
	if o.err == nil {
		o.err = report.LabelHeader(o.w, s)
	}
}

// keyReader reads the session's answers: from a terminal one key at a time,
// in raw mode only while it waits for the key, so ctrl-c and the screen
// behave as usual in between; from anything else, the first letter of each
// line, so a script can answer. Both stop waiting when ctx ends, so an
// interrupted session ends at once with the interrupt's exit status.
type keyReader struct {
	tty     *os.File
	lines   chan keyLine // a script's lines, read ahead by one goroutine
	pending []byte       // keys a raw read brought beyond the first
}

// keyLine is one line of a script's answers, or why there is none.
type keyLine struct {
	line string
	err  error
}

// lowerKey is an ASCII letter in lower case, any other byte as it is.
func lowerKey(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

func newKeyReader(ctx context.Context, in io.Reader) *keyReader {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return &keyReader{tty: f}
	}
	k := &keyReader{lines: make(chan keyLine)}
	go func() {
		r := bufio.NewReader(in)
		for {
			line, err := r.ReadString('\n')
			select {
			case k.lines <- keyLine{line, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return k
}

// next returns the next key, lowercased; 0 for one that is no key of ours,
// such as an arrow's escape sequence or an empty line; io.EOF when the
// input ends; ctx's error when it ends first.
func (k *keyReader) next(ctx context.Context) (byte, error) {
	if k.tty == nil {
		select {
		case l, ok := <-k.lines:
			if !ok {
				return 0, io.EOF
			}
			if s := strings.TrimSpace(l.line); s != "" {
				if l.err != nil {
					close(k.lines) // the last line had no line break: the next read is the end
				}
				return lowerKey(s[0]), nil
			}
			if l.err != nil {
				close(k.lines)
				return 0, l.err
			}
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if len(k.pending) > 0 {
		b := k.pending[0]
		k.pending = k.pending[1:]
		return lowerKey(b), nil
	}
	fd := int(k.tty.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return 0, fmt.Errorf("reading a key: %w", err)
	}
	type read struct {
		b   []byte
		err error
	}
	got := make(chan read, 1)
	go func() {
		buf := make([]byte, 8)
		n, err := k.tty.Read(buf)
		got <- read{buf[:n], err}
	}()
	select {
	case r := <-got:
		_ = term.Restore(fd, old)
		if r.err != nil {
			return 0, r.err
		}
		if len(r.b) == 0 || r.b[0] == 0x1b {
			return 0, nil // an arrow's or a function key's escape sequence
		}
		// Keys typed faster than the session asks arrive in one read: the
		// rest answer the next prompts.
		k.pending = append(k.pending, r.b[1:]...)
		return lowerKey(r.b[0]), nil
	case <-ctx.Done():
		// The read goroutine stays blocked until a key; the process is
		// ending, and the terminal is itself again.
		_ = term.Restore(fd, old)
		return 0, ctx.Err()
	}
}

// labelsWriter appends rows to a labels file, each written through to the
// disk before the next change is shown.
type labelsWriter struct {
	f *os.File
	w *csv.Writer
}

// openLabels opens path to append to, 0600 when it creates it, with a
// header row when it is new, and a line break first when its last row has
// none.
func openLabels(path string, existing []byte) (*labelsWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, usagef("%s: %v", path, pathCause(err))
	}
	w := &labelsWriter{f: f, w: csv.NewWriter(f)}
	info, err := f.Stat()
	switch {
	case err != nil:
	case info.Size() == 0:
		err = w.w.Write([]string{"change", "label", "labeller"})
	case len(existing) > 0 && existing[len(existing)-1] != '\n':
		_, err = f.WriteString("\n")
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return w, nil
}

// add appends one label and syncs it.
func (l *labelsWriter) add(change, value, labeller string) error {
	if err := l.w.Write([]string{change, value, labeller}); err != nil {
		return fmt.Errorf("writing %s: %w", l.f.Name(), err)
	}
	l.w.Flush()
	if err := l.w.Error(); err != nil {
		return fmt.Errorf("writing %s: %w", l.f.Name(), err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("writing %s: %w", l.f.Name(), err)
	}
	return nil
}
