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
// faster: a clone without a commit-graph makes blame parse every commit
// object, and one without changed-path filters makes it walk every commit
// for every file (1.4 and 2.85 times as long on 30 public repositories, 3.7
// to 4.5 times without filters on a 52,000-commit one, with identical
// facts; #56). A scan never writes into a clone it reads
// (docs/spec/git-invocation.md), so it says what to run instead, the
// command sync runs in a mirror:
//
//   - On git 2.46 and later, a clone without filters gets the command that
//     writes version 2 filters. Plain --changed-paths writes version 1
//     while commitGraph.changedPathsVersion is unset, and version 1 hashed
//     path bytes above 0x7f differently where char is signed, so a graph
//     written on one platform could tell blame on another that a commit did
//     not touch a path it did.
//   - On 2.45, the floor, which writes only version 1, a clone without any
//     graph gets the command that writes one without filters, and a clone
//     with one gets no hint.
//
// So the mirrors sync keeps get no hint: a graph with filters on 2.46 and
// later, one without on 2.45. A check that fails says nothing: it only
// decides whether to print a hint.
func commitGraphHint(ctx context.Context, d Deps, repo domain.Repo, blame time.Duration) {
	if blame < slowBlame {
		return
	}
	graph, filters, err := collect.CommitGraph(ctx, d.Git, repo)
	if err != nil || filters {
		return
	}
	const never = " (stillhere never writes into your clones)"
	switch {
	case gitAtLeast(d.Git, 2, 46):
		d.Log.Info("hint: blame in this clone would be about 3 times faster with a commit-graph with changed-path filters; "+
			"run '"+changedPathsCommand+"' in it"+never,
			"repo", repo.Name, "blame", blame.Round(time.Second))
	case !graph:
		d.Log.Info("hint: blame in this clone would be faster with a commit-graph (about 1.4 times; with git 2.46 "+
			"or later, about 3 times with changed-path filters); run 'git commit-graph write --reachable' in it"+never,
			"repo", repo.Name, "blame", blame.Round(time.Second))
	}
}

// changedPathsCommand writes a commit-graph with version 2 changed-path
// filters, as sync does in a mirror (git 2.46 and later).
const changedPathsCommand = "git -c commitGraph.changedPathsVersion=2 commit-graph write --reachable --changed-paths"

// gitAtLeast reports whether the git that git runs is major.minor or newer,
// for a runner that can tell (gitcli.Runner.AtLeast); false for one that
// cannot, which takes the floor's behaviour. A capability check rather than
// a port method: only the hint needs it, and every other caller of the
// runner neither knows nor cares.
func gitAtLeast(git domain.GitRunner, major, minor int) bool {
	v, ok := git.(interface{ AtLeast(major, minor int) bool })
	return ok && v.AtLeast(major, minor)
}
