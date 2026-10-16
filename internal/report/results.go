package report

import "time"

// The answers of the read commands. They live here, beside Model, so that
// the text, Markdown and JSON renderers and the MCP server (#15) share one
// shape for each answer; internal/app builds them. Each carries its
// provenance and its caveats, so an answer can be read, or handed to an
// agent, on its own.

// Value is one metric value as a report prints it: domain.MetricValue with
// the published metric id and a display name for a person.
type Value struct {
	Metric  string             `json:"metric"` // metric@version
	Scope   Scope              `json:"scope"`
	Subject Subject            `json:"subject"`
	Window  Interval           `json:"window"`
	Value   float64            `json:"value"`
	Num     float64            `json:"num"` // ratio metrics; otherwise equal to Value
	Den     float64            `json:"den"` // ratio metrics; otherwise 1
	N       int                `json:"n"`   // the sample size eligibility reads
	Detail  map[string]float64 `json:"detail"`
}

// Subject is what a value is about.
type Subject struct {
	Kind         string `json:"kind"`          // person, area, repo, group or change (domain.Subject*)
	Key          string `json:"key"`           // a person's canonical email, or "@login" for an unlinked GitHub account; JSON only
	Name         string `json:"name"`          // a person's display name, "@login", or "" when there is none a page may show (personName); "" for other kinds
	NameWithheld bool   `json:"name_withheld"` // a person's name is "" because it gives an address away
	Bot          bool   `json:"bot"`
}

// MetricInfo is a metric's published definition (docs/metrics.md).
type MetricInfo struct {
	ID          string `json:"id"` // metric@version
	Question    string `json:"question"`
	Unit        string `json:"unit"`
	Ranked      bool   `json:"ranked"`
	Forge       bool   `json:"forge"` // reads the GitHub data a run read (metrics.Definition.Forge): dated by its sync
	Ratio       bool   `json:"ratio"` // its values are shares, num of den (metrics.Definition.Ratio): printed as a percent
	Eligibility string `json:"eligibility"`
	BlindSpots  string `json:"blind_spots"`
	Gaming      string `json:"gaming"`
	Oracle      string `json:"oracle"`
	Validity    string `json:"validity"`
}

// Catalog is the metric registry: every metric version with its question,
// unit, eligibility, blind spots, how it can be gamed, its oracle and its
// validity.
type Catalog struct {
	Metrics []MetricInfo `json:"metrics"` // in registry order
}

// Explain is the explain answer: one subject's values for one metric in one
// run, and the changes behind them.
type Explain struct {
	Provenance Provenance `json:"provenance"`
	Metric     MetricInfo `json:"metric"`
	Subject    Subject    `json:"subject"`
	Values     []Value    `json:"values"`  // one per scope and window, sorted
	Changes    []Change   `json:"changes"` // a ranked metric's counted changes for a person; empty otherwise
	// Landings, Commits and Files are what a person's value of a metric
	// that is not ranked is made of (#109): the landings landed@1 and
	// landed@2 count, the blamed commits whose lines surviving@1 sums, the
	// files majority_owner@1 counts; empty for every other metric.
	Landings []ExplainLanding `json:"landings"` // by repository, then time
	Commits  []ExplainCommit  `json:"commits"`  // the most surviving lines first
	Files    []ExplainFile    `json:"files"`    // the most of the person's lines first
	Caveats  []Caveat         `json:"caveats"`

	addrs []string // the run's addresses (#90); never encoded
}

func (e *Explain) runAddresses() []string { return e.addrs }

// ExplainLanding is one landing a landed value counts for the person.
type ExplainLanding struct {
	Repo    string    `json:"repo"` // name
	RepoKey string    `json:"repo_key"`
	Landing string    `json:"landing"` // full SHA
	Time    time.Time `json:"time"`    // committer time
	Merge   bool      `json:"merge"`
	Title   string    `json:"title"` // the landing commit's subject: local output only, never sent to an AI
	// Commits are the person's commits in it that the value counts:
	// landed@2's non-merge commits, landed@1's landing commit.
	Commits        int  `json:"commits"`
	LinesAdded     *int `json:"lines_added"`      // what those commits added on paths with a role; null when unknown (#74)
	ProdLinesAdded *int `json:"prod_lines_added"` // the prod subset
}

// ExplainCommit is one blamed commit whose prod lines at HEAD surviving@1
// credits to the person: it may be older than the history.
type ExplainCommit struct {
	Repo    string    `json:"repo"` // name
	RepoKey string    `json:"repo_key"`
	Commit  string    `json:"commit"` // full SHA
	Time    time.Time `json:"time"`   // author time
	Title   string    `json:"title"`  // its subject when the history holds it, else ""; local output only
	Lines   int       `json:"lines"`  // its non-blank prod lines at HEAD
}

// ExplainFile is one prod file the person holds at least half of.
type ExplainFile struct {
	Repo    string `json:"repo"` // name
	RepoKey string `json:"repo_key"`
	Path    string `json:"path"`
	Lines   int    `json:"lines"` // the person's non-blank lines in it
	Of      int    `json:"of"`    // the file's non-blank lines
}

// Change is one change's contribution to a person's value: a cluster, the
// unit the bootstrap resamples.
type Change struct {
	Repo    string    `json:"repo"` // name
	RepoKey string    `json:"repo_key"`
	Landing string    `json:"landing"` // the landing's full SHA, or a pull request id from M2
	Time    time.Time `json:"time"`    // the landing's committer time
	Merge   bool      `json:"merge"`
	Title   string    `json:"title"` // the landing commit's subject: local output only, never sent to an AI
	Num     float64   `json:"num"`
	Den     float64   `json:"den"` // ratio metrics; 0 for a sum
}

// WhoKnows is the who_knows@1 answer for a path: a routing answer, not a
// rank.
type WhoKnows struct {
	Provenance Provenance     `json:"provenance"`
	Path       string         `json:"path"` // repo-relative prefix; "" for the whole repository
	Repo       string         `json:"repo"` // the --repo filter (Repo.Key), or "" for every repository
	Repos      []WhoKnowsRepo `json:"repos"`
	Caveats    []Caveat       `json:"caveats"`

	addrs []string // the run's addresses (#90); never encoded
}

func (w *WhoKnows) runAddresses() []string { return w.addrs }

// WhoKnowsRepo is who knows the path in one repository.
type WhoKnowsRepo struct {
	Repo              string   `json:"repo"` // name
	Key               string   `json:"key"`
	Lines             int      `json:"lines"`               // surviving prod non-blank lines under the path
	ProdFiles         int      `json:"prod_files"`          // prod files under the path
	FilesWithMajority int      `json:"files_with_majority"` // of those, files with a majority owner
	MajorityOwners    []Knower `json:"majority_owners"`     // hold at least half the lines of a file under the path
	TopAuthors        []Knower `json:"top_authors"`         // up to five, by prod lines under the path, then email
	RecentLanders     []Knower `json:"recent_landers"`      // credited with a change under the path in the last 90 days
	// RecentReviewers reviewed, in the last 90 days, a merged pull request
	// that changed the path, by the GitHub data the run read: [] also when
	// the run has none for the repository, which provenance.forge then does
	// not list.
	RecentReviewers []Knower `json:"recent_reviewers"`
	Bots            []Knower `json:"bots"` // listed apart, never mixed into the lists above
	// KnowledgeLoss, People and PeopleInactive are knowledge_loss@1 for the
	// code under the path, and Concentration knowledge_concentration@1
	// (#105): the computation a scan stores per repository and top-level
	// directory, asked for any path (metrics.PathKnowledge). null where the
	// metric has no value there (no person holds a line there).
	KnowledgeLoss  *Share         `json:"knowledge_loss"`
	People         *int           `json:"people"`          // humans holding prod lines under the path
	PeopleInactive *int           `json:"people_inactive"` // of them, those gone quiet
	Concentration  *Concentration `json:"concentration"`
}

// Knower is one person's knowledge of a path. Email is a reviewer's
// "@login" when the run cannot link the login to an email.
type Knower struct {
	Email          string `json:"email"`
	Name           string `json:"name"`            // as StillHereRow's
	NameWithheld   bool   `json:"name_withheld"`   // as StillHereRow's
	Lines          int    `json:"lines"`           // surviving prod non-blank lines under the path
	MajorityFiles  int    `json:"majority_files"`  // files under the path where the person is a majority owner
	RecentLandings int    `json:"recent_landings"` // landings in the last 90 days that changed the path
	RecentReviews  int    `json:"recent_reviews"`  // pull requests that changed the path, reviewed in the last 90 days
}

// Me is one person's own page: every value and rank interval the run holds
// about them. That is the facts a lead sees about them (ADR 0001), and also
// their own AI-assisted share, which no other page shows (ADR 0007,
// OwnPageOnly).
type Me struct {
	Provenance Provenance `json:"provenance"`
	Person     Subject    `json:"person"`
	Values     []Value    `json:"values"` // sorted by metric, scope and window
	Ranks      []MeRank   `json:"ranks"`  // sorted by metric and scope
	// Metrics are the definitions of the metrics the page shows, in
	// registry order, as explain carries its one (#66): a value is printed
	// as a share exactly when its metric's Ratio says so, whatever its den.
	Metrics []MetricInfo `json:"metrics"`
	Caveats []Caveat     `json:"caveats"`

	addrs []string // the run's addresses (#90); never encoded
}

func (m *Me) runAddresses() []string { return m.addrs }

// MeRank is one person's place in one rank table.
type MeRank struct {
	Metric   string   `json:"metric"`
	Scope    Scope    `json:"scope"`
	Window   Interval `json:"window"`
	Percent  bool     `json:"percent"`
	MinN     int      `json:"min_n"`
	Eligible bool     `json:"eligible"`
	RankLo   int      `json:"rank_lo"` // 0 when not eligible
	RankHi   int      `json:"rank_hi"`
	Of       int      `json:"of"` // eligible people in the table
	Estimate float64  `json:"estimate"`
	Lo       float64  `json:"lo"`
	Hi       float64  `json:"hi"`
	N        int      `json:"n"`
}

// Validation is how well each ranked metric separates the changes a person
// labelled meaningful from those labelled routine
// (docs/metrics.md#validity-checks). A change counts once: one label per
// change and labeller, the last row winning, and over several labellers
// the label most of them gave.
type Validation struct {
	Provenance Provenance  `json:"provenance"`
	Labels     int         `json:"labels"`     // labelled rows read (one labeller's, with --labeller)
	Labellers  int         `json:"labellers"`  // distinct labellers named; rows that name none are no labeller's
	Changes    int         `json:"changes"`    // the distinct changes of the run the rows label, each with one label
	Meaningful int         `json:"meaningful"` // changes labelled meaningful
	Routine    int         `json:"routine"`
	Noise      int         `json:"noise"`     // read and left out of every AUC
	Unmatched  int         `json:"unmatched"` // rows that name no change of the run (a landing or a pull request), or several
	Metrics    []MetricAUC `json:"metrics"`   // sorted by metric id
	Caveats    []Caveat    `json:"caveats"`
}

// The bar a ranked metric's labels clear (product.md, ADR 0014): an AUC of
// at least BarAUC on at least BarLabels of its scored labels.
const (
	BarAUC    = 0.7
	BarLabels = 50
)

// MetricAUC is one metric's separation of the labelled changes.
type MetricAUC struct {
	Metric     string   `json:"metric"`
	AUC        *float64 `json:"auc"` // 0.5 is chance; nil when no meaningful or no routine change was scored
	Lo         *float64 `json:"lo"`  // 95% bootstrap interval of AUC (rank.AUCInterval), resampling changes; nil with AUC
	Hi         *float64 `json:"hi"`
	N          int      `json:"n"` // distinct labelled changes the metric scored, each at its own grain (metrics.Scorer)
	Meaningful int      `json:"meaningful"`
	Routine    int      `json:"routine"`
	// Beta is true when these labels do not clear the bar (AUC 0.7 on 50
	// scored labels). False says they clear it, which is evidence about the
	// labelled code: the beta mark in stillhere's output changes only with
	// the metric's published Validity line (ADR 0014).
	Beta bool `json:"beta"`
}
