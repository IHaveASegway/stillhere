package collect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// blameTimeout bounds one file's blame when BlameOptions.Timeout is not set
// (docs/spec/git-invocation.md: blame 120 s per file).
const blameTimeout = 120 * time.Second

// blameProgressEvery is how many files Blame finishes between two calls of
// BlameOptions.Progress.
const blameProgressEvery = 1000

// BlameOptions configures one repository's blame.
type BlameOptions struct {
	// Paths are the classified paths at Head that received a role. Each is
	// blamed once, however often it is listed; a path Head does not have
	// fails like any other file. With no paths, Blame blames nothing and
	// only resolves IgnoreRevs. A scan calls it that way for a repository
	// whose line facts it skips (a shallow or partial clone), so durable@1
	// and simplifying@1 still exclude the commits the list names and a bad
	// IgnoreRevsFile is still a usage error.
	Paths []string
	// IgnoreRevsFile is the --ignore-revs-file override, for single-repo
	// runs only: it replaces the repository's own .git-blame-ignore-revs.
	// A file that cannot be read, or that holds a line that is not a full
	// commit ID, is a *domain.UsageError.
	IgnoreRevsFile string
	// Slots bounds the git blame processes of a whole run: one
	// NewBlameSlots(jobs), shared by every repository. Nil is an error.
	Slots *BlameSlots
	// Timeout bounds one file's blame; zero means 120 s.
	Timeout time.Duration
	// Progress, when not nil, is called after every 1,000th file and after
	// the last, with the files finished so far and the total. Calls for one
	// repository never overlap; calls for different repositories may.
	Progress func(done, total int)
}

// BlameSlots bounds the git blame processes of a whole run, across every
// repository that shares it. Blame takes a slot before it starts a file's
// git blame, and before the goroutine that runs it, and frees the slot when
// that git blame exits. Only Blame takes and frees slots, so a caller cannot
// get the protocol wrong: the type owns it.
//
// It wraps golang.org/x/sync's weighted semaphore, which ends a wait when
// its context does and serves waiters in arrival order, so repositories
// that share the slots take turns.
type BlameSlots struct {
	sem *semaphore.Weighted
}

// NewBlameSlots returns n slots, the number of git blame processes a run
// may have at once; an n below 1 means 1.
func NewBlameSlots(n int) *BlameSlots {
	return &BlameSlots{sem: semaphore.NewWeighted(int64(max(n, 1)))}
}

// acquire takes one slot, or returns false when ctx ends first.
func (s *BlameSlots) acquire(ctx context.Context) bool {
	if s.sem.Acquire(ctx, 1) != nil {
		return false
	}
	if ctx.Err() != nil {
		s.sem.Release(1) // taken as ctx ended: start no more blames
		return false
	}
	return true
}

// release frees a slot that acquire took.
func (s *BlameSlots) release() { s.sem.Release(1) }

// BlameFacts is what Blame learned about one repository.
type BlameFacts struct {
	Owners     []domain.LineOwner // sorted by Path, then Commit
	IgnoreRevs domain.IgnoreRevs  // the ignore-revs list blame used: exactly the commits git ignored
	Errors     []domain.FileError // one per blamed path that failed, sorted by Path; messages scrubbed and truncated
	// Warnings holds at most one warning, with code "ignore-revs": the
	// repository's own .git-blame-ignore-revs was not used, or not whole.
	// The caller adds it to RepoFacts.Warnings.
	Warnings []domain.Warning
}

// Blame attributes the non-blank lines of every path in opt.Paths at
// repo.Head to the commits that wrote them, with git blame -w -M and the
// repository's ignore-revs file (docs/spec/blame-collector.md), and records
// the Co-authored-by trailers of every blamed commit, so blamed lines are
// credited like commits (ADR 0007).
//
// A file is a bulkhead, as a repository is: a blame that fails, times out
// or prints porcelain that does not parse adds a FileError, and the other
// files continue. A problem with the repository's own ignore-revs file is a
// warning, and blame goes on without the lines or the file it could not
// use. Blame returns an error only for the repository as a whole:
// IgnoreRevsFile is unusable (a *domain.UsageError), Head's tree cannot be
// listed, the listed IDs cannot be resolved to commits, the co-authors
// cannot be read, the ignore-revs list cannot be written, or ctx ends. Then
// errors.Is(err, context.Canceled) tells a cancelled run, which should
// stop, from a failed repository, which should not. The temporary
// ignore-revs list is removed before Blame returns, whatever it returns.
func Blame(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt BlameOptions) (*BlameFacts, error) {
	if opt.Slots == nil || opt.Slots.sem == nil {
		return nil, errors.New("collect.Blame: BlameOptions.Slots must come from NewBlameSlots")
	}
	if !blameIsObjectID(repo.Head, 40) && !blameIsObjectID(repo.Head, 64) {
		return nil, fmt.Errorf("collect.Blame: Repo.Head %q is not a full commit ID", repo.Head)
	}

	ignore, err := blameIgnoreRevs(ctx, git, repo, opt.IgnoreRevsFile)
	if err != nil {
		return nil, fmt.Errorf("collect.Blame: %w", err)
	}
	facts := &BlameFacts{IgnoreRevs: ignore.revs}
	if ignore.warning != "" {
		facts.Warnings = []domain.Warning{{
			Code:    blameIgnoreRevsCode,
			Message: redact.Message(ignore.warning),
			Repo:    repo.Key,
		}}
	}

	paths := slices.Compact(slices.Sorted(slices.Values(opt.Paths)))
	if len(paths) == 0 {
		return facts, nil
	}
	b := &blamer{git: git, repo: repo, timeout: opt.Timeout}
	if b.timeout <= 0 {
		b.timeout = blameTimeout
	}
	if len(ignore.revs.Revs) > 0 {
		file, remove, err := blameWriteIgnoreRevs(ignore.revs.Revs)
		if err != nil {
			return nil, fmt.Errorf("collect.Blame: %w", err)
		}
		defer remove()
		b.ignoreFile = file
	}

	results := b.all(ctx, paths, opt.Slots, opt.Progress)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("collect.Blame: %w", err)
	}
	for i, r := range results {
		if r.err != nil {
			facts.Errors = append(facts.Errors, domain.FileError{
				Path:    paths[i],
				Message: redact.Message(r.err.Error()),
			})
			continue
		}
		facts.Owners = append(facts.Owners, r.owners...)
	}
	if err := blameCoAuthors(ctx, git, repo, facts.Owners); err != nil {
		return nil, fmt.Errorf("collect.Blame: %w", err)
	}

	slices.SortFunc(facts.Owners, func(a, b domain.LineOwner) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Commit, b.Commit))
	})
	slices.SortFunc(facts.Errors, func(a, b domain.FileError) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Message, b.Message))
	})
	return facts, nil
}

// blamer blames the files of one repository.
type blamer struct {
	git        domain.GitRunner
	repo       domain.Repo
	ignoreFile string        // the --ignore-revs-file list; "" for none
	timeout    time.Duration // per file
}

// blameResult is the outcome of one file's blame: its owners, or the error
// that stopped it.
type blameResult struct {
	owners []domain.LineOwner
	err    error
}

// all blames every path, holding one of slots for each git blame it runs,
// and returns one result per path, in the order of paths. When ctx ends it
// stops starting blames and leaves the rest of the results empty; the
// caller checks ctx. Every goroutine it starts has ended before it returns.
func (b *blamer) all(ctx context.Context, paths []string, slots *BlameSlots, progress func(done, total int)) []blameResult {
	results := make([]blameResult, len(paths))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex // guards done, and keeps progress calls in order
		done int
	)
	for i, path := range paths {
		// Taking the slot before starting the goroutine makes the slots
		// bound the goroutines as well as the git processes, so a
		// 50,000-file repository never parks 50,000 goroutines on the
		// semaphore (the semaphore in Effective Go, "Channels").
		if !slots.acquire(ctx) {
			break
		}
		wg.Go(func() {
			r := b.file(ctx, path)
			slots.release() // git has exited: free the slot before the bookkeeping
			results[i] = r
			if progress == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			done++
			if done%blameProgressEvery == 0 || done == len(paths) {
				progress(done, len(paths))
			}
		})
	}
	wg.Wait()
	return results
}

// file blames one path at Head:
//
//	git --attr-source=<empty tree> blame -w -M --no-textconv
//	    --indent-heuristic --encoding=UTF-8 --line-porcelain
//	    --no-ignore-revs-file [--ignore-revs-file <list>] <head> -- <path>
//
// -w -M is git-fame's surviving-lines benchmark; -M credits a block moved
// within the file to the commit that wrote it. The other options pin what
// the repository's checkout or config would change
// (docs/spec/blame-collector.md, each verified on git 2.50.1):
//
//   - --attr-source reads no .gitattributes file (attrSource), as History's
//     logs do. Blame reads the diff attribute when it follows a rename,
//     and a file marked binary is compared byte for byte, so a rename that
//     also turned CRLF into LF was followed in a bare clone but not in a
//     work tree whose uncommitted .gitattributes said "*.py -diff", nor,
//     when blame read Head's attributes, in a clone with
//     core.ignorecase=true whose committed "*.PY -diff" then matched the
//     file. Each time the renamer got every line.
//   - --no-textconv counts the stored bytes, as History does. By default
//     blame runs a textconv program that a diff=<driver> attribute and the
//     repository's diff.<driver>.textconv name, and counts its output: one
//     that turned blank lines into text made a commit keep 3 non-blank
//     lines where its own patch added 2.
//   - --indent-heuristic is git's default, which a repository's
//     diff.indentHeuristic=false turns off. It decides which of two
//     identical lines a deletion removes, so turning it off moved lines
//     from one author to another.
//   - --encoding=UTF-8 prints author names in UTF-8, as History's log
//     does, whatever i18n.logOutputEncoding says: ISO-8859-1 printed the ë
//     of Zoë as the byte 0xEB, so one person had two names.
//   - --no-ignore-revs-file comes before any --ignore-revs-file because it
//     clears a repository's own blame.ignoreRevsFile, which would otherwise
//     add to the list stillhere records, and fail every blame when it names
//     a missing file (docs/spec/git-invocation.md).
func (b *blamer) file(ctx context.Context, path string) blameResult {
	var owners []domain.LineOwner
	err := b.git.Stream(ctx, domain.GitCmd{Dir: b.repo.Path, Args: b.args(path), Timeout: b.timeout},
		func(r io.Reader) error {
			var err error
			owners, err = blameParse(r, path)
			return err
		})
	if err != nil {
		return blameResult{err: err}
	}
	return blameResult{owners: owners}
}

// args returns the arguments of path's blame, which prints
// "--line-porcelain". TestBlamePorcelain prints the same blame as
// "--porcelain" by replacing that one argument.
func (b *blamer) args(path string) []string {
	args := []string{attrSource(b.repo.Head), "blame", "-w", "-M", "--no-textconv", "--indent-heuristic",
		"--encoding=UTF-8", "--line-porcelain", "--no-ignore-revs-file"}
	if b.ignoreFile != "" {
		args = append(args, "--ignore-revs-file", b.ignoreFile)
	}
	return append(args, b.repo.Head, "--", path)
}

// blameCoAuthors sets CoAuthors on every owner from its commit's
// Co-authored-by trailers, read with one coAuthors call (coauthors.go) for
// the distinct blamed commits. Blamed commits can be older than any history
// window, so the history's commits cannot supply them.
func blameCoAuthors(ctx context.Context, git domain.GitRunner, repo domain.Repo, owners []domain.LineOwner) error {
	if len(owners) == 0 {
		return nil
	}
	shas := make([]string, 0, len(owners))
	for _, o := range owners {
		shas = append(shas, o.Commit)
	}
	slices.Sort(shas)
	shas = slices.Compact(shas)
	co, err := coAuthors(ctx, git, repo, shas)
	if err != nil {
		return fmt.Errorf("co-authors of %d blamed commits: %w", len(shas), err)
	}
	for i := range owners {
		// Owners of one commit share its slice: facts are values that no
		// reader modifies.
		if c := co[owners[i].Commit]; len(c) > 0 {
			owners[i].CoAuthors = c
		}
	}
	return nil
}
