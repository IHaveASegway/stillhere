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
and running label again resumes where it stopped: the same run, --n and --seed
give the same sample, and a larger --n extends it (after a newer scan, --run ID
keeps the run the labels began on). 'stillhere validate' then scores every
ranked metric against the labels. Piped input answers one line per change.

The sample comes from the changes a ranked metric scores, people's only, half
from each window the metrics read (durable@1: 90 to 365 days ago; simplifying@1
and reviews_given@1: the last 90 days), in proportion to each repository's
small, medium and large changes. It is blinded: no metric value, rank or author
is shown. docs/validation.md has the rubric and worked examples.

--sheet writes the sample as a CSV instead, to label in a spreadsheet; validate
reads the filled-in sheet as it is. --plan prints the sample and its strata.

The labels judge colleagues' work: the file is kept beside the facts database
($STILLHERE_HOME/labels.csv), outside every repository, unless --labels names
another.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkRun(run); err != nil {
				return err
			}
			if sheet && plan {
				return usagef("--sheet and --plan each print the sample: give one")
			}
			interactive := !sheet && !plan
			if interactive && e.format != formatText {
				return usagef("label asks about changes as text: --format applies to --plan")
			}
			flag, path, err := e.labelsPath(labels)
			if err != nil {
				return err
			}
			data, err := readLabelsFile(flag, path, true) // a new file is made at the first label
			if err != nil {
				return err
			}
			req := app.LabelRequest{RunID: run, N: size, Seed: seed, Labels: data, LabelsName: path}
			return e.with(cmd.Context(), need{store: true, git: interactive}, func(d app.Deps) error {
				sample, err := app.Label(cmd.Context(), d, req)
				if err != nil {
					return err
				}
				switch {
				case sheet:
					e.log.Info("the sample", "changes", len(sample.Changes), "pool", sample.Pool, "labelled", sample.Labelled,
						"run", sample.Provenance.RunID)
					return report.Sheet(e.stdout, sample)
				case plan:
					return e.render(sample)
				}
				return e.labelSession(cmd.Context(), cmd.InOrStdin(), d, sample, path, data, labeller)
			})
		},
	}
	cmd.Flags().IntVar(&size, "n", app.DefaultLabelSample, "the sample's size: half from each window the ranked metrics read")
	cmd.Flags().Uint64Var(&seed, "seed", 1, "the sample's seed: the same run, --n and --seed give the same sample")
	cmd.Flags().StringVar(&labels, "labels", "", "labels CSV `FILE` to append to (default labels.csv beside the facts database)")
	cmd.Flags().StringVar(&labeller, "labeller", "", "a `NAME` for the labeller, written in each row's third column")
	cmd.Flags().BoolVar(&sheet, "sheet", false, "write the sample as a CSV to label in a spreadsheet, and label nothing here")
	cmd.Flags().BoolVar(&plan, "plan", false, "print the sample and its strata in --format, and label nothing")
	addRunFlag(cmd, &run)
	return cmd
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

// readLabelsFile reads a labels file through readable, so that a FIFO or a
// device is refused rather than opened, and only up to maxLabelsBytes. A
// missing file is nil when optional: label's, which its first label makes;
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

// labelSession asks about each sampled change the labels file holds no
// label for, in the sample's order, and appends each answer to it at once.
func (e *env) labelSession(ctx context.Context, in io.Reader, d app.Deps, s *report.LabelSample, path string, existing []byte,
	labeller string) error {
	var todo []int
	for i, c := range s.Changes {
		if c.Label == "" {
			todo = append(todo, i)
		}
	}
	out := &labelScreen{w: e.stdout}
	out.say("label · run %d · %d of %d sampled changes to label, %d labelled already in %s\n",
		s.Provenance.RunID, len(todo), len(s.Changes), s.Labelled, terminalSafe(path))
	if len(todo) == 0 {
		out.say("Every sampled change has a label. Next: stillhere validate --labels %s (or label more with a larger --n)\n",
			terminalSafe(path))
		return out.err
	}
	out.say("Blinded: no metric value, rank or author is shown. Judge the change; press ? for the rubric.\n")
	keys := newKeyReader(in)
	var w *labelsWriter
	defer func() {
		if w != nil {
			_ = w.f.Close()
		}
	}()
	done := 0
	finish := func() error {
		out.say("\nLabelled %d this session, %d of %d sampled. Next: stillhere validate --labels %s\n",
			done, s.Labelled+done, len(s.Changes), terminalSafe(path))
		if w != nil {
			err := w.f.Close()
			w = nil
			if err != nil {
				return err
			}
		}
		return out.err
	}
	for _, i := range todo {
		card := &report.LabelCard{Index: i + 1, Of: len(s.Changes), Change: s.Changes[i]}
		out.say("\n")
		out.card(card)
		value := ""
		for value == "" {
			if out.err != nil {
				return out.err
			}
			out.say("%s > ", labelKeys)
			key, err := keys.next(ctx)
			if err != nil {
				out.say("\n")
				if errors.Is(err, io.EOF) {
					return finish()
				}
				return err
			}
			out.say("%c\n", key)
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
				diff, err := app.Diff(ctx, d, app.DiffRequest{RunID: s.Provenance.RunID, Repo: card.Change.RepoKey, Change: card.Change.Change})
				if err != nil {
					out.say("  no diff: %s\n", terminalSafe(e.userMessage(err, false)))
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
				return finish()
			}
		}
		if w == nil {
			var err error
			if w, err = openLabels(path, existing); err != nil {
				return err
			}
		}
		if err := w.add(card.Change.Change, value, labeller); err != nil {
			return err
		}
		done++
	}
	return finish()
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

// keyReader reads the session's answers: from a terminal one key at a time,
// in raw mode only while it waits for the key, so ctrl-c and the screen
// behave as usual in between; from anything else, the first letter of each
// line, so a script can answer.
type keyReader struct {
	tty     *os.File
	lines   *bufio.Reader
	pending []byte // keys a raw read brought beyond the first
}

// lowerKey is an ASCII letter in lower case, any other byte as it is.
func lowerKey(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

func newKeyReader(in io.Reader) *keyReader {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return &keyReader{tty: f}
	}
	return &keyReader{lines: bufio.NewReader(in)}
}

// next returns the next key, lowercased; 0 for one that is no key of ours,
// such as an arrow's escape sequence; io.EOF when the input ends.
func (k *keyReader) next(ctx context.Context) (byte, error) {
	if k.tty == nil {
		line, err := k.lines.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			return lowerKey(s[0]), nil
		}
		return 0, err
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
