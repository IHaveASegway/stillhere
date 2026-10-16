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
// what a person reads or pastes. A display name that gives an address away
// is withheld in every format, JSON included (name "" and name_withheld),
// and a page labels the people it cannot name by their order on it
// (names.go). An address that repository text holds, a commit subject's or
// a git message's, is withheld in every format too (addresses.go).
package report

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math"
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
// read command (Explain, WhoKnows, Me, Validation, Catalog, Identities).
type Document interface {
	writeText(p *printer)
	writeMarkdown(p *printer)
}

// Text renders d as plain text for a terminal, readable at 100 columns.
// Untrusted text is printed with control characters escaped, and each
// email address in it as "(address withheld)" (addresses.go). A table of
// people prints its first rows and counts the rest unless opts ask for
// every row (AllRows, SR-14).
func Text(w io.Writer, d Document, opts ...Option) error {
	d, err := page(d)
	if err != nil {
		return err
	}
	p := newPrinter(w, opts)
	d.writeText(p)
	if p.err != nil {
		return fmt.Errorf("report: write text: %w", p.err)
	}
	return nil
}

// Markdown renders d as GitHub-flavoured Markdown, with untrusted text
// escaped so it renders literally, and email addresses withheld as Text
// withholds them.
func Markdown(w io.Writer, d Document, opts ...Option) error {
	d, err := page(d)
	if err != nil {
		return err
	}
	p := newPrinter(w, opts)
	d.writeMarkdown(p)
	if p.err != nil {
		return fmt.Errorf("report: write Markdown: %w", p.err)
	}
	return nil
}

// JSON renders d as indented JSON in a stable order: fields in declaration
// order, map keys sorted, an empty list as [] and an empty map as {}, and an
// absent section as null.
//
// Two inputs that encoding/json/v2 rejects by default are mapped instead,
// so that one odd commit cannot cost a whole report. git does not guarantee
// UTF-8: a commit written without an encoding header (cvs2git, early
// git-svn, JGit) keeps its Latin-1 bytes, and --encoding=UTF-8 leaves them
// as they are. Invalid UTF-8 becomes U+FFFD, as encoding/json v1 did. A
// number that is not finite (NaN, an infinity) becomes null. An email
// address appears only in the fields the shape gives a person's email
// (email, a person subject's key): anywhere else it is withheld, as text
// and Markdown withhold it (addresses.go). The document is encoded in
// memory and written only once it is whole, so a failure never leaves half
// a document on w.
func JSON(w io.Writer, d Document) error {
	indent := jsontext.WithIndent("  ")
	doc, err := encodeDocument(d, indent)
	if err != nil {
		return fmt.Errorf("report: encode JSON: %w", err)
	}
	if doc, _, err = withholdAddresses(doc, knownOf(d), indent); err != nil {
		return fmt.Errorf("report: withhold addresses: %w", err)
	}
	buf := bytes.NewBuffer(doc)
	buf.WriteByte('\n')
	if _, err := buf.WriteTo(w); err != nil {
		return fmt.Errorf("report: write JSON: %w", err)
	}
	return nil
}

// finiteOrNull encodes a NaN or an infinity as null and leaves every other
// float64 to the default encoder.
func finiteOrNull(enc *jsontext.Encoder, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return enc.WriteToken(jsontext.Null)
	}
	return errors.ErrUnsupported
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
	Window    Window      `json:"window"`    // what "landed" means here: the window landed@2 (and landed@1) read
	History   Interval    `json:"history"`   // the landing window the history covers (365 days for a scan)
	Metrics   []string    `json:"metrics"`   // every metric@version the report's numbers come from, sorted
	Groups    []Group     `json:"groups"`    // sorted by name
	Warnings  []Warning   `json:"warnings"`  // run-level warnings, such as excluded-repo; repository warnings are on the repository
	Caveats   []Caveat    `json:"caveats"`   // what could make this report wrong, across the run
	Overviews []Overview  `json:"overviews"` // the platform view, read first: every repository (with --scope all, or without groups), then one per group
	Ranks     []RankTable `json:"ranks"`     // only for metrics named by --rank
	Repos     []Repo      `json:"repos"`     // sorted by group, name and key
	Timings   *Timings    `json:"timings"`   // wall-clock measurements: the only part two identical runs do not share

	addrs []string // the run's addresses, which every format withholds in repository text (#90); never encoded
}

func (m *Model) runAddresses() []string { return m.addrs }

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

// Window is the landed window, which landed@2 and landed@1 read: what
// "landed" means in a report.
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
	Code    string `json:"code"` // duplicate-repo, shallow-clone, shallow-window, partial-clone, ignore-revs, excluded-repo, path-failed
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
	// Forge is the GitHub data the run read for the repositories in Heads,
	// which dates every number computed from the forge replica (ADR 0004);
	// null when they had none, and in a week report.
	Forge *ForgeProvenance `json:"forge"`
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

// Overview is the platform view of one scope, a group or every repository:
// what a lead or an agent reads first about it.
type Overview struct {
	Scope         Scope          `json:"scope"`
	Provenance    Provenance     `json:"provenance"`
	Totals        Totals         `json:"totals"`
	People        *StillHere     `json:"people"`         // survival, depth, breadth and activity per person; nil when no repository has line facts
	Week          *WeekTable     `json:"week"`           // what landed in the window, summed per person
	Concentration *Concentration `json:"concentration"`  // the walk re-run on the group's summed majority files; nil for every repository
	KnowledgeLoss *Share         `json:"knowledge_loss"` // the share of human-held prod lines whose people have gone quiet
	AIAssisted    *Share         `json:"ai_assisted"`    // the share of landings tagged AI-assisted: a lower bound
	BotShare      *Share         `json:"bot_share"`      // surviving@1: the share of surviving prod lines held by bots
	// ReviewCoverage is how much of what landed in the scope someone other
	// than its author reviewed (#107); null without GitHub data.
	ReviewCoverage *ReviewCoverage `json:"review_coverage"`
	// Repos is one row per member repository (#105), the scope's
	// repositories at a glance, in report order.
	Repos   []RepoRow `json:"repos"`
	Caveats []Caveat  `json:"caveats"`
}

// RepoRow is one repository of an overview on one line (#105): its card's
// numbers, so a platform of many repositories reads as one table.
type RepoRow struct {
	Name            string          `json:"name"`
	Key             string          `json:"key"`
	Status          string          `json:"status"`           // ok or error
	LandingsWindow  int             `json:"landings_window"`  // landings in the landed window
	LandingsHistory int             `json:"landings_history"` // landings in the history window
	People          *int            `json:"people"`           // the card's: humans with surviving prod lines
	ProdLines       *int            `json:"prod_lines"`       // the card's size.prod_lines
	KnowledgeLoss   *Share          `json:"knowledge_loss"`
	AuthorsInPrefix *int            `json:"authors_in_prefix"` // the card's concentration
	ReviewCoverage  *ReviewCoverage `json:"review_coverage"`
	LastLanding     *time.Time      `json:"last_landing"` // the card's landings.last
}

// ReviewCoverage is review_coverage@1 about a repository, a group or the
// whole run (#107): the share of its human-authored landings in the
// window whose pull request someone other than its author reviewed before
// it merged. It describes how a codebase's changes land, never a person.
type ReviewCoverage struct {
	Metric string   `json:"metric"`
	Window Interval `json:"window"`
	Num    float64  `json:"num"` // reviewed landings
	Den    float64  `json:"den"` // human-authored landings
	Value  float64  `json:"value"`
	// ViaPullRequest counts the landings with a pull request; Den less it
	// is Direct, landings with no pull request in the GitHub data the run
	// read, which no review can cover: pushed directly, unless the data
	// lacks their pull request.
	ViaPullRequest int `json:"via_pull_request"`
	Direct         int `json:"direct"`
	// AfterSync and Unmatched count the human landings with no pull
	// request that Den leaves out, since whether one had a review is
	// unknown: those at or after the GitHub data's as-of (a clone fresher
	// than the last sync), and those whose subject names a pull request,
	// as GitHub's merges write it, that the data lacks (#101).
	AfterSync int `json:"after_sync"`
	Unmatched int `json:"unmatched"`
	// ApprovedNoComment counts the reviewed landings whose only reviews
	// were approvals with no text and no inline comment: rubber stamps
	// count as reviews, so they are shown beside the share.
	ApprovedNoComment int `json:"approved_no_comment"`
	BotLandings       int `json:"bot_landings"` // a bot's landings, left out of Den
	Repos             int `json:"repos"`        // the repositories with GitHub data it counts
}

// Totals are the sizes of a scope. The counts of paths and landings are
// facts; the surviving lines, the files that hold them and their people are
// surviving@1's, and null when it has no value for the scope (no
// repository has line facts).
type Totals struct {
	Repos           int  `json:"repos"`
	ReposOK         int  `json:"repos_ok"`
	ReposFailed     int  `json:"repos_failed"`
	ProdPaths       int  `json:"prod_paths"`       // paths with role prod at each HEAD, whether or not they hold a line
	ProdFiles       *int `json:"prod_files"`       // surviving@1: prod files with surviving lines
	ProdLines       *int `json:"prod_lines"`       // surviving@1: surviving prod non-blank lines
	People          *int `json:"people"`           // surviving@1: humans with surviving prod lines
	Landers         *int `json:"landers"`          // humans with a landing in the landed window; null when what landed there is unknown (week null)
	Bots            int  `json:"bots"`             // bots with surviving prod lines or a landing in the window
	LandingsWindow  int  `json:"landings_window"`  // landings in the landed window
	LandingsHistory int  `json:"landings_history"` // landings in the history window
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
	Email             string         `json:"email"`         // the canonical key; JSON only
	Name              string         `json:"name"`          // the display name, "@login", or "" when there is none a page may show (personName)
	NameWithheld      bool           `json:"name_withheld"` // the name is "" because it gives an address away
	Bot               bool           `json:"bot"`
	ProdNonblankLines int            `json:"prod_nonblank_lines"` // the headline: survival
	MajorityFiles     int            `json:"majority_files"`      // depth: files where the person holds at least half the lines
	LinesByRole       map[string]int `json:"lines_by_role"`       // every role, prod included
	Repos             int            `json:"repos"`               // breadth: repositories with surviving prod lines
	Landings365d      int            `json:"landings_365d"`       // activity: landings credited in the last 365 days
	// LastActive is the person's last activity in the run by
	// knowledge_loss@1's rule (a landing, a review, a pull request opened or
	// pushed to, a commit on another ref) and LastActivity what it was
	// (domain.Activity's kinds), "none" with a null time when none is in the
	// 365-day history, "" when the reader cannot tell. QuietInDays: whole days left
	// before 180 days have passed since it, when knowledge_loss@1 starts to
	// count the person as gone quiet; null once they have. GoingQuiet:
	// within 30 days of it.
	LastActive   *time.Time `json:"last_active"`
	LastActivity string     `json:"last_activity"`
	QuietInDays  *int       `json:"quiet_in_days"`
	GoingQuiet   bool       `json:"going_quiet"`
}

// WeekTable is what landed in the window: landed@2, who wrote what landed
// (#108). A landing counts for each person credited with one of its
// non-merge commits, so a merged branch counts for its authors and a bot's
// commits for the bot, whoever merged them. The numbers are activity and
// are not comparable to surviving lines.
//
// A scope whose history does not cover the window, a shallow clone's
// boundary inside it, has no table: its week is null, unknown, where a
// covered window in which nothing landed has a table whose total is 0
// (#74: unknown never reads as 0).
type WeekTable struct {
	// Total is the scope's own numbers, each landing once, which the rows
	// do not add up to: a landing that holds two people's commits is a
	// row of each. Over the scope's repositories whose history covers the
	// window; a table always has one.
	Total  *WeekTotal `json:"total"`
	Humans []WeekRow  `json:"humans"` // by the window's sort key, descending, then email
	Bots   []WeekRow  `json:"bots"`
}

// WeekRow is one person's part of what landed in the window.
type WeekRow struct {
	Email        string `json:"email"`
	Name         string `json:"name"`          // as StillHereRow's
	NameWithheld bool   `json:"name_withheld"` // as StillHereRow's
	Bot          bool   `json:"bot"`
	Landings     int    `json:"landings"` // landings that hold a non-merge commit credited to the person
	Commits      int    `json:"commits"`  // those commits
	Merged       int    `json:"merged"`   // merge landings whose landing commit is credited to the person
	// LinesAdded is their commits' own numstat insertions, blank lines
	// included, on paths with a role; null when no landing's lines are
	// known (a partial clone's), never 0 (#74).
	LinesAdded     *int `json:"lines_added"`
	ProdLinesAdded *int `json:"prod_lines_added"` // the prod subset; null with LinesAdded
	// LinesUnknown counts the landings whose lines are unknown, which the
	// two lines fields leave out.
	LinesUnknown int `json:"landings_lines_unknown"`
}

// WeekTotal is what landed in a scope in the window, each landing once:
// landed@2's value about the repository, the group or the whole run.
type WeekTotal struct {
	Landings       int  `json:"landings"`
	Commits        int  `json:"commits"` // the non-merge commits they hold
	Merged         int  `json:"merged"`  // the merge landings among them
	LinesAdded     *int `json:"lines_added"`
	ProdLinesAdded *int `json:"prod_lines_added"`
	LinesUnknown   int  `json:"landings_lines_unknown"`
}

// Concentration is knowledge_concentration@1: how few authors hold the
// majority of half the files that have a majority owner.
type Concentration struct {
	Metric            string `json:"metric"`
	FilesWithMajority int    `json:"files_with_majority"` // each file once, even with two majority owners
	AuthorsInPrefix   int    `json:"authors_in_prefix"`
	BotsInPrefix      int    `json:"bots_in_prefix"`
	Label             string `json:"label"` // always ConcentrationLabel
	// IfMerged (#104): the value with the run's identity candidates merged,
	// when candidates hold lines in the scope; null otherwise, and in a run
	// stored before stillhere computed it.
	IfMerged *ConcentrationIfMerged `json:"if_merged"`
}

// ConcentrationIfMerged is knowledge_concentration@1 with the run's
// identity candidates merged: Detail identity_candidates,
// identity_candidates_high, if_merged and if_all_merged.
type ConcentrationIfMerged struct {
	Candidates  int `json:"candidates"`   // identity candidates with an address among the people holding lines in the scope
	High        int `json:"high"`         // of them, those with high-confidence lines (a high candidate, or a weaker one's high set), which the mailmap form writes uncommented
	HighAuthors int `json:"high_authors"` // authors in the prefix with the high-confidence lines merged
	AllAuthors  int `json:"all_authors"`  // with every candidate merged
}

// Area is one top-level directory of a repository with the knowledge
// numbers a scan stores about it, subject area (#106): where in a large
// repository the knowledge has gone quiet or rests with few people.
type Area struct {
	Path string `json:"path"` // the directory, with its trailing slash
	// Lines is knowledge_loss@1's Den: the surviving prod lines people hold
	// there; 0 without its value (only bots hold lines there).
	Lines          int            `json:"lines"`
	KnowledgeLoss  *Share         `json:"knowledge_loss"`  // nil without its value
	People         *int           `json:"people"`          // knowledge_loss@1: humans holding lines there
	PeopleInactive *int           `json:"people_inactive"` // of them, those gone quiet
	Concentration  *Concentration `json:"concentration"`   // nil without its value
	ProdFiles      *int           `json:"prod_files"`      // knowledge_concentration@1: prod files there
}

// Share is a ratio with its parts, so a reader can see what it is a share
// of.
type Share struct {
	Metric string  `json:"metric"`
	Num    float64 `json:"num"`
	Den    float64 `json:"den"`
	Value  float64 `json:"value"` // Num / Den
	// IfMerged (#104): knowledge_loss@1's share with the run's identity
	// candidates merged, when candidates hold lines in the scope; null for
	// every other share, otherwise, and in a run stored before stillhere
	// computed it.
	IfMerged *ShareIfMerged `json:"if_merged"`
	// GoingQuiet is knowledge_loss@1's going_quiet detail: the people
	// holding lines in the scope who go quiet within 30 days unless they
	// are active again, their lines, and the share once they have; null
	// when no one is, for every other share, and in a run stored before
	// stillhere computed it.
	GoingQuiet *ShareGoingQuiet `json:"going_quiet"`
}

// ShareGoingQuiet is knowledge_loss@1's Detail going_quiet_people and
// going_quiet_num over the share's own Den. The overview of every
// repository adds its repositories' up, so a person holding lines in two
// of them counts in People twice; the lines add up exactly.
type ShareGoingQuiet struct {
	People int     `json:"people"` // people holding lines here whose last activity is 150 to 180 days old
	Lines  float64 `json:"lines"`  // the lines they hold here
	Num    float64 `json:"num"`    // Num once they have gone quiet: Num + Lines
	Value  float64 `json:"value"`  // Num / Den
	// Holders names those people, the soonest to go quiet first, then the
	// most lines, then email: the section's still-here rows whose last
	// activity is within 30 days of the line, so a page says who, not only
	// how many (round 2: the people going quiet hold few lines, so a table's
	// first rows never showed them). null for a share no section's rows
	// back, an area's or a who-knows path's, and when the page cannot tell
	// anyone's activity (a pointer, as IdentityCandidate.Mailmap is: JSON
	// writes a nil list as [], which would read as "no one").
	Holders *[]QuietHolder `json:"holders"`
}

// QuietHolder is one person of ShareGoingQuiet.Holders: who, the surviving
// prod lines they hold in the section's scope, and when they were last
// active, by knowledge_loss@1's rule (metrics.LastActivity).
type QuietHolder struct {
	Email        string    `json:"email"`
	Name         string    `json:"name"`          // as StillHereRow's
	NameWithheld bool      `json:"name_withheld"` // as StillHereRow's
	Lines        int       `json:"lines"`
	LastActive   time.Time `json:"last_active"`
	LastActivity string    `json:"last_activity"`
	QuietInDays  int       `json:"quiet_in_days"` // whole days left before 180 days have passed since LastActive
}

// ShareIfMerged is knowledge_loss@1 with the run's identity candidates
// merged: Detail identity_candidates, identity_candidates_high,
// if_merged_num and if_all_merged_num, over the share's own Den, which
// merging people leaves as it is. The overview of every repository adds
// its repositories' up, so a candidate holding lines in two of them counts
// in Candidates and High twice; the lines add up exactly.
type ShareIfMerged struct {
	Candidates int     `json:"candidates"` // identity candidates with an address among the people holding lines in the scope
	High       int     `json:"high"`       // of them, those with high-confidence lines (a high candidate, or a weaker one's high set), which the mailmap form writes uncommented
	HighNum    float64 `json:"high_num"`   // Num with the high-confidence lines merged
	HighValue  float64 `json:"high_value"` // HighNum / Den
	AllNum     float64 `json:"all_num"`    // Num with every candidate merged: the most an unmerged identity can move it
	AllValue   float64 `json:"all_value"`  // AllNum / Den
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
	People        *int           `json:"people"`  // surviving@1: humans with surviving prod lines; nil without its value
	Bots          *int           `json:"bots"`    // bots with surviving prod lines; nil without line facts
	Concentration *Concentration `json:"concentration"`
	KnowledgeLoss *Share         `json:"knowledge_loss"`
	AIAssisted    *Share         `json:"ai_assisted"`
	BotShare      *Share         `json:"bot_share"`
	// ReviewCoverage is how much of what landed someone other than its
	// author reviewed (#107); null without GitHub data for the repository.
	ReviewCoverage *ReviewCoverage `json:"review_coverage"`
	// Areas are the repository's top-level directories with the knowledge
	// numbers the scan stores about each, the largest first (Areas, #105).
	Areas    []Area   `json:"areas"`
	Landings Landings `json:"landings"`
	Caveats  []Caveat `json:"caveats"`
}

// Age is how old a repository is.
type Age struct {
	FirstCommit time.Time `json:"first_commit"` // author time of the earliest root commit
	Days        int       `json:"days"`         // whole days from FirstCommit to the as-of instant
}

// Size is how big a repository is at HEAD. Files and ProdPaths count the
// classified paths; ProdFiles and ProdLines are surviving@1's, nil when it
// has no value for the repository (no line facts).
type Size struct {
	Files     int  `json:"files"`      // paths at HEAD
	ProdPaths int  `json:"prod_paths"` // paths at HEAD with role prod, whether or not they hold a line
	ProdFiles *int `json:"prod_files"` // surviving@1: prod files with surviving lines
	ProdLines *int `json:"prod_lines"` // surviving@1: surviving prod non-blank lines
}

// Author is one of the people a repository is made of.
type Author struct {
	Email        string  `json:"email"`
	Name         string  `json:"name"`          // as StillHereRow's
	NameWithheld bool    `json:"name_withheld"` // as StillHereRow's
	ProdLines    int     `json:"prod_lines"`
	Share        float64 `json:"share"` // of the repository's surviving prod lines
	// The person's last activity, as their still-here row's.
	LastActive   *time.Time `json:"last_active"`
	LastActivity string     `json:"last_activity"`
	QuietInDays  *int       `json:"quiet_in_days"`
	GoingQuiet   bool       `json:"going_quiet"`
}

// Landings counts a repository's landings.
type Landings struct {
	Window  int        `json:"window"`  // in the landed window
	History int        `json:"history"` // in the history window
	Last    *time.Time `json:"last"`    // the committer time of the newest one the history holds; null for none
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
	Metric   string   `json:"metric"`
	Question string   `json:"question"`
	Scope    Scope    `json:"scope"`
	Window   Interval `json:"window"`
	Percent  bool     `json:"percent"` // a ratio: estimates print as percentages
	MinN     int      `json:"min_n"`   // the eligibility threshold: clusters (changes) per person
	// Beside names the Detail key of each person's value that the table
	// shows beside the estimate, a counterweight the metric's definition
	// promises next to it (PF-5: reviews_given@1's no_comment_share); ""
	// for none.
	Beside     string     `json:"beside"`
	Provenance Provenance `json:"provenance"`
	Ranked     []RankRow  `json:"ranked"`    // by rank interval
	BelowMin   []RankRow  `json:"below_min"` // listed, not ranked
	Bots       []RankRow  `json:"bots"`      // never ranked
	Caveats    []Caveat   `json:"caveats"`
}

// RankRow is one person's estimate and, when eligible, rank interval.
type RankRow struct {
	Email        string  `json:"email"`
	Name         string  `json:"name"`          // as StillHereRow's
	NameWithheld bool    `json:"name_withheld"` // as StillHereRow's
	RankLo       int     `json:"rank_lo"`       // 95% interval of the rank; 0 when not ranked
	RankHi       int     `json:"rank_hi"`
	Estimate     float64 `json:"estimate"`
	Lo           float64 `json:"lo"` // 95% interval of the value; for bots, equal to Estimate
	Hi           float64 `json:"hi"`
	N            int     `json:"n"` // clusters: the person's changes the metric counted
	// Beside is the person's value of the table's Beside key; null when
	// the table names none or the value has none.
	Beside *float64 `json:"beside"`
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
