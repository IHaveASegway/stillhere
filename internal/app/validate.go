package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

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

// The bar a ranked metric clears to lose its beta mark.
const (
	minAUC    = 0.7
	minLabels = 50
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
// with the AUC, over the changes of a stored run.
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
	v := &report.Validation{Labels: len(labels)}
	labellers := map[string]bool{}
	type judged struct {
		change int // index into changes
		value  string
	}
	var matched []judged
	unmatchedPulls := 0
	for _, l := range labels {
		if l.labeller != "" {
			labellers[l.labeller] = true
		}
		switch l.value {
		case labelMeaningful:
			v.Meaningful++
		case labelRoutine:
			v.Routine++
		default:
			v.Noise++
		}
		i, ok := idx.resolve(l.ref)
		if !ok {
			v.Unmatched++
			if l.ref.number > 0 {
				unmatchedPulls++
			}
			continue
		}
		if l.value != labelNoise {
			matched = append(matched, judged{change: i, value: l.value})
		}
	}
	v.Labellers = len(labellers)
	var ids []string
	for _, m := range allRanked() {
		scorer, err := metrics.NewScorer(m.ranked, in)
		if err != nil {
			return nil, fmt.Errorf("scoring changes with %s: %w", m.def.ID, err)
		}
		a := report.MetricAUC{Metric: m.def.ID.String()}
		var pos, neg []float64
		for _, j := range matched {
			x, ok := scorer.Score(&changes[j.change])
			if !ok {
				continue
			}
			if j.value == labelMeaningful {
				pos = append(pos, x)
			} else {
				neg = append(neg, x)
			}
		}
		a.Meaningful, a.Routine, a.N = len(pos), len(neg), len(pos)+len(neg)
		if len(pos) > 0 && len(neg) > 0 {
			auc := rank.AUC(pos, neg)
			lo, hi := rank.AUCInterval(m.def.ID, pos, neg)
			a.AUC, a.Lo, a.Hi = &auc, &lo, &hi
		}
		a.Beta = a.AUC == nil || *a.AUC < minAUC || a.N < minLabels
		v.Metrics = append(v.Metrics, a)
		ids = append(ids, a.Metric)
	}
	slices.SortFunc(v.Metrics, func(a, b report.MetricAUC) int { return cmp.Compare(a.Metric, b.Metric) })
	run := s.run
	v.Provenance = report.AnswerProvenance(&run, s.facts, s.forge, ids)
	v.Caveats = report.AnswerCaveats(s.facts)
	v.Caveats = append(v.Caveats, report.ForgeCaveats(&run, s.facts, s.forge, ids)...)
	if v.Unmatched > 0 {
		v.Caveats = append(v.Caveats, unmatchedCaveat(v.Unmatched, unmatchedPulls, s))
	}
	return v, nil
}

// unmatchedCaveat says how many labelled rows name no change of the run, or
// several, and how a change is named; when some named a pull request and
// the run read no GitHub data, that pull requests need it.
func unmatchedCaveat(n, pulls int, s *stored) report.Caveat {
	what := fmt.Sprintf("%d labelled changes are not changes of run %d, or name more than one, and were left out", n, s.run.ID)
	if n == 1 {
		what = fmt.Sprintf("1 labelled change is not a change of run %d, or names more than one, and was left out", s.run.ID)
	}
	what += ": a change is a landing's SHA or an unambiguous prefix of at least 7 of its hex digits, or a pull request " +
		"that landed in the run's history, written #950, pr:950 or owner/name#950"
	if pulls > 0 && (s.facts.Forge == nil || len(s.facts.Forge.PullRequests) == 0) {
		what += fmt.Sprintf("; %d of them name a pull request, and run %d read no GitHub data: run 'stillhere sync github', "+
			"then scan again", pulls, s.run.ID)
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
	return rest == "" || rest[0] == ',' || rest[0] == '\r'
}

// labelsFile is what a labels file holds.
type labelsFile struct {
	labels     []label     // the rows labelled meaningful, routine or noise
	skipped    []changeRef // the rows labelled skip: passed over, so stillhere label does not ask again
	unlabelled int         // the rows that name a change and give no label yet, as a sheet's do
	comments   int         // the lines read as comments
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
		switch value {
		case labelMeaningful, labelRoutine, labelNoise:
		case labelSkip:
			if isRef {
				f.skipped = append(f.skipped, ref)
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
		l := label{ref: ref, value: value}
		if !isRef {
			l.ref = changeRef{sha: strings.ToLower(strings.TrimSpace(rec[0]))} // names no change: unmatched
		}
		if len(rec) > 2 {
			l.labeller = strings.TrimSpace(rec[2])
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
