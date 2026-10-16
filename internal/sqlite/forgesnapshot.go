package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file belongs to #38 (ADR 0011): what a scan run keeps of the forge
// replica it read, its forge snapshot, in the tables that
// migrations/0004_forge_snapshot.sql creates. A run computes its numbers
// over its own part of the replica (FactSet.Forge), so it keeps that part
// whole, as it keeps its git facts, and its readers compute over it rather
// than over the replica, which later syncs move on. Its unexported
// identifiers carry the prefix snapshot, as forge.go's carry forge.

// PutRunForge records, for a running run, its forge repositories as the
// replica listed them and every pull request of them it read, whole,
// replacing any it recorded before. A pull request of a repository that is
// not among repos is an error. The rows are deleted with the run (ON DELETE
// CASCADE), by retention or as a crash's leftover.
func (s *Store) PutRunForge(ctx context.Context, runID int64, repos []domain.ForgeRepo, pulls []domain.PullRequest) error {
	known := make(map[string]bool, len(repos))
	for _, r := range repos {
		known[r.ID] = true
	}
	for i := range pulls {
		if p := &pulls[i]; !known[p.Repo] {
			return fmt.Errorf("sqlite.PutRunForge: run %d: pull request %s belongs to forge repository %s, which is none of the run's",
				runID, p.ID, p.Repo)
		}
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := claimRun(ctx, tx, runID); err != nil {
			return err
		}
		for _, table := range snapshotTables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE run_id = ?`, runID); err != nil {
				return fmt.Errorf("clearing %s: %w", table, err)
			}
		}
		return snapshotPut(ctx, tx, runID, repos, pulls)
	})
	if err != nil {
		return fmt.Errorf("sqlite.PutRunForge: run %d: %w", runID, err)
	}
	return nil
}

// snapshotTables are the forge snapshot's tables, children first.
var snapshotTables = []string{
	"forge_snapshot_commits", "forge_snapshot_reviews", "forge_snapshot_labels", "forge_snapshot_pulls", "forge_snapshot_repos",
}

// snapshotPut writes the rows of one run's forge snapshot, each list
// numbered by its position, so RunForge returns it as it was put.
func snapshotPut(ctx context.Context, tx *sql.Tx, runID int64, repos []domain.ForgeRepo, pulls []domain.PullRequest) error {
	repoRows := newInserter(ctx, tx, "forge_snapshot_repos", "run_id", "seq", "id", "key", "owner", "name",
		"default_branch", "archived", "fork")
	for i, r := range repos {
		repoRows.add(runID, i, r.ID, r.Key, r.Owner, r.Name, r.DefaultBranch, encodeBool(r.Archived), encodeBool(r.Fork))
	}
	pullRows := newInserter(ctx, tx, "forge_snapshot_pulls", "run_id", "seq", "id", "repo", "number", "title", "author",
		"author_bot", "state", "draft", "base_branch", "created", "updated", "closed", "merged", "merge_sha",
		"merge_author_email", "merge_author_login")
	labels := newInserter(ctx, tx, "forge_snapshot_labels", "run_id", "pull_seq", "seq", "name")
	reviews := newInserter(ctx, tx, "forge_snapshot_reviews", "run_id", "pull_seq", "seq", "id", "author", "author_bot",
		"state", "submitted", "body_length", "comments")
	commits := newInserter(ctx, tx, "forge_snapshot_commits", "run_id", "pull_seq", "seq", "sha", "author_email",
		"author_login")
	for i := range pulls {
		p := &pulls[i]
		pullRows.add(runID, i, p.ID, p.Repo, p.Number, p.Title, p.Author.Login, encodeBool(p.Author.Bot), p.State,
			encodeBool(p.Draft), p.BaseBranch, pullRows.time(p.Created), pullRows.time(p.Updated), pullRows.time(p.Closed),
			pullRows.time(p.Merged), p.MergeCommit.SHA, p.MergeCommit.AuthorEmail, p.MergeCommit.AuthorLogin)
		for j, name := range p.Labels {
			labels.add(runID, i, j, name)
		}
		for j := range p.Reviews {
			r := &p.Reviews[j]
			reviews.add(runID, i, j, r.ID, r.Author.Login, encodeBool(r.Author.Bot), r.State, reviews.time(r.Submitted),
				r.BodyLength, r.Comments)
		}
		for j, c := range p.Commits {
			commits.add(runID, i, j, c.SHA, c.AuthorEmail, c.AuthorLogin)
		}
	}
	return closeAll(repoRows, pullRows, labels, reviews, commits)
}

// RunForge returns the forge snapshot a complete or partial run kept, with
// the watermarks it recorded as its Watermarks and the join it recorded
// with them (their Keys) as its Join, or nil when it kept no forge
// repository; domain.ErrNotFound for a run that is unknown, running or
// failed. It reads in one transaction, so the snapshot, the watermarks and
// the join come from one state of the file.
func (s *Store) RunForge(ctx context.Context, runID int64) (*domain.ForgeFacts, error) {
	var f *domain.ForgeFacts
	err := s.read(ctx, func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, runID).Scan(&status)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return domain.ErrNotFound
		case err != nil:
			return err
		case status != string(domain.RunStatusComplete) && status != string(domain.RunStatusPartial):
			return fmt.Errorf("it is %s: %w", status, domain.ErrNotFound)
		}
		f, err = snapshotLoad(ctx, tx, runID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite.RunForge: run %d: %w", runID, err)
	}
	return f, nil
}

// snapshotLoad reads one run's forge snapshot table by table, each in its
// primary key's order, so no query sorts.
func snapshotLoad(ctx context.Context, tx *sql.Tx, runID int64) (*domain.ForgeFacts, error) {
	var f domain.ForgeFacts
	args := []any{runID}
	if err := each(ctx, tx, `
SELECT seq, id, key, owner, name, default_branch, archived, fork FROM forge_snapshot_repos WHERE run_id = ? ORDER BY seq`,
		args, func(rows *sql.Rows) error {
			var seq int64
			var r domain.ForgeRepo
			if err := rows.Scan(&seq, &r.ID, &r.Key, &r.Owner, &r.Name, &r.DefaultBranch, &r.Archived, &r.Fork); err != nil {
				return err
			}
			f.Repos = append(f.Repos, r)
			return position(seq, len(f.Repos)-1)
		}); err != nil {
		return nil, fmt.Errorf("reading forge_snapshot_repos: %w", err)
	}
	if len(f.Repos) == 0 {
		return nil, nil
	}
	if err := each(ctx, tx, `SELECT repo, `+forgeMarkSelect+` FROM forge_run_watermarks WHERE run_id = ? ORDER BY repo`,
		args, func(rows *sql.Rows) error {
			wm, err := forgeScanWatermark(rows)
			f.Watermarks = append(f.Watermarks, wm)
			return err
		}); err != nil {
		return nil, fmt.Errorf("reading forge_run_watermarks: %w", err)
	}
	// The join the run recorded with its watermarks (migration 3), which
	// its metrics count by: the one the scan made, never one made again.
	if err := each(ctx, tx, `SELECT repo, key FROM forge_run_repos WHERE run_id = ? ORDER BY repo, key`,
		args, func(rows *sql.Rows) error {
			var repo, key string
			if err := rows.Scan(&repo, &key); err != nil {
				return err
			}
			if f.Join == nil {
				f.Join = map[string][]string{}
			}
			f.Join[repo] = append(f.Join[repo], key)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading forge_run_repos: %w", err)
	}
	if err := each(ctx, tx, `
SELECT seq, id, repo, number, title, author, author_bot, state, draft, base_branch, created, updated, closed, merged,
       merge_sha, merge_author_email, merge_author_login
FROM forge_snapshot_pulls WHERE run_id = ? ORDER BY seq`, args, func(rows *sql.Rows) error {
		var seq int64
		var p domain.PullRequest
		var created, updated, closed, merged sql.NullString
		if err := rows.Scan(&seq, &p.ID, &p.Repo, &p.Number, &p.Title, &p.Author.Login, &p.Author.Bot, &p.State,
			&p.Draft, &p.BaseBranch, &created, &updated, &closed, &merged,
			&p.MergeCommit.SHA, &p.MergeCommit.AuthorEmail, &p.MergeCommit.AuthorLogin); err != nil {
			return err
		}
		var err error
		for _, t := range []struct {
			dst *time.Time
			col sql.NullString
		}{{&p.Created, created}, {&p.Updated, updated}, {&p.Closed, closed}, {&p.Merged, merged}} {
			if *t.dst, err = decodeTime(t.col); err != nil {
				return fmt.Errorf("pull request %s: %w", p.ID, err)
			}
		}
		f.PullRequests = append(f.PullRequests, p)
		return position(seq, len(f.PullRequests)-1)
	}); err != nil {
		return nil, fmt.Errorf("reading forge_snapshot_pulls: %w", err)
	}
	pull := func(seq int64) (*domain.PullRequest, error) { return item(f.PullRequests, seq) }
	if err := each(ctx, tx, `SELECT pull_seq, seq, name FROM forge_snapshot_labels WHERE run_id = ? ORDER BY pull_seq, seq`,
		args, func(rows *sql.Rows) error {
			var pullSeq, seq int64
			var name string
			if err := rows.Scan(&pullSeq, &seq, &name); err != nil {
				return err
			}
			p, err := pull(pullSeq)
			if err != nil {
				return err
			}
			p.Labels = append(p.Labels, name)
			return position(seq, len(p.Labels)-1)
		}); err != nil {
		return nil, fmt.Errorf("reading forge_snapshot_labels: %w", err)
	}
	if err := each(ctx, tx, `
SELECT pull_seq, seq, id, author, author_bot, state, submitted, body_length, comments
FROM forge_snapshot_reviews WHERE run_id = ? ORDER BY pull_seq, seq`, args, func(rows *sql.Rows) error {
		var pullSeq, seq int64
		var r domain.Review
		var submitted sql.NullString
		if err := rows.Scan(&pullSeq, &seq, &r.ID, &r.Author.Login, &r.Author.Bot, &r.State, &submitted,
			&r.BodyLength, &r.Comments); err != nil {
			return err
		}
		var err error
		if r.Submitted, err = decodeTime(submitted); err != nil {
			return fmt.Errorf("review %s: %w", r.ID, err)
		}
		p, err := pull(pullSeq)
		if err != nil {
			return err
		}
		p.Reviews = append(p.Reviews, r)
		return position(seq, len(p.Reviews)-1)
	}); err != nil {
		return nil, fmt.Errorf("reading forge_snapshot_reviews: %w", err)
	}
	if err := each(ctx, tx, `
SELECT pull_seq, seq, sha, author_email, author_login FROM forge_snapshot_commits WHERE run_id = ? ORDER BY pull_seq, seq`,
		args, func(rows *sql.Rows) error {
			var pullSeq, seq int64
			var c domain.ForgeCommit
			if err := rows.Scan(&pullSeq, &seq, &c.SHA, &c.AuthorEmail, &c.AuthorLogin); err != nil {
				return err
			}
			p, err := pull(pullSeq)
			if err != nil {
				return err
			}
			p.Commits = append(p.Commits, c)
			return position(seq, len(p.Commits)-1)
		}); err != nil {
		return nil, fmt.Errorf("reading forge_snapshot_commits: %w", err)
	}
	return &f, nil
}
