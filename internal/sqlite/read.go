package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The read side. Each method reads in one read transaction on a query_only
// connection, so it sees one snapshot of the file even while another
// process writes a run or prunes old ones, and it selects published runs
// only: a run that is unknown, running or failed is domain.ErrNotFound.
// Lists come back in the order they were put, and an empty list or map
// comes back nil (docs/spec/codebase.md#determinism).

// published is the condition that makes a run visible to readers.
const published = `status IN ('complete', 'partial')`

// Run returns a complete or partial run by id, as BeginRun recorded it,
// with its final status and finish time, or domain.ErrNotFound.
func (s *Store) Run(ctx context.Context, id int64) (domain.Run, error) {
	var r domain.Run
	err := s.read(ctx, func(tx *sql.Tx) error {
		var err error
		r, err = readRun(ctx, tx, `WHERE id = ? AND `+published, id)
		return err
	})
	if err != nil {
		return domain.Run{}, fmt.Errorf("sqlite.Run: run %d: %w", id, err)
	}
	return r, nil
}

// LatestRun returns the newest complete or partial run of the kind, the
// one begun last (run ids only grow), or domain.ErrNotFound. Begin order,
// not Run.Now: the read commands' default is the run the user made last, a
// run made with an earlier --now included, until the next run of the kind.
// Ordered by Now, one --now mistyped into the future would stay the default
// for good, since retention, which orders by Now, never deletes the newest.
func (s *Store) LatestRun(ctx context.Context, kind domain.RunKind) (domain.Run, error) {
	var r domain.Run
	err := s.read(ctx, func(tx *sql.Tx) error {
		var err error
		r, err = readRun(ctx, tx, `WHERE kind = ? AND `+published+` ORDER BY id DESC LIMIT 1`, string(kind))
		return err
	})
	if err != nil {
		return domain.Run{}, fmt.Errorf("sqlite.LatestRun: %s: %w", kind, err)
	}
	return r, nil
}

// Runs returns the complete and partial runs of the kind, newest first (by
// id, as LatestRun orders them), each with its groups and repositories as
// LoadFacts reads them and none of their other facts, in one read
// transaction: a few rows per run (MC-17).
func (s *Store) Runs(ctx context.Context, kind domain.RunKind) ([]domain.RunScopes, error) {
	var out []domain.RunScopes
	err := s.read(ctx, func(tx *sql.Tx) error {
		var ids []int64
		err := each(ctx, tx, `SELECT id FROM runs WHERE kind = ? AND `+published+` ORDER BY id DESC`, []any{string(kind)},
			func(rows *sql.Rows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
				return nil
			})
		if err != nil {
			return fmt.Errorf("reading runs: %w", err)
		}
		for _, id := range ids {
			r, err := readRun(ctx, tx, `WHERE id = ?`, id)
			if err != nil {
				return err
			}
			l := &loader{ctx: ctx, tx: tx, runID: id, set: &domain.FactSet{}}
			for _, step := range []func() error{l.groups, l.groupRepos, l.repoRows} {
				if err := step(); err != nil {
					return fmt.Errorf("run %d: %w", id, err)
				}
			}
			rs := domain.RunScopes{Run: r, Groups: l.set.Groups}
			for _, f := range l.set.Repos {
				rs.Repos = append(rs.Repos, f.Repo)
			}
			out = append(out, rs)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite.Runs: %s: %w", kind, err)
	}
	return out, nil
}

// readRun reads the one run that the clause selects, with its options, or
// returns domain.ErrNotFound.
func readRun(ctx context.Context, tx *sql.Tx, clause string, args ...any) (domain.Run, error) {
	var r domain.Run
	var now, started, finished sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT id, kind, status, now, started, finished, tool_version, policy_version, policy_sha256, policy_source
		FROM runs `+clause, args...).Scan(&r.ID, &r.Kind, &r.Status, &now, &started, &finished, &r.ToolVersion,
		&r.Policy.Version, &r.Policy.SHA256, &r.PolicySource)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Run{}, domain.ErrNotFound
	case err != nil:
		return domain.Run{}, fmt.Errorf("reading runs: %w", err)
	}
	if r.Now, err = decodeTime(now); err != nil {
		return domain.Run{}, err
	}
	if r.Started, err = decodeTime(started); err != nil {
		return domain.Run{}, err
	}
	if r.Finished, err = decodeTime(finished); err != nil {
		return domain.Run{}, err
	}
	err = each(ctx, tx, `SELECT name, value FROM run_options WHERE run_id = ? ORDER BY name`, []any{r.ID},
		func(rows *sql.Rows) error {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				return err
			}
			if r.Options == nil {
				r.Options = make(map[string]string)
			}
			r.Options[name] = value
			return nil
		})
	if err != nil {
		return domain.Run{}, fmt.Errorf("reading run_options: %w", err)
	}
	return r, nil
}

// LoadFacts returns a complete or partial run's facts exactly as they were
// put, with the repositories sorted by key, or domain.ErrNotFound. Its Now
// and Policy are the run's. Forge is nil: the forge replica is read through
// domain.ForgeStore.
func (s *Store) LoadFacts(ctx context.Context, runID int64) (*domain.FactSet, error) {
	var set *domain.FactSet
	err := s.read(ctx, func(tx *sql.Tx) error {
		r, err := readRun(ctx, tx, `WHERE id = ? AND `+published, runID)
		if err != nil {
			return err
		}
		l := &loader{ctx: ctx, tx: tx, runID: runID, set: &domain.FactSet{Now: r.Now, Policy: r.Policy}}
		if err := l.load(); err != nil {
			return err
		}
		set = l.set
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite.LoadFacts: run %d: %w", runID, err)
	}
	return set, nil
}

// loader reads one run's facts table by table: one query per table for the
// whole run, in the primary key's order, so no query sorts.
type loader struct {
	ctx   context.Context
	tx    *sql.Tx
	runID int64
	set   *domain.FactSet
	repos map[int64]*domain.RepoFacts // run_repos.id to its entry in set.Repos
}

// ofRun selects the rows of a repository table that belong to the run.
const ofRun = `repo_id IN (SELECT id FROM run_repos WHERE run_id = ?)`

func (l *loader) load() error {
	for _, step := range []func() error{
		l.groups, l.groupRepos, l.runWarnings, l.repoRows, l.repoWarnings, l.paths, l.commits, l.parents,
		l.commitCoAuthors, l.landings, l.landingCommits, l.changes, l.reverts, l.owners, l.ownerCoAuthors,
		l.ignoreRevs, l.blameErrors, l.aliases,
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// each runs a query of table with the run's id as its argument and calls fn
// for every row.
func (l *loader) each(table, query string, fn func(*sql.Rows) error) error {
	if err := each(l.ctx, l.tx, query, []any{l.runID}, fn); err != nil {
		return fmt.Errorf("reading %s: %w", table, err)
	}
	return nil
}

// repo returns the facts that a repository table's row belongs to.
func (l *loader) repo(id int64) (*domain.RepoFacts, error) {
	f, ok := l.repos[id]
	if !ok {
		return nil, fmt.Errorf("row of unknown repository %d", id)
	}
	return f, nil
}

func (l *loader) groups() error {
	return l.each("run_groups", `SELECT seq, name, path FROM run_groups WHERE run_id = ? ORDER BY seq`,
		func(rows *sql.Rows) error {
			var seq int64
			var g domain.Group
			if err := rows.Scan(&seq, &g.Name, &g.Path); err != nil {
				return err
			}
			if err := position(seq, len(l.set.Groups)); err != nil {
				return err
			}
			l.set.Groups = append(l.set.Groups, g)
			return nil
		})
}

func (l *loader) groupRepos() error {
	return l.each("run_group_repos",
		`SELECT group_seq, seq, repo_key FROM run_group_repos WHERE run_id = ? ORDER BY group_seq, seq`,
		func(rows *sql.Rows) error {
			var group, seq int64
			var key string
			if err := rows.Scan(&group, &seq, &key); err != nil {
				return err
			}
			g, err := item(l.set.Groups, group)
			if err != nil {
				return err
			}
			if err := position(seq, len(g.Repos)); err != nil {
				return err
			}
			g.Repos = append(g.Repos, key)
			return nil
		})
}

func (l *loader) runWarnings() error {
	return l.each("run_warnings", `SELECT seq, code, message, repo FROM run_warnings WHERE run_id = ? ORDER BY seq`,
		func(rows *sql.Rows) error {
			var seq int64
			var w domain.Warning
			if err := rows.Scan(&seq, &w.Code, &w.Message, &w.Repo); err != nil {
				return err
			}
			if err := position(seq, len(l.set.Warnings)); err != nil {
				return err
			}
			l.set.Warnings = append(l.set.Warnings, w)
			return nil
		})
}

func (l *loader) repoRows() error {
	var ids []int64
	err := l.each("run_repos", `
		SELECT id, repo_key, name, group_name, path, bare, remote, branch, head, shallow, partial, status, error,
			first_commit, history_since, history_until, line_facts, ignore_revs_source, ignore_revs_sha256
		FROM run_repos WHERE run_id = ? ORDER BY repo_key`,
		func(rows *sql.Rows) error {
			var id int64
			var f domain.RepoFacts
			var first, since, until sql.NullString
			r := &f.Repo
			if err := rows.Scan(&id, &r.Key, &r.Name, &r.Group, &r.Path, &r.Bare, &r.Remote, &r.Branch, &r.Head,
				&r.Shallow, &r.Partial, &f.Status, &f.Error, &first, &since, &until, &f.LineFacts,
				&f.IgnoreRevs.Source, &f.IgnoreRevs.SHA256); err != nil {
				return err
			}
			var err error
			if f.FirstCommit, err = decodeTime(first); err != nil {
				return err
			}
			if f.History.Since, err = decodeTime(since); err != nil {
				return err
			}
			if f.History.Until, err = decodeTime(until); err != nil {
				return err
			}
			ids = append(ids, id)
			l.set.Repos = append(l.set.Repos, f)
			return nil
		})
	if err != nil {
		return err
	}
	// The slice is complete, so pointers into it stay valid.
	l.repos = make(map[int64]*domain.RepoFacts, len(ids))
	for i, id := range ids {
		l.repos[id] = &l.set.Repos[i]
	}
	return nil
}

func (l *loader) repoWarnings() error {
	return l.each("repo_warnings",
		`SELECT repo_id, seq, code, message, repo FROM repo_warnings WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var w domain.Warning
			if err := rows.Scan(&repo, &seq, &w.Code, &w.Message, &w.Repo); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Warnings)); err != nil {
				return err
			}
			f.Warnings = append(f.Warnings, w)
			return nil
		})
}

func (l *loader) paths() error {
	return l.each("path_classes",
		`SELECT repo_id, seq, path, role, skip, size, at_head FROM path_classes WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var p domain.PathClass
			if err := rows.Scan(&repo, &seq, &p.Path, &p.Role, &p.Skip, &p.Size, &p.AtHead); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Paths)); err != nil {
				return err
			}
			f.Paths = append(f.Paths, p)
			return nil
		})
}

func (l *loader) commits() error {
	return l.each("commits", `
		SELECT repo_id, seq, sha, author_name, author_email, author_time, commit_time, subject, patch_id
		FROM commits WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var c domain.Commit
			var authored, committed sql.NullString
			if err := rows.Scan(&repo, &seq, &c.SHA, &c.Author.Name, &c.Author.Email, &authored, &committed,
				&c.Subject, &c.PatchID); err != nil {
				return err
			}
			var err error
			if c.AuthorTime, err = decodeTime(authored); err != nil {
				return err
			}
			if c.CommitTime, err = decodeTime(committed); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Commits)); err != nil {
				return err
			}
			f.Commits = append(f.Commits, c)
			return nil
		})
}

func (l *loader) parents() error {
	return l.each("commit_parents",
		`SELECT repo_id, commit_seq, seq, sha FROM commit_parents WHERE `+ofRun+` ORDER BY repo_id, commit_seq, seq`,
		func(rows *sql.Rows) error {
			var repo, commit, seq int64
			var sha string
			if err := rows.Scan(&repo, &commit, &seq, &sha); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			c, err := item(f.Commits, commit)
			if err != nil {
				return err
			}
			if err := position(seq, len(c.Parents)); err != nil {
				return err
			}
			c.Parents = append(c.Parents, sha)
			return nil
		})
}

func (l *loader) commitCoAuthors() error {
	return l.each("commit_coauthors", `
		SELECT repo_id, commit_seq, seq, name, email
		FROM commit_coauthors WHERE `+ofRun+` ORDER BY repo_id, commit_seq, seq`,
		func(rows *sql.Rows) error {
			var repo, commit, seq int64
			var p domain.Person
			if err := rows.Scan(&repo, &commit, &seq, &p.Name, &p.Email); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			c, err := item(f.Commits, commit)
			if err != nil {
				return err
			}
			if err := position(seq, len(c.CoAuthors)); err != nil {
				return err
			}
			c.CoAuthors = append(c.CoAuthors, p)
			return nil
		})
}

func (l *loader) landings() error {
	return l.each("landings",
		`SELECT repo_id, seq, sha, time, merge FROM landings WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var ld domain.Landing
			var at sql.NullString
			if err := rows.Scan(&repo, &seq, &ld.SHA, &at, &ld.Merge); err != nil {
				return err
			}
			var err error
			if ld.Time, err = decodeTime(at); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Landings)); err != nil {
				return err
			}
			f.Landings = append(f.Landings, ld)
			return nil
		})
}

func (l *loader) landingCommits() error {
	return l.each("landing_commits", `
		SELECT repo_id, landing_seq, seq, sha
		FROM landing_commits WHERE `+ofRun+` ORDER BY repo_id, landing_seq, seq`,
		func(rows *sql.Rows) error {
			var repo, landing, seq int64
			var sha string
			if err := rows.Scan(&repo, &landing, &seq, &sha); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			ld, err := item(f.Landings, landing)
			if err != nil {
				return err
			}
			if err := position(seq, len(ld.Commits)); err != nil {
				return err
			}
			ld.Commits = append(ld.Commits, sha)
			return nil
		})
}

func (l *loader) changes() error {
	return l.each("commit_files", `
		SELECT repo_id, seq, commit_sha, path, old_path, added, removed, binary, added_nonblank, removed_nonblank
		FROM commit_files WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq, added, removed, addedNB, removedNB int64
			var c domain.FileChange
			if err := rows.Scan(&repo, &seq, &c.Commit, &c.Path, &c.OldPath, &added, &removed, &c.Binary,
				&addedNB, &removedNB); err != nil {
				return err
			}
			c.Added, c.Removed, c.AddedNonBlank, c.RemovedNonBlank = int(added), int(removed), int(addedNB), int(removedNB)
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Changes)); err != nil {
				return err
			}
			f.Changes = append(f.Changes, c)
			return nil
		})
}

func (l *loader) reverts() error {
	return l.each("reverts", `SELECT repo_id, seq, sha, reverted FROM reverts WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var r domain.Revert
			if err := rows.Scan(&repo, &seq, &r.SHA, &r.Reverted); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Reverts)); err != nil {
				return err
			}
			f.Reverts = append(f.Reverts, r)
			return nil
		})
}

func (l *loader) owners() error {
	return l.each("line_owners", `
		SELECT repo_id, seq, path, commit_sha, orig_path, author_name, author_email, author_time, lines
		FROM line_owners WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq, lines int64
			var o domain.LineOwner
			var authored sql.NullString
			if err := rows.Scan(&repo, &seq, &o.Path, &o.Commit, &o.OrigPath, &o.Author.Name, &o.Author.Email,
				&authored, &lines); err != nil {
				return err
			}
			o.Lines = int(lines)
			var err error
			if o.AuthorTime, err = decodeTime(authored); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Owners)); err != nil {
				return err
			}
			f.Owners = append(f.Owners, o)
			return nil
		})
}

func (l *loader) ownerCoAuthors() error {
	return l.each("line_owner_coauthors", `
		SELECT repo_id, owner_seq, seq, name, email
		FROM line_owner_coauthors WHERE `+ofRun+` ORDER BY repo_id, owner_seq, seq`,
		func(rows *sql.Rows) error {
			var repo, owner, seq int64
			var p domain.Person
			if err := rows.Scan(&repo, &owner, &seq, &p.Name, &p.Email); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			o, err := item(f.Owners, owner)
			if err != nil {
				return err
			}
			if err := position(seq, len(o.CoAuthors)); err != nil {
				return err
			}
			o.CoAuthors = append(o.CoAuthors, p)
			return nil
		})
}

func (l *loader) ignoreRevs() error {
	return l.each("ignore_revs", `SELECT repo_id, seq, sha FROM ignore_revs WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var sha string
			if err := rows.Scan(&repo, &seq, &sha); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.IgnoreRevs.Revs)); err != nil {
				return err
			}
			f.IgnoreRevs.Revs = append(f.IgnoreRevs.Revs, sha)
			return nil
		})
}

func (l *loader) blameErrors() error {
	return l.each("blame_errors",
		`SELECT repo_id, seq, path, message FROM blame_errors WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var e domain.FileError
			if err := rows.Scan(&repo, &seq, &e.Path, &e.Message); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.BlameErrors)); err != nil {
				return err
			}
			f.BlameErrors = append(f.BlameErrors, e)
			return nil
		})
}

// aliases reads the emails git's mailmap rewrote (#53, repo_aliases).
func (l *loader) aliases() error {
	return l.each("repo_aliases",
		`SELECT repo_id, seq, email, name, canonical FROM repo_aliases WHERE `+ofRun+` ORDER BY repo_id, seq`,
		func(rows *sql.Rows) error {
			var repo, seq int64
			var a domain.Alias
			if err := rows.Scan(&repo, &seq, &a.Email, &a.Name, &a.Canonical); err != nil {
				return err
			}
			f, err := l.repo(repo)
			if err != nil {
				return err
			}
			if err := position(seq, len(f.Aliases)); err != nil {
				return err
			}
			f.Aliases = append(f.Aliases, a)
			return nil
		})
}

// LoadMetrics returns a complete or partial run's metric values and rank
// intervals in the order they were put, or domain.ErrNotFound.
func (s *Store) LoadMetrics(ctx context.Context, runID int64) ([]domain.MetricValue, []domain.RankInterval, error) {
	var values []domain.MetricValue
	var ranks []domain.RankInterval
	err := s.read(ctx, func(tx *sql.Tx) error {
		var visible int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id = ? AND `+published, runID).Scan(&visible)
		switch {
		case err != nil:
			return fmt.Errorf("reading runs: %w", err)
		case visible == 0:
			return domain.ErrNotFound
		}
		if values, err = loadValues(ctx, tx, runID); err != nil {
			return err
		}
		ranks, err = loadRanks(ctx, tx, runID)
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite.LoadMetrics: run %d: %w", runID, err)
	}
	return values, ranks, nil
}

func loadValues(ctx context.Context, tx *sql.Tx, runID int64) ([]domain.MetricValue, error) {
	var values []domain.MetricValue
	err := each(ctx, tx, `
		SELECT seq, metric, version, scope_kind, scope_key, subject_kind, subject_key, window_since, window_until,
			value, num, den, n
		FROM metric_values WHERE run_id = ? ORDER BY seq`, []any{runID},
		func(rows *sql.Rows) error {
			var seq, version, n int64
			var v domain.MetricValue
			var since, until sql.NullString
			var value, num, den sql.NullFloat64
			if err := rows.Scan(&seq, &v.Metric.Name, &version, &v.Scope.Kind, &v.Scope.Key, &v.Subject.Kind,
				&v.Subject.Key, &since, &until, &value, &num, &den, &n); err != nil {
				return err
			}
			v.Metric.Version, v.N = int(version), int(n)
			v.Value, v.Num, v.Den = decodeFloat(value), decodeFloat(num), decodeFloat(den)
			var err error
			if v.Window.Since, err = decodeTime(since); err != nil {
				return err
			}
			if v.Window.Until, err = decodeTime(until); err != nil {
				return err
			}
			if err := position(seq, len(values)); err != nil {
				return err
			}
			values = append(values, v)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading metric_values: %w", err)
	}
	err = each(ctx, tx, `SELECT value_seq, name, value FROM metric_details WHERE run_id = ? ORDER BY value_seq, name`,
		[]any{runID}, func(rows *sql.Rows) error {
			var seq int64
			var name string
			var value sql.NullFloat64
			if err := rows.Scan(&seq, &name, &value); err != nil {
				return err
			}
			v, err := item(values, seq)
			if err != nil {
				return err
			}
			if v.Detail == nil {
				v.Detail = make(map[string]float64)
			}
			v.Detail[name] = decodeFloat(value)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading metric_details: %w", err)
	}
	return values, nil
}

func loadRanks(ctx context.Context, tx *sql.Tx, runID int64) ([]domain.RankInterval, error) {
	var ranks []domain.RankInterval
	err := each(ctx, tx, `
		SELECT seq, metric, version, scope_kind, scope_key, subject_kind, subject_key, window_since, window_until,
			estimate, lo, hi, rank_lo, rank_hi, n, eligible
		FROM rank_intervals WHERE run_id = ? ORDER BY seq`, []any{runID},
		func(rows *sql.Rows) error {
			var seq, version, rankLo, rankHi, n int64
			var r domain.RankInterval
			var since, until sql.NullString
			var estimate, lo, hi sql.NullFloat64
			if err := rows.Scan(&seq, &r.Metric.Name, &version, &r.Scope.Kind, &r.Scope.Key, &r.Subject.Kind,
				&r.Subject.Key, &since, &until, &estimate, &lo, &hi, &rankLo, &rankHi, &n, &r.Eligible); err != nil {
				return err
			}
			r.Metric.Version, r.RankLo, r.RankHi, r.N = int(version), int(rankLo), int(rankHi), int(n)
			r.Estimate, r.Lo, r.Hi = decodeFloat(estimate), decodeFloat(lo), decodeFloat(hi)
			var err error
			if r.Window.Since, err = decodeTime(since); err != nil {
				return err
			}
			if r.Window.Until, err = decodeTime(until); err != nil {
				return err
			}
			if err := position(seq, len(ranks)); err != nil {
				return err
			}
			ranks = append(ranks, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading rank_intervals: %w", err)
	}
	return ranks, nil
}

// each runs query in tx and calls fn for every row, stopping at the first
// error.
func each(ctx context.Context, tx *sql.Tx, query string, args []any, fn func(*sql.Rows) error) (err error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// position checks that a row's seq is the next position of the list it is
// appended to. The store writes positions 0, 1, 2 without gaps, so any
// other value means rows were changed by hand.
func position(seq int64, n int) error {
	if seq != int64(n) {
		return fmt.Errorf("row at position %d follows %d rows: the table was changed outside stillhere", seq, n)
	}
	return nil
}

// item returns the list item a nested row belongs to, by its position.
func item[T any](list []T, seq int64) (*T, error) {
	if seq < 0 || seq >= int64(len(list)) {
		return nil, fmt.Errorf("row of item %d of a list of %d: the table was changed outside stillhere", seq, len(list))
	}
	return &list[seq], nil
}
