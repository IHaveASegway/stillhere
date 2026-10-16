package app

import (
	"context"
	"time"

	"github.com/IHaveASegway/stillhere/internal/collect"
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// slowBlame is how long a repository's blame takes before a scan points out
// that a commit-graph with changed-path filters would speed it up (#56).
// Below it, the hint would save the user less than the time it takes to
// read.
const slowBlame = 30 * time.Second

// commitGraphHint logs, once per repository, how to make a slow blame
// faster: a clone without a commit-graph with changed-path filters makes
// blame walk every commit for every file (2.85 times as long on 30 public
// repositories, 3.7 to 4.5 times on a 52,000-commit one, with identical
// facts; #56). A scan never writes into a clone it reads
// (docs/spec/git-invocation.md), so it says what to run instead. Mirrors
// sync keeps have the filters already, so they get no hint. A check that
// fails says nothing: it only decides whether to print a hint.
func commitGraphHint(ctx context.Context, d Deps, repo domain.Repo, blame time.Duration) {
	if blame < slowBlame {
		return
	}
	if ok, err := collect.ChangedPathFilters(ctx, d.Git, repo); err != nil || ok {
		return
	}
	d.Log.Info("hint: blame in this clone would be about 3 times faster with a commit-graph; "+
		"run 'git commit-graph write --reachable --changed-paths' in it (stillhere never writes into your clones)",
		"repo", repo.Name, "blame", blame.Round(time.Second))
}
