-- Migration 5: each line owner keeps the path its lines had in their commit (#54).
--
-- durable@1 divides the lines blame still gives a commit at HEAD by the
-- lines its own patch added, and leaves out of both sums the lines whose
-- count one side cannot know, such as those of a change git took for
-- binary (docs/metrics.md, "Missing facts are not zeros"). The history
-- names such a change by its commit and the path in that commit, while a
-- line owner named only the path at HEAD, so the metric matched the two
-- through the renames the history had seen. Those are only some of the
-- renames blame follows: the history sees none outside its window and
-- gives up on a commit with too many candidates. Blame names the path the
-- lines had in the commit itself (its porcelain "filename"), so a line
-- owner now keeps it, and both sums match lines by (commit, path in it).
--
-- '' means the path at HEAD, as commit_files.old_path's '' means no rename.
-- A row written before this migration gets '', the path at HEAD: its run's
-- durable@1, computed again by a reader, then matches a binary change to
-- the lines at HEAD that blame gave its commit in the same path only.

ALTER TABLE line_owners ADD COLUMN orig_path TEXT NOT NULL DEFAULT '';  -- LineOwner.OrigPath: the path in commit_sha; '' when it is path
