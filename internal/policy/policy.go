// Package policy is the model of stillhere.toml: the noise rules, roles, bot
// and AI patterns, and thresholds that one run applies. It holds the embedded
// default policy and deny list, parses a user's file, and hashes what it
// read, so every run records exactly which policy produced its numbers.
//
// It is part of the functional core. It may import the TOML parser, and the
// command line reads the file and passes its bytes in, so the package does
// no I/O. Exactly one policy governs a run: --policy, else ./stillhere.toml,
// else the packaged default, which a user file replaces as a whole
// (docs/spec/classification.md#deny-list). A run stores that policy's
// source, and every reader of the run rebuilds it with FromSource rather
// than loading today's policy (docs/spec/codebase.md#run-model).
//
// The model, in types.go, is a contract between issues; the parsing and
// the packaged default are #5's.
package policy

import (
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"

	// The TOML parser behind Parse (#5). Importing it here pins the version
	// in go.mod for every branch until Parse uses it.
	_ "github.com/BurntSushi/toml"
)

// Default returns the packaged policy: the embedded default.toml plus the
// embedded deny list generated_paths.txt, with Source set.
func Default() (*Policy, error) {
	return nil, fmt.Errorf("policy.Default: %w (#5)", domain.ErrNotImplemented)
}

// Parse parses a user's policy file and sets Source. name is used in
// messages. A missing required table or key, or a pattern RE2 rejects, is a
// *domain.UsageError.
func Parse(name string, data []byte) (*Policy, error) {
	return nil, fmt.Errorf("policy.Parse: %w (#5)", domain.ErrNotImplemented)
}

// FromSource rebuilds the policy a run stored as domain.Run.PolicySource,
// parsing source exactly as Default or Parse first did, so the result has
// the same Ref. name is used in messages, such as "run 12". The caller
// compares Ref with the run's domain.PolicyRef before trusting it.
func FromSource(name string, source []byte) (*Policy, error) {
	return nil, fmt.Errorf("policy.FromSource: %w (#5)", domain.ErrNotImplemented)
}

// Ref returns the version and hash a run records for this policy.
func (p *Policy) Ref() domain.PolicyRef {
	return domain.PolicyRef{Version: p.Version, SHA256: p.SHA256}
}
