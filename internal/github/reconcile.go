package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// inventoryJobs bounds the git config reads that take the inventory of the
// mirrors: each is a local process of a few milliseconds.
const inventoryJobs = 8

// holdDir, root/.stale/.moving, holds a mirror for a moment while the path
// it moves to is freed, which settles two repositories that swapped names.
// It is under staleDir, so a crash between the two renames leaves the mirror
// where the next inventory finds it and where scan never looks.
const holdDir = ".moving"

// errTaken is returned by place for a path that another directory holds.
var errTaken = errors.New("the path is taken by another directory")

// ReconcileMirrors moves the mirrors under root to the paths of the
// repositories they belong to, before any mirror is fetched, and returns the
// moves, sorted by From. A mirror says which repository it belongs to by the
// node id in its config (forgeIDKey), which a rename or a transfer keeps:
//
//   - The mirror of a listed repository that is not at the repository's
//     path, root/<owner>/<name>.git, moves there: the repository was renamed
//     or transferred (MirrorRenamed), or is listed again after its mirror
//     went stale (MirrorRestored). Mirror then points its origin at the new
//     URL.
//   - Under root/<owner>, for the owners of this listing, the mirror of a
//     repository the listing no longer holds moves to root/.stale/<owner>
//     (MirrorStale), and so does a second mirror of a listed repository
//     (MirrorDuplicate). Nothing is deleted: a repository that comes back,
//     here or under another owner, gets its mirror back.
//
// Otherwise a renamed repository would be cloned again under its new name
// and scanned twice, once under each name, since discovery keys
// repositories by origin URL.
//
// The moves run one at a time, before the parallel fetches: the moves that
// free paths first, then the moves into them, through root/.stale/.moving
// when a path is taken. A move that fails is returned with its Err and the
// others go on. Directories that carry no node id are not stillhere's and
// are never moved.
func (s *Source) ReconcileMirrors(ctx context.Context, owner string, listed []domain.ForgeRepo, root string) ([]domain.MirrorMove, error) {
	if s.git == nil {
		return nil, errors.New("github: ReconcileMirrors needs Config.Git")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("mirrors directory: %w", err)
	}
	found, err := s.inventory(ctx, root)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	want := map[string]string{} // node id → the path its mirror belongs at
	owners := map[string]bool{} // the owner directories this listing speaks for, lowercased
	if ValidName(owner) {
		owners[strings.ToLower(owner)] = true
	}
	for _, r := range listed {
		if ValidName(r.Owner) && ValidName(r.Name) && validID(r.ID) {
			want[r.ID] = filepath.Join(root, r.Owner, r.Name+".git")
			owners[strings.ToLower(r.Owner)] = true
		}
	}
	moves := planMoves(found, want, owners)
	if len(moves) == 0 {
		return nil, nil
	}
	return moveMirrors(root, moves), nil
}

// mirrorAt is a mirror the inventory found.
type mirrorAt struct {
	id    string // the node id it carries
	path  string // absolute
	owner string // the name of the owner directory it is in
	stale bool   // under root/.stale
}

// inventory finds every mirror under root that carries a node id:
// root/<owner>/*.git and root/.stale/<owner>/*.git. A root that does not
// exist yet holds none.
func (s *Source) inventory(ctx context.Context, root string) ([]mirrorAt, error) {
	owners, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mirrors directory: %w", err)
	}
	var cands []mirrorAt
	add := func(dir, owner string, stale bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			s.log.Warn("mirrors: cannot list a directory", "path", redact.Scrub(dir), "error", redact.Scrub(err.Error()))
			return
		}
		for _, e := range entries {
			// A mirror is a directory, never a symbolic link, whose name
			// ends in .git; clonesDir and anything else is not one.
			if e.IsDir() && strings.HasSuffix(e.Name(), ".git") {
				cands = append(cands, mirrorAt{path: filepath.Join(dir, e.Name()), owner: owner, stale: stale})
			}
		}
	}
	for _, o := range owners {
		dir := filepath.Join(root, o.Name())
		switch {
		case o.Name() == staleDir && o.IsDir():
			subs, err := os.ReadDir(dir)
			if err != nil {
				return nil, fmt.Errorf("mirrors directory: %w", err)
			}
			for _, sub := range subs {
				if sub.IsDir() {
					add(filepath.Join(dir, sub.Name()), sub.Name(), true)
				}
			}
		case strings.HasPrefix(o.Name(), "."):
		case isDir(dir): // an owner directory, or a link to one on another disk
			add(dir, o.Name(), false)
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, inventoryJobs)
	for i := range cands {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			cands[i].id = s.forgeID(ctx, cands[i].path)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found := slices.DeleteFunc(cands, func(m mirrorAt) bool { return m.id == "" })
	slices.SortFunc(found, func(a, b mirrorAt) int { return cmp.Compare(a.path, b.path) })
	return found, nil
}

// isDir reports whether path is a directory, following a symbolic link.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// forgeID returns the node id the mirror at dir carries, or "" for a
// directory that carries none and so is not a mirror stillhere made. It
// reads dir/config alone (git config --file follows no include and needs no
// repository around it).
func (s *Source) forgeID(ctx context.Context, dir string) string {
	config := filepath.Join(dir, "config")
	if fi, err := os.Lstat(config); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	out, err := s.git.Run(ctx, domain.GitCmd{
		Dir:     dir,
		Args:    []string{"config", "--file", config, "--get", forgeIDKey},
		Timeout: localTimeout,
		OKExit:  []int{1}, // no node id
		Profile: domain.Sync,
	})
	if err != nil {
		s.log.Debug("mirrors: cannot read a node id", "path", redact.Scrub(dir), "error", redact.Scrub(err.Error()))
		return ""
	}
	if id := strings.TrimSpace(string(out)); validID(id) {
		return id
	}
	return ""
}

// planMoves decides the moves: for each listed node id, its mirror moves to
// the path in want, and any other mirror of it outside root/.stale is a
// duplicate; a mirror of an id want does not hold goes stale when it is in
// one of owners' directories. The To of a move to root/.stale is set when
// the move is made, to a free name.
func planMoves(found []mirrorAt, want map[string]string, owners map[string]bool) []domain.MirrorMove {
	byID := map[string][]mirrorAt{}
	var ids []string
	for _, m := range found {
		if byID[m.id] == nil {
			ids = append(ids, m.id)
		}
		byID[m.id] = append(byID[m.id], m)
	}
	var moves []domain.MirrorMove
	for _, id := range ids {
		ms := byID[id]
		target, listed := want[id]
		if !listed {
			for _, m := range ms {
				if !m.stale && owners[strings.ToLower(m.owner)] {
					moves = append(moves, domain.MirrorMove{Repo: id, From: m.path, Reason: domain.MirrorStale})
				}
			}
			continue
		}
		// The mirror that stays: the one at the path, else one outside
		// root/.stale, else the first (found is sorted by path).
		keep := slices.IndexFunc(ms, func(m mirrorAt) bool { return m.path == target })
		if keep < 0 {
			keep = max(0, slices.IndexFunc(ms, func(m mirrorAt) bool { return !m.stale }))
		}
		for i, m := range ms {
			switch {
			case i == keep && m.path == target:
			case i == keep && m.stale:
				moves = append(moves, domain.MirrorMove{Repo: id, From: m.path, To: target, Reason: domain.MirrorRestored})
			case i == keep:
				moves = append(moves, domain.MirrorMove{Repo: id, From: m.path, To: target, Reason: domain.MirrorRenamed})
			case !m.stale:
				moves = append(moves, domain.MirrorMove{Repo: id, From: m.path, Reason: domain.MirrorDuplicate})
			}
		}
	}
	return moves
}

// moveMirrors makes the moves: those to root/.stale first, which only free
// paths, then those to a repository's path. A path still taken when its
// turn comes is freed by a move that is yet to run, as when two
// repositories swapped names, or held by a directory stillhere does not
// manage: the mirror waits in root/.stale/.moving for the other moves, and
// goes to root/.stale if its path is still taken after them.
func moveMirrors(root string, moves []domain.MirrorMove) []domain.MirrorMove {
	type held struct {
		i    int    // index in moves
		path string // where the mirror waits
	}
	var waiting []held
	for i := range moves {
		if m := &moves[i]; m.To == "" {
			to, err := retire(root, m.From, m.From)
			m.To = to
			if err != nil {
				m.Err = redact.Scrub(err.Error())
			}
		}
	}
	for i := range moves {
		m := &moves[i]
		if m.Reason != domain.MirrorRenamed && m.Reason != domain.MirrorRestored {
			continue
		}
		err := place(m.From, m.To)
		if errors.Is(err, errTaken) {
			hold, herr := freeName(filepath.Join(root, staleDir, holdDir), "mirror.git")
			if herr == nil {
				herr = os.Rename(m.From, hold)
			}
			if herr == nil {
				waiting = append(waiting, held{i: i, path: hold})
				continue
			}
			err = errors.Join(err, herr)
		}
		if err != nil {
			m.Err = redact.Scrub(err.Error())
		}
	}
	for _, h := range waiting {
		m := &moves[h.i]
		err := place(h.path, m.To)
		if err == nil {
			continue
		}
		// Taken by a directory stillhere does not manage: the mirror waits
		// in root/.stale, where a later sync finds it once the path is free.
		at, rerr := retire(root, h.path, m.From)
		if rerr != nil {
			at = h.path
		}
		m.Err = redact.Scrub(fmt.Sprintf("%s: %v; the mirror is at %s until that directory moves away", m.To, err, at))
	}
	_ = os.Remove(filepath.Join(root, staleDir, holdDir)) // only once empty
	slices.SortFunc(moves, func(a, b domain.MirrorMove) int { return cmp.Compare(a.From, b.From) })
	return moves
}

// place moves the directory at from to the free path to, making to's
// parent when it is missing. It returns errTaken when another directory is
// at to; a path that differs from from in case only, on a file system that
// ignores case, is the same directory, and the rename changes its case.
func place(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if have, err := os.Lstat(to); err == nil {
		if mine, err := os.Lstat(from); err != nil || !os.SameFile(have, mine) {
			return errTaken
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}

// retire moves the mirror at from, which was at the path orig, to a free
// name under root/.stale/<owner of orig>, and returns where it went.
func retire(root, from, orig string) (string, error) {
	dir := filepath.Join(root, staleDir, filepath.Base(filepath.Dir(orig)))
	to, err := freeName(dir, filepath.Base(orig))
	if err != nil {
		return to, err
	}
	return to, os.Rename(from, to)
}

// freeName returns dir/name, creating dir, or dir/<stem>-2.git, -3 and so
// on for the first name no directory holds yet.
func freeName(dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return filepath.Join(dir, name), err
	}
	stem := strings.TrimSuffix(name, ".git")
	for n := 1; ; n++ {
		try := name
		if n > 1 {
			try = stem + "-" + strconv.Itoa(n) + ".git"
		}
		path := filepath.Join(dir, try)
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return path, err
		}
	}
}
