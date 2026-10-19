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
// scopes, numbers, digests of landings, and people numbered within each
// decision.

// EgressRun names a run in the record: its id in its facts.db and its
// start time, since two facts.db files, or one deleted and scanned again,
// reuse ids; and the window of its AI-assisted shares, by which a new
// decision finds the shares already recorded that it could be subtracted
// from: every decision whose window overlaps its own.
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

// EgressRepo is one repository a recorded share adds up, as a later
// decision matches it with its own: by its key (Repo.Key) and its name
// (Repo.Name, as the answers name it); by History, the hex SHA-256 of the
// SHAs of its landings in the window, which a copy of it, or a clone whose
// origin is spelt otherwise, shares under another key ("" when no landing
// fell in the window); and by its own counts in the run. People are the
// people its landings are made of as the floor counts them
// (app.AIShareMinPeople): one number per block of addresses
// (identity.Blocks), "1", "2" and on, distinct within the decision and
// meaningless outside it (numbered in an order no one can repeat), so the
// record holds no address and no list of candidate addresses can be tested
// against it; "" is the empty address, which counts only alone. People are
// sorted, and nil for a repository of no person's landing.
type EgressRepo struct {
	Key, Name string
	History   string
	Num, Den  float64
	People    []string
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
	// it holds none, it calls decide with every recorded decision whose
	// window overlaps run.Window, records what decide returns as run's,
	// durably, and returns it and true. The lookup, the call and the write
	// are one transaction under the record's write lock, so two servers
	// never decide one run twice, nor two runs of overlapping windows
	// without the later seeing the earlier's decision. An error from decide
	// records nothing.
	Decide(ctx context.Context, run EgressRun, decide func(sent []EgressDecision) (EgressDecision, error)) (EgressDecision, bool, error)
	// Decisions returns every recorded decision, oldest first.
	Decisions(ctx context.Context) ([]EgressDecision, error)
	// Close releases the record.
	Close() error
}
