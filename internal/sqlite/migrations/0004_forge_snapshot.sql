-- Migration 4: a run keeps the GitHub facts it read (#38, ADR 0011).
--
-- Migrations 2 and 3 keep, per run, only when each of its forge
-- repositories was synced (forge_run_watermarks) and which of its
-- repositories each one is (forge_run_repos). The pull requests stayed in
-- the replica, which a sync replaces as a whole, so a number computed when
-- a stored run is read, such as who_knows@1's recent reviewers for any
-- path, could only come from today's replica: another snapshot than the
-- run's git facts and watermarks. So a scan now copies its own part of the
-- replica (FactSet.Forge: the forge repositories domain.ForgeJoin joins to
-- its repositories, and their pull requests updated in the history window)
-- into these tables, and its readers read it back instead of the replica.
-- What a run read is immutable, like its git facts (Helland, "Immutability
-- Changes Everything", CIDR 2015): a dimension snapshot per run, as
-- Beauchemin's "Functional Data Engineering" (2018) keeps one per partition.
--
-- The tables mirror the replica's of migration 2, as migration 1 mirrors the
-- domain types: a list is rows numbered by seq, its 0-based position, so a
-- load returns FactSet.Forge exactly as it was put, and a list inside a pull
-- request names its parent's position (pull_seq). Times are encoded as in
-- migration 1. Every table references runs (ON DELETE CASCADE), as
-- migration 1's lists reference their repository rather than their parent
-- row, so the rows of a list can be inserted in batches in any order, and a
-- run's deletion, by retention or as a crash's leftover, finds them by the
-- primary key's prefix.
--
-- A run stored before this migration kept no pull requests: its pages date
-- the GitHub numbers it stored by its watermarks, and who-knows, which
-- computes reviewers from the pull requests, says nothing about GitHub for
-- it rather than compute over today's replica (app.load).

-- FactSet.Forge.Repos of a scan run: its forge repositories as the replica
-- listed them when the run read it.
CREATE TABLE forge_snapshot_repos (
    run_id         INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq            INTEGER NOT NULL,
    id             TEXT    NOT NULL,  -- ForgeRepo.ID
    key            TEXT    NOT NULL,  -- the HTTPS clone URL, which domain.ForgeJoin matches origins against
    owner          TEXT    NOT NULL,
    name           TEXT    NOT NULL,
    default_branch TEXT    NOT NULL,
    archived       INTEGER NOT NULL,
    fork           INTEGER NOT NULL,
    PRIMARY KEY (run_id, seq)
) STRICT, WITHOUT ROWID;

-- FactSet.Forge.PullRequests: every pull request of those repositories the
-- run read, whole. repo is one of the run's forge_snapshot_repos, which
-- PutRunForge checks.
CREATE TABLE forge_snapshot_pulls (
    run_id             INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq                INTEGER NOT NULL,
    id                 TEXT    NOT NULL,  -- node id
    repo               TEXT    NOT NULL,  -- ForgeRepo.ID
    number             INTEGER NOT NULL,
    title              TEXT    NOT NULL,  -- local only; never sent to an AI
    author             TEXT    NOT NULL,  -- login; '' for a deleted account
    author_bot         INTEGER NOT NULL,
    state              TEXT    NOT NULL,
    draft              INTEGER NOT NULL,
    base_branch        TEXT    NOT NULL,
    created            TEXT,
    updated            TEXT,
    closed             TEXT,
    merged             TEXT,
    merge_sha          TEXT    NOT NULL,  -- '' unless merged
    merge_author_email TEXT    NOT NULL,
    merge_author_login TEXT    NOT NULL,
    PRIMARY KEY (run_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE forge_snapshot_labels (
    run_id   INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    pull_seq INTEGER NOT NULL,
    seq      INTEGER NOT NULL,
    name     TEXT    NOT NULL,
    PRIMARY KEY (run_id, pull_seq, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE forge_snapshot_reviews (
    run_id      INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    pull_seq    INTEGER NOT NULL,
    seq         INTEGER NOT NULL,  -- PullRequest.Reviews order: by submission, then id
    id          TEXT    NOT NULL,  -- node id
    author      TEXT    NOT NULL,
    author_bot  INTEGER NOT NULL,
    state       TEXT    NOT NULL,
    submitted   TEXT,
    body_length INTEGER NOT NULL,  -- characters of the review's own text, trimmed; the text is not kept
    comments    INTEGER NOT NULL,
    PRIMARY KEY (run_id, pull_seq, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE forge_snapshot_commits (
    run_id       INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    pull_seq     INTEGER NOT NULL,
    seq          INTEGER NOT NULL,  -- the forge's order, oldest first
    sha          TEXT    NOT NULL,
    author_email TEXT    NOT NULL,  -- as GitHub read it, lowercased; no mailmap applied
    author_login TEXT    NOT NULL,
    PRIMARY KEY (run_id, pull_seq, seq)
) STRICT, WITHOUT ROWID;
