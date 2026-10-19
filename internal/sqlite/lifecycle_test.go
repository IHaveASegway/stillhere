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
	"strings"
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
	t.Run("OpenExisting creates nothing, refuses what Open refuses, and migrates a file it reads", testOpenExisting)
	t.Run("first opens racing in separate processes all succeed", testRacingOpens)
	t.Run("a run left running is deleted once the store that began it has been silent for a day", testLease)
	t.Run("retention and LatestRun follow the rule", testRetention)
	t.Run("the egress record decides a run once, is append-only, and is no facts database", testEgress)
}

// testEgress covers the egress record (ADR 0015): a reader creates nothing;
// the writer makes the file 0600; Decide records a run's decision once,
// durably, hands a later run every earlier decision whose window overlaps
// its own, a second later or an hour, and one of a window that does not
// overlap none, and round-trips what it recorded; SQL that updates or
// deletes a row is refused; a reader reads a record in a directory it
// cannot write, and refuses one whose log holds a commit there with what to
// do; and the record and facts.db refuse each other.
func testEgress(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "home")
	path := filepath.Join(dir, "egress.db")
	var usage *domain.UsageError
	if _, err := sqlite.OpenEgressReadOnly(ctx, path); !errors.As(err, &usage) || !strings.Contains(err.Error(), path) {
		t.Errorf("OpenEgressReadOnly(a missing record) = %v, want a *domain.UsageError that names it", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenEgressReadOnly created %s (%v)", dir, err)
	}
	e, err := sqlite.OpenEgress(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	now := time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC)
	window := domain.Window{Since: now.AddDate(0, 0, -365), Until: now}
	first := domain.EgressRun{ID: 1, Started: now.Add(time.Hour), Window: window}
	ai := domain.MetricID{Name: "ai_assisted", Version: 1}
	want := domain.EgressDecision{Run: first, Inputs: "digest-1", Metric: ai, Version: "test",
		Shares: []domain.EgressShare{{Scope: domain.Scope{Kind: domain.ScopeGroup, Key: "ai"}, Repos: []string{"local:solo", "local:team"}, Num: 4, Den: 16}},
		Repos: []domain.EgressRepo{{Key: "local:solo", Name: "solo", History: "h-solo", Num: 3, Den: 4, People: []string{"aa"}},
			{Key: "local:team", Name: "team", Num: 1, Den: 12, People: []string{"", "bb", "cc"}}}}
	got, made, err := e.Decide(ctx, first, func(sent []domain.EgressDecision) (domain.EgressDecision, error) {
		if len(sent) != 0 {
			t.Errorf("the first decision got %d earlier decisions, want none", len(sent))
		}
		return want, nil
	})
	if err != nil || !made || got.Decided.IsZero() {
		t.Fatalf("Decide(run 1) = %+v, %v, %v, want it recorded now", got, made, err)
	}
	want.Decided = got.Decided
	again, made, err := e.Decide(ctx, first, func([]domain.EgressDecision) (domain.EgressDecision, error) {
		t.Error("Decide decided run 1 again")
		return domain.EgressDecision{}, nil
	})
	if err != nil || made {
		t.Fatalf("Decide(run 1 again) = %v, %v, want the recorded decision", made, err)
	}
	wantEqual(t, "the recorded decision", want, again)
	// A rescan a second later sees run 1's decision, and one an hour later
	// both; a run whose window ends where theirs began sees none.
	shifted := func(d time.Duration) domain.Window {
		return domain.Window{Since: window.Since.Add(d), Until: window.Until.Add(d)}
	}
	second := domain.EgressRun{ID: 2, Started: now.Add(2 * time.Hour), Window: shifted(time.Second)}
	if _, _, err := e.Decide(ctx, second, func(sent []domain.EgressDecision) (domain.EgressDecision, error) {
		wantEqual(t, "the decisions of windows that overlap run 2's", []domain.EgressDecision{want}, sent)
		return domain.EgressDecision{Inputs: "digest-2", Metric: ai, Version: "test"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	later := domain.EgressRun{ID: 3, Started: now.Add(3 * time.Hour), Window: shifted(time.Hour)}
	if _, _, err := e.Decide(ctx, later, func(sent []domain.EgressDecision) (domain.EgressDecision, error) {
		if len(sent) != 2 {
			t.Errorf("a run an hour later got %d decisions, want runs 1 and 2", len(sent))
		}
		return domain.EgressDecision{}, errors.New("decided nothing")
	}); err == nil {
		t.Error("Decide recorded a decision whose decide failed")
	}
	apart := domain.EgressRun{ID: 4, Started: now.Add(4 * time.Hour), Window: domain.Window{Since: window.Since.AddDate(-1, 0, 0), Until: window.Since}}
	if _, _, err := e.Decide(ctx, apart, func(sent []domain.EgressDecision) (domain.EgressDecision, error) {
		if len(sent) != 0 {
			t.Errorf("a run of a window that does not overlap got %d decisions, want none", len(sent))
		}
		return domain.EgressDecision{}, errors.New("decided nothing")
	}); err == nil {
		t.Error("Decide recorded a decision whose decide failed")
	}
	raw := openRaw(t, path)
	for _, q := range []string{`UPDATE decision_shares SET num = 3`, `DELETE FROM decisions`, `DELETE FROM decision_people`,
		`UPDATE decision_repos SET history = ''`} {
		if _, err := raw.ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: %v, want it refused as append-only", q, err)
		}
	}
	ro, err := sqlite.OpenEgressReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if ds, err := ro.Decisions(ctx); err != nil || len(ds) != 2 || ds[0].Run != first || ds[1].Run != second {
		t.Errorf("Decisions = %+v, %v, want runs 1 and 2, oldest first", ds, err)
	}
	// In a directory it cannot write, a reader reads the record as it is
	// (#79), and makes nothing there; with a commit in the log, which it
	// cannot read without a -shm file there, it says what to do.
	if runtime.GOOS != "windows" {
		execRaw(t, raw, `PRAGMA wal_checkpoint(TRUNCATE)`) // every commit in the file, as a writer that closed leaves it
		copyTo := func(dir string, from string, names ...string) {
			t.Helper()
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				data, err := os.ReadFile(from + strings.TrimPrefix(name, "egress.db"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		}
		locked := filepath.Join(t.TempDir(), "locked")
		copyTo(locked, path, "egress.db")
		r, err := sqlite.OpenEgressReadOnly(ctx, filepath.Join(locked, "egress.db"))
		if err != nil {
			t.Fatalf("OpenEgressReadOnly(a record in a read-only directory) = %v", err)
		}
		if ds, err := r.Decisions(ctx); err != nil || len(ds) != 2 {
			t.Errorf("Decisions of a record in a read-only directory = %d, %v, want runs 1 and 2", len(ds), err)
		}
		_ = r.Close()
		if entries, _ := os.ReadDir(locked); len(entries) != 1 {
			t.Errorf("the read-only directory holds %d files after reading, want egress.db alone", len(entries))
		}
		src := filepath.Join(t.TempDir(), "egress.db")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		w := openRaw(t, src)
		execRaw(t, w, `PRAGMA wal_autocheckpoint = 0`, `CREATE TABLE scratch (a)`)
		walled := filepath.Join(t.TempDir(), "walled")
		copyTo(walled, src, "egress.db", "egress.db-wal")
		if r, err := sqlite.OpenEgressReadOnly(ctx, filepath.Join(walled, "egress.db")); !errors.As(err, &usage) ||
			!strings.Contains(usage.Msg, "egress.db-wal") || !strings.Contains(usage.Msg, "--egress") {
			if err == nil {
				_ = r.Close()
			}
			t.Errorf("OpenEgressReadOnly(a read-only directory whose log holds a commit) = %v, want a *domain.UsageError that names egress.db-wal and --egress", err)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v, want mode 0600", path, fi, err)
		}
	}
	facts := filepath.Join(dir, "facts.db")
	if err := openStore(t, facts).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.OpenEgress(ctx, facts); !errors.As(err, &usage) || !strings.Contains(err.Error(), "not a stillhere egress record") {
		t.Errorf("OpenEgress(facts.db) = %v, want it refused", err)
	}
	if _, err := sqlite.Open(ctx, path); !errors.As(err, &usage) || !strings.Contains(err.Error(), "not a stillhere facts database") {
		t.Errorf("Open(egress.db) = %v, want it refused", err)
	}
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

	// A file, or the directory of one, that this process cannot write:
	// refused at Open, naming it, rather than at the first write, which a
	// scan makes after collecting every repository (#71). The superuser
	// writes anything, and Windows has no such modes.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	home := filepath.Join(t.TempDir(), "home")
	db := filepath.Join(home, "facts.db")
	if err := openStore(t, db).Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700); _ = os.Chmod(db, 0o600) })
	for _, c := range []struct{ chmod, unwritable string }{{db, db}, {home, home}} {
		if err := os.Chmod(c.chmod, 0o500); err != nil {
			t.Fatal(err)
		}
		before := readFile(t, db)
		s, err := sqlite.Open(ctx, db)
		if err == nil {
			_ = s.Close()
		}
		if !errors.As(err, &usage) || !strings.Contains(usage.Msg, "cannot write "+c.unwritable+" ") {
			t.Errorf("Open with %s read-only = %v, want a *domain.UsageError that names it", c.chmod, err)
		}
		if !bytes.Equal(before, readFile(t, db)) {
			t.Errorf("Open with %s read-only changed facts.db", c.chmod)
		}
		if err := os.Chmod(c.chmod, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func testOpenExisting(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	var usage *domain.UsageError

	// A mistyped --db: a usage error naming the path, and no directory or
	// file made there, where Open would make an empty database (#71).
	missing := filepath.Join(dir, "typo", "fact.db")
	if s, err := sqlite.OpenExisting(ctx, missing); !errors.As(err, &usage) || !strings.Contains(usage.Msg, missing) {
		if err == nil {
			_ = s.Close()
		}
		t.Errorf("OpenExisting(a missing file) = %v, want a *domain.UsageError that names it", err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenExisting(a missing file) left %s (%v)", filepath.Dir(missing), err)
	}
	if _, err := sqlite.OpenExisting(ctx, dir); !errors.As(err, &usage) {
		t.Errorf("OpenExisting(a directory) = %v, want a *domain.UsageError", err)
	}
	foreign := filepath.Join(dir, "other.db")
	execRaw(t, openRaw(t, foreign), `CREATE TABLE notes (body TEXT)`)
	if _, err := sqlite.OpenExisting(ctx, foreign); !errors.As(err, &usage) {
		t.Errorf("OpenExisting(another program's database) = %v, want a *domain.UsageError", err)
	}

	// A database a writer made reads back. A run a crash left running is
	// the next writer's to delete, not a reader's.
	path := filepath.Join(dir, "facts.db")
	w := openStore(t, path)
	id, err := w.BeginRun(ctx, domain.Run{Kind: domain.RunKindScan})
	if err == nil {
		err = w.FinishRun(ctx, id, domain.RunStatusComplete)
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	abandoned := abandonRun(t, path)
	setLease(t, openRaw(t, path), abandoned, time.Now().Add(-48*time.Hour))
	r, err := sqlite.OpenExisting(ctx, path)
	if err != nil {
		t.Fatalf("OpenExisting(a facts database) = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if run, err := r.LatestRun(ctx, domain.RunKindScan); err != nil || run.ID != id {
		t.Errorf("LatestRun = %d, %v; want run %d", run.ID, err, id)
	}
	if got := storedRuns(t, openRaw(t, path)); !slices.ContainsFunc(got, func(s string) bool {
		return strings.HasPrefix(s, strconv.FormatInt(abandoned, 10)+" ")
	}) {
		t.Errorf("OpenExisting deleted the abandoned run %d: runs %v", abandoned, got)
	}

	// A reader that cannot write the directory, as on a read-only mount,
	// reads the database read-only, the MCP server's open included, and
	// never says it must write (#79): with no write-ahead log holding
	// data, every commit is in the file. When the log holds commits, the
	// reader cannot read them without the shared-memory file SQLite would
	// make there, and the refusal says what to do.
	if os.Geteuid() != 0 { // the superuser writes any directory
		readOnlyDirectory(t, path, id)
	}

	// An empty file is a facts database before its first migration, as an
	// older one is before its later ones: the reader migrates it.
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := sqlite.OpenExisting(ctx, empty)
	if err != nil {
		t.Fatalf("OpenExisting(an empty file) = %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.LatestRun(ctx, domain.RunKindScan); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("LatestRun on the migrated empty file = %v, want domain.ErrNotFound", err)
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
			if kind == domain.RunKindScan {
				retentionForge(t, s, id)
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
		if orphans := orphanRunRows(t, raw); len(orphans) > 0 {
			t.Fatalf("step %d: after FinishRun(%d, %s) rows of deleted runs remain: %v", step, id, status, orphans)
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

// retentionForge records, for a running scan run, GitHub data it read (ADR
// 0011): a watermark with its join, a pull request with a label, a review
// and a commit, and an older pull request's link (#103), so that every
// table of a run's GitHub data holds a row of it, which retention must
// delete with the run.
func retentionForge(t *testing.T, s *sqlite.Store, id int64) {
	t.Helper()
	ctx := t.Context()
	repo := domain.ForgeRepo{ID: "R_api", Key: "https://github.com/acme/api.git", Owner: "acme", Name: "api"}
	if err := s.PutRunWatermarks(ctx, id, []domain.RunWatermark{
		{ForgeWatermark: domain.ForgeWatermark{Repo: repo.ID}, Name: "acme/api", Keys: []string{"local:api"}},
	}); err != nil {
		t.Fatal(err)
	}
	pull := domain.PullRequest{ID: "PR_1", Repo: repo.ID, Number: 1, State: domain.PullOpen, Labels: []string{"bug"},
		Reviews: []domain.Review{{ID: "PRR_1", Author: domain.ForgeUser{Login: "ada"}, State: domain.ReviewApproved}},
		Commits: []domain.ForgeCommit{{SHA: sha('a')}}}
	link := domain.ForgeLink{Repo: repo.ID, ForgeCommit: domain.ForgeCommit{SHA: sha('b'), AuthorLogin: "ada"}}
	if err := s.PutRunForge(ctx, id, []domain.ForgeRepo{repo}, []domain.PullRequest{pull}, []domain.ForgeLink{link}); err != nil {
		t.Fatal(err)
	}
}

// orphanRunRows names each table with a run_id column, every table whose
// rows a run owns, that holds rows of a run the file no longer has, with
// their count. The tables come from the schema itself, so a table a later
// migration adds without ON DELETE CASCADE fails here too.
func orphanRunRows(t *testing.T, raw *sql.DB) []string {
	t.Helper()
	ctx := t.Context()
	rows, err := raw.QueryContext(ctx, `
SELECT m.name FROM sqlite_master AS m, pragma_table_info(m.name) AS c
WHERE m.type = 'table' AND c.name = 'run_id' ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tables, "forge_snapshot_commits") {
		t.Fatalf("the tables with a run_id column are %v, which lacks the forge snapshot's", tables)
	}
	var out []string
	for _, table := range tables {
		var n int
		if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE run_id NOT IN (SELECT id FROM runs)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%s: %d", table, n))
		}
	}
	return out
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

// readOnlyDirectory reads a copy of the facts database at path, whose
// latest scan run is id, in a directory the test then makes read-only, and
// one whose write-ahead log holds a commit.
func readOnlyDirectory(t *testing.T, path string, id int64) {
	t.Helper()
	ctx := t.Context()
	lock := func(dir string) {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	copyFile := func(from, to string) {
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clean := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(clean, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFile(path, filepath.Join(clean, "facts.db"))
	lock(clean)
	for name, open := range map[string]func(context.Context, string) (*sqlite.Store, error){
		"OpenExisting": sqlite.OpenExisting, "OpenReadOnly": sqlite.OpenReadOnly,
	} {
		r, err := open(ctx, filepath.Join(clean, "facts.db"))
		if err != nil {
			t.Errorf("%s(a facts database in a read-only directory) = %v", name, err)
			continue
		}
		if run, err := r.LatestRun(ctx, domain.RunKindScan); err != nil || run.ID != id {
			t.Errorf("%s in a read-only directory: LatestRun = %d, %v; want run %d", name, run.ID, err, id)
		}
		_ = r.Close()
	}
	if entries, _ := os.ReadDir(clean); len(entries) != 1 {
		t.Errorf("the read-only directory holds %d files after reading, want facts.db alone", len(entries))
	}

	// Another user may write the directory this reader cannot, as the owner
	// of a team server's copy scans into it. A writer that holds commits
	// in its log is refused, not read without the locks that keep it out,
	// and once it has checkpointed and closed, the same reader reads what
	// it wrote.
	r, err := sqlite.OpenReadOnly(ctx, filepath.Join(clean, "facts.db"))
	if err != nil {
		t.Fatalf("OpenReadOnly(a facts database in a read-only directory) = %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := os.Chmod(clean, 0o755); err != nil { // the writer's permission, not the reader's
		t.Fatal(err)
	}
	w := openRaw(t, filepath.Join(clean, "facts.db"))
	execRaw(t, w, `PRAGMA wal_autocheckpoint = 0`, `CREATE TABLE scratch (a)`)
	var usage *domain.UsageError
	if _, err := r.LatestRun(ctx, domain.RunKindScan); !errors.As(err, &usage) || !strings.Contains(usage.Msg, "facts.db-wal") {
		t.Errorf("an immutable reader's LatestRun while a writer's log holds a commit = %v, want a *domain.UsageError that names facts.db-wal", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if run, err := r.LatestRun(ctx, domain.RunKindScan); err != nil || run.ID != id {
		t.Errorf("an immutable reader's LatestRun after the writer closed = %d, %v; want run %d", run.ID, err, id)
	}

	// A commit left in the write-ahead log, as a writer that has not
	// checkpointed leaves it: copied while the writer holds it open, with
	// no shared-memory file beside it.
	src := filepath.Join(t.TempDir(), "wal.db")
	copyFile(path, src)
	w = openRaw(t, src)
	execRaw(t, w, `PRAGMA wal_autocheckpoint = 0`, `CREATE TABLE scratch (a)`)
	walled := filepath.Join(t.TempDir(), "walled")
	if err := os.Mkdir(walled, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFile(src, filepath.Join(walled, "facts.db"))
	copyFile(src+"-wal", filepath.Join(walled, "facts.db-wal"))
	lock(walled)
	if r, err := sqlite.OpenExisting(ctx, filepath.Join(walled, "facts.db")); !errors.As(err, &usage) ||
		!strings.Contains(usage.Msg, "facts.db-wal") || strings.Contains(usage.Msg, "cannot be written:") {
		if err == nil {
			_ = r.Close()
		}
		t.Errorf("OpenExisting(a read-only directory whose log holds a commit) = %v, want a *domain.UsageError that names facts.db-wal", err)
	}
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
