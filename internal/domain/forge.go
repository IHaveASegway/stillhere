package domain

// This file belongs to the GitHub sync (#14): it defines the forge facts
// and fills the two forge ports below in M2. Nothing implements the ports
// before then, so adding their methods breaks no other package.

// ForgeFacts is the replica of forge data (pull requests, reviews, login
// links and watermarks) that a run read. It is empty until the GitHub sync
// (#14) defines it in M2.
type ForgeFacts struct{}

// ForgeSource is the port to a forge's API: GitHub's GraphQL and REST APIs
// in v1, GitLab later (ADR 0004). The GitHub sync (#14) defines its methods.
type ForgeSource interface{}

// ForgeStore is the port to the forge replica in facts.db: pull requests,
// reviews and login links upserted by node id, with a watermark per
// repository. It is a port of its own rather than more FactStore methods
// because the two hold different kinds of data: FactStore holds per-run
// snapshots of git facts, while forge facts are a replica that outlives
// runs (docs/spec/codebase.md#run-model). Keeping them apart also lets the
// sync add its methods without touching FactStore or its implementation
// (Interface Segregation Principle). The GitHub sync (#14) defines its
// methods.
type ForgeStore interface{}
