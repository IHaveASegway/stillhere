package fixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// DiscoveryToken is the credential the discovery fixture plants in remote
// URLs. TestDiscover asserts that it appears in no output: no returned
// struct, warning or log line, and from M1 no report or store row.
const DiscoveryToken = "TOKENVALUE"

// DiscoveryWidgetRemote is the origin of the two duplicate widget clones: a
// URL that keeps its credential in the userinfo, as many clones do. It
// scrubs to https://github.com/example/widget.git.
const DiscoveryWidgetRemote = "https://user:" + DiscoveryToken + "@github.com/example/widget.git"

// DiscoveryRepos is the discovery fixture of testdata/fixture/SPEC.md
// (TestDiscover): the paths a test names on the command line, all absolute.
// Each repository is a clone of Upstream, whose main has two commits and
// whose trunk is one commit ahead of main, unless its field says otherwise.
// A remote listed for a clone replaces the upstream path git cloned from.
type DiscoveryRepos struct {
	Root     string // holds everything below
	Upstream string // the repository the clones were cloned from; never named on a command line

	// Duplicates (discovery rule 5).
	WidgetA   string // work tree; origin DiscoveryWidgetRemote
	WidgetB   string // work tree; origin DiscoveryWidgetRemote: a duplicate of WidgetA
	Other     string // work tree; origin https://github.com/example/other.git
	Mirror    string // bare mirror of https://github.com/example/widget.git, as stillhere sync writes one
	SCP       string // work tree; origin git@github.com:o/r.git
	HTTPS     string // work tree; origin https://github.com/o/r.git
	UnparsedA string // work tree; origin https:/user:TOKENVALUE@github.com/example/widget.git, which does not parse
	UnparsedB string // work tree; origin https//user:TOKENVALUE@github.com/example/widget.git, which does not parse

	// Repositories and groups (rules 1, 2, 3 and 6).
	Bare  string // bare repository named directly; no origin; HEAD is trunk
	Group string // group team: work tree api, bare web.git (HEAD trunk) and linked, a worktree whose .git is a file; notes/, which holds a clone two levels down, and README.md are not members
	Outer string // work tree with a nested clone, inner, that discovery must not add
	SrcA  string // group a/src holding work tree one (no origin)
	SrcB  string // group b/src holding work tree two (no origin): a second group named src
	SrcC  string // group c/src-2, empty: a suffix must not take its name

	// Default branch.
	Tracking   string // origin/HEAD is origin/trunk; local main exists; local trunk has one commit origin/trunk lacks
	MainFirst  string // no origin; main, master (at trunk) and feature; HEAD on feature
	MasterOnly string // no origin; master (at main) and feature (at trunk); HEAD on feature
	Current    string // no origin; only branch dev, at trunk
	Detached   string // no origin; no branch; HEAD detached at trunk

	// Paths that are neither a repository nor a group.
	Empty     string // a repository with no commits
	Broken    string // a directory whose .git file names a git directory that does not exist
	Stray     string // outer/stray, whose .git is an empty directory: git skips it and answers for Outer
	StrayBare string // bare.git/stray.git, laid out like a git directory with a HEAD git rejects: git answers for Bare
	File      string // a regular file
	Missing   string // a path that does not exist
}

// Discovery builds the discovery fixture in a new temporary directory and
// returns its paths. It stops the test with t.Fatal if the build fails.
func Discovery(t testing.TB) *DiscoveryRepos {
	t.Helper()
	r, err := BuildDiscovery(t.Context(), filepath.Join(t.TempDir(), "discovery"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// BuildDiscovery builds the discovery fixture in dir, which is created when
// missing and must otherwise be empty. Cancelling ctx kills the running git
// process and returns its error.
func BuildDiscovery(ctx context.Context, dir string) (*DiscoveryRepos, error) {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	in := func(rel ...string) string { return filepath.Join(append([]string{dir}, rel...)...) }
	r := &DiscoveryRepos{
		Root:       dir,
		Upstream:   in("upstream"),
		WidgetA:    in("widget-a"),
		WidgetB:    in("widget-b"),
		Other:      in("other"),
		Mirror:     in("mirror.git"),
		SCP:        in("scp"),
		HTTPS:      in("https"),
		UnparsedA:  in("unparsed-a"),
		UnparsedB:  in("unparsed-b"),
		Bare:       in("bare.git"),
		Group:      in("team"),
		Outer:      in("outer"),
		SrcA:       in("a", "src"),
		SrcB:       in("b", "src"),
		SrcC:       in("c", "src-2"),
		Tracking:   in("tracking"),
		MainFirst:  in("main-first"),
		MasterOnly: in("master-only"),
		Current:    in("current"),
		Detached:   in("detached"),
		Empty:      in("empty"),
		Broken:     in("broken"),
		Stray:      in("outer", "stray"),
		StrayBare:  in("bare.git", "stray.git"),
		File:       in("file.txt"),
		Missing:    in("missing"),
	}
	if err := discoveryUpstream(ctx, r.Upstream); err != nil {
		return nil, fmt.Errorf("fixture: build discovery upstream in %s: %w", r.Upstream, err)
	}
	up := r.Upstream

	// Duplicates.
	b.discoveryClone(up, r.WidgetA, DiscoveryWidgetRemote)
	b.discoveryClone(up, r.WidgetB, DiscoveryWidgetRemote)
	b.discoveryClone(up, r.Other, "https://github.com/example/other.git")
	b.discoveryClone(up, r.Mirror, "https://github.com/example/widget.git", "--bare")
	b.git("-C", r.Mirror, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	b.discoveryClone(up, r.SCP, "git@github.com:o/r.git")
	b.discoveryClone(up, r.HTTPS, "https://github.com/o/r.git")
	b.discoveryClone(up, r.UnparsedA, "https:/user:"+DiscoveryToken+"@github.com/example/widget.git")
	b.discoveryClone(up, r.UnparsedB, "https//user:"+DiscoveryToken+"@github.com/example/widget.git")

	// Repositories and groups.
	b.discoveryClone(up, r.Bare, "", "--bare")
	b.git("-C", r.Bare, "symbolic-ref", "HEAD", "refs/heads/trunk")
	b.discoveryClone(up, filepath.Join(r.Group, "api"), "https://github.com/example/api.git")
	web := filepath.Join(r.Group, "web.git")
	b.discoveryClone(up, web, "https://github.com/example/web.git", "--bare")
	b.git("-C", web, "symbolic-ref", "HEAD", "refs/heads/trunk")
	// linked's own repository sits outside the group and has no origin, so
	// linked is keyed by its own name and is nobody's duplicate.
	worktreeMain := in("worktree-main")
	b.discoveryClone(up, worktreeMain, "")
	b.git("-C", worktreeMain, "worktree", "add", "--quiet", "-b", "linked", filepath.Join(r.Group, "linked"), "main")
	b.discoveryClone(up, filepath.Join(r.Group, "notes", "deep"), "https://github.com/example/deep.git")
	b.discoveryWrite(filepath.Join(r.Group, "README.md"), "not a repository\n")
	b.discoveryClone(up, r.Outer, "https://github.com/example/outer.git")
	b.discoveryClone(up, filepath.Join(r.Outer, "inner"), "https://github.com/example/inner.git")
	b.discoveryClone(up, filepath.Join(r.SrcA, "one"), "")
	b.discoveryClone(up, filepath.Join(r.SrcB, "two"), "")
	b.discoveryMkdir(r.SrcC)

	// Default branch. Each repository leaves the rule it tests as the first
	// that applies, and makes every later rule give a different answer.
	b.discoveryClone(up, r.Tracking, "https://github.com/example/tracking.git")
	b.git("-C", r.Tracking, "remote", "set-head", "origin", "trunk")
	b.git("-C", r.Tracking, "switch", "--quiet", "trunk")
	b.row(at(time.February, 1, 12), person{"Cara Dev", "cara@example.com"})
	b.git("-C", r.Tracking, "commit", "--quiet", "--allow-empty", "--message", "local commit, never pushed")

	// These four end with no origin, so their branches are made from
	// origin/trunk before origin is removed.
	noOrigin := []string{r.MainFirst, r.MasterOnly, r.Current, r.Detached}
	for _, dir := range noOrigin {
		b.git("clone", "--quiet", up, dir)
	}
	b.git("-C", r.MainFirst, "branch", "master", "origin/trunk")
	b.git("-C", r.MainFirst, "switch", "--quiet", "--create", "feature")
	b.git("-C", r.MasterOnly, "branch", "--move", "main", "master")
	b.git("-C", r.MasterOnly, "switch", "--quiet", "--create", "feature", "origin/trunk")
	b.git("-C", r.Current, "switch", "--quiet", "--create", "dev", "origin/trunk")
	b.git("-C", r.Current, "branch", "--quiet", "--delete", "--force", "main")
	b.git("-C", r.Detached, "switch", "--quiet", "--detach", "origin/trunk")
	b.git("-C", r.Detached, "branch", "--quiet", "--delete", "--force", "main")
	for _, dir := range noOrigin {
		b.git("-C", dir, "remote", "remove", "origin")
	}

	// Paths that are neither. Missing is never created.
	b.git("init", "--quiet", "--template=", "--initial-branch=main", r.Empty)
	b.discoveryWrite(filepath.Join(r.Broken, ".git"), "gitdir: "+in("no-such-git-dir")+"\n")
	b.discoveryMkdir(filepath.Join(r.Stray, ".git"))
	b.discoveryWrite(filepath.Join(r.StrayBare, "HEAD"), "not a ref\n")
	b.discoveryMkdir(filepath.Join(r.StrayBare, "objects"))
	b.discoveryMkdir(filepath.Join(r.StrayBare, "refs"))
	b.discoveryWrite(r.File, "not a repository\n")

	if b.err != nil {
		return nil, fmt.Errorf("fixture: build discovery repositories in %s: %w", dir, b.err)
	}
	return r, nil
}

// CloneHealthRepos is the clone-health fixture of testdata/fixture/SPEC.md
// (TestCloneHealth): clones of one upstream that differ only in how much of
// it they hold. Every path is absolute, and each clone's origin is a
// distinct URL, so discovering them together removes no duplicate.
type CloneHealthRepos struct {
	Upstream      string // the repository the clones were cloned from: main has two commits, the second changing app.py
	Full          string // a complete clone; origin https://github.com/example/full.git
	Shallow       string // git clone --depth 1; origin https://github.com/example/shallow.git
	Partial       string // git clone --filter=blob:none, so the first app.py blob is missing; origin, its promisor remote, is the promisor URL
	Extension     string // a complete clone with extensions.partialClone=origin, as older git marked a partial clone; origin https://github.com/example/extension.git
	OtherPromisor string // a complete clone with a second remote, backup, marked promisor; origin https://github.com/example/other-promisor.git
	NotPromisor   string // a complete clone with remote.origin.promisor=false; origin https://github.com/example/not-promisor.git
}

// CloneHealth builds the clone-health fixture in a new temporary directory
// and returns its paths. promisor is the URL the partial clone's origin is
// set to once the clone is made, such as a local listener that records
// connections: every lazy fetch of a missing object goes there. It stops the
// test with t.Fatal if the build fails.
func CloneHealth(t testing.TB, promisor string) *CloneHealthRepos {
	t.Helper()
	r, err := BuildCloneHealth(t.Context(), filepath.Join(t.TempDir(), "clone-health"), promisor)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// BuildCloneHealth builds the clone-health fixture in dir, which is created
// when missing and must otherwise be empty, with the partial clone's origin
// set to promisor. Cancelling ctx kills the running git process and returns
// its error.
func BuildCloneHealth(ctx context.Context, dir, promisor string) (*CloneHealthRepos, error) {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	r := &CloneHealthRepos{
		Upstream:      filepath.Join(dir, "upstream"),
		Full:          filepath.Join(dir, "full"),
		Shallow:       filepath.Join(dir, "shallow"),
		Partial:       filepath.Join(dir, "partial"),
		Extension:     filepath.Join(dir, "extension"),
		OtherPromisor: filepath.Join(dir, "other-promisor"),
		NotPromisor:   filepath.Join(dir, "not-promisor"),
	}
	if err := discoveryUpstream(ctx, r.Upstream); err != nil {
		return nil, fmt.Errorf("fixture: build clone-health upstream in %s: %w", r.Upstream, err)
	}
	// git ignores --depth and --filter on a clone from a plain path, so
	// those two clone through file://, as from a server.
	upURL := discoveryFileURL(r.Upstream)

	b.discoveryClone(r.Upstream, r.Full, "https://github.com/example/full.git")
	b.discoveryClone(upURL, r.Shallow, "https://github.com/example/shallow.git", "--depth", "1")
	b.discoveryClone(upURL, r.Partial, promisor, "--filter=blob:none")
	b.discoveryClone(r.Upstream, r.Extension, "https://github.com/example/extension.git")
	b.git("-C", r.Extension, "config", "extensions.partialClone", "origin")
	b.discoveryClone(r.Upstream, r.OtherPromisor, "https://github.com/example/other-promisor.git")
	b.git("-C", r.OtherPromisor, "remote", "add", "backup", "https://github.com/example/backup.git")
	b.git("-C", r.OtherPromisor, "config", "remote.backup.promisor", "true")
	b.discoveryClone(r.Upstream, r.NotPromisor, "https://github.com/example/not-promisor.git")
	b.git("-C", r.NotPromisor, "config", "remote.origin.promisor", "false")

	if b.err != nil {
		return nil, fmt.Errorf("fixture: build clone-health repositories in %s: %w", dir, b.err)
	}
	return r, nil
}

// discoveryUpstream builds the repository the discovery and clone-health
// clones are cloned from: main with two commits, the second extending
// app.py, so a blob-less clone lacks the first version of app.py; and trunk,
// one commit ahead of main. It serves --filter clones over file://, which
// upload-pack allows only when this repository's config says so.
func discoveryUpstream(ctx context.Context, dir string) error {
	b, err := newBuilder(ctx, dir)
	if err != nil {
		return err
	}
	ada := person{"Ada Example", "ada@example.com"}
	bob := person{"Bob Builder", "bob@example.com"}

	b.initRepo()
	b.git("config", "commit.gpgsign", "false")
	b.git("config", "uploadpack.allowFilter", "true")

	b.row(at(time.January, 1, 12), ada)
	b.write("README.md", lines("# widget"))
	b.write("app.py", lines("x = 1"))
	b.commit("add app.py")

	b.row(at(time.January, 2, 12), ada)
	b.extend("app.py", lines("y = 2"))
	b.commit("extend app.py")

	b.git("switch", "--quiet", "--create", "trunk")
	b.row(at(time.January, 3, 12), bob)
	b.extend("app.py", lines("z = 3"))
	b.commit("extend app.py on trunk")
	b.git("switch", "--quiet", "main")
	return b.err
}

// discoveryClone clones src into dst with extra clone options, then sets
// dst's origin to remote, or removes origin when remote is "".
func (b *builder) discoveryClone(src, dst, remote string, options ...string) {
	b.git(slices.Concat([]string{"clone", "--quiet"}, options, []string{src, dst})...)
	if remote == "" {
		b.git("-C", dst, "remote", "remove", "origin")
		return
	}
	b.git("-C", dst, "remote", "set-url", "origin", remote)
}

// discoveryWrite writes content to the file at path, outside any work tree,
// creating its directory.
func (b *builder) discoveryWrite(path, content string) {
	if b.err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		b.err = fmt.Errorf("write %s: %w", path, err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		b.err = fmt.Errorf("write %s: %w", path, err)
	}
}

// discoveryMkdir creates the directory at path.
func (b *builder) discoveryMkdir(path string) {
	if b.err != nil {
		return
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		b.err = fmt.Errorf("create %s: %w", path, err)
	}
}

// discoveryFileURL is the file:// URL of the local directory dir.
func discoveryFileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // a Windows drive path: file:///C:/...
	}
	return "file://" + p
}
