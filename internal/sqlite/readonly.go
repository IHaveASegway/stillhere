package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The read-only open is the store for a long-lived reader, the MCP server
// (#15): a process that answers an agent's questions for hours and must never
// change the facts it serves. Open is the wrong door for it, because opening
// is not free of writes: Open creates a missing file, migrates the schema and
// deletes runs a crash left running. OpenReadOnly does none of that, and asks
// SQLite to enforce the rest.

// OpenReadOnly opens the existing facts database at path so that this
// process cannot write to it. Both of the Store's pools connect with SQLite's
// mode=ro URI parameter: the database file is opened read-only, and every
// write statement fails inside SQLite with SQLITE_READONLY, whatever method
// issues it. That is stronger than the query_only readers of Open, whose
// pragma a later statement could turn off. SQLite may still create the
// -wal and -shm files beside a database in WAL mode, which a reader needs
// to see another process's committed writes (https://sqlite.org/wal.html);
// it never writes to the database file itself.
//
// Nothing is created, migrated or cleaned up. A missing path, a directory,
// another program's file, and a facts database whose schema is older or
// newer than this binary's are each a *domain.UsageError that says what to
// run instead: an older database is migrated by any other command that
// opens it, such as stillhere report (OpenExisting).
//
// The Store reads exactly as Open's does, one consistent snapshot per read
// while another process writes a run, and only complete and partial runs
// are visible. Its write methods return an error. Close it as any Store.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, &domain.UsageError{Msg: "no facts database path"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite.OpenReadOnly %s: %w", path, err)
	}
	switch fi, err := os.Stat(abs); {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"sqlite.OpenReadOnly %s: no facts database here yet: run 'stillhere scan PATH...' first, or pass --db", abs)}
	case err != nil:
		return nil, fmt.Errorf("sqlite.OpenReadOnly %s: %w", abs, err)
	case !fi.Mode().IsRegular():
		return nil, &domain.UsageError{Msg: fmt.Sprintf("sqlite.OpenReadOnly %s: not a facts database file", abs)}
	}
	s := &Store{path: abs}
	if err := s.readOnlyOpen(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite.OpenReadOnly %s: %w", abs, err), s.Close())
	}
	return s, nil
}

func (s *Store) readOnlyOpen(ctx context.Context) error {
	var err error
	if s.ro, err = sql.Open("sqlite", readOnlyDSN(s.path)); err != nil {
		return err
	}
	s.ro.SetMaxOpenConns(readConns)
	s.ro.SetMaxIdleConns(readConns)
	// The write path (Store.write) needs a pool too. This one is read-only
	// like the readers, so a write fails with SQLITE_READONLY rather than
	// on a nil pool. database/sql connects lazily: a Store that never
	// writes never opens it.
	if s.writer, err = sql.Open("sqlite", readOnlyDSN(s.path)); err != nil {
		return err
	}
	s.writer.SetMaxOpenConns(1)
	ms, err := migrations()
	if err != nil {
		return err
	}
	var h fileHeader
	err = retryBusy(ctx, func() error {
		var err error
		h, err = readHeader(ctx, s.ro)
		return err
	})
	if err != nil {
		return err
	}
	if err := h.check(len(ms)); err != nil {
		return err
	}
	switch {
	case h.version == 0:
		return &domain.UsageError{Msg: "an empty file, not a facts database yet: run 'stillhere scan PATH...' first"}
	case h.version < len(ms):
		return &domain.UsageError{Msg: fmt.Sprintf(
			"schema version %d is older than this stillhere's %d, and a read-only open cannot migrate it: "+
				"run 'stillhere report' once, which migrates the file, then start again", h.version, len(ms))}
	}
	return nil
}

// readOnlyDSN is dsn's reader form with SQLite's mode=ro, which only a
// file: URI passes to SQLite (modernc.org/sqlite's driver documentation):
// the file is opened read-only, a missing file is SQLITE_CANTOPEN instead of
// a new empty database, and every write is SQLITE_READONLY.
func readOnlyDSN(path string) string {
	return dsn(path, true, "ro")
}
