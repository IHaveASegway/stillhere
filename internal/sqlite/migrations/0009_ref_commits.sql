-- Migration 9: the commits on a repository's other refs (wave 10, lane
-- knowledge; RepoFacts.RefCommits).
--
-- knowledge_loss@1 counts a person as gone quiet when they have had no
-- activity for 180 days. Until now activity was a landing on the default
-- branch or a review, so a maintainer preparing a release on a branch, or
-- anyone whose work waits in an open pull request's branch, looked gone
-- (encode/httpx read 100% while its maintainer committed on version-1.0
-- two weeks before). A scan now reads, with the history, the commits of the
-- clone's branches, remote-tracking branches and tags that the commit the
-- run reads does not hold, committed in the 365-day history window, and
-- keeps who wrote each and when: no parents, no subject, no diff. They are
-- facts of the run like its commits, read back by every reader, so a
-- stored run's knowledge loss stays a function of what it read. A run
-- stored before this migration has none, and its knowledge_loss@1 values
-- were computed under the earlier rule (landings and reviews), which its
-- pages' per-query activity no longer follows: its readers compute its
-- knowledge values again from its facts (app's computeCorrected, told by
-- the going_quiet detail those values lack), counting its pull requests
-- but no other ref's commit, and log that a new scan counts those.
--
-- Size: one row per commit, about 230 bytes with its indexes; cli/cli's mirror holds 473
-- such commits in a year, encode/httpx's clone 18.

CREATE TABLE ref_commits (
    repo_id      INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    sha          TEXT    NOT NULL,
    author_name  TEXT    NOT NULL,
    author_email TEXT    NOT NULL,  -- mailmapped and lowercased, as commits.author_email
    author_time  TEXT,
    commit_time  TEXT,
    PRIMARY KEY (repo_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE ref_commit_coauthors (
    repo_id    INTEGER NOT NULL REFERENCES run_repos (id) ON DELETE CASCADE,
    commit_seq INTEGER NOT NULL,
    seq        INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    email      TEXT    NOT NULL,
    PRIMARY KEY (repo_id, commit_seq, seq)
) STRICT, WITHOUT ROWID;
