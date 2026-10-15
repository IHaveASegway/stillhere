package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// batchRows is how many rows one INSERT statement carries. Every statement
// execution costs a trip through database/sql and the driver whatever it
// holds, so rows go in batches. Measured on one repository of 637,500 rows
// (250,000 line owners): one row per statement took about 2.0 s, 32 rows
// 1.3 s, and 128 or 512 rows no less. The batch stays far below SQLite's
// limit of 32766 parameters per statement.
const batchRows = 32

// inserter adds the rows of one table within one transaction, batchRows rows
// per execution of a statement prepared once. Like bufio.Writer it keeps the
// first error and turns every later call into a no-op, so code that writes
// rows reads as a list of rows and checks the error once, at close (Rob
// Pike, "Errors are values").
type inserter struct {
	ctx   context.Context
	tx    *sql.Tx
	table string
	cols  []string
	stmt  *sql.Stmt // the full-batch statement, prepared on first use
	args  []any     // the pending rows' values, at most one batch
	err   error
}

func newInserter(ctx context.Context, tx *sql.Tx, table string, cols ...string) *inserter {
	return &inserter{ctx: ctx, tx: tx, table: table, cols: cols}
}

// add queues one row, whose values follow the columns given to newInserter,
// and executes a batch once one is full.
func (b *inserter) add(values ...any) {
	if b.err != nil {
		return
	}
	if len(values) != len(b.cols) {
		b.err = fmt.Errorf("%s: %d values for %d columns", b.table, len(values), len(b.cols))
		return
	}
	b.args = append(b.args, values...)
	if len(b.args) == batchRows*len(b.cols) {
		b.exec()
	}
}

// time returns t encoded for a column, recording an unstorable time as the
// inserter's error.
func (b *inserter) time(t time.Time) any {
	v, err := encodeTime(t)
	if err != nil && b.err == nil {
		b.err = fmt.Errorf("%s: %w", b.table, err)
	}
	return v
}

// exec inserts the pending rows: a full batch with the prepared statement,
// the shorter last batch with a statement of its own.
func (b *inserter) exec() {
	rows := len(b.args) / len(b.cols)
	var err error
	if rows == batchRows {
		if b.stmt == nil {
			b.stmt, err = b.tx.PrepareContext(b.ctx, b.statement(rows))
		}
		if err == nil {
			_, err = b.stmt.ExecContext(b.ctx, b.args...)
		}
	} else {
		_, err = b.tx.ExecContext(b.ctx, b.statement(rows), b.args...)
	}
	if err != nil {
		b.err = fmt.Errorf("inserting into %s: %w", b.table, err)
	}
	clear(b.args) // drop the references; the driver copied the values
	b.args = b.args[:0]
}

// statement returns INSERT INTO table (cols) VALUES (?, ...), ... for rows
// rows. The table and column names are constants of this package.
func (b *inserter) statement(rows int) string {
	row := "(" + strings.Repeat("?, ", len(b.cols)-1) + "?)"
	var s strings.Builder
	s.WriteString("INSERT INTO ")
	s.WriteString(b.table)
	s.WriteString(" (")
	s.WriteString(strings.Join(b.cols, ", "))
	s.WriteString(") VALUES ")
	for i := range rows {
		if i > 0 {
			s.WriteString(", ")
		}
		s.WriteString(row)
	}
	return s.String()
}

// close inserts the rows still pending, releases the prepared statement and
// returns the first error.
func (b *inserter) close() error {
	if b.err == nil && len(b.args) > 0 {
		b.exec()
	}
	if b.stmt != nil {
		if err := b.stmt.Close(); err != nil && b.err == nil {
			b.err = err
		}
	}
	return b.err
}

// closeAll closes every inserter, so each releases its statement, and
// returns their errors joined.
func closeAll(bs ...*inserter) error {
	var errs []error
	for _, b := range bs {
		errs = append(errs, b.close())
	}
	return errors.Join(errs...)
}
