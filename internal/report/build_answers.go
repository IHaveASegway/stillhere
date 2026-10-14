package report

import (
	"cmp"
	"slices"

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
}

// NewExplain builds the explain answer: the subject's values and, for a
// ranked metric, each change behind them with its contribution.
func NewExplain(in *ExplainInput) *Explain {
	e := &Explain{
		Metric:  in.Metric,
		Subject: SubjectOf(in.Subject, in.Who),
		Values:  Values(in.Values, in.Who),
	}
	involved := map[string]bool{}
	for _, v := range in.Values {
		if v.Scope.Kind == domain.ScopeRepo {
			involved[v.Scope.Key] = true
		}
	}
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
			ch := Change{Repo: rf.Repo.Name, RepoKey: rf.Repo.Key, Landing: c.Change, Num: c.Num, Den: c.Den}
			if l, ok := landings[c.Change]; ok {
				ch.Time, ch.Merge, ch.Title = l.Time, l.Merge, titles[l.SHA]
			}
			e.Changes = append(e.Changes, ch)
		}
	}
	sortChanges(e.Changes)
	repos := reposWith(in.Facts, involved)
	e.Provenance = answerProvenance(in.Run, in.Facts, repos, []string{in.Metric.ID})
	e.Caveats = answerCaveats(repos)
	if in.Subject.Kind == domain.SubjectPerson {
		if c, ok := sameName(in.Facts, in.Who, in.Subject.Key); ok {
			e.Caveats = append(e.Caveats, c)
		}
	}
	if in.Metric.Validity != "" {
		e.Caveats = append(e.Caveats, Caveat{Code: codeValidity, Message: "Validity: " + in.Metric.Validity})
	}
	return e
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
	Facts *domain.FactSet // the run's facts, for provenance and caveats
	Who   People
	Path  string // repo-relative prefix; "" for the whole repository
	Repo  string // Repo.Key, or "" for every repository
	// Values are who_knows@1's values computed over the facts under Path:
	// one per person and repository (MetricWhoKnows in values.go).
	Values []domain.MetricValue
}

// NewWhoKnows builds the who-knows answer: per repository, the majority
// owners, the top five authors and the recent landers under the path, with
// bots apart (docs/metrics.md#who_knows1-per-path).
func NewWhoKnows(in *WhoKnowsInput) *WhoKnows {
	w := &WhoKnows{Path: in.Path, Repo: in.Repo}
	ix := newValueIndex(in.Values)
	var asked []*domain.RepoFacts
	for i := range in.Facts.Repos {
		rf := &in.Facts.Repos[i]
		if in.Repo != "" && rf.Repo.Key != in.Repo {
			continue
		}
		asked = append(asked, rf)
		r := WhoKnowsRepo{Repo: rf.Repo.Name, Key: rf.Repo.Key}
		var humans []Knower
		for _, v := range ix.people(MetricWhoKnows, domain.Scope{Kind: domain.ScopeRepo, Key: rf.Repo.Key}) {
			email := v.Subject.Key
			k := Knower{
				Email: email, Name: displayName(in.Who, email),
				Lines:          count(v.Value),
				MajorityFiles:  count(v.Detail[detailMajorityFiles]),
				RecentLandings: count(v.Detail[detailRecentLandings]),
			}
			if isBot(in.Who, email) {
				r.Bots = append(r.Bots, k)
			} else {
				humans = append(humans, k)
			}
		}
		for _, k := range humans {
			if k.MajorityFiles > 0 {
				r.MajorityOwners = append(r.MajorityOwners, k)
			}
			if k.Lines > 0 {
				r.TopAuthors = append(r.TopAuthors, k)
			}
			if k.RecentLandings > 0 {
				r.RecentLanders = append(r.RecentLanders, k)
			}
		}
		slices.SortFunc(r.MajorityOwners, func(a, b Knower) int {
			return cmp.Or(cmp.Compare(b.MajorityFiles, a.MajorityFiles), cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.Email, b.Email))
		})
		byLines := func(a, b Knower) int { return cmp.Or(cmp.Compare(b.Lines, a.Lines), cmp.Compare(a.Email, b.Email)) }
		slices.SortFunc(r.TopAuthors, byLines)
		if len(r.TopAuthors) > 5 {
			r.TopAuthors = r.TopAuthors[:5]
		}
		slices.SortFunc(r.RecentLanders, func(a, b Knower) int {
			return cmp.Or(cmp.Compare(b.RecentLandings, a.RecentLandings), cmp.Compare(a.Email, b.Email))
		})
		slices.SortFunc(r.Bots, byLines)
		if len(r.MajorityOwners)+len(r.TopAuthors)+len(r.RecentLanders)+len(r.Bots) > 0 {
			w.Repos = append(w.Repos, r)
		}
	}
	slices.SortFunc(w.Repos, func(a, b WhoKnowsRepo) int { return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key)) })
	w.Provenance = answerProvenance(in.Run, in.Facts, asked, []string{MetricWhoKnows})
	w.Caveats = answerCaveats(asked)
	return w
}

// MeInput is what NewMe builds a person's own page from.
type MeInput struct {
	Run    *domain.Run
	Facts  *domain.FactSet
	Who    People
	Email  string               // canonical
	Values []domain.MetricValue // the person's values
	Ranks  []RankSet            // whole rank tables: the person's place is found in each
}

// NewMe builds a person's own page: their values and their place in every
// rank table they are in.
func NewMe(in *MeInput) *Me {
	m := &Me{
		Person: SubjectOf(domain.Subject{Kind: domain.SubjectPerson, Key: in.Email}, in.Who),
		Values: Values(in.Values, in.Who),
	}
	involved := map[string]bool{}
	metrics := map[string]bool{}
	for _, v := range in.Values {
		metrics[v.Metric.String()] = true
		if v.Scope.Kind == domain.ScopeRepo {
			involved[v.Scope.Key] = true
		}
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
		m.Ranks = append(m.Ranks, MeRank{
			Metric: rs.Metric.ID, Scope: scopeOf(rs.Scope), Window: intervalOf(rs.Window),
			Percent: rs.Percent, MinN: rs.MinN, Eligible: mine.Eligible,
			RankLo: mine.RankLo, RankHi: mine.RankHi, Of: of,
			Estimate: mine.Estimate, Lo: mine.Lo, Hi: mine.Hi, N: mine.N,
		})
	}
	slices.SortFunc(m.Ranks, func(a, b MeRank) int {
		return cmp.Or(cmp.Compare(a.Metric, b.Metric), cmp.Compare(a.Scope.Kind, b.Scope.Kind), cmp.Compare(a.Scope.Key, b.Scope.Key))
	})
	var ids []string
	for id := range metrics {
		ids = append(ids, id)
	}
	repos := reposWith(in.Facts, involved)
	m.Provenance = answerProvenance(in.Run, in.Facts, repos, ids)
	m.Caveats = answerCaveats(repos)
	if c, ok := sameName(in.Facts, in.Who, in.Email); ok {
		m.Caveats = append(m.Caveats, c)
	}
	return m
}

// AnswerProvenance says where an answer that covers repos and reads metrics
// comes from. run may be nil.
func AnswerProvenance(run *domain.Run, fs *domain.FactSet, metrics []string) Provenance {
	var repos []*domain.RepoFacts
	for i := range fs.Repos {
		repos = append(repos, &fs.Repos[i])
	}
	return answerProvenance(run, fs, repos, metrics)
}

func answerProvenance(run *domain.Run, fs *domain.FactSet, repos []*domain.RepoFacts, metrics []string) Provenance {
	b := &builder{in: &Input{Run: run, Facts: fs}}
	sorted := slices.Clone(repos)
	slices.SortFunc(sorted, func(x, y *domain.RepoFacts) int {
		return cmp.Or(cmp.Compare(x.Repo.Name, y.Repo.Name), cmp.Compare(x.Repo.Key, y.Repo.Key))
	})
	return b.provenance(sorted, metrics)
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
func sameName(fs *domain.FactSet, who People, email string) (Caveat, bool) {
	name := displayName(who, email)
	if name == email {
		return Caveat{}, false
	}
	seen := map[string]bool{email: true}
	others := 0
	check := func(e string) {
		if !seen[e] {
			seen[e] = true
			if displayName(who, e) == name {
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
			"their numbers are split between the two, and a .mailmap line merges them.",
	}, true
}
