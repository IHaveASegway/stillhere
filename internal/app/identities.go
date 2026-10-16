package app

import (
	"context"
	"fmt"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/identity"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// IdentitiesRequest asks for the identity candidates of a stored run.
type IdentitiesRequest struct {
	RunID int64        // 0: the latest complete or partial scan run
	Scope domain.Scope // the zero Scope: every repository of the run; else a group or one repository
	// Mailmap adds each candidate's suggested .mailmap lines, which are
	// made of emails: only for a reader that may see them (the MCP server
	// with --allow-emails); the identities command prints them with
	// --format mailmap instead.
	Mailmap bool
}

// Identities suggests .mailmap merges for the people of a stored run, each
// with its evidence, and applies none (docs/spec/identities.md). The
// identities command and the MCP server's identity_candidates tool both
// answer through it.
//
// It reads the run's facts with the run's own policy, whose [bots] and [ai]
// tables tell bots and agents apart, and never its metric values: the
// candidates come from the facts alone, so the use case reads less than
// the report does. Display names come from the whole run, as every other
// page names people; the candidates come from the scope's repositories. A
// scope that names no group or repository of the run is a
// *domain.UsageError.
func Identities(ctx context.Context, d Deps, r IdentitiesRequest) (*report.Identities, error) {
	d, err := d.ready("app.Identities", false, false, true)
	if err != nil {
		return nil, err
	}
	run, err := resolveRun(ctx, d.Store, r.RunID)
	if err != nil {
		return nil, err
	}
	p, err := runPolicy(run)
	if err != nil {
		return nil, err
	}
	fs, err := d.Store.LoadFacts(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("reading run %d's facts: %w", run.ID, err)
	}
	// The run's own GitHub data (ADR 0011): GitHub's links from commits to
	// accounts are the same-forge-account rule's evidence (#103).
	fs, forge, _, err := withForge(ctx, d, run, fs)
	if err != nil {
		return nil, err
	}
	scoped, scope, err := identitiesScope(fs, r.Scope, run.ID)
	if err != nil {
		return nil, err
	}
	who := identity.New(fs, p)
	survey := identity.Candidates(scoped, who)
	d.Log.Debug("identity candidates", "run", run.ID, "addresses", survey.Addresses, "candidates", len(survey.Candidates))
	return report.NewIdentities(&report.IdentitiesInput{Run: &run, Facts: scoped, Scope: scope, Survey: survey, Who: who,
		Forge: forge, Mailmap: r.Mailmap}), nil
}

// identitiesScope returns the facts of the repositories a scope names: every
// repository for the zero scope or "all", a group's members, or one
// repository by key. A repository key pasted with its credentials is
// matched by its scrubbed form, and an error quotes only that form
// (docs/spec/git-invocation.md#url-scrubbing).
func identitiesScope(fs *domain.FactSet, s domain.Scope, runID int64) (*domain.FactSet, domain.Scope, error) {
	var keep func(*domain.RepoFacts) bool
	switch s.Kind {
	case "", domain.ScopeAll:
		if s.Key != "" {
			return nil, s, &domain.UsageError{Msg: fmt.Sprintf("scope %q takes no key: every repository is in it", s.Kind)}
		}
		return fs, domain.Scope{Kind: domain.ScopeAll}, nil
	case domain.ScopeGroup:
		found := false
		for _, g := range fs.Groups {
			found = found || g.Name == s.Key
		}
		if !found {
			return nil, s, &domain.UsageError{Msg: fmt.Sprintf(
				"group %q is not a group of run %d: name a group as 'stillhere report --format json' lists them", s.Key, runID)}
		}
		keep = func(rf *domain.RepoFacts) bool { return rf.Repo.Group == s.Key }
	case domain.ScopeRepo:
		if !hasRepo(fs, s.Key) {
			s.Key = redact.URL(s.Key)
			if !hasRepo(fs, s.Key) {
				return nil, s, &domain.UsageError{Msg: fmt.Sprintf(
					"repository %q is not a repository of run %d: name it by its key, as 'stillhere report --format json' lists them", s.Key, runID)}
			}
		}
		keep = func(rf *domain.RepoFacts) bool { return rf.Repo.Key == s.Key }
	default:
		return nil, s, &domain.UsageError{Msg: fmt.Sprintf("scope kind %q: want all, group or repo", s.Kind)}
	}
	out := *fs
	out.Repos, out.Groups = nil, nil
	for i := range fs.Repos {
		if keep(&fs.Repos[i]) {
			out.Repos = append(out.Repos, fs.Repos[i])
		}
	}
	for _, g := range fs.Groups {
		if s.Kind == domain.ScopeGroup && g.Name == s.Key {
			out.Groups = append(out.Groups, g)
		}
	}
	return &out, s, nil
}
