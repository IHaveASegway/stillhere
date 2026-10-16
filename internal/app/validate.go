package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The validity check (docs/metrics.md#validity-checks): does a ranked
// metric separate the changes a person labelled meaningful from those they
// labelled routine? Each labelled change gets the metric's measure of that
// change (metrics.Scorer: what the change adds to the metric's clusters),
// and the AUC is the chance that a meaningful change scores above a
// routine one. A change is a landing, or a pull request with the landings
// that put it on the default branch (metrics.Changes), so a label names
// either and every ranked metric scores it at its own grain: durable@1 and
// simplifying@1 by its landings, reviews_given@1 by its pull request (#77).
// Canonical reading: Kaner and Bond, "Software Engineering Metrics: What Do
// They Measure and How Do We Know?" (METRICS 2004).

// The labels a labels file may give. A row labelled skip, or not labelled
// yet (a sheet's empty cell), is no label: stillhere label writes skip for
// a change its labeller passed over, so a resumed session does not show it
// again.
const (
	labelMeaningful = "meaningful"
	labelRoutine    = "routine"
	labelNoise      = "noise"
	labelSkip       = "skip"
)

// The bar a ranked metric's labels clear (rank.BarAUC, rank.BarLabels).
const (
	minAUC    = rank.BarAUC
	minLabels = rank.BarLabels
)

// label is one labelled row of a labels file.
type label struct {
	ref      changeRef
	value    string // meaningful, routine or noise
	labeller string
}

// changeRef is how a labels row names a change (KB-19): a landing by its
// SHA or a prefix of one, or a pull request by its number, in one forge
// repository or in any of the run's.
type changeRef struct {
	sha    string // lowercased hex, at least 7 digits; "" for a pull request
	repo   string // a pull request's repository, "owner/name" lowercased; "" for any
	number int    // a pull request's number; 0 for a landing
}

// Validate scores each ranked metric in the registry against hand labels
// with the AUC, over the changes of a stored run. A change counts once
// (labelVotes): one label per change and labeller, and over several
// labellers the label most of them gave.
func Validate(ctx context.Context, d Deps, r ValidateRequest) (*report.Validation, error) {
	d, err := d.ready("app.Validate", false, false, true)
	if err != nil {
		return nil, err
	}
	labels, err := parseLabels(r.Labels, r.LabelsName)
	if err != nil {
		return nil, err
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	in := s.input()
	changes, err := metrics.Changes(in)
	if err != nil {
		return nil, fmt.Errorf("reading run %d's changes: %w", s.run.ID, err)
	}
	idx := newChangeIndex(changes)
	votes := labelVotes(labels, idx, r.Labeller)
	if r.Labeller != "" && votes.rows == 0 {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("%s: no labelled row of labeller %q (--labeller): the file's labellers are %s",
			cmp.Or(r.LabelsName, "labels"), r.Labeller, votes.names())}
	}
	v := &report.Validation{Labels: votes.rows, Labellers: votes.named, Unmatched: votes.unmatched}
	var judged []int   // indexes into changes, of the changes labelled meaningful or routine, in order
	var labelled []int // and of every change with a label, noise included, for --scores
	for _, i := range votes.order {
		switch votes.label[i] {
		case labelMeaningful:
			v.Meaningful++
			judged = append(judged, i)
		case labelRoutine:
			v.Routine++
			judged = append(judged, i)
		case labelNoise:
			v.Noise++
		default:
			continue // no majority
		}
		labelled = append(labelled, i)
		v.Changes++
	}
	// scored is each labelled change's measure by each metric, when the
	// request asks for them: the very numbers each AUC is computed from.
	var scored map[int]map[string]*float64
	if r.Scores {
		scored = make(map[int]map[string]*float64, len(labelled))
		for _, i := range labelled {
			scored[i] = map[string]*float64{}
		}
	}
	var ids, alone []string
	var left []report.Caveat // what a metric left out of its AUC, and why
	for _, m := range allRanked() {
		scorer, err := metrics.NewScorer(m.ranked, in)
		if err != nil {
			return nil, fmt.Errorf("scoring changes with %s: %w", m.def.ID, err)
		}
		a := report.MetricAUC{Metric: m.def.ID.String()}
		var pos, neg, sizePos, sizeNeg []float64
		var pairPos, pairNeg [][2]float64 // each scored change's score and size, together, for the paired comparison
		inWindow := 0                     // the changes labelled meaningful or routine that landed in its window
		for i, byMetric := range scored {
			byMetric[a.Metric] = nil
			if x, ok := scorer.Score(&changes[i]); ok {
				byMetric[a.Metric] = &x
			}
		}
		for _, i := range judged {
			c := &changes[i]
			if scorer.Window().Contains(c.Time) {
				inWindow++
			}
			x, ok := scorer.Score(c)
			if !ok {
				continue
			}
			size := float64(c.Added + c.Removed)
			if votes.label[i] == labelMeaningful {
				pos, sizePos, pairPos = append(pos, x), append(sizePos, size), append(pairPos, [2]float64{x, size})
			} else {
				neg, sizeNeg, pairNeg = append(neg, x), append(sizeNeg, size), append(pairNeg, [2]float64{x, size})
			}
		}
		a.Meaningful, a.Routine, a.N = len(pos), len(neg), len(pos)+len(neg)
		if len(pos) > 0 && len(neg) > 0 {
			auc := rank.AUC(pos, neg)
			lo, hi := rank.AUCInterval(m.def.ID, pos, neg)
			a.AUC, a.Lo, a.Hi = &auc, &lo, &hi
			// Size alone, over the same changes: a metric that grows with
			// size can separate the classes through size alone.
			size := rank.AUC(sizePos, sizeNeg)
			sizeLo, sizeHi := rank.AUCInterval(m.def.ID, sizePos, sizeNeg)
			a.SizeAUC, a.SizeLo, a.SizeHi = &size, &sizeLo, &sizeHi
			// Whether it beats size is a question about one sample, so the
			// two AUCs are compared on the same resampled changes, not by
			// their two intervals.
			diffLo, diffHi := rank.AUCDiffInterval(m.def.ID, pairPos, pairNeg)
			a.SizeDiffLo, a.SizeDiffHi = &diffLo, &diffHi
			if sep, ok := a.Separation(); ok {
				a.Verdict = sep.Verdict()
			}
		}
		a.Beta = a.AUC == nil || *a.AUC < minAUC || a.N < minLabels
		v.Metrics = append(v.Metrics, a)
		ids = append(ids, a.Metric)
		if m.def.Forge && a.N == 0 {
			alone = append(alone, a.Metric) // nothing scored: its own GitHub caveats say why, as a section about it alone does
		}
		if len(judged) > 0 && (inWindow == 0 || a.N < inWindow) {
			left = append(left, unscoredCaveat(a.Metric, scorer, a.N, inWindow, s.facts.Now))
		}
	}
	slices.SortFunc(v.Metrics, func(a, b report.MetricAUC) int { return cmp.Compare(a.Metric, b.Metric) })
	if scored != nil {
		v.Scores = changeScores(changes, slices.Sorted(maps.Keys(scored)), scored, labelFacts(s.facts), votes.label)
	}
	run := s.run
	v.Provenance = report.AnswerProvenance(&run, s.facts, s.forge, ids)
	v.Caveats = report.AnswerCaveats(s.facts)
	v.Caveats = append(v.Caveats, report.ForgeCaveats(&run, s.facts, s.forge, slices.DeleteFunc(slices.Clone(ids),
		func(id string) bool { return slices.Contains(alone, id) }))...)
	for _, id := range alone {
		v.Caveats = append(v.Caveats, report.ForgeCaveatsAlone(&run, s.facts, s.forge, id)...)
	}
	v.Caveats = append(v.Caveats, left...)
	v.Caveats = append(v.Caveats, votes.caveats(r.Labeller)...)
	if v.Unmatched > 0 {
		v.Caveats = append(v.Caveats, unmatchedCaveat(v.Unmatched, votes.unmatchedPulls, s))
	}
	return v, nil
}

// changeScores is the labelled changes, by their indexes into changes in
// the run's order, as --scores gives them: each one's label, its size as
// label's strata measure it, and each metric's measure of it.
func changeScores(changes []metrics.Change, labelled []int, scored map[int]map[string]*float64, x *labelFactsIndex,
	label map[int]string) []report.ChangeScore {
	out := make([]report.ChangeScore, 0, len(labelled))
	for _, i := range labelled {
		c := &changes[i]
		cs := report.ChangeScore{Change: c.Landings[0], Repo: x.name(c.Repo), Landed: c.Time, Label: label[i], Size: labelSize(c),
			Added: c.Added, Removed: c.Removed, Scores: scored[i]}
		if c.Pull != nil {
			cs.Pull = &report.LabelPull{Repo: c.Pull.Repo, Number: c.Pull.Number}
		}
		out = append(out, cs)
	}
	return out
}

// unscoredCaveat says how many of the labelled changes of a metric's window
// it scored, and which changes it scores, or that none of them landed in
// its window.
func unscoredCaveat(metric string, s *metrics.Scorer, scored, inWindow int, now time.Time) report.Caveat {
	ago := labelAgo(now, s.Window())
	if inWindow == 0 {
		return report.Caveat{Code: "unscored-labels", Message: fmt.Sprintf("%s scores the changes that landed %s, "+
			"and none of the changes labelled meaningful or routine did.", metric, ago)}
	}
	return report.Caveat{Code: "unscored-labels", Message: fmt.Sprintf("%s scored %d of the %s labelled meaningful or "+
		"routine that landed %s, the window it scores: it scores %s.", metric, scored, nounCount(inWindow, "change", "changes"),
		ago, s.Scores())}
}

// nounCount is "1 change" or "3 changes".
func nounCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// votes is a labels file's labels of the run's changes, one per change: each
// labeller's last row for a change wins, as label resumes, and over several
// labellers the label most of them gave, none when two labels tie.
type votes struct {
	order          []int          // the labelled changes, by their first row
	label          map[int]string // change → its label; "" for a tie
	rows           int            // the labelled rows read: one labeller's, when one is asked for
	named          int            // distinct labellers named
	labellers      map[string]bool
	other          int // rows of other labellers, left out
	otherNames     map[string]bool
	unmatched      int // rows that name no change of the run, or several
	unmatchedPulls int // of them, rows that name a pull request
	repeated       int // rows that a later row of the same labeller for the same change replaced
	several        int // changes labelled by more than one labeller
	agreed         int // of them, those every labeller gave one label
	ties           int // of them, those with no majority label
}

// labelVotes resolves each row of labels to its change, keeping only the
// rows of labeller when one is named.
func labelVotes(labels []label, idx *changeIndex, labeller string) *votes {
	type key struct {
		change   int
		labeller string
	}
	v := &votes{label: map[int]string{}, labellers: map[string]bool{}, otherNames: map[string]bool{}}
	last := map[key]string{}
	var keys []key
	for _, l := range labels {
		if labeller != "" && l.labeller != labeller {
			v.other++
			v.otherNames[l.labeller] = true
			continue
		}
		v.rows++
		v.labellers[l.labeller] = true
		i, ok := idx.resolve(l.ref)
		if !ok {
			v.unmatched++
			if l.ref.number > 0 {
				v.unmatchedPulls++
			}
			continue
		}
		k := key{i, l.labeller}
		if _, seen := last[k]; seen {
			v.repeated++
		} else {
			keys = append(keys, k)
		}
		last[k] = l.value
	}
	for name := range v.labellers {
		if name != "" {
			v.named++
		}
	}
	count := map[int]map[string]int{}
	for _, k := range keys {
		if count[k.change] == nil {
			count[k.change] = map[string]int{}
			v.order = append(v.order, k.change)
		}
		count[k.change][last[k]]++
	}
	for _, i := range v.order {
		best, top, tie, n := "", 0, false, 0
		for _, value := range []string{labelMeaningful, labelRoutine, labelNoise} {
			c := count[i][value]
			n += c
			switch {
			case c > top:
				best, top, tie = value, c, false
			case c == top && c > 0:
				tie = true
			}
		}
		if n > 1 {
			v.several++
			if top == n {
				v.agreed++
			}
		}
		if tie {
			v.ties++
			best = ""
		}
		v.label[i] = best
	}
	return v
}

// names lists the file's labellers, for a message.
func (v *votes) names() string {
	var out []string
	for name := range v.otherNames {
		if name == "" {
			name = "(none)"
		} else {
			name = strconv.Quote(name)
		}
		out = append(out, name)
	}
	slices.Sort(out)
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// caveats says which rows were replaced, left out, or pooled.
func (v *votes) caveats(labeller string) []report.Caveat {
	var out []report.Caveat
	if v.repeated > 0 {
		what := fmt.Sprintf("%d rows label a change that another row", v.repeated)
		if v.repeated == 1 {
			what = "1 row labels a change that another row"
		}
		out = append(out, report.Caveat{Code: "repeated-labels", Message: what + " of the same labeller labels too (by its " +
			"SHA, a prefix of it, or its pull request): a change counts once, by its labeller's last row, as stillhere label resumes."})
	}
	if labeller != "" && v.other > 0 {
		out = append(out, report.Caveat{Code: "several-labellers", Message: fmt.Sprintf("Only %s's labels count "+
			"(--labeller): the %s of the file's other labellers were left out.", labeller, nounCount(v.other, "row", "rows"))})
	}
	if v.several > 0 {
		msg := fmt.Sprintf("%s were labelled by more than one labeller: each counts once, by the label most of them "+
			"gave; they agreed on %d of them", nounCount(v.several, "change", "changes"), v.agreed)
		if v.several == 1 {
			agreed := "who gave it different labels"
			if v.agreed == 1 {
				agreed = "who agreed"
			}
			msg = "1 change was labelled by more than one labeller, " + agreed + ": it counts once, by the label most of them gave"
		}
		if v.ties > 0 {
			msg += fmt.Sprintf("; %s with no majority (two labels given equally often) left out", nounCount(v.ties, "change", "changes"))
		}
		out = append(out, report.Caveat{Code: "several-labellers", Message: msg + ". 'stillhere validate --labeller NAME' " +
			"scores one labeller's labels, so two leads' AUCs can be compared."})
	}
	return out
}

// unmatchedCaveat says how many labelled rows name no change of the run, or
// several, and how a change is named; when some named a pull request and
// the run read no GitHub data, that pull requests need it.
func unmatchedCaveat(n, pulls int, s *stored) report.Caveat {
	what := fmt.Sprintf("%d labelled rows name no change of run %d, or name more than one, and were left out", n, s.run.ID)
	if n == 1 {
		what = fmt.Sprintf("1 labelled row names no change of run %d, or names more than one, and was left out", s.run.ID)
	}
	what += ": a change is a landing's SHA or an unambiguous prefix of at least 7 of its hex digits, or a pull request " +
		"that landed in the run's history, written #950, pr:950 or owner/name#950"
	if pulls > 0 && (s.facts.Forge == nil || len(s.facts.Forge.PullRequests) == 0) {
		which := fmt.Sprintf("%d of them name a pull request", pulls)
		if pulls == 1 {
			which = "1 of them names a pull request"
			if n == 1 {
				which = "it names a pull request"
			}
		}
		what += fmt.Sprintf("; %s, and run %d read no GitHub data: run 'stillhere sync github', then scan again", which, s.run.ID)
	}
	return report.Caveat{Code: "unmatched-labels", Message: what + "."}
}

// changeIndex finds the change a labels row names.
type changeIndex struct {
	shas    []string       // every landing's SHA, lowercased and sorted
	landing map[string]int // landing SHA → its change
	pulls   map[int][]int  // pull request number → the changes of that number, in any repository
	changes []metrics.Change
}

func newChangeIndex(changes []metrics.Change) *changeIndex {
	x := &changeIndex{landing: map[string]int{}, pulls: map[int][]int{}, changes: changes}
	for i, c := range changes {
		for _, sha := range c.Landings {
			sha = strings.ToLower(sha)
			x.shas = append(x.shas, sha)
			x.landing[sha] = i
		}
		if c.Pull != nil {
			x.pulls[c.Pull.Number] = append(x.pulls[c.Pull.Number], i)
		}
	}
	slices.Sort(x.shas)
	x.shas = slices.Compact(x.shas)
	return x
}

// resolve returns the one change ref names: the change holding the one
// landing whose SHA starts with ref.sha, or the one pull request of that
// number (in ref.repo, when it names one) that landed in the run's history.
func (x *changeIndex) resolve(ref changeRef) (int, bool) {
	if ref.number > 0 {
		found := -1
		for _, i := range x.pulls[ref.number] {
			if ref.repo != "" && !strings.EqualFold(x.changes[i].Pull.Repo, ref.repo) {
				continue
			}
			if found >= 0 {
				return 0, false // two repositories have a pull request of that number
			}
			found = i
		}
		return found, found >= 0
	}
	if !changeID(ref.sha) {
		return 0, false
	}
	i, _ := slices.BinarySearch(x.shas, ref.sha)
	if i >= len(x.shas) || !strings.HasPrefix(x.shas[i], ref.sha) {
		return 0, false
	}
	if i+1 < len(x.shas) && strings.HasPrefix(x.shas[i+1], ref.sha) {
		return 0, false
	}
	return x.landing[x.shas[i]], true
}

// changeID reports whether s can name a landing: a SHA or a prefix of at
// least 7 hex digits, lowercased.
func changeID(s string) bool {
	return len(s) >= 7 && strings.Trim(s, "0123456789abcdef") == ""
}

// parseChangeRef reads a labels row's first field (KB-19): a pull request
// written #950, pr:950 or owner/name#950, else a landing SHA or a prefix of
// one. It reports false for a field that is neither, such as a header's
// "change".
func parseChangeRef(field string) (changeRef, bool) {
	s := strings.ToLower(strings.TrimSpace(field))
	if n, ok := strings.CutPrefix(s, "pr:"); ok {
		return pullRef("", n)
	}
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		repo := s[:i]
		if repo != "" && strings.Count(repo, "/") != 1 {
			return changeRef{}, false
		}
		return pullRef(repo, s[i+1:])
	}
	if changeID(s) {
		return changeRef{sha: s}, true
	}
	return changeRef{}, false
}

// pullRef is a pull request's ref, when digits is its number.
func pullRef(repo, digits string) (changeRef, bool) {
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 || strings.Trim(digits, "0123456789") != "" {
		return changeRef{}, false
	}
	return changeRef{repo: repo, number: n}, true
}

// pullRow reports whether a line starts a row that names a pull request by
// "#" and its number, "#950,meaningful": the one kind of line that starts
// with "#" and is not a comment (KB-19).
func pullRow(line string) bool {
	rest, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "#")
	if !ok {
		return false
	}
	digits := len(rest) - len(strings.TrimLeft(rest, "0123456789"))
	if digits == 0 {
		return false
	}
	rest = strings.TrimLeft(rest[digits:], " \t")
	return rest == "" || rest[0] == ',' || rest[0] == '\r' || rest[0] == '\n'
}

// labelsFile is what a labels file holds.
type labelsFile struct {
	labels     []label // the rows labelled meaningful, routine or noise
	skipped    []label // the rows labelled skip: passed over, so stillhere label does not ask its labeller again
	unlabelled int     // the rows that name a change and give no label yet, as a sheet's do
	comments   int     // the lines read as comments
}

// readLabels reads a labels CSV: change, label, and optionally the
// labeller, one change per row, with an optional header row and comments.
// A line that starts with "#" is a comment, unless it names a pull request
// as "#" and its number ("#950,meaningful"): pullRow. The first row is a
// header only when it names no change and gives no label, so a misspelt
// label on the first row is an error like on any other. A row labelled
// skip, or with no label yet, is no label. A malformed file is a usage
// error that names the line.
func readLabels(data []byte, name string) (*labelsFile, error) {
	if name == "" {
		name = "labels"
	}
	// Comments go before the CSV reader sees the file, line for line, so
	// every line keeps its number for the messages; encoding/csv's own
	// Comment would read "#950,meaningful" as one.
	f := &labelsFile{}
	lines := bytes.SplitAfter(data, []byte("\n"))
	for i, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) > 0 && trimmed[0] == '#' && !pullRow(string(line)) {
			lines[i] = []byte("\n")
			f.comments++
		}
	}
	r := csv.NewReader(bytes.NewReader(bytes.Join(lines, nil)))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	for first := true; ; first = false {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("%s: %v", name, err)}
		}
		line, _ := r.FieldPos(0)
		value := ""
		if len(rec) > 1 {
			value = strings.ToLower(strings.TrimSpace(rec[1]))
		}
		ref, isRef := parseChangeRef(rec[0])
		labeller := ""
		if len(rec) > 2 {
			labeller = strings.TrimSpace(rec[2])
		}
		switch value {
		case labelMeaningful, labelRoutine, labelNoise:
		case labelSkip:
			if isRef {
				f.skipped = append(f.skipped, label{ref: ref, value: labelSkip, labeller: labeller})
			}
			continue
		case "":
			if isRef {
				f.unlabelled++
			}
			continue // not labelled yet, or a header such as "change"
		default:
			if first && !isRef {
				continue // a header row, such as "change,label,labeller"
			}
			return nil, &domain.UsageError{Msg: fmt.Sprintf("%s:%d: label %q: want meaningful, routine or noise "+
				"(skip, or no label, passes a change over)", name, line, rec[1])}
		}
		l := label{ref: ref, value: value, labeller: labeller}
		if !isRef {
			l.ref = changeRef{sha: strings.ToLower(strings.TrimSpace(rec[0]))} // names no change: unmatched
		}
		f.labels = append(f.labels, l)
	}
	return f, nil
}

// parseLabels reads a labels CSV for validate (readLabels), which needs at
// least one label: a file with none is a usage error that says why it
// found none.
func parseLabels(data []byte, name string) ([]label, error) {
	f, err := readLabels(data, name)
	if err != nil {
		return nil, err
	}
	if len(f.labels) > 0 {
		return f.labels, nil
	}
	if name == "" {
		name = "labels"
	}
	msg := name + ": no labelled changes: want rows of change, label (meaningful, routine or noise) and optionally the labeller"
	if n := f.unlabelled + len(f.skipped); n > 0 {
		msg += fmt.Sprintf("; rows skipped or not labelled yet: %d", n)
	}
	if f.comments > 0 {
		msg += fmt.Sprintf("; lines that start with '#' and were read as comments: %d (a pull request is #950, the "+
			"digits right after the '#', or pr:950 or owner/name#950)", f.comments)
	}
	return nil, &domain.UsageError{Msg: msg}
}
