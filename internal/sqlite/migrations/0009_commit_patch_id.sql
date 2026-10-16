-- Migration 9: each commit keeps its patch id (wave 10, the merges lane).
--
-- A maintenance branch that carries cherry-picks of fixes already on the
-- default branch, merged back into it, brings each of those changes onto
-- the default branch a second time, as new commits with new SHAs. landed@2
-- credited each such copy as a second change, and durable@1 scored it 0,
-- since blame gives its lines to the original (docs/metrics.md, "Change
-- facts"). git patch-id --stable is git's own fingerprint of a change, which
-- a copy keeps (domain.Commit.PatchID), so the metrics read it to credit a
-- change once.
--
-- '' means none: a merge, a shallow clone's boundary, an empty patch (a
-- whitespace-only change under -w; a mode change or a rename has one), a
-- partial clone's commit, and every row written before this migration, so
-- a reader computing over an older run finds no copy in it, as that run's
-- scan did not.

ALTER TABLE commits ADD COLUMN patch_id TEXT NOT NULL DEFAULT '';  -- Commit.PatchID; '' when none was read
