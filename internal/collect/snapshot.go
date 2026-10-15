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
	// Below counts the first-parent commits below Commit whose committer
	// time is at or after now: landings the run reads, whose lines blame
	// counts, but that no window holds, so their authors can look gone. A
	// backdated commit above newer ones, or a clock that ran ahead, leaves
	// such a chain; on a chain whose committer times grow toward the head
	// it is 0.
	Below int
}

// SnapshotAt finds the commit a run with this now reads repo at, with one
// git rev-list --first-parent --timestamp of repo.Head, read from the head
// down: the first commit whose committer time is before now. That is the
// newest commit on the branch's first-parent chain, the chain windows count
// landings on, not the one with the latest committer time: what lies below
// it is read with it, whatever its date, as a wrong clock or a rebase that
// kept old committer dates can leave a chain. The walk reads the chain to
// its end, as the history's first-parent log does, to count the landings
// below that commit dated at or after now (Snapshot.Below), which a reader
// must be told of: on a chain of 200,000 commits it took 0.65 s without a
// commit-graph and 0.17 s with one (git 2.50.1), where stopping at the
// commit took 0.01 s. A shallow clone's chain ends at its boundary, so its
// snapshot is the newest commit before now that the clone holds.
//
// git prints the raw committer timestamp, in whole seconds; the comparison
// with now is Go's, the same as domain.Window.Contains, so the snapshot and
// every window agree on a commit at exactly now: it is not read, and below
// the snapshot it counts in Below.
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
			before := time.Unix(secs, 0).Before(now)
			switch {
			case snap.Commit != "":
				if !before {
					snap.Below++
				}
			case before:
				snap.Commit, snap.Time = sha, time.Unix(secs, 0).UTC()
			default:
				snap.After++
			}
		}
		return s.Err()
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("collect.SnapshotAt: %s: %w", repo.Name, err)
	}
	return snap, nil
}
