package sqlite

import (
	"context"
	"crypto/rand"
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
// Every commit is synchronous=FULL: in WAL mode NORMAL survives a crash of
// the process but can lose the last commits to a power cut, and a lost
// decision would let a later one disagree with an answer that was sent. A
// decision takes one connection's IMMEDIATE transaction for its lookup, the
// decision itself and its rows, so two servers that share the record queue
// for it and each sees the other's decision.

//go:embed egress/*.sql
var egressFiles embed.FS

// egressAppID marks an egress record in the header's application id: the
// bytes "steg", so neither facts.db nor another program's file opens as one.
const egressAppID = 0x73746567

// egressSchema is the egress record's.
var egressSchema = schema{name: "egress record", files: egressFiles, dir: "egress", appID: egressAppID,
	writers: "stillhere mcp records there the AI-assisted shares it lets leave, and withholds every share while it cannot; " +
		"make it writable, or pass --egress or set STILLHERE_HOME to a place that is"}

// egressSaltBytes is the length of the record's salt: 256 bits, the
// HMAC-SHA-256 key size.
const egressSaltBytes = 32

// Egress is one open egress record. It is safe for concurrent use.
type Egress struct {
	path     string
	db       *sql.DB // one connection, whose transactions begin IMMEDIATE (or read-only, readOnly)
	salt     []byte  // nil for a read-only record
	readOnly bool
}

var _ domain.EgressLedger = (*Egress)(nil)

// OpenEgress opens the egress record at path for the MCP server, its one
// writer: it creates a missing directory 0700 and file 0600, migrates the
// schema, and makes the record's salt the first time. Another program's file,
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
	if e.db, err = sql.Open("sqlite", dsnSynced(e.path, false, "", "FULL")); err != nil {
		return err
	}
	e.db.SetMaxOpenConns(1)
	if err := egressSchema.migrate(ctx, e.db); err != nil {
		return err
	}
	salt := make([]byte, egressSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	return e.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO egress_meta (key, value) SELECT 'salt', ?
			WHERE NOT EXISTS (SELECT 1 FROM egress_meta WHERE key = 'salt')`, salt); err != nil {
			return fmt.Errorf("making the record's salt: %w", err)
		}
		return tx.QueryRowContext(ctx, `SELECT value FROM egress_meta WHERE key = 'salt'`).Scan(&e.salt)
	})
}

// OpenEgressReadOnly opens the existing egress record at path for a reader,
// stillhere egress, which creates nothing and cannot write it: a missing
// file is a *domain.UsageError that names the path and says when the MCP
// server makes one, so a mistyped --egress is not read as "nothing sent".
// It refuses what OpenEgress refuses, and a record older than this binary,
// which only its writer migrates.
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
	e := &Egress{path: abs, readOnly: true}
	if e.db, err = sql.Open("sqlite", dsn(abs, true, "ro")); err != nil {
		return nil, err
	}
	ms, err := egressSchema.migrations()
	if err != nil {
		return nil, errors.Join(err, e.Close())
	}
	var h fileHeader
	err = retryBusy(ctx, func() error {
		var err error
		h, err = egressSchema.readHeader(ctx, e.db)
		return err
	})
	if err == nil {
		err = h.check(egressSchema, len(ms))
	}
	if err == nil && h.version < len(ms) {
		err = &domain.UsageError{Msg: fmt.Sprintf("an egress record of schema version %d, older than this stillhere's %d: "+
			"stillhere mcp upgrades it the next time it starts with it", h.version, len(ms))}
	}
	if err != nil {
		return nil, errors.Join(openFailed("sqlite.OpenEgressReadOnly", abs, err), e.Close())
	}
	return e, nil
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
	decide func(salt []byte, sent []domain.EgressDecision) (domain.EgressDecision, error)) (domain.EgressDecision, bool, error) {
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
		sent, err := egressLoad(ctx, tx, `WHERE window_since IS ? AND window_until IS ?`, at[1], at[2])
		if err != nil {
			return err
		}
		d, err := decide(slices.Clone(e.salt), sent)
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
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: e.readOnly})
	if err != nil {
		return nil, fmt.Errorf("sqlite.Egress.Decisions %s: %w", e.path, err)
	}
	ds, err := egressLoad(ctx, tx, "")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite.Egress.Decisions %s: %w", e.path, err), rollback(tx))
	}
	return ds, tx.Commit()
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
	for i, id := range ids {
		if err := egressLoadParts(ctx, tx, id, &out[i]); err != nil {
			return nil, fmt.Errorf("decision %d: %w", id, err)
		}
	}
	return out, nil
}

// egressLoadParts reads one decision's shares, their repositories and the
// people of each repository.
func egressLoadParts(ctx context.Context, tx *sql.Tx, id int64, d *domain.EgressDecision) error {
	rows, err := tx.QueryContext(ctx, `SELECT kind, key, num, den FROM decision_shares WHERE decision = ? ORDER BY kind, key`, id)
	if err != nil {
		return err
	}
	index := map[domain.Scope]int{}
	for rows.Next() {
		var sh domain.EgressShare
		var num, den sql.NullFloat64
		if err := rows.Scan(&sh.Scope.Kind, &sh.Scope.Key, &num, &den); err != nil {
			return errors.Join(err, rows.Close())
		}
		sh.Num, sh.Den = decodeFloat(num), decodeFloat(den)
		index[sh.Scope] = len(d.Shares)
		d.Shares = append(d.Shares, sh)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT kind, key, repo FROM decision_share_repos WHERE decision = ? ORDER BY kind, key, repo`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sc domain.Scope
		var repo string
		if err := rows.Scan(&sc.Kind, &sc.Key, &repo); err != nil {
			return errors.Join(err, rows.Close())
		}
		i, ok := index[sc]
		if !ok {
			return errors.Join(fmt.Errorf("a repository of share %s %q, which the decision does not hold", sc.Kind, sc.Key), rows.Close())
		}
		d.Shares[i].Repos = append(d.Shares[i].Repos, repo)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT repo, num, den FROM decision_repos WHERE decision = ? ORDER BY repo`, id)
	if err != nil {
		return err
	}
	repos := map[string]int{}
	for rows.Next() {
		var r domain.EgressRepo
		var num, den sql.NullFloat64
		if err := rows.Scan(&r.Key, &num, &den); err != nil {
			return errors.Join(err, rows.Close())
		}
		r.Num, r.Den = decodeFloat(num), decodeFloat(den)
		repos[r.Key] = len(d.Repos)
		d.Repos = append(d.Repos, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT repo, person FROM decision_people WHERE decision = ? ORDER BY repo, person`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var repo, person string
		if err := rows.Scan(&repo, &person); err != nil {
			return errors.Join(err, rows.Close())
		}
		i, ok := repos[repo]
		if !ok {
			return errors.Join(fmt.Errorf("the people of %q, a repository the decision does not hold", repo), rows.Close())
		}
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_repos (decision, repo, num, den) VALUES (?, ?, ?, ?)`,
			id, r.Key, encodeFloat(r.Num), encodeFloat(r.Den)); err != nil {
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
