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
	"maps"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	// left running once the Store that began it has written nothing for
	// this long. That Store renews the lease with every write it makes,
	// whatever the write is for (see leases): a sync writes only forge
	// rows, which name no run. A live run's longest silence is one step of
	// its work, such as one very large repository's collection or mirror
	// fetch, which can exceed an hour. Keeping a crashed run's invisible
	// rows for a day costs only disk space; deleting a live run would fail
	// its scan or sync.
	abandonedAfter = 24 * time.Hour
)

// Store is one open facts database. It is safe for concurrent use.
//
// Every write goes through write and every read through read, the forge
// replica's included. write waits in Go for another process's write lock,
// with backoff that sees the context, and renews the leases of the runs this
// Store began; read reads one snapshot on the query_only pool. A statement
// run on writer directly would wait only busyTimeout for another process's
// lock and then fail with SQLITE_BUSY, so only Open's setup uses it.
type Store struct {
	path   string  // the database file, absolute
	writer *sql.DB // one connection, whose transactions begin IMMEDIATE: use write
	ro     *sql.DB // the readers, query_only connections: use read
	leases leases  // the runs this Store began and has not finished
}

var _ domain.FactStore = (*Store)(nil)

// Open opens the facts database at path. It creates a missing directory
// with mode 0700 and a missing file with mode 0600 (the -wal and -shm files
// SQLite adds inherit the file's mode), and leaves the mode of an existing
// directory or file alone. It then migrates the schema to this binary's
// version and deletes runs a crash left running (see abandonedAfter).
//
// A database written by a newer stillhere, and a file that is not a facts
// database (another program's SQLite database, or not SQLite at all), is a
// *domain.UsageError. Open refuses such a file before it writes anything to
// it, and without waiting for another process's lock.
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
	if s.writer, err = sql.Open("sqlite", dsn(s.path, false)); err != nil {
		return err
	}
	// One connection: writes queue in database/sql instead of spinning on
	// SQLite's busy handler, and IMMEDIATE takes the write lock at BEGIN,
	// so a transaction never fails halfway because another connection
	// wrote after it first read (https://sqlite.org/lang_transaction.html).
	s.writer.SetMaxOpenConns(1)
	if err := migrate(ctx, s.writer); err != nil {
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
// for a misspelt column name. The writer also syncs at NORMAL (safe in WAL
// mode; a power cut can lose the last commits of a file that is rebuilt
// from git anyway) and begins every transaction IMMEDIATE. Readers are
// query_only.
//
// WAL mode is not set here: the driver would set it as it connects, before
// Open has looked at the file, converting a file that Open then refuses,
// and SQLite would answer a conflicting switch with SQLITE_BUSY at once.
// migrate turns it on instead (walMode).
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Set("_busy_timeout", strconv.FormatInt(busyTimeout.Milliseconds(), 10))
	q.Set("_foreign_keys", "1")
	q.Set("_dqs", "0")
	if readOnly {
		q.Set("_query_only", "1")
	} else {
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
// wait, or fail because another process holds the database: it looks
// before it takes the write lock, so a command that only reads locks
// nothing when there is nothing to delete, and it tries the lock once,
// leaving the deletion to a later Open while another process writes. An
// abandoned run is invisible to readers either way.
func (s *Store) deleteAbandonedRuns(ctx context.Context, now time.Time) error {
	cutoff, err := encodeTime(now.Add(-abandonedAfter))
	if err != nil {
		return err
	}
	const where = `WHERE status = 'running' AND heartbeat < ?`
	var n int
	err = s.writer.QueryRowContext(ctx, `SELECT count(*) FROM runs `+where, cutoff).Scan(&n)
	switch {
	case busy(err):
		return nil
	case err != nil:
		return fmt.Errorf("finding abandoned runs: %w", err)
	case n == 0:
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil) // one attempt: busyTimeout
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
// it IMMEDIATE (beginImmediate), renews in it the leases of the runs this
// Store began, and commits it when fn succeeds. Every write of the store
// goes through it.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := beginImmediate(ctx, s.writer)
	if err != nil {
		return err
	}
	if err := s.leases.renew(ctx, tx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	return tx.Commit()
}

// leases holds the runs a Store began and has not finished: the runs whose
// leases (runs.heartbeat, see abandonedAfter) it keeps. Every write
// transaction of the Store renews them, whatever it writes, so a run stays
// alive as long as the process that began it keeps writing anything. The
// lease belongs to its holder, as in Gray and Cheriton's "Leases" (SOSP
// 1989), rather than to writes that name the run: a sync writes only forge
// rows, which name none.
type leases struct {
	mu  sync.Mutex
	ids map[int64]bool
}

// hold adds a run the Store began.
func (l *leases) hold(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = make(map[int64]bool)
	}
	l.ids[id] = true
}

// release removes a run the Store finished, or tried to finish.
func (l *leases) release(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ids, id)
}

// renew sets the heartbeat of every held run that is still running to the
// wall clock, inside the write transaction tx. A held run another process
// deleted as abandoned is not brought back: its next Put or FinishRun is
// domain.ErrNotFound.
func (l *leases) renew(ctx context.Context, tx *sql.Tx) error {
	l.mu.Lock()
	ids := slices.Sorted(maps.Keys(l.ids))
	l.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	heartbeat, err := encodeTime(time.Now())
	if err != nil {
		return err
	}
	args := make([]any, 0, 1+len(ids))
	args = append(args, heartbeat)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET heartbeat = ? WHERE status = 'running' AND id IN (?`+
		strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return fmt.Errorf("renewing the leases of runs %v: %w", ids, err)
	}
	return nil
}

// beginImmediate begins a transaction on the writer db, whose connection
// begins it IMMEDIATE (see dsn), so it holds the write lock from its first
// statement and cannot fail halfway for want of it. While another process
// holds the lock, it waits as retryBusy does. SQLITE_BUSY is the only error
// retried: it comes from BEGIN, before the transaction has done anything.
func beginImmediate(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	var tx *sql.Tx
	err := retryBusy(ctx, func() error {
		var err error
		tx, err = db.BeginTx(ctx, nil)
		return err
	})
	return tx, err
}

// retryBusy calls fn until it returns anything but SQLITE_BUSY, which means
// that another process holds a lock fn needs, with jittered exponential
// backoff between calls, and stops when ctx ends or after lockWait. fn must
// be safe to call again after SQLITE_BUSY: a BEGIN, a read, a switch of
// journal mode.
//
// SQLite's own busy handler sleeps and polls without a queue, so a process
// that commits back to back can starve another's; the jitter breaks
// lockstep, and the long wait in Go keeps a cancelled command from waiting
// more than busyTimeout. SQLite also skips its busy handler where waiting
// could deadlock, answering SQLITE_BUSY at once: a connection that has read
// and then asks for the write lock while another connection holds it, as a
// switch to WAL mode does. Only a retry from outside the statement gets past
// that.
func retryBusy(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(lockWait)
	pause := 10 * time.Millisecond
	for {
		err := fn()
		if !busy(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("another process held the facts database for over %v: %w", lockWait, err)
		}
		t := time.NewTimer(pause/2 + rand.N(pause))
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("waiting for another process to release the facts database: %w", ctx.Err())
		case <-t.C:
		}
		pause = min(2*pause, 500*time.Millisecond)
	}
}

// busy reports whether err is SQLITE_BUSY, with or without an extended code.
func busy(err error) bool { return resultCode(err) == sqlite3.SQLITE_BUSY }

// notADatabase reports whether err is SQLITE_NOTADB: the file is not a
// SQLite database.
func notADatabase(err error) bool { return resultCode(err) == sqlite3.SQLITE_NOTADB }

// resultCode returns the primary result code of an error from SQLite, the
// extended code's low byte, or SQLITE_OK for nil and any other error.
func resultCode(err error) int {
	var e *modernc.Error
	if !errors.As(err, &e) {
		return sqlite3.SQLITE_OK
	}
	return e.Code() & 0xff
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
	if s.writer != nil {
		errs = append(errs, s.writer.Close())
	}
	return errors.Join(errs...)
}
