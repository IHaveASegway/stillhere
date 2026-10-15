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
//   - by window, equally: each ranked metric scores the changes of its own
//     window (durable@1 90 to 365 days back, simplifying@1 and
//     reviews_given@1 the last 90), so each window gets half of the sample,
//     as many labels as the bar each of its metrics must clear;
//   - within a window, by repository and size, in proportion to the changes
//     each holds: the sample's mix of repositories and of small, medium and
//     large changes is the window's own, so an AUC over it describes the
//     changes the ranks are made of.
//
// The sample is a sequence, and a size is a prefix of it: within a window,
// each next change comes from the stratum furthest below its share (the
// Sainte-Laguë divisors 1, 3, 5, ..., so every prefix is as close to
// proportional as whole numbers allow), and the windows take turns. A
// labeller who wants 50 more asks for 50 more and keeps every label made;
// the same run, size and seed always give the same sample. Within a
// stratum, changes come in the order of a hash of the seed and their SHA:
// random, and the same on every platform and Go release.

// LabelRequest asks for a sample of a stored run's changes to label.
type LabelRequest struct {
	RunID      int64  // 0: the latest complete or partial scan run
	N          int    // the sample's size; 0 means DefaultLabelSample
	Seed       uint64 // with the run and N, decides the sample; a larger N extends the same sequence
	Labels     []byte // the labels file so far, read by the command line; nil when there is none
	LabelsName string // where Labels came from, for messages
}

// The sample's size: by default 100, 50 for each window, which is the
// number of labelled changes a ranked metric needs to lose its beta mark
// (minLabels); at most 10,000.
const (
	DefaultLabelSample = 2 * minLabels
	MaxLabelSample     = 10000
)

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

// labelWindow is one window the ranked metrics score changes in, with the
// metrics that read it.
type labelWindow struct {
	w       domain.Window
	ids     []string
	scorers []*metrics.Scorer
	scored  map[string]bool // the metrics that score a change of the pool in it
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

// Label returns a stratified sample of a stored run's changes to label,
// with the labels the labels file already holds for them.
func Label(ctx context.Context, d Deps, r LabelRequest) (*report.LabelSample, error) {
	d, err := d.ready("app.Label", false, false, true)
	if err != nil {
		return nil, err
	}
	n := r.N
	if n == 0 {
		n = DefaultLabelSample
	}
	if n < 1 || n > MaxLabelSample {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("a sample of %d changes: want 1 to %d", n, MaxLabelSample)}
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
	changes, err := metrics.Changes(in)
	if err != nil {
		return nil, fmt.Errorf("reading run %d's changes: %w", s.run.ID, err)
	}
	var windows []*labelWindow
	var ids []string
	for _, m := range allRanked() {
		sc, err := metrics.NewScorer(m.ranked, in)
		if err != nil {
			return nil, fmt.Errorf("scoring changes with %s: %w", m.def.ID, err)
		}
		id := m.def.ID.String()
		ids = append(ids, id)
		i := slices.IndexFunc(windows, func(w *labelWindow) bool {
			return w.w.Since.Equal(sc.Window().Since) && w.w.Until.Equal(sc.Window().Until)
		})
		if i < 0 {
			windows = append(windows, &labelWindow{w: sc.Window(), scored: map[string]bool{}})
			i = len(windows) - 1
		}
		windows[i].ids = append(windows[i].ids, id)
		windows[i].scorers = append(windows[i].scorers, sc)
	}
	slices.SortFunc(windows, func(a, b *labelWindow) int { return a.w.Since.Compare(b.w.Since) })

	cells := labelPool(changes, windows)
	pool := 0
	order := map[int]uint64{}
	for _, c := range cells {
		pool += len(c.members)
		for _, i := range c.members {
			order[i] = labelOrder(r.Seed, changes[i].Landings[0])
		}
		slices.SortFunc(c.members, func(a, b int) int {
			return cmp.Or(cmp.Compare(order[a], order[b]), cmp.Compare(changes[a].Landings[0], changes[b].Landings[0]))
		})
	}
	picked := labelSequence(cells, len(windows), n)

	// A change's label so far: a skip, unless a row labels it too.
	idx := newChangeIndex(changes)
	labelled := map[int]string{}
	for _, ref := range file.skipped {
		if i, ok := idx.resolve(ref); ok {
			labelled[i] = labelSkip
		}
	}
	for _, l := range file.labels {
		if i, ok := idx.resolve(l.ref); ok {
			labelled[i] = l.value
		}
	}

	sample := &report.LabelSample{Seed: r.Seed, Asked: n, Pool: pool}
	facts := labelFacts(s.facts)
	for _, p := range picked {
		c := &cells[p.cell]
		lc := facts.change(&changes[p.change], c.size, labelAgo(s.facts.Now, windows[c.window].w))
		lc.Label = labelled[p.change]
		if lc.Label != "" {
			sample.Labelled++
		}
		sample.Changes = append(sample.Changes, lc)
	}
	for _, c := range cells {
		w := windows[c.window]
		var scored []string
		for _, id := range w.ids {
			if w.scored[id] {
				scored = append(scored, id)
			}
		}
		sample.Strata = append(sample.Strata, report.LabelStratum{
			Window: report.Interval{Since: w.w.Since, Until: w.w.Until}, Ago: labelAgo(s.facts.Now, w.w), Metrics: scored,
			Size: c.size, Repo: facts.name(c.repo), RepoKey: c.repo, Pool: len(c.members), Sample: c.taken,
		})
	}
	run := s.run
	sample.Provenance = report.AnswerProvenance(&run, s.facts, s.forge, ids)
	sample.Caveats = append(report.AnswerCaveats(s.facts), labelCaveats(sample, windows, cells, n, s.facts.Now)...)
	return sample, nil
}

// labelPool sorts the run's changes that a ranked metric scores into
// strata. A bot's change is left out, as every rank leaves bots out, and a
// change goes to the first window, oldest first, whose metrics score it.
func labelPool(changes []metrics.Change, windows []*labelWindow) []labelCell {
	type key struct {
		window     int
		repo, size string
	}
	at := map[key]int{}
	var cells []labelCell
	for i := range changes {
		c := &changes[i]
		if c.Bot {
			continue
		}
		for wi, w := range windows {
			if !w.w.Contains(c.Time) {
				continue
			}
			in := false
			for j, sc := range w.scorers {
				if _, ok := sc.Score(c); ok {
					w.scored[w.ids[j]] = true
					in = true
				}
			}
			if !in {
				continue
			}
			k := key{wi, c.Repo, labelSize(c)}
			ci, ok := at[k]
			if !ok {
				ci = len(cells)
				at[k] = ci
				cells = append(cells, labelCell{window: wi, repo: c.Repo, size: k.size})
			}
			cells[ci].members = append(cells[ci].members, i)
			break
		}
	}
	sizeRank := func(s string) int { return slices.Index(report.LabelSizes, s) }
	slices.SortFunc(cells, func(a, b labelCell) int {
		return cmp.Or(cmp.Compare(a.window, b.window), cmp.Compare(sizeRank(a.size), sizeRank(b.size)), cmp.Compare(a.repo, b.repo))
	})
	return cells
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

// labelPick is one sampled change: its stratum and its index in the run's
// changes.
type labelPick struct{ cell, change int }

// labelSequence draws up to n changes from the strata: the windows take
// turns, and within a window each next change comes from the stratum with
// the most changes per (2·taken + 1), the Sainte-Laguë divisors, the first
// such stratum on a tie. Each stratum gives its changes in their order.
func labelSequence(cells []labelCell, windows, n int) []labelPick {
	var out []labelPick
	for len(out) < n {
		drew := false
		for w := 0; w < windows && len(out) < n; w++ {
			best := -1
			for i := range cells {
				c := &cells[i]
				if c.window != w || c.taken == len(c.members) {
					continue
				}
				// pool / (2·taken + 1) is larger: compare the cross products, in integers.
				if best < 0 || len(c.members)*(2*cells[best].taken+1) > len(cells[best].members)*(2*c.taken+1) {
					best = i
				}
			}
			if best < 0 {
				continue
			}
			c := &cells[best]
			out = append(out, labelPick{cell: best, change: c.members[c.taken]})
			c.taken++
			drew = true
		}
		if !drew {
			break
		}
	}
	return out
}

// labelAgo names a window by its days before now: "0–90 days ago".
func labelAgo(now time.Time, w domain.Window) string {
	day := 24 * time.Hour
	return fmt.Sprintf("%d–%d days ago", int(now.Sub(w.Until)/day), int(now.Sub(w.Since)/day))
}

// labelCaveats says when the sample is smaller than asked, and when a
// window holds fewer labels than its metrics need to lose their beta mark.
func labelCaveats(s *report.LabelSample, windows []*labelWindow, cells []labelCell, n int, now time.Time) []report.Caveat {
	var out []report.Caveat
	if len(s.Changes) < n {
		out = append(out, report.Caveat{Code: "small-sample", Message: fmt.Sprintf("The run holds %d changes that a ranked "+
			"metric scores, a person's, fewer than the %d asked: the sample is all of them.", s.Pool, n)})
	}
	for wi, w := range windows {
		taken := 0
		for _, c := range cells {
			if c.window == wi {
				taken += c.taken
			}
		}
		if taken < minLabels && len(w.scored) > 0 {
			var scored []string
			for _, id := range w.ids {
				if w.scored[id] {
					scored = append(scored, id)
				}
			}
			verb := "score"
			if len(scored) == 1 {
				verb = "scores"
			}
			out = append(out, report.Caveat{Code: "small-sample", Message: fmt.Sprintf("%d of the sampled changes landed %s, "+
				"where %s %s changes: fewer than the %d labelled meaningful or routine that a metric needs to lose its beta mark.",
				taken, labelAgo(now, w.w), strings.Join(scored, " and "), verb, minLabels)})
		}
	}
	return out
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
		// The commits a merge brought in, oldest first; the landing's own
		// subject is the title.
		for _, k := range slices.Backward(l.Commits) {
			if k == sha {
				continue
			}
			if cm := r.commits[k]; cm != nil && len(cm.Parents) < 2 && labelBlind(cm.Subject) != lc.Title {
				if len(lc.Commits) < labelMaxCommits {
					lc.Commits = append(lc.Commits, labelBlind(cm.Subject))
				} else {
					lc.MoreCommits++
				}
			}
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
