package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The schema is a list of migrations, embedded SQL files named
// NNNN_<name>.sql and numbered 1, 2, 3 without gaps. Each one applies in
// its own IMMEDIATE transaction, together with the PRAGMA user_version that
// records it, so a crash leaves the file at one version or the next, never
// between. Adding a migration is adding a file: the runner reads the
// directory, so two branches each adding one collide only on the number.
//
// A migration file must not BEGIN or COMMIT, and it runs with foreign keys
// enforced: PRAGMA foreign_keys is a no-op inside a transaction, so a
// migration that rebuilds a table must keep every reference valid. A table
// that references runs(id) must say ON DELETE CASCADE, because retention
// deletes runs.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// applicationID marks a SQLite file as a stillhere facts database in the
// header's application id (PRAGMA application_id), as "SQLite As An
// Application File Format" recommends: the bytes "stil".
const applicationID = 0x7374696c

// schema is one kind of file this package owns, with its own migrations and
// the application id that marks it: facts.db (factsSchema) and the egress
// record (egressSchema, egress.go). One runner applies either, so the rules
// above hold for both, and a file of one kind is refused as the other.
type schema struct {
	name  string // what a message calls such a file: "facts database"
	files fs.FS  // the embedded migrations, NNNN_<name>.sql in dir
	dir   string
	appID int // PRAGMA application_id
	// writers says, to a user whose file of this kind cannot be written,
	// who writes it there and what to do.
	writers string
	// flag is the flag that names such a file, and writer what writes
	// one, as a reader's refusal names them (immutableRead).
	flag, writer string
}

// factsSchema is facts.db's.
var factsSchema = schema{name: "facts database", files: migrationFiles, dir: "migrations", appID: applicationID,
	writers: "scan and sync write the facts database there; make it writable, or pass --db or set STILLHERE_HOME to a place that is",
	flag:    "--db", writer: "the scan or sync"}

// migration is one embedded schema change.
type migration struct {
	version int    // its NNNN; user_version once it has applied
	name    string // the file name
	sql     string
}

// migrations returns the embedded migrations in order, checking that they
// are numbered 1, 2, 3 without gaps.
func (sc schema) migrations() ([]migration, error) {
	entries, err := fs.ReadDir(sc.files, sc.dir)
	if err != nil {
		return nil, err
	}
	ms := make([]migration, 0, len(entries)) // ReadDir sorts by name
	for _, e := range entries {
		v, ok := migrationVersion(e.Name())
		if !ok {
			return nil, fmt.Errorf("migration %s: want a name like 0001_facts.sql", e.Name())
		}
		if v != len(ms)+1 {
			return nil, fmt.Errorf("migration %s: want version %d: migrations are numbered 1, 2, 3 without gaps", e.Name(), len(ms)+1)
		}
		data, err := fs.ReadFile(sc.files, sc.dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(data)})
	}
	return ms, nil
}

// migrationVersion parses the version of a file named NNNN_<name>.sql.
func migrationVersion(file string) (int, bool) {
	const digits = 4
	if len(file) <= digits+len("_.sql") || file[digits] != '_' || file[len(file)-len(".sql"):] != ".sql" {
		return 0, false
	}
	for _, c := range file[:digits] {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	v, err := strconv.Atoi(file[:digits])
	return v, err == nil && v > 0
}

// migrate makes db a file of the schema at its newest embedded version. It
// first reads the file's header and refuses, with a *domain.UsageError, a
// file that is not of the schema and one that a newer stillhere migrated
// past this binary's last migration. It refuses before it changes the file
// and without waiting for another process's lock, which is safe because
// neither verdict can be undone: a foreign file never becomes one of ours,
// and user_version never goes down. Then it turns on WAL mode (walMode) and
// applies the migrations the file lacks.
func (sc schema) migrate(ctx context.Context, db *sql.DB) error {
	ms, err := sc.migrations()
	if err != nil {
		return err
	}
	var h fileHeader
	err = retryBusy(ctx, func() error {
		var err error
		h, err = sc.readHeader(ctx, db)
		return err
	})
	if err != nil {
		return err
	}
	if err := h.check(sc, len(ms)); err != nil {
		return err
	}
	if err := walMode(ctx, db); err != nil {
		return err
	}
	if h.version == len(ms) {
		return nil // the common case, an up-to-date file, takes no write lock
	}
	for {
		done, err := sc.migrateOnce(ctx, db, ms)
		if err != nil || done {
			return err
		}
	}
}

// walMode turns on write-ahead logging, a property of the file that every
// later connection inherits, so readers see one snapshot while a run is
// written and never block the writer. It runs after the header check, so a
// file that Open refuses is never converted, and before the migrations, so
// they run in WAL mode, where synchronous=NORMAL is safe. Leaving
// rollback-journal mode needs the write lock after a read, which SQLite
// refuses at once with SQLITE_BUSY while another connection holds it, so
// the switch is retried (retryBusy); on a file already in WAL mode it
// changes nothing.
func walMode(ctx context.Context, db *sql.DB) error {
	var mode string
	err := retryBusy(ctx, func() error {
		return db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode)
	})
	switch {
	case err != nil:
		return fmt.Errorf("turning on WAL mode: %w", err)
	case !strings.EqualFold(mode, "wal"):
		return fmt.Errorf("turning on WAL mode: the database stayed in %s mode", mode)
	}
	return nil
}

// migrateOnce applies the next migration, or reports that none is left. It
// reads the header again inside its IMMEDIATE transaction: another process
// may have migrated the file since the caller looked.
func (sc schema) migrateOnce(ctx context.Context, db *sql.DB, ms []migration) (done bool, err error) {
	tx, err := beginImmediate(ctx, db)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, rollback(tx))
		}
	}()
	h, err := sc.readHeader(ctx, tx)
	if err != nil {
		return false, err
	}
	if err := h.check(sc, len(ms)); err != nil {
		return false, err
	}
	if h.version == len(ms) {
		return true, tx.Commit()
	}
	m := ms[h.version]
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	// PRAGMA takes no parameters; both values are integers formatted here.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", sc.appID)); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	return m.version == len(ms), nil
}

// fileHeader is what migrate reads of a file before it changes anything.
type fileHeader struct {
	version int // PRAGMA user_version: the last migration applied
	app     int // PRAGMA application_id: the schema's appID in one of its files
	objects int // the schema's tables, indexes, views and triggers: none in a new file
}

// check refuses a file this binary must not migrate as sc: another
// program's database, or this package's other kind of file, and a file of
// sc newer than this binary, which knows the versions up to latest.
func (h fileHeader) check(sc schema, latest int) error {
	switch {
	case h.app != sc.appID && (h.app != 0 || h.version != 0):
		return sc.foreign() // another program's id, or a schema version without ours
	case h.version > latest:
		return &domain.UsageError{Msg: fmt.Sprintf(
			"schema version %d is newer than this stillhere, which knows versions up to %d: upgrade stillhere or use another database file",
			h.version, latest)}
	case h.version == 0 && h.objects > 0:
		return sc.foreign() // another program's database that set neither header field
	}
	return nil // a new, empty file, or a file of sc this binary can migrate
}

// foreign is the refusal of a file that is not of sc: another program's, or
// this package's other kind. The opens prefix the path (openFailed).
func (sc schema) foreign() error {
	return &domain.UsageError{Msg: "not a stillhere " + sc.name + ": use another file"}
}

// querier is what readHeader needs of a *sql.DB or a *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readHeader reads the header in one statement, so its fields come from one
// snapshot even while another process migrates the file. A file that is not
// a SQLite database at all is refused like another program's database.
func (sc schema) readHeader(ctx context.Context, q querier) (fileHeader, error) {
	var h fileHeader
	err := q.QueryRowContext(ctx, `
		SELECT (SELECT user_version FROM pragma_user_version()),
			(SELECT application_id FROM pragma_application_id()),
			(SELECT count(*) FROM sqlite_schema)`).Scan(&h.version, &h.app, &h.objects)
	switch {
	case notADatabase(err):
		return fileHeader{}, sc.foreign()
	case err != nil:
		return fileHeader{}, fmt.Errorf("reading the database header: %w", err)
	}
	return h, nil
}
