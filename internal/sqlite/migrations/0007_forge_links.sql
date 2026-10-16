-- Migration 7: which GitHub account each author email of a repository's
-- history belongs to (#103's evidence, ADR 0013).
--
-- A sync once read every pull request ever, and with each one its commits'
-- links to the accounts GitHub resolved their author emails to. Since
-- migration 6 a sync reads only the pull requests updated in the history
-- window, which no metric reads beyond; identity's same-forge-account rule
-- reads those links, though, and an email's account does not age. On the
-- go-chi organization the window's pull requests linked 12 accounts to two
-- or more emails each, where the whole list had linked 59. So a sync now
-- asks GitHub about each email of the mirror's history once, through its
-- newest commit, a hundred emails a request (ForgeSource.LinkAuthors), and
-- keeps the answer here: one row per email, whatever the history's length.
--
-- An email GitHub links to no account is kept too, with author_login '', so
-- that it is not asked about again on every sync. A row is replaced only by
-- a newer answer about the same email.

CREATE TABLE forge_links (
    repo         TEXT NOT NULL REFERENCES forge_repos (id),
    author_email TEXT NOT NULL,  -- as git wrote it in the mirror, lowercased; no mailmap applied
    sha          TEXT NOT NULL,  -- the commit GitHub was asked about: the newest with that email on the default branch
    author_login TEXT NOT NULL,  -- the account GitHub links the email to; '' for none
    PRIMARY KEY (repo, author_email)
) STRICT, WITHOUT ROWID;
