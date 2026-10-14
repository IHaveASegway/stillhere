// Package policy is the model of stillhere.toml: the noise rules, roles, bot
// and AI patterns, and thresholds that one run applies. It holds the embedded
// default policy and deny list, parses a user's file, and hashes what it
// read, so every run records exactly which policy produced its numbers.
//
// It is part of the functional core. It may import the TOML parser, and the
// command line reads the file and passes its bytes in, so the package does
// no I/O. Exactly one policy governs a run: --policy, else ./stillhere.toml,
// else the packaged default, which a user file replaces as a whole
// (docs/spec/classification.md#deny-list).
package policy

import (
	"fmt"
	"regexp"

	"github.com/IHaveASegway/stillhere/internal/domain"

	// The TOML parser behind Parse (#5). Importing it here pins the version
	// in go.mod for every branch until Parse uses it.
	_ "github.com/BurntSushi/toml"
)

// Policy is one parsed policy file.
type Policy struct {
	Name     string // where it came from: a file path, or the packaged default's name
	Version  int    // the file's own schema version
	SHA256   string // hex digest of the TOML bytes, a length prefix and the deny list
	Headline Headline
	Week     Week
	Blame    Blame
	Bots     Bots
	AI       AI
	Roles    Roles
	Paths    Paths
	Ranks    Ranks
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
	MaxBytes int64 // a larger file is skipped as too_big (default 400000)
}

// Bots is the [bots] table (docs/spec/git-invocation.md#identity-mailmap-and-bots).
type Bots struct {
	Fragments []string // lowercased substrings of a canonical name or email
}

// AI is the versioned [ai] table that tags AI-assisted changes (ADR 0007).
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
	Path       []PathRule             // ordered, first match wins
}

// PathRule assigns a role by path pattern. Path rules override extension and
// name roles.
type PathRule struct {
	Role    domain.Role
	Pattern *regexp.Regexp // RE2, matched against the repo-relative path with forward slashes
	When    []domain.Role  // empty: always; else only when the extension or name role is one of these
}

// Paths is the [paths] table: the deny list.
type Paths struct {
	Deny   []*regexp.Regexp // RE2, matched against the repo-relative path
	Source string           // "package:generated_paths.txt" or "policy"
}

// Ranks is the [ranks] table: eligibility and bootstrap settings
// (docs/metrics.md#ranking-rules).
type Ranks struct {
	MinLandings int // landings a person needs to be ranked on a change metric
	MinReviews  int // reviewed pull requests a person needs to be ranked on reviews_given
	Replicates  int // bootstrap replicates (B)
}

// Default returns the packaged policy: the embedded default.toml plus the
// embedded deny list generated_paths.txt.
func Default() (*Policy, error) {
	return nil, fmt.Errorf("policy.Default: %w (#5)", domain.ErrNotImplemented)
}

// Parse parses a user's policy file. name is used in messages. A missing
// required table or a pattern RE2 rejects is a *domain.UsageError.
func Parse(name string, data []byte) (*Policy, error) {
	return nil, fmt.Errorf("policy.Parse: %w (#5)", domain.ErrNotImplemented)
}

// Ref returns the version and hash a run records for this policy.
func (p *Policy) Ref() domain.PolicyRef {
	return domain.PolicyRef{Version: p.Version, SHA256: p.SHA256}
}
