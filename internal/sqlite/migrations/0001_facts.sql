-- Migration 1: the facts schema (#8).
--
-- facts.db holds runs: snapshots of the facts one scan learned, published by
-- a status flip (docs/spec/codebase.md#run-model). Each table mirrors one
-- field of the domain types in internal/domain, so that
-- LoadFacts(PutRepoFacts(x)) == x:
--
-- - A list becomes rows numbered by seq, the item's 0-based position, so a
--   load returns the list in the order it was put. A list inside a list
--   item names its parent's position (commit_seq, landing_seq, owner_seq).
-- - A time is TEXT in UTC, RFC 3339 with nine fractional digits
--   (2026-10-07T00:00:00.000000000Z): exact to the nanosecond, sortable as
--   text, and understood by SQLite's date functions. NULL is the zero time.
-- - A bool is INTEGER 0 or 1. A float is REAL, and NULL stands for NaN,
--   which SQLite cannot store. A zero loads as +0: SQLite writes a REAL
--   with no fractional part as an integer, which has no sign, so the store
--   writes -0 as +0.
-- - Child rows reference their run or repository ON DELETE CASCADE, so
--   deleting a run (retention, or a run a crash left running) deletes
--   everything it holds.
--
-- The tables are STRICT, so a value of the wrong type is an error rather
-- than a silent conversion. The runner applies this file in one
-- transaction and then sets PRAGMA user_version.

-- One row per run. status is the publication state: readers select
-- complete and partial runs only. heartbeat is a lease, renewed by every
-- write of the store that began the run: Open deletes a run left running
-- only once its lease has expired, a day after that store last wrote.
CREATE TABLE runs (
	id             INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused: a run id printed once keeps meaning that run
	kind           TEXT    NOT NULL,                  -- scan, sync
	status         TEXT    NOT NULL CHECK (status IN ('running', 'complete', 'partial', 'failed')),
	now            TEXT,                              -- the snapshot instant (--now)
	started        TEXT,
	finished       TEXT,                              -- set by FinishRun
	heartbeat      TEXT    NOT NULL,
	tool_version   TEXT    NOT NULL,
	policy_version INTEGER NOT NULL,
	policy_sha256  TEXT    NOT NULL,
	policy_source  BLOB                               -- the bytes policy_sha256 hashes; NULL when empty
) STRICT;

-- Run.Options: the effective options, never secrets.
CREATE TABLE run_options (
	run_id INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	name   TEXT    NOT NULL,
	value  TEXT    NOT NULL,
	PRIMARY KEY (run_id, name)
) STRICT, WITHOUT ROWID;

-- FactSet.Groups, and each group's Repos (repository keys).
CREATE TABLE run_groups (
	run_id INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	seq    INTEGER NOT NULL,
	name   TEXT    NOT NULL,
	path   TEXT    NOT NULL,
	PRIMARY KEY (run_id, seq),
	UNIQUE (run_id, name)
) STRICT, WITHOUT ROWID;

CREATE TABLE run_group_repos (
	run_id    INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	group_seq INTEGER NOT NULL,
	seq       INTEGER NOT NULL,
	repo_key  TEXT    NOT NULL,
	PRIMARY KEY (run_id, group_seq, seq)
) STRICT, WITHOUT ROWID;

-- FactSet.Warnings: run-level warnings.
CREATE TABLE run_warnings (
	run_id  INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	code    TEXT    NOT NULL,
	message TEXT    NOT NULL,
	repo    TEXT    NOT NULL, -- Warning.Repo, as put
	PRIMARY KEY (run_id, seq)
) STRICT, WITHOUT ROWID;

-- One row per RepoFacts: the repository (Repo), its status, its scalar
-- facts and its ignore-revs record. Every table below hangs off it.
CREATE TABLE run_repos (
	id                 INTEGER PRIMARY KEY,
	run_id             INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	repo_key           TEXT    NOT NULL,
	name               TEXT    NOT NULL,
	group_name         TEXT    NOT NULL,
	path               TEXT    NOT NULL,
	bare               INTEGER NOT NULL,
	remote             TEXT    NOT NULL,
	branch             TEXT    NOT NULL,
	head               TEXT    NOT NULL,
	shallow            INTEGER NOT NULL,
	partial            INTEGER NOT NULL,
	status             TEXT    NOT NULL,
	error              TEXT    NOT NULL,
	first_commit       TEXT,
	history_since      TEXT,
	history_until      TEXT,
	line_facts         INTEGER NOT NULL,
	ignore_revs_source TEXT    NOT NULL,
	ignore_revs_sha256 TEXT    NOT NULL,
	UNIQUE (run_id, repo_key)
) STRICT;

CREATE TABLE repo_warnings (
	repo_id INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	code    TEXT    NOT NULL,
	message TEXT    NOT NULL,
	repo    TEXT    NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.Paths: the classifier's verdict on each path.
CREATE TABLE path_classes (
	repo_id INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	path    TEXT    NOT NULL,
	role    TEXT    NOT NULL,
	skip    TEXT    NOT NULL,
	size    INTEGER NOT NULL,
	at_head INTEGER NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.Commits, with each commit's Parents and CoAuthors (trailer
-- order, which credit reads).
CREATE TABLE commits (
	repo_id      INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq          INTEGER NOT NULL,
	sha          TEXT    NOT NULL,
	author_name  TEXT    NOT NULL,
	author_email TEXT    NOT NULL,
	author_time  TEXT,
	commit_time  TEXT,
	subject      TEXT    NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE commit_parents (
	repo_id    INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	commit_seq INTEGER NOT NULL,
	seq        INTEGER NOT NULL,
	sha        TEXT    NOT NULL,
	PRIMARY KEY (repo_id, commit_seq, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE commit_coauthors (
	repo_id    INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	commit_seq INTEGER NOT NULL,
	seq        INTEGER NOT NULL,
	name       TEXT    NOT NULL,
	email      TEXT    NOT NULL,
	PRIMARY KEY (repo_id, commit_seq, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.Landings, and the landing map: the commits each landing
-- brought in (Landing.Commits).
CREATE TABLE landings (
	repo_id INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	sha     TEXT    NOT NULL,
	time    TEXT,
	merge   INTEGER NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE landing_commits (
	repo_id     INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	landing_seq INTEGER NOT NULL,
	seq         INTEGER NOT NULL,
	sha         TEXT    NOT NULL,
	PRIMARY KEY (repo_id, landing_seq, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.Changes: what each commit did to each path.
CREATE TABLE commit_files (
	repo_id          INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq              INTEGER NOT NULL,
	commit_sha       TEXT    NOT NULL,
	path             TEXT    NOT NULL,
	old_path         TEXT    NOT NULL,
	added            INTEGER NOT NULL,
	removed          INTEGER NOT NULL,
	binary           INTEGER NOT NULL,
	added_nonblank   INTEGER NOT NULL,
	removed_nonblank INTEGER NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.Reverts.
CREATE TABLE reverts (
	repo_id  INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq      INTEGER NOT NULL,
	sha      TEXT    NOT NULL,
	reverted TEXT    NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.Owners: non-blank lines blame attributes to one commit in one
-- path at HEAD, with the commit's co-authors, so blamed lines are credited
-- like commits (ADR 0007).
CREATE TABLE line_owners (
	repo_id      INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq          INTEGER NOT NULL,
	path         TEXT    NOT NULL,
	commit_sha   TEXT    NOT NULL,
	author_name  TEXT    NOT NULL,
	author_email TEXT    NOT NULL,
	author_time  TEXT,
	lines        INTEGER NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE line_owner_coauthors (
	repo_id   INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	owner_seq INTEGER NOT NULL,
	seq       INTEGER NOT NULL,
	name      TEXT    NOT NULL,
	email     TEXT    NOT NULL,
	PRIMARY KEY (repo_id, owner_seq, seq)
) STRICT, WITHOUT ROWID;

-- IgnoreRevs.Revs: the commits durable@1 and simplifying@1 exclude.
CREATE TABLE ignore_revs (
	repo_id INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	sha     TEXT    NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

-- RepoFacts.BlameErrors.
CREATE TABLE blame_errors (
	repo_id INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	path    TEXT    NOT NULL,
	message TEXT    NOT NULL,
	PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

-- The metric values computed for a run, keyed by metric@version, with each
-- value's Detail; and the rank intervals of ranked metrics.
CREATE TABLE metric_values (
	run_id       INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	seq          INTEGER NOT NULL,
	metric       TEXT    NOT NULL,
	version      INTEGER NOT NULL,
	scope_kind   TEXT    NOT NULL,
	scope_key    TEXT    NOT NULL,
	subject_kind TEXT    NOT NULL,
	subject_key  TEXT    NOT NULL,
	window_since TEXT,
	window_until TEXT,
	value        REAL,
	num          REAL,
	den          REAL,
	n            INTEGER NOT NULL,
	PRIMARY KEY (run_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE metric_details (
	run_id    INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	value_seq INTEGER NOT NULL,
	name      TEXT    NOT NULL,
	value     REAL,
	PRIMARY KEY (run_id, value_seq, name)
) STRICT, WITHOUT ROWID;

CREATE TABLE rank_intervals (
	run_id       INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
	seq          INTEGER NOT NULL,
	metric       TEXT    NOT NULL,
	version      INTEGER NOT NULL,
	scope_kind   TEXT    NOT NULL,
	scope_key    TEXT    NOT NULL,
	subject_kind TEXT    NOT NULL,
	subject_key  TEXT    NOT NULL,
	window_since TEXT,
	window_until TEXT,
	estimate     REAL,
	lo           REAL,
	hi           REAL,
	rank_lo      INTEGER NOT NULL,
	rank_hi      INTEGER NOT NULL,
	n            INTEGER NOT NULL,
	eligible     INTEGER NOT NULL,
	PRIMARY KEY (run_id, seq)
) STRICT, WITHOUT ROWID;
