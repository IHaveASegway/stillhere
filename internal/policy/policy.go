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
	_ "embed" // the packaged policy, below
	"fmt"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The packaged policy. They are variables only because go:embed needs one;
// nothing assigns to them, and strings cannot be changed in place.
var (
	//go:embed default.toml
	defaultTOML string

	//go:embed generated_paths.txt
	generatedPaths string
)

const (
	// defaultName is the packaged policy's Policy.Name.
	defaultName = "package:default.toml"

	// packageFile is the only file [paths] package_file may name: the
	// embedded deny list.
	packageFile = "generated_paths.txt"
)

// Default returns the packaged policy: the embedded default.toml plus the
// embedded deny list generated_paths.txt, with Source set.
func Default() (*Policy, error) {
	p, problems := build(defaultName, []byte(defaultTOML), []byte(generatedPaths), fromPackage)
	if len(problems) > 0 {
		// A broken build, not a usage error: TestPolicy loads the packaged
		// policy, so this cannot ship.
		return nil, fmt.Errorf("policy.Default: %s: %s", defaultName, strings.Join(problems, "; "))
	}
	return p, nil
}

// Parse parses a user's policy file and sets Source. name is used in
// messages. A missing required table or key, or a pattern RE2 rejects, is a
// *domain.UsageError.
//
// Every key of every required table must be present, and a key the schema
// does not know is an error rather than ignored, so a misspelt key cannot
// leave a value silently at zero. The deny list is [paths] deny; a user file
// may not name a package_file, because its hash would not cover that file.
func Parse(name string, data []byte) (*Policy, error) {
	p, problems := build(name, data, nil, fromUser)
	if len(problems) > 0 {
		return nil, &domain.UsageError{Msg: name + ": " + strings.Join(problems, "; ")}
	}
	return p, nil
}

// FromSource rebuilds the policy a run stored as domain.Run.PolicySource,
// parsing source exactly as Default or Parse first did, so the result has
// the same Ref. name is used in messages, such as "run 12". The caller
// compares Ref with the run's domain.PolicyRef before trusting it.
//
// It reads nothing but source: a run made with the packaged policy carries
// its deny list inside source, so a newer packaged default never changes
// what an old run's policy says. A source that does not parse is an error,
// but not a usage error: the user did not type it.
func FromSource(name string, source []byte) (*Policy, error) {
	tomlText, denyText, err := openSource(source)
	if err != nil {
		return nil, fmt.Errorf("%s: stored policy: %w", name, err)
	}
	p, problems := build(name, tomlText, denyText, fromStore)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: stored policy: %s", name, strings.Join(problems, "; "))
	}
	return p, nil
}

// Ref returns the version and hash a run records for this policy.
func (p *Policy) Ref() domain.PolicyRef {
	return domain.PolicyRef{Version: p.Version, SHA256: p.SHA256}
}

// Packaged reports whether p is the policy stillhere ships (Default), or a
// stored run's copy of it: only the packaged policy names a package file
// for its deny list, so a policy file, even one 'stillhere policy' wrote
// out from it, is not.
func (p *Policy) Packaged() bool {
	return strings.HasPrefix(p.Paths.Source, pathsFromPackage)
}
