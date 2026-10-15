-- Migration 2: the forge replica (#14).
--
-- GitHub is the system of record for every row here (ADR 0004); these tables
-- are a read-only copy that sync upserts by GitHub node id. They are not per
-- run: they outlive runs, and each repository has a watermark that says how
-- far its copy reaches (docs/spec/codebase.md#run-model). A pull request is
-- replaced as a whole with its labels, reviews and commits, so a removed
-- label or a force-pushed commit does not linger, and only by a copy updated
-- at the same time or later, so an overlapping sync cannot undo a newer one.
-- What a run read is kept per run in forge_run_watermarks, which dates the
-- run's forge numbers.
--
-- Times are encoded as in migration 1: TEXT in UTC, RFC 3339 with nine
-- fractional digits, which sorts like the time. NULL is the zero time: not
-- closed, not merged, never synced.

CREATE TABLE forge_repos (
    id             TEXT PRIMARY KEY,  -- node id, stable across renames
    key            TEXT NOT NULL,     -- the HTTPS clone URL: its mirror's Repo.Key
    owner          TEXT NOT NULL,
    name           TEXT NOT NULL,
    default_branch TEXT NOT NULL,     -- '' for an empty repository
    archived       INTEGER NOT NULL,
    fork           INTEGER NOT NULL
) STRICT;

CREATE INDEX forge_repos_key ON forge_repos (key);

CREATE TABLE forge_watermarks (
    repo    TEXT PRIMARY KEY REFERENCES forge_repos (id),
    updated TEXT,  -- every pull request updated before this is in the replica; never decreases
    synced  TEXT   -- the start of the last sync that brought the repository up to date
) STRICT;

CREATE TABLE forge_pull_requests (
    id                 TEXT PRIMARY KEY,  -- node id
    repo               TEXT NOT NULL REFERENCES forge_repos (id),
    number             INTEGER NOT NULL,
    title              TEXT NOT NULL,
    author             TEXT NOT NULL,     -- login; '' for a deleted account
    author_bot         INTEGER NOT NULL,
    state              TEXT NOT NULL,     -- open, closed, merged
    draft              INTEGER NOT NULL,
    base_branch        TEXT NOT NULL,
    created            TEXT,
    updated            TEXT,              -- what the watermark follows, and what keeps a replacement monotonic
    closed             TEXT,
    merged             TEXT,
    merge_sha          TEXT NOT NULL,     -- '' unless merged
    merge_author_email TEXT NOT NULL,
    merge_author_login TEXT NOT NULL
) STRICT;

CREATE INDEX forge_pull_requests_repo ON forge_pull_requests (repo, number);
CREATE INDEX forge_pull_requests_updated ON forge_pull_requests (updated);

CREATE TABLE forge_pull_labels (
    pull TEXT NOT NULL REFERENCES forge_pull_requests (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    PRIMARY KEY (pull, name)
) STRICT, WITHOUT ROWID;

CREATE TABLE forge_reviews (
    id          TEXT PRIMARY KEY,  -- node id
    pull        TEXT NOT NULL REFERENCES forge_pull_requests (id) ON DELETE CASCADE,
    author      TEXT NOT NULL,
    author_bot  INTEGER NOT NULL,
    state       TEXT NOT NULL,     -- approved, changes_requested, commented, dismissed
    submitted   TEXT,
    body_length INTEGER NOT NULL,  -- characters of the review's own text, trimmed; the text is not kept
    comments    INTEGER NOT NULL   -- inline comments in the review
) STRICT;

CREATE INDEX forge_reviews_pull ON forge_reviews (pull);

CREATE TABLE forge_pull_commits (
    pull         TEXT NOT NULL REFERENCES forge_pull_requests (id) ON DELETE CASCADE,
    position     INTEGER NOT NULL,  -- the forge's order, oldest first
    sha          TEXT NOT NULL,
    author_email TEXT NOT NULL,     -- as GitHub read it, lowercased; no mailmap applied
    author_login TEXT NOT NULL,     -- the account GitHub resolved that email to; '' when none
    PRIMARY KEY (pull, position)
) STRICT, WITHOUT ROWID;

CREATE INDEX forge_pull_commits_sha ON forge_pull_commits (sha);

-- The watermarks each run read (ForgeStore.PutRunWatermarks), which date its
-- forge numbers: a stored run shows "as of" them after later syncs moved the
-- replica on. A run keeps these times, not a copy of the pull requests. They
-- go with the run, which migration 1's runs table holds.
CREATE TABLE forge_run_watermarks (
    run_id  INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    repo    TEXT NOT NULL,  -- ForgeRepo.ID
    updated TEXT,
    synced  TEXT,
    PRIMARY KEY (run_id, repo)
) STRICT, WITHOUT ROWID;
