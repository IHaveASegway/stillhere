package collect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file reads each commit's patch id (domain.Commit.PatchID): git's
// own fingerprint of a change, which a cherry-pick or a backport keeps
// while its SHA, its committer and its parent change. A maintenance branch
// that carries cherry-picks of fixes already on the default branch, merged
// back into it, brings each of those changes onto the default branch a
// second time; the metrics read the patch ids to see that
// (docs/metrics.md#change-facts-shared-by-durable1-and-simplifying1). git
// cherry and git log --cherry-mark compare commits the same way.
//
// The fingerprint is git patch-id --stable over the own-patch query
// (ownPatchArgs): the -U0 -w patch that readPatches counts lines in. With
// no context lines, a backport onto code that differs around the change
// keeps the id of its original, where git cherry's patch, with three lines
// of context, does not: on tokio-rs/axum's year to 2026-10-16, the -U0 -w
// ids found 18 of the copies its maintenance merges brought in, every one
// carrying its original's pull request number, and the three-line ids 12.
// patch-id ignores whitespace and line numbers but not paths, so two
// commits share an id when they make the same change to the same files.
// --stable sums one hash per file, so the order git lists files in does
// not matter, and it beats a repository's patchid.verbatim, which would
// keep whitespace (verified on 2.50.1).

// readPatchIDs sets the patch id of every commit with a patch of its own
// (ownPatchCommits), in both modes: landed@2, which week prints too, does
// not credit a copy again.
//
// The own-patch query's output streams into git patch-id's standard input
// through a pipe, two git processes at once for the repository, as a shell
// pipeline runs them: the patches are as large as the history (18 MB over cli/cli's
// year), so they are never held whole, and patch-id only hashes what it
// reads. Its header is the commit's SHA alone (%H), the line patch-id
// takes for the start of a commit; no message is printed, so no line of
// one can be taken for it. A commit whose patch is empty, a whitespace-only
// change under -w or a mode change alone, gets no id from patch-id, and
// keeps "".
func (h *historyRun) readPatchIDs(ctx context.Context) error {
	want := h.ownPatchCommits()
	if len(want) == 0 {
		return nil
	}
	pr, pw := io.Pipe()
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		cmd := domain.GitCmd{Dir: h.repo.Path, Args: h.ownPatchArgs("%H"), Stdin: historyStdin(want)}
		err := h.git.Stream(gctx, cmd, func(r io.Reader) error {
			_, err := io.Copy(pw, r)
			return err
		})
		pw.CloseWithError(err) // nil: patch-id reads its end
		return err
	})
	ids := make(map[string]string, len(want))
	g.Go(func() error {
		cmd := domain.GitCmd{Dir: h.repo.Path, Args: []string{"patch-id", "--stable"}, Stdin: pr}
		err := h.git.Stream(gctx, cmd, func(r io.Reader) error {
			s := newScanner(r, bufio.ScanLines)
			for s.Scan() {
				id, sha, ok := strings.Cut(s.Text(), " ")
				if !ok || !historySHA(id) || !historySHA(sha) {
					return errors.New("malformed patch-id line")
				}
				if _, ok := h.commits[sha]; !ok {
					return fmt.Errorf("git patch-id named commit %s, which it was not given", sha)
				}
				ids[sha] = id
			}
			return s.Err()
		})
		// The query's writes fail from here on, rather than wait for a
		// reader that is gone, when patch-id stopped early.
		pr.CloseWithError(io.ErrClosedPipe)
		return err
	})
	if err := g.Wait(); err != nil {
		return fmt.Errorf("reading patch ids: %w", err)
	}
	for sha, id := range ids {
		h.commits[sha].commit.PatchID = id
	}
	return nil
}
