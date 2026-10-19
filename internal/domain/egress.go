package domain

import (
	"context"
	"time"
)

// The egress record (#85, ADR 0015): what the MCP server has let leave the
// machine of a run's AI-assisted shares. An answer that left is a fact
// (Helland, "Immutability Changes Everything", CIDR 2015): the record keeps
// each run's decision about which shares may leave, written before the
// first of them does, as a transactional outbox records a message before
// it is sent, and every later decision is checked against it. It is a store
// of its own, apart from facts.db, which the MCP server cannot write and a
// user may delete and scan again; it holds no fact and no email, only
// scopes, numbers and salted hashes.

// EgressRun names a run in the record: its id in its facts.db and its
// start time, since two facts.db files, or one deleted and scanned again,
// reuse ids; and the window of its AI-assisted shares, by which a new
// decision finds the shares already recorded that it could be subtracted
// from (the same as-of and length).
type EgressRun struct {
	ID      int64
	Started time.Time
	Window  Window
}

// EgressShare is one AI-assisted share a decision lets leave: its scope, the
// repositories whose landings its value adds up (Repo.Key, sorted), and its
// value as it leaves.
type EgressShare struct {
	Scope    Scope
	Repos    []string
	Num, Den float64
}

// EgressRepo is one repository a recorded share adds up: its own counts in
// the run, which a later decision of the window compares with its own, and
// the people its landings are made of as the floor counts them
// (app.AIShareMinPeople): one key per block of addresses (identity.Blocks),
// each a hex HMAC-SHA-256 of the block's key under the record's salt, so
// the record holds no address; "" is the empty address, which counts only
// alone. People are sorted, and nil for a repository of no person's
// landing.
type EgressRepo struct {
	Key      string
	Num, Den float64
	People   []string
}

// EgressDecision is one run's recorded decision.
type EgressDecision struct {
	Run    EgressRun
	Inputs string        // hex SHA-256 of the run's AI-share inputs (app): the numbers the decision was made over
	Metric MetricID      // the metric whose shares it decides: ai_assisted@1
	Shares []EgressShare // the shares that may leave, by kind and key; none: every share of the run is withheld
	Repos  []EgressRepo  // every repository a share in Shares adds up, by key
	// Version is the stillhere that decided it, and Decided the wall clock
	// when it was recorded, which the record stamps: neither changes a
	// number.
	Version string
	Decided time.Time
}

// EgressLedger is the port to the egress record. Its one writer is the MCP
// server, and only through Decide; readers audit it (Decisions).
type EgressLedger interface {
	// Decide returns the decision the record holds for run, and false. When
	// it holds none, it calls decide with the record's salt and every
	// decision recorded for run.Window, records what decide returns as
	// run's, durably, and returns it and true. The lookup, the call and the
	// write are one transaction under the record's write lock, so two
	// servers never decide one run twice, nor two runs of one window without
	// each seeing the other's decision. An error from decide records
	// nothing.
	Decide(ctx context.Context, run EgressRun, decide func(salt []byte, sent []EgressDecision) (EgressDecision, error)) (EgressDecision, bool, error)
	// Decisions returns every recorded decision, oldest first.
	Decisions(ctx context.Context) ([]EgressDecision, error)
	// Close releases the record.
	Close() error
}
