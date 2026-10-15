package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// MetricID names one version of one metric, such as durable@1. A change to
// a metric's definition is a new version, never a silent edit
// (docs/metrics.md).
type MetricID struct {
	Name    string
	Version int // 1 or more; 0 means the latest version
}

// String returns the published form, "durable@1", or the bare name
// "durable" when Version is 0 (the latest version).
func (m MetricID) String() string {
	if m.Version == 0 {
		return m.Name
	}
	return m.Name + "@" + strconv.Itoa(m.Version)
}

// ParseMetricID parses "durable@1" into {durable 1} and "durable" into
// {durable 0}, where version 0 means the latest. The name must start with a
// lowercase letter followed by lowercase letters, digits or underscores, and
// a version must be a positive integer written without a sign or leading
// zeros, so every accepted id has exactly one spelling. Any other input is a
// *UsageError, because ids come from the command line.
func ParseMetricID(s string) (MetricID, error) {
	name, version, hasVersion := strings.Cut(s, "@")
	if !validMetricName(name) {
		return MetricID{}, &UsageError{Msg: fmt.Sprintf(
			"invalid metric id %q: the name must match [a-z][a-z0-9_]*, as in durable@1", s)}
	}
	if !hasVersion {
		return MetricID{Name: name}, nil
	}
	v, ok := parseVersion(version)
	if !ok {
		return MetricID{}, &UsageError{Msg: fmt.Sprintf(
			"invalid metric id %q: the version must be a positive integer, as in durable@1", s)}
	}
	return MetricID{Name: name, Version: v}, nil
}

func validMetricName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func parseVersion(s string) (int, bool) {
	if s == "" || s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

// Subject is what a metric value is about.
type Subject struct{ Kind, Key string } // Kind is one of the Subject* constants

// The kinds of Subject and what their Key holds.
const (
	SubjectPerson = "person" // canonical email
	SubjectArea   = "area"   // Repo.Key + ":" + path prefix
	SubjectRepo   = "repo"   // Repo.Key
	SubjectChange = "change" // landing SHA, or pull request id from M2
)

// Scope is the population a value or rank was computed within. Ranks never
// mix groups unless asked to.
type Scope struct{ Kind, Key string } // Kind is one of the Scope* constants

// The kinds of Scope and what their Key holds.
const (
	ScopeRepo  = "repo"  // Repo.Key
	ScopeGroup = "group" // Group.Name
	ScopeAll   = "all"   // ""
)

// MetricValue is one metric's value for one subject, within one scope and
// window.
type MetricValue struct {
	Metric  MetricID
	Scope   Scope
	Subject Subject
	Window  Window
	Value   float64
	Num     float64            // ratio metrics; otherwise equal to Value
	Den     float64            // ratio metrics; otherwise 1
	N       int                // the sample size the eligibility rule reads (landings, reviewed pull requests)
	Detail  map[string]float64 // context numbers shown next to Value (encoding/json sorts the keys)
}

// RankInterval is one subject's bootstrap interval for a ranked metric
// (docs/metrics.md#ranking-rules).
type RankInterval struct {
	Metric         MetricID
	Scope          Scope
	Subject        Subject
	Window         Window
	Estimate       float64
	Lo, Hi         float64 // 95% interval of the value; 0 when not eligible
	RankLo, RankHi int     // 95% interval of the rank; 0 when not eligible
	N              int
	Eligible       bool
}
