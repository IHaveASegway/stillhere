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
	// fails like any other file.
	Paths []string
	// IgnoreRevsFile is the --ignore-revs-file override, for single-repo
	// runs only: it replaces the repository's own .git-blame-ignore-revs.
	// A file that cannot be read, or that holds a line that is not a full
	// commit ID, is a *domain.UsageError.
	IgnoreRevsFile string
	// Slots bounds the git blame processes of a whole run: one buffered
	// channel, created empty with one slot per blame allowed at once
	// (make(chan struct{}, jobs)) and shared by every repository. Blame
	// sends to take a slot before each git blame starts and receives to
	// free it when that git blame ends. A nil or unbuffered channel is an
	// error, because it would block forever.
	Slots chan struct{}
	// Timeout bounds one file's blame; zero means 120 s.
	Timeout time.Duration
	// Progress, when not nil, is called after every 1,000th file and after
	// the last, with the files finished so far and the total. Calls for one
	// repository never overlap; calls for different repositories may.
	Progress func(done, total int)
}

// BlameFacts is what Blame learned about one repository.
type BlameFacts struct {
	Owners     []domain.LineOwner // sorted by Path, then Commit
	IgnoreRevs domain.IgnoreRevs  // the ignore-revs list blame used
	Errors     []domain.FileError // sorted by Path; messages scrubbed and truncated
}

// Blame attributes the non-blank lines of every path in opt.Paths at
// repo.Head to the commits that wrote them, with git blame -w -M and the
// repository's ignore-revs file (docs/spec/blame-collector.md), and records
// the Co-authored-by trailers of every blamed commit, so blamed lines are
// credited like commits (ADR 0007).
//
// A file is a bulkhead, as a repository is: a blame that fails, times out
// or prints porcelain that does not parse adds a FileError, and the other
// files continue. Blame returns an error only for the repository as a
// whole: its ignore-revs file or its co-authors cannot be read, the
// ignore-revs list cannot be written, or ctx ends. Then errors.Is(err,
// context.Canceled) tells a cancelled run, which should stop, from a failed
// repository, which should not. The temporary ignore-revs list is removed
// before Blame returns, whatever it returns.
func Blame(ctx context.Context, git domain.GitRunner, repo domain.Repo, opt BlameOptions) (*BlameFacts, error) {
	if cap(opt.Slots) == 0 {
		return nil, errors.New("collect.Blame: BlameOptions.Slots must be a buffered channel")
	}
	if !blameIsObjectID(repo.Head, 40) && !blameIsObjectID(repo.Head, 64) {
		return nil, fmt.Errorf("collect.Blame: Repo.Head %q is not a full commit ID", repo.Head)
	}

	ignore, err := blameIgnoreRevs(ctx, git, repo, opt.IgnoreRevsFile)
	if err != nil {
		return nil, fmt.Errorf("collect.Blame: %w", err)
	}
	facts := &BlameFacts{IgnoreRevs: ignore.revs}
	if ignore.issue != nil {
		facts.Errors = append(facts.Errors, *ignore.issue)
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
func (b *blamer) all(ctx context.Context, paths []string, slots chan struct{}, progress func(done, total int)) []blameResult {
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
		// channel (the semaphore in Effective Go, "Channels").
		if !blameAcquire(ctx, slots) {
			break
		}
		wg.Go(func() {
			r := b.file(ctx, path)
			<-slots // git has exited: free the slot before the bookkeeping
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

// blameAcquire takes one of slots, or returns false when ctx ends first.
func blameAcquire(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	if ctx.Err() != nil {
		<-slots // select took the slot although ctx had ended: give it back
		return false
	}
	return true
}

// file blames one path at Head:
//
//	git blame -w -M --line-porcelain --no-ignore-revs-file
//	    [--ignore-revs-file <list>] <head> -- <path>
//
// -w -M is git-fame's surviving-lines benchmark; -M credits a block moved
// within the file to the commit that wrote it. --no-ignore-revs-file comes
// first because it clears a repository's own blame.ignoreRevsFile, which
// would otherwise add to the list stillhere records, and fail every blame
// when it names a missing file (docs/spec/git-invocation.md).
func (b *blamer) file(ctx context.Context, path string) blameResult {
	args := []string{"blame", "-w", "-M", "--line-porcelain", "--no-ignore-revs-file"}
	if b.ignoreFile != "" {
		args = append(args, "--ignore-revs-file", b.ignoreFile)
	}
	args = append(args, b.repo.Head, "--", path)

	var owners []domain.LineOwner
	err := b.git.Stream(ctx, domain.GitCmd{Dir: b.repo.Path, Args: args, Timeout: b.timeout},
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
