package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/classify"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Paths lists each path of a stored run with the class the classifier gave
// it and the rule that decided (classify.Explain), from the run's own
// policy, which the run keeps: the answer a scan's "skipped paths" counts
// point to, and the way to check what counts as product code. It reads the
// run's facts and policy only: no git, no metric, no GitHub data, so it is
// as fast on a large run as listing its paths.
//
// Prefixes, Repo, Role and Skipped filter the listing: a path is listed
// when it is under one of the prefixes (any, when there are none), in the
// repository named (any, for ""), and has the role, or was skipped, as
// asked. A role that is not one of the classifier's, or a role asked
// together with Skipped, is a usage error, since no path could pass.
func Paths(ctx context.Context, d Deps, r PathsRequest) (*report.Paths, error) {
	d, err := d.ready("app.Paths", false, false, true)
	if err != nil {
		return nil, err
	}
	if r.Role != "" {
		if !knownRole(r.Role) {
			return nil, &domain.UsageError{Msg: fmt.Sprintf("--role %q is not a role: want one of %s", r.Role, strings.Join(roleNames(), ", "))}
		}
		if r.Skipped {
			return nil, &domain.UsageError{Msg: "--role and --skipped ask for two kinds of path at once: a skipped path has no role"}
		}
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
	repo, err := repoKey(fs, r.Repo, run.ID)
	if err != nil {
		return nil, err
	}
	var prefixes []string
	for _, pre := range r.Prefixes {
		// A path is echoed in the answer, and a URL pasted as one can carry
		// a token: it is scrubbed where it enters, as who-knows does (#71).
		prefixes = append(prefixes, redact.Scrub(cleanPrefix(pre)))
	}
	source := classify.SourcePackaged
	if !p.Packaged() {
		source = policyFileName(run)
	}
	asked := *fs
	asked.Repos = nil
	rows := map[string][]report.PathRow{}
	for _, rf := range fs.Repos {
		if repo != "" && rf.Repo.Key != repo {
			continue
		}
		asked.Repos = append(asked.Repos, rf)
		var list []report.PathRow
		for _, pc := range rf.Paths {
			if !pathListed(pc, prefixes, r.Role, r.Skipped) {
				continue
			}
			rule := classify.Explain(p, pc)
			if rule.Source == classify.SourceFile {
				rule.Source = source
			}
			list = append(list, report.PathRow{
				Path: pc.Path, Role: string(pc.Role), Skip: string(pc.Skip), AtHead: pc.AtHead, Size: pc.Size,
				Rule: report.PathRule{Kind: string(rule.Kind), Text: rule.Text, Where: rule.Where, Source: rule.Source, Note: rule.Note},
			})
		}
		rows[rf.Repo.Key] = list
	}
	return report.NewPaths(&report.PathsInput{
		Run: &run, Facts: &asked, Prefixes: prefixes, Repo: repo, Role: r.Role, Skipped: r.Skipped,
		PolicySource: source, Rows: rows,
	}), nil
}

// pathListed reports whether a classified path passes the paths answer's
// filters.
func pathListed(pc domain.PathClass, prefixes []string, role string, skipped bool) bool {
	switch {
	case skipped && pc.Skip == "":
		return false
	case role != "" && string(pc.Role) != role:
		return false
	case len(prefixes) == 0:
		return true
	}
	for _, pre := range prefixes {
		if under(pc.Path, pre) {
			return true
		}
	}
	return false
}

// policyFileName is how the paths answer names a run's policy file: the
// name the scan recorded, as the user gave it (./stillhere.toml, or the
// --policy FILE), scrubbed when it was stored.
func policyFileName(run domain.Run) string {
	if name := run.Options["policy"]; name != "" {
		return name
	}
	return classify.SourceFile
}

// knownRole reports whether s names one of the classifier's roles.
func knownRole(s string) bool {
	for _, r := range roleNames() {
		if r == s {
			return true
		}
	}
	return false
}

// roleNames are the classifier's roles, as --role takes them.
func roleNames() []string {
	return []string{
		string(domain.RoleProd), string(domain.RoleTest), string(domain.RoleDoc), string(domain.RoleIaC),
		string(domain.RoleMarkup), string(domain.RoleSQL), string(domain.RoleMigration), string(domain.RoleSeed),
	}
}
