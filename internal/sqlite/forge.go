package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file belongs to the GitHub sync (#14). It implements
// domain.ForgeStore on Store: the forge replica, in the tables that
// migrations/0002_forge.sql creates (migration 1 is #8's schema), and what
// each run read of it, with the join migrations/0003_forge_run_join.sql
// adds. Keeping them here leaves store.go to #8.
//
// The replica is upserted by node id, and a pull request is an aggregate:
// storing one deletes the labels, reviews and commits the replica held for
// it and inserts the new ones, in the transaction that also raises the
// repository's watermark. A page and its watermark therefore land together
// or not at all, which is what makes a sync resumable. A pull request is
// replaced only by a copy updated at the same time or later, so two syncs
// that overlap cannot undo each other: the replica keeps the newest copy
// whichever page commits last.

var _ domain.ForgeStore = (*Store)(nil)

// PutForgeRepos upserts the repositories a sync is about to bring up to
// date, by ID.
func (s *Store) PutForgeRepos(ctx context.Context, repos []domain.ForgeRepo) error {
	return s.forgeTx(ctx, "storing forge repositories", func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
INSERT INTO forge_repos (id, key, owner, name, default_branch, archived, fork)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    key = excluded.key, owner = excluded.owner, name = excluded.name,
    default_branch = excluded.default_branch, archived = excluded.archived, fork = excluded.fork`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, r := range repos {
			if _, err := stmt.ExecContext(ctx, r.ID, r.Key, r.Owner, r.Name, r.DefaultBranch,
				forgeFlag(r.Archived), forgeFlag(r.Fork)); err != nil {
				return fmt.Errorf("repository %s/%s: %w", r.Owner, r.Name, err)
			}
		}
		return nil
	})
}

// ForgeWatermark returns a repository's watermark, with zero times when the
// replica has none.
func (s *Store) ForgeWatermark(ctx context.Context, repo string) (domain.ForgeWatermark, error) {
	wm := domain.ForgeWatermark{Repo: repo}
	err := s.forgeRead(ctx, "reading the forge watermark of "+repo, func(tx *sql.Tx) error {
		var c forgeMarkCols
		err := tx.QueryRowContext(ctx,
			`SELECT `+forgeMarkSelect+` FROM forge_watermarks WHERE repo = ?`, repo).Scan(c.dest()...)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return c.decode(&wm)
	})
	return wm, err
}

// forgeMarkSelect names a watermark's columns, in forge_watermarks and
// forge_run_watermarks alike, in the order forgeMarkCols scans them.
const forgeMarkSelect = `updated, synced, since, missing, pending`

// forgeMarkCols holds a watermark's columns as scanned.
type forgeMarkCols struct {
	updated, synced, since sql.NullString
	missing, pending       int64
}

func (c *forgeMarkCols) dest() []any {
	return []any{&c.updated, &c.synced, &c.since, &c.missing, &c.pending}
}

// decode fills wm's times and completeness from the columns, leaving Repo.
func (c *forgeMarkCols) decode(wm *domain.ForgeWatermark) error {
	wm.Missing, wm.Pending = int(c.missing), c.pending != 0
	return forgeDecodeTimes([]*time.Time{&wm.Updated, &wm.Synced, &wm.From}, c.updated, c.synced, c.since)
}

// PutForgePage stores a page in one transaction: each pull request
// replaces what the replica held for it, unless the replica's copy was
// updated later. A page that completes its pass (page.Updated set) raises
// the repository's watermark to page.Updated and clears pending; any other
// page sets pending and leaves the watermark as it was, so a pass that stops
// halfway moves it nowhere (#101). The watermark never falls, so a page from
// an older pass cannot undo a newer one.
func (s *Store) PutForgePage(ctx context.Context, page *domain.ForgePage) error {
	return s.forgeTx(ctx, "storing a page of forge pull requests", func(tx *sql.Tx) error {
		w, err := newForgePullWriter(ctx, tx)
		if err != nil {
			return err
		}
		defer w.close()
		for i := range page.PullRequests {
			p := &page.PullRequests[i]
			if p.Repo != page.Repo {
				return fmt.Errorf("pull request %s belongs to repository %s, not %s", p.ID, p.Repo, page.Repo)
			}
			if err := w.put(ctx, p); err != nil {
				return fmt.Errorf("pull request %s: %w", p.ID, err)
			}
		}
		updated, err := forgeEncodeTime(page.Updated)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO forge_watermarks (repo, updated, synced, pending) VALUES (?, ?, NULL, ?)
ON CONFLICT (repo) DO UPDATE SET updated = CASE
    WHEN excluded.updated IS NOT NULL
         AND (forge_watermarks.updated IS NULL OR excluded.updated > forge_watermarks.updated)
    THEN excluded.updated ELSE forge_watermarks.updated END,
    pending = excluded.pending`,
			page.Repo, updated, forgeFlag(page.Updated.IsZero()))
		return err
	})
}

// MarkForgeSynced records that the sync that started at `at` brought the
// repository's pull requests up to date.
func (s *Store) MarkForgeSynced(ctx context.Context, repo string, at time.Time) error {
	return s.forgeTx(ctx, "marking "+repo+" synced", func(tx *sql.Tx) error {
		synced, err := forgeEncodeTime(at)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO forge_watermarks (repo, updated, synced) VALUES (?, NULL, ?)
ON CONFLICT (repo) DO UPDATE SET synced = excluded.synced`, repo, synced)
		return err
	})
}

// MarkForgeChecked records what a sync that started at `at` found once its
// passes completed: the replica is complete from `from`, the completeness
// check counted `missing` pull requests it lacks, and the repository is
// synced as of `at`. The three move together, in one transaction, so a
// reader never sees a new "as of" beside an old check.
func (s *Store) MarkForgeChecked(ctx context.Context, repo string, at, from time.Time, missing int) error {
	if missing < 0 {
		return fmt.Errorf("sqlite: marking %s checked: %d missing pull requests", repo, missing)
	}
	return s.forgeTx(ctx, "marking "+repo+" checked", func(tx *sql.Tx) error {
		times, err := forgeEncodeTimes(at, from)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO forge_watermarks (repo, updated, synced, since, missing) VALUES (?, NULL, ?, ?, ?)
ON CONFLICT (repo) DO UPDATE SET synced = excluded.synced, since = excluded.since, missing = excluded.missing`,
			repo, times[0], times[1], missing)
		return err
	})
}

// CountForgePulls is how many of the repository's pull requests the replica
// holds as last updated at or after since, all of them for a zero since.
func (s *Store) CountForgePulls(ctx context.Context, repo string, since time.Time) (int, error) {
	bound, err := forgeEncodeTime(since)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.forgeRead(ctx, "counting the forge pull requests of "+repo, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM forge_pull_requests WHERE repo = ? AND (? IS NULL OR updated >= ?)`,
			repo, bound, bound).Scan(&n)
	})
	return n, err
}

// LoadForge returns the replica's repositories and watermarks, and every
// pull request last updated at or after since (all of them for a zero
// since), sorted like domain.ForgeFacts; nil when the replica holds no
// repository. It reads in one transaction, so a sync writing at the same
// time is seen whole or not at all.
func (s *Store) LoadForge(ctx context.Context, since time.Time) (*domain.ForgeFacts, error) {
	var f *domain.ForgeFacts
	err := s.forgeRead(ctx, "loading the forge replica", func(tx *sql.Tx) error {
		var err error
		f, err = forgeLoad(ctx, tx, since)
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// PutRunWatermarks records the watermarks a running run read with
// LoadForge, each with its name and the run's repositories it joined,
// replacing any it recorded before. They are deleted with the run (ON
// DELETE CASCADE), by retention or as a crash's leftover.
func (s *Store) PutRunWatermarks(ctx context.Context, runID int64, watermarks []domain.RunWatermark) error {
	return s.forgeTx(ctx, fmt.Sprintf("recording the forge watermarks of run %d", runID), func(tx *sql.Tx) error {
		if err := claimRun(ctx, tx, runID); err != nil {
			return err
		}
		// The join rows go with their watermarks (ON DELETE CASCADE).
		if _, err := tx.ExecContext(ctx, `DELETE FROM forge_run_watermarks WHERE run_id = ?`, runID); err != nil {
			return err
		}
		mark, err := tx.PrepareContext(ctx, `
INSERT INTO forge_run_watermarks (run_id, repo, name, updated, synced, since, missing, pending)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = mark.Close() }()
		join, err := tx.PrepareContext(ctx, `INSERT INTO forge_run_repos (run_id, repo, key) VALUES (?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = join.Close() }()
		for _, wm := range watermarks {
			if len(wm.Keys) == 0 {
				// A reader takes a watermark without a join for one recorded
				// before migration 3, whose repositories it cannot tell.
				return fmt.Errorf("watermark of %s: it joins none of the run's repositories", wm.Repo)
			}
			times, err := forgeEncodeTimes(wm.Updated, wm.Synced, wm.From)
			if err != nil {
				return fmt.Errorf("watermark of %s: %w", wm.Repo, err)
			}
			if _, err := mark.ExecContext(ctx, runID, wm.Repo, wm.Name, times[0], times[1], times[2],
				wm.Missing, forgeFlag(wm.Pending)); err != nil {
				return fmt.Errorf("watermark of %s: %w", wm.Repo, err)
			}
			for _, key := range wm.Keys {
				if _, err := join.ExecContext(ctx, runID, wm.Repo, key); err != nil {
					return fmt.Errorf("watermark of %s, repository %s: %w", wm.Repo, key, err)
				}
			}
		}
		return nil
	})
}

// RunWatermarks returns the watermarks a complete or partial run recorded,
// each with its name and its join, sorted by repository; nil when it
// recorded none, and domain.ErrNotFound for a run that is unknown, running
// or failed.
func (s *Store) RunWatermarks(ctx context.Context, runID int64) ([]domain.RunWatermark, error) {
	var out []domain.RunWatermark
	err := s.forgeRead(ctx, fmt.Sprintf("reading the forge watermarks of run %d", runID), func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, runID).Scan(&status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("run %d: %w", runID, domain.ErrNotFound)
		case err != nil:
			return err
		case status != string(domain.RunStatusComplete) && status != string(domain.RunStatusPartial):
			return fmt.Errorf("run %d is %s: %w", runID, status, domain.ErrNotFound)
		}
		if err := forgeRows(ctx, tx, `
SELECT repo, `+forgeMarkSelect+`, name FROM forge_run_watermarks WHERE run_id = ? ORDER BY repo`,
			[]any{runID}, func(rows *sql.Rows) error {
				var wm domain.RunWatermark
				var c forgeMarkCols
				if err := rows.Scan(append(append([]any{&wm.Repo}, c.dest()...), &wm.Name)...); err != nil {
					return err
				}
				if err := c.decode(&wm.ForgeWatermark); err != nil {
					return err
				}
				out = append(out, wm)
				return nil
			}); err != nil {
			return err
		}
		index := make(map[string]int, len(out)) // ForgeRepo.ID → position in out
		for i, wm := range out {
			index[wm.Repo] = i
		}
		return forgeRows(ctx, tx, `SELECT repo, key FROM forge_run_repos WHERE run_id = ? ORDER BY repo, key`,
			[]any{runID}, func(rows *sql.Rows) error {
				var repo, key string
				if err := rows.Scan(&repo, &key); err != nil {
					return err
				}
				i, ok := index[repo]
				if !ok {
					return fmt.Errorf("a join row names forge repository %s, which has no watermark", repo)
				}
				out[i].Keys = append(out[i].Keys, key)
				return nil
			})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// The SELECT lists of LoadForge. The pull requests after since are named
// once, so that every child query reads the same set.
const (
	forgeSincePulls  = `SELECT id FROM forge_pull_requests WHERE ? IS NULL OR updated >= ?`
	forgeSelectPulls = `
SELECT id, repo, number, title, author, author_bot, state, draft, base_branch,
       created, updated, closed, merged, merge_sha, merge_author_email, merge_author_login
FROM forge_pull_requests WHERE id IN (` + forgeSincePulls + `) ORDER BY repo, number, id`
	forgeSelectLabels = `
SELECT pull, name FROM forge_pull_labels WHERE pull IN (` + forgeSincePulls + `) ORDER BY pull, name`
	forgeSelectReviews = `
SELECT pull, id, author, author_bot, state, submitted, body_length, comments
FROM forge_reviews WHERE pull IN (` + forgeSincePulls + `) ORDER BY pull, submitted, id`
	forgeSelectCommits = `
SELECT pull, sha, author_email, author_login
FROM forge_pull_commits WHERE pull IN (` + forgeSincePulls + `) ORDER BY pull, position`
)

func forgeLoad(ctx context.Context, tx *sql.Tx, since time.Time) (*domain.ForgeFacts, error) {
	var f domain.ForgeFacts
	if err := forgeRows(ctx, tx, `
SELECT id, key, owner, name, default_branch, archived, fork FROM forge_repos ORDER BY key, id`,
		nil, func(rows *sql.Rows) error {
			var r domain.ForgeRepo
			var archived, fork int64
			if err := rows.Scan(&r.ID, &r.Key, &r.Owner, &r.Name, &r.DefaultBranch, &archived, &fork); err != nil {
				return err
			}
			r.Archived, r.Fork = archived != 0, fork != 0
			f.Repos = append(f.Repos, r)
			return nil
		}); err != nil {
		return nil, err
	}
	if len(f.Repos) == 0 {
		return nil, nil
	}
	if err := forgeRows(ctx, tx, `SELECT repo, `+forgeMarkSelect+` FROM forge_watermarks ORDER BY repo`,
		nil, func(rows *sql.Rows) error {
			wm, err := forgeScanWatermark(rows)
			f.Watermarks = append(f.Watermarks, wm)
			return err
		}); err != nil {
		return nil, err
	}

	bound, err := forgeEncodeTime(since)
	if err != nil {
		return nil, err
	}
	args := []any{bound, bound}
	index := map[string]int{} // pull request id → position in f.PullRequests
	if err := forgeRows(ctx, tx, forgeSelectPulls, args, func(rows *sql.Rows) error {
		var p domain.PullRequest
		var authorBot, draft int64
		var created, updated, closed, merged sql.NullString
		if err := rows.Scan(&p.ID, &p.Repo, &p.Number, &p.Title, &p.Author.Login, &authorBot, &p.State,
			&draft, &p.BaseBranch, &created, &updated, &closed, &merged,
			&p.MergeCommit.SHA, &p.MergeCommit.AuthorEmail, &p.MergeCommit.AuthorLogin); err != nil {
			return err
		}
		p.Author.Bot, p.Draft = authorBot != 0, draft != 0
		if err := forgeDecodeTimes([]*time.Time{&p.Created, &p.Updated, &p.Closed, &p.Merged},
			created, updated, closed, merged); err != nil {
			return fmt.Errorf("pull request %s: %w", p.ID, err)
		}
		index[p.ID] = len(f.PullRequests)
		f.PullRequests = append(f.PullRequests, p)
		return nil
	}); err != nil {
		return nil, err
	}
	pull := func(id string) (*domain.PullRequest, error) {
		i, ok := index[id]
		if !ok {
			return nil, fmt.Errorf("a child row names pull request %s, which the query did not return", id)
		}
		return &f.PullRequests[i], nil
	}
	if err := forgeRows(ctx, tx, forgeSelectLabels, args, func(rows *sql.Rows) error {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		p, err := pull(id)
		if err != nil {
			return err
		}
		p.Labels = append(p.Labels, name)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := forgeRows(ctx, tx, forgeSelectReviews, args, func(rows *sql.Rows) error {
		var id string
		var r domain.Review
		var authorBot int64
		var submitted sql.NullString
		if err := rows.Scan(&id, &r.ID, &r.Author.Login, &authorBot, &r.State, &submitted,
			&r.BodyLength, &r.Comments); err != nil {
			return err
		}
		r.Author.Bot = authorBot != 0
		if err := forgeDecodeTimes([]*time.Time{&r.Submitted}, submitted); err != nil {
			return fmt.Errorf("review %s: %w", r.ID, err)
		}
		p, err := pull(id)
		if err != nil {
			return err
		}
		p.Reviews = append(p.Reviews, r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := forgeRows(ctx, tx, forgeSelectCommits, args, func(rows *sql.Rows) error {
		var id string
		var c domain.ForgeCommit
		if err := rows.Scan(&id, &c.SHA, &c.AuthorEmail, &c.AuthorLogin); err != nil {
			return err
		}
		p, err := pull(id)
		if err != nil {
			return err
		}
		p.Commits = append(p.Commits, c)
		return nil
	}); err != nil {
		return nil, err
	}
	return &f, nil
}

// forgeScanWatermark scans a row of repo and forgeMarkSelect's columns.
func forgeScanWatermark(rows *sql.Rows) (domain.ForgeWatermark, error) {
	var wm domain.ForgeWatermark
	var c forgeMarkCols
	if err := rows.Scan(append([]any{&wm.Repo}, c.dest()...)...); err != nil {
		return wm, err
	}
	return wm, c.decode(&wm)
}

// forgePullWriter holds the prepared statements that replace pull requests.
type forgePullWriter struct {
	stmts                                    []*sql.Stmt
	upsert, delLabels, delReviews, delCommit *sql.Stmt
	label, review, commit                    *sql.Stmt
}

func newForgePullWriter(ctx context.Context, tx *sql.Tx) (*forgePullWriter, error) {
	w := &forgePullWriter{}
	for _, p := range []struct {
		stmt **sql.Stmt
		sql  string
	}{
		// The WHERE makes the replacement monotonic: a copy older than the
		// replica's, from a slower sync that read it first, changes nothing.
		// The times sort as text (forgeTimeLayout).
		{&w.upsert, `
INSERT INTO forge_pull_requests (id, repo, number, title, author, author_bot, state, draft, base_branch,
    created, updated, closed, merged, merge_sha, merge_author_email, merge_author_login)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    repo = excluded.repo, number = excluded.number, title = excluded.title,
    author = excluded.author, author_bot = excluded.author_bot, state = excluded.state,
    draft = excluded.draft, base_branch = excluded.base_branch, created = excluded.created,
    updated = excluded.updated, closed = excluded.closed, merged = excluded.merged,
    merge_sha = excluded.merge_sha, merge_author_email = excluded.merge_author_email,
    merge_author_login = excluded.merge_author_login
WHERE forge_pull_requests.updated IS NULL OR excluded.updated >= forge_pull_requests.updated`},
		{&w.delLabels, `DELETE FROM forge_pull_labels WHERE pull = ?`},
		{&w.delReviews, `DELETE FROM forge_reviews WHERE pull = ?`},
		{&w.delCommit, `DELETE FROM forge_pull_commits WHERE pull = ?`},
		{&w.label, `INSERT INTO forge_pull_labels (pull, name) VALUES (?, ?)`},
		{&w.review, `
INSERT INTO forge_reviews (id, pull, author, author_bot, state, submitted, body_length, comments)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`},
		{&w.commit, `
INSERT INTO forge_pull_commits (pull, position, sha, author_email, author_login) VALUES (?, ?, ?, ?, ?)`},
	} {
		stmt, err := tx.PrepareContext(ctx, p.sql)
		if err != nil {
			w.close()
			return nil, err
		}
		*p.stmt = stmt
		w.stmts = append(w.stmts, stmt)
	}
	return w, nil
}

func (w *forgePullWriter) close() {
	for _, s := range w.stmts {
		_ = s.Close()
	}
}

// put replaces one pull request and everything the replica held for it,
// unless the replica holds a copy updated later, which it keeps whole.
func (w *forgePullWriter) put(ctx context.Context, p *domain.PullRequest) error {
	times, err := forgeEncodeTimes(p.Created, p.Updated, p.Closed, p.Merged)
	if err != nil {
		return err
	}
	res, err := w.upsert.ExecContext(ctx, p.ID, p.Repo, p.Number, p.Title, p.Author.Login, forgeFlag(p.Author.Bot),
		p.State, forgeFlag(p.Draft), p.BaseBranch, times[0], times[1], times[2], times[3],
		p.MergeCommit.SHA, p.MergeCommit.AuthorEmail, p.MergeCommit.AuthorLogin)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err // n == 0: the replica's copy is newer, so its children stay too
	}
	for _, del := range []*sql.Stmt{w.delLabels, w.delReviews, w.delCommit} {
		if _, err := del.ExecContext(ctx, p.ID); err != nil {
			return err
		}
	}
	for _, name := range p.Labels {
		if _, err := w.label.ExecContext(ctx, p.ID, name); err != nil {
			return fmt.Errorf("label %q: %w", name, err)
		}
	}
	for _, r := range p.Reviews {
		submitted, err := forgeEncodeTime(r.Submitted)
		if err != nil {
			return fmt.Errorf("review %s: %w", r.ID, err)
		}
		if _, err := w.review.ExecContext(ctx, r.ID, p.ID, r.Author.Login, forgeFlag(r.Author.Bot), r.State,
			submitted, r.BodyLength, r.Comments); err != nil {
			return fmt.Errorf("review %s: %w", r.ID, err)
		}
	}
	for i, c := range p.Commits {
		if _, err := w.commit.ExecContext(ctx, p.ID, i, c.SHA, c.AuthorEmail, c.AuthorLogin); err != nil {
			return fmt.Errorf("commit %s: %w", c.SHA, err)
		}
	}
	return nil
}

// forgeTx runs fn in one write transaction and commits it, or rolls it back
// when fn fails; what names the operation in the error. Every write to the
// forge tables goes through it.
//
// It runs on the Store's write path (Store.write): one IMMEDIATE transaction
// on the writer connection that waits, up to lockWait, for another
// process's write lock and renews the leases of the runs this Store began,
// so a sync waits for a scan's commit instead of failing beside it, and a
// long sync keeps its own run alive.
func (s *Store) forgeTx(ctx context.Context, what string, fn func(*sql.Tx) error) error {
	if err := s.write(ctx, fn); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	return nil
}

// forgeRead runs fn in one read-only transaction, so that every query fn
// makes reads the same snapshot; what names the operation in the error.
// Every read of the forge tables goes through it.
//
// It runs on the Store's read path (Store.read), the query_only reader
// pool, so a long LoadForge (scan, the MCP server) never holds the single
// writer connection.
func (s *Store) forgeRead(ctx context.Context, what string, fn func(*sql.Tx) error) error {
	if err := s.read(ctx, fn); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	return nil
}

// forgeRows runs a SELECT in tx and calls fn for each row.
func forgeRows(ctx context.Context, tx *sql.Tx, q string, args []any, fn func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// forgeTimeLayout is how the forge tables store a time: migration 1's
// encoding (#8's timeLayout), RFC 3339 in UTC with exactly nine fractional
// digits, so that facts.db has one time encoding, a time round-trips to the
// nanosecond, and the text sorts like the time. #13 may fold these helpers
// into #8's encodeTime and decodeTime.
const forgeTimeLayout = "2006-01-02T15:04:05.000000000Z"

// forgeEncodeTime returns t as UTC text in forgeTimeLayout, or nil (NULL)
// for the zero time. A year outside 0000 to 9999 would not sort, so it is an
// error.
func forgeEncodeTime(t time.Time) (any, error) {
	if t.IsZero() {
		return nil, nil
	}
	u := t.UTC()
	if y := u.Year(); y < 0 || y > 9999 {
		return nil, fmt.Errorf("time %s is outside the years 0000 to 9999 that facts.db stores", t.Format(time.RFC3339Nano))
	}
	return u.Format(forgeTimeLayout), nil
}

// forgeEncodeTimes encodes each time, stopping at the first one facts.db
// cannot store.
func forgeEncodeTimes(ts ...time.Time) ([]any, error) {
	out := make([]any, len(ts))
	for i, t := range ts {
		v, err := forgeEncodeTime(t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// forgeDecodeTimes is the inverse of forgeEncodeTime for each column into
// the time dst names: NULL is the zero time, any other value a time in UTC.
func forgeDecodeTimes(dst []*time.Time, cols ...sql.NullString) error {
	for i, c := range cols {
		*dst[i] = time.Time{}
		if !c.Valid {
			continue
		}
		t, err := time.Parse(forgeTimeLayout, c.String)
		if err != nil {
			return fmt.Errorf("stored time %q: %w", c.String, err)
		}
		*dst[i] = t
	}
	return nil
}

// forgeFlag stores a bool as the 0 or 1 a STRICT INTEGER column takes.
func forgeFlag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
