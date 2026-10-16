package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	sqlite3 "modernc.org/sqlite/lib"

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
	return openReadOnly(ctx, abs, "sqlite.OpenReadOnly")
}

// openReadOnly opens the facts database at abs read-only, as op. A reader
// in a directory it cannot write (a read-only mount, another user's
// directory) cannot make the -shm file SQLite keeps beside a WAL-mode
// database, so SQLite refuses even a read-only open
// (SQLITE_READONLY_DIRECTORY, https://sqlite.org/wal.html, "Read-Only
// Databases"). Then, when no write-ahead log holds data, every committed
// page is in the file itself, and it is opened immutable=1, which needs no
// -shm: SQLite then trusts that nothing changes the file while it reads,
// which holds where no one may write. A log that holds data cannot be read
// that way, and the refusal says what to do. Neither refusal says the
// reader must write (#79).
func openReadOnly(ctx context.Context, abs, op string) (*Store, error) {
	s := &Store{path: abs}
	err := s.readOnlyOpen(ctx, false)
	if err == nil {
		return s, nil
	}
	err = errors.Join(err, s.Close())
	if !unwritableHere(err) {
		return nil, readOnlyFailed(op, abs, err, false)
	}
	if walHolds(abs) {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("%s %s: this directory cannot be written, and the database's "+
			"write-ahead log (%s-wal) holds commits that SQLite reads only through a shared-memory file beside it: "+
			"make the directory writable for one read, or copy %s and %s-wal to a directory that is and pass --db",
			op, abs, filepath.Base(abs), filepath.Base(abs), filepath.Base(abs))}
	}
	s = &Store{path: abs}
	if err := s.readOnlyOpen(ctx, true); err != nil {
		return nil, readOnlyFailed(op, abs, errors.Join(err, s.Close()), true)
	}
	return s, nil
}

// readOnlyFailed words a failed read-only open, in an unwritable directory
// or not (here).
func readOnlyFailed(op, abs string, err error, here bool) error {
	var older *olderSchema
	if errors.As(err, &older) {
		if here {
			return &domain.UsageError{Msg: fmt.Sprintf("%s %s: schema version %d is older than this stillhere's %d, and "+
				"this directory cannot be written, so the file cannot be migrated here: copy it to a directory that can, "+
				"run 'stillhere report --db COPY' once, which migrates the copy, and read the copy", op, abs,
				older.version, older.latest)}
		}
		return &domain.UsageError{Msg: fmt.Sprintf("%s %s: schema version %d is older than this stillhere's %d, and a "+
			"read-only open cannot migrate it: run 'stillhere report' once, which migrates the file, then start again",
			op, abs, older.version, older.latest)}
	}
	var usage *domain.UsageError
	if errors.As(err, &usage) {
		return &domain.UsageError{Msg: fmt.Sprintf("%s %s: %s", op, abs, usage.Msg)}
	}
	return fmt.Errorf("%s %s: %w", op, abs, err)
}

// olderSchema is a facts database a read-only open cannot migrate.
type olderSchema struct{ version, latest int }

func (e *olderSchema) Error() string {
	return fmt.Sprintf("schema version %d is older than this binary's %d", e.version, e.latest)
}

// unwritableHere reports whether err is SQLite failing because the file or
// its directory cannot be written: READONLY, among them a directory where
// the -shm file cannot be made, and CANTOPEN, which a read-write open of a
// read-only file gives.
func unwritableHere(err error) bool {
	switch resultCode(err) {
	case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_PERM:
		return true
	}
	return false
}

// walHolds reports whether the database's write-ahead log holds data: a
// -wal file that is not empty, whose commits the database file may lack
// until a checkpoint. A writer that closes cleanly checkpoints it and
// removes it.
func walHolds(abs string) bool {
	fi, err := os.Stat(abs + "-wal")
	return err == nil && fi.Size() > 0
}

func (s *Store) readOnlyOpen(ctx context.Context, immutable bool) error {
	var err error
	if s.ro, err = sql.Open("sqlite", readOnlyDSN(s.path, immutable)); err != nil {
		return err
	}
	s.ro.SetMaxOpenConns(readConns)
	s.ro.SetMaxIdleConns(readConns)
	// The write path (Store.write) needs a pool too. This one is read-only
	// like the readers, so a write fails with SQLITE_READONLY rather than
	// on a nil pool. database/sql connects lazily: a Store that never
	// writes never opens it.
	if s.writer, err = sql.Open("sqlite", readOnlyDSN(s.path, immutable)); err != nil {
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
		return &olderSchema{version: h.version, latest: len(ms)}
	}
	return nil
}

// readOnlyDSN is dsn's reader form with SQLite's mode=ro, which only a
// file: URI passes to SQLite (modernc.org/sqlite's driver documentation):
// the file is opened read-only, a missing file is SQLITE_CANTOPEN instead of
// a new empty database, and every write is SQLITE_READONLY. immutable adds
// SQLite's immutable=1, for a directory the reader cannot write
// (openReadOnly).
func readOnlyDSN(path string, immutable bool) string {
	d := dsn(path, true, "ro")
	if immutable {
		d += "&immutable=1"
	}
	return d
}
