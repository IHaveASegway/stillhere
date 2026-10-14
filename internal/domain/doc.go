// Package domain is stillhere's core vocabulary: the facts the collectors
// learn from git (and, from M2, GitHub), the metric values computed over
// those facts, the ports through which the core reaches the outside world,
// and the typed errors that decide exit codes.
//
// It is the centre of the hexagon described in docs/spec/codebase.md. It
// imports the standard library only, and every other package may import it.
// Facts are plain values with no behaviour beyond small helpers, times are
// UTC, and slices are sorted by the package that builds them before they
// leave it.
package domain
