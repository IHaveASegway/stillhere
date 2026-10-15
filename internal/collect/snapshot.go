package collect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// Snapshot is the commit a run reads a repository at: the repository as it
// stood at the run's --now (docs/spec/codebase.md#run-model). Every window
// ends at now, so a run that read the branch's head, newer than now, would
// put lines written after now beside activity counted before it: their
// authors would hold code while their landings are not in any window yet.
type Snapshot struct {
	// Commit is the newest first-parent commit of repo.Head whose committer
	// time is before now, strictly, as every window is half-open: the head
	// itself unless it was committed at or after now. It is "" when the
	// chain holds no commit before now.
	Commit string
	// Time is Commit's committer time; zero when Commit is "".
	Time time.Time
	// After counts the first-parent commits above Commit, the landings at
	// or after now that the run does not read: 0 when Commit is the head,
	// and the whole chain when Commit is "".
	After int
}

// errSnapshotFound stops the first-parent walk once the snapshot is found:
// the rest of the chain is older, and a large repository's chain is long.
var errSnapshotFound = errors.New("collect: snapshot found")

// SnapshotAt finds the commit a run with this now reads repo at, with one
// git rev-list --first-parent --timestamp of repo.Head, read from the head
// down until the first commit whose committer time is before now, and no
// further. That is the newest commit on the branch's first-parent chain,
// the chain windows count landings on, not the one with the latest
// committer time: what lies below it is read with it, whatever its date, as
// a wrong clock or a rebase that kept old committer dates can leave a
// chain. A shallow clone's chain ends at its boundary, so its snapshot is
// the newest commit before now that the clone holds.
//
// git prints the raw committer timestamp, in whole seconds; the comparison
// with now is Go's, the same as domain.Window.Contains, so the snapshot and
// every window agree on a commit at exactly now: it is not read.
func SnapshotAt(ctx context.Context, git domain.GitRunner, repo domain.Repo, now time.Time) (Snapshot, error) {
	if !historySHA(repo.Head) {
		return Snapshot{}, errors.New("collect.SnapshotAt: repository head is not a full commit SHA")
	}
	var snap Snapshot
	cmd := domain.GitCmd{Dir: repo.Path, Args: []string{"rev-list", "--first-parent", "--timestamp", repo.Head, "--"}}
	err := git.Stream(ctx, cmd, func(r io.Reader) error {
		s := newScanner(r, bufio.ScanLines)
		for s.Scan() {
			ts, sha, ok := strings.Cut(s.Text(), " ")
			secs, err := strconv.ParseInt(ts, 10, 64)
			if !ok || err != nil || !historySHA(sha) {
				return fmt.Errorf("malformed rev-list --timestamp line %q", s.Text())
			}
			if t := time.Unix(secs, 0).UTC(); t.Before(now) {
				snap.Commit, snap.Time = sha, t
				return errSnapshotFound
			}
			snap.After++
		}
		return s.Err()
	})
	if err != nil && !errors.Is(err, errSnapshotFound) {
		return Snapshot{}, fmt.Errorf("collect.SnapshotAt: %s: %w", repo.Name, err)
	}
	return snap, nil
}
