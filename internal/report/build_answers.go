package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/rank"
)

// Builders for the read commands' answers. Like Build, they arrange values
// the metrics computed and compute none.

// ExplainInput is what NewExplain builds an explanation from.
type ExplainInput struct {
	Run     *domain.Run
	Facts   *domain.FactSet
	Who     People
	Metric  MetricInfo
	Subject domain.Subject
	Values  []domain.MetricValue // the subject's values for the metric
	// Clusters holds, by Repo.Key, the subject's clusters in that
	// repository's scope: the changes a ranked metric counted for them.
	Clusters map[string][]rank.Cluster
	// Trace is what a person's values of a metric that is not ranked are
	// made of (#109, metrics.Traced); nil for any other.
	Trace *domain.Trace
	// Window is the window the metric reads (its clusters' or its trace's),
	// which says why an answer with no value is empty (#92); zero for a
	// value at HEAD, or when the reader cannot tell.
	Window   domain.Window
	Forge    *ForgeRead // the GitHub data the run read; nil when the reader cannot tell
	Commands Commands   // how the answer names the commands it sends the reader to; nil: the command line's
}

// NewExplain builds the explain answer: the subject's values and, for a
// ranked metric, each change behind them with its contribution.
func NewExplain(in *ExplainInput) *Explain {
	e := &Explain{
		Metric:  in.Metric,
		Subject: SubjectOf(in.Subject, in.Who),
		Values:  Values(in.Values, in.Who),
		addrs:   runAddresses(in.Who),
	}
	involved := map[string]bool{}
	for _, v := range in.Values {
		if v.Scope.Kind == domain.ScopeRepo {
			involved[v.Scope.Key] = true
		}
		forgeScope(in.Facts, involved, in.Forge, v.Metric.String(), v.Scope)
	}
	pulls := 0 // clusters that are pull requests, not landings
	for i := range in.Facts.Repos {
		rf := &in.Facts.Repos[i]
		clusters := in.Clusters[rf.Repo.Key]
		if len(clusters) == 0 {
			continue
		}
		involved[rf.Repo.Key] = true
		landings := map[string]domain.Landing{}
		for _, l := range rf.Landings {
			landings[l.SHA] = l
		}
		titles := map[string]string{}
		for _, c := range rf.Commits {
			titles[c.SHA] = c.Subject
		}
		for _, c := range clusters {
			l, ok := landings[c.Change]
			if !ok {
				// A pull request (reviews_given@1's clusters): explain lists
				// landings, and says what it leaves out.
				pulls++
				continue
			}
			e.Changes = append(e.Changes, Change{Repo: rf.Repo.Name, RepoKey: rf.Repo.Key, Landing: c.Change,
				Time: l.Time, Merge: l.Merge, Title: titles[l.SHA], Num: c.Num, Den: c.Den})
		}
	}
	sortChanges(e.Changes)
	if in.Trace != nil {
		e.Landings, e.Commits, e.Files = traced(in.Facts, in.Trace, involved)
		e.Coverage = coverageTraced(in.Facts, in.Trace, in.Who, involved)
	}
	repos := reposWith(in.Facts, involved)
	e.Provenance = datedProvenance(in.Run, in.Facts, in.Forge, repos, []string{in.Metric.ID})
	e.Caveats = answerCaveats(repos)
	if in.Forge.reads(in.Metric.ID) {
		b := answerBuilder(in.Run, in.Facts, in.Forge)
		e.Caveats = append(e.Caveats, b.forgeCaveats(reposWith(in.Facts, nil), in.Metric.ID, true)...)
	}
	if pulls > 0 {
		e.Caveats = append(e.Caveats, pullsNotListed(in.Metric.ID))
	}
	if len(e.Values) == 0 {
		e.Caveats = append(e.Caveats, explainEmpty(in))
	}
	if in.Subject.Kind == domain.SubjectPerson {
		if c, ok := sameName(in.Facts, in.Who, in.Subject.Key, in.Commands); ok {
			e.Caveats = append(e.Caveats, c)
		}
	}
	if c, ok := explainCandidates(in.Values, in.Commands); ok {
		e.Caveats = append(e.Caveats, c)
	}
	if in.Metric.ID == MetricAIAssisted && len(e.Values) > 0 {
		// A page that holds an AI-assisted share says it is a lower bound,
		// where the share is (report-json.md, caveats).
		e.Caveats = append(e.Caveats, aiLowerBound())
	}
	if note := ValidityNote(in.Metric); note != "" {
		e.Caveats = append(e.Caveats, Caveat{Code: codeValidity, Message: "Validity: " + note})
	}
	return e
}

// explainCandidates is the unmerged-identities caveat (#104) of an
// explanation of knowledge_loss@1 or knowledge_concentration@1 whose value
// identity candidates hold lines in: about a repository, a group or an
// area, as a card and an overview carry it (candidatesCaveat).
func explainCandidates(values []domain.MetricValue, cmds Commands) (Caveat, bool) {
	for _, v := range values {
		switch v.Metric.String() {
		case MetricKnowledgeLoss:
			if c, ok := candidatesCaveat(shareOf(MetricKnowledgeLoss, v), nil, 0, cmds); ok {
				return c, true
			}
		case MetricConcentration:
			if c, ok := candidatesCaveat(nil, concentrationOf(v), 0, cmds); ok {
				return c, true
			}
		}
	}
	return Caveat{}, false
}

// traced maps a trace (#109) onto the answer's lists, each item with its
// repository's name and, for a landing or a commit the history holds, its
// subject, and adds the repositories it names to involved.
func traced(fs *domain.FactSet, t *domain.Trace, involved map[string]bool) ([]ExplainLanding, []ExplainCommit, []ExplainFile) {
	type repo struct {
		name     string
		subjects map[string]string
	}
	repos := map[string]*repo{}
	of := func(key string) *repo {
		if r := repos[key]; r != nil {
			return r
		}
		r := &repo{subjects: map[string]string{}}
		for i := range fs.Repos {
			if rf := &fs.Repos[i]; rf.Repo.Key == key {
				r.name = rf.Repo.Name
				for _, c := range rf.Commits {
					r.subjects[c.SHA] = c.Subject
				}
			}
		}
		repos[key] = r
		involved[key] = true
		return r
	}
	known := func(n int) *int {
		if n < 0 {
			return nil
		}
		return &n
	}
	var landings []ExplainLanding
	for _, l := range t.Landings {
		r := of(l.Repo)
		landings = append(landings, ExplainLanding{Repo: r.name, RepoKey: l.Repo, Landing: l.SHA, Time: l.Time, Merge: l.Merge,
			Title: r.subjects[l.SHA], Commits: l.Commits, Copies: l.Copies, LinesAdded: known(l.Lines),
			ProdLinesAdded: known(l.ProdLines)})
	}
	slices.SortFunc(landings, func(a, b ExplainLanding) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.RepoKey, b.RepoKey), a.Time.Compare(b.Time),
			cmp.Compare(a.Landing, b.Landing))
	})
	var commits []ExplainCommit
	for _, c := range t.Commits {
		r := of(c.Repo)
		commits = append(commits, ExplainCommit{Repo: r.name, RepoKey: c.Repo, Commit: c.SHA, Time: c.Time,
			Title: r.subjects[c.SHA], Lines: c.Lines})
	}
	var files []ExplainFile
	for _, f := range t.Files {
		files = append(files, ExplainFile{Repo: of(f.Repo).name, RepoKey: f.Repo, Path: f.Path, Lines: f.Lines, Of: f.Of})
	}
	return landings, commits, files
}

// coverageTraced maps review_coverage@1's changes (domain.Trace.Changes)
// onto the answer's list, each with its repository's name, its author as
// every page names a person, and its title: the pull request's for one, so
// a pull request merged into another branch is not shown under the merge
// that brought it, else its landing commit's subject.
func coverageTraced(fs *domain.FactSet, t *domain.Trace, who People, involved map[string]bool) []CoverageChange {
	if len(t.Changes) == 0 {
		return nil
	}
	names := map[string]string{}    // Repo.Key → name
	subjects := map[string]string{} // commit SHA → subject, every repository's
	for i := range fs.Repos {
		rf := &fs.Repos[i]
		names[rf.Repo.Key] = rf.Repo.Name
		for _, c := range rf.Commits {
			subjects[c.SHA] = c.Subject
		}
	}
	type pullKey struct {
		repo   string
		number int
	}
	titles := map[pullKey]string{}
	if fs.Forge != nil {
		join := fs.Forge.Join
		if join == nil {
			join = domain.ForgeJoin(fs.Repos, fs.Forge.Repos)
		}
		for _, p := range fs.Forge.PullRequests {
			for _, key := range join[p.Repo] {
				if _, ok := titles[pullKey{key, p.Number}]; !ok {
					titles[pullKey{key, p.Number}] = p.Title
				}
			}
		}
	}
	out := make([]CoverageChange, 0, len(t.Changes))
	for _, c := range t.Changes {
		involved[c.Repo] = true
		cc := CoverageChange{Repo: names[c.Repo], RepoKey: c.Repo, Landing: c.Landing, Time: c.Time, Base: c.Base,
			OtherBranch: c.Via, Title: subjects[c.Landing], Outcome: c.Outcome, Reviewers: c.Reviewers,
			ApprovedOnly: c.ApprovedOnly}
		if c.Pull > 0 {
			n := c.Pull
			cc.Pull = &n
			if title, ok := titles[pullKey{c.Repo, n}]; ok {
				cc.Title = title
			}
		}
		if c.Named > 0 {
			n := c.Named
			cc.Named = &n
		}
		if c.Author != "" {
			s := SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: c.Author}, who)
			cc.Author = &s
		}
		out = append(out, cc)
	}
	return out
}

// explainEmpty is the no-values caveat of an explanation that holds no
// value of the metric about a subject the run has other values about (#92):
// an empty answer must say whether it is a real nothing, and why, where
// the run can tell.
func explainEmpty(in *ExplainInput) Caveat {
	msg := in.Metric.ID + " holds no value about this " + in.Subject.Kind + " in this run"
	switch {
	case in.Subject.Kind != domain.SubjectPerson:
	case !in.Window.Since.IsZero() || !in.Window.Until.IsZero():
		msg += ": no change of theirs in the window it reads, " + interval(intervalOf(in.Window)) + ", counts for it"
	case in.Metric.ID == MetricSurviving || in.Metric.ID == MetricMajority:
		msg += ": blame credits them with no prod line at HEAD that it counts"
	}
	return Caveat{Code: codeNoValues, Message: msg + ", though it holds other metrics' values about it: an empty answer, " +
		"not a missing one."}
}

// sortChanges orders an explanation's changes by repository, then landing
// time, then SHA.
func sortChanges(cs []Change) {
	slices.SortFunc(cs, func(a, b Change) int {
		return cmp.Or(
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.RepoKey, b.RepoKey),
			a.Time.Compare(b.Time),
			cmp.Compare(a.Landing, b.Landing),
		)
	})
}

// WhoKnowsInput is what NewWhoKnows builds a who-knows answer from.
type WhoKnowsInput struct {
	Run   *domain.Run
	Facts *domain.FactSet // the run's facts: provenance, caveats, and the line owners under Path that the no-values check reads
	Who   People
	Path  string // repo-relative prefix; "" for the whole repository
	Repo  string // Repo.Key, or "" for every repository
	// Values are who_knows@1's values for the path, per repository: one
	// about the path itself and one per person (MetricWhoKnows in
	// values.go).
	Values   []domain.MetricValue
	Forge    *ForgeRead // the GitHub data the run read; nil when the reader cannot tell
	Commands Commands   // how the answer names the commands it sends the reader to; nil: the command line's
	// Knowledge are knowledge_loss@1's and knowledge_concentration@1's
	// values about the path, per repository (metrics.PathKnowledge, #105).
	Knowledge []domain.MetricValue
	// Activity is each person's last activity in the run
	// (metrics.LastActivity), which each person listed shows; nil: none.
	Activity map[string]domain.Activity
}

// NewWhoKnows builds the who-knows answer: per repository, the majority
// owners, the top five authors, the recent landers and, from the GitHub
// data the run read, the recent reviewers under the path, with bots apart
// (docs/metrics.md#who_knows1-per-path).
func NewWhoKnows(in *WhoKnowsInput) *WhoKnows {
	w := &WhoKnows{Path: in.Path, Repo: in.Repo, addrs: runAddresses(in.Who)}
	ix := newValueIndex(in.Values)
	kx := newValueIndex(in.Knowledge)
	metrics := []string{MetricWhoKnows}
	var asked []*domain.RepoFacts
	for i := range in.Facts.Repos {
		rf := &in.Facts.Repos[i]
		if in.Repo != "" && rf.Repo.Key != in.Repo {
			continue
		}
		asked = append(asked, rf)
		scope := domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}
		r := WhoKnowsRepo{Repo: rf.Repo.Name, Key: rf.Repo.Key}
		for _, v := range ix[MetricWhoKnows][scope] {
			if v.Subject.Kind != domain.SubjectPerson {
				// The value about the path itself: the repository, a
				// file or a directory.
				r.Lines, r.ProdFiles = count(v.Value), count(v.Detail[detailProdFiles])
				r.FilesWithMajority = count(v.Detail[detailFilesWithMajority])
				r.Roles, r.Files = countedRoles(v.Detail)
				r.Uncounted = uncountedRoles(v.Detail)
				break
			}
		}
		for _, v := range kx[MetricKnowledgeLoss][scope] {
			r.KnowledgeLoss = shareOf(MetricKnowledgeLoss, v)
			people, inactive := count(v.Detail[detailPeople]), count(v.Detail[detailPeopleInactive])
			r.People, r.PeopleInactive = &people, &inactive
			metrics = append(metrics, MetricKnowledgeLoss)
		}
		for _, v := range kx[MetricConcentration][scope] {
			r.Concentration = concentrationOf(v)
			metrics = append(metrics, MetricConcentration)
		}
		var humans []Knower
		top := map[string]int{} // the metric's own top-author places, 1 to 5
		for _, v := range ix.people(MetricWhoKnows, scope) {
			email := v.Subject.Key
			name, withheld := personName(in.Who, email)
			k := Knower{
				Email: email, Name: name, NameWithheld: withheld,
				Lines:          count(v.Value),
				MajorityFiles:  count(v.Detail[detailMajorityFiles]),
				RecentLandings: count(v.Detail[detailRecentLandings]),
				RecentReviews:  count(v.Detail[detailRecentReviews]),
			}
			if isBot(in.Who, email) {
				if k.Lines > 0 {
					r.Bots = append(r.Bots, k)
				}
				continue
			}
			k.LastActive, k.LastActivity, k.QuietInDays, k.GoingQuiet = lastActive(in.Activity, email)
			humans = append(humans, k)
			if place := count(v.Detail[detailTopAuthor]); place > 0 {
				top[email] = place
			}
		}
		for _, k := range humans {
			if k.MajorityFiles > 0 {
				r.MajorityOwners = append(r.MajorityOwners, k)
			}
			if _, ok := top[k.Email]; ok || (len(top) == 0 && k.Lines > 0) {
				r.TopAuthors = append(r.TopAuthors, k)
			}
			if k.RecentLandings > 0 {
				r.RecentLanders = append(r.RecentLanders, k)
			}
			if k.RecentReviews > 0 {
				r.RecentReviewers = append(r.RecentReviewers, k)
			}
		}
		slices.SortFunc(r.MajorityOwners, func(a, b Knower) int {
			return cmp.Or(cmp.Compare(b.MajorityFiles, a.MajorityFiles), cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.Email, b.Email))
		})
		byLines := func(a, b Knower) int { return cmp.Or(cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.Email, b.Email)) }
		// The metric names the top five; without its places, the same rule
		// (prod lines, then email) picks them.
		slices.SortFunc(r.TopAuthors, func(a, b Knower) int {
			return cmp.Or(cmp.Compare(top[a.Email], top[b.Email]), byLines(a, b))
		})
		if len(r.TopAuthors) > maxTopAuthors {
			r.TopAuthors = r.TopAuthors[:maxTopAuthors]
		}
		slices.SortFunc(r.RecentLanders, func(a, b Knower) int {
			return cmp.Or(cmp.Compare(b.RecentLandings, a.RecentLandings), cmp.Compare(a.Email, b.Email))
		})
		slices.SortFunc(r.RecentReviewers, func(a, b Knower) int {
			return cmp.Or(cmp.Compare(b.RecentReviews, a.RecentReviews), cmp.Compare(a.Email, b.Email))
		})
		slices.SortFunc(r.Bots, byLines)
		if r.Lines > 0 || len(r.MajorityOwners)+len(r.TopAuthors)+len(r.RecentLanders)+len(r.RecentReviewers)+len(r.Bots) > 0 {
			w.Repos = append(w.Repos, r)
		}
	}
	slices.SortFunc(w.Repos, func(a, b WhoKnowsRepo) int { return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key)) })
	w.Provenance = datedProvenance(in.Run, in.Facts, in.Forge, asked, metrics)
	w.Caveats = answerCaveats(asked)
	if c, ok := whoKnowsMissing(in.Path, asked, ix); ok {
		w.Caveats = append(w.Caveats, c)
	}
	if c, ok := whoKnowsOtherRoles(in.Path, w.Repos, in.Commands); ok {
		w.Caveats = append(w.Caveats, c)
	}
	if c, ok := whoKnowsUncounted(in.Path, w.Repos); ok {
		w.Caveats = append(w.Caveats, c)
	}
	// Two people the answer lists under one name may be one person whose
	// ownership and landings are split between two addresses, which can
	// hide a majority owner: say so, as the report, explain and me do (#69).
	listed := map[string]bool{}
	for _, r := range w.Repos {
		for _, ks := range [][]Knower{r.MajorityOwners, r.TopAuthors, r.RecentLanders, r.RecentReviewers} {
			for _, k := range ks {
				listed[k.Email] = true
			}
		}
	}
	if c, ok := sharedNames(in.Who, listed, in.Commands); ok {
		w.Caveats = append(w.Caveats, c)
	}
	// The path's knowledge loss and concentration count each address
	// apart, as a card's do: where merging the run's identity candidates
	// would move them, say how far, in the one caveat the shared names
	// share (#104).
	var moved []string
	for _, r := range w.Repos {
		c, ok := candidatesCaveat(r.KnowledgeLoss, r.Concentration, 0, in.Commands)
		if !ok {
			continue
		}
		if len(w.Repos) > 1 {
			c.Message = r.Repo + ": " + c.Message
		}
		moved = append(moved, r.Repo)
		w.Caveats = withCaveat(w.Caveats, c)
	}
	if len(moved) > 0 && len(w.Repos) > 1 {
		i := slices.IndexFunc(w.Caveats, func(c Caveat) bool { return c.Code == codeIdentities })
		w.Caveats[i].Repos = slices.Compact(slices.Sorted(slices.Values(append(w.Caveats[i].Repos, moved...))))
	}
	if in.Forge.reads(MetricWhoKnows) {
		// The reviewers of each repository the answer lists come from its
		// GitHub data: say where that is missing, as for any number it
		// qualifies, and say nothing of a repository with nothing under
		// the path.
		var listed []*domain.RepoFacts
		for _, rf := range asked {
			if slices.ContainsFunc(w.Repos, func(r WhoKnowsRepo) bool { return r.Key == rf.Repo.Key }) {
				listed = append(listed, rf)
			}
		}
		b := answerBuilder(in.Run, in.Facts, in.Forge)
		w.Caveats = append(w.Caveats, b.forgeCaveats(listed, MetricWhoKnows, false)...)
		if slices.Contains(metrics, MetricKnowledgeLoss) && in.Forge.reads(MetricKnowledgeLoss) {
			// The path's knowledge loss counts a review in any repository of
			// the run as activity, as a card's does: say where that is
			// missing, in the repositories listed and outside them.
			w.Caveats = append(w.Caveats, b.forgeCaveats(listed, MetricKnowledgeLoss, false)...)
			var others []*domain.RepoFacts
			for i := range in.Facts.Repos {
				if rf := &in.Facts.Repos[i]; !slices.Contains(listed, rf) {
					others = append(others, rf)
				}
			}
			w.Caveats = append(w.Caveats, b.forgeElsewhere(others, MetricKnowledgeLoss)...)
		}
	}
	return w
}

// whoKnowsOtherRoles is the caveat of a who-knows answer that counts the
// lines of roles other than prod in some repository: the path holds no
// surviving prod line there, so who_knows@1 answers for its tests, docs or
// build files, whose people know those, not the product code; and
// knowledge loss and concentration, which count prod lines only, have no
// value for it. A page that listed them as the path's owners without
// saying so would read as product knowledge (round 2: tests/ and docs/
// answered "nobody", although blame held thousands of lines there). Where
// the reader has it (Commands.Paths), it names the list of the path's files
// with the rule that gave each its role, such as policy revision 3's
// examples/ as doc and benches/ as test (wave 10).
func whoKnowsOtherRoles(prefix string, repos []WhoKnowsRepo, cmds Commands) (Caveat, bool) {
	var names, roles []string
	for _, r := range repos {
		if len(r.Roles) > 0 && !slices.Equal(r.Roles, []string{string(domain.RoleProd)}) {
			names = append(names, r.Repo)
			roles = append(roles, r.Roles...)
		}
	}
	if len(names) == 0 {
		return Caveat{}, false
	}
	roles = slices.Compact(slices.Sorted(slices.Values(roles)))
	where := "this path holds no surviving prod line"
	if prefix == "" {
		where = "the repository holds no surviving prod line"
	}
	rules := ""
	if list := CommandsOr(cmds).Paths(); list != "" {
		rules = " " + list + " lists each file with the rule that gave it its role."
	}
	return Caveat{
		Code: codeOtherRoles,
		Message: fmt.Sprintf("%s, so %s counts its %s lines instead: the people listed hold %s files, not product code, "+
			"and knowledge loss and concentration, which count prod lines only, have no value for it.%s",
			identSentence(where), MetricWhoKnows, joinNames(roles), joinNames(roles), rules),
		Repos: slices.Compact(slices.Sorted(slices.Values(names))),
	}, true
}

// countedRoles reads which roles a who_knows@1 path value counts, and how
// many of their files: its <role>_files Detail beside prod_files, which
// the metric gives only under a path with no surviving prod line; else
// prod and its prod files.
func countedRoles(d map[string]float64) ([]string, int) {
	var roles []string
	files := 0
	for k, n := range d {
		if role, ok := strings.CutSuffix(k, detailFilesSuffix); ok && k != detailProdFiles && !strings.HasPrefix(k, detailUncounted) && n > 0 {
			roles = append(roles, role)
			files += count(n)
		}
	}
	if len(roles) == 0 {
		return []string{string(domain.RoleProd)}, count(d[detailProdFiles])
	}
	slices.Sort(roles)
	return roles, files
}

// uncountedRoles reads the lines of other roles a who_knows@1 path value
// that counts prod lines leaves out (uncounted_<role>_lines and _files), by
// role; nil for none.
func uncountedRoles(d map[string]float64) []RoleLines {
	var out []RoleLines
	for k, n := range d {
		rest, ok := strings.CutPrefix(k, detailUncounted)
		if role, lines := strings.CutSuffix(rest, "_lines"); ok && lines && n > 0 {
			out = append(out, RoleLines{Role: role, Lines: count(n), Files: count(d[detailUncounted+role+detailFilesSuffix])})
		}
	}
	slices.SortFunc(out, func(a, b RoleLines) int { return cmp.Compare(a.Role, b.Role) })
	return out
}

// whoKnowsUncounted is the uncounted-roles caveat of a who-knows answer
// whose path holds more lines of roles other than prod and test than the
// prod lines it counts in some repository: most of what is there is docs,
// build files or data whose people the answer does not list, which a
// reader of "53 surviving prod lines" under docs/ would not guess (wave
// 10's review: werkzeug's docs/ holds 2,691 doc and iac lines beside
// docs/conf.py's 53). Test lines do not count toward it: tests sit beside
// the code they test, as Go's _test.go files do, and are mostly its own
// people's, so a package that holds twice as many test lines as code
// (cli/cli's pkg/cmd/pr) is answered for its code without a caveat. Every
// answer's totals still say what it left out.
func whoKnowsUncounted(prefix string, repos []WhoKnowsRepo) (Caveat, bool) {
	var names, parts []string
	for _, r := range repos {
		left := 0
		for _, u := range r.Uncounted {
			if u.Role != string(domain.RoleTest) {
				left += u.Lines
			}
		}
		if left <= r.Lines {
			continue
		}
		names = append(names, r.Repo)
		var roles []string
		for _, u := range r.Uncounted {
			roles = append(roles, fmt.Sprintf("%s %s lines in %s", grouped(u.Lines), u.Role, plural(u.Files, "file", "files")))
		}
		parts = append(parts, fmt.Sprintf("%s beside %s prod lines in %s", joinNames(roles), grouped(r.Lines), r.Repo))
	}
	if len(names) == 0 {
		return Caveat{}, false
	}
	where := "This path holds"
	if prefix == "" {
		where = "The repository holds"
	}
	return Caveat{
		Code: codeUncountedRoles,
		Message: fmt.Sprintf("%s more lines of docs, build files or data than prod lines (%s), and %s counts prod lines "+
			"wherever a path holds any: the people listed know its product code, not the rest, whose own authors the answer "+
			"leaves out. A path under it that holds no prod file is answered for its own lines.",
			where, strings.Join(parts, "; "), MetricWhoKnows),
		Repos: slices.Compact(slices.Sorted(slices.Values(names))),
	}, true
}

// whoKnowsMissing is the no-values caveat of a who-knows answer: some
// repository holds surviving prod lines under the path, yet who_knows@1 gave
// no value there, although it always describes the path itself when it has
// lines. An empty list would then pass for "nobody knows this code", so the
// caveat says the answer is missing instead.
func whoKnowsMissing(prefix string, asked []*domain.RepoFacts, ix valueIndex) (Caveat, bool) {
	var repos []string
	lines := 0
	for _, rf := range asked {
		if rf.Status != domain.RepoStatusOK || !rf.LineFacts {
			continue
		}
		n := blamedProdLines(rf, func(p string) bool { return underPath(p, prefix) })
		if n > 0 && len(ix[MetricWhoKnows][domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}]) == 0 {
			repos = append(repos, rf.Repo.Name)
			lines += n
		}
	}
	if len(repos) == 0 {
		return Caveat{}, false
	}
	slices.Sort(repos)
	return Caveat{
		Code: codeNoValues,
		Message: MetricWhoKnows + " produced no values for this path although the run holds " + grouped(lines) +
			" surviving prod lines under it, so this answer is missing, not a real \"nobody\".",
		Repos: slices.Compact(repos),
	}, true
}

// underPath reports whether path p is prefix itself or inside it, by whole
// segments: "src" covers src/pay.go but not srcutil/x.go, and "" covers
// every path.
func underPath(p, prefix string) bool {
	return prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")
}

// MeInput is what NewMe builds a person's own page from.
type MeInput struct {
	Run    *domain.Run
	Facts  *domain.FactSet
	Who    People
	Email  string               // canonical
	Values []domain.MetricValue // the person's values
	Ranks  []RankSet            // whole rank tables: the person's place is found in each
	Forge  *ForgeRead           // the GitHub data the run read; nil when the reader cannot tell
	// Metrics are the registry's definitions, at least of the page's
	// metrics. The page keeps those of its own (Me.Metrics), whose Ratio
	// says which values print as a share (#66), and the ranked ones among
	// them carry their validity notes to it (ValidityCaveats).
	Metrics  []MetricInfo
	Commands Commands // how the page names the commands it sends the reader to; nil: the command line's
}

// NewMe builds a person's own page: their values and their place in every
// rank table they are in.
func NewMe(in *MeInput) *Me {
	m := &Me{
		Person: SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: in.Email}, in.Who),
		Values: Values(in.Values, in.Who),
		addrs:  runAddresses(in.Who),
	}
	involved := map[string]bool{}
	metrics := map[string]bool{}
	for _, v := range in.Values {
		metrics[v.Metric.String()] = true
		if v.Scope.Kind == domain.ScopeRepo {
			involved[v.Scope.Key] = true
		}
		forgeScope(in.Facts, involved, in.Forge, v.Metric.String(), v.Scope)
	}
	for _, rs := range in.Ranks {
		of := 0
		var mine *domain.RankInterval
		for i, ri := range rs.Intervals {
			if ri.Eligible && !isBot(in.Who, ri.Subject.Key) {
				of++
			}
			if ri.Subject.Kind == domain.SubjectPerson && ri.Subject.Key == in.Email {
				mine = &rs.Intervals[i]
			}
		}
		if mine == nil {
			continue
		}
		metrics[rs.Metric.ID] = true
		forgeScope(in.Facts, involved, in.Forge, rs.Metric.ID, rs.Scope)
		row := RankedRow(Subject{}, *mine)
		m.Ranks = append(m.Ranks, MeRank{
			Metric: rs.Metric.ID, Scope: scopeOf(rs.Scope), Window: intervalOf(rs.Window),
			Percent: rs.Percent, MinN: rs.MinN, Eligible: mine.Eligible,
			RankLo: row.RankLo, RankHi: row.RankHi, Of: of,
			Estimate: mine.Estimate, Lo: row.Lo, Hi: row.Hi, N: mine.N,
		})
	}
	slices.SortFunc(m.Ranks, func(a, b MeRank) int {
		return cmp.Or(cmp.Compare(a.Metric, b.Metric), cmp.Compare(a.Scope.Kind, b.Scope.Kind), cmp.Compare(a.Scope.Key, b.Scope.Key))
	})
	var ids []string
	for id := range metrics {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	repos := reposWith(in.Facts, involved)
	m.Provenance = datedProvenance(in.Run, in.Facts, in.Forge, repos, ids)
	m.Caveats = answerCaveats(repos)
	// A person's review numbers can come from any repository of the run, so
	// each GitHub number the page shows is caveated over all of them.
	m.Caveats = append(m.Caveats, ForgeCaveats(in.Run, in.Facts, in.Forge, ids)...)
	if c, ok := sameName(in.Facts, in.Who, in.Email, in.Commands); ok {
		m.Caveats = append(m.Caveats, c)
	}
	// The page carries the definitions of the metrics it shows, as explain
	// carries its one, so the renderers print a value as a share by its
	// metric's one declaration (MetricInfo.Ratio), never by its den (#66).
	// And each caveat goes where the number it qualifies is (#48): a person's
	// page shows their ranked values and ranks, so it says how far each
	// ranked metric is validated, as its rank table does, and their own
	// AI-assisted share, on their own page, is a lower bound (#69).
	var shown []MetricInfo
	for _, mi := range in.Metrics {
		if metrics[mi.ID] {
			shown = append(shown, mi)
		}
	}
	m.Metrics = shown
	m.Caveats = append(m.Caveats, ValidityCaveats(shown...)...)
	if metrics[MetricAIAssisted] {
		m.Caveats = append(m.Caveats, aiLowerBound())
	}
	return m
}

// ValidityNote is how far the metric m is validated, as a section about it
// alone (its rank table, explain, the catalog) and its docs/metrics.md
// Validity line say it: its validity note, such as the ranked metrics'
// "Beta: not yet checked against a lead's labels", and its reference
// result, "Reference (model panel, …): VERDICT: …", with what the source is
// in plain words.
func ValidityNote(m MetricInfo) string { return validityNote(m.Validity, m.Reference) }

// RankValidity is the validity line of rank table t: "Validity: " and its
// metric's note (ValidityNote), the beta mark with the reference result
// beside it, never in its place (ADR 0014). The table's validity caveat
// says it, and text and Markdown print it under the table's question
// (wave 12), so the verdict stands beside the rows it qualifies wherever
// they are shown. It is "" for a metric with no note.
func RankValidity(t *RankTable) string {
	if note := validityNote(t.Validity, t.Reference); note != "" {
		return "Validity: " + note
	}
	return ""
}

// validityNote is ValidityNote over a metric's note and reference.
func validityNote(validity string, r *Reference) string {
	if r == nil {
		return validity
	}
	return strings.TrimSpace(validity + " Reference (" + r.Source + "): " + r.Result + ". " + r.About)
}

// ValidityCaveats says how far the ranked metrics among ms are validated,
// for an answer that shows their values, ranks or clusters: each one's
// validity note and reference result (ValidityNote). A caveat goes where
// the number it qualifies is (#48), so every answer that shows a ranked
// metric's number carries its note, not its rank table alone (#69).
// Metrics that share a note and a reference's source share one caveat,
// which names them, says the note and the source once and then each one's
// result by name (three ranked metrics, one caveat, not three that
// repeat the mark and what a model panel is); a metric that is not ranked,
// or has no note, gets none, and a section about one metric alone (its
// rank table, explain) words its own.
func ValidityCaveats(ms ...MetricInfo) []Caveat {
	type shared struct{ note, source, about string }
	byNote := map[shared][]MetricInfo{} // what the metrics share → the ranked metrics that share it
	for _, m := range ms {
		if !m.Ranked || m.Validity == "" {
			continue
		}
		k := shared{note: m.Validity}
		if m.Reference != nil {
			k.source, k.about = m.Reference.Source, m.Reference.About
		}
		if !slices.ContainsFunc(byNote[k], func(x MetricInfo) bool { return x.ID == m.ID }) {
			byNote[k] = append(byNote[k], m)
		}
	}
	var out []Caveat
	for k, group := range byNote {
		slices.SortFunc(group, func(a, b MetricInfo) int { return cmp.Compare(a.ID, b.ID) })
		var ids, results []string
		for _, m := range group {
			ids = append(ids, m.ID)
			if m.Reference != nil {
				results = append(results, m.ID+" "+m.Reference.Result)
			}
		}
		msg := "Validity of " + joinNames(ids) + ": "
		switch {
		case len(group) == 1:
			msg += ValidityNote(group[0])
		case k.source == "":
			msg += k.note
		default:
			msg += strings.TrimSpace(k.note + " Reference (" + k.source + "): " + strings.Join(results, "; ") + ". " + k.about)
		}
		out = append(out, Caveat{Code: codeValidity, Message: msg})
	}
	slices.SortFunc(out, func(a, b Caveat) int { return cmp.Compare(a.Message, b.Message) })
	return out
}

// AnswerProvenance says where an answer that covers every repository of fs
// and reads metrics comes from, as every section of a report says for
// itself: forge, the GitHub data the run read, dates its repositories
// whether or not the answer shows a forge number (report-json.md's
// provenance row), and a nil forge means the reader cannot tell. run may
// be nil.
func AnswerProvenance(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metrics []string) Provenance {
	return datedProvenance(run, fs, forge, reposWith(fs, nil), metrics)
}

// ForgeCaveats says what could make the GitHub numbers of an answer over
// every repository of fs wrong, for each of metrics that reads GitHub data
// (ForgeRead.Metrics), as a card says for its own: nil for an answer that
// shows none, since a caveat goes where the number it qualifies is, and
// nil in a run that read no GitHub data at all, which is the git-only case
// the metrics' definitions describe.
func ForgeCaveats(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metrics []string) []Caveat {
	b := answerBuilder(run, fs, forge)
	var out []Caveat
	for _, id := range slices.Compact(slices.Sorted(slices.Values(metrics))) {
		if forge.reads(id) {
			out = append(out, b.forgeCaveats(reposWith(fs, nil), id, false)...)
		}
	}
	return out
}

// ForgeCaveatsAlone is ForgeCaveats for one metric that reads GitHub data,
// as a section about that metric alone says them (forge-not-synced): even
// in a run that read no GitHub data, so that validate and label can say
// why reviews_given@1 scored nothing.
func ForgeCaveatsAlone(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, metric string) []Caveat {
	if !forge.reads(metric) {
		return nil
	}
	return answerBuilder(run, fs, forge).forgeCaveats(reposWith(fs, nil), metric, true)
}

// datedProvenance is the provenance of an answer over repos, whose GitHub
// data forge, what the run read, dates.
func datedProvenance(run *domain.Run, fs *domain.FactSet, forge *ForgeRead, repos []*domain.RepoFacts, metrics []string) Provenance {
	b := answerBuilder(run, fs, forge)
	sorted := slices.Clone(repos)
	slices.SortFunc(sorted, func(x, y *domain.RepoFacts) int {
		return cmp.Or(cmp.Compare(x.Repo.Name, y.Repo.Name), cmp.Compare(x.Repo.Key, y.Repo.Key))
	})
	return b.provenance(sorted, metrics)
}

// answerBuilder is the part of a report builder an answer's provenance and
// caveats read.
func answerBuilder(run *domain.Run, fs *domain.FactSet, forge *ForgeRead) *builder {
	return &builder{in: &Input{Run: run, Facts: fs, Forge: forge}}
}

// AnswerCaveats says what could make an answer drawn from every repository
// of a run wrong.
func AnswerCaveats(fs *domain.FactSet) []Caveat {
	var repos []*domain.RepoFacts
	for i := range fs.Repos {
		repos = append(repos, &fs.Repos[i])
	}
	return answerCaveats(repos)
}

func answerCaveats(repos []*domain.RepoFacts) []Caveat {
	out := []Caveat{notFetched()}
	var all []Caveat
	for _, rf := range repos {
		all = append(all, repoCaveats(rf)...)
	}
	return append(out, summarize(all)...)
}

// forgeScope adds to involved the repositories behind a number of metric in
// scope when the metric reads GitHub data (forge.Metrics): its value in a
// group, or in every repository, counts the reviews in each repository of
// the scope, a review missing from any of them included, so the answer must
// list and date all of them (forgeProvenance), not only those the person
// has code in. A git metric's number is left to its repository-scope
// values, which name the repositories the person's changes are in.
func forgeScope(fs *domain.FactSet, involved map[string]bool, forge *ForgeRead, metric string, scope domain.Scope) {
	if !forge.reads(metric) {
		return
	}
	switch scope.Kind {
	case domain.ScopeRepo:
		involved[scope.Key] = true
	case domain.ScopeGroup:
		for _, g := range fs.Groups {
			if g.Name == scope.Key {
				for _, key := range g.Repos {
					involved[key] = true
				}
			}
		}
	case domain.ScopeAll:
		for i := range fs.Repos {
			involved[fs.Repos[i].Repo.Key] = true
		}
	}
}

// reposWith returns the run's repositories whose keys are in keys, or every
// repository when keys is empty.
func reposWith(fs *domain.FactSet, keys map[string]bool) []*domain.RepoFacts {
	var out []*domain.RepoFacts
	for i := range fs.Repos {
		if len(keys) == 0 || keys[fs.Repos[i].Repo.Key] {
			out = append(out, &fs.Repos[i])
		}
	}
	return out
}

// sameName says when another email in the run has the person's display
// name: if they are one person, their numbers are split between the two.
// It compares the names as the identities command does (People.NameKey),
// a withheld one included, and prints none.
func sameName(fs *domain.FactSet, who People, email string, cmds Commands) (Caveat, bool) {
	if who == nil {
		return Caveat{}, false
	}
	key := who.NameKey(email)
	if key == "" {
		return Caveat{}, false
	}
	seen := map[string]bool{email: true}
	others := 0
	check := func(e string) {
		if !seen[e] {
			seen[e] = true
			if who.NameKey(e) == key {
				others++
			}
		}
	}
	for _, rf := range fs.Repos {
		for _, c := range rf.Commits {
			check(c.Author.Email)
			for _, co := range c.CoAuthors {
				check(co.Email)
			}
		}
		for _, o := range rf.Owners {
			check(o.Author.Email)
		}
	}
	if others == 0 {
		return Caveat{}, false
	}
	return Caveat{
		Code: codeIdentities,
		Message: "Another email in this run has the same display name and was not merged: if it is the same person, " +
			"their numbers are split between the two, and a .mailmap line merges them. " + identitiesHint(cmds),
	}, true
}
