package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The write side. A run is written while its status is running, one
// transaction per call, and published by FinishRun. Every Put first claims
// the run inside its transaction (claimRun): a run that is not running,
// because it was published, failed, or deleted as abandoned, takes no more
// rows, so a published run never changes.

// Retention, applied to the runs of one kind each time one of them finishes
// (the grandfather-father-son rotation of backups, with restic's
// --keep-last and --keep-monthly meanings).
const (
	keepLast    = 8  // the newest published runs
	keepMonthly = 12 // the newest published run of each of the most recent months that have one
)

// BeginRun records r as running and returns its id. It stores r's kind,
// times, tool version, policy reference, policy source and options; the id,
// status and finish time are the store's: Run reports the run as begun,
// with the id BeginRun returned, its final status, and the time FinishRun
// recorded. Until FinishRun, every write of this Store renews the run's
// lease (see leases).
func (s *Store) BeginRun(ctx context.Context, r domain.Run) (int64, error) {
	id, err := s.beginRun(ctx, &r)
	if err != nil {
		return 0, fmt.Errorf("sqlite.BeginRun: %w", err)
	}
	s.leases.hold(id)
	return id, nil
}

func (s *Store) beginRun(ctx context.Context, r *domain.Run) (int64, error) {
	if r.Kind == "" {
		return 0, errors.New("the run has no kind")
	}
	times, err := encodeTimes(r.Now, r.Started, time.Now()) // the last is the first heartbeat
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO runs (kind, status, now, started, heartbeat, tool_version, policy_version, policy_sha256, policy_source)
			VALUES (?, 'running', ?, ?, ?, ?, ?, ?, ?)
			RETURNING id`,
			string(r.Kind), times[0], times[1], times[2], r.ToolVersion, r.Policy.Version, r.Policy.SHA256,
			encodeBytes(r.PolicySource),
		).Scan(&id)
		if err != nil {
			return err
		}
		opts := newInserter(ctx, tx, "run_options", "run_id", "name", "value")
		for _, name := range slices.Sorted(maps.Keys(r.Options)) {
			opts.add(id, name, r.Options[name])
		}
		return opts.close()
	})
	return id, err
}

// claimRun checks, inside a write transaction, that the run exists and is
// still running, and renews its lease (runs.heartbeat), so Open in another
// process does not take it for abandoned. A run that does not exist is
// domain.ErrNotFound.
func claimRun(ctx context.Context, tx *sql.Tx, runID int64) error {
	heartbeat, err := encodeTime(time.Now())
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET heartbeat = ? WHERE id = ? AND status = 'running'`, heartbeat, runID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, runID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("run %d: %w", runID, domain.ErrNotFound)
	case err != nil:
		return err
	}
	return fmt.Errorf("run %d is %s, not running: a finished run's facts are final", runID, status)
}

// PutRepoFacts stores one repository's facts for a running run, in one
// transaction. Each repository is stored once per run: a second call with
// the same Repo.Key fails and changes nothing. LoadFacts returns every list
// in the order it was put, and the repositories sorted by key.
func (s *Store) PutRepoFacts(ctx context.Context, runID int64, f *domain.RepoFacts) error {
	if f == nil {
		return errors.New("sqlite.PutRepoFacts: nil facts")
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := claimRun(ctx, tx, runID); err != nil {
			return err
		}
		return putRepo(ctx, tx, runID, f)
	})
	if err != nil {
		return fmt.Errorf("sqlite.PutRepoFacts: repository %s in run %d: %w", f.Repo.Key, runID, err)
	}
	return nil
}

// putRepo writes the run_repos row and every table that hangs off it.
func putRepo(ctx context.Context, tx *sql.Tx, runID int64, f *domain.RepoFacts) error {
	if f.Repo.Key == "" {
		return errors.New("the repository has no key")
	}
	times, err := encodeTimes(f.FirstCommit, f.History.Since, f.History.Until)
	if err != nil {
		return err
	}
	r := &f.Repo
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO run_repos (run_id, repo_key, name, group_name, path, bare, remote, branch, head, shallow, partial,
			status, error, first_commit, history_since, history_until, line_facts, ignore_revs_source, ignore_revs_sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		runID, r.Key, r.Name, r.Group, r.Path, encodeBool(r.Bare), r.Remote, r.Branch, r.Head,
		encodeBool(r.Shallow), encodeBool(r.Partial), f.Status, f.Error, times[0], times[1], times[2],
		encodeBool(f.LineFacts), f.IgnoreRevs.Source, f.IgnoreRevs.SHA256,
	).Scan(&id)
	if err != nil {
		return fmt.Errorf("inserting into run_repos: %w", err)
	}
	for _, put := range []func(context.Context, *sql.Tx, int64, *domain.RepoFacts) error{
		putRepoLists, putCommits, putLandings, putOwners,
	} {
		if err := put(ctx, tx, id, f); err != nil {
			return err
		}
	}
	return nil
}

// putRepoLists writes the repository's flat lists: warnings, path classes,
// file changes, reverts, ignore-revs, blame errors and mailmap aliases.
func putRepoLists(ctx context.Context, tx *sql.Tx, id int64, f *domain.RepoFacts) error {
	warnings := newInserter(ctx, tx, "repo_warnings", "repo_id", "seq", "code", "message", "repo")
	for i, w := range f.Warnings {
		warnings.add(id, i, w.Code, w.Message, w.Repo)
	}
	paths := newInserter(ctx, tx, "path_classes", "repo_id", "seq", "path", "role", "skip", "size", "at_head")
	for i, p := range f.Paths {
		paths.add(id, i, p.Path, string(p.Role), string(p.Skip), p.Size, encodeBool(p.AtHead))
	}
	changes := newInserter(ctx, tx, "commit_files", "repo_id", "seq", "commit_sha", "path", "old_path",
		"added", "removed", "binary", "added_nonblank", "removed_nonblank")
	for i := range f.Changes {
		c := &f.Changes[i]
		changes.add(id, i, c.Commit, c.Path, c.OldPath, c.Added, c.Removed, encodeBool(c.Binary),
			c.AddedNonBlank, c.RemovedNonBlank)
	}
	reverts := newInserter(ctx, tx, "reverts", "repo_id", "seq", "sha", "reverted")
	for i, r := range f.Reverts {
		reverts.add(id, i, r.SHA, r.Reverted)
	}
	revs := newInserter(ctx, tx, "ignore_revs", "repo_id", "seq", "sha")
	for i, sha := range f.IgnoreRevs.Revs {
		revs.add(id, i, sha)
	}
	blameErrors := newInserter(ctx, tx, "blame_errors", "repo_id", "seq", "path", "message")
	for i, e := range f.BlameErrors {
		blameErrors.add(id, i, e.Path, e.Message)
	}
	aliases := newInserter(ctx, tx, "repo_aliases", "repo_id", "seq", "email", "canonical")
	for i, a := range f.Aliases {
		aliases.add(id, i, a.Email, a.Canonical)
	}
	return closeAll(warnings, paths, changes, reverts, revs, blameErrors, aliases)
}

// putCommits writes the commits with their parents and co-authors.
func putCommits(ctx context.Context, tx *sql.Tx, id int64, f *domain.RepoFacts) error {
	commits := newInserter(ctx, tx, "commits", "repo_id", "seq", "sha", "author_name", "author_email",
		"author_time", "commit_time", "subject")
	parents := newInserter(ctx, tx, "commit_parents", "repo_id", "commit_seq", "seq", "sha")
	coAuthors := newInserter(ctx, tx, "commit_coauthors", "repo_id", "commit_seq", "seq", "name", "email")
	for i := range f.Commits {
		c := &f.Commits[i]
		commits.add(id, i, c.SHA, c.Author.Name, c.Author.Email, commits.time(c.AuthorTime),
			commits.time(c.CommitTime), c.Subject)
		for j, p := range c.Parents {
			parents.add(id, i, j, p)
		}
		for j, a := range c.CoAuthors {
			coAuthors.add(id, i, j, a.Name, a.Email)
		}
	}
	return closeAll(commits, parents, coAuthors)
}

// putLandings writes the landings and the landing map.
func putLandings(ctx context.Context, tx *sql.Tx, id int64, f *domain.RepoFacts) error {
	landings := newInserter(ctx, tx, "landings", "repo_id", "seq", "sha", "time", "merge")
	brought := newInserter(ctx, tx, "landing_commits", "repo_id", "landing_seq", "seq", "sha")
	for i := range f.Landings {
		l := &f.Landings[i]
		landings.add(id, i, l.SHA, landings.time(l.Time), encodeBool(l.Merge))
		for j, sha := range l.Commits {
			brought.add(id, i, j, sha)
		}
	}
	return closeAll(landings, brought)
}

// putOwners writes the line owners with their commits' co-authors.
func putOwners(ctx context.Context, tx *sql.Tx, id int64, f *domain.RepoFacts) error {
	owners := newInserter(ctx, tx, "line_owners", "repo_id", "seq", "path", "commit_sha", "orig_path", "author_name",
		"author_email", "author_time", "lines")
	coAuthors := newInserter(ctx, tx, "line_owner_coauthors", "repo_id", "owner_seq", "seq", "name", "email")
	for i := range f.Owners {
		o := &f.Owners[i]
		owners.add(id, i, o.Path, o.Commit, o.OrigPath, o.Author.Name, o.Author.Email, owners.time(o.AuthorTime), o.Lines)
		for j, a := range o.CoAuthors {
			coAuthors.add(id, i, j, a.Name, a.Email)
		}
	}
	return closeAll(owners, coAuthors)
}

// PutRunFacts stores the run's groups and run-level warnings for a running
// run. A second call appends to the first; a group name may appear once
// per run.
func (s *Store) PutRunFacts(ctx context.Context, runID int64, groups []domain.Group, warnings []domain.Warning) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := claimRun(ctx, tx, runID); err != nil {
			return err
		}
		gseq, err := nextSeq(ctx, tx, "run_groups", runID)
		if err != nil {
			return err
		}
		wseq, err := nextSeq(ctx, tx, "run_warnings", runID)
		if err != nil {
			return err
		}
		gs := newInserter(ctx, tx, "run_groups", "run_id", "seq", "name", "path")
		repos := newInserter(ctx, tx, "run_group_repos", "run_id", "group_seq", "seq", "repo_key")
		for i, g := range groups {
			gs.add(runID, gseq+i, g.Name, g.Path)
			for j, key := range g.Repos {
				repos.add(runID, gseq+i, j, key)
			}
		}
		ws := newInserter(ctx, tx, "run_warnings", "run_id", "seq", "code", "message", "repo")
		for i, w := range warnings {
			ws.add(runID, wseq+i, w.Code, w.Message, w.Repo)
		}
		return closeAll(gs, repos, ws)
	})
	if err != nil {
		return fmt.Errorf("sqlite.PutRunFacts: run %d: %w", runID, err)
	}
	return nil
}

// PutMetrics stores metric values and rank intervals for a running run. A
// second call appends to the first, and LoadMetrics returns both lists in
// the order they were put. A NaN is stored as NULL and loads as NaN.
func (s *Store) PutMetrics(ctx context.Context, runID int64, values []domain.MetricValue, ranks []domain.RankInterval) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := claimRun(ctx, tx, runID); err != nil {
			return err
		}
		vseq, err := nextSeq(ctx, tx, "metric_values", runID)
		if err != nil {
			return err
		}
		rseq, err := nextSeq(ctx, tx, "rank_intervals", runID)
		if err != nil {
			return err
		}
		vs := newInserter(ctx, tx, "metric_values", "run_id", "seq", "metric", "version", "scope_kind", "scope_key",
			"subject_kind", "subject_key", "window_since", "window_until", "value", "num", "den", "n")
		details := newInserter(ctx, tx, "metric_details", "run_id", "value_seq", "name", "value")
		for i := range values {
			v := &values[i]
			vs.add(runID, vseq+i, v.Metric.Name, v.Metric.Version, v.Scope.Kind, v.Scope.Key, v.Subject.Kind,
				v.Subject.Key, vs.time(v.Window.Since), vs.time(v.Window.Until), encodeFloat(v.Value),
				encodeFloat(v.Num), encodeFloat(v.Den), v.N)
			for _, name := range slices.Sorted(maps.Keys(v.Detail)) {
				details.add(runID, vseq+i, name, encodeFloat(v.Detail[name]))
			}
		}
		rs := newInserter(ctx, tx, "rank_intervals", "run_id", "seq", "metric", "version", "scope_kind", "scope_key",
			"subject_kind", "subject_key", "window_since", "window_until", "estimate", "lo", "hi", "rank_lo",
			"rank_hi", "n", "eligible")
		for i := range ranks {
			r := &ranks[i]
			rs.add(runID, rseq+i, r.Metric.Name, r.Metric.Version, r.Scope.Kind, r.Scope.Key, r.Subject.Kind,
				r.Subject.Key, rs.time(r.Window.Since), rs.time(r.Window.Until), encodeFloat(r.Estimate),
				encodeFloat(r.Lo), encodeFloat(r.Hi), r.RankLo, r.RankHi, r.N, encodeBool(r.Eligible))
		}
		return closeAll(vs, details, rs)
	})
	if err != nil {
		return fmt.Errorf("sqlite.PutMetrics: run %d: %w", runID, err)
	}
	return nil
}

// nextSeq returns the position after the last row a run has in a table
// keyed by (run_id, seq), so that a second Put appends.
func nextSeq(ctx context.Context, tx *sql.Tx, table string, runID int64) (int, error) {
	var n int
	// table is a constant of this package, never input.
	err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq) + 1, 0) FROM `+table+` WHERE run_id = ?`, runID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", table, err)
	}
	return n, nil
}

// FinishRun publishes a running run by setting its final status (complete
// or partial), or marks it failed, which readers never see. It records the
// finish time from the wall clock, a timing that no number depends on.
// Then, in the same transaction, it applies retention to the runs of the
// same kind: it keeps this run, the newest 8 published runs, and the newest
// published run of each of the 12 most recent months that have one, by
// Run.Now (the snapshot instant), and deletes the other published runs and
// every failed run. Runs still running are left to their writers.
func (s *Store) FinishRun(ctx context.Context, runID int64, status domain.RunStatus) error {
	switch status {
	case domain.RunStatusComplete, domain.RunStatusPartial, domain.RunStatusFailed:
	default:
		return fmt.Errorf("sqlite.FinishRun: run %d: %q is not a final status", runID, status)
	}
	// Whatever the outcome, this Store stops renewing the run's lease: it is
	// finished, gone, or left to a retry of FinishRun, whose claim renews it.
	defer s.leases.release(runID)
	finished, err := encodeTime(time.Now())
	if err != nil {
		return fmt.Errorf("sqlite.FinishRun: run %d: %w", runID, err)
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := claimRun(ctx, tx, runID); err != nil {
			return err
		}
		var kind string
		err := tx.QueryRowContext(ctx, `UPDATE runs SET status = ?, finished = ? WHERE id = ? RETURNING kind`,
			string(status), finished, runID).Scan(&kind)
		if err != nil {
			return err
		}
		return prune(ctx, tx, kind, runID)
	})
	if err != nil {
		return fmt.Errorf("sqlite.FinishRun: run %d: %w", runID, err)
	}
	return nil
}

// prune deletes the runs of kind that retention does not keep (see
// FinishRun). finished, the run being finished, is always kept, so a run
// is never deleted by its own FinishRun: a backfilled snapshot older than
// the retention horizon stays readable until the next run finishes.
func prune(ctx context.Context, tx *sql.Tx, kind string, finished int64) error {
	type run struct {
		id        int64
		published bool
		now       time.Time
	}
	var runs []run
	err := each(ctx, tx, `
		SELECT id, status, now FROM runs
		WHERE kind = ? AND status != 'running'
		ORDER BY now DESC, id DESC`, []any{kind}, func(rows *sql.Rows) error {
		var r run
		var status string
		var now sql.NullString
		if err := rows.Scan(&r.id, &status, &now); err != nil {
			return err
		}
		var err error
		if r.now, err = decodeTime(now); err != nil {
			return err
		}
		r.published = status != string(domain.RunStatusFailed)
		runs = append(runs, r)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading runs for retention: %w", err)
	}
	keep := map[int64]bool{finished: true}
	published, months, lastMonth := 0, 0, -1
	for _, r := range runs { // newest first; NULL, the zero time, sorts last
		if !r.published {
			continue
		}
		if published < keepLast {
			keep[r.id] = true
		}
		published++
		if month := r.now.Year()*12 + int(r.now.Month()) - 1; month != lastMonth && months < keepMonthly {
			keep[r.id] = true // the newest run of its month
			months++
			lastMonth = month
		}
	}
	for _, r := range runs {
		if keep[r.id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, r.id); err != nil {
			return fmt.Errorf("deleting run %d: %w", r.id, err)
		}
	}
	return nil
}
