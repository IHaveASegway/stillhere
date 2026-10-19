package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// stillhere label (docs/validation.md, ADR 0014): a sample of a stored
// run's changes for a person to label meaningful, routine or noise, which
// validate then scores every ranked metric against. The sample is drawn
// from the changes a ranked metric scores at its own grain (metrics.Scorer),
// so no label is spent on a change no metric can be checked with, and from
// people's changes only: a bot's are never ranked. It is stratified (W. G.
// Cochran, Sampling Techniques, 3rd ed., 1977, ch. 5):
//
//   - by window: each ranked metric scores the changes of its own window
//     (durable@1 90 to 365 days back, simplifying@1 and reviews_given@1 the
//     last 90), so each window gets its own part of the sample, as many
//     labels as the bar each of its metrics must clear;
//   - within a window, by repository and size, in proportion to the changes
//     each holds: the sample's mix of repositories and of small, medium and
//     large changes is the window's own, so an AUC over it describes the
//     changes the ranks are made of.
//
// Each window's changes are one sequence, and a window's part of a sample
// is a prefix of it: each next change comes from the stratum furthest below
// its share (the Sainte-Laguë divisors 1, 3, 5, ..., so every prefix is as
// close to proportional as whole numbers allow). A sample of a size the
// labeller asks for (--n) lets the windows take turns; by default each
// window's part runs until every metric of the window scores 50 of its
// changes that the labeller has not answered noise or skip (labelUntil), so
// answering noise or skip draws more next time. Either way a larger sample
// extends a smaller one and keeps every label made, and the same run, size,
// seed and labels always give the same sample. Within a stratum, changes
// come in the order of a hash of the seed and their SHA: random, and the
// same on every platform and Go release.

// LabelRequest asks for a sample of a stored run's changes to label.
type LabelRequest struct {
	RunID      int64  // 0: the latest complete or partial scan run
	N          int    // the sample's size; 0: until the bar (labelUntil)
	Seed       uint64 // with the run, N and the labels so far, decides the sample; a larger sample extends a smaller one
	Labels     []byte // the labels file so far, read by the command line; nil when there is none
	LabelsName string // where Labels came from, for messages
	Labeller   string // whose labels the sample resumes: the rows of this labeller, "" for the rows that name none
}

// MaxLabelSample is the most changes a sample asked for may hold.
const MaxLabelSample = 10000

// labelWindowMost is the most changes the default sample takes from one
// window: twice the bar, so a window whose metrics cannot reach the bar,
// such as reviews_given@1 where few changes are pull requests, asks no
// more than that unless --n does.
const labelWindowMost = 2 * minLabels

// The size strata, in lines a change's own diffs add and remove on paths
// with a role (metrics.Change): small below 10, large from 100.
const (
	labelMediumFrom = 10
	labelLargeFrom  = 100
)

// labelMergeFrom is the subject GitHub's merge button writes, "Merge pull
// request #950 from owner/branch", whose owner is the login of the
// branch's author: 433 of cli/cli's 440 merge landings of 2026 carry it.
var labelMergeFrom = regexp.MustCompile(`^(Merge pull request #[0-9]+) from \S+$`)

// labelBlind is a subject as a blinded sample shows it: without the login
// a GitHub merge subject names. Other text is the change's own; a subject
// that names its author some other way still does.
func labelBlind(subject string) string {
	return labelMergeFrom.ReplaceAllString(subject, "$1")
}

// The most files and brought-in commits a sampled change lists.
const (
	labelMaxFiles   = 40
	labelMaxCommits = 10
)

// labelMetric is one ranked metric, with its scorer and its window.
type labelMetric struct {
	id     string
	forge  bool
	scorer *metrics.Scorer
	window int // index into the windows
}

// labelWindow is one window the ranked metrics score changes in.
type labelWindow struct {
	w       domain.Window
	metrics []int // indexes into the metrics, in registry order
}

// labelCell is one stratum: a window, a repository and a size, with the
// pool's changes in it, in their sampling order.
type labelCell struct {
	window  int
	repo    string // Repo.Key
	size    string
	members []int // indexes into the run's changes
	taken   int
}

// labelPick is one sampled change: its stratum and its index in the run's
// changes.
type labelPick struct{ cell, change int }

// labelRun is what a sample is drawn from: the run's changes, the ranked
// metrics that score them, and the strata of the pool.
type labelRun struct {
	changes  []metrics.Change
	metrics  []labelMetric
	windows  []labelWindow
	cells    []labelCell
	scoredBy map[int][]int // a pool change → the metrics that score it
	scores   []bool        // metric → it scores a change of the pool
	pool     int
}

// Label returns a stratified sample of a stored run's changes to label,
// with the labels the labels file already holds for them.
func Label(ctx context.Context, d Deps, r LabelRequest) (*report.LabelSample, error) {
	d, err := d.ready("app.Label", false, false, true)
	if err != nil {
		return nil, err
	}
	if r.N < 0 || r.N > MaxLabelSample {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("a sample of %d changes: want 1 to %d", r.N, MaxLabelSample)}
	}
	file := &labelsFile{}
	if len(r.Labels) > 0 {
		if file, err = readLabels(r.Labels, r.LabelsName); err != nil {
			return nil, err
		}
	}
	s, err := load(ctx, d, r.RunID)
	if err != nil {
		return nil, err
	}
	in := s.input()
	lr := &labelRun{scoredBy: map[int][]int{}}
	if lr.changes, err = metrics.Changes(in); err != nil {
		return nil, fmt.Errorf("reading run %d's changes: %w", s.run.ID, err)
	}
	var ids []string
	for _, m := range allRanked() {
		sc, err := metrics.NewScorer(m.ranked, in)
		if err != nil {
			return nil, fmt.Errorf("scoring changes with %s: %w", m.def.ID, err)
		}
		lm := labelMetric{id: m.def.ID.String(), forge: m.def.Forge, scorer: sc, window: -1}
		for wi, w := range lr.windows {
			if w.w.Since.Equal(sc.Window().Since) && w.w.Until.Equal(sc.Window().Until) {
				lm.window = wi
			}
		}
		if lm.window < 0 {
			lm.window = len(lr.windows)
			lr.windows = append(lr.windows, labelWindow{w: sc.Window()})
		}
		lr.windows[lm.window].metrics = append(lr.windows[lm.window].metrics, len(lr.metrics))
		lr.metrics = append(lr.metrics, lm)
		ids = append(ids, lm.id)
	}
	lr.sortWindows()
	lr.strata(r.Seed)

	// The labeller's label of a change so far: a skip, unless a row labels
	// it too; another labeller's rows are theirs.
	idx := newChangeIndex(lr.changes)
	labelled := map[int]string{}
	others := map[string]bool{}
	for _, l := range file.skipped {
		if l.labeller != r.Labeller {
			others[l.labeller] = true
		} else if i, ok := idx.resolve(l.ref); ok {
			labelled[i] = labelSkip
		}
	}
	for _, l := range file.labels {
		if l.labeller != r.Labeller {
			others[l.labeller] = true
		} else if i, ok := idx.resolve(l.ref); ok {
			labelled[i] = l.value
		}
	}

	var picked []labelPick
	if r.N > 0 {
		picked = lr.sequence(r.N, nil)
	} else {
		picked = lr.sequence(MaxLabelSample, func(w int, order []labelPick) int { return lr.until(w, order, labelled) })
	}
	sample := &report.LabelSample{Seed: r.Seed, Asked: r.N, Pool: lr.pool, Labeller: r.Labeller, OtherLabellers: len(others)}
	facts := labelFacts(s.facts)
	for _, p := range picked {
		c := &lr.cells[p.cell]
		c.taken++
		lc := facts.change(&lr.changes[p.change], c.size, labelAgo(s.facts.Now, lr.windows[c.window].w))
		lc.Label = labelled[p.change]
		if lc.Label != "" {
			sample.Labelled++
		}
		sample.Changes = append(sample.Changes, lc)
	}
	for _, c := range lr.cells {
		w := lr.windows[c.window]
		sample.Strata = append(sample.Strata, report.LabelStratum{
			Window: report.Interval{Since: w.w.Since, Until: w.w.Until}, Ago: labelAgo(s.facts.Now, w.w), Metrics: lr.scoring(c.window),
			Size: c.size, Repo: facts.name(c.repo), RepoKey: c.repo, Pool: len(c.members), Sample: c.taken,
		})
	}
	sample.Progress = lr.progress(picked, labelled, s.facts.Now)
	run := s.run
	sample.Provenance = report.AnswerProvenance(&run, s.facts, s.forge, ids)
	sample.Caveats = report.AnswerCaveats(s.facts, s.who)
	for j, m := range lr.metrics {
		if m.forge && !lr.scores[j] {
			sample.Caveats = append(sample.Caveats, report.ForgeCaveatsAlone(&run, s.facts, s.forge, m.id)...)
		}
	}
	sample.Caveats = append(sample.Caveats, lr.caveats(sample, r.N, labelled, s.run.ID, s.facts.Now)...)
	return sample, nil
}

// sortWindows puts the windows oldest first, keeping each metric's.
func (lr *labelRun) sortWindows() {
	order := make([]int, len(lr.windows))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return lr.windows[a].w.Since.Compare(lr.windows[b].w.Since) })
	at := make([]int, len(order)) // old index → new
	windows := make([]labelWindow, len(order))
	for to, from := range order {
		at[from] = to
		windows[to] = lr.windows[from]
	}
	lr.windows = windows
	for i := range lr.metrics {
		lr.metrics[i].window = at[lr.metrics[i].window]
	}
}

// strata sorts the run's changes that a ranked metric scores into strata,
// each in its random order. A bot's change is left out, as every rank
// leaves bots out, and a change goes to the first window, oldest first,
// whose metrics score it.
func (lr *labelRun) strata(seed uint64) {
	lr.scores = make([]bool, len(lr.metrics))
	type key struct {
		window     int
		repo, size string
	}
	at := map[key]int{}
	for i := range lr.changes {
		c := &lr.changes[i]
		if c.Bot {
			continue
		}
		for wi, w := range lr.windows {
			if !w.w.Contains(c.Time) {
				continue
			}
			var by []int
			for _, j := range w.metrics {
				if _, ok := lr.metrics[j].scorer.Score(c); ok {
					by = append(by, j)
				}
			}
			if len(by) == 0 {
				continue
			}
			lr.scoredBy[i] = by
			for _, j := range by {
				lr.scores[j] = true
			}
			k := key{wi, c.Repo, labelSize(c)}
			ci, ok := at[k]
			if !ok {
				ci = len(lr.cells)
				at[k] = ci
				lr.cells = append(lr.cells, labelCell{window: wi, repo: c.Repo, size: k.size})
			}
			lr.cells[ci].members = append(lr.cells[ci].members, i)
			break
		}
	}
	sizeRank := func(s string) int { return slices.Index(report.LabelSizes, s) }
	slices.SortFunc(lr.cells, func(a, b labelCell) int {
		return cmp.Or(cmp.Compare(a.window, b.window), cmp.Compare(sizeRank(a.size), sizeRank(b.size)), cmp.Compare(a.repo, b.repo))
	})
	order := map[int]uint64{}
	for ci := range lr.cells {
		c := &lr.cells[ci]
		lr.pool += len(c.members)
		for _, i := range c.members {
			order[i] = labelOrder(seed, lr.changes[i].Landings[0])
		}
		slices.SortFunc(c.members, func(a, b int) int {
			return cmp.Or(cmp.Compare(order[a], order[b]), cmp.Compare(lr.changes[a].Landings[0], lr.changes[b].Landings[0]))
		})
	}
}

// labelSize is a change's size stratum.
func labelSize(c *metrics.Change) string {
	switch lines := c.Added + c.Removed; {
	case lines >= labelLargeFrom:
		return report.LabelLarge
	case lines >= labelMediumFrom:
		return report.LabelMedium
	}
	return report.LabelSmall
}

// labelOrder is a change's place in its stratum's random order: the first 8
// bytes of a SHA-256 over the seed and its first landing's SHA.
func labelOrder(seed uint64, sha string) uint64 {
	b := binary.BigEndian.AppendUint64([]byte("stillhere/label/v1\x00"), seed)
	sum := sha256.Sum256(append(b, sha...))
	return binary.BigEndian.Uint64(sum[:8])
}

// windowOrder is window w's whole sequence: each next change comes from the
// stratum with the most changes per (2·taken + 1), the Sainte-Laguë
// divisors, the first such stratum on a tie. Each stratum gives its changes
// in their order.
func (lr *labelRun) windowOrder(w int) []labelPick {
	taken := make([]int, len(lr.cells))
	var out []labelPick
	for {
		best := -1
		for i := range lr.cells {
			c := &lr.cells[i]
			if c.window != w || taken[i] == len(c.members) {
				continue
			}
			// pool / (2·taken + 1) is larger: compare the cross products, in integers.
			if best < 0 || len(c.members)*(2*taken[best]+1) > len(lr.cells[best].members)*(2*taken[i]+1) {
				best = i
			}
		}
		if best < 0 {
			return out
		}
		out = append(out, labelPick{cell: best, change: lr.cells[best].members[taken[best]]})
		taken[best]++
	}
}

// sequence draws up to n changes, the windows taking turns, from each
// window's sequence, or from the prefix of it that size gives when size is
// not nil.
func (lr *labelRun) sequence(n int, size func(w int, order []labelPick) int) []labelPick {
	orders := make([][]labelPick, len(lr.windows))
	for w := range lr.windows {
		orders[w] = lr.windowOrder(w)
		if size != nil {
			orders[w] = orders[w][:size(w, orders[w])]
		}
	}
	var out []labelPick
	for k := 0; len(out) < n; k++ {
		drew := false
		for w := range orders {
			if k < len(orders[w]) && len(out) < n {
				out = append(out, orders[w][k])
				drew = true
			}
		}
		if !drew {
			break
		}
	}
	return out
}

// until is the default size of window w's part of the sample: the shortest
// prefix of its sequence in which each of its metrics scores minLabels
// changes the labeller has not answered noise or skip, or every such change
// of the window, and at most labelWindowMost.
func (lr *labelRun) until(w int, order []labelPick, labelled map[int]string) int {
	open := func(i int) bool { return labelled[i] != labelNoise && labelled[i] != labelSkip }
	avail := map[int]int{} // metric → the window's changes it scores that are open
	for _, p := range order {
		if open(p.change) {
			for _, j := range lr.scoredBy[p.change] {
				avail[j]++
			}
		}
	}
	count := map[int]int{}
	done := func() bool {
		for _, j := range lr.windows[w].metrics {
			if count[j] < min(minLabels, avail[j]) {
				return false
			}
		}
		return true
	}
	k := 0
	for ; k < len(order) && k < labelWindowMost && !done(); k++ {
		if i := order[k].change; open(i) {
			for _, j := range lr.scoredBy[i] {
				count[j]++
			}
		}
	}
	return k
}

// scoring is the metrics of window w that score a change of the pool.
func (lr *labelRun) scoring(w int) []string {
	var out []string
	for _, j := range lr.windows[w].metrics {
		if lr.scores[j] {
			out = append(out, lr.metrics[j].id)
		}
	}
	return out
}

// progress is, per ranked metric, how far the labeller's labels are toward
// the bar: the changes labelled meaningful or routine that it scores, and
// the sampled changes it scores that are not answered noise or skip.
func (lr *labelRun) progress(picked []labelPick, labelled map[int]string, now time.Time) []report.LabelProgress {
	out := make([]report.LabelProgress, len(lr.metrics))
	for j, m := range lr.metrics {
		out[j] = report.LabelProgress{Metric: m.id, Ago: labelAgo(now, lr.windows[m.window].w)}
	}
	for i, value := range labelled {
		if value != labelMeaningful && value != labelRoutine {
			continue
		}
		for j, m := range lr.metrics {
			if _, ok := m.scorer.Score(&lr.changes[i]); ok {
				out[j].Labelled++
			}
		}
	}
	for _, p := range picked {
		if v := labelled[p.change]; v == labelNoise || v == labelSkip {
			continue
		}
		for _, j := range lr.scoredBy[p.change] {
			out[j].Sampled++
		}
	}
	for j := range out {
		out[j].Needed = max(0, minLabels-out[j].Labelled)
	}
	slices.SortFunc(out, func(a, b report.LabelProgress) int { return cmp.Compare(a.Metric, b.Metric) })
	return out
}

// labelAgo names a window by its days before now: "0–90 days ago".
func labelAgo(now time.Time, w domain.Window) string {
	day := 24 * time.Hour
	return fmt.Sprintf("%d–%d days ago", int(now.Sub(w.Until)/day), int(now.Sub(w.Since)/day))
}

// caveats says why the run holds no change to label, which metric scores
// none of them and why, when the sample is smaller than asked, and when a
// metric's part of it is below the bar.
func (lr *labelRun) caveats(s *report.LabelSample, n int, labelled map[int]string, run int64, now time.Time) []report.Caveat {
	var out []report.Caveat
	if lr.pool == 0 {
		out = append(out, lr.nothing(run, now))
	}
	for j, m := range lr.metrics {
		if !lr.scores[j] {
			out = append(out, report.Caveat{Code: "unscored-changes", Message: fmt.Sprintf("%s scores none of the "+
				"people's changes that landed %s: it scores %s.", m.id, labelAgo(now, lr.windows[m.window].w),
				m.scorer.Scores())})
		}
	}
	if lr.pool == 0 {
		return out
	}
	if n > 0 && len(s.Changes) < n {
		out = append(out, report.Caveat{Code: "small-sample", Message: fmt.Sprintf("The run holds %s that a ranked "+
			"metric scores, a person's, fewer than the %d asked: the sample is all of them.",
			nounCount(lr.pool, "change", "changes"), n)})
	}
	for j, m := range lr.metrics {
		i := slices.IndexFunc(s.Progress, func(p report.LabelProgress) bool { return p.Metric == m.id })
		if i < 0 || !lr.scores[j] {
			continue
		}
		p := s.Progress[i]
		if p.Sampled >= minLabels || p.Labelled >= minLabels {
			continue
		}
		open := 0 // the pool's changes it scores that the labeller has not answered noise or skip
		for c, by := range lr.scoredBy {
			if slices.Contains(by, j) && labelled[c] != labelNoise && labelled[c] != labelSkip {
				open++
			}
		}
		why := "The run holds no more of them."
		switch {
		case open > p.Sampled && n > 0:
			why = "A larger --n draws more."
		case open > p.Sampled:
			why = fmt.Sprintf("The sample takes at most %d changes from a window unless --n asks for more.", labelWindowMost)
		}
		out = append(out, report.Caveat{Code: "small-sample", Message: fmt.Sprintf("%s scores %s of the sample "+
			"(of those that landed %s, less any answered noise or skip): fewer than the %d meaningful or routine labels "+
			"its bar needs. %s", m.id, nounCount(p.Sampled, "change", "changes"), p.Ago, minLabels, why)})
	}
	return out
}

// nothing says why the run holds no change to label: how many changes
// landed in each window, and how many of them are a bot's.
func (lr *labelRun) nothing(run int64, now time.Time) report.Caveat {
	var since, until time.Time
	var parts []string
	for wi, w := range lr.windows {
		if wi == 0 || w.w.Since.Before(since) {
			since = w.w.Since
		}
		if wi == 0 || w.w.Until.After(until) {
			until = w.w.Until
		}
		landed, bots := 0, 0
		for i := range lr.changes {
			if w.w.Contains(lr.changes[i].Time) {
				landed++
				if lr.changes[i].Bot {
					bots++
				}
			}
		}
		part := fmt.Sprintf("%s landed %s", nounCount(landed, "change", "changes"), labelAgo(now, w.w))
		if landed == 0 {
			part = "no change landed " + labelAgo(now, w.w)
		}
		if bots > 0 {
			part += fmt.Sprintf(", %d of them a bot's", bots)
		}
		parts = append(parts, part)
	}
	return report.Caveat{Code: "nothing-to-label", Message: fmt.Sprintf("Run %d holds no change to label: no person's "+
		"change that landed %s is one a ranked metric scores (%s). The caveats below say what each metric scores.",
		run, labelAgo(now, domain.Window{Since: since, Until: until}), strings.Join(parts, "; "))}
}

// labelFactsIndex finds what a sampled change shows: its repository's
// name, its landings' commits, files and classes.
type labelFactsIndex struct {
	repos map[string]*labelRepo
}

type labelRepo struct {
	rf       *domain.RepoFacts
	commits  map[string]*domain.Commit
	landings map[string]*domain.Landing
	changes  map[string][]*domain.FileChange
	classes  map[string]domain.PathClass
}

func labelFacts(fs *domain.FactSet) *labelFactsIndex {
	x := &labelFactsIndex{repos: map[string]*labelRepo{}}
	for i := range fs.Repos {
		x.repos[fs.Repos[i].Repo.Key] = &labelRepo{rf: &fs.Repos[i]}
	}
	return x
}

func (x *labelFactsIndex) name(key string) string {
	if r := x.repos[key]; r != nil {
		return r.rf.Repo.Name
	}
	return key
}

// repo indexes one repository's facts the first time a sampled change
// needs them.
func (x *labelFactsIndex) repo(key string) *labelRepo {
	r := x.repos[key]
	if r == nil || r.commits != nil {
		return r
	}
	f := r.rf
	r.commits = make(map[string]*domain.Commit, len(f.Commits))
	for i := range f.Commits {
		r.commits[f.Commits[i].SHA] = &f.Commits[i]
	}
	r.landings = make(map[string]*domain.Landing, len(f.Landings))
	for i := range f.Landings {
		r.landings[f.Landings[i].SHA] = &f.Landings[i]
	}
	r.changes = map[string][]*domain.FileChange{}
	for i := range f.Changes {
		r.changes[f.Changes[i].Commit] = append(r.changes[f.Changes[i].Commit], &f.Changes[i])
	}
	r.classes = make(map[string]domain.PathClass, len(f.Paths))
	for _, pc := range f.Paths {
		r.classes[pc.Path] = pc
	}
	return r
}

// change is c as its labeller sees it: what it is, where it landed and
// what it touched, and nothing any metric made of it, nor who wrote it.
func (x *labelFactsIndex) change(c *metrics.Change, size, ago string) report.LabelChange {
	lc := report.LabelChange{Change: c.Landings[0], Repo: x.name(c.Repo), RepoKey: c.Repo, Landings: c.Landings,
		Landed: c.Time, Ago: ago, Size: size, Added: c.Added, Removed: c.Removed}
	if c.Pull != nil {
		lc.Pull = &report.LabelPull{Repo: c.Pull.Repo, Number: c.Pull.Number}
		lc.Title = c.Pull.Title
	}
	r := x.repo(c.Repo)
	if r == nil {
		return lc
	}
	if lc.Title == "" {
		if k := r.commits[c.Landings[0]]; k != nil {
			lc.Title = labelBlind(k.Subject)
		}
	}
	files := map[string]*report.LabelFile{}
	var brought []*domain.Commit // the commits it brought in, below
	for _, sha := range c.Landings {
		for _, fc := range r.changes[sha] {
			f := files[fc.Path]
			if f == nil {
				f = &report.LabelFile{Path: fc.Path, OldPath: fc.OldPath}
				if pc, ok := r.classes[fc.Path]; ok {
					f.Class = string(pc.Role)
					if f.Class == "" {
						f.Class = string(pc.Skip)
					}
				}
				files[fc.Path] = f
			}
			f.Added += fc.Added
			f.Removed += fc.Removed
			f.Binary = f.Binary || fc.Binary
		}
		l := r.landings[sha]
		if l == nil {
			continue
		}
		// The commits a merge brought in, and each landing of a change of
		// several: a rebase merge puts each of its commits on the branch as
		// a landing of its own. The landing's own subject is the title.
		for _, k := range l.Commits {
			if cm := r.commits[k]; (k != sha || len(c.Landings) > 1) && cm != nil && len(cm.Parents) < 2 && labelBlind(cm.Subject) != lc.Title {
				brought = append(brought, cm)
			}
		}
	}
	// Oldest first. A rebase merge's landings come newest first
	// (metrics.Change.Landings), one commit each, so reversed they are in
	// the branch's order; a merge's commits by committer time
	// (Landing.Commits is sorted by SHA), then by author time, which a
	// rebase that gave them one committer time kept in order.
	if len(c.Landings) > 1 {
		slices.Reverse(brought)
	} else {
		slices.SortFunc(brought, func(a, b *domain.Commit) int {
			return cmp.Or(a.CommitTime.Compare(b.CommitTime), a.AuthorTime.Compare(b.AuthorTime), cmp.Compare(a.SHA, b.SHA))
		})
	}
	for _, cm := range brought {
		if len(lc.Commits) < labelMaxCommits {
			lc.Commits = append(lc.Commits, labelBlind(cm.Subject))
		} else {
			lc.MoreCommits++
		}
	}
	for _, f := range files {
		lc.Files = append(lc.Files, *f)
	}
	slices.SortFunc(lc.Files, func(a, b report.LabelFile) int {
		return cmp.Or(cmp.Compare(b.Added+b.Removed, a.Added+a.Removed), cmp.Compare(a.Path, b.Path))
	})
	if len(lc.Files) > labelMaxFiles {
		lc.MoreFiles = len(lc.Files) - labelMaxFiles
		lc.Files = lc.Files[:labelMaxFiles]
	}
	return lc
}
