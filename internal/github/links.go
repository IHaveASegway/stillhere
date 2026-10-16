package github

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// How the author links are read (LinkAuthors).
const (
	// linkBatch is how many commits one AuthorLinks query asks about, each
	// by its own alias. A query of 100 lookups costs 1 point (measured with
	// rateLimit on 2026-10-15: go-chi/chi, 100 emails, 2.4 s), where the
	// default branch's history would cost a point per 100 commits: on
	// cli/cli, 8 requests for its 732 emails instead of 124 for its 12,354
	// commits.
	linkBatch = 100

	// logTimeout bounds the read of a mirror's authors, which, like
	// commit-graph write, reads every commit.
	logTimeout = graphTimeout
)

// linksData is an AuthorLinks response: the commit each alias asked about,
// by alias ("c0", "c1", ...), null for one GitHub does not have.
type linksData struct {
	RateLimit rateLimitJSON          `json:"rateLimit"`
	Node      map[string]*commitJSON `json:"node"`
}

func (d *linksData) rate() rateLimitJSON { return d.RateLimit }

// LinkAuthors asks GitHub which account each author email of the mirror's
// history belongs to, but the emails skip holds: one commit per email, its
// newest, a hundred a query, and hands each query's answers to put
// (ForgeSource.LinkAuthors). GitHub's Commit.author.user is "the GitHub user
// corresponding to the email field": a function of the email, so one commit
// answers for every commit written with it.
//
// The emails come from the mirror itself, git log's %ae, the email as the
// commit holds it: neither %aE nor --use-mailmap, since GitHub reads the
// commit, not the mirror's .mailmap. git log is a local command, run
// without the credentials (docs/spec/git-invocation.md#sync-commands-network).
func (s *Source) LinkAuthors(ctx context.Context, repo domain.ForgeRepo, mirror string, skip map[string]bool,
	put func([]domain.ForgeCommit) error) error {
	if s.git == nil {
		return errors.New("github: LinkAuthors needs Config.Git")
	}
	if mirror == "" {
		return errors.New("github: LinkAuthors needs the mirror's path")
	}
	todo, err := s.mirrorAuthors(ctx, mirror, skip)
	if err != nil {
		return err
	}
	tolerated := 0
	for len(todo) > 0 {
		batch := todo[:min(linkBatch, len(todo))]
		todo = todo[len(batch):]
		vars := map[string]any{"repo": repo.ID}
		for i, c := range batch {
			vars[fmt.Sprintf("o%d", i)] = c.SHA
		}
		data, err := query[linksData](ctx, s, "AuthorLinks", linksQuery(len(batch)), vars, false, &tolerated)
		if err != nil {
			return err
		}
		if data.Node == nil {
			return errNoNode("repository", repo.Owner+"/"+repo.Name)
		}
		var answers []domain.ForgeCommit
		for i, c := range batch {
			got := data.Node[fmt.Sprintf("c%d", i)]
			if got == nil {
				continue // GitHub does not have the commit: ask again next sync
			}
			// The email as git wrote it is the key; GitHub's copy of it is
			// the same commit's.
			c.AuthorLogin = commit(got).AuthorLogin
			answers = append(answers, c)
		}
		if err := put(answers); err != nil {
			return err
		}
	}
	if tolerated > 0 {
		s.log.Info("GitHub could not resolve some accounts (deleted since); read as no account",
			"repo", repo.Owner+"/"+repo.Name, "fields", tolerated)
	}
	return nil
}

// mirrorAuthors lists the author emails of the commits reachable from the
// mirror's HEAD, lowercased, newest commit first, each once with its newest
// commit, leaving out an empty email and those skip holds.
func (s *Source) mirrorAuthors(ctx context.Context, mirror string, skip map[string]bool) ([]domain.ForgeCommit, error) {
	var out []domain.ForgeCommit
	seen := map[string]bool{}
	err := s.git.Stream(ctx, domain.GitCmd{
		Dir:     mirror,
		Args:    []string{"log", "--format=%H%x00%ae", "HEAD"},
		Timeout: logTimeout,
		Profile: domain.Sync,
	}, func(r io.Reader) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			sha, email, ok := bytes.Cut(sc.Bytes(), []byte{0})
			if !ok || !validSHA(string(sha)) {
				return errors.New("git log printed a line that is no commit and email")
			}
			e := strings.ToLower(strings.TrimSpace(string(email)))
			if e == "" || seen[e] || skip[e] {
				continue
			}
			seen[e] = true
			out = append(out, domain.ForgeCommit{SHA: string(sha), AuthorEmail: e})
		}
		return sc.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("reading the authors of the mirror %s: %w", redact.Scrub(mirror), err)
	}
	return out, nil
}

// validSHA reports whether s is a full object name: 40 hex digits (SHA-1)
// or 64 (SHA-256), lowercase as git prints them.
func validSHA(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

// linksQuery is the AuthorLinks document for n commits: an alias per commit,
// c0 to c<n-1>, each its own variable, o0 to o<n-1>, so the document holds
// no value read from a repository. GraphQL has no way to look up a list of
// objects by name in one field, and node ids of commits are not known
// locally.
func linksQuery(n int) string {
	var b strings.Builder
	b.WriteString("query AuthorLinks($repo: ID!")
	for i := range n {
		fmt.Fprintf(&b, ", $o%d: GitObjectID!", i)
	}
	b.WriteString(") {\n  rateLimit { cost remaining resetAt }\n  node(id: $repo) {\n    ... on Repository {\n")
	for i := range n {
		fmt.Fprintf(&b, "      c%d: object(oid: $o%d) { ...commit }\n", i, i)
	}
	b.WriteString("    }\n  }\n}")
	b.WriteString(commitFields)
	return b.String()
}
