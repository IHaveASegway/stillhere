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
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/rank"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The validity check (docs/metrics.md#validity-checks): does a ranked
// metric separate the changes a person labelled meaningful from those they
// labelled routine? Each labelled change gets the metric's value for that
// change, and the AUC is the chance that a meaningful change scores above a
// routine one. Canonical reading: Kaner and Bond, "Software Engineering
// Metrics: What Do They Measure and How Do We Know?" (METRICS 2004).

// The labels a labels file may give.
const (
	labelMeaningful = "meaningful"
	labelRoutine    = "routine"
	labelNoise      = "noise"
)

// The bar a ranked metric clears to lose its beta mark.
const (
	minAUC    = 0.7
	minLabels = 50
)

// label is one row of a labels file.
type label struct {
	change   string // a landing SHA or an unambiguous prefix of one, lowercased
	value    string // meaningful, routine or noise
	labeller string
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
	v := &report.Validation{Labels: len(labels)}
	labellers := map[string]bool{}
	landings := landingSHAs(s.facts)
	type judged struct {
		sha   string
		value string
	}
	var matched []judged
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
		sha, ok := matchLanding(landings, l.change)
		if !ok {
			v.Unmatched++
			continue
		}
		if l.value != labelNoise {
			matched = append(matched, judged{sha: sha, value: l.value})
		}
	}
	v.Labellers = len(labellers)
	var ids []string
	in := s.input()
	for _, m := range allRanked() {
		ri, err := m.ranked.Clusters(in, domain.Scope{Kind: domain.ScopeAll})
		if err != nil {
			return nil, fmt.Errorf("scoring changes with %s: %w", m.def.ID, err)
		}
		score := changeScores(ri)
		a := report.MetricAUC{Metric: m.def.ID.String()}
		var pos, neg []float64
		for _, j := range matched {
			x, ok := score[j.sha]
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
			a.AUC = &auc
		}
		a.Beta = a.AUC == nil || *a.AUC < minAUC || a.N < minLabels
		v.Metrics = append(v.Metrics, a)
		ids = append(ids, a.Metric)
	}
	slices.SortFunc(v.Metrics, func(a, b report.MetricAUC) int { return cmp.Compare(a.Metric, b.Metric) })
	run := s.run
	v.Provenance = report.AnswerProvenance(&run, s.facts, s.forge, ids)
	v.Caveats = report.AnswerCaveats(s.facts)
	if v.Unmatched > 0 {
		what := fmt.Sprintf("%d labelled changes are not landings of run %d, or name more than one, and were left out", v.Unmatched, s.run.ID)
		if v.Unmatched == 1 {
			what = fmt.Sprintf("1 labelled change is not a landing of run %d, or names more than one, and was left out", s.run.ID)
		}
		v.Caveats = append(v.Caveats, report.Caveat{Code: "unmatched-labels", Message: what +
			": a change id is a landing SHA or an unambiguous prefix of one (pull request ids need the GitHub sync, M2)."})
	}
	return v, nil
}

// changeScores is a ranked metric's value for each change: its clusters'
// numerators over their denominators for a ratio, their sum for a sum.
// A merge landing can be several clusters, one per person it credits.
func changeScores(ri metrics.RankInput) map[string]float64 {
	num, den := map[string]float64{}, map[string]float64{}
	for _, cs := range ri.Data {
		for _, c := range cs {
			num[c.Change] += c.Num
			den[c.Change] += c.Den
		}
	}
	out := make(map[string]float64, len(num))
	for change, n := range num {
		if ri.Kind == rank.Ratio {
			if den[change] == 0 {
				continue
			}
			out[change] = n / den[change]
			continue
		}
		out[change] = n
	}
	return out
}

// landingSHAs lists every landing of the run, sorted.
func landingSHAs(fs *domain.FactSet) []string {
	var out []string
	for _, rf := range fs.Repos {
		for _, l := range rf.Landings {
			out = append(out, strings.ToLower(l.SHA))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// changeID reports whether s can name a change: a landing SHA or a prefix
// of at least 7 hex digits, lowercased.
func changeID(s string) bool {
	return len(s) >= 7 && strings.Trim(s, "0123456789abcdef") == ""
}

// matchLanding finds the one landing whose SHA starts with prefix.
func matchLanding(sorted []string, prefix string) (string, bool) {
	if !changeID(prefix) {
		return "", false
	}
	i, _ := slices.BinarySearch(sorted, prefix)
	if i >= len(sorted) || !strings.HasPrefix(sorted[i], prefix) {
		return "", false
	}
	if i+1 < len(sorted) && strings.HasPrefix(sorted[i+1], prefix) {
		return "", false
	}
	return sorted[i], true
}

// parseLabels reads a labels CSV: change id, label, and optionally the
// labeller, one change per row, with an optional header row and # comments.
// The first row is a header only when it names no change (its first field
// is not a change id) and gives no label, so a misspelt label on the first
// row is an error like on any other. A malformed file is a usage error that
// names the line.
func parseLabels(data []byte, name string) ([]label, error) {
	if name == "" {
		name = "labels"
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comment = '#'
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	var out []label
	for first := true; ; first = false {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("%s: %v", name, err)}
		}
		line, _ := r.FieldPos(0)
		if len(rec) < 2 {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("%s:%d: want change id, label and labeller", name, line)}
		}
		l := label{change: strings.ToLower(strings.TrimSpace(rec[0])), value: strings.ToLower(strings.TrimSpace(rec[1]))}
		if len(rec) > 2 {
			l.labeller = strings.TrimSpace(rec[2])
		}
		switch l.value {
		case labelMeaningful, labelRoutine, labelNoise:
		default:
			if first && !changeID(l.change) {
				continue // a header row, such as "change,label,labeller"
			}
			return nil, &domain.UsageError{Msg: fmt.Sprintf("%s:%d: label %q: want meaningful, routine or noise", name, line, rec[1])}
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, &domain.UsageError{Msg: name + ": no labelled changes"}
	}
	return out, nil
}
