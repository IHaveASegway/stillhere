package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The egress record (#85, ADR 0015): domain.EgressLedger over a SQLite file
// of its own, egress.db beside facts.db. Its one writer is the MCP server,
// which records each run's decision about the AI-assisted shares that may
// leave before the first of them does, and checks every new decision
// against those already recorded. It is a separate file rather than tables
// in facts.db because the two have opposite lives: facts.db is a replica
// the user may delete and scan again, which the MCP server opens so that it
// cannot write, while what left the machine cannot be recalled, so its
// record is append-only (triggers refuse every UPDATE and DELETE) and must
// stay readable from the first release on.
//
// Every commit is synchronous=FULL with fullfsync (dsnSynced): in WAL mode
// NORMAL survives a crash of the process but can lose the last commits to a
// power cut, as can FULL on macOS without fullfsync, and a lost decision
// would let a later one disagree with an answer that was sent. A decision
// takes one connection's IMMEDIATE transaction for its lookup, the decision
// itself and its rows, so two servers that share the record queue for it
// and the later sees the earlier's decision.

//go:embed egress/*.sql
var egressFiles embed.FS

// egressAppID marks an egress record in the header's application id: the
// bytes "steg", so neither facts.db nor another program's file opens as one.
const egressAppID = 0x73746567

// egressSchema is the egress record's. Its refusal of a file it cannot
// write never suggests a new file: a new record forgets what the old one
// let leave, and the first answers it records could be subtracted from
// those.
var egressSchema = schema{name: "egress record", files: egressFiles, dir: "egress", appID: egressAppID,
	writers: "stillhere mcp records there the AI-assisted shares it lets leave, and withholds every share while it cannot: " +
		"make it writable again, or move the record and its -wal file to a place that is and pass that path as --egress. " +
		"Do not start a new record instead: it would forget what this one let leave",
	flag: "--egress", writer: "stillhere mcp's decision"}

// Egress is one open egress record. It is safe for concurrent use.
type Egress struct {
	path      string
	db        *sql.DB // one connection, whose transactions begin IMMEDIATE (or read-only, readOnly)
	readOnly  bool
	immutable bool // read-only in a directory it cannot write: every read guards itself (immutableRead)
}

var _ domain.EgressLedger = (*Egress)(nil)

// OpenEgress opens the egress record at path for the MCP server, its one
// writer: it creates a missing directory 0700 and file 0600 and migrates
// the schema. Another program's file,
// a facts database and a record a newer stillhere wrote are each a
// *domain.UsageError that names the path, as is a file or a directory it
// cannot write.
func OpenEgress(ctx context.Context, path string) (*Egress, error) {
	if path == "" {
		return nil, &domain.UsageError{Msg: "no egress record path"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite.OpenEgress %s: %w", path, err)
	}
	if err := createFile(abs, egressSchema); err != nil {
		var usage *domain.UsageError
		if errors.As(err, &usage) {
			return nil, err
		}
		return nil, fmt.Errorf("sqlite.OpenEgress %s: %w", abs, err)
	}
	e := &Egress{path: abs}
	if err := e.open(ctx); err != nil {
		return nil, errors.Join(openFailed("sqlite.OpenEgress", abs, err), e.Close())
	}
	return e, nil
}

func (e *Egress) open(ctx context.Context) error {
	var err error
	if e.db, err = sql.Open("sqlite", dsnSynced(e.path, false, "", true)); err != nil {
		return err
	}
	e.db.SetMaxOpenConns(1)
	return egressSchema.migrate(ctx, e.db)
}

// OpenEgressReadOnly opens the existing egress record at path for a reader,
// stillhere egress, which creates nothing and cannot write it: a missing
// file is a *domain.UsageError that names the path and says when the MCP
// server makes one, so a mistyped --egress is not read as "nothing sent".
// It refuses what OpenEgress refuses, and a record older than this binary,
// which only its writer migrates. Like facts.db's reader (openReadOnly), it
// reads a record in a directory it cannot write (a read-only mount, a copy
// in another user's directory) with immutable=1 when the write-ahead log
// holds nothing, and otherwise says what to do. Beside a record it can
// write, SQLite may make the -wal and -shm files a reader of a WAL-mode
// database needs; the record itself is never written.
func OpenEgressReadOnly(ctx context.Context, path string) (*Egress, error) {
	if path == "" {
		return nil, &domain.UsageError{Msg: "no egress record path"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite.OpenEgressReadOnly %s: %w", path, err)
	}
	switch fi, err := os.Stat(abs); {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &domain.UsageError{Msg: fmt.Sprintf("%s: no egress record here: stillhere mcp makes one the first "+
			"time it lets an AI-assisted share leave; pass the --egress it was started with", abs)}
	case err != nil:
		return nil, fmt.Errorf("sqlite.OpenEgressReadOnly %s: %w", abs, err)
	case !fi.Mode().IsRegular():
		return nil, &domain.UsageError{Msg: abs + ": not an egress record file"}
	}
	const op = "sqlite.OpenEgressReadOnly"
	e := &Egress{path: abs, readOnly: true}
	err = e.readOnlyOpen(ctx)
	if err == nil {
		return e, nil
	}
	err = errors.Join(err, e.Close())
	if !unwritableHere(err) {
		return nil, openFailed(op, abs, err)
	}
	if walHolds(abs) {
		return nil, walUnreadable(op, abs, egressSchema)
	}
	e = &Egress{path: abs, readOnly: true, immutable: true}
	if err := e.readOnlyOpen(ctx); err != nil {
		return nil, errors.Join(openFailed(op, abs, err), e.Close())
	}
	return e, nil
}

// readOnlyOpen opens the record read-only (immutable=1 too when e is) and
// checks its header.
func (e *Egress) readOnlyOpen(ctx context.Context) error {
	var err error
	if e.db, err = sql.Open("sqlite", readOnlyDSN(e.path, e.immutable)); err != nil {
		return err
	}
	if e.immutable {
		e.db.SetMaxIdleConns(0) // no page cache outlives a read (immutableRead)
	}
	ms, err := egressSchema.migrations()
	if err != nil {
		return err
	}
	var h fileHeader
	err = e.read(func() error {
		return retryBusy(ctx, func() error {
			var err error
			h, err = egressSchema.readHeader(ctx, e.db)
			return err
		})
	})
	if err != nil {
		return err
	}
	if err := h.check(egressSchema, len(ms)); err != nil {
		return err
	}
	switch {
	case h.version == 0:
		return &domain.UsageError{Msg: "an empty file, not an egress record yet: stillhere mcp makes one the first " +
			"time it lets an AI-assisted share leave"}
	case h.version < len(ms):
		return &domain.UsageError{Msg: fmt.Sprintf("an egress record of schema version %d, older than this stillhere's %d: "+
			"stillhere mcp upgrades it the next time it starts with it", h.version, len(ms))}
	}
	return nil
}

// read runs fn, guarded as an immutable open's reads must be.
func (e *Egress) read(fn func() error) error {
	if e.immutable {
		return immutableRead(e.path, egressSchema, fn)
	}
	return fn()
}

// write runs fn in one IMMEDIATE transaction, waiting for another
// process's lock as facts.db's writes do, and commits it when fn succeeds.
func (e *Egress) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := beginImmediate(ctx, e.db)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, rollback(tx))
	}
	return tx.Commit()
}

// Decide implements domain.EgressLedger.
func (e *Egress) Decide(ctx context.Context, run domain.EgressRun,
	decide func(sent []domain.EgressDecision) (domain.EgressDecision, error)) (domain.EgressDecision, bool, error) {
	if e.readOnly {
		return domain.EgressDecision{}, false, fmt.Errorf("sqlite.Egress.Decide %s: the record was opened read-only", e.path)
	}
	at, err := encodeTimes(run.Started, run.Window.Since, run.Window.Until)
	if err != nil {
		return domain.EgressDecision{}, false, err
	}
	var out domain.EgressDecision
	made := false
	err = e.write(ctx, func(tx *sql.Tx) error {
		held, err := egressLoad(ctx, tx, `WHERE run_id = ? AND run_started IS ?`, run.ID, at[0])
		if err != nil || len(held) > 0 {
			if len(held) > 0 {
				out = held[0]
			}
			return err
		}
		// Every decision whose window overlaps the run's: its shares and
		// the run's are terms of one table wherever a repository carries the
		// same counts in both (app.ShareDecision).
		sent, err := egressLoad(ctx, tx, `WHERE window_until > ? AND window_since < ?`, at[1], at[2])
		if err != nil {
			return err
		}
		d, err := decide(sent)
		if err != nil {
			return err
		}
		d.Run, d.Decided = run, time.Now().UTC()
		if err := egressInsert(ctx, tx, &d); err != nil {
			return err
		}
		out, made = d, true
		return nil
	})
	if err != nil {
		return domain.EgressDecision{}, false, fmt.Errorf("sqlite.Egress.Decide %s: %w", e.path, err)
	}
	return out, made, nil
}

// Decisions implements domain.EgressLedger.
func (e *Egress) Decisions(ctx context.Context) ([]domain.EgressDecision, error) {
	var ds []domain.EgressDecision
	err := e.read(func() error {
		tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: e.readOnly})
		if err != nil {
			return err
		}
		if ds, err = egressLoad(ctx, tx, ""); err != nil {
			return errors.Join(err, rollback(tx))
		}
		return tx.Commit()
	})
	var usage *domain.UsageError
	if err != nil && !errors.As(err, &usage) {
		err = fmt.Errorf("sqlite.Egress.Decisions %s: %w", e.path, err)
	}
	return ds, err
}

// Close releases the record. It is safe on a nil Egress, one whose open
// failed, and when called twice.
func (e *Egress) Close() error {
	if e == nil || e.db == nil {
		return nil
	}
	return e.db.Close()
}

// egressLoad reads the decisions a WHERE clause over decisions selects,
// oldest first, each with its shares, their repositories and the people.
func egressLoad(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]domain.EgressDecision, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, run_id, run_started, inputs, metric, window_since, window_until, version, decided
		FROM decisions `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var out []domain.EgressDecision
	for rows.Next() {
		var id int64
		var d domain.EgressDecision
		var started, since, until, decided sql.NullString
		var metric string
		if err := rows.Scan(&id, &d.Run.ID, &started, &d.Inputs, &metric, &since, &until, &d.Version, &decided); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		var errs []error
		var e error
		d.Run.Started, e = decodeTime(started)
		errs = append(errs, e)
		d.Run.Window.Since, e = decodeTime(since)
		errs = append(errs, e)
		d.Run.Window.Until, e = decodeTime(until)
		errs = append(errs, e)
		d.Decided, e = decodeTime(decided)
		errs = append(errs, e)
		d.Metric, e = domain.ParseMetricID(metric)
		errs = append(errs, e)
		if err := errors.Join(errs...); err != nil {
			return nil, errors.Join(fmt.Errorf("decision %d: %w", id, err), rows.Close())
		}
		ids = append(ids, id)
		out = append(out, d)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	at := make(map[int64]int, len(ids))
	for i, id := range ids {
		at[id] = i
	}
	if err := egressLoadParts(ctx, tx, where, args, at, out); err != nil {
		return nil, err
	}
	return out, nil
}

// egressLoadParts reads the shares, their repositories and the people of
// each repository of the decisions where selects, at their index in out
// (at, by id): four queries, whatever the number of decisions.
func egressLoadParts(ctx context.Context, tx *sql.Tx, where string, args []any, at map[int64]int, out []domain.EgressDecision) error {
	if len(out) == 0 {
		return nil
	}
	of := `WHERE decision IN (SELECT id FROM decisions ` + where + `)`
	decision := func(id int64) (*domain.EgressDecision, error) {
		i, ok := at[id]
		if !ok {
			return nil, fmt.Errorf("a row of decision %d, which the record does not hold", id)
		}
		return &out[i], nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT decision, kind, key, num, den FROM decision_shares `+of+` ORDER BY decision, kind, key`, args...)
	if err != nil {
		return err
	}
	type share struct {
		id int64
		sc domain.Scope
	}
	shares := map[share]int{}
	for rows.Next() {
		var id int64
		var sh domain.EgressShare
		var num, den sql.NullFloat64
		if err := rows.Scan(&id, &sh.Scope.Kind, &sh.Scope.Key, &num, &den); err != nil {
			return errors.Join(err, rows.Close())
		}
		d, err := decision(id)
		if err != nil {
			return errors.Join(err, rows.Close())
		}
		sh.Num, sh.Den = decodeFloat(num), decodeFloat(den)
		shares[share{id, sh.Scope}] = len(d.Shares)
		d.Shares = append(d.Shares, sh)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT decision, kind, key, repo FROM decision_share_repos `+of+` ORDER BY decision, kind, key, repo`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var sc domain.Scope
		var repo string
		if err := rows.Scan(&id, &sc.Kind, &sc.Key, &repo); err != nil {
			return errors.Join(err, rows.Close())
		}
		i, ok := shares[share{id, sc}]
		if !ok {
			return errors.Join(fmt.Errorf("decision %d: a repository of share %s %q, which the decision does not hold", id, sc.Kind, sc.Key), rows.Close())
		}
		d, _ := decision(id)
		d.Shares[i].Repos = append(d.Shares[i].Repos, repo)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT decision, repo, name, history, num, den FROM decision_repos `+of+` ORDER BY decision, repo`, args...)
	if err != nil {
		return err
	}
	type repo struct {
		id  int64
		key string
	}
	repos := map[repo]int{}
	for rows.Next() {
		var id int64
		var r domain.EgressRepo
		var num, den sql.NullFloat64
		if err := rows.Scan(&id, &r.Key, &r.Name, &r.History, &num, &den); err != nil {
			return errors.Join(err, rows.Close())
		}
		d, err := decision(id)
		if err != nil {
			return errors.Join(err, rows.Close())
		}
		r.Num, r.Den = decodeFloat(num), decodeFloat(den)
		repos[repo{id, r.Key}] = len(d.Repos)
		d.Repos = append(d.Repos, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT decision, repo, person FROM decision_people `+of+` ORDER BY decision, repo, person`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var key, person string
		if err := rows.Scan(&id, &key, &person); err != nil {
			return errors.Join(err, rows.Close())
		}
		i, ok := repos[repo{id, key}]
		if !ok {
			return errors.Join(fmt.Errorf("decision %d: the people of %q, a repository the decision does not hold", id, key), rows.Close())
		}
		d, _ := decision(id)
		d.Repos[i].People = append(d.Repos[i].People, person)
	}
	return errors.Join(rows.Err(), rows.Close())
}

// egressInsert appends one decision with its rows.
func egressInsert(ctx context.Context, tx *sql.Tx, d *domain.EgressDecision) error {
	at, err := encodeTimes(d.Run.Started, d.Run.Window.Since, d.Run.Window.Until, d.Decided)
	if err != nil {
		return err
	}
	if slices.Contains(at, nil) {
		return errors.New("a decision needs its run's start time, its window and the time it was made")
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO decisions (run_id, run_started, inputs, metric, window_since, window_until, version, decided)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, d.Run.ID, at[0], d.Inputs, d.Metric.String(), at[1], at[2], d.Version, at[3])
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, r := range d.Repos {
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_repos (decision, repo, name, history, num, den) VALUES (?, ?, ?, ?, ?, ?)`,
			id, r.Key, r.Name, r.History, encodeFloat(r.Num), encodeFloat(r.Den)); err != nil {
			return fmt.Errorf("repository %q: %w", r.Key, err)
		}
	}
	for _, sh := range d.Shares {
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_shares (decision, kind, key, num, den) VALUES (?, ?, ?, ?, ?)`,
			id, sh.Scope.Kind, sh.Scope.Key, encodeFloat(sh.Num), encodeFloat(sh.Den)); err != nil {
			return fmt.Errorf("share %s %q: %w", sh.Scope.Kind, sh.Scope.Key, err)
		}
		for _, repo := range sh.Repos {
			if _, err := tx.ExecContext(ctx, `INSERT INTO decision_share_repos (decision, kind, key, repo) VALUES (?, ?, ?, ?)`,
				id, sh.Scope.Kind, sh.Scope.Key, repo); err != nil {
				return fmt.Errorf("share %s %q: %w", sh.Scope.Kind, sh.Scope.Key, err)
			}
		}
	}
	for _, r := range d.Repos {
		for _, p := range r.People {
			if _, err := tx.ExecContext(ctx, `INSERT INTO decision_people (decision, repo, person) VALUES (?, ?, ?)`,
				id, r.Key, p); err != nil {
				return fmt.Errorf("the people of %q: %w", r.Key, err)
			}
		}
	}
	return nil
}
