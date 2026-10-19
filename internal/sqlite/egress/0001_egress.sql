-- Migration 1 of the egress record (#85, ADR 0015).
--
-- egress.db records which AI-assisted shares the MCP server lets leave the
-- machine, one decision per run, written before the first of them leaves
-- (domain.EgressLedger). It is a store of its own, not facts.db: the MCP
-- server cannot write facts.db, and facts.db is disposable, while what left
-- the machine cannot be recalled. So the record is append-only: the
-- triggers at the end refuse every UPDATE and DELETE, and a decision, once
-- recorded, is never revised. Its format must stay readable by every later
-- stillhere, so every change is a new numbered migration that adds.
--
-- It holds no fact and no email: scopes (repository keys and group names),
-- the numbers sent, and the people behind each repository's share as
-- HMAC-SHA-256 hashes under a random salt kept in egress_meta. Times are TEXT
-- in UTC with nine fractional digits, as in facts.db; the tables are STRICT.

-- The record's own settings: 'salt', 32 random bytes made when the file is
-- first opened for writing.
CREATE TABLE egress_meta (
	key   TEXT PRIMARY KEY,
	value BLOB NOT NULL
) STRICT;

-- One row per run: a run is its id in its facts.db and its start time, since
-- two facts.db files, or one deleted and scanned again, reuse ids.
CREATE TABLE decisions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id       INTEGER NOT NULL,
	run_started  TEXT    NOT NULL,
	inputs       TEXT    NOT NULL, -- hex SHA-256 of the run's AI-share inputs: the numbers it was decided over
	metric       TEXT    NOT NULL, -- ai_assisted@1
	window_since TEXT    NOT NULL, -- the shares' window: decisions of one window are checked against each other
	window_until TEXT    NOT NULL,
	version      TEXT    NOT NULL, -- the stillhere that decided it
	decided      TEXT    NOT NULL, -- the wall clock when it was recorded
	UNIQUE (run_id, run_started)
) STRICT;

CREATE INDEX decisions_by_window ON decisions (window_since, window_until);

-- The shares a decision lets leave, as they leave. None: every share of the
-- run is withheld.
CREATE TABLE decision_shares (
	decision INTEGER NOT NULL REFERENCES decisions (id),
	kind     TEXT    NOT NULL, -- all, group, repo
	key      TEXT    NOT NULL, -- '' for all
	num      REAL    NOT NULL,
	den      REAL    NOT NULL,
	PRIMARY KEY (decision, kind, key)
) STRICT;

-- Every repository a share of the decision adds up (Repo.Key), with its
-- own counts in the run, which a later decision compares with its own: a
-- repository whose counts differ between two runs of one window (a policy
-- or a .mailmap changed between the scans) would let the difference out.
CREATE TABLE decision_repos (
	decision INTEGER NOT NULL REFERENCES decisions (id),
	repo     TEXT    NOT NULL,
	num      REAL    NOT NULL,
	den      REAL    NOT NULL,
	PRIMARY KEY (decision, repo)
) STRICT;

-- The repositories whose landings each share adds up.
CREATE TABLE decision_share_repos (
	decision INTEGER NOT NULL,
	kind     TEXT    NOT NULL,
	key      TEXT    NOT NULL,
	repo     TEXT    NOT NULL,
	PRIMARY KEY (decision, kind, key, repo),
	FOREIGN KEY (decision, kind, key) REFERENCES decision_shares (decision, kind, key),
	FOREIGN KEY (decision, repo) REFERENCES decision_repos (decision, repo)
) STRICT;

-- The people of each such repository: one row per block of addresses
-- (identity.Blocks), its key hashed; '' is the empty address. A repository
-- with no row holds no person's landing.
CREATE TABLE decision_people (
	decision INTEGER NOT NULL,
	repo     TEXT    NOT NULL,
	person   TEXT    NOT NULL,
	PRIMARY KEY (decision, repo, person),
	FOREIGN KEY (decision, repo) REFERENCES decision_repos (decision, repo)
) STRICT;

-- Append-only: an answer that left the machine is a fact.
CREATE TRIGGER egress_meta_no_update BEFORE UPDATE ON egress_meta
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER egress_meta_no_delete BEFORE DELETE ON egress_meta
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decisions_no_update BEFORE UPDATE ON decisions
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decisions_no_delete BEFORE DELETE ON decisions
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_shares_no_update BEFORE UPDATE ON decision_shares
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_shares_no_delete BEFORE DELETE ON decision_shares
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_repos_no_update BEFORE UPDATE ON decision_repos
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_repos_no_delete BEFORE DELETE ON decision_repos
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_share_repos_no_update BEFORE UPDATE ON decision_share_repos
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_share_repos_no_delete BEFORE DELETE ON decision_share_repos
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_people_no_update BEFORE UPDATE ON decision_people
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
CREATE TRIGGER decision_people_no_delete BEFORE DELETE ON decision_people
BEGIN SELECT RAISE(ABORT, 'the egress record is append-only'); END;
