package report

import (
	"fmt"
	"slices"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// This file dates the GitHub data behind a report (ADR 0004). GitHub is the
// system of record for pull requests and reviews; facts.db holds a replica
// that outlives runs, and a run keeps the watermarks it read rather than a
// copy of the pull requests (docs/spec/codebase.md#run-model). So every
// section says when the GitHub data of its repositories was synced, and a
// caveat says when there is none: a forge number is then absent, never
// zero.

// metricReviewsGiven is the metric that reads the forge replica
// (FactSet.Forge): its numbers are only as current as the GitHub data the
// run read, and absent without it.
const metricReviewsGiven = "reviews_given@1"

// The caveat codes of GitHub data.
const (
	codeForgeNotSynced = "forge-not-synced" // the section's repositories have no GitHub data, or only part of it
	codeForgeNotKept   = "forge-not-kept"   // explain: a run does not keep the pull requests behind a forge metric's value
)

// forgeMetric reports whether a metric reads the forge replica.
func forgeMetric(id string) bool { return id == metricReviewsGiven }

// ForgeRead is the GitHub data a scan run read from the forge replica: the
// watermark of each forge repository that the origin of one of the run's
// repositories names (domain.ForgeJoin). The use case builds it from the
// watermarks the run keeps (domain.RunWatermark), at scan time and for a
// stored run alike, so a later sync changes nothing in it. A nil
// *ForgeRead means the reader cannot tell, as in a week report, and leaves
// the GitHub provenance and caveats out.
type ForgeRead struct {
	Repos []ForgeSync // sorted by Repo, then Key; nil when the run read no GitHub data
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
}

// forgeProvenance is the GitHub data the run read for repos: nil when it
// read none for them, or when the reader cannot tell. Its AsOf is the
// oldest Synced among them, because a number over several repositories is
// only as current as its stalest input, as a dataflow operator's output
// watermark is the minimum of its inputs' (Akidau et al., "The Dataflow
// Model", VLDB 2015).
func (b *builder) forgeProvenance(repos []*domain.RepoFacts) *ForgeProvenance {
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
	return &p
}

// forgeCaveats says what could make the GitHub numbers of a section that
// shows them wrong: some of its repositories have no GitHub data, or only
// part of it, so the forge metrics count none, or only some, of their
// reviews. Only a section that shows a forge metric's numbers calls it (a
// rank table of one, me, explain of one): elsewhere the caveat would
// qualify no number. A failed repository is left out: its own caveat says
// it adds nothing, and no GitHub data of it counts (domain.ForgeJoin).
func (b *builder) forgeCaveats(members []*domain.RepoFacts) []Caveat {
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
			_, onForge := domain.RemoteRepo(rf.Repo.Remote)
			syncable = syncable || onForge
		case !synced:
			partial = append(partial, rf.Repo.Name)
		}
	}
	switch {
	case len(ok) == 0:
		return nil
	case len(missing) == len(ok) && syncable:
		return []Caveat{{Code: codeForgeNotSynced, Message: "GitHub data not synced: " + metricReviewsGiven +
			" has no values. 'stillhere sync github --org ORG' copies an organization's pull requests and reviews into " +
			"facts.db, and the next scan reads them."}}
	case len(missing) == len(ok):
		return []Caveat{{Code: codeForgeNotSynced, Message: "No GitHub data: " + metricReviewsGiven +
			" has no values, and no repository here has an origin on a forge that a sync could copy it from."}}
	}
	var out []Caveat
	if len(missing) > 0 {
		out = append(out, Caveat{Code: codeForgeNotSynced, Repos: sortedNames(missing), Message: fmt.Sprintf(
			"No GitHub data for %s of %d: %s counts none of their reviews.",
			plural(len(missing), "repository", "repositories"), len(ok), metricReviewsGiven)})
	}
	if len(partial) > 0 {
		out = append(out, Caveat{Code: codeForgeNotSynced, Repos: sortedNames(partial), Message: fmt.Sprintf(
			"The first GitHub sync of %s did not finish: %s counts only the pull requests it reached.",
			plural(len(partial), "repository", "repositories"), metricReviewsGiven)})
	}
	return out
}

// forgeNotKept is explain's caveat about a forge metric: the run keeps when
// GitHub was synced, not the pull requests, so the changes behind the value
// cannot be listed from it.
func forgeNotKept(metric string) Caveat {
	return Caveat{Code: codeForgeNotKept, Message: "A run keeps when GitHub was synced, not the pull requests behind " +
		metric + ", so none is listed here: the values above are the run's own."}
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
