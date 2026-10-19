-- Migration 11: the other names GitHub resolves to a repository (wave 11,
-- round 3's KT-2).
--
-- GitHub redirects a renamed or transferred repository's old name to the
-- repository, for its pages, its API and git alike, so a clone made before
-- the move keeps fetching while its origin still names the old owner or
-- name. In round 3 a sync asked for a transferred repository by its old
-- name got it under its new owner's, copied 535 pull requests under that
-- name, and kept nothing of the name it was asked for; a scan of the
-- clone, whose origin named the old one, then joined none of them
-- (domain.ForgeJoin matched origins by the current name alone) and said
-- nothing.
--
-- forge_repo_aliases is the replica's record of each such name: one a sync
-- looked up that GitHub answered under another name, or the one a mirror's
-- path held when a sync found the mirror there by its node id. A name
-- resolves to one repository: a later sync that sees another repository
-- answer to it replaces the row, and a repository that holds the name now
-- deletes it (sqlite.PutForgeRepos), since GitHub ends a redirect when a
-- repository takes the old name. Only a sync writes it (ADR 0004); a scan
-- joins a clone by it (ForgeRepo.Aliases).
--
-- forge_snapshot_repo_aliases keeps, with a scan run, the aliases of the
-- forge repositories it joined, as the run keeps the repositories
-- themselves (ADR 0011), so its readers resolve a repository argument by
-- any name GitHub gave the repository then. A run stored before this
-- migration has none.

CREATE TABLE forge_repo_aliases (
    name TEXT NOT NULL PRIMARY KEY,                    -- "owner/name", lowercased: never a repository's own name
    repo TEXT NOT NULL REFERENCES forge_repos (id)     -- the ForgeRepo.ID GitHub resolves the name to
) STRICT, WITHOUT ROWID;

CREATE TABLE forge_snapshot_repo_aliases (
    run_id   INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    repo_seq INTEGER NOT NULL,  -- the forge_snapshot_repos row (its seq) the name is an alias of
    seq      INTEGER NOT NULL,
    name     TEXT    NOT NULL,  -- "owner/name", lowercased
    PRIMARY KEY (run_id, repo_seq, seq)
) STRICT, WITHOUT ROWID;
