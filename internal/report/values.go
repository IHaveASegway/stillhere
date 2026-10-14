package report

import (
	"cmp"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file is the one place that knows how internal/metrics lays out the
// values a report reads: which metric version, which subject and scope, and
// which Detail key holds each printed number. docs/spec/report-json.md
// ("Where the numbers come from") is the same table for readers; change
// both together. A report reads specific versions on purpose: moving a
// section to a new metric version is a reviewed change, never a silent one.

// The metric versions a report reads.
const (
	MetricLanded        = "landed@1"                  // week rows: Value is the landings credited; Detail lines_added, prod_lines_added
	MetricSurviving     = "surviving@1"               // still-here rows: Value is prod non-blank lines; Detail majority_files, lines_<role>, repos, landings_365d
	MetricMajority      = "majority_owner@1"          // a person's majority files, when surviving@1 has no majority_files detail
	MetricConcentration = "knowledge_concentration@1" // Value is authors_in_prefix; Detail files_with_majority, bots_in_prefix
	MetricKnowledgeLoss = "knowledge_loss@1"          // Num is lines held by people gone quiet, Den lines held by people
	MetricAIAssisted    = "ai_assisted@1"             // Num is AI-assisted landings, Den landings in 365 days
	MetricWhoKnows      = "who_knows@1"               // per person under a path: Value is prod lines; Detail majority_files, recent_landings
)

// The Detail keys a report reads.
const (
	detailLinesAdded        = "lines_added"
	detailProdLinesAdded    = "prod_lines_added"
	detailMajorityFiles     = "majority_files"
	detailRepos             = "repos"
	detailLandings365d      = "landings_365d"
	detailRolePrefix        = "lines_" // lines_prod, lines_test, ...: one per role
	detailFilesWithMajority = "files_with_majority"
	detailBotsInPrefix      = "bots_in_prefix"
	detailRecentLandings    = "recent_landings"
)

// valueIndex finds the values of one metric within one scope.
type valueIndex map[string]map[domain.Scope][]domain.MetricValue

func newValueIndex(values []domain.MetricValue) valueIndex {
	ix := valueIndex{}
	for _, v := range values {
		id := v.Metric.String()
		if ix[id] == nil {
			ix[id] = map[domain.Scope][]domain.MetricValue{}
		}
		ix[id][v.Scope] = append(ix[id][v.Scope], v)
	}
	for _, byScope := range ix {
		for _, vs := range byScope {
			slices.SortFunc(vs, compareValues)
		}
	}
	return ix
}

// has reports whether the metric has any value at all.
func (ix valueIndex) has(metric string) bool { return len(ix[metric]) > 0 }

// people returns the metric's values about people in the scope, one per
// person: the earliest window wins when a metric gives a person several.
func (ix valueIndex) people(metric string, scope domain.Scope) []domain.MetricValue {
	var out []domain.MetricValue
	seen := map[string]bool{}
	for _, v := range ix[metric][scope] {
		if v.Subject.Kind == domain.SubjectPerson && !seen[v.Subject.Key] {
			seen[v.Subject.Key] = true
			out = append(out, v)
		}
	}
	return out
}

// person returns the metric's value about one person in the scope.
func (ix valueIndex) person(metric string, scope domain.Scope, email string) (domain.MetricValue, bool) {
	for _, v := range ix[metric][scope] {
		if v.Subject.Kind == domain.SubjectPerson && v.Subject.Key == email {
			return v, true
		}
	}
	return domain.MetricValue{}, false
}

// aggregate returns the metric's value about the scope as a whole. In a
// repository scope that is the value whose subject is the repository, else
// the one about its root area (the repository key, a colon and an empty
// prefix). In a group or all scope it is the one value about neither a
// person, a change nor an area inside a repository. It reads both layouts
// because the metrics may give a repository's own number either subject:
// a tolerant reader (Martin Fowler, "Tolerant Reader").
func (ix valueIndex) aggregate(metric string, scope domain.Scope) (domain.MetricValue, bool) {
	vs := ix[metric][scope]
	if scope.Kind == domain.ScopeRepo {
		for _, kind := range []string{domain.SubjectRepo, domain.SubjectArea} {
			want := scope.Key
			if kind == domain.SubjectArea {
				want += ":"
			}
			for _, v := range vs {
				if v.Subject.Kind == kind && v.Subject.Key == want {
					return v, true
				}
			}
		}
		return domain.MetricValue{}, false
	}
	var found []domain.MetricValue
	for _, v := range vs {
		switch v.Subject.Kind {
		case domain.SubjectPerson, domain.SubjectChange:
		case domain.SubjectArea:
			if v.Subject.Key == "" {
				found = append(found, v)
			}
		default:
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		return domain.MetricValue{}, false
	}
	return found[0], true
}

// majorityFiles reads a person's majority files from surviving@1's detail,
// else from majority_owner@1's value.
func (ix valueIndex) majorityFiles(v domain.MetricValue) (int, bool) {
	if n, ok := v.Detail[detailMajorityFiles]; ok {
		return count(n), false
	}
	if m, ok := ix.person(MetricMajority, v.Scope, v.Subject.Key); ok {
		return count(m.Value), true
	}
	return 0, false
}

// linesByRole reads surviving@1's lines_<role> details.
func linesByRole(detail map[string]float64) map[string]int {
	var out map[string]int
	for k, n := range detail {
		role, ok := strings.CutPrefix(k, detailRolePrefix)
		if !ok || role == "" || role == "added" {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[role] = count(n)
	}
	return out
}

// compareValues orders values by metric, scope, subject and window.
func compareValues(a, b domain.MetricValue) int {
	return cmp.Or(
		cmp.Compare(a.Metric.Name, b.Metric.Name),
		cmp.Compare(a.Metric.Version, b.Metric.Version),
		cmp.Compare(a.Scope.Kind, b.Scope.Kind),
		cmp.Compare(a.Scope.Key, b.Scope.Key),
		cmp.Compare(a.Subject.Kind, b.Subject.Kind),
		cmp.Compare(a.Subject.Key, b.Subject.Key),
		a.Window.Since.Compare(b.Window.Since),
		a.Window.Until.Compare(b.Window.Until),
	)
}

// Values converts metric values to the form a report prints, sorted by
// metric, scope, subject and window, with display names for people. who
// may be nil, in which case a person is named by their email.
func Values(values []domain.MetricValue, who People) []Value {
	var out []Value
	for _, v := range values {
		out = append(out, valueOf(v, who))
	}
	slices.SortFunc(out, func(a, b Value) int {
		return cmp.Or(
			cmp.Compare(a.Metric, b.Metric),
			cmp.Compare(a.Scope.Kind, b.Scope.Kind),
			cmp.Compare(a.Scope.Key, b.Scope.Key),
			cmp.Compare(a.Subject.Kind, b.Subject.Kind),
			cmp.Compare(a.Subject.Key, b.Subject.Key),
			a.Window.Since.Compare(b.Window.Since),
			a.Window.Until.Compare(b.Window.Until),
		)
	})
	return out
}

func valueOf(v domain.MetricValue, who People) Value {
	var detail map[string]float64
	if len(v.Detail) > 0 {
		detail = make(map[string]float64, len(v.Detail))
		for k, n := range v.Detail {
			detail[k] = n
		}
	}
	return Value{
		Metric:  v.Metric.String(),
		Scope:   scopeOf(v.Scope),
		Subject: SubjectOf(v.Subject, who),
		Window:  intervalOf(v.Window),
		Value:   v.Value,
		Num:     v.Num,
		Den:     v.Den,
		N:       v.N,
		Detail:  detail,
	}
}

// SubjectOf converts a subject, naming a person by display name. who may be
// nil, in which case a person is named by their email.
func SubjectOf(s domain.Subject, who People) Subject {
	out := Subject{Kind: s.Kind, Key: s.Key}
	if s.Kind == domain.SubjectPerson {
		out.Name = displayName(who, s.Key)
		out.Bot = isBot(who, s.Key)
	}
	return out
}

func scopeOf(s domain.Scope) Scope { return Scope{Kind: s.Kind, Key: s.Key} }

func intervalOf(w domain.Window) Interval { return Interval{Since: w.Since, Until: w.Until} }

// People is what a report needs to know about the people in it: their
// display names and whether they are bots. *identity.Directory implements
// it; the report depends on these two questions only, not on how identity
// answers them.
type People interface {
	Name(email string) string // the display name for a canonical email
	Bot(email string) bool    // whether the email is a bot's
}

// displayName is the name a person is shown by: the identity directory's
// display name, else their email (a person with no name in the facts, which
// the directory never returns once identity is built).
func displayName(who People, email string) string {
	if who != nil {
		if n := who.Name(email); n != "" {
			return n
		}
	}
	return email
}

func isBot(who People, email string) bool {
	return who != nil && who.Bot(email)
}
