package report

import (
	"fmt"
	"slices"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file dates the GitHub data behind a report (ADR 0004). GitHub is the
// system of record for pull requests and reviews; facts.db holds a replica
// that outlives runs, and a run keeps what it read of it, the watermarks
// and the pull requests (docs/spec/codebase.md#run-model, ADR 0011). So
// every section says when the GitHub data of its repositories was synced,
// and a caveat says where there is none, so a number computed from GitHub
// data is never mistaken for a complete one.

// The caveat codes of GitHub data.
const (
	codeForgeNotSynced = "forge-not-synced" // the section's repositories have no GitHub data, or only part of it
	codePullsNotListed = "pulls-not-listed" // explain: the changes behind a value are pull requests, which it does not list
)

// detailRecentReviews is who_knows@1's Detail key for a person's recent
// reviews: the merged pull requests that changed the path which they
// reviewed in the window, present only when the run has GitHub data for the
// repository. values.go lays out the metric's other keys.
const detailRecentReviews = "recent_reviews"

// ForgeRead is the GitHub data a scan run read from the forge replica: the
// watermark of each forge repository that the origin of one of the run's
// repositories names (domain.ForgeJoin), and the metrics that read it. The
// use case builds it from the watermarks the run keeps
// (domain.RunWatermark), at scan time and for a stored run alike, so a later
// sync changes nothing in it. A nil *ForgeRead means the reader cannot
// tell, as in a week report, and leaves the GitHub provenance and caveats
// out.
type ForgeRead struct {
	Repos []ForgeSync // sorted by Repo, then Key; nil when the run read no GitHub data
	// Metrics are the metric@version of every metric of the registry that
	// reads GitHub data (metrics.Definition.Forge), sorted: the registry's
	// one declaration, which dates and caveats every section that shows
	// one of their numbers, so no second list here can forget one.
	Metrics []string
}

// reads reports whether metric reads GitHub data, as the registry
// declares; false when the reader cannot tell.
func (f *ForgeRead) reads(metric string) bool {
	return f != nil && slices.Contains(f.Metrics, metric)
}

// readAny reports whether the run read GitHub data for any of its
// repositories: a page or a finished sync.
func (f *ForgeRead) readAny() bool {
	return f != nil && slices.ContainsFunc(f.Repos, ForgeSync.hasData)
}

// ForgeSync is one forge repository's watermark as a run read it, for one
// of the run's repositories: how far its pull requests reached in the
// replica, and when they were last synced.
type ForgeSync struct {
	Repo    string     `json:"repo"`    // the forge repository, "owner/name", as the run read it
	Key     string     `json:"key"`     // the Repo.Key of the run's repository whose origin named it when the run read it
	Updated *time.Time `json:"updated"` // every pull request last updated before this was in the replica; null before its first page
	Synced  *time.Time `json:"synced"`  // when the last sync that brought it up to date started: its "as of"; null until a sync finished
}

// hasData reports whether the replica held any GitHub data for the
// repository when the run read it: a page, or a finished sync. A forge
// repository a sync listed but never stored a page of has neither, and
// every part of a report treats it the same way: no GitHub data, so out of
// the provenance, and in the forge-not-synced caveat.
func (s ForgeSync) hasData() bool { return s.Updated != nil || s.Synced != nil }

// ForgeProvenance is the GitHub data behind a section: the watermarks the
// run read for the section's repositories.
type ForgeProvenance struct {
	// AsOf is the oldest Synced among Repos: every number the section
	// computed from GitHub data is at least this current. It is null while
	// the first sync of one of them has not finished.
	AsOf  *time.Time  `json:"as_of"`
	Repos []ForgeSync `json:"repos"` // sorted by repo, then key
	// Metrics are the section's metrics (Provenance.Metrics) that read
	// GitHub data, sorted: the numbers AsOf dates, which text and Markdown
	// date in the section's header. [] when it shows none of them.
	Metrics []string `json:"metrics"`
}

// forgeProvenance is the GitHub data the run read for repos, which dates
// the section's numbers of metrics: nil when the run read none for them, or
// when the reader cannot tell. Its AsOf is the oldest Synced among them,
// because a number over several repositories is only as current as its
// stalest input, as a dataflow operator's output watermark is the minimum
// of its inputs' (Akidau et al., "The Dataflow Model", VLDB 2015).
func (b *builder) forgeProvenance(repos []*domain.RepoFacts, metrics []string) *ForgeProvenance {
	f := b.in.Forge
	if f == nil {
		return nil
	}
	keys := make(map[string]bool, len(repos))
	for _, rf := range repos {
		keys[rf.Repo.Key] = true
	}
	var p ForgeProvenance
	synced := true
	for _, s := range f.Repos {
		if !keys[s.Key] || !s.hasData() {
			continue
		}
		p.Repos = append(p.Repos, s)
		switch {
		case s.Synced == nil:
			synced = false
		case p.AsOf == nil || s.Synced.Before(*p.AsOf):
			p.AsOf = s.Synced
		}
	}
	if len(p.Repos) == 0 {
		return nil
	}
	if !synced {
		p.AsOf = nil
	}
	for _, id := range metrics {
		if f.reads(id) {
			p.Metrics = append(p.Metrics, id)
		}
	}
	return &p
}

// forgeDated reports whether a section's provenance dates a number computed
// from GitHub data: the section shows a metric that reads it, and its
// repositories had some.
func forgeDated(pv Provenance) bool { return pv.Forge != nil && len(pv.Forge.Metrics) > 0 }

// forgeCaveats says what could make a section's numbers of metric, which
// reads GitHub data, wrong: some of members, the repositories the numbers
// count the reviews of, have no GitHub data, or only part of it, so metric
// counts none, or only some, of their reviews. Only a section that shows
// metric's numbers calls it, so a caveat stands where the number it
// qualifies is. A failed repository is left out: its own caveat says it
// adds nothing, and no GitHub data of it counts (domain.ForgeJoin).
//
// own says the section is about metric alone: its rank table, or explain
// of it, which a user asks for by name, so even a run that read no GitHub
// data says so there, with how to get it, and it names every repository of
// the section without GitHub data. Any other section (a card, an overview,
// a who-knows answer, me) shows metric among git numbers. It weighs only the
// repositories with an origin on a forge, the GitHub data a sync could
// copy: a repository without one has none to miss. And in a run that read
// no GitHub data at all it says nothing: that is the git-only case each
// metric's definition describes, and a git-only user gets no GitHub caveat
// on every page. In a run that read some, it names the repositories
// without it, where the numbers would otherwise pass for complete.
func (b *builder) forgeCaveats(members []*domain.RepoFacts, metric string, own bool) []Caveat {
	f := b.in.Forge
	if f == nil {
		return nil
	}
	var ok, missing, partial []string
	syncable := false // a repository without GitHub data has an origin on a forge, which a sync can copy it from
	for _, rf := range members {
		if rf.Status != domain.RepoStatusOK {
			continue
		}
		_, onForge := domain.RemoteRepo(rf.Repo.Remote)
		if !own && !onForge {
			continue
		}
		ok = append(ok, rf.Repo.Name)
		data, synced := false, false
		for _, s := range f.Repos {
			if s.Key == rf.Repo.Key {
				data = data || s.hasData()
				synced = synced || s.Synced != nil
			}
		}
		switch {
		case !data:
			missing = append(missing, rf.Repo.Name)
			syncable = syncable || onForge
		case !synced:
			partial = append(partial, rf.Repo.Name)
		}
	}
	of := fmt.Sprintf("%d", len(ok)) // the repositories weighed: on a forge, unless own
	if !own {
		of += " on a forge"
	}
	switch {
	case len(ok) == 0, len(missing) == len(ok) && !own && !f.readAny():
		return nil
	case len(missing) == len(ok) && syncable:
		return []Caveat{{Code: codeForgeNotSynced, Message: "GitHub data not synced: " + metric +
			" counts no reviews here. 'stillhere sync github --org ORG' copies an organization's pull requests and " +
			"reviews into facts.db, and the next scan reads them."}}
	case len(missing) == len(ok) && own:
		return []Caveat{{Code: codeForgeNotSynced, Message: "No GitHub data: " + metric +
			" counts no reviews here, and no repository here has an origin on a forge that a sync could copy it from."}}
	}
	var out []Caveat
	if len(missing) > 0 {
		msg := fmt.Sprintf("No GitHub data for %s of %s: %s counts none of their reviews.",
			plural(len(missing), "repository", "repositories"), of, metric)
		if len(ok) == 1 {
			msg = "No GitHub data for this repository: " + metric + " counts none of its reviews."
		}
		out = append(out, Caveat{Code: codeForgeNotSynced, Repos: sortedNames(missing), Message: msg})
	}
	if len(partial) > 0 {
		msg := fmt.Sprintf("The first GitHub sync of %s did not finish: %s counts only the pull requests it reached.",
			plural(len(partial), "repository", "repositories"), metric)
		if len(ok) == 1 {
			msg = "The first GitHub sync of this repository did not finish: " + metric + " counts only the pull requests it reached."
		}
		out = append(out, Caveat{Code: codeForgeNotSynced, Repos: sortedNames(partial), Message: msg})
	}
	return out
}

// pullsNotListed is explain's caveat for a value whose changes are pull
// requests, reviews_given@1's: explain lists landings, so it lists none of
// them, although the run keeps them (ADR 0011).
func pullsNotListed(metric string) Caveat {
	return Caveat{Code: codePullsNotListed, Message: "The changes behind " + metric + " are pull requests, which explain " +
		"does not list yet: the values above are the run's own."}
}

// forgeAsOf is the clause that dates a section's GitHub numbers: " · GitHub
// data as of 2026-10-06", " · GitHub data partly synced", or "" when the
// section has none (a caveat then says so).
func forgeAsOf(f *ForgeProvenance) string {
	switch {
	case f == nil:
		return ""
	case f.AsOf == nil:
		return " · GitHub data partly synced"
	}
	return " · GitHub data as of " + day(*f.AsOf)
}

// forgeSummary is the report header's line about GitHub data, over every
// repository's card: "GitHub data as of 2026-10-06, the oldest sync of 3
// repositories", or "" when the report has none.
func forgeSummary(m *Model) string {
	seen := map[string]bool{}
	var oldest *time.Time
	unsynced := 0
	for _, r := range m.Repos {
		f := r.Card.Provenance.Forge
		if f == nil {
			continue
		}
		for _, s := range f.Repos {
			if seen[s.Repo] {
				continue
			}
			seen[s.Repo] = true
			switch {
			case s.Synced == nil:
				unsynced++
			case oldest == nil || s.Synced.Before(*oldest):
				oldest = s.Synced
			}
		}
	}
	switch {
	case len(seen) == 0:
		return ""
	case unsynced > 0:
		return fmt.Sprintf("GitHub data partly synced: the first sync of %d of %s did not finish",
			unsynced, plural(len(seen), "repository", "repositories"))
	}
	return "GitHub data as of " + day(*oldest) + ", the oldest sync of " + plural(len(seen), "repository", "repositories")
}

// sortedNames sorts repository names and drops repeats.
func sortedNames(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}
