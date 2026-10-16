package report

import (
	"cmp"
	"math"
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
	MetricLanded        = "landed@2"                  // week rows (#108): Value is the landings that hold the person's commits; Detail commits, merged, lines_added, prod_lines_added (absent when unknown), landings_lines_unknown; about the repository, group or all: the scope's totals
	MetricSurviving     = "surviving@1"               // still-here rows: Value is prod non-blank lines; Detail majority_files, lines_<role>, repos, landings_365d; about a repository or group: Value prod lines, Detail prod_files, people, bot_share
	MetricMajority      = "majority_owner@1"          // a person's majority files, when surviving@1 has no majority_files detail
	MetricConcentration = "knowledge_concentration@1" // Value is authors_in_prefix; Detail files_with_majority, bots_in_prefix
	MetricKnowledgeLoss = "knowledge_loss@1"          // Num is lines held by people gone quiet, Den lines held by people
	MetricAIAssisted    = "ai_assisted@1"             // Num is AI-assisted landings, Den landings in 365 days
	MetricWhoKnows      = "who_knows@1"               // per path: one value about the path (Detail prod_files, files_with_majority) and one per person (Value prod lines; Detail majority_files, recent_landings, top_author)
	MetricReviews       = "reviews_given@1"           // rank rows (PF-5): Detail no_comment_share, shown beside the count
	// MetricReviewCoverage (#107) is about a repository, a group or the
	// whole run, never a person: Num the reviewed landings, Den the human
	// ones; Detail via_pull_request, direct, approved_no_comment,
	// bot_landings, repos.
	MetricReviewCoverage = "review_coverage@1"
)

// maxTopAuthors is how many top authors a who-knows answer lists
// (docs/metrics.md#who_knows1-per-path).
const maxTopAuthors = 5

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
	detailTopAuthor         = "top_author" // a top author's place, 1 to 5
	detailProdFiles         = "prod_files"
	detailPeople            = "people"    // surviving@1 about a repository or group: humans with surviving prod lines; knowledge_loss@1: humans holding lines
	detailBotShare          = "bot_share" // surviving@1 about a repository or group: bot-held lines over prod lines
	// knowledge_loss@1 and knowledge_concentration@1 (#104), present when
	// identity candidates hold lines in the area: how many, the high ones,
	// and the value with them merged (the high ones, every one).
	detailCandidates     = "identity_candidates"
	detailCandidatesHigh = "identity_candidates_high"
	detailIfMergedNum    = "if_merged_num"     // knowledge_loss@1's Num
	detailIfAllMergedNum = "if_all_merged_num" // knowledge_loss@1's Num
	detailIfMerged       = "if_merged"         // knowledge_concentration@1's authors_in_prefix
	detailIfAllMerged    = "if_all_merged"     // knowledge_concentration@1's authors_in_prefix
	detailPeopleInactive = "people_inactive"
)

// shareIfMerged reads knowledge_loss@1's identity detail (#104): nil when
// no candidate holds lines in the value's area, or the run predates it.
func shareIfMerged(v domain.MetricValue) *ShareIfMerged {
	n, ok := v.Detail[detailCandidates]
	if !ok || n == 0 {
		return nil
	}
	m := &ShareIfMerged{Candidates: count(n), High: count(v.Detail[detailCandidatesHigh]),
		HighNum: v.Detail[detailIfMergedNum], AllNum: v.Detail[detailIfAllMergedNum]}
	if v.Den > 0 {
		m.HighValue, m.AllValue = m.HighNum/v.Den, m.AllNum/v.Den
	}
	return m
}

// addIfMerged adds a repository's identity detail into a sum over several
// (the overview of every repository); a nil part adds its Num to both
// sums, since with no candidate in it, merging moves none of its lines.
func addIfMerged(sum *ShareIfMerged, part *ShareIfMerged, num float64) *ShareIfMerged {
	if sum == nil {
		sum = &ShareIfMerged{}
	}
	if part == nil {
		sum.HighNum += num
		sum.AllNum += num
		return sum
	}
	sum.Candidates += part.Candidates
	sum.High += part.High
	sum.HighNum += part.HighNum
	sum.AllNum += part.AllNum
	return sum
}

// concentrationIfMerged reads knowledge_concentration@1's identity detail
// (#104): nil when no candidate holds lines in the value's area, or the run
// predates it.
func concentrationIfMerged(v domain.MetricValue) *ConcentrationIfMerged {
	n, ok := v.Detail[detailCandidates]
	if !ok || n == 0 {
		return nil
	}
	return &ConcentrationIfMerged{Candidates: count(n), High: count(v.Detail[detailCandidatesHigh]),
		HighAuthors: count(v.Detail[detailIfMerged]), AllAuthors: count(v.Detail[detailIfAllMerged])}
}

// The Detail keys of landed@2 and review_coverage@1, and the one a
// reviews_given@1 rank row shows beside the count.
const (
	detailCommits           = "commits"
	detailMerged            = "merged"
	detailLinesUnknown      = "landings_lines_unknown"
	detailViaPullRequest    = "via_pull_request"
	detailDirect            = "direct"
	detailApprovedNoComment = "approved_no_comment"
	detailBotLandings       = "bot_landings"
	detailAfterSync         = "after_sync"
	detailUnmatched         = "unmatched"
	detailNoCommentShare    = "no_comment_share"
)

// rankBeside names, per ranked metric, the Detail key of a person's value
// that a rank table prints beside the estimate: a counterweight the metric's
// definition promises next to it (PF-5: reviews_given@1's share of reviews
// that were approvals with no comment). A metric not listed shows none.
var rankBeside = map[string]string{MetricReviews: detailNoCommentShare}

// forgeAnywhere holds the metrics that read GitHub data and count a review
// in any repository of the run, beyond a section's own, as a person's
// activity: knowledge_loss@1 (docs/metrics.md). A card or an overview that
// shows one of them also says where the run's other repositories lack
// GitHub data (forgeElsewhere); review_coverage@1 counts its own scope's
// pull requests only.
var forgeAnywhere = map[string]bool{MetricKnowledgeLoss: true}

// population is surviving@1's value about a repository's or a group's code
// as a whole. A field is nil when the value does not carry it.
type population struct {
	prodLines *int   // Value: surviving prod non-blank lines
	prodFiles *int   // Detail prod_files: prod files with surviving lines
	people    *int   // Detail people
	botShare  *Share // Detail bot_share, with the lines it is a share of
}

// populationOf reads a surviving@1 value about a repository or a group.
// The bots' lines are bot_share times the prod lines, rounded: the metric
// divided two counts, so the product is that count again.
func populationOf(v domain.MetricValue) population {
	lines := count(v.Value)
	p := population{prodLines: &lines}
	if n, ok := v.Detail[detailProdFiles]; ok {
		files := count(n)
		p.prodFiles = &files
	}
	if n, ok := v.Detail[detailPeople]; ok {
		people := count(n)
		p.people = &people
	}
	if share, ok := v.Detail[detailBotShare]; ok && lines > 0 && !math.IsNaN(share) {
		p.botShare = &Share{Metric: MetricSurviving, Num: float64(count(share * float64(lines))), Den: float64(lines), Value: share}
	}
	return p
}

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
// may be nil, in which case no person has a name (personName).
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

// SubjectOf converts a subject, naming a person as every page does
// (personName): Name is "" when the person has no name a page may show,
// and NameWithheld says when that is because it gives an address away.
// who may be nil, in which case no person has a name.
func SubjectOf(s domain.Subject, who People) Subject {
	out := Subject{Kind: s.Kind, Key: s.Key}
	if s.Kind == domain.SubjectPerson {
		out.Name, out.NameWithheld = personName(who, s.Key)
		out.Bot = isBot(who, s.Key)
	}
	return out
}

func scopeOf(s domain.Scope) Scope { return Scope{Kind: s.Kind, Key: s.Key} }

func intervalOf(w domain.Window) Interval { return Interval{Since: w.Since, Until: w.Until} }

// People is what a report needs to know about the people in it: their
// display names, whether they are bots, whether a name gives an address
// away, and which names are one name to the identities rules.
// *identity.Directory implements it; the report depends on these four
// questions only, not on how identity answers them.
type People interface {
	// Name is the display name for a canonical email, or for a GitHub
	// login's key ("@zoe") the login as GitHub shows it ("@Zoe").
	Name(email string) string
	Bot(email string) bool // whether the email is a bot's
	// Withheld reports whether the email's display name gives away an
	// address of the run, so that no page shows it (personName).
	Withheld(email string) bool
	// NameKey is the key the identities same-name rule compares for the
	// email's display name, "" when it compares none
	// (identity.Directory.NameKey): every caveat that says two emails share
	// a name groups them by it, so it names what identities compares.
	NameKey(email string) string
	// Addresses is every address of the run, logins left out
	// (identity.Directory.Addresses): what a page withholds wherever
	// repository text holds one, whether or not the person is on the page
	// (#90).
	Addresses() []string
}

// runAddresses is who's Addresses, nil for a nil who.
func runAddresses(who People) []string {
	if who == nil {
		return nil
	}
	return who.Addresses()
}

// OwnPageOnly reports whether a metric's values about a person are shown on
// that person's own page only (stillhere me), and never in a report, a rank
// table or an explanation that someone else may read: a person's own
// AI-assisted share (ADR 0007). The MCP server's person tool (#15) follows
// the same rule.
func OwnPageOnly(metric domain.MetricID) bool { return metric.Name == "ai_assisted" }

// detailAIAssistedShare is surviving's copy, in its value about a
// repository or a group, of that scope's AI-assisted share: ai_assisted@1's
// Num over Den (docs/metrics.md#surviving1). No page reads it from there.
const detailAIAssistedShare = "ai_assisted_share"

// AIShareIn says where a value holds an AI-assisted share. whole is true
// when the value is one: ai_assisted's own values (OwnPageOnly), a
// repository's or a group's share, a person's own share, or a change's tag.
// key names the Detail key in which another metric's value copies its
// scope's share, "" for none: surviving's population makeup carries it as
// ai_assisted_share. This is the one place that says where the shares are,
// so a reader that must withhold one (the MCP server, by the run's decision
// app.AIShares) withholds every copy of it; a metric that copies the share
// under another key must be added here. The key is named whether or not
// the value's Detail holds it. A whole share's own Detail (ai_assisted@1's
// cohort comparisons, docs/metrics.md#ai_assisted1) is no copy of a share,
// but it can be one or two people's, so the MCP server sends a share it
// shows without any of it (#87).
func AIShareIn(v Value) (whole bool, key string) {
	id, err := domain.ParseMetricID(v.Metric)
	switch {
	case err != nil:
		return false, ""
	case OwnPageOnly(id):
		return true, ""
	case id.Name == "surviving":
		return false, detailAIAssistedShare
	}
	return false, ""
}

// Areas reads the area values of one repository (subject area, key
// "<repo key>:<dir>/") of knowledge_loss@1 and knowledge_concentration@1,
// the metrics a scan stores per top-level directory, as one row per area:
// the largest first, by the lines people hold there, then by the lines
// held by people gone quiet, then by path (#106). It is the one mapping of
// those values for any page that lists areas; nil for none.
func Areas(values []domain.MetricValue, repo string) []Area {
	byPath := map[string]*Area{}
	row := func(v domain.MetricValue) *Area {
		path, ok := strings.CutPrefix(v.Subject.Key, repo+":")
		if v.Subject.Kind != domain.SubjectArea || v.Scope.Kind != domain.ScopeRepo || v.Scope.Key != repo || !ok {
			return nil
		}
		a := byPath[path]
		if a == nil {
			a = &Area{Path: path}
			byPath[path] = a
		}
		return a
	}
	for _, v := range values {
		switch v.Metric.String() {
		case MetricKnowledgeLoss:
			if a := row(v); a != nil {
				a.Lines = count(v.Den)
				a.KnowledgeLoss = &Share{Metric: MetricKnowledgeLoss, Num: v.Num, Den: v.Den, Value: v.Value}
				people, inactive := count(v.Detail[detailPeople]), count(v.Detail[detailPeopleInactive])
				a.People, a.PeopleInactive = &people, &inactive
			}
		case MetricConcentration:
			if a := row(v); a != nil {
				a.Concentration = concentrationOf(v)
				files := count(v.Detail[detailProdFiles])
				a.ProdFiles = &files
			}
		}
	}
	var out []Area
	for _, a := range byPath {
		out = append(out, *a)
	}
	gone := func(a Area) float64 {
		if a.KnowledgeLoss == nil {
			return 0
		}
		return a.KnowledgeLoss.Num
	}
	slices.SortFunc(out, func(x, y Area) int {
		return cmp.Or(cmp.Compare(y.Lines, x.Lines), cmp.Compare(gone(y), gone(x)), cmp.Compare(x.Path, y.Path))
	})
	return out
}

func isBot(who People, email string) bool {
	return who != nil && who.Bot(email)
}
