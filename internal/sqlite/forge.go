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
// migrations/0002_forge.sql creates (migration 1 is #8's schema). Keeping
// them here leaves store.go to #8.
//
// The replica is upserted by node id, and a pull request is an aggregate:
// storing one deletes the labels, reviews and commits the replica held for
// it and inserts the new ones, in the transaction that also raises the
// repository's watermark. A page and its watermark therefore land together
// or not at all, which is what makes a sync resumable.

var _ domain.ForgeStore = (*Store)(nil)

// PutForgeRepos upserts the repositories a sync is about to bring up to
// date, by ID.
func (s *Store) PutForgeRepos(ctx context.Context, repos []domain.ForgeRepo) error {
	return s.forgeTx(ctx, func(tx *sql.Tx) error {
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
	}, "storing forge repositories")
}

// ForgeWatermark returns a repository's watermark, with zero times when the
// replica has none.
func (s *Store) ForgeWatermark(ctx context.Context, repo string) (domain.ForgeWatermark, error) {
	wm := domain.ForgeWatermark{Repo: repo}
	var updated, synced sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT updated, synced FROM forge_watermarks WHERE repo = ?`, repo).Scan(&updated, &synced)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return wm, nil
	case err != nil:
		return wm, fmt.Errorf("sqlite: reading the forge watermark of %s: %w", repo, err)
	}
	wm.Updated, wm.Synced = forgeTime(updated), forgeTime(synced)
	return wm, nil
}

// PutForgePage stores a page in one transaction: each pull request replaces
// what the replica held for it, and the repository's watermark rises to
// page.Updated. It never falls, so a page from an older pass cannot undo a
// newer one.
func (s *Store) PutForgePage(ctx context.Context, page *domain.ForgePage) error {
	return s.forgeTx(ctx, func(tx *sql.Tx) error {
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
		_, err = tx.ExecContext(ctx, `
INSERT INTO forge_watermarks (repo, updated, synced) VALUES (?, ?, NULL)
ON CONFLICT (repo) DO UPDATE SET updated = CASE
    WHEN excluded.updated IS NOT NULL
         AND (forge_watermarks.updated IS NULL OR excluded.updated > forge_watermarks.updated)
    THEN excluded.updated ELSE forge_watermarks.updated END`,
			page.Repo, forgeUnix(page.Updated))
		return err
	}, "storing a page of forge pull requests")
}

// MarkForgeSynced records that the sync that started at `at` brought the
// repository's pull requests up to date.
func (s *Store) MarkForgeSynced(ctx context.Context, repo string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO forge_watermarks (repo, updated, synced) VALUES (?, NULL, ?)
ON CONFLICT (repo) DO UPDATE SET synced = excluded.synced`, repo, forgeUnix(at))
	if err != nil {
		return fmt.Errorf("sqlite: marking %s synced: %w", repo, err)
	}
	return nil
}

// LoadForge returns the replica's repositories and watermarks, and every
// pull request last updated at or after since (all of them for a zero
// since), sorted like domain.ForgeFacts; nil when the replica holds no
// repository. It reads in one transaction, so a sync writing at the same
// time is seen whole or not at all.
func (s *Store) LoadForge(ctx context.Context, since time.Time) (*domain.ForgeFacts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("sqlite: loading the forge replica: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	f, err := forgeLoad(ctx, tx, since)
	if err != nil {
		return nil, fmt.Errorf("sqlite: loading the forge replica: %w", err)
	}
	return f, nil
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
	if err := forgeRows(ctx, tx, `SELECT repo, updated, synced FROM forge_watermarks ORDER BY repo`,
		nil, func(rows *sql.Rows) error {
			wm := domain.ForgeWatermark{}
			var updated, synced sql.NullInt64
			if err := rows.Scan(&wm.Repo, &updated, &synced); err != nil {
				return err
			}
			wm.Updated, wm.Synced = forgeTime(updated), forgeTime(synced)
			f.Watermarks = append(f.Watermarks, wm)
			return nil
		}); err != nil {
		return nil, err
	}

	bound := forgeUnix(since)
	args := []any{bound, bound}
	index := map[string]int{} // pull request id → position in f.PullRequests
	if err := forgeRows(ctx, tx, forgeSelectPulls, args, func(rows *sql.Rows) error {
		var p domain.PullRequest
		var authorBot, draft int64
		var created, updated, closed, merged sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Repo, &p.Number, &p.Title, &p.Author.Login, &authorBot, &p.State,
			&draft, &p.BaseBranch, &created, &updated, &closed, &merged,
			&p.MergeCommit.SHA, &p.MergeCommit.AuthorEmail, &p.MergeCommit.AuthorLogin); err != nil {
			return err
		}
		p.Author.Bot, p.Draft = authorBot != 0, draft != 0
		p.Created, p.Updated, p.Closed, p.Merged = forgeTime(created), forgeTime(updated), forgeTime(closed), forgeTime(merged)
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
		var submitted sql.NullInt64
		if err := rows.Scan(&id, &r.ID, &r.Author.Login, &authorBot, &r.State, &submitted,
			&r.BodyLength, &r.Comments); err != nil {
			return err
		}
		r.Author.Bot, r.Submitted = authorBot != 0, forgeTime(submitted)
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
    merge_author_login = excluded.merge_author_login`},
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

// put replaces one pull request and everything the replica held for it.
func (w *forgePullWriter) put(ctx context.Context, p *domain.PullRequest) error {
	if _, err := w.upsert.ExecContext(ctx, p.ID, p.Repo, p.Number, p.Title, p.Author.Login, forgeFlag(p.Author.Bot),
		p.State, forgeFlag(p.Draft), p.BaseBranch, forgeUnix(p.Created), forgeUnix(p.Updated), forgeUnix(p.Closed),
		forgeUnix(p.Merged), p.MergeCommit.SHA, p.MergeCommit.AuthorEmail, p.MergeCommit.AuthorLogin); err != nil {
		return err
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
		if _, err := w.review.ExecContext(ctx, r.ID, p.ID, r.Author.Login, forgeFlag(r.Author.Bot), r.State,
			forgeUnix(r.Submitted), r.BodyLength, r.Comments); err != nil {
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

// forgeTx runs fn in a write transaction and commits it, or rolls it back
// when fn fails. what names the operation in the error.
func (s *Store) forgeTx(ctx context.Context, fn func(*sql.Tx) error, what string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
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

// forgeUnix stores a time as Unix seconds, and the zero time as NULL.
func forgeUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// forgeTime is the inverse of forgeUnix: NULL is the zero time, and any other
// value is a time in UTC.
func forgeTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).UTC()
}

// forgeFlag stores a bool as the 0 or 1 a STRICT INTEGER column takes.
func forgeFlag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
