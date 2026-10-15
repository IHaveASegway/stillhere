package sqlite_test

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// The test binary doubles as the helper process of the racing first opens:
// started with openEnv set to a path, it opens that file at the instant
// startEnv names (Unix nanoseconds), so the helpers' opens coincide, and
// exits. Separate processes race for the file's locks the way two commands
// do; goroutines in one process share one SQLite library, whose in-process
// lock bookkeeping hides most of that race.
const (
	openEnv  = "STILLHERE_SQLITE_TEST_OPEN"
	startEnv = "STILLHERE_SQLITE_TEST_START"
)

// TestMain runs the helper process when openEnv says so, and the tests
// otherwise.
func TestMain(m *testing.M) {
	if path := os.Getenv(openEnv); path != "" {
		os.Exit(openHelper(path))
	}
	os.Exit(m.Run())
}

// openHelper opens and closes the store at path, and reports a failure on
// stderr and in its exit code.
func openHelper(path string) int {
	if at, err := strconv.ParseInt(os.Getenv(startEnv), 10, 64); err == nil {
		time.Sleep(time.Until(time.Unix(0, at)))
	}
	s, err := sqlite.Open(context.Background(), path)
	if err == nil {
		err = s.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// TestStoreLifecycle covers the store's life around the round trip
// (docs/spec/codebase.md#run-model, docs/spec/codebase.md#tests): the files
// Open creates, the files it refuses, first opens racing in separate
// processes, the lease of a run left running, and retention with LatestRun.
func TestStoreLifecycle(t *testing.T) {
	t.Run("Open creates the directory 0700 and the files 0600, in WAL mode", testOpenCreates)
	t.Run("Open refuses a file it does not own, without waiting, and leaves it as it was", testOpenRefuses)
	t.Run("first opens racing in separate processes all succeed", testRacingOpens)
	t.Run("a run left running is deleted once the store that began it has been silent for a day", testLease)
	t.Run("retention and LatestRun follow the rule", testRetention)
}

func testOpenCreates(t *testing.T) {
	ctx := t.Context()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "home", "stillhere")
	path := filepath.Join(dir, "facts.db")
	s := openStore(t, path)
	if _, err := s.BeginRun(ctx, domain.Run{Kind: domain.RunKindScan}); err != nil {
		t.Fatal(err)
	}
	// Header bytes 18 and 19, the file format's read and write versions,
	// are 2 in WAL mode (https://sqlite.org/fileformat2.html#database_header).
	if h := readFile(t, path); len(h) < 20 || h[18] != 2 || h[19] != 2 {
		t.Errorf("facts.db is not in WAL mode: header %v", h[:min(len(h), 20)])
	}
	if runtime.GOOS == "windows" {
		return // no Unix file modes
	}
	for _, c := range []struct {
		path string
		want fs.FileMode
	}{
		{parent, fs.ModeDir | 0o755}, // an existing directory keeps its mode
		{filepath.Dir(dir), fs.ModeDir | 0o700},
		{dir, fs.ModeDir | 0o700},
		{path, 0o600},
		{path + "-wal", 0o600},
		{path + "-shm", 0o600},
	} {
		fi, err := os.Stat(c.path)
		if err != nil {
			t.Error(err)
			continue
		}
		if got := fi.Mode() & (fs.ModeDir | fs.ModePerm); got != c.want {
			t.Errorf("%s: mode %v, want %v", c.path, got, c.want)
		}
	}
}

func testOpenRefuses(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	// Another program's database, in SQLite's default rollback-journal
	// mode, which Open must not switch to WAL.
	foreign := filepath.Join(dir, "other.db")
	execRaw(t, openRaw(t, foreign), `PRAGMA journal_mode = DELETE`, `CREATE TABLE notes (body TEXT)`,
		`INSERT INTO notes VALUES ('keep me')`)
	// A file that is not SQLite at all.
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("Notes, not a database.\nSQLite reads these bytes as a database header and finds no magic string.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A facts database from a newer stillhere, whose writer holds the
	// write lock all along: the refusal must not wait for it.
	newer := filepath.Join(dir, "newer.db")
	if err := openStore(t, newer).Close(); err != nil {
		t.Fatal(err)
	}
	execRaw(t, openRaw(t, newer), `PRAGMA user_version = 99`)
	holdWriteLock(t, newer)

	for _, path := range []string{foreign, text, newer} {
		before := readFile(t, path)
		// A refusal that waited for the lock would end at this deadline.
		openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		s, err := sqlite.Open(openCtx, path)
		cancel()
		if err == nil {
			_ = s.Close()
		}
		var usage *domain.UsageError
		if !errors.As(err, &usage) {
			t.Errorf("Open(%s) = %v, want a *domain.UsageError", filepath.Base(path), err)
		}
		if !bytes.Equal(before, readFile(t, path)) {
			t.Errorf("Open(%s) changed the file", filepath.Base(path))
		}
	}
	for _, name := range []string{"other.db-wal", "other.db-shm", "other.db-journal", "notes.txt-wal", "notes.txt-shm"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open left %s next to the file it refused (%v)", name, err)
		}
	}
	var usage *domain.UsageError
	if _, err := sqlite.Open(ctx, dir); !errors.As(err, &usage) {
		t.Errorf("Open(a directory) = %v, want a *domain.UsageError", err)
	}
}

func testRacingOpens(t *testing.T) {
	const procs, trials = 8, 4
	for trial := range trials {
		path := filepath.Join(t.TempDir(), "home", "facts.db")
		start := strconv.FormatInt(time.Now().Add(500*time.Millisecond).UnixNano(), 10)
		outs := make([][]byte, procs)
		errs := make([]error, procs)
		var wg sync.WaitGroup
		for i := range procs {
			wg.Go(func() {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
				cmd.Env = append(os.Environ(), openEnv+"="+path, startEnv+"="+start)
				outs[i], errs[i] = cmd.CombinedOutput()
			})
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("trial %d, process %d: %v: %s", trial, i, err, bytes.TrimSpace(outs[i]))
			}
		}
		// Migrated once (a second migration 1 would have failed an open),
		// and ready: a store without runs.
		s := openStore(t, path)
		if _, err := s.LatestRun(t.Context(), domain.RunKindScan); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("trial %d: LatestRun of a new store = %v, want ErrNotFound", trial, err)
		}
	}
}

func testLease(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "facts.db")
	raw := openRaw(t, path)
	// Two runs whose stores closed without finishing them, as after a crash.
	crashed, recent := abandonRun(t, path), abandonRun(t, path)
	// A sync's store, which writes forge rows: writes that name no run.
	syncing := openStore(t, path)
	syncRun, err := syncing.BeginRun(ctx, domain.Run{Kind: domain.RunKindSync})
	if err != nil {
		t.Fatal(err)
	}
	setLease(t, raw, crashed, time.Now().Add(-25*time.Hour))
	setLease(t, raw, recent, time.Now().Add(-23*time.Hour))
	setLease(t, raw, syncRun, time.Now().Add(-25*time.Hour))
	// A write of the syncing store that is not to the sync run renews its
	// lease: here a BeginRun stands in for a forge write.
	silent, err := syncing.BeginRun(ctx, domain.Run{Kind: domain.RunKindScan})
	if err != nil {
		t.Fatal(err)
	}
	openStore(t, path) // the open of another command, which deletes abandoned runs

	if err := syncing.PutRunFacts(ctx, crashed, nil, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a write to the run abandoned 25 hours ago = %v, want ErrNotFound: Open deletes it", err)
	}
	if err := syncing.PutRunFacts(ctx, recent, nil, nil); err != nil {
		t.Errorf("a write to the run abandoned 23 hours ago = %v, want nil: its lease has not lapsed", err)
	}
	if err := syncing.FinishRun(ctx, syncRun, domain.RunStatusComplete); err != nil {
		t.Errorf("FinishRun of the sync whose store kept writing = %v, want nil", err)
	} else if _, err := syncing.Run(ctx, syncRun); err != nil {
		t.Errorf("Run of the finished sync = %v, want it published", err)
	}
	// A store that falls silent for a day loses its run all the same.
	setLease(t, raw, silent, time.Now().Add(-25*time.Hour))
	openStore(t, path)
	if err := syncing.FinishRun(ctx, silent, domain.RunStatusComplete); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FinishRun of a run whose store was silent for a day = %v, want ErrNotFound", err)
	}
}

// testRetention runs random sequences of BeginRun and FinishRun of two kinds,
// with Now walking across months, gaps, year-long pauses, ties and
// backfills, and after every
// FinishRun compares the runs the store holds, and LatestRun, with a model
// of the rule in docs/spec/codebase.md#run-model written apart from the
// store's code.
func testRetention(t *testing.T) {
	for seed := range uint64(2) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) { retentionRuns(t, seed) })
	}
}

func retentionRuns(t *testing.T, seed uint64) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "facts.db")
	s, raw := openStore(t, path), openRaw(t, path)
	rng := rand.New(rand.NewPCG(seed, 8))
	m := &retentionModel{runs: map[int64]*modelRun{}}
	day := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	var running []int64
	for step := range 200 {
		if len(running) == 0 || rng.IntN(5) < 2 {
			kind := domain.RunKindScan
			if rng.IntN(4) == 0 {
				kind = domain.RunKindSync
			}
			now := day
			switch r := rng.IntN(20); {
			case r < 2: // the Now of the last run that moved the clock: a tie
			case r < 3: // a backfill: --now in the past
				now = day.AddDate(0, 0, -1-rng.IntN(400))
			case r < 5: // a gap of a month or more
				day = day.AddDate(0, 0, 20+rng.IntN(60))
				now = day
			case r < 6: // a pause of about a year: the month number comes round again
				day = day.AddDate(0, 0, 340+rng.IntN(60))
				now = day
			default:
				day = day.AddDate(0, 0, 1+rng.IntN(9))
				now = day
			}
			id, err := s.BeginRun(ctx, domain.Run{Kind: kind, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			m.begin(id, kind, now)
			if rng.IntN(20) > 0 { // else left running for good, which retention never touches
				running = append(running, id)
			}
			continue
		}
		i := rng.IntN(len(running))
		id := running[i]
		running = slices.Delete(running, i, i+1)
		status := []domain.RunStatus{domain.RunStatusComplete, domain.RunStatusComplete, domain.RunStatusComplete,
			domain.RunStatusPartial, domain.RunStatusFailed}[rng.IntN(5)]
		if err := s.FinishRun(ctx, id, status); err != nil {
			t.Fatalf("step %d: FinishRun(%d, %s): %v", step, id, status, err)
		}
		m.finish(id, status)
		if got, want := storedRuns(t, raw), m.stored(); !slices.Equal(got, want) {
			t.Fatalf("step %d: after FinishRun(%d, %s) the store holds\n%v\nwant\n%v", step, id, status, got, want)
		}
		for _, kind := range []domain.RunKind{domain.RunKindScan, domain.RunKindSync} {
			got, err := s.LatestRun(ctx, kind)
			want, ok := m.latest(kind)
			switch {
			case !ok && !errors.Is(err, domain.ErrNotFound):
				t.Fatalf("step %d: LatestRun(%s) = run %d, %v; want ErrNotFound", step, kind, got.ID, err)
			case ok && (err != nil || got.ID != want):
				t.Fatalf("step %d: LatestRun(%s) = run %d, %v; want run %d, the last begun", step, kind, got.ID, err, want)
			}
		}
	}
	for _, id := range m.begun {
		_, err := s.Run(ctx, id)
		r, held := m.runs[id]
		visible := held && r.published()
		if (err == nil) != visible || (err != nil && !errors.Is(err, domain.ErrNotFound)) {
			t.Errorf("Run(%d) = %v; want it visible: %v", id, err, visible)
		}
	}
}

// retentionModel is the store as the rule describes it: the runs it holds.
type retentionModel struct {
	runs  map[int64]*modelRun
	begun []int64 // every run ever begun
}

type modelRun struct {
	kind   domain.RunKind
	now    time.Time
	status domain.RunStatus
}

func (r *modelRun) published() bool {
	return r.status == domain.RunStatusComplete || r.status == domain.RunStatusPartial
}

func (m *retentionModel) begin(id int64, kind domain.RunKind, now time.Time) {
	m.runs[id] = &modelRun{kind: kind, now: now, status: domain.RunStatusRunning}
	m.begun = append(m.begun, id)
}

// finish applies the rule: among the runs of the kind, keep the finished
// run, the newest 8 published runs and the newest published run of each of
// the 12 most recent months that have one, newest by Now and then by id;
// delete the other finished runs; leave running runs alone.
func (m *retentionModel) finish(id int64, status domain.RunStatus) {
	kind := m.runs[id].kind
	m.runs[id].status = status
	var published []int64
	for other, r := range m.runs {
		if r.kind == kind && r.published() {
			published = append(published, other)
		}
	}
	slices.SortFunc(published, func(a, b int64) int {
		if c := m.runs[b].now.Compare(m.runs[a].now); c != 0 {
			return c
		}
		return cmp.Compare(b, a)
	})
	keep := map[int64]bool{id: true}
	months := map[int]bool{}
	for i, p := range published {
		if i < 8 {
			keep[p] = true
		}
		if month := m.runs[p].now.Year()*12 + int(m.runs[p].now.Month()); !months[month] && len(months) < 12 {
			months[month] = true
			keep[p] = true
		}
	}
	for other, r := range m.runs {
		if r.kind == kind && r.status != domain.RunStatusRunning && !keep[other] {
			delete(m.runs, other)
		}
	}
}

// stored lists the runs the model holds as storedRuns does.
func (m *retentionModel) stored() []string {
	ids := slices.Sorted(maps.Keys(m.runs))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%d %s %s", id, m.runs[id].kind, m.runs[id].status))
	}
	return out
}

// latest is the run LatestRun must return: the last begun published run of
// the kind.
func (m *retentionModel) latest(kind domain.RunKind) (int64, bool) {
	var latest int64
	for id, r := range m.runs {
		if r.kind == kind && r.published() && id > latest {
			latest = id
		}
	}
	return latest, latest > 0
}

// storedRuns lists every run in the file, running and failed ones included,
// by id: the runs table is part of the SQL that facts.db offers (ADR 0003).
func storedRuns(t *testing.T, raw *sql.DB) []string {
	t.Helper()
	rows, err := raw.QueryContext(t.Context(), `SELECT id, kind, status FROM runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id int64
		var kind, status string
		if err := rows.Scan(&id, &kind, &status); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d %s %s", id, kind, status))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// openStore opens the store at path and closes it when the test ends.
func openStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// openRaw opens path with database/sql directly, as another program or
// person would, on one connection, and closes it when the test ends.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func execRaw(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, q := range statements {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// holdWriteLock takes path's write lock on a connection of its own, as
// another process's long transaction would, until the test ends.
func holdWriteLock(t *testing.T, path string) {
	t.Helper()
	conn, err := openRaw(t, path).Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		_ = conn.Close()
	})
}

// abandonRun begins a run in a store of its own and closes that store
// without finishing the run, as a crash would.
func abandonRun(t *testing.T, path string) int64 {
	t.Helper()
	s, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, err := s.BeginRun(t.Context(), domain.Run{Kind: domain.RunKindScan})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// setLease moves the lease of run id to at, as if its store had last
// written then: the one thing this test cannot do through the API, short of
// waiting a day. The store keeps times as RFC 3339 text in UTC with nine
// fractional digits (migrations/0001_facts.sql).
func setLease(t *testing.T, raw *sql.DB, id int64, at time.Time) {
	t.Helper()
	res, err := raw.ExecContext(t.Context(), `UPDATE runs SET heartbeat = ? WHERE id = ?`,
		at.UTC().Format("2006-01-02T15:04:05.000000000Z"), id)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("setting the lease of run %d: %d rows, %v", id, n, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
