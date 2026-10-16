package collect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file holds the co-author reader that the history (#6) and blame (#7)
// collectors share, so trailers are parsed and canonicalised in one place
// (docs/spec/codebase.md#working-in-parallel). History reads the trailers
// from its own log streams and canonicalises them with coAuthorCanonical;
// blame calls coAuthors for the commits it attributes lines to, which can be
// older than any history window. coAuthors reads the same format
// (historyLogFormat) with the same parser (historyParseLog) as History's
// logs, and both run under the runner's common prefix, which pins how git
// parses trailers (core.commentChar, trailer.separators;
// docs/spec/git-invocation.md), so both give one commit the same
// co-authors, in the same order.

// coAuthorFormat is the log placeholder for a commit's Co-authored-by trailer
// values, separated by U+001E. Git matches the key in any case, so
// Co-Authored-By: is found too (verified on 2.50.1). unfold joins a value
// that a trailer continued on an indented line, so no value holds a newline.
const coAuthorFormat = "%(trailers:key=Co-authored-by,valueonly,separator=%x1e,unfold)"

// coAuthors returns the Co-authored-by trailers of each of shas, keyed by
// full SHA, in trailer order and canonicalised with git's mailmap. A commit
// without co-authors is absent from the map. It reads every commit in one
// batched call (git log --no-walk --stdin with historyLogFormat, whose
// trailer field is %(trailers:key=Co-authored-by,valueonly)), matching the
// trailer key in any case, and canonicalises the identities with one git
// check-mailmap --stdin.
//
// A trailer value that names no email in angle brackets, such as
// "Co-authored-by: someone", is not an identity and is left out, as is a
// second trailer for a person already listed. shas must be full SHAs of
// commits in repo; the order of shas does not matter. It adds to aliases
// what the mailmap rewrote in those commits, their authors' emails and
// their trailers'.
func coAuthors(ctx context.Context, git domain.GitRunner, repo domain.Repo, shas []string, aliases aliasSet) (map[string][]domain.Person, error) {
	want := slices.Clone(shas)
	slices.Sort(want)
	want = slices.Compact(want)
	if len(want) == 0 {
		return nil, nil
	}
	if slices.ContainsFunc(want, func(sha string) bool { return !historySHA(sha) }) {
		// --stdin also reads options such as --all, so only a SHA may pass.
		return nil, errors.New("collect.coAuthors: every commit must be named by its full SHA")
	}

	raw := make(map[string][]domain.Person)
	args := []string{"log", "--no-walk", "--stdin", "-z", "--no-color", "--encoding=UTF-8",
		"--pretty=format:" + historyLogFormat, "--"}
	cmd := domain.GitCmd{Dir: repo.Path, Args: args, Stdin: historyStdin(want)}
	err := git.Stream(ctx, cmd, func(r io.Reader) error {
		return historyParseLog(r, func(rec *historyRecord) error {
			aliases.add(rec.written, rec.commit.Author.Email)
			if rec.coAuthors != nil {
				raw[rec.commit.SHA] = rec.coAuthors
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("collect.coAuthors: reading trailers: %w", err)
	}
	canonical, err := coAuthorCanonical(ctx, git, repo, raw, aliases)
	if err != nil {
		return nil, fmt.Errorf("collect.coAuthors: %w", err)
	}
	return canonical, nil
}

// coAuthorParse splits the output of coAuthorFormat into the identities it
// names, as the trailers wrote them: before the mailmap, and with the
// email's case as written. It returns nil when no value names an identity.
func coAuthorParse(field string) []domain.Person {
	var people []domain.Person
	for _, value := range strings.Split(field, "\x1e") {
		if p, ok := coAuthorContact(value); ok {
			people = append(people, p)
		}
	}
	return people
}

// coAuthorContact parses one "Name <email>" the way git's split_ident_line
// does, which is how git check-mailmap reads the same text: the name is
// everything before the first '<', and the email runs from there to the next
// '>'. Anything after that '>' is ignored. A value with no email, or with a
// byte that would break git check-mailmap's one-contact-per-line input, is
// not an identity.
func coAuthorContact(value string) (domain.Person, bool) {
	if strings.ContainsAny(value, "\n\r\x00") {
		return domain.Person{}, false
	}
	name, rest, ok := strings.Cut(value, "<")
	if !ok {
		return domain.Person{}, false
	}
	email, _, ok := strings.Cut(rest, ">")
	email = strings.TrimSpace(email)
	if !ok || email == "" {
		return domain.Person{}, false
	}
	return domain.Person{Name: strings.TrimSpace(name), Email: email}, true
}

// aliasSet gathers what git's mailmap rewrote while a collector read a
// repository (#53, domain.Alias): each email a commit or a trailer was
// written with, with the canonical email git gave it. A nil set gathers
// nothing.
type aliasSet map[domain.Alias]bool

// add records that git gave the email written the canonical email
// canonical, unless the two are one, or either is empty.
func (s aliasSet) add(written, canonical string) {
	written, canonical = strings.ToLower(strings.TrimSpace(written)), strings.ToLower(strings.TrimSpace(canonical))
	if s != nil && written != "" && canonical != "" && written != canonical {
		s[domain.Alias{Email: written, Canonical: canonical}] = true
	}
}

// sorted returns the set by Email, then Canonical; nil when empty.
func (s aliasSet) sorted() []domain.Alias {
	return MergeAliases(slices.Collect(maps.Keys(s)))
}

// MergeAliases returns the aliases of several collectors (History's and
// Blame's) as RepoFacts.Aliases holds them: sorted by Email, then
// Canonical, each once; nil when there are none.
func MergeAliases(lists ...[]domain.Alias) []domain.Alias {
	var out []domain.Alias
	for _, l := range lists {
		out = append(out, l...)
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, func(a, b domain.Alias) int {
		return cmp.Or(strings.Compare(a.Email, b.Email), strings.Compare(a.Canonical, b.Canonical))
	})
	return slices.Compact(out)
}

// coAuthorLine is a person as one line of git check-mailmap input.
func coAuthorLine(p domain.Person) string {
	if p.Name == "" {
		return "<" + p.Email + ">"
	}
	return p.Name + " <" + p.Email + ">"
}

// coAuthorCanonical maps the co-authors of each commit, as coAuthorParse
// read them, through the mailmap with one git check-mailmap --stdin for the
// whole repository. It keeps trailer order, because credit reads the first
// human co-author (ADR 0007), lowercases every email (domain.Person), and
// keeps only the first trailer for each canonical email. A mailmap entry
// that maps a contact to no email drops that co-author. A commit left
// without co-authors is absent from the result, which is nil when no commit
// has any.
//
// git check-mailmap reads the same mailmaps as the log placeholders %aN and
// %aE: the -c mailmap.file= the runner passes, and the repository's own
// .mailmap (gitmailmap(5)). Each trailer email it rewrote is added to
// aliases.
func coAuthorCanonical(ctx context.Context, git domain.GitRunner, repo domain.Repo, raw map[string][]domain.Person,
	aliases aliasSet) (map[string][]domain.Person, error) {
	var lines []string
	for _, people := range raw {
		for _, p := range people {
			lines = append(lines, coAuthorLine(p))
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)

	out, err := git.Run(ctx, domain.GitCmd{
		Dir:   repo.Path,
		Args:  []string{"check-mailmap", "--stdin"},
		Stdin: strings.NewReader(strings.Join(lines, "\n") + "\n"),
	})
	if err != nil {
		return nil, fmt.Errorf("canonicalising co-authors: %w", err)
	}
	// One output line per input line, in input order.
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(lines) {
		return nil, fmt.Errorf("canonicalising co-authors: git check-mailmap answered %d of %d contacts", len(got), len(lines))
	}
	canonical := make(map[string]domain.Person, len(lines))
	for i, line := range got {
		if p, ok := coAuthorContact(line); ok {
			p.Email = strings.ToLower(p.Email)
			canonical[lines[i]] = p
			if written, ok := coAuthorContact(lines[i]); ok {
				aliases.add(written.Email, p.Email)
			}
		}
	}

	result := make(map[string][]domain.Person, len(raw))
	for sha, people := range raw {
		var list []domain.Person
		for _, p := range people {
			c, ok := canonical[coAuthorLine(p)]
			if ok && !slices.ContainsFunc(list, func(q domain.Person) bool { return q.Email == c.Email }) {
				list = append(list, c)
			}
		}
		if list != nil {
			result[sha] = list
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}
