package collect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// HistoryMode selects how much history collects.
type HistoryMode int

// The history modes.
const (
	// HistoryWeek collects the first-parent landings in the window with
	// numstat: what week prints.
	HistoryWeek HistoryMode = iota
	// HistoryScan adds the landing map, the commits each landing brought
	// in, every commit's own non-blank added and removed lines, reverts and
	// the repository's first commit: what scan stores.
	HistoryScan
)

// HistoryOptions says which landings to collect and how much about them.
type HistoryOptions struct {
	// Window is the landing window, [Since, Until) by committer time: the
	// week for HistoryWeek, and for HistoryScan the 365 days before --now,
	// where durable@1's cohort starts.
	Window domain.Window
	Mode   HistoryMode
}

// HistoryFacts is what History learned about one repository. Every slice is
// nil when empty and sorted by the key its comment names, so the facts are
// deterministic and a store can return them in the same order. Two slices
// keep git's order instead, because the order carries meaning:
// Commit.Parents (the first parent first) and Commit.CoAuthors (trailer
// order, which credit reads).
type HistoryFacts struct {
	// FirstCommit is the repository's age: the author time of the earliest
	// root commit reachable from Head (git log --max-parents=0
	// --format=%at <head>; a repository can have several roots). It is
	// zero when unknown, such as in a shallow clone, whose boundary
	// commits are not roots, and in week runs, which do not need it.
	FirstCommit time.Time
	// Commits holds the landing commits and, in scan runs, every commit a
	// landing brought in, sorted by SHA. Co-authors are canonicalised with
	// git check-mailmap in both modes.
	Commits []domain.Commit
	// Landings are the first-parent commits of Head whose committer time
	// is in the window, sorted by SHA. Landing.Commits, the landing map,
	// is collected in scan runs only and is nil in week runs.
	Landings []domain.Landing
	// Changes holds one row per commit and path that the commit's diff
	// touched, sorted by Commit, then Path. Added and Removed are numstat
	// against the first parent: a merge landing's first-parent diff, as
	// week counts it, and a root commit's diff against the empty tree.
	// AddedNonBlank and RemovedNonBlank come from the commit's own -U0 -w
	// patch in scan runs, which is empty for a merge, and are -1 in week
	// runs. A partial clone has no rows: its blobs are not local.
	Changes []domain.FileChange
	// Reverts links each revert landing in the window to the commit its
	// revert names (scan runs only), sorted by SHA, then Reverted. A revert
	// landing is a landing whose map holds a commit whose message carries
	// "This reverts commit <sha>.": the landing itself, or, for a revert
	// merged with a merge commit, the side-branch commit the merge brought
	// in. So SHA is always a landing, and a landing that brought in both a
	// commit and its revert names a commit of its own map.
	Reverts []domain.Revert
	// Warnings holds shallow-window when a shallow clone's history ends
	// inside what the window needs, sorted by Code, then Message.
	Warnings []domain.Warning
}

// History walks the first-parent history of repo.Head over opt.Window and
// streams git's output into facts (docs/spec/week-collector.md).
//
// Every git command names commits by SHA (repo.Head, or SHAs git printed),
// never by branch, and pins what the repository's own config would change
// in what it prints: -M and -l1000 for rename detection, --root for the
// root commit's diff, --diff-algorithm=myers, --no-textconv (no program the
// repository names runs, and lines are counted on stored content),
// --ignore-submodules=none and --submodule=short for gitlinks, --no-color,
// --default-prefix and --encoding=UTF-8, and, as -c settings ahead of the
// subcommand, core.bigFileThreshold, core.commentChar and
// trailer.separators (historyConfig). Numstat is read with -z, so every
// path, including a rename's two paths, arrives exactly as stored, however
// odd it is.
//
// In HistoryWeek it runs one first-parent log. HistoryScan adds:
//
//   - the landing map (git rev-list L^1..L for every landing L), computed
//     from one rev-list --parents walk for all landings rather than one
//     process per merge;
//   - the commits merge landings brought in, with their numstat, from one
//     git log --no-walk --stdin;
//   - every non-merge commit's own -U0 -w patch, from one git log
//     --no-walk --stdin -p over the mapped commits, never --first-parent,
//     under which git prints a merge's first-parent diff as the merger's;
//   - the revert query over the same mapped commits, so a revert that a
//     merge brought in is found too, and the repository's first commit.
//
// Co-authors are canonicalised with one git check-mailmap --stdin in both
// modes. A partial clone gets commit facts only. In a shallow clone, a
// parentless commit is the clone's boundary, whose diff against the empty
// tree is not its own change: it keeps its commit facts, gets no Changes,
// and the facts carry warning shallow-window when the boundary is inside
// what the window needs.
func History(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt HistoryOptions) (*HistoryFacts, error) {
	if err := historyCheck(repo, opt); err != nil {
		return nil, fmt.Errorf("collect.History: %w", err)
	}
	h := &historyRun{
		git:     git,
		repo:    repo,
		opt:     opt,
		scan:    opt.Mode == HistoryScan,
		lines:   !repo.Partial,
		commits: make(map[string]*historyCommit),
		changes: make(map[historyChangeKey]*domain.FileChange),
	}
	if err := h.run(ctx); err != nil {
		return nil, fmt.Errorf("collect.History: %s: %w", repo.Name, err)
	}
	return h.facts(), nil
}

// historyCheck rejects options no caller should pass. Head must be a full
// SHA because every command names it as a revision, where a value that
// starts with "-" would be read as an option.
func historyCheck(repo domain.Repo, opt HistoryOptions) error {
	switch {
	case repo.Path == "":
		return errors.New("repository path is empty")
	case !historySHA(repo.Head):
		return errors.New("repository head is not a full commit SHA")
	case opt.Mode != HistoryWeek && opt.Mode != HistoryScan:
		return fmt.Errorf("unknown history mode %d", opt.Mode)
	case !opt.Window.Since.Before(opt.Window.Until):
		return errors.New("the window is empty: Since must be before Until")
	}
	return nil
}

// historyRun is one repository's history collection: the facts collected
// so far, keyed for the steps that join them.
type historyRun struct {
	git   domain.GitRunner
	repo  domain.Repo
	opt   HistoryOptions
	scan  bool // HistoryScan
	lines bool // numstat and patches; false in a partial clone

	commits  map[string]*historyCommit // every commit collected, by SHA
	landings []string                  // the window's landings, in first-parent order from Head
	maps     map[string][]string       // landing SHA → the commits it brought in, sorted (scan)
	changes  map[historyChangeKey]*domain.FileChange
	cut      []string // parentless commits of a shallow clone: its boundary
	reverts  []domain.Revert
	first    time.Time
	warnings []domain.Warning
}

// historyCommit is a commit with its co-authors as its trailers wrote them,
// before git check-mailmap canonicalises them.
type historyCommit struct {
	commit    domain.Commit
	coAuthors []domain.Person
}

// historyChangeKey identifies a FileChange: a commit and a destination path.
type historyChangeKey struct{ commit, path string }

// run collects the facts, one git command at a time. A repository is
// collected sequentially on purpose: the caller already runs repositories
// in parallel under a bound, so concurrency here would multiply the number
// of git processes past it.
func (h *historyRun) run(ctx context.Context) error {
	if err := h.readLandings(ctx); err != nil {
		return err
	}
	if h.scan {
		if err := h.mapLandings(ctx); err != nil {
			return err
		}
		if err := h.readBroughtIn(ctx); err != nil {
			return err
		}
		if h.lines {
			if err := h.readPatches(ctx); err != nil {
				return err
			}
		}
		if err := h.readReverts(ctx); err != nil {
			return err
		}
	}
	if h.scan || h.repo.Shallow {
		if err := h.readRoots(ctx); err != nil {
			return err
		}
	}
	return h.canonicalise(ctx)
}

// historyConfig pins the repository settings that change the diff or the
// trailers a log prints and that no log option overrides. A -c ahead of the
// subcommand beats the repository's own config, and each value here is
// git's default (each verified on 2.50.1 by setting it in the repository's
// config). Above core.bigFileThreshold every blob is binary, with no
// numstat counts and no patch lines. git's trailer parser skips comment
// lines, so core.commentChar=C (or core.commentString, the same setting)
// hides every Co-authored-by line, and without ":" in trailer.separators
// no line is a trailer at all. Every history log that prints a diff or
// trailers starts with these; domain.GitSubcommand skips the -c pairs.
func historyConfig() []string {
	return []string{
		"-c", "core.bigFileThreshold=512m",
		"-c", "core.commentChar=#",
		"-c", "trailer.separators=:",
	}
}

// historyDiffArgs pin the diff that numstat and the own patches count, so
// that repository config cannot change it (each verified on 2.50.1):
// rename detection is git's default, where diff.renames=false would turn a
// rename into a full delete and add, and so is its limit of 1000 files,
// where diff.renameLimit=1 does the same to every rename that also edits
// the file; the root commit is diffed against the empty tree, which
// log.showRoot=false would hide; the algorithm is git's default whatever
// diff.algorithm says; no textconv program that the repository's
// attributes and config name runs, which log -p otherwise does; and a
// gitlink change is shown, as a one-line hunk, where
// diff.ignoreSubmodules=all would hide it and diff.submodule=log or diff
// would print a summary with no hunk to count.
func historyDiffArgs() []string {
	return []string{
		"-M", "-l1000", "--root", "--diff-algorithm=myers", "--no-textconv",
		"--ignore-submodules=none", "--submodule=short",
	}
}

// windowArgs limit the first-parent log to the window by committer time.
// --since-as-filter (git 2.37) filters, as --until does. Plain --since
// stops the walk at the first commit older than the window, and under
// --first-parent there is only one chain, so one first-parent commit with
// a backdated committer time (rebase --committer-date-is-author-date, a
// wrong clock) would hide every landing below it (verified on 2.50.1). The
// price is a walk of the whole first-parent chain, which reads commit
// headers only. git includes both boundary instants, so callers also
// filter with Window.Contains.
func (h *historyRun) windowArgs() []string {
	return []string{
		"--since-as-filter=" + h.opt.Window.Since.UTC().Format(time.RFC3339),
		"--until=" + h.opt.Window.Until.UTC().Format(time.RFC3339),
	}
}

// readLandings runs the first-parent log of the window: the landings, and
// in a clone with blobs their numstat, a merge's against its first parent.
//
// A partial clone drops --diff-merges=first-parent with --numstat: even
// with nothing to print, it makes git diff every commit, and rename
// detection then reads blobs the clone does not have (verified on 2.50.1:
// "could not fetch ... from promisor remote" under GIT_NO_LAZY_FETCH).
func (h *historyRun) readLandings(ctx context.Context) error {
	args := append(historyConfig(), "log", "--first-parent")
	if h.lines {
		args = append(args, "--diff-merges=first-parent")
	}
	args = append(args, h.windowArgs()...)
	args = append(args, "-z")
	if h.lines {
		args = append(args, "--numstat")
		args = append(args, historyDiffArgs()...)
	}
	args = append(args, "--no-color", "--encoding=UTF-8", "--pretty=format:"+historyLogFormat, h.repo.Head, "--")

	err := h.git.Stream(ctx, domain.GitCmd{Dir: h.repo.Path, Args: args}, func(r io.Reader) error {
		return historyParseLog(r, func(rec *historyRecord) error {
			if !h.opt.Window.Contains(rec.commit.CommitTime) {
				return nil // git's --since-as-filter and --until both include the boundary instant
			}
			h.landings = append(h.landings, rec.commit.SHA)
			return h.add(rec)
		})
	})
	if err != nil {
		return fmt.Errorf("reading landings: %w", err)
	}
	return nil
}

// add records one commit of a history log and its numstat rows.
func (h *historyRun) add(rec *historyRecord) error {
	sha := rec.commit.SHA
	if _, dup := h.commits[sha]; dup {
		return fmt.Errorf("git printed commit %s twice", sha)
	}
	h.commits[sha] = &historyCommit{commit: rec.commit, coAuthors: rec.coAuthors}
	if h.repo.Shallow && len(rec.commit.Parents) == 0 {
		// The clone's boundary: its numstat is its whole tree, not its change.
		h.cut = append(h.cut, sha)
		return nil
	}
	nonBlank := -1 // not collected in week runs
	if h.scan && h.lines {
		nonBlank = 0 // readPatches adds the counts
	}
	for _, n := range rec.numstat {
		key := historyChangeKey{commit: sha, path: n.path}
		if fc, ok := h.changes[key]; ok {
			// One path twice in one diff; git prints a typechange as one
			// row today, but summing keeps a split one whole.
			fc.Added += n.added
			fc.Removed += n.removed
			fc.Binary = fc.Binary || n.binary
			fc.OldPath = cmp.Or(fc.OldPath, n.oldPath)
			continue
		}
		h.changes[key] = &domain.FileChange{
			Commit:          sha,
			Path:            n.path,
			OldPath:         n.oldPath,
			Added:           n.added,
			Removed:         n.removed,
			Binary:          n.binary,
			AddedNonBlank:   nonBlank,
			RemovedNonBlank: nonBlank,
		}
	}
	return nil
}

// readBroughtIn reads the commits that merge landings brought in, which
// the first-parent log never printed: their metadata, co-authors and
// numstat. A merge among them prints no diff (--diff-merges=off), so it
// gets no Changes.
func (h *historyRun) readBroughtIn(ctx context.Context) error {
	var want []string
	for _, landing := range h.landings {
		for _, sha := range h.maps[landing] {
			if _, ok := h.commits[sha]; !ok {
				want = append(want, sha)
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	slices.Sort(want)

	args := append(historyConfig(), "log", "--no-walk", "--stdin", "--diff-merges=off", "-z")
	if h.lines {
		args = append(args, "--numstat")
		args = append(args, historyDiffArgs()...)
	}
	args = append(args, "--no-color", "--encoding=UTF-8", "--pretty=format:"+historyLogFormat, "--")

	cmd := domain.GitCmd{Dir: h.repo.Path, Args: args, Stdin: historyStdin(want)}
	err := h.git.Stream(ctx, cmd, func(r io.Reader) error {
		return historyParseLog(r, h.add)
	})
	if err != nil {
		return fmt.Errorf("reading the commits merges brought in: %w", err)
	}
	for _, sha := range want {
		if _, ok := h.commits[sha]; !ok {
			return fmt.Errorf("git log --no-walk did not print commit %s", sha)
		}
	}
	return nil
}

// readRoots reads the parentless commits reachable from Head. In a full
// clone they are the roots, and the earliest author time among them is
// FirstCommit (scan runs). In a shallow clone they are the boundary, and
// the facts carry shallow-window when the boundary is in the window or a
// collected commit is part of it, because history behind it is missing.
func (h *historyRun) readRoots(ctx context.Context) error {
	type root struct {
		sha          string
		author, cmtr time.Time
	}
	var roots []root
	args := []string{"log", "--max-parents=0", "-z", "--no-color",
		"--pretty=format:%H%x1f%at%x1f%ct", h.repo.Head, "--"}
	err := h.git.Stream(ctx, domain.GitCmd{Dir: h.repo.Path, Args: args}, func(r io.Reader) error {
		s := newScanner(r, scanNUL)
		for s.Scan() {
			if s.Text() == "" {
				continue
			}
			f := strings.Split(s.Text(), "\x1f")
			if len(f) != 3 || !historySHA(f[0]) {
				return errors.New("malformed root commit record")
			}
			at, ok1 := historyUnix(f[1])
			ct, ok2 := historyUnix(f[2])
			if !ok1 || !ok2 {
				return fmt.Errorf("root commit %s: malformed author or commit time", f[0])
			}
			roots = append(roots, root{sha: f[0], author: at, cmtr: ct})
		}
		return s.Err()
	})
	if err != nil {
		return fmt.Errorf("reading root commits: %w", err)
	}

	if !h.repo.Shallow {
		for _, r := range roots {
			if h.first.IsZero() || r.author.Before(h.first) {
				h.first = r.author
			}
		}
		return nil
	}
	// Shallow: a boundary at or after Since cuts the window's history; a
	// collected boundary commit cuts a landing map even when it is older.
	var newest *root
	for i := range roots {
		r := &roots[i]
		if !r.cmtr.Before(h.opt.Window.Since) && (newest == nil || r.cmtr.After(newest.cmtr)) {
			newest = r
		}
	}
	switch {
	case newest != nil:
		h.warn("shallow-window", fmt.Sprintf(
			"shallow clone: history ends at commit %s (%s), after the window starts (%s); landings and changes before that commit are missing",
			newest.sha[:12], newest.cmtr.Format(time.RFC3339), h.opt.Window.Since.UTC().Format(time.RFC3339)))
	case len(h.cut) > 0:
		slices.Sort(h.cut)
		h.warn("shallow-window", fmt.Sprintf(
			"shallow clone: a landing in the window brought in commit %s, where the clone's history ends; the commits behind it are missing",
			h.cut[0][:12]))
	}
	return nil
}

// warn records a repository warning. Messages carry SHAs and dates only.
func (h *historyRun) warn(code, msg string) {
	h.warnings = append(h.warnings, domain.Warning{Code: code, Message: msg, Repo: h.repo.Key})
}

// canonicalise maps every collected commit's co-authors through the
// mailmap with one git check-mailmap --stdin.
func (h *historyRun) canonicalise(ctx context.Context) error {
	raw := make(map[string][]domain.Person)
	for sha, c := range h.commits {
		if c.coAuthors != nil {
			raw[sha] = c.coAuthors
		}
	}
	canonical, err := coAuthorCanonical(ctx, h.git, h.repo, raw)
	if err != nil {
		return err
	}
	for sha, people := range canonical {
		h.commits[sha].commit.CoAuthors = people
	}
	return nil
}

// facts assembles the result, sorted as HistoryFacts documents.
func (h *historyRun) facts() *HistoryFacts {
	f := &HistoryFacts{FirstCommit: h.first, Warnings: h.warnings}
	for _, c := range h.commits {
		f.Commits = append(f.Commits, c.commit)
	}
	slices.SortFunc(f.Commits, func(a, b domain.Commit) int { return strings.Compare(a.SHA, b.SHA) })

	for _, sha := range h.landings {
		c := h.commits[sha].commit
		f.Landings = append(f.Landings, domain.Landing{
			SHA:     sha,
			Time:    c.CommitTime,
			Merge:   len(c.Parents) > 1,
			Commits: h.maps[sha], // nil in week runs
		})
	}
	slices.SortFunc(f.Landings, func(a, b domain.Landing) int { return strings.Compare(a.SHA, b.SHA) })

	for _, fc := range h.changes {
		f.Changes = append(f.Changes, *fc)
	}
	slices.SortFunc(f.Changes, func(a, b domain.FileChange) int {
		return cmp.Or(strings.Compare(a.Commit, b.Commit), strings.Compare(a.Path, b.Path))
	})

	f.Reverts = h.reverts
	slices.SortFunc(f.Warnings, func(a, b domain.Warning) int {
		return cmp.Or(strings.Compare(a.Code, b.Code), strings.Compare(a.Message, b.Message))
	})
	return f
}

// historySHA reports whether s is a full object name in git's lowercase
// hex: 40 characters for SHA-1, 64 for SHA-256.
func historySHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// historyStdin is the standard input of a --stdin command: one SHA per
// line. Every caller passes SHAs git printed, so none can be an option.
func historyStdin(shas []string) *strings.Reader {
	return strings.NewReader(strings.Join(shas, "\n") + "\n")
}
