-- Migration 2: the forge replica (#14).
--
-- GitHub is the system of record for every row here (ADR 0004); these tables
-- are a read-only copy that sync upserts by GitHub node id. They are not per
-- run: they outlive runs, and each repository has a watermark that says how
-- far its copy reaches (docs/spec/codebase.md#run-model). A pull request is
-- replaced as a whole with its labels, reviews and commits, so a removed
-- label or a force-pushed commit does not linger.
--
-- Times are Unix seconds, UTC, GitHub's precision. NULL is the zero time:
-- not closed, not merged, never synced.

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
    updated INTEGER,  -- every pull request updated before this is in the replica; never decreases
    synced  INTEGER   -- the start of the last sync that brought the repository up to date
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
    created            INTEGER,
    updated            INTEGER,
    closed             INTEGER,
    merged             INTEGER,
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
    submitted   INTEGER,
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
