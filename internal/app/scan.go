package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Scan runs discovery, reads the forge replica, then runs history,
// classification and blame per repository (each one a bulkhead, every blame
// sharing one pool of --jobs slots), computes every metric and every rank,
// stores it all as one run and publishes the run. It returns
// domain.ErrNoFacts, alongside the report, when no repository produced
// facts; then nothing is stored.
//
// The run is written only after everything is computed, so it is "running"
// for as short a time as possible, and a scan that fails before then leaves
// nothing behind. Readers never see it until FinishRun flips it to complete
// or partial (docs/spec/codebase.md#run-model).
//
// GitHub data comes from the replica that sync github keeps in facts.db
// (Deps.ForgeStore), never from the network (ADR 0004). The scan reads it
// once, before any git process starts. Once collection says which of its
// repositories produced facts, its own part of the replica (the forge
// repositories domain.ForgeJoin joins to them, with their watermarks and
// pull requests) is the run's GitHub data, FactSet.Forge, which the run
// keeps with the watermarks that date it, so every later reader computes
// and dates over what the scan read (ADR 0011). An empty replica is normal:
// the run then has no GitHub data, and every forge-based value is absent,
// never zero. A replica that cannot be read stops the scan: it is the
// facts.db the run would be written to, so the run could most likely not
// be stored either, and a run published without the GitHub data the
// replica holds would read as "not synced", plausible but wrong (fail fast:
// Shore, "Fail Fast", IEEE Software, 2004).
func Scan(ctx context.Context, d Deps, r ScanRequest) (*report.Model, error) {
	d, err := d.ready("app.Scan", true, true, true)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	timings := &report.Timings{Started: started.UTC()}
	scope := r.Scope
	if scope == "" {
		scope = domain.ScopeGroup
	}
	if scope != domain.ScopeGroup && scope != domain.ScopeAll {
		return nil, &domain.UsageError{Msg: fmt.Sprintf("invalid rank scope %q: want group or all", scope)}
	}
	// Resolve --rank before any git process starts: a typo should cost
	// nothing.
	ranked, err := resolveRanked(r.Rank, d.Commands)
	if err != nil {
		return nil, err
	}
	jobs := r.Jobs
	if jobs <= 0 {
		jobs = defaultJobs()
	}

	pl, err := discover(ctx, d, r.Paths, r.Exclude)
	if err != nil {
		return nil, err
	}
	if r.IgnoreRevsFile != "" && len(pl.jobs) > 1 {
		return nil, &domain.UsageError{Msg: fmt.Sprintf(
			"--ignore-revs-file needs a single-repository run, and this one has %d: commit ids belong to one repository", len(pl.jobs))}
	}
	timings.DiscoverMS = time.Since(started).Milliseconds()

	window := domain.Window{Since: r.Now.Add(-scanHistoryDays * 24 * time.Hour), Until: r.Now}
	replica, err := loadReplica(ctx, d, window.Since)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	results, err := collectAll(ctx, pl.jobs, jobs, collector(d, collectOptions{
		mode: collect.HistoryScan, window: window, now: r.Now, ignoreRevsFile: r.IgnoreRevsFile,
		// One slot per running git blame, shared by every repository.
		slots: collect.NewBlameSlots(jobs),
	}))
	if err != nil {
		return nil, err
	}
	timings.CollectMS = time.Since(start).Milliseconds()
	timings.Repos = repoTimings(results)

	fs := factSet(r.Now, d.Policy, pl, results)
	var forge *report.ForgeRead // nil without the replica port: the scan cannot speak for the replica, as a reader without it cannot
	var watermarks []domain.RunWatermark
	if d.ForgeStore != nil {
		forgeDuplicates(fs, replica)
		fs.Forge, watermarks = runForge(fs.Repos, replica)
		forge = forgeRead(watermarks)
		forgeNameWarnings(fs.Repos, fs.Forge, replica)
	}
	// After the run's GitHub data: it links forge logins to the run's people.
	who := identity.New(fs, d.Policy)
	landed := report.Landed{Days: d.Policy.Week.Days, Sort: d.Policy.Week.Sort}
	var activity map[string]domain.Activity // each person's last activity, once the metrics are computed
	var candidates []identity.Candidate     // the run's identity candidates, once they are found
	build := func(run *domain.Run, values []domain.MetricValue, sets []report.RankSet) *report.Model {
		finish(timings, started)
		return report.Build(&report.Input{
			Kind: report.KindScan, Tool: d.Version, Run: run, Facts: fs, Policy: d.Policy, Who: who,
			Values: values, Ranks: sets, Landed: landed, History: window, Timings: timings, Scope: scope,
			Forge: forge, Activity: activity, Candidates: candidates,
		})
	}
	if !produced(fs) {
		return build(nil, nil, nil), domain.ErrNoFacts
	}

	start = time.Now()
	// The run's identity candidates, which the two metrics that count
	// people holding lines merge in their Detail, so every page that shows
	// them can say how far an unmerged identity moves them (#104).
	survey := identity.Candidates(fs, who)
	candidates = survey.Candidates
	d.Log.Debug("identity candidates", "addresses", survey.Addresses, "candidates", len(survey.Candidates))
	in := &metrics.Input{Facts: fs, Policy: d.Policy, Who: who, Days: d.Policy.Week.Days, Candidates: survey.Candidates}
	values, err := metrics.Compute(in)
	if err != nil {
		return nil, fmt.Errorf("computing metrics: %w", err)
	}
	// Per query, as a reader computes it: each person's last activity by
	// knowledge_loss@1's rule, which the pages show beside their lines.
	if activity, err = metrics.LastActivity(in); err != nil {
		return nil, fmt.Errorf("computing last activity: %w", err)
	}
	timings.MetricsMS = time.Since(start).Milliseconds()
	start = time.Now()
	// Every ranked metric is ranked in every scope and stored with the run,
	// whichever ones --rank names, so a reader uses the stored ranks: one
	// rule for every metric, which leaves no metric to forget. A reader
	// bootstraps only a table the run did not store, such as a newer metric
	// version's (rankAll; docs/spec/codebase.md#run-model). --rank picks the
	// tables this report prints.
	all, intervals, err := rankAll(in, allRanked(), rankScopes(scope, fs), nil, nil)
	if err != nil {
		return nil, err
	}
	sets := chosenSets(all, ranked)
	timings.RankMS = time.Since(start).Milliseconds()

	start = time.Now()
	run := domain.Run{
		Kind: domain.RunKindScan, Status: runStatus(fs), Now: r.Now, Started: started.UTC(),
		ToolVersion: d.Version, Policy: d.Policy.Ref(), PolicySource: d.Policy.Source,
		Options: scanOptions(r, d, pl, jobs, scope, ranked),
	}
	id, err := storeRun(ctx, d, run, fs, values, intervals, watermarks)
	timings.StoreMS = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		// The analysis is still worth reading: print it, with no run.
		return build(nil, values, sets), err
	}
	run.ID = id
	d.Log.Info("stored run", "run", id, "status", string(run.Status), "repos", len(fs.Repos),
		"values", len(values), "ranks", len(intervals))
	return build(&run, values, sets), nil
}

// runStatus is complete when every repository and every path produced
// facts, and partial when some failed: pages say so.
func runStatus(fs *domain.FactSet) domain.RunStatus {
	for _, rf := range fs.Repos {
		if rf.Status != domain.RepoStatusOK {
			return domain.RunStatusPartial
		}
	}
	for _, w := range fs.Warnings {
		if w.Code == warnPathFailed {
			return domain.RunStatusPartial
		}
	}
	return domain.RunStatusComplete
}

// scanOptions are the effective options a run records: enough to know how
// it was made, and never a secret. The paths are those discovery resolved
// (plan.paths), not the arguments as typed, and every value is scrubbed, so
// a token pasted into an argument reaches neither facts.db nor a report.
// The ranked metrics are recorded with the versions they resolved to, so a
// later report ranks the same versions.
func scanOptions(r ScanRequest, d Deps, pl *plan, jobs int, scope string, ranked []rankedMetric) map[string]string {
	opts := map[string]string{
		"jobs":         strconv.Itoa(jobs),
		"scope":        scope,
		"history_days": strconv.Itoa(scanHistoryDays),
		"paths":        strings.Join(pl.paths, "\n"),
		"policy":       d.Policy.Name,
	}
	if len(ranked) > 0 {
		var ids []string
		for _, m := range ranked {
			ids = append(ids, m.def.ID.String())
		}
		opts["rank"] = strings.Join(ids, ",")
	}
	if len(r.Exclude) > 0 {
		opts["exclude_repo"] = strings.Join(r.Exclude, "\n")
	}
	if r.IgnoreRevsFile != "" {
		opts["ignore_revs_file"] = r.IgnoreRevsFile
	}
	for k, v := range opts {
		opts[k] = redact.Scrub(v)
	}
	return opts
}

// storeRun writes a run and publishes it with run.Status. If any write
// fails, the run is marked failed, which hides it from every reader, even
// when ctx was cancelled. The GitHub data the run read, its watermarks and
// its pull requests, is recorded while it is still running, so no reader
// sees the run without it.
func storeRun(ctx context.Context, d Deps, run domain.Run, fs *domain.FactSet,
	values []domain.MetricValue, ranks []domain.RankInterval, watermarks []domain.RunWatermark) (int64, error) {
	s := d.Store
	status := run.Status
	run.Status = domain.RunStatusRunning
	id, err := s.BeginRun(ctx, run)
	if err != nil {
		return 0, fmt.Errorf("storing the run: %w", err)
	}
	abort := func(err error) (int64, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		defer cancel()
		if ferr := s.FinishRun(fctx, id, domain.RunStatusFailed); ferr != nil {
			err = errors.Join(err, fmt.Errorf("marking run %d failed: %w", id, ferr))
		}
		return 0, err
	}
	for i := range fs.Repos {
		if err := s.PutRepoFacts(ctx, id, &fs.Repos[i]); err != nil {
			return abort(fmt.Errorf("storing %s: %w", fs.Repos[i].Repo.Name, err))
		}
	}
	if err := s.PutRunFacts(ctx, id, fs.Groups, fs.Warnings); err != nil {
		return abort(fmt.Errorf("storing run %d's groups and warnings: %w", id, err))
	}
	// Watermarks and pull requests come only from a replica the ForgeStore
	// loaded.
	if len(watermarks) > 0 && d.ForgeStore != nil {
		if err := d.ForgeStore.PutRunWatermarks(ctx, id, watermarks); err != nil {
			return abort(fmt.Errorf("storing run %d's forge watermarks: %w", id, err))
		}
		if err := d.ForgeStore.PutRunForge(ctx, id, fs.Forge.Repos, fs.Forge.PullRequests, fs.Forge.Links); err != nil {
			return abort(fmt.Errorf("storing run %d's GitHub data: %w", id, err))
		}
	}
	if err := s.PutMetrics(ctx, id, values, ranks); err != nil {
		return abort(fmt.Errorf("storing run %d's metrics: %w", id, err))
	}
	if err := s.FinishRun(ctx, id, status); err != nil {
		return abort(fmt.Errorf("publishing run %d: %w", id, err))
	}
	return id, nil
}

// loadReplica reads the forge replica a scan computes over (FactSet.Forge):
// every repository and watermark, and the pull requests updated since the
// start of the history window, which holds every metric's window. It returns
// nil without the replica port, and for an empty replica: the run then has
// no GitHub data.
func loadReplica(ctx context.Context, d Deps, since time.Time) (*domain.ForgeFacts, error) {
	if d.ForgeStore == nil {
		return nil, nil
	}
	f, err := d.ForgeStore.LoadForge(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub data in facts.db, before collecting anything: %w", err)
	}
	if f == nil {
		d.Log.Info("no GitHub data in facts.db: forge-based metrics get no values ('stillhere sync github' copies it)")
		return nil, nil
	}
	d.Log.Info("read the GitHub data in facts.db", "forge_repos", len(f.Repos), "pull_requests", len(f.PullRequests))
	return f, nil
}

// runForge returns the run's GitHub data, FactSet.Forge, and the watermarks
// that date it (ForgeStore.PutRunWatermarks), both cut from the replica the
// scan read by the one join (domain.ForgeJoin) of its forge repositories to
// the repositories it collected (repos), so what the metrics compute over,
// what the run keeps (PutRunForge) and what its watermarks date are the
// same repositories. The replica's other repositories, such as another
// organization's, are none of the run's business, and neither is a
// repository whose collection failed. A forge repository the replica holds
// no watermark for gets a zero one: the run read none of its pull requests.
// Both are nil when no forge repository is the run's.
func runForge(repos []domain.RepoFacts, replica *domain.ForgeFacts) (*domain.ForgeFacts, []domain.RunWatermark) {
	if replica == nil {
		return nil, nil
	}
	join := domain.ForgeJoin(repos, replica.Repos)
	aliasJoinsShared(join, repos, replica)
	if len(join) == 0 {
		return nil, nil
	}
	marks := make(map[string]domain.ForgeWatermark, len(replica.Watermarks))
	for _, wm := range replica.Watermarks {
		marks[wm.Repo] = wm
	}
	// The join travels with the data: the metrics count by it, and the
	// watermarks record it (their Keys), so the run has one join, made here.
	own := &domain.ForgeFacts{Join: join}
	var watermarks []domain.RunWatermark
	for _, r := range replica.Repos {
		keys := join[r.ID]
		if len(keys) == 0 {
			continue
		}
		wm, ok := marks[r.ID]
		if !ok {
			wm = domain.ForgeWatermark{Repo: r.ID}
		}
		own.Repos = append(own.Repos, r)
		own.Watermarks = append(own.Watermarks, wm)
		watermarks = append(watermarks, domain.RunWatermark{ForgeWatermark: wm, Name: r.Owner + "/" + r.Name, Keys: keys})
	}
	for i := range replica.PullRequests {
		if p := &replica.PullRequests[i]; len(join[p.Repo]) > 0 {
			own.PullRequests = append(own.PullRequests, *p)
		}
	}
	// The older pull requests' links to accounts, which no metric reads and
	// identity's same-forge-account rule does (#103): GitHub's attribution
	// of an address does not age, so a run keeps it beside the pull
	// requests in its window, as ADR 0011 widens what a scan reads; one
	// link for each repository, address and account (identity.KeptLinks),
	// so what a run keeps is bounded by the people its history shows.
	var links []domain.ForgeLink
	for _, l := range replica.Links {
		if len(join[l.Repo]) > 0 {
			links = append(links, l)
		}
	}
	own.Links = identity.KeptLinks(&domain.FactSet{Repos: repos}, links)
	// The order ForgeFacts documents, which RunForge reads back: repositories
	// as the replica sorted them, watermarks by repository, pull requests by
	// repository, then number, as the replica sorted them.
	slices.SortFunc(own.Watermarks, func(a, b domain.ForgeWatermark) int { return cmp.Compare(a.Repo, b.Repo) })
	slices.SortFunc(watermarks, func(a, b domain.RunWatermark) int { return cmp.Compare(a.Repo, b.Repo) })
	return own, watermarks
}

// The warnings a scan gives a repository whose origin names a GitHub
// repository by a name GitHub moved it from (round 3's KT-2).
const (
	// warnForgeMoved: the origin names an old name of the forge repository
	// its GitHub data was joined by (domain.ForgeRepo.Aliases).
	warnForgeMoved = "forge-moved"
	// warnForgeUnmatched: no forge repository is the repository's, while
	// the replica holds GitHub data of one whose history the clone shares.
	warnForgeUnmatched = "forge-unmatched"
	// warnDuplicate is discovery's code (collect.Discovery.Warnings): a
	// scan skips a clone whose origin names, by an old name GitHub
	// resolves to it, the repository another clone's origin names now.
	warnDuplicate = "duplicate-repo"
)

// forgeDuplicates leaves out of the run each collected clone whose origin
// names a forge repository by one of its aliases while another repository
// of the run names it by its current name, as discovery skips a second
// clone of one remote (docs/spec/discovery.md, rule 5): both are one
// repository, which GitHub resolves both names to, and counting both would
// count its commits, lines and pull requests twice (wave 11's review: one
// scan of an old name's clone beside the new name's mirror gave 124 of 160
// reviewed, each clone's 62 of 80). It runs once collection has said what
// each clone holds, since an alias joins only a clone that shares the
// repository's history (aliasJoinsShared). The kept repository carries a
// duplicate-repo warning naming both paths, the skipped clone's origin and
// GitHub's name. Two clones of one name in two forms (SSH and HTTPS) stay
// two repositories, as discovery keeps them.
func forgeDuplicates(fs *domain.FactSet, replica *domain.ForgeFacts) {
	if replica == nil || len(fs.Repos) < 2 {
		return
	}
	join := domain.ForgeJoin(fs.Repos, replica.Repos)
	aliasJoinsShared(join, fs.Repos, replica)
	at := make(map[string]int, len(fs.Repos)) // Repo.Key → index in fs.Repos
	for i := range fs.Repos {
		at[fs.Repos[i].Repo.Key] = i
	}
	skip := map[string]bool{}
	for _, r := range replica.Repos {
		now, ok := domain.RemoteRepo(r.Key)
		if !ok || len(join[r.ID]) < 2 {
			continue
		}
		var current, old []int
		for _, key := range join[r.ID] {
			if origin, _ := domain.RemoteRepo(fs.Repos[at[key]].Repo.Remote); origin == now {
				current = append(current, at[key])
			} else {
				old = append(old, at[key])
			}
		}
		if len(current) == 0 || len(old) == 0 {
			continue
		}
		kept := &fs.Repos[slices.MinFunc(current, func(a, b int) int {
			return cmp.Compare(fs.Repos[a].Repo.Path, fs.Repos[b].Repo.Path)
		})]
		var dups []domain.Warning
		for _, i := range old {
			o := fs.Repos[i].Repo
			skip[o.Key] = true
			dups = append(dups, domain.Warning{Code: warnDuplicate, Message: fmt.Sprintf(
				"skipped %s; its origin (%s) names a repository GitHub now calls %s/%s, the remote of %s (%s)",
				o.Path, o.Remote, r.Owner, r.Name, kept.Repo.Path, kept.Repo.Remote)})
		}
		kept.Warnings = repoWarnings(kept.Repo, kept.Warnings, dups)
	}
	if len(skip) == 0 {
		return
	}
	fs.Repos = slices.DeleteFunc(fs.Repos, func(rf domain.RepoFacts) bool { return skip[rf.Repo.Key] })
	for i := range fs.Groups {
		fs.Groups[i].Repos = slices.DeleteFunc(slices.Clone(fs.Groups[i].Repos), func(k string) bool { return skip[k] })
	}
}

// forgeNameWarnings gives each collected repository whose origin names a
// GitHub repository by another name than the replica's a warning, which
// the run keeps and every section with its numbers says (its card's
// caveats): forge-moved when the run's join (own, runForge's) took its
// GitHub data by a name GitHub moved the repository from, with the command
// that names it as GitHub does now; and forge-unmatched when no forge
// repository joined it while the replica holds GitHub data of one whose
// history it shares (sharedHistory) and that no other repository of the
// run is a clone of, as a sync that copied the repository under its new
// name before GitHub's move was recorded leaves it, with the sync that
// records the move. Round 3's KT-2: a clone of a moved repository, scanned
// with 535 pull requests synced under its new owner's name, showed no
// review coverage and said nothing.
//
// Wave 11's review: a name alone says nothing. A repository of the same
// name under another owner is as often an unrelated one (anyone who syncs
// an organization has its docs, cli or homebrew-tap), and a rename under
// the same owner keeps no name at all; the commits are what a moved
// repository and its clone share. So is a fork's history, and only GitHub's
// answer to a sync of the origin's name tells a fork from a move, so the
// warning gives that command, and no git remote set-url, which would point
// a fork's clone at its upstream.
func forgeNameWarnings(repos []domain.RepoFacts, own, replica *domain.ForgeFacts) {
	if replica == nil {
		return
	}
	joined := map[string]domain.ForgeRepo{} // Repo.Key → the forge repository joined to it
	if own != nil {
		for _, r := range own.Repos {
			for _, key := range own.Join[r.ID] {
				joined[key] = r
			}
		}
	}
	var named map[string][]string // SHA → the unjoined forge repositories whose GitHub data names it; built once, when needed
	for i := range repos {
		rf := &repos[i]
		origin, ok := domain.RemoteRepo(rf.Repo.Remote)
		if rf.Status == domain.RepoStatusError || !ok {
			continue
		}
		host, path, _ := strings.Cut(origin, "/")
		var w domain.Warning
		if r, ok := joined[rf.Repo.Key]; ok {
			now, _ := domain.RemoteRepo(r.Key)
			if now == origin {
				continue
			}
			w = domain.Warning{Code: warnForgeMoved, Message: fmt.Sprintf("Its origin names %s, which GitHub now calls "+
				"%s/%s: the GitHub data synced under that name is this repository's, joined by the old name, which GitHub "+
				"resolves to it. 'git remote set-url origin %s' names it as GitHub does, before another repository can take "+
				"the old name and end GitHub's redirect.", path, r.Owner, r.Name, renamedRemote(rf.Repo.Remote, r.Owner, r.Name))}
		} else {
			if named == nil {
				// A forge repository the run joined is the repository of the
				// run it joined, whose own clone the run holds: another clone
				// sharing its history is a fork or a copy, not the clone of a
				// moved repository.
				named = forgeCommits(replica, func(id string) bool { return own != nil && len(own.Join[id]) > 0 })
			}
			same := sharedHistory(rf, host, named, replica)
			if len(same) == 0 {
				continue
			}
			w = domain.Warning{Code: warnForgeUnmatched, Message: fmt.Sprintf("No GitHub data in facts.db is this "+
				"repository's: its origin names %s, while facts.db holds the GitHub data of %s, whose history this clone "+
				"shares. If GitHub moved or renamed the repository, 'stillhere sync github --repo %s' records the move and "+
				"the next scan joins it; if this clone is a fork's, the same command syncs the fork's own.",
				path, joinNames(same), path)}
		}
		rf.Warnings = repoWarnings(rf.Repo, rf.Warnings, []domain.Warning{w})
	}
}

// aliasJoinsShared drops from join each repository that it joined by an
// alias of a forge repository (domain.ForgeRepo.Aliases) and whose facts
// hold no commit that repository's GitHub data names (forgeCommits): the
// name has gone to another repository since a sync recorded it, as when
// the old owner made a new one under it, which ends GitHub's redirect. A
// clone of that new repository shares no history with the old one, and
// its origin is no clone of the data (wave 11's review: a recorded alias
// joined an unrelated clone whose origin named the old name to the moved
// repository's pull requests). A repository joined by a forge
// repository's own name is joined by GitHub's word for it, and stays.
func aliasJoinsShared(join map[string][]string, repos []domain.RepoFacts, replica *domain.ForgeFacts) {
	byKey := make(map[string]*domain.RepoFacts, len(repos))
	for i := range repos {
		byKey[repos[i].Repo.Key] = &repos[i]
	}
	var named map[string][]string // built once, when some join is by an alias
	for _, r := range replica.Repos {
		keys, ok := join[r.ID]
		if !ok {
			continue
		}
		now, _ := domain.RemoteRepo(r.Key)
		var kept []string
		for _, key := range keys {
			rf := byKey[key]
			if origin, _ := domain.RemoteRepo(rf.Repo.Remote); origin != now {
				if named == nil {
					named = forgeCommits(replica, nil)
				}
				if !heldForge(rf, named)[r.ID] {
					continue
				}
			}
			kept = append(kept, key)
		}
		if len(kept) == 0 {
			delete(join, r.ID)
		} else {
			join[r.ID] = kept
		}
	}
}

// forgeCommits indexes the commits the replica's GitHub data names, by
// SHA, to the forge repositories that name them: each pull request's merge
// commit and own commits, and the commit of each link to an account
// (domain.ForgeFacts.Links: the newest commit of each author email of the
// mirror's history, among them). A forge repository leave reports (nil:
// none) is left out.
func forgeCommits(replica *domain.ForgeFacts, leave func(id string) bool) map[string][]string {
	out := map[string][]string{}
	add := func(sha, repo string) {
		if sha == "" || leave != nil && leave(repo) {
			return
		}
		if ids := out[sha]; !slices.Contains(ids, repo) {
			out[sha] = append(ids, repo)
		}
	}
	for i := range replica.PullRequests {
		p := &replica.PullRequests[i]
		add(p.MergeCommit.SHA, p.Repo)
		for _, c := range p.Commits {
			add(c.SHA, p.Repo)
		}
	}
	for _, l := range replica.Links {
		add(l.SHA, l.Repo)
	}
	return out
}

// heldForge returns the forge repositories, by ID, whose GitHub data names
// a commit rf holds (named, forgeCommits): one of its history, a blamed
// line's or another ref's. A commit id names one commit, so a repository
// with none in common shares no history.
func heldForge(rf *domain.RepoFacts, named map[string][]string) map[string]bool {
	found := map[string]bool{}
	see := func(sha string) {
		for _, id := range named[strings.ToLower(sha)] {
			found[id] = true
		}
	}
	for j := range rf.Commits {
		see(rf.Commits[j].SHA)
	}
	for j := range rf.Owners {
		see(rf.Owners[j].Commit)
	}
	for j := range rf.RefCommits {
		see(rf.RefCommits[j].SHA)
	}
	return found
}

// sharedHistory returns, as "owner/name" and sorted, the forge repositories
// on host whose GitHub data names a commit rf holds (heldForge).
func sharedHistory(rf *domain.RepoFacts, host string, named map[string][]string, replica *domain.ForgeFacts) []string {
	found := heldForge(rf, named)
	var names []string
	for _, r := range replica.Repos {
		if have, ok := domain.RemoteRepo(r.Key); ok && found[r.ID] && strings.HasPrefix(have, host+"/") {
			names = append(names, r.Owner+"/"+r.Name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// renamedRemote is remote, a scrubbed origin that domain.RemoteRepo reads,
// naming owner/name instead, in the remote's own form: an scp-like remote
// stays scp-like, user included, a URL keeps its scheme, host and port, and
// a trailing ".git" stays when it had one. Scrubbing leaves a URL's user
// out (redact.URL), which an ssh:// URL needs: GitHub's SSH user is git,
// so it gets that one back. Wave 11's review: GitHub's HTTPS clone URL in
// its place would move an SSH user's pushes to HTTPS credentials.
func renamedRemote(remote, owner, name string) string {
	suffix := ""
	if strings.HasSuffix(strings.TrimRight(remote, "/"), ".git") {
		suffix = ".git"
	}
	if scheme, rest, ok := strings.Cut(remote, "://"); ok {
		authority, _, _ := strings.Cut(rest, "/")
		if strings.HasSuffix(strings.ToLower(scheme), "ssh") && !strings.Contains(authority, "@") {
			authority = "git@" + authority
		}
		return scheme + "://" + authority + "/" + owner + "/" + name + suffix
	}
	hostPart, _, _ := strings.Cut(remote, ":")
	return hostPart + ":" + owner + "/" + name + suffix
}

// joinNames is "a", "a and b", "a, b and c": names in a sentence.
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// forgeRead dates a run's GitHub data for the report: one ForgeSync for
// each watermark the run kept and each of its repositories the watermark
// joined, sorted by forge repository, then key, with the metrics that read
// it, as the registry declares them. The scan and every reader of the
// stored run build it from the same watermarks, the run's own, so a stored
// run dates its GitHub data exactly as its scan did, whatever a later sync
// renamed.
func forgeRead(watermarks []domain.RunWatermark) *report.ForgeRead {
	read := &report.ForgeRead{Metrics: forgeMetrics()}
	for _, wm := range watermarks {
		for _, key := range wm.Keys {
			read.Repos = append(read.Repos, report.ForgeSync{Repo: wm.Name, Key: key,
				Updated: instant(wm.Updated), Synced: instant(wm.Synced), From: instant(wm.From),
				Missing: wm.Missing, Pending: wm.Pending})
		}
	}
	slices.SortFunc(read.Repos, func(a, b report.ForgeSync) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key))
	})
	return read
}

// forgeMetrics lists, sorted, the metric@version of every metric in the
// registry that reads GitHub data (metrics.Definition.Forge): the one
// declaration the report dates and caveats their numbers by.
func forgeMetrics() []string {
	var out []string
	for _, m := range metrics.All() {
		if def := m.Definition(); def.Forge {
			out = append(out, def.ID.String())
		}
	}
	slices.Sort(out)
	return out
}

// instant is t in UTC, or nil for the zero time, which a watermark uses for
// "not yet".
func instant(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// chosenSets keeps the rank tables of the metrics --rank named, in the
// order named, each in its scopes' order.
func chosenSets(sets []report.RankSet, named []rankedMetric) []report.RankSet {
	var out []report.RankSet
	for _, m := range named {
		id := m.def.ID.String()
		for _, s := range sets {
			if s.Metric.ID == id {
				out = append(out, s)
			}
		}
	}
	return out
}
