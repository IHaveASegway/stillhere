package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strconv"

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

// migration is one embedded schema change.
type migration struct {
	version int    // its NNNN; user_version once it has applied
	name    string // the file name
	sql     string
}

// migrations returns the embedded migrations in order, checking that they
// are numbered 1, 2, 3 without gaps.
func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
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
		data, err := fs.ReadFile(migrationFiles, "migrations/"+e.Name())
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

// migrate brings the database to the newest embedded version. It refuses,
// with a *domain.UsageError and without writing, a file that is not a facts
// database and one that a newer stillhere migrated past this binary's last
// migration.
func migrate(ctx context.Context, db *sql.DB) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	// The common case, an up-to-date file, needs no write lock.
	version, app, err := header(ctx, db)
	if err != nil {
		return err
	}
	if version == len(ms) && app == applicationID {
		return nil
	}
	for {
		done, err := migrateOnce(ctx, db, ms)
		if err != nil || done {
			return err
		}
	}
}

// migrateOnce applies the next migration, or reports that none is left. It
// reads the header again inside its IMMEDIATE transaction: another process
// may have migrated the file since the caller looked.
func migrateOnce(ctx context.Context, db *sql.DB, ms []migration) (done bool, err error) {
	tx, err := beginImmediate(ctx, db)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, rollback(tx))
		}
	}()
	version, app, err := header(ctx, tx)
	if err != nil {
		return false, err
	}
	if err := checkHeader(ctx, tx, version, app, len(ms)); err != nil {
		return false, err
	}
	if version == len(ms) {
		return true, tx.Commit()
	}
	m := ms[version]
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	// PRAGMA takes no parameters; both values are integers formatted here.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	return m.version == len(ms), nil
}

// checkHeader refuses a file this binary must not migrate: another
// application's database, and a facts database newer than this binary.
func checkHeader(ctx context.Context, tx *sql.Tx, version, app, latest int) error {
	if app != applicationID && (app != 0 || version != 0) {
		// Another application's id, or a schema version without ours.
		return notFacts()
	}
	if version > latest {
		return &domain.UsageError{Msg: fmt.Sprintf(
			"schema version %d is newer than this stillhere, which knows versions up to %d: upgrade stillhere or use another database file",
			version, latest)}
	}
	if version == 0 {
		// A new, empty file, or another program's database that set
		// neither header field: only the first may be migrated.
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return notFacts()
		}
	}
	return nil
}

// notFacts is the refusal of a SQLite file that another program owns. Open
// prefixes the path.
func notFacts() error {
	return &domain.UsageError{Msg: "not a stillhere facts database: use another file"}
}

// querier is what header needs of a *sql.DB or a *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// header reads the schema version (PRAGMA user_version) and the
// application id from the database header.
func header(ctx context.Context, q querier) (version, app int, err error) {
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, 0, fmt.Errorf("reading the schema version: %w", err)
	}
	if err := q.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&app); err != nil {
		return 0, 0, fmt.Errorf("reading the application id: %w", err)
	}
	return version, app, nil
}
