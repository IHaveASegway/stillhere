// Package report is stillhere's presentation model: the report a week run
// or a stored scan run renders as (stillhere.report/v1), the answers of the
// read commands, and the text, Markdown and JSON renderers for all of them
// (docs/spec/report-json.md). The JSON form is the published language for
// tools that do not read facts.db, and the MCP server (#15) answers from the
// same Go types.
//
// It is part of the functional core, and it is a view: Build arranges facts
// and metric values into a Model, and never computes a metric itself. Every
// number about a person comes from a metric value, and each section cites
// the metric versions it read in its provenance, so the definition of a
// number lives in internal/metrics only (ADR 0003). Renderers write to an
// io.Writer and open no files. Every slice is sorted when it is built, so two
// runs on the same inputs render byte-identical output once timings are
// removed.
//
// The types carry their own JSON field names, so the published shape does
// not move when an internal type does. Text and Markdown name people by
// display name only: emails appear in JSON, the machine output, and never in
// what a person reads or pastes.
package report

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"time"
)

// Schema is the version tag every report carries.
const Schema = "stillhere.report/v1"

// The kinds of report a Model holds.
const (
	KindWeek = "week" // what landed in a window; nothing stored
	KindScan = "scan" // a run stored in facts.db: history, blame, metrics and ranks
)

// ConcentrationLabel is printed with every majority-owner concentration: it
// is a prefix walk over majority files, not Avelino's truck factor
// (docs/metrics.md#knowledge_concentration1-per-area).
const ConcentrationLabel = "not a truck factor"

// Document is what the renderers print: a report Model, or the answer of a
// read command (Explain, WhoKnows, Me, Validation, Catalog).
type Document interface {
	writeText(p *printer)
	writeMarkdown(p *printer)
}

// Text renders d as plain text for a terminal, readable at 100 columns.
// Untrusted text is printed with control characters escaped.
func Text(w io.Writer, d Document) error {
	p := &printer{w: w}
	d.writeText(p)
	if p.err != nil {
		return fmt.Errorf("report: write text: %w", p.err)
	}
	return nil
}

// Markdown renders d as GitHub-flavoured Markdown, with untrusted text
// escaped so it renders literally.
func Markdown(w io.Writer, d Document) error {
	p := &printer{w: w}
	d.writeMarkdown(p)
	if p.err != nil {
		return fmt.Errorf("report: write Markdown: %w", p.err)
	}
	return nil
}

// JSON renders d as indented JSON in a stable order: fields in declaration
// order, map keys sorted, an empty list as [] and an empty map as {}, and an
// absent section as null.
func JSON(w io.Writer, d Document) error {
	err := json.MarshalWrite(w, d, json.Deterministic(true), jsontext.WithIndent("  "))
	if err == nil {
		_, err = io.WriteString(w, "\n")
	}
	if err != nil {
		return fmt.Errorf("report: write JSON: %w", err)
	}
	return nil
}

// Model is one stillhere.report/v1 document: a week run, or a scan run as
// stored in facts.db. A section the run did not compute is nil and renders
// as null; an empty list renders as [].
type Model struct {
	Schema    string      `json:"schema"`    // always Schema
	Kind      string      `json:"kind"`      // KindWeek or KindScan
	Tool      Tool        `json:"tool"`      // the binary that built the report
	Run       *Run        `json:"run"`       // the stored run; nil for week, and for a scan that stored nothing
	Now       time.Time   `json:"now"`       // the as-of instant: --now
	Policy    Policy      `json:"policy"`    // the policy the facts were collected under
	Window    Window      `json:"window"`    // what "landed" means here: landed@1's window
	History   Interval    `json:"history"`   // the landing window the history covers (365 days for a scan)
	Metrics   []string    `json:"metrics"`   // every metric@version the report's numbers come from, sorted
	Groups    []Group     `json:"groups"`    // sorted by name
	Warnings  []Warning   `json:"warnings"`  // run-level warnings, such as excluded-repo; repository warnings are on the repository
	Caveats   []Caveat    `json:"caveats"`   // what could make this report wrong, across the run
	Overviews []Overview  `json:"overviews"` // one per group: the platform view, read first
	Ranks     []RankTable `json:"ranks"`     // only for metrics named by --rank
	Repos     []Repo      `json:"repos"`     // sorted by group, name and key
	Timings   *Timings    `json:"timings"`   // wall-clock measurements: the only part two identical runs do not share
}

// Tool names the binary that built a report.
type Tool struct {
	Name    string `json:"name"`    // "stillhere"
	Version string `json:"version"` // such as "0.1.0", or "dev"
}

// Run identifies a stored run.
type Run struct {
	ID          int64             `json:"id"`
	Status      string            `json:"status"`       // complete or partial (some repository or path failed)
	ToolVersion string            `json:"tool_version"` // the version that ran the scan
	Options     map[string]string `json:"options"`      // the effective options the run recorded; never secrets
}

// Policy identifies the policy behind a report's numbers.
type Policy struct {
	Version  int    `json:"version"`  // the policy file's own schema version
	SHA256   string `json:"sha256"`   // what the run recorded: the hash of the TOML, a length prefix and the deny list
	Headline string `json:"headline"` // the [headline] number, such as prod_nonblank_lines
}

// Window is the landed@1 window: what "landed" means in a report.
type Window struct {
	Days      int       `json:"days"`
	DaysFrom  string    `json:"days_from"` // "cli" or "policy"
	Sort      string    `json:"sort"`      // "lines_added" or "commits"
	SortFrom  string    `json:"sort_from"` // "cli" or "policy"
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`      // exclusive, the as-of instant
	DateField string    `json:"date_field"` // always "committer"
	Walk      string    `json:"walk"`       // always "first-parent"
	Diff      string    `json:"diff"`       // always "first-parent"
}

// Interval is a half-open time window, [Since, Until).
type Interval struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// Group is a directory named on the command line whose children are
// repositories.
type Group struct {
	Name  string   `json:"name"`
	Path  string   `json:"path"`
	Repos []string `json:"repos"` // Repo.Key of each member, sorted
}

// Warning is a condition a collector recorded that degraded the run
// without failing it.
type Warning struct {
	Code    string `json:"code"` // duplicate-repo, shallow-clone, shallow-window, partial-clone, numstat-rename, excluded-repo, path-failed
	Message string `json:"message"`
}

// Caveat is one thing that could make a section's numbers wrong, said for
// the person or agent reading them. Caveats come from the run's warnings
// and facts: failed, shallow and partial repositories, files blame could
// not read, identities that may be one person, repositories without a
// .git-blame-ignore-revs, and the stale clone analysis cannot see.
type Caveat struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Repos   []string `json:"repos"` // names of the repositories it concerns, sorted; empty when it concerns the whole section
}

// Provenance says where a section's numbers come from, so the section can
// be read, or handed to an agent, on its own.
type Provenance struct {
	RunID   int64     `json:"run_id"` // 0 for a week report, which stores nothing
	AsOf    time.Time `json:"as_of"`
	Policy  string    `json:"policy"`  // the policy's sha256
	Heads   []Head    `json:"heads"`   // every repository the section covers, sorted by name and key
	Metrics []string  `json:"metrics"` // metric@version of every number in the section, sorted
}

// Head is the commit a repository was read at.
type Head struct {
	Repo   string `json:"repo"` // name
	Key    string `json:"key"`
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

// Scope is the population a section's numbers were computed within.
type Scope struct {
	Kind string `json:"kind"` // repo, group or all
	Key  string `json:"key"`  // Repo.Key, Group.Name, or ""
}

// Overview is the platform view of one group: what a lead or an agent reads
// first about it.
type Overview struct {
	Scope         Scope          `json:"scope"`
	Provenance    Provenance     `json:"provenance"`
	Totals        Totals         `json:"totals"`
	People        *StillHere     `json:"people"`         // survival, depth, breadth and activity per person; nil when no repository has line facts
	Week          *WeekTable     `json:"week"`           // what landed in the window, summed per person
	Concentration *Concentration `json:"concentration"`  // the walk re-run on the group's summed majority files
	KnowledgeLoss *Share         `json:"knowledge_loss"` // the share of human-held prod lines whose people have gone quiet
	AIAssisted    *Share         `json:"ai_assisted"`    // the share of landings tagged AI-assisted: a lower bound
	BotShare      *Share         `json:"bot_share"`      // the share of surviving prod lines held by bots
	Caveats       []Caveat       `json:"caveats"`
}

// Totals are the sizes of a group.
type Totals struct {
	Repos           int `json:"repos"`
	ReposOK         int `json:"repos_ok"`
	ReposFailed     int `json:"repos_failed"`
	ProdFiles       int `json:"prod_files"`       // prod paths at each HEAD
	ProdLines       int `json:"prod_lines"`       // surviving prod non-blank lines; 0 without line facts
	People          int `json:"people"`           // humans with surviving prod lines; 0 without line facts
	Landers         int `json:"landers"`          // humans with a landing in the landed window
	Bots            int `json:"bots"`             // bots with surviving prod lines or a landing in the window
	LandingsWindow  int `json:"landings_window"`  // landings in the landed window
	LandingsHistory int `json:"landings_history"` // landings in the history window
}

// StillHere is whose lines blame still attributes at HEAD: surviving@1.
type StillHere struct {
	FilesBlamed int            `json:"files_blamed"` // files blame read without an error
	Humans      []StillHereRow `json:"humans"`       // by prod lines, descending, then email
	Bots        []StillHereRow `json:"bots"`
}

// StillHereRow is one person's surviving lines in one scope. Repos and
// Landings365d are the breadth and activity a group's overview shows.
type StillHereRow struct {
	Email             string         `json:"email"` // the canonical key; JSON only
	Name              string         `json:"name"`
	Bot               bool           `json:"bot"`
	ProdNonblankLines int            `json:"prod_nonblank_lines"` // the headline: survival
	MajorityFiles     int            `json:"majority_files"`      // depth: files where the person holds at least half the lines
	LinesByRole       map[string]int `json:"lines_by_role"`       // every role, prod included
	Repos             int            `json:"repos"`               // breadth: repositories with surviving prod lines
	Landings365d      int            `json:"landings_365d"`       // activity: landings credited in the last 365 days
}

// WeekTable is what landed in the window: landed@1. The numbers are
// activity and are not comparable to surviving lines.
type WeekTable struct {
	Humans []WeekRow `json:"humans"` // by the window's sort key, descending, then email
	Bots   []WeekRow `json:"bots"`
}

// WeekRow is one person's landings in the window.
type WeekRow struct {
	Email          string `json:"email"`
	Name           string `json:"name"`
	Bot            bool   `json:"bot"`
	Commits        int    `json:"commits"`          // landings credited to the person
	LinesAdded     int    `json:"lines_added"`      // numstat insertions, blank lines included, on paths with a role
	ProdLinesAdded int    `json:"prod_lines_added"` // the prod subset
}

// Concentration is knowledge_concentration@1: how few authors hold the
// majority of half the files that have a majority owner.
type Concentration struct {
	Metric            string `json:"metric"`
	FilesWithMajority int    `json:"files_with_majority"` // each file once, even with two majority owners
	AuthorsInPrefix   int    `json:"authors_in_prefix"`
	BotsInPrefix      int    `json:"bots_in_prefix"`
	Label             string `json:"label"` // always ConcentrationLabel
}

// Share is a ratio with its parts, so a reader can see what it is a share
// of.
type Share struct {
	Metric string  `json:"metric"`
	Num    float64 `json:"num"`
	Den    float64 `json:"den"`
	Value  float64 `json:"value"` // Num / Den
}

// Repo is one repository's part of a report: its card first, then the
// detail behind it.
type Repo struct {
	Name       string     `json:"name"`
	Key        string     `json:"key"`
	Group      string     `json:"group"` // "" when the repository was named directly
	Path       string     `json:"path"`
	Remote     string     `json:"remote_scrubbed"` // "" when there is no origin
	Branch     string     `json:"branch"`
	Head       string     `json:"head"`
	Bare       bool       `json:"bare"`
	Shallow    bool       `json:"shallow"`
	Partial    bool       `json:"partial"`
	Status     string     `json:"status"`     // ok or error
	Error      string     `json:"error"`      // scrubbed; "" when ok
	LineFacts  bool       `json:"line_facts"` // blame ran: false for week runs, shallow and partial clones
	Card       RepoCard   `json:"card"`
	Warnings   []Warning  `json:"warnings"`
	Errors     Errors     `json:"errors"`
	Skipped    Skipped    `json:"skipped"`
	IgnoreRevs IgnoreRevs `json:"blame_ignore_revs"`
	Week       *WeekTable `json:"week"`       // nil when the repository failed
	StillHere  *StillHere `json:"still_here"` // nil without line facts
}

// RepoCard is what a lead or an agent reads first about one repository:
// how old and how big it is, who it is made of, where its knowledge is
// concentrated or has gone quiet, and its AI and bot share.
type RepoCard struct {
	Provenance    Provenance     `json:"provenance"`
	Age           *Age           `json:"age"` // nil when the first commit is unknown, as in a shallow clone
	Size          Size           `json:"size"`
	MadeOf        []Author       `json:"made_of"` // the top five humans by surviving prod lines
	People        int            `json:"people"`  // humans with surviving prod lines
	Bots          int            `json:"bots"`    // bots with surviving prod lines
	Concentration *Concentration `json:"concentration"`
	KnowledgeLoss *Share         `json:"knowledge_loss"`
	AIAssisted    *Share         `json:"ai_assisted"`
	BotShare      *Share         `json:"bot_share"`
	Landings      Landings       `json:"landings"`
	Caveats       []Caveat       `json:"caveats"`
}

// Age is how old a repository is.
type Age struct {
	FirstCommit time.Time `json:"first_commit"` // author time of the earliest root commit
	Days        int       `json:"days"`         // whole days from FirstCommit to the as-of instant
}

// Size is how big a repository is at HEAD.
type Size struct {
	Files     int `json:"files"`      // paths at HEAD
	ProdFiles int `json:"prod_files"` // paths at HEAD with role prod
	ProdLines int `json:"prod_lines"` // surviving prod non-blank lines; 0 without line facts
}

// Author is one of the people a repository is made of.
type Author struct {
	Email     string  `json:"email"`
	Name      string  `json:"name"`
	ProdLines int     `json:"prod_lines"`
	Share     float64 `json:"share"` // of the repository's surviving prod lines
}

// Landings counts a repository's landings.
type Landings struct {
	Window  int `json:"window"`  // in the landed window
	History int `json:"history"` // in the history window
}

// Errors holds each kind of per-file failure in exactly one list.
type Errors struct {
	Blame []FileError `json:"blame"`
}

// FileError is a failure confined to one path.
type FileError struct {
	Path    string `json:"path"`
	Message string `json:"message"` // scrubbed and truncated
}

// Skipped counts the classified paths that add nothing, by reason.
type Skipped struct {
	DeniedPath      int `json:"denied_path"`
	TooBig          int `json:"too_big"`
	GeneratedHeader int `json:"generated_header"`
	Unclassified    int `json:"unclassified"`
}

// IgnoreRevs records the .git-blame-ignore-revs blame used.
type IgnoreRevs struct {
	Source string   `json:"source"` // root, cli, or "" for none
	SHA256 string   `json:"sha256"`
	Revs   []string `json:"revs"` // the commits durable@1 and simplifying@1 leave out, sorted
}

// RankTable is one ranked metric within one scope: rank intervals for
// eligible people, everyone below the minimum sample listed and not ranked,
// and bots apart (ADR 0001).
type RankTable struct {
	Metric     string     `json:"metric"`
	Question   string     `json:"question"`
	Scope      Scope      `json:"scope"`
	Window     Interval   `json:"window"`
	Percent    bool       `json:"percent"` // a ratio: estimates print as percentages
	MinN       int        `json:"min_n"`   // the eligibility threshold: clusters (changes) per person
	Provenance Provenance `json:"provenance"`
	Ranked     []RankRow  `json:"ranked"`    // by rank interval
	BelowMin   []RankRow  `json:"below_min"` // listed, not ranked
	Bots       []RankRow  `json:"bots"`      // never ranked
	Caveats    []Caveat   `json:"caveats"`
}

// RankRow is one person's estimate and, when eligible, rank interval.
type RankRow struct {
	Email    string  `json:"email"`
	Name     string  `json:"name"`
	RankLo   int     `json:"rank_lo"` // 95% interval of the rank; 0 when not ranked
	RankHi   int     `json:"rank_hi"`
	Estimate float64 `json:"estimate"`
	Lo       float64 `json:"lo"` // 95% interval of the value; for bots, equal to Estimate
	Hi       float64 `json:"hi"`
	N        int     `json:"n"` // clusters: the person's changes the metric counted
}

// Timings are wall-clock measurements of the run that built the report. They
// are the only part of a report that differs between two runs on the same
// inputs, which is why they sit in one object a comparison can remove.
type Timings struct {
	Started    time.Time    `json:"started"`
	Finished   time.Time    `json:"finished"`
	TotalMS    int64        `json:"total_ms"`
	DiscoverMS int64        `json:"discover_ms"`
	CollectMS  int64        `json:"collect_ms"` // history, classification and blame, every repository
	MetricsMS  int64        `json:"metrics_ms"`
	RankMS     int64        `json:"rank_ms"`
	StoreMS    int64        `json:"store_ms"`
	LoadMS     int64        `json:"load_ms"` // reading a stored run back
	Repos      []RepoTiming `json:"repos"`
}

// RepoTiming is how long one repository's collectors took.
type RepoTiming struct {
	Repo       string `json:"repo"`
	Key        string `json:"key"`
	HistoryMS  int64  `json:"history_ms"`
	ClassifyMS int64  `json:"classify_ms"`
	BlameMS    int64  `json:"blame_ms"`
}
