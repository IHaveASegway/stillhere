// Package sqlite is the outbound adapter that implements domain.FactStore on
// SQLite through modernc.org/sqlite, a pure-Go translation of SQLite, so the
// binary needs no cgo and stays static (ADR 0002, ADR 0003).
//
// facts.db is a derived, disposable replica: git, GitHub and .mailmap stay
// the sources of truth. Every scan is a run, written as running and
// published by a status flip, so readers never see half a run
// (docs/spec/codebase.md#run-model).
//
// The schema is a set of embedded migrations (migrations/NNNN_name.sql) that
// Open applies in order, tracked by PRAGMA user_version. It mirrors the
// domain types table by table, so a run's facts load back exactly as they
// were put. A Store holds two connection pools over one WAL-mode file: a
// single writer connection whose transactions begin IMMEDIATE, so writers
// queue for the lock instead of failing halfway, and query_only readers,
// each transaction of which reads one consistent snapshot while a run is
// being written. That split is the store's half of CQRS: the read side
// cannot write.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// The driver registers itself as "sqlite"; its Error says SQLITE_BUSY.
	modernc "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

const (
	// busyTimeout is how long one statement waits inside SQLite for
	// another connection's lock before failing with SQLITE_BUSY. SQLite
	// does not see the context while it waits (a cancelled command would
	// sit out the whole timeout), so the wait is short and a writer waits
	// longer in beginImmediate, which does see it.
	busyTimeout = time.Second

	// lockWait is how long a write waits in all for the write lock while
	// another process holds it. Another process's longest transaction, a
	// very large repository's facts or a FinishRun that prunes several
	// runs, takes seconds.
	lockWait = 5 * time.Minute

	// readConns bounds the read pool. A command reads in one transaction
	// at a time; the spare connections serve a long-lived reader (the MCP
	// server) answering several requests at once.
	readConns = 4

	// abandonedAfter is the lease of a running run: Open deletes a run
	// left running once nothing has written to it for this long. A live
	// scan renews the lease with every write, and its longest silence is
	// one repository's collection, which on a very large repository can
	// exceed an hour. Keeping a crashed run's invisible rows for a day
	// costs only disk space; deleting a live run would fail its scan.
	abandonedAfter = 24 * time.Hour
)

// Store is one open facts database. It is safe for concurrent use.
type Store struct {
	path string  // the database file, absolute
	db   *sql.DB // the writer: one connection; every transaction begins IMMEDIATE
	ro   *sql.DB // the readers: query_only connections
}

var _ domain.FactStore = (*Store)(nil)

// Open opens the facts database at path. It creates a missing directory
// with mode 0700 and a missing file with mode 0600 (the -wal and -shm files
// SQLite adds inherit the file's mode), and leaves the mode of an existing
// directory or file alone. It then migrates the schema to this binary's
// version and deletes runs a crash left running (see abandonedAfter).
//
// A database written by a newer stillhere, or a SQLite file that is not a
// facts database, is a *domain.UsageError: Open never changes such a file.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, &domain.UsageError{Msg: "no facts database path"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite.Open %s: %w", path, err)
	}
	if err := createFile(abs); err != nil {
		return nil, fmt.Errorf("sqlite.Open %s: %w", abs, err)
	}
	s := &Store{path: abs}
	if err := s.open(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite.Open %s: %w", abs, err), s.Close())
	}
	return s, nil
}

func (s *Store) open(ctx context.Context) error {
	var err error
	if s.db, err = sql.Open("sqlite", dsn(s.path, false)); err != nil {
		return err
	}
	// One connection: writes queue in database/sql instead of spinning on
	// SQLite's busy handler, and IMMEDIATE takes the write lock at BEGIN,
	// so a transaction never fails halfway because another connection
	// wrote after it first read (https://sqlite.org/lang_transaction.html).
	s.db.SetMaxOpenConns(1)
	if err := migrate(ctx, s.db); err != nil {
		return err
	}
	if err := s.deleteAbandonedRuns(ctx, time.Now()); err != nil {
		return err
	}
	if s.ro, err = sql.Open("sqlite", dsn(s.path, true)); err != nil {
		return err
	}
	s.ro.SetMaxOpenConns(readConns)
	s.ro.SetMaxIdleConns(readConns)
	return nil
}

// createFile creates the database's directory (0700) and the empty
// database file (0600) when they are missing. SQLite would create the file
// itself, but with mode 0644: it forces that default on every file it
// creates, whatever the umask, and gives the -wal and -shm files the mode of
// the database file. Creating the file first is what keeps all three 0600.
func createFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		return err
	case fi.IsDir():
		return &domain.UsageError{Msg: "a directory, not a facts database file"}
	}
	return nil
}

// dsn returns the modernc.org/sqlite data source name for path: a file:
// URI, so a path holding '?', '#' or '%' stays one path, with the
// per-connection settings as validated driver parameters. Every connection
// enforces foreign keys and waits busyTimeout for a lock, and rejects a
// double-quoted string literal (_dqs=0), which SQLite would otherwise take
// for a misspelt column name. The writer also turns on WAL (a property of
// the file, so every later connection inherits it), syncs at NORMAL (safe
// in WAL mode; a power cut can lose the last commits of a file that is
// rebuilt from git anyway) and begins every transaction IMMEDIATE.
// Readers are query_only.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Set("_busy_timeout", strconv.FormatInt(busyTimeout.Milliseconds(), 10))
	q.Set("_foreign_keys", "1")
	q.Set("_dqs", "0")
	if readOnly {
		q.Set("_query_only", "1")
	} else {
		q.Set("_journal_mode", "WAL")
		q.Set("_synchronous", "NORMAL")
		q.Set("_txlock", "immediate")
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // a Windows drive path: file:///C:/...
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String()
}

// deleteAbandonedRuns deletes the runs left running whose lease expired
// before now (abandonedAfter). It is housekeeping that never makes Open
// wait: it looks before it takes the write lock, so a command that only
// reads locks nothing when there is nothing to delete, and it tries the
// lock once, leaving the deletion to a later Open while another process
// writes. An abandoned run is invisible to readers either way.
func (s *Store) deleteAbandonedRuns(ctx context.Context, now time.Time) error {
	cutoff, err := encodeTime(now.Add(-abandonedAfter))
	if err != nil {
		return err
	}
	const where = `WHERE status = 'running' AND heartbeat < ?`
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runs `+where, cutoff).Scan(&n); err != nil {
		return fmt.Errorf("finding abandoned runs: %w", err)
	}
	if n == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil) // one attempt: busyTimeout
	if busy(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs `+where, cutoff); err != nil {
		return errors.Join(fmt.Errorf("deleting abandoned runs: %w", err), rollback(tx))
	}
	return tx.Commit()
}

// write runs fn in one transaction on the writer connection, which begins
// it IMMEDIATE, and commits it when fn succeeds.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	return tx.Commit()
}

// beginImmediate begins a transaction on the writer db, whose connection
// begins it IMMEDIATE (see dsn), so it holds the write lock from its first
// statement and cannot fail halfway for want of it. While another process
// holds the lock, it retries with jittered exponential backoff, stopping when
// ctx ends or after lockWait. SQLite's own busy handler sleeps and polls
// without a queue, so a process that commits back to back can starve
// another's; the jitter breaks lockstep, and the long wait in Go keeps a
// cancelled command from waiting more than busyTimeout. SQLITE_BUSY is the
// only error retried: it comes from BEGIN, before fn has done anything.
func beginImmediate(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	deadline := time.Now().Add(lockWait)
	pause := 10 * time.Millisecond
	for {
		tx, err := db.BeginTx(ctx, nil)
		if err == nil || !busy(err) {
			return tx, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another writer held the facts database for over %v: %w", lockWait, err)
		}
		t := time.NewTimer(pause/2 + rand.N(pause))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, fmt.Errorf("waiting for another writer to release the facts database: %w", ctx.Err())
		case <-t.C:
		}
		pause = min(2*pause, 500*time.Millisecond)
	}
}

// busy reports whether err is SQLITE_BUSY, with or without an extended code.
func busy(err error) bool {
	var e *modernc.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}

// read runs fn in one read transaction on a reader connection: every query
// fn makes sees the same snapshot of the database.
func (s *Store) read(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	return tx.Commit()
}

// rollback rolls tx back, ignoring the error of a transaction that already
// ended, as one does when its context is cancelled.
func rollback(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

// Close releases the database. The last connection to the file, in any
// process, checkpoints the write-ahead log into it. Close is safe on a nil
// Store, on a Store whose Open failed, and when called twice.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.ro != nil {
		errs = append(errs, s.ro.Close())
	}
	if s.db != nil {
		errs = append(errs, s.db.Close())
	}
	return errors.Join(errs...)
}
