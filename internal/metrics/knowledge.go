package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// knowledgeLossV1 is knowledge_loss@1: the share of an area's surviving prod
// lines held by people who have had no activity anywhere in the scanned
// organization for 180 days: no landing, review, pull request or commit on
// any ref of a clone (activity.go). Knowledge is held by people, so lines
// credited to bots are in neither part of the share.
type knowledgeLossV1 struct{}

var knowledgeLossID = domain.MetricID{Name: "knowledge_loss", Version: 1}

// detailGoingQuietNum is knowledge_loss@1's Detail of the lines whose people
// go quiet within 30 days, which every value has since wave 10 (Corrected).
const detailGoingQuietNum = "going_quiet_num"

// Definition returns knowledge_loss@1's catalog entry.
func (knowledgeLossV1) Definition() Definition {
	return Definition{
		ID:       knowledgeLossID,
		Question: "How much of the code is held by people who have gone quiet?",
		Unit: "Num / Den per repository (subject repo), per top-level directory (subject area, \"<repo key>:<dir>/\") and per " +
			"group (subject group): Den is the surviving prod lines credited to people (bots excluded), Num the part credited to " +
			"people with no activity in [now - 180d, now) in any scanned repository. A person has had activity when a commit " +
			"credited to them, merge commits included, is in a landing in the window, so a side-branch author is active when " +
			"the branch lands; when they submitted a review (approved, changes requested or commented) in the window on any " +
			"pull request of the scanned repositories, open, closed or merged, in the GitHub data the run read; when they " +
			"opened a pull request there in the window, or a commit of theirs on one of their own pull requests, which the " +
			"clone holds, was written in it (its author time; an update by anyone else is not theirs); or when a commit " +
			"credited to them on another ref of a clone, a branch, a remote-tracking branch or a tag, was written in the " +
			"window (its author time, committed before now). A GitHub account is the person the run links its login to " +
			"(identity.Directory.Reviewer). Detail: people (humans holding lines), people_inactive, going_quiet_num and " +
			"going_quiet_people (the lines and the people of Den whose last activity is between 150 and 180 days before " +
			"now: they cross the line within 30 days unless they are active again); and, when identity candidates hold " +
			"lines in the area, identity_candidates, identity_candidates_high, and if_merged_num and if_all_merged_num, " +
			"Num with the high-confidence lines (identity.Candidate.High) and with every candidate merged into one person each.",
		Ratio:       true,
		Ranked:      false,
		Forge:       true,
		Subjects:    []string{domain.SubjectRepo, domain.SubjectArea, domain.SubjectGroup},
		Eligibility: "Not ranked: it describes code, not people.",
		BlindSpots: "People who moved to repositories outside the scan look gone, and so does work on a fork's branch, " +
			"which no clone of the scan holds. Mailmap gaps split one person in two and can make either half look gone: a " +
			"commit on another branch counts for the address it was written with. Reviews and pull requests count only in " +
			"repositories whose GitHub data the run read, and only for an account the run links to an address (a login " +
			"GitHub attributes a commit of the run's to): an account shown as @login holds no line. Without GitHub data " +
			"the value counts landings and commits on the clone's refs only. Blame's blind spots apply.",
		Gaming:   "Not a target for people; a quiet expert who lands one trivial change, submits one review, opens a pull request or pushes a commit to any branch resets their clock.",
		Oracle:   "testdata/fixture/SPEC.md#knowledge_loss1-main-fixture and #reviews-in-knowledge_loss1-and-who_knows1-main-fixture",
		Validity: knowledgeLossValidity,
	}
}

// knowledgeLossValidity is the prior art for blame-based knowledge loss.
const knowledgeLossValidity = "Context, unvalidated. Prior art measures the same thing: Rigby, Zhu, Donadelli and Mockus, \"Quantifying " +
	"and Mitigating Turnover-Induced Knowledge Loss\" (ICSE 2016), used files abandoned by departed developers as the proxy for " +
	"knowledge loss at Chrome and Avaya, and CodeScene's knowledge-loss view (Tornhill, Software Design X-Rays, 2018) does it " +
	"with history. Here inactivity stands in for departure, which it is not."

// Compute returns knowledge_loss@1's values.
func (m knowledgeLossV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (knowledgeLossV1) compute(v *view) ([]domain.MetricValue, error) {
	w := v.window(activityDays, 0)
	active, last := knowledgeActive(v, w)
	var out []domain.MetricValue
	add := func(s domain.Scope, sub domain.Subject, files []*fileView) {
		if val, ok := knowledgeLoss(v, active, last, s, sub, w, files); ok {
			out = append(out, val)
		}
	}
	groups := make(map[string][]*fileView)
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		files := r.sortedFiles()
		repoScope := domain.Scope{Kind: domain.ScopeRepo, Key: r.key}
		add(repoScope, repoSubject(r.key), files)
		for _, a := range byTopDir(files) {
			add(repoScope, areaSubject(r.key, a.dir), a.files)
		}
		if r.group != "" {
			groups[r.group] = append(groups[r.group], files...)
		}
	}
	for name, files := range groups {
		add(domain.Scope{Kind: domain.ScopeGroup, Key: name}, groupSubject(name), files)
	}
	return sorted(out), nil
}

// Corrected returns the metrics whose values among values, a stored run's,
// a stillhere computed before a correction this one made to their
// definitions in place (before the first release a definition is
// corrected keeping its version: docs/metrics.md's changelogs), so a reader
// computes them again from the run's facts rather than show them beside
// what it computes per query by the corrected rules. A run is told by its
// data, never by a version: knowledge_loss@1's values carry going_quiet
// since activity off the default branch was added (wave 10), so one
// without it was stored under the earlier activity rule, landings and
// reviews only, which per-query last activity and PathKnowledge no longer
// follow; knowledge_concentration@1 comes with it, since that wave also
// made GitHub's link high for an address whose names differ, which moves
// both metrics' identity detail. None for a run that holds no
// knowledge_loss@1 value, whose readers show none.
func Corrected(values []domain.MetricValue) []domain.MetricID {
	for _, v := range values {
		if v.Metric != knowledgeLossID {
			continue
		}
		if _, ok := v.Detail[detailGoingQuietNum]; !ok {
			return []domain.MetricID{knowledgeConcentrationID, knowledgeLossID}
		}
		return nil
	}
	return nil
}

// knowledgeActive returns the people knowledge_loss@1 counts as active in
// w, anywhere in the view, with each person's last activity: those whose
// last activity (view.lastActivity, the one rule) is in w. w ends at Now
// and starts inside the history window, so a person is active exactly when
// something they did falls in w.
func knowledgeActive(v *view, w domain.Window) (map[string]bool, map[string]domain.Activity) {
	last := v.lastActivity()
	active := make(map[string]bool, len(last))
	for email, a := range last {
		if w.Contains(a.Time) {
			active[email] = true
		}
	}
	return active, last
}

// PathKnowledge answers knowledge_loss@1 and knowledge_concentration@1 for
// the code under a repo-relative path prefix, such as "pkg", "pkg/sync" or
// "pkg/sync/pool.go" ("" or "." for the whole repository), in the
// repository whose Repo.Key is repo, or in each repository when repo is "".
// A prefix matches whole segments, as who_knows@1's does, and repo is
// matched as WhoKnows matches it. A scan stores both metrics per top-level
// directory only (docs/metrics.md#units, "Area"), since every directory of
// every repository would be more rows than the facts; this computes them
// per query, for any prefix, so who-knows and the MCP server can drill
// into a dominant area such as pkg/ without a second implementation of
// either metric. It is the computation a scan stores (knowledgeLoss and
// concentration over the prefix's files, activity read over every
// repository of the run), so a top-level directory's answer is its stored
// area value, and the root's is the repository's.
//
// Values are about the prefix (subject repo for the root, else area,
// keyed "<repo key>:<file>" or "<repo key>:<dir>/"), in the scope of their
// repository: one of each metric where it has a value, so a prefix that
// holds no prod line with an owner gives none, and a repository without
// blame gives none.
func PathKnowledge(in *Input, repo, prefix string) ([]domain.MetricValue, error) {
	if in == nil || in.Facts == nil {
		return nil, errNoFacts
	}
	key, err := repoKey(in.Facts, repo)
	if err != nil {
		return nil, err
	}
	// Every repository: knowledge_loss@1's activity is anywhere in the run.
	v, err := newView(in, nil)
	if err != nil {
		return nil, err
	}
	clean := cleanPrefix(prefix)
	under := underPrefix(clean)
	w := v.window(activityDays, 0)
	active, last := knowledgeActive(v, w)
	var out []domain.MetricValue
	for _, r := range v.repos {
		if (key != "" && r.key != key) || !r.lines {
			continue
		}
		var files []*fileView
		for _, f := range r.sortedFiles() {
			if under(f.path) {
				files = append(files, f)
			}
		}
		scope, sub := domain.Scope{Kind: domain.ScopeRepo, Key: r.key}, pathSubject(r, clean)
		if val, ok := knowledgeLoss(v, active, last, scope, sub, w, files); ok {
			out = append(out, val)
		}
		if val, ok := concentration(v, scope, sub, files); ok {
			out = append(out, val)
		}
	}
	return sorted(out), nil
}

// knowledgeLoss computes the share over files. ok is false when no person
// holds a line there, which leaves a share of nothing.
func knowledgeLoss(v *view, active map[string]bool, last map[string]domain.Activity, s domain.Scope, sub domain.Subject,
	w domain.Window, files []*fileView) (domain.MetricValue, bool) {
	held, gone, people, inactive := lossCounts(v, active, files)
	if held == 0 {
		return domain.MetricValue{}, false
	}
	quietLines, quietPeople := goingQuiet(v, last, files)
	detail := map[string]float64{
		"people":             float64(people),
		"people_inactive":    float64(inactive),
		detailGoingQuietNum:  float64(quietLines),
		"going_quiet_people": float64(quietPeople),
	}
	v.lossIfMerged(detail, active, files) // #104: identities.go
	return ratio(knowledgeLossID, s, sub, w, gone, held, 0, detail), true
}

// goingQuiet counts, over files, the lines held by people who are active
// now but within goingQuietDays of the activity line (domain.Activity's
// GoingQuiet, which view.quietIn sets), and those people: Num grows by
// these lines within goingQuietDays unless they are active again. A page
// warns of it before the line is crossed.
func goingQuiet(v *view, last map[string]domain.Activity, files []*fileView) (lines, people int) {
	seen := make(map[string]bool)
	for _, f := range files {
		for email, n := range f.byPerson {
			if v.who.Bot(email) || !last[email].GoingQuiet {
				continue
			}
			lines += n
			if !seen[email] {
				seen[email] = true
				people++
			}
		}
	}
	return lines, people
}

// lossCounts counts, over files, the lines held by people (bots left out),
// those held by people with no activity, and the people and inactive
// people who hold them: knowledge_loss@1's parts, which lossIfMerged also
// counts with identity candidates merged.
func lossCounts(v *view, active map[string]bool, files []*fileView) (held, gone, people, inactive int) {
	seen := make(map[string]bool)
	for _, f := range files {
		for email, n := range f.byPerson {
			if v.who.Bot(email) {
				continue
			}
			held += n
			if !active[email] {
				gone += n
			}
			if !seen[email] {
				seen[email] = true
				people++
				if !active[email] {
					inactive++
				}
			}
		}
	}
	return held, gone, people, inactive
}
