package policy

import (
	"regexp"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file is the policy model: a contract between issues. #5 produces it
// (Default, Parse); classification, metrics and reports consume it. Change
// it only together with docs/spec/codebase.md.
//
// Every key of a required table is required. A missing table or key is a
// *domain.UsageError and is never filled in from the packaged default
// (docs/spec/classification.md#deny-list), so the hash describes the whole
// policy. The values the specs call defaults, such as [blame] max_bytes
// 400000, are the values of the packaged default.toml.

// Policy is one parsed policy file.
type Policy struct {
	Name     string // where it came from: a file path, the packaged default's name, or a run for FromSource
	Version  int    // the file's own schema version
	SHA256   string // hex digest of Source
	Source   []byte // the bytes SHA256 hashes: the TOML, a length prefix and the deny list; a run stores them (domain.Run.PolicySource)
	Headline Headline
	Week     Week
	Blame    Blame
	Bots     Bots
	AI       AI
	Roles    Roles
	Paths    Paths
}

// Headline is the [headline] table: the number a still-here report leads with.
type Headline struct {
	Metric string // "prod_nonblank_lines"
}

// Week is the [week] table: the defaults --days and --week-sort override.
type Week struct {
	Days int    // window length
	Sort string // "lines_added" or "commits"
}

// Blame is the [blame] table.
type Blame struct {
	MaxBytes int64 // a larger file is skipped as too_big (400000 in the packaged policy)
}

// Bots is the [bots] table (docs/spec/git-invocation.md#identity-mailmap-and-bots).
type Bots struct {
	Fragments []string // lowercased substrings of a canonical name or email
}

// AI is the versioned [ai] table that tags AI-assisted changes (ADR 0007).
//
// Its rules match the two signals the history collector keeps: the emails
// of Co-authored-by trailers, and commit authors. A rule on another
// trailer, such as Cursor's unverified "Made-with: Cursor", needs the
// collector to keep that trailer (domain.Commit has no field for it) and a
// new ai_assisted version; it is not a policy change alone.
type AI struct {
	Version        int
	CoauthorEmails []string // co-author emails of coding agents, such as noreply@anthropic.com
	AgentAuthors   []string // fragments for agents that author commits with a human co-author (Copilot)
}

// Roles is the [roles] table: how a path gets its role
// (docs/spec/classification.md#roles).
type Roles struct {
	Extensions map[string]domain.Role // keys lowercased, with the dot: ".go"
	Names      map[string]domain.Role // exact basenames: "Dockerfile"
	Path       []PathRule             // in order; see PathRule for which one wins
}

// PathRule assigns a role by path pattern. Path rules override extension
// and name roles. They are tried in order, and the first rule whose Pattern
// matches the path and whose When admits the path's extension or name role
// wins: db/migrations/seeds/x.sql (extension role sql) skips a migration
// rule whose When is [prod] and takes a seed rule whose When is [prod, sql].
//
// MaxBytes, when set, bounds the files the rule gives its role: a file the
// rule admits whose known size is larger is skipped as too_big, by this
// rule, as [blame] max_bytes skips any file. The packaged policy uses it
// for data under test directories: a JSON file of test vectors is test
// data, not test code, once it is that large (policy revision 3).
type PathRule struct {
	Role     domain.Role
	Pattern  *regexp.Regexp // RE2, matched against the repo-relative path with forward slashes
	When     []domain.Role  // empty: any path; else only a path whose extension or name role is one of these
	MaxBytes int64          // 0: no bound; else the largest size, in bytes, the rule gives its role to
	// Note is why the rule exists, as the file's own comment in the rule's
	// [[roles.path]] table says it: its first sentence, up to a colon, such
	// as "Example code documents the product; it is not the product". ""
	// when the table has no comment. It is read from the TOML text, which
	// the hash covers, so it changes nothing a run counts; 'stillhere paths'
	// prints it beside the rule that decided a path.
	Note string
}

// DenyRule is one pattern of a deny list and where the policy holds it:
// what Paths.Denying returns, for a reader that says which rule denied a
// path (classify.Explain).
type DenyRule struct {
	Pattern string // as the policy writes it
	Index   int    // its position among the deny patterns, from 1: [paths] deny's element in a policy file
	Line    int    // its line in the packaged deny list (generated_paths.txt); 0 for a policy file's [paths] deny
	Note    string // the packaged list's label for it, the first words of the comment above its group, such as "Minified JavaScript and CSS"; "" for a policy file's
}

// Paths is the [paths] table: the deny list. The packaged default names
// its embedded deny list with package_file; a user file sets deny instead,
// and setting both or neither is a usage error.
type Paths struct {
	Deny   []*regexp.Regexp // RE2, matched against the repo-relative path; match with Denies
	Source string           // "package:generated_paths.txt" or "policy"

	// filter holds, for each Deny pattern, the literals one of which any
	// match must contain, so Denies can skip a pattern without running it.
	// Default, Parse and FromSource build it; a pattern without one is
	// always tried.
	filter []denyFilter
	// origins holds, for each Deny pattern of a package file, its line and
	// label there (DenyRule.Line, DenyRule.Note); nil for a policy file's.
	origins []denyOrigin
}
