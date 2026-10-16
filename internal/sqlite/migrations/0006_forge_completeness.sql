-- Migration 6: how complete a repository's forge replica is (#101, ADR 0013).
--
-- Until now a watermark said only how far a sync had read, as the newest
-- update it read, a value taken from the pull requests themselves. GitHub's
-- list order disagreed with those values on a real repository, and one pull
-- request out of order moved the watermark four months ahead of what was
-- stored, leaving a hole later syncs never filled. A watermark is now set
-- only by a pass that completed, to the time the pass began, and three
-- columns say how far it can be trusted:
--
-- since    the oldest last update the replica is complete from: the start of
--          the history window, or of --since, that the last full pass read
--          down to. NULL for a replica whose first pass read every pull
--          request, as every one stored before this migration did.
-- missing  what the last completed sync's completeness check found: how many
--          pull requests GitHub counts as updated in the window that the
--          replica lacks. 0 when they match; while it is not 0, a sync reads
--          the whole window again and the run's pages carry a caveat.
-- pending  1 while pages of a pass that has not completed are stored: with
--          synced NULL, the first sync of the repository stopped halfway.
--
-- A run keeps the same three with the watermark it read
-- (forge_run_watermarks), so a stored run says what it said when it was
-- scanned. A row stored before this migration reads as complete from the
-- repository's first pull request, as it claimed; the next sync's check
-- finds a hole it hides and repairs it.

ALTER TABLE forge_watermarks ADD COLUMN since TEXT;
ALTER TABLE forge_watermarks ADD COLUMN missing INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forge_watermarks ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;

ALTER TABLE forge_run_watermarks ADD COLUMN since TEXT;
ALTER TABLE forge_run_watermarks ADD COLUMN missing INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forge_run_watermarks ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;
