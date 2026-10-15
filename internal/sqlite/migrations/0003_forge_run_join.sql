-- Migration 3: a run keeps the join its forge watermarks were read under (#13).
--
-- Migration 2 keeps, per run, the watermark of each forge repository the run
-- read (forge_run_watermarks), and a reader matched each one to the run's
-- repositories again through forge_repos. But forge_repos follows GitHub: a
-- rename, which sync follows by node id, changes a repository's key and
-- name, so after a later sync a stored run's report lost the "as of" of a
-- renamed repository. What a run read is the run's own, like its facts, so
-- each watermark now keeps the forge repository's name as the run read it,
-- and forge_run_repos the run's repositories its origin named then
-- (domain.ForgeJoin). A reader reads forge_repos not at all.
--
-- A watermark recorded under migration 2 keeps its times and gets no name
-- and no join; only a pre-release build of #13 wrote one. Its run's
-- readers say nothing about its GitHub data rather than guess which
-- repositories it dates (app.readForge).

ALTER TABLE forge_run_watermarks ADD COLUMN name TEXT NOT NULL DEFAULT '';  -- "owner/name" as the run read it

CREATE TABLE forge_run_repos (
    run_id INTEGER NOT NULL,
    repo   TEXT NOT NULL,  -- ForgeRepo.ID
    key    TEXT NOT NULL,  -- Repo.Key of a repository of the run whose origin named it
    PRIMARY KEY (run_id, repo, key),
    FOREIGN KEY (run_id, repo) REFERENCES forge_run_watermarks (run_id, repo) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
