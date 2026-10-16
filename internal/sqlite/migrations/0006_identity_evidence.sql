-- Migration 6: a run keeps the evidence of who is who that its facts left
-- out (#103, #53).
--
-- forge_snapshot_links (#103). A run keeps the pull requests updated in its
-- 365-day history window (migration 4, ADR 0011), which hold every
-- metric's window. GitHub's attribution of a commit's author email to an
-- account does not age, though: it is evidence of who an address is however
-- old the pull request, and identity's same-forge-account rule reads it. On
-- the go-chi organization's replica, the window's pull requests make 2
-- identity candidates high, every synced one 8. So a run also keeps,
-- of the pull requests outside its window, only their commits' links (the
-- pull request's own commits and its merge commit, distinct): about 40
-- bytes per commit, not the whole pull request. ADR 0011's "Revisit if"
-- asks for exactly this: widen what a scan reads, rather than read the
-- replica from a stored run. A run stored before this migration has none,
-- so its identity candidates rest on its window's pull requests alone.
--
-- repo_aliases (#53). The facts are mailmapped: git rewrote every alias a
-- .mailmap names before stillhere saw a commit, so a suggested line can map
-- an address that is itself the canonical address of lines git applied,
-- and git, which applies one line per commit and never follows a chain,
-- then merges only part of the person. A run now keeps what git rewrote
-- (stillhere never parses a mailmap): each email a commit or a
-- Co-authored-by trailer was written with, with the canonical email git
-- gave it, when the two differ, read from the history's and the blamed
-- commits' %ae beside %aE and from git check-mailmap. The identities
-- answer reads them to write the lines git needs, and to leave out a line
-- git already applies. A run stored before this migration has none.

CREATE TABLE forge_snapshot_links (
    run_id       INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    repo         TEXT    NOT NULL,  -- ForgeRepo.ID, one of the run's forge_snapshot_repos
    sha          TEXT    NOT NULL,
    author_email TEXT    NOT NULL,  -- as GitHub read it, lowercased; no mailmap applied
    author_login TEXT    NOT NULL,  -- the account GitHub resolved that email to; never ''
    PRIMARY KEY (run_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE repo_aliases (
    repo_id   INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
    seq       INTEGER NOT NULL,
    email     TEXT    NOT NULL,  -- Alias.Email: as a commit or a trailer wrote it, lowercased
    canonical TEXT    NOT NULL,  -- Alias.Canonical: the email git's mailmap gave it, lowercased
    PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;
