package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// This file is stillhere label's answers (docs/validation.md): a stratified
// sample of a run's changes for a person to label meaningful, routine or
// noise, as a document (--plan), one change at a time as a card (the
// interactive session), and as a sheet (--sheet), a CSV to label in a
// spreadsheet that validate reads back as it is. Every one is blinded: it
// holds no metric value, no rank and no author, so a label says what the
// labeller thinks of the change, not of the metric's verdict or of its
// author (docs/adr/0014-labels-are-evidence-not-the-gate.md).

// LabelSample is the changes stillhere label asks about, in the order to
// label them.
type LabelSample struct {
	Provenance Provenance `json:"provenance"`
	Seed       uint64     `json:"seed"`     // the same run, size, seed and labels give the same sample; a larger one extends it
	Asked      int        `json:"asked"`    // the sample size asked for (--n); 0: until the bar, per window
	Pool       int        `json:"pool"`     // the run's changes a ranked metric scores, a person's: what the sample is drawn from
	Labeller   string     `json:"labeller"` // whose labels it resumes (--labeller); "" for the rows that name none
	// OtherLabellers counts the labellers of the labels file's other rows,
	// whose labels this sample does not resume.
	OtherLabellers int             `json:"other_labellers"`
	Labelled       int             `json:"labelled"` // sampled changes the labeller has labelled or skipped
	Progress       []LabelProgress `json:"progress"` // per ranked metric, by id: the labels toward its bar
	Strata         []LabelStratum  `json:"strata"`   // by window, then size, then repository
	Changes        []LabelChange   `json:"changes"`  // the sample, in the order to label it
	Caveats        []Caveat        `json:"caveats"`
}

// LabelProgress is how far one ranked metric's labels are toward the bar:
// BarLabels changes labelled meaningful or routine that it scores.
type LabelProgress struct {
	Metric   string `json:"metric"`
	Ago      string `json:"ago"`      // the window it scores, in days before the run's now
	Labelled int    `json:"labelled"` // changes the labeller labelled meaningful or routine that it scores, sampled or not
	Sampled  int    `json:"sampled"`  // sampled changes it scores, less those answered noise or skip
	Needed   int    `json:"needed"`   // BarLabels less Labelled, at least 0
}

// LabelStratum is one window, size and repository of the sample, with the
// changes in it the sample was drawn from.
type LabelStratum struct {
	Window  Interval `json:"window"`
	Ago     string   `json:"ago"`     // the window in days before the run's now: "0–90 days ago"
	Metrics []string `json:"metrics"` // the ranked metrics that score the changes of the window
	Size    string   `json:"size"`    // LabelSizes
	Repo    string   `json:"repo"`    // repository name
	RepoKey string   `json:"repo_key"`
	Pool    int      `json:"pool"`
	Sample  int      `json:"sample"`
}

// The size of a change: the lines its own diffs add and remove on paths
// with a role (metrics.Change). Small is fewer than 10, large 100 or more.
const (
	LabelSmall  = "small"
	LabelMedium = "medium"
	LabelLarge  = "large"
)

// LabelSizes are the sizes, smallest first.
var LabelSizes = []string{LabelSmall, LabelMedium, LabelLarge}

// LabelChange is one sampled change, as its labeller sees it.
type LabelChange struct {
	Change      string      `json:"change"` // the full SHA of its first landing: what a labels row names it by
	Repo        string      `json:"repo"`   // repository name
	RepoKey     string      `json:"repo_key"`
	Landings    []string    `json:"landings"` // every landing of the change, the first one first
	Landed      time.Time   `json:"landed"`
	Ago         string      `json:"ago"` // its stratum's window
	Size        string      `json:"size"`
	Pull        *LabelPull  `json:"pull"`    // null for a landing no pull request landed
	Title       string      `json:"title"`   // the pull request's title, else the landing commit's subject
	Commits     []string    `json:"commits"` // the subjects of the commits a merge brought in, or of a rebase merge's landings, oldest first; [] otherwise
	MoreCommits int         `json:"more_commits"`
	Added       int         `json:"added"` // numstat lines on paths with a role, as Size counts them
	Removed     int         `json:"removed"`
	Files       []LabelFile `json:"files"` // the paths its landings' own diffs touch, the most lines first
	MoreFiles   int         `json:"more_files"`
	Label       string      `json:"label"` // the labels file's label for it so far: meaningful, routine, noise, skip, or ""
}

// LabelPull is the pull request a sampled change is.
type LabelPull struct {
	Repo   string `json:"repo"` // "owner/name"
	Number int    `json:"number"`
}

// LabelFile is one path a sampled change touches.
type LabelFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path"` // a rename's source, else ""
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
	Class   string `json:"class"` // its role, or why it has none (denied_path, too_big, generated_header, unclassified)
}

// LabelCard is one change of a sample as the interactive session shows it,
// with its patch once the labeller asks for it.
type LabelCard struct {
	Index     int         `json:"index"` // 1-based place in the sample
	Of        int         `json:"of"`
	Change    LabelChange `json:"change"`
	Patch     string      `json:"patch"` // "" until asked for
	Truncated bool        `json:"truncated"`
}

// pullText names c's pull request as a labels row may ("owner/name#950"),
// or is "" for a landing no pull request landed.
func (c *LabelChange) pullText() string {
	if c.Pull == nil {
		return ""
	}
	if c.Pull.Repo == "" {
		return "#" + strconv.Itoa(c.Pull.Number)
	}
	return c.Pull.Repo + "#" + strconv.Itoa(c.Pull.Number)
}

func (s *LabelSample) summary() string {
	asked := fmt.Sprintf("%d asked", s.Asked)
	if s.Asked == 0 {
		asked = "until the bar"
	}
	who := "no labeller named"
	if s.Labeller != "" {
		who = "labeller " + s.Labeller
	}
	return fmt.Sprintf("%s sampled of %s that a ranked metric scores (seed %d, %s) · %s, %d labelled so far",
		plural(len(s.Changes), "change", "changes"), plural(s.Pool, "change", "changes"), s.Seed, asked, who, s.Labelled)
}

// ProgressText says how far each ranked metric's labels are toward the
// bar, in one sentence; "" when there is nothing to label.
func (s *LabelSample) ProgressText() string {
	if s.Pool == 0 {
		return ""
	}
	var parts []string
	for _, p := range s.Progress {
		part := fmt.Sprintf("%s %d of %d", p.Metric, min(p.Labelled, BarLabels), BarLabels)
		if p.Needed > 0 {
			part += fmt.Sprintf(" (%d more)", p.Needed)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Toward the bar, %d labels meaningful or routine that a metric scores: %s.", BarLabels,
		strings.Join(parts, ", "))
}

// Ready reports whether every ranked metric has the labels its bar needs.
func (s *LabelSample) Ready() bool {
	for _, p := range s.Progress {
		if p.Needed > 0 {
			return false
		}
	}
	return len(s.Progress) > 0
}

// LabelHeader writes what stillhere label's session prints before its
// first card: the sample, the labels toward the bar, and the caveats, with
// addresses withheld as on every page.
func LabelHeader(w io.Writer, s *LabelSample) error {
	d, err := page(s)
	if err != nil {
		return err
	}
	s = d.(*LabelSample)
	p := &printer{w: w}
	p.line("label · " + provenanceText(s.Provenance))
	p.wrapped("  ", "  ", s.summary())
	if s.OtherLabellers > 0 {
		p.wrapped("  ", "  ", fmt.Sprintf("The labels file also holds the labels of %s, which this session neither "+
			"shows nor changes: --labeller NAME resumes one's.", plural(s.OtherLabellers, "other labeller", "other labellers")))
	}
	if t := s.ProgressText(); t != "" {
		p.wrapped("  ", "  ", t)
	}
	textCaveats(p, "", "What could make this wrong", s.Caveats, false)
	if p.err != nil {
		return fmt.Errorf("report: write text: %w", p.err)
	}
	return nil
}

// windowRows are the strata summed over repositories: one row per window
// and size.
func (s *LabelSample) windowRows() [][]string {
	type key struct{ ago, size string }
	var order []key
	sums := map[key][2]int{}
	metrics := map[string]string{}
	for _, st := range s.Strata {
		k := key{st.Ago, st.Size}
		if _, ok := sums[k]; !ok {
			order = append(order, k)
		}
		v := sums[k]
		v[0] += st.Pool
		v[1] += st.Sample
		sums[k] = v
		metrics[st.Ago] = strings.Join(st.Metrics, ", ")
	}
	var rows [][]string
	for _, k := range order {
		rows = append(rows, []string{k.ago, metrics[k.ago], k.size, grouped(sums[k][0]), grouped(sums[k][1])})
	}
	return rows
}

// repoRows are the strata summed over windows and sizes: one row per
// repository, when the sample holds more than one.
func (s *LabelSample) repoRows(esc func(string) string) [][]string {
	var order []string
	names := map[string]string{}
	sums := map[string][2]int{}
	for _, st := range s.Strata {
		if _, ok := sums[st.RepoKey]; !ok {
			order = append(order, st.RepoKey)
			names[st.RepoKey] = st.Repo
		}
		v := sums[st.RepoKey]
		v[0] += st.Pool
		v[1] += st.Sample
		sums[st.RepoKey] = v
	}
	if len(order) < 2 {
		return nil
	}
	var rows [][]string
	for _, k := range order {
		rows = append(rows, []string{esc(names[k]), grouped(sums[k][0]), grouped(sums[k][1])})
	}
	return rows
}

func (s *LabelSample) changeRow(i int, c *LabelChange, esc func(string) string, sha func(string) string) []string {
	label := c.Label
	if label == "" {
		label = "—"
	}
	return []string{strconv.Itoa(i + 1), sha(short(c.Change)), esc(c.Repo), date(c.Landed), c.Ago, c.Size,
		fmt.Sprintf("+%s −%s", grouped(c.Added), grouped(c.Removed)), label, esc(c.Title)}
}

var labelChangeCols = []column{{head: "#", right: true}, {head: "change"}, {head: "repo"}, {head: "landed"}, {head: "window"},
	{head: "size"}, {head: "lines", right: true}, {head: "label"}, {head: "title"}}

func (s *LabelSample) writeText(p *printer) {
	p.line("label · " + provenanceText(s.Provenance))
	p.wrapped("  ", "  ", s.summary())
	p.line("  blinded: no metric value, rank or author is shown, so a label judges the change alone")
	if t := s.ProgressText(); t != "" {
		p.wrapped("  ", "  ", t)
	}
	if rows := s.windowRows(); len(rows) > 0 {
		p.blank()
		p.line("  Strata (pool → sample)")
		p.textTable("  ", []column{{head: "window"}, {head: "scored by"}, {head: "size"}, {head: "pool", right: true},
			{head: "sample", right: true}}, rows)
	}
	if rows := s.repoRows(clean); rows != nil {
		p.blank()
		p.textTable("  ", []column{{head: "repository"}, {head: "pool", right: true}, {head: "sample", right: true}}, rows)
	}
	if len(s.Changes) > 0 {
		p.blank()
		var rows [][]string
		for i := range s.Changes {
			rows = append(rows, s.changeRow(i, &s.Changes[i], clean, func(x string) string { return x }))
		}
		p.textTable("  ", labelChangeCols, rows)
	}
	textCaveats(p, "", "What could make this wrong", s.Caveats, false)
}

func (s *LabelSample) writeMarkdown(p *printer) {
	p.line("# Changes to label")
	p.blank()
	p.line("- " + provenanceText(s.Provenance))
	p.line("- " + s.summary())
	p.line("- Blinded: no metric value, rank or author is shown, so a label judges the change alone.")
	if t := s.ProgressText(); t != "" {
		p.line("- " + t)
	}
	if rows := s.windowRows(); len(rows) > 0 {
		p.blank()
		p.mdTable([]column{{head: "Window"}, {head: "Scored by"}, {head: "Size"}, {head: "Pool", right: true},
			{head: "Sample", right: true}}, rows)
	}
	if rows := s.repoRows(mdText); rows != nil {
		p.blank()
		p.mdTable([]column{{head: "Repository"}, {head: "Pool", right: true}, {head: "Sample", right: true}}, rows)
	}
	if len(s.Changes) > 0 {
		p.blank()
		var rows [][]string
		for i := range s.Changes {
			rows = append(rows, s.changeRow(i, &s.Changes[i], mdText, mdCode))
		}
		p.mdTable(labelChangeCols, rows)
	}
	mdCaveats(p, "##", s.Caveats, false)
}

// maxCardFiles is how many paths a card lists; the rest are counted.
const maxCardFiles = 15

func (c *LabelCard) writeText(p *printer) {
	ch := &c.Change
	head := fmt.Sprintf("[%d of %d] %s · landed %s · %s · %s", c.Index, c.Of, clean(ch.Repo), date(ch.Landed), ch.Ago, ch.Size)
	if pr := ch.pullText(); pr != "" {
		head += " · pull request " + clean(pr)
	} else {
		head += " · landing " + short(ch.Change)
	}
	p.line(head)
	p.wrapped("  ", "  ", clean(ch.Title))
	if len(ch.Commits) > 0 {
		p.line("  commits it brought in:")
		for _, s := range ch.Commits {
			p.wrapped("    - ", "      ", clean(s))
		}
		if ch.MoreCommits > 0 {
			p.linef("    and %s", plural(ch.MoreCommits, "more commit", "more commits"))
		}
	}
	p.linef("  %s; +%s −%s on paths with a role", plural(len(ch.Files)+ch.MoreFiles, "file", "files"),
		grouped(ch.Added), grouped(ch.Removed))
	var rows [][]string
	for i, f := range ch.Files {
		if i == maxCardFiles {
			break
		}
		lines := fmt.Sprintf("+%s −%s", grouped(f.Added), grouped(f.Removed))
		if f.Binary {
			lines = "binary"
		}
		path := clean(f.Path)
		if f.OldPath != "" {
			path = clean(f.OldPath) + " → " + path
		}
		rows = append(rows, []string{lines, path, f.Class})
	}
	if len(rows) > 0 {
		p.textTable("    ", []column{{head: "lines", right: true}, {head: "path"}, {head: "class"}}, rows)
	}
	if more := len(ch.Files) - min(len(ch.Files), maxCardFiles) + ch.MoreFiles; more > 0 {
		p.linef("    and %s", plural(more, "more file", "more files"))
	}
	if c.Patch != "" {
		p.blank()
		for _, l := range strings.Split(strings.TrimRight(c.Patch, "\n"), "\n") {
			p.line("  " + patchLine(l))
		}
		if c.Truncated {
			p.line("  (the patch is cut here)")
		}
	}
}

func (c *LabelCard) writeMarkdown(p *printer) {
	ch := &c.Change
	p.linef("## %d of %d: %s", c.Index, c.Of, mdText(ch.Title))
	p.blank()
	p.linef("- %s, landed %s, %s, %s", mdText(ch.Repo), date(ch.Landed), ch.Ago, ch.Size)
	if pr := ch.pullText(); pr != "" {
		p.line("- pull request " + mdText(pr))
	}
	p.linef("- landing %s, +%s −%s on paths with a role", mdCode(ch.Change), grouped(ch.Added), grouped(ch.Removed))
	if c.Patch != "" {
		p.blank()
		fence := "```"
		for strings.Contains(c.Patch, fence) {
			fence += "`"
		}
		p.line(fence + "diff")
		for _, l := range strings.Split(strings.TrimRight(c.Patch, "\n"), "\n") {
			p.line(patchLine(l))
		}
		p.line(fence)
	}
}

// patchLine makes one line of a patch safe to print on a terminal as clean
// does, but keeps its spacing, which a patch needs: a tab is four spaces,
// and other whitespace stays as it is.
func patchLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == ' ':
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SheetColumns are the sheet's columns. The first three are a labels
// file's, so validate reads a filled-in sheet as it is; the rest are what
// a labeller reads, and validate ignores them.
var SheetColumns = []string{"change", "label", "labeller", "repo", "pull_request", "landed", "window", "size",
	"added", "removed", "files", "title", "commits"}

// Sheet writes the sample as a CSV, one row per change in its order, with
// each change's label so far and its labeller (LabelSample.Labeller): what
// a labeller fills in, in a spreadsheet, for validate. Addresses in its text are withheld as on every page. A cell
// a spreadsheet would read as a formula (one that starts with =, +, -, @,
// a tab or a carriage return) starts with a ' instead, which spreadsheets
// show as text: a commit subject is anyone's text (OWASP, "CSV
// Injection").
func Sheet(w io.Writer, s *LabelSample) error {
	d, err := page(s)
	if err != nil {
		return err
	}
	s = d.(*LabelSample)
	cw := csv.NewWriter(w)
	if err := cw.Write(SheetColumns); err != nil {
		return fmt.Errorf("report: write sheet: %w", err)
	}
	for i := range s.Changes {
		c := &s.Changes[i]
		commits := strings.Join(c.Commits, " · ")
		if c.MoreCommits > 0 {
			commits += fmt.Sprintf(" · and %d more", c.MoreCommits)
		}
		row := []string{c.Change, c.Label, sheetCell(s.Labeller), sheetCell(c.Repo), sheetCell(c.pullText()), date(c.Landed), c.Ago, c.Size,
			strconv.Itoa(c.Added), strconv.Itoa(c.Removed), strconv.Itoa(len(c.Files) + c.MoreFiles), sheetCell(c.Title),
			sheetCell(commits)}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("report: write sheet: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("report: write sheet: %w", err)
	}
	return nil
}

// ScoreColumns are the first columns of validate's scores (--scores), named
// as the sheet names them; one column per ranked metric follows, by id.
var ScoreColumns = []string{"change", "label", "repo", "pull_request", "landed", "size", "added", "removed"}

// Scores writes a Validation's per-change scores (Validation.Scores) as a
// CSV: ScoreColumns, then each of its metrics' measure of the change, empty
// where the metric does not score it. With it, anyone can recompute each
// AUC, or compare it with size on exactly the changes the metric scored.
// Its text is withheld and kept from formulas as the sheet's is.
func Scores(w io.Writer, v *Validation) error {
	d, err := page(v)
	if err != nil {
		return err
	}
	v = d.(*Validation)
	cw := csv.NewWriter(w)
	head := slices.Clone(ScoreColumns)
	for _, a := range v.Metrics {
		head = append(head, a.Metric)
	}
	if err := cw.Write(head); err != nil {
		return fmt.Errorf("report: write scores: %w", err)
	}
	for _, c := range v.Scores {
		pull := ""
		if c.Pull != nil {
			pull = (&LabelChange{Pull: c.Pull}).pullText()
		}
		row := []string{c.Change, c.Label, sheetCell(c.Repo), sheetCell(pull), date(c.Landed), c.Size,
			strconv.Itoa(c.Added), strconv.Itoa(c.Removed)}
		for _, a := range v.Metrics {
			cell := ""
			if x := c.Scores[a.Metric]; x != nil {
				cell = strconv.FormatFloat(*x, 'f', -1, 64)
			}
			row = append(row, cell)
		}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("report: write scores: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("report: write scores: %w", err)
	}
	return nil
}

// sheetCell is untrusted text as a sheet holds it: clean, and never read as
// a formula.
func sheetCell(s string) string {
	s = clean(s)
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
