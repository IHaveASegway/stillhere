package domain

import "time"

// RunKind says which job wrote a run.
type RunKind string

// The kinds of run.
const (
	RunKindScan RunKind = "scan"
	RunKindSync RunKind = "sync"
)

// RunStatus is a run's publication state. A run is written as running and
// published by flipping it to complete or partial after its last row, so
// readers never see half a run (docs/spec/codebase.md#run-model).
type RunStatus string

// The run states. Readers select complete and partial runs only.
const (
	RunStatusRunning  RunStatus = "running"
	RunStatusComplete RunStatus = "complete"
	RunStatusPartial  RunStatus = "partial" // some repository failed; the others are complete
	RunStatusFailed   RunStatus = "failed"
)

// Run is one snapshot of facts at one set of HEADs, under one policy and one
// mailmap, with the metric values computed from them.
type Run struct {
	ID          int64
	Kind        RunKind
	Status      RunStatus
	Now         time.Time
	Started     time.Time
	Finished    time.Time
	ToolVersion string
	Policy      PolicyRef
	Options     map[string]string // effective options; never secrets
}
