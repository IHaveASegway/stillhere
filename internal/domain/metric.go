package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
	SubjectGroup  = "group"  // Group.Name: a group's own numbers, such as its rollup of the concentration walk
	SubjectChange = "change" // landing SHA, or pull request id from M2
	// SubjectAll is the whole run's own numbers, in scope all, Key "": a
	// total over every repository of the run, such as landed@2's and
	// review_coverage@1's, which no group's value gives when the run's
	// repositories are in several groups or in none.
	SubjectAll = "all"
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

// Trace is what one person's value of a metric that is not ranked is made
// of (#109), so every number reaches the commits behind it: the landings
// landed@1 and landed@2 count, the blamed commits whose lines surviving@1
// sums, the files majority_owner@1 counts. A ranked metric's are its
// clusters instead (metrics.Ranked). Each list is in the order a page
// prints it.
type Trace struct {
	Window   Window          // the window the value reads; zero for a value at HEAD
	Landings []TracedLanding // by repository, then time
	Commits  []TracedCommit  // the most surviving lines first
	Files    []TracedFile    // the most of the person's lines first
}

// Activity is a person's last activity in a run, by knowledge_loss@1's
// rule (metrics.LastActivity): the newest of their landings, reviews,
// pull requests opened or pushed to, and commits on any ref of the clone,
// in the history window before Now.
type Activity struct {
	Time time.Time
	Kind string // ActivityLanding, ActivityReview, ActivityPullRequest or ActivityCommit
	// QuietIn is how many whole days are left at Now before 180 days have
	// passed since Time, when knowledge_loss@1 starts to count the person as
	// gone quiet; -1 once they have. GoingQuiet: within 30 days of it, which
	// knowledge_loss@1's going_quiet detail counts.
	QuietIn    int
	GoingQuiet bool
}

// The kinds of activity, in the order a tie at one instant is told by.
const (
	ActivityLanding     = "landing"      // a commit credited to them reached the default branch in a landing
	ActivityReview      = "review"       // a review they submitted on GitHub
	ActivityPullRequest = "pull_request" // a pull request they opened, or one of theirs they pushed a commit of their own to
	ActivityCommit      = "commit"       // a commit credited to them on another ref of the clone: a branch or a tag
)

// TracedLanding is one landing a landed value counts for the person.
type TracedLanding struct {
	Repo    string // Repo.Key
	SHA     string
	Time    time.Time // committer time
	Merge   bool
	Commits int // the person's commits in it that the value counts: landed@2's non-merge ones, landed@1's landing commit
	// Lines and ProdLines are the lines those commits added, numstat
	// insertions on paths with a role and the prod subset; -1 when the
	// history counted no numstat (a partial clone).
	Lines, ProdLines int
}

// TracedCommit is one blamed commit whose prod lines at HEAD surviving@1
// credits to the person.
type TracedCommit struct {
	Repo  string    // Repo.Key
	SHA   string    // the blamed commit, which may be older than the history
	Time  time.Time // its author time, as blame gives it
	Lines int       // its non-blank prod lines at HEAD
}

// TracedFile is one prod file at HEAD the person holds at least half of.
type TracedFile struct {
	Repo  string // Repo.Key
	Path  string
	Lines int // the person's non-blank lines in it
	Of    int // the file's non-blank lines
}
