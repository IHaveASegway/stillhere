package collect

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// Discovery is what Discover found on the paths it was given.
type Discovery struct {
	Repos    []domain.Repo      // health-checked, duplicates removed, sorted by Key
	Groups   []domain.Group     // sorted by Name
	Warnings []domain.Warning   // duplicate-repo, shallow-clone, partial-clone; sorted by Repo, Code, Message
	Failed   []domain.FileError // paths that gave no repository, each once, sorted by Path: a PATH slot that is neither a repo nor a group, or a repo git cannot read or whose default branch has no commit; the use cases report each as a run-level path-failed warning and mark the run partial
}

// The warning codes discovery emits (docs/spec/report-json.md).
const (
	discoverDuplicateRepo = "duplicate-repo"
	discoverShallowClone  = "shallow-clone"
	discoverPartialClone  = "partial-clone"
)

const (
	// discoverTimeout bounds each git command discovery runs. They read a
	// few config keys and refs and answer in milliseconds, so a repository
	// that takes longer, such as one on a hung network mount, fails its
	// path instead of holding the run for the profile's 300 s default.
	discoverTimeout = 30 * time.Second

	// discoverMaxJobs bounds the repositories inspected at once, and with
	// them the git processes discovery runs. Each repository runs its
	// commands one after another: three for a work tree with origin/HEAD,
	// four for one without it (the fourth tells a missing origin/HEAD from
	// one whose target is gone), six for one that falls back to HEAD; four
	// for a bare repository, five for one that a .git file names.
	discoverMaxJobs = 8
)

// discoverKind is what a directory is, as far as the filesystem can tell
// before git is asked, and then as git confirmed it.
type discoverKind int

const (
	discoverOther    discoverKind = iota // neither: a PATH slot like this is a group
	discoverWorkTree                     // has a .git file or directory (rule 1)
	discoverBare                         // has objects and refs, or a name ending in .git (rule 2)
)

// discoverDir is a directory named on the command line or found in a group.
type discoverDir struct {
	path string      // absolute, spelled as it was named or listed
	info fs.FileInfo // os.Stat of path, through symbolic links: what os.SameFile compares
}

// dir returns d. discoverCandidate embeds discoverDir, so discoverDistinct
// takes either.
func (d discoverDir) dir() discoverDir { return d }

// discoverCandidate is a directory that looks like a repository, waiting for
// git to confirm it.
type discoverCandidate struct {
	discoverDir
	group string // the group's final name; "" for a repository named directly
	kind  discoverKind
}

// Discover expands command-line paths into repositories and groups
// (docs/spec/discovery.md). Each path is a work tree, a bare repository or a
// group of them, tried in that order; anything else is a Failed entry, and
// the other paths still run. Discover keeps one repository per Repo.Key,
// the lexicographically first path, with a duplicate-repo warning for each
// other; resolves each default branch to the full SHA of its commit; and
// flags shallow and partial clones with a warning, keeping them so that the
// collectors drop only the facts such a clone cannot give. It reads
// repositories and never writes to one or fetches.
//
// The filesystem answers first and git confirms: a directory is a candidate
// only with the layout git itself looks for (a .git entry, or objects and
// refs), and git must then place the repository exactly there. So a plain
// directory inside another work tree is never taken for a repository, and a
// candidate git cannot read is a failure rather than "not a repository": a
// directory with an unusable .git fails instead of becoming a group.
//
// One directory is inspected once, however it is reached: twice by the same
// path, through its group and on its own, through a symbolic link, or in
// another letter case on a case-insensitive file system. It is one
// repository, or one failure, and a repository stays in its group.
//
// Every remote passes through redact.URL before it is stored or compared, so
// no credential in a remote reaches a Repo, a Warning or a Failed message.
// The result is sorted, so it does not depend on the order of paths.
// Discover returns an error only when ctx ends; every other failure is
// confined to its path.
func Discover(ctx context.Context, git domain.GitRunner, paths []string) (*Discovery, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	slots, failed := discoverSlots(paths)
	var (
		named     []discoverCandidate
		groupDirs []discoverDir
	)
	for _, slot := range slots {
		kind, err := discoverShape(slot.path)
		switch {
		case err != nil:
			failed = append(failed, discoverFailure(slot.path, err))
		case kind == discoverOther:
			groupDirs = append(groupDirs, slot)
		default:
			named = append(named, discoverCandidate{discoverDir: slot, kind: kind})
		}
	}
	groups, members, memberFailed := discoverGroups(groupDirs)
	failed = append(failed, memberFailed...)
	// Members come first, in path order, so a repository that is also named
	// on its own stays in its group (rule 5).
	slices.SortFunc(members, func(a, b discoverCandidate) int { return cmp.Compare(a.path, b.path) })
	cands := discoverDistinct(slices.Concat(members, named))

	repos, repoFailed, err := discoverInspect(ctx, git, cands)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	failed = append(failed, repoFailed...)

	kept, warnings := discoverDedupe(repos)
	warnings = append(warnings, discoverHealth(kept)...)
	slices.SortFunc(warnings, func(a, b domain.Warning) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Code, b.Code), cmp.Compare(a.Message, b.Message))
	})

	byName := make(map[string]int, len(groups))
	for i, g := range groups {
		byName[g.Name] = i
	}
	for _, r := range kept { // sorted by Key, so each group's Repos are too
		if i, ok := byName[r.Group]; ok && r.Group != "" {
			groups[i].Repos = append(groups[i].Repos, r.Key)
		}
	}
	slices.SortFunc(groups, func(a, b domain.Group) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(failed, func(a, b domain.FileError) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Message, b.Message))
	})
	// A path named directly and found in a group, whose layout cannot be
	// read, fails both ways with the same message: it is reported once.
	failed = slices.Compact(failed)
	return &Discovery{Repos: kept, Groups: groups, Warnings: warnings, Failed: failed}, nil
}

// discoverSlots makes each command-line path absolute and looks it up. A
// path that does not exist or is not a directory fails here (rule 4). Paths
// that name one directory, as the same string, through a symbolic link or in
// another letter case on a case-insensitive file system, are one slot, under
// the lexicographically first spelling. The result is sorted, so that group
// suffixes and duplicate winners follow path order, never argument order.
func discoverSlots(paths []string) ([]discoverDir, []domain.FileError) {
	var (
		abs    []string
		dirs   []discoverDir
		failed []domain.FileError
	)
	for _, p := range paths {
		if p == "" {
			failed = append(failed, domain.FileError{Path: p, Message: "empty path"})
			continue
		}
		a, err := filepath.Abs(p) // also cleans: widget/ and widget are one string
		if err != nil {
			failed = append(failed, discoverFailure(p, err))
			continue
		}
		abs = append(abs, a)
	}
	slices.Sort(abs)
	for _, p := range slices.Compact(abs) {
		info, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			failed = append(failed, domain.FileError{Path: p, Message: "does not exist"})
		case err != nil:
			failed = append(failed, discoverFailure(p, err))
		case !info.IsDir():
			failed = append(failed, domain.FileError{Path: p, Message: "not a directory, so neither a repository nor a group"})
		default:
			dirs = append(dirs, discoverDir{path: p, info: info})
		}
	}
	return discoverDistinct(dirs), failed
}

// discoverDistinct keeps the first of dirs that name each directory and
// drops the later ones, which reach it as another string: through a
// symbolic link, or in another letter case on a case-insensitive file
// system, as on macOS by default. A directory's identity is its device and
// inode, which only os.SameFile compares without the syscall package the
// dependency rule keeps out of collect, so every pair is compared: 5,000
// directories take about 12 ms, against seconds for the git processes that
// then inspect them.
func discoverDistinct[T interface{ dir() discoverDir }](dirs []T) []T {
	var kept []T
	for _, d := range dirs {
		info := d.dir().info
		if !slices.ContainsFunc(kept, func(k T) bool { return os.SameFile(k.dir().info, info) }) {
			kept = append(kept, d)
		}
	}
	return kept
}

// discoverShape reports whether the directory dir has the layout of a
// repository, before git is asked. A .git entry, which a work tree, a
// linked worktree and a submodule checkout all have, makes a work-tree
// candidate (rule 1, checked first). objects and refs, two of the three
// entries git's own setup looks for in a git directory, or a name ending in
// .git, the convention for bare repositories, make a bare candidate (rule
// 2). HEAD, the third, is left for git to check, so that a bare repository
// without one fails with git's own message instead of passing for an empty
// group.
func discoverShape(dir string) (discoverKind, error) {
	ok, err := discoverExists(filepath.Join(dir, ".git"), false)
	switch {
	case err != nil:
		return 0, err
	case ok:
		return discoverWorkTree, nil
	case strings.HasSuffix(filepath.Base(dir), ".git"):
		return discoverBare, nil
	}
	for _, sub := range []string{"objects", "refs"} {
		ok, err := discoverExists(filepath.Join(dir, sub), true)
		if err != nil {
			return 0, err
		}
		if !ok {
			return discoverOther, nil
		}
	}
	return discoverBare, nil
}

// discoverExists reports whether path exists, and when dir is set, whether
// it is a directory (following symbolic links). A path that does not exist
// is false; any other failure to look is an error.
func discoverExists(path string, dir bool) (bool, error) {
	stat := os.Lstat
	if dir {
		stat = os.Stat
	}
	fi, err := stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	return !dir || fi.IsDir(), nil
}

// discoverGroups lists the members of each group directory (rule 3): its
// immediate children that look like repositories, with no deeper walk. A
// child that is not a directory, or a directory that is not a repository,
// is not a member and is not reported. A group that cannot be listed fails
// and takes no name, so it cannot push another group to a suffix.
func discoverGroups(dirs []discoverDir) ([]domain.Group, []discoverCandidate, []domain.FileError) {
	type listed struct {
		dir     string
		members []discoverCandidate
	}
	var (
		ok     []listed
		failed []domain.FileError
	)
	for _, d := range dirs {
		members, memberFailed, err := discoverMembers(d.path)
		if err != nil {
			failed = append(failed, discoverFailure(d.path, err))
			continue
		}
		ok = append(ok, listed{d.path, members})
		failed = append(failed, memberFailed...)
	}
	listedDirs := make([]string, len(ok))
	for i, l := range ok {
		listedDirs[i] = l.dir
	}
	names := discoverGroupNames(listedDirs)
	var (
		groups []domain.Group
		cands  []discoverCandidate
	)
	for i, l := range ok {
		groups = append(groups, domain.Group{Name: names[i], Path: l.dir})
		for _, m := range l.members {
			m.group = names[i]
			cands = append(cands, m)
		}
	}
	return groups, cands, failed
}

// discoverMembers returns the immediate children of the group directory dir
// that look like repositories; a symbolic link counts as the directory it
// points at. A child that cannot be looked into, such as lost+found at the
// root of a mount or a folder the operating system protects, satisfies
// neither rule 1 nor rule 2 as far as anyone can tell, so it is not a member
// and is not reported. Any other failure to look is reported, since the
// child may be a repository.
func discoverMembers(dir string) ([]discoverCandidate, []domain.FileError, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var (
		members []discoverCandidate
		failed  []domain.FileError
	)
	for _, e := range entries {
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			continue // a file
		}
		child := filepath.Join(dir, e.Name())
		info, err := os.Stat(child)
		if err != nil || !info.IsDir() {
			continue // a link to a file, a dangling link, or a child removed since the listing
		}
		kind, err := discoverShape(child)
		switch {
		case errors.Is(err, fs.ErrPermission):
			continue // unreadable: not a member (rule 3)
		case err != nil:
			failed = append(failed, discoverFailure(child, err))
		case kind != discoverOther:
			members = append(members, discoverCandidate{discoverDir: discoverDir{path: child, info: info}, kind: kind})
		}
	}
	return members, failed, nil
}

// discoverGroupNames names each group directory by its basename, and gives
// every later group with the same basename, in path order, the smallest
// free suffix: src, src-2, src-3 (rule 6). A suffix never takes a name that
// another group has as its own basename, so no two groups share a name.
// dirs must be sorted; the names line up with them.
func discoverGroupNames(dirs []string) []string {
	names := make([]string, len(dirs))
	taken := make(map[string]bool, len(dirs))
	for i, dir := range dirs {
		if base := filepath.Base(dir); !taken[base] {
			taken[base] = true
			names[i] = base
		}
	}
	for i, dir := range dirs {
		if names[i] != "" {
			continue
		}
		base := filepath.Base(dir)
		for n := 2; ; n++ {
			if name := base + "-" + strconv.Itoa(n); !taken[name] {
				taken[name] = true
				names[i] = name
				break
			}
		}
	}
	return names
}

// discoverInspect asks git about every candidate, at most discoverMaxJobs at
// a time. A candidate git cannot confirm or resolve is a Failed entry; only
// the end of ctx is an error, because then every later answer would be a
// cancellation rather than a fact about a repository.
func discoverInspect(ctx context.Context, git domain.GitRunner, cands []discoverCandidate) ([]domain.Repo, []domain.FileError, error) {
	type result struct {
		repo   domain.Repo
		failed *domain.FileError
	}
	results := make([]result, len(cands))
	var g errgroup.Group
	g.SetLimit(min(discoverMaxJobs, runtime.GOMAXPROCS(0)))
	for i, c := range cands {
		g.Go(func() error {
			repo, err := discoverRepo(ctx, git, c)
			if err != nil {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				f := discoverFailure(c.path, err)
				results[i].failed = &f
				return nil
			}
			results[i].repo = repo
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	var (
		repos  []domain.Repo
		failed []domain.FileError
	)
	for _, r := range results {
		if r.failed != nil {
			failed = append(failed, *r.failed)
			continue
		}
		repos = append(repos, r.repo)
	}
	return repos, failed, nil
}

// discoverRepo confirms one candidate with git and reads what the run needs
// to know about it: what kind of repository it is, whether it is shallow,
// its scrubbed origin, whether it is a partial clone, and its default branch
// and Head. The raw origin URL lives only in this function; the Repo holds
// redact.URL's form of it.
func discoverRepo(ctx context.Context, git domain.GitRunner, c discoverCandidate) (domain.Repo, error) {
	kind, shallow, err := discoverProbe(ctx, git, c)
	if err != nil {
		return domain.Repo{}, err
	}
	origin, partial, err := discoverConfig(ctx, git, c.path)
	if err != nil {
		return domain.Repo{}, err
	}
	var branch, head string
	if kind == discoverBare {
		branch, head, err = discoverHEAD(ctx, git, c.path)
	} else {
		branch, head, err = discoverWorkTreeBranch(ctx, git, c.path)
	}
	if err != nil {
		return domain.Repo{}, err
	}
	name := discoverName(c.path)
	remote := redact.URL(origin)
	return domain.Repo{
		Key:     discoverKey(remote, name),
		Name:    name,
		Group:   c.group,
		Path:    c.path,
		Bare:    kind == discoverBare,
		Remote:  remote,
		Branch:  branch,
		Head:    head,
		Shallow: shallow,
		Partial: partial,
	}, nil
}

// discoverProbe asks git, in one rev-parse, to confirm the candidate, and
// returns what kind of repository git says it is and whether it is a
// shallow clone. git looks upward from a directory it cannot use, so its
// answers are checked against the candidate's own path:
//
//   - A bare candidate must be its own git directory: in a directory inside
//     a repository, git answers for that repository.
//   - A work-tree candidate must be the top of its work tree, with an empty
//     --show-prefix: past a .git it cannot use, git answers for an enclosing
//     work tree.
//   - A work-tree candidate whose .git names a bare repository is that bare
//     repository (rule 1). That is the bare-clone-plus-worktrees layout,
//     where .git holds "gitdir: ./.bare" and the worktrees sit beside it, or
//     a bare clone made into .git itself. A second rev-parse, run only in
//     this case, checks that .git names the repository git found, rather
//     than git having walked up into an enclosing bare repository.
func discoverProbe(ctx context.Context, git domain.GitRunner, c discoverCandidate) (discoverKind, bool, error) {
	if c.kind == discoverBare {
		out, err := discoverGit(ctx, git, c.path, nil,
			"rev-parse", "--is-bare-repository", "--is-shallow-repository", "--absolute-git-dir")
		if err != nil {
			return 0, false, err
		}
		a := discoverAnswers(out, 3)
		bare, shallow, gitDir := a[0], a[1], a[2]
		same, err := discoverSameDir(gitDir, c.path)
		switch {
		case err != nil:
			return 0, false, err
		case !same:
			return 0, false, fmt.Errorf("not a repository itself: git answered for the enclosing repository %s instead", gitDir)
		case bare != "true":
			return 0, false, errors.New("a git directory that is not a bare repository; name its work tree instead")
		}
		return discoverBare, shallow == "true", nil
	}
	out, err := discoverGit(ctx, git, c.path, nil, "rev-parse",
		"--is-inside-work-tree", "--is-bare-repository", "--is-shallow-repository", "--show-prefix", "--absolute-git-dir")
	if err != nil {
		return 0, false, err
	}
	a := discoverAnswers(out, 5)
	inside, bare, shallow, prefix, gitDir := a[0], a[1], a[2], a[3], a[4]
	switch {
	case inside == "true" && prefix == "":
		return discoverWorkTree, shallow == "true", nil
	case inside == "true":
		return 0, false, errors.New("has a .git that git cannot use: git found an enclosing work tree instead")
	case bare != "true":
		return 0, false, errors.New("has a .git, but git sees no work tree here")
	}
	named, err := discoverGitDir(ctx, git, c.path)
	if err != nil {
		return 0, false, err
	}
	same := false
	if named != "" {
		if same, err = discoverSameDir(named, gitDir); err != nil {
			return 0, false, err
		}
	}
	if !same {
		return 0, false, fmt.Errorf("has a .git that git cannot use: git found the enclosing repository %s instead", gitDir)
	}
	return discoverBare, shallow == "true", nil
}

// discoverGitDir returns the repository dir/.git names, as git resolves it:
// dir/.git itself when it is a git directory, or the repository a .git file
// points at. It is "" when git cannot use dir/.git.
func discoverGitDir(ctx context.Context, git domain.GitRunner, dir string) (string, error) {
	// git dies, with exit 128, when the path is neither a git directory nor
	// a .git file that names one: an answer here, not a failure to ask.
	out, err := discoverGit(ctx, git, dir, []int{128}, "rev-parse", "--resolve-git-dir", filepath.Join(dir, ".git"))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// discoverAnswers splits rev-parse output into its n answers, one per line.
// The last answer is a path, so it is never split further; an answer git did
// not print is "".
func discoverAnswers(out []byte, n int) []string {
	a := strings.SplitN(strings.TrimSuffix(string(out), "\n"), "\n", n)
	for len(a) < n {
		a = append(a, "")
	}
	return a
}

// discoverSameDir reports whether a and b name the same directory, through
// symbolic links and case-insensitive file systems alike.
func discoverSameDir(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}

// discoverConfig reads, in one git config call, the raw origin URL and
// whether the repository is a partial clone: a remote.<name>.promisor that
// is true for any remote, or extensions.partialClone set
// (discovery.md#clone-health). A --filter clone sets only the first, and
// older git set only the second, so both are read. The call reads git's
// whole view of the config, includes and worktree config too, and -z
// keeps each value intact: "key\nvalue", or just "key" for a key with no
// value. Exit 1 means no key matched. origin is the URL git fetches from:
// the first remote.origin.url after the last empty one, which clears the
// list before it; "" when there is none.
func discoverConfig(ctx context.Context, git domain.GitRunner, dir string) (origin string, partial bool, err error) {
	out, err := discoverGit(ctx, git, dir, []int{1},
		"config", "-z", "--get-regexp", `^(remote\.origin\.url|remote\..+\.promisor|extensions\.partialclone)$`)
	if err != nil {
		return "", false, err
	}
	haveOrigin := false
	s := newScanner(bytes.NewReader(out), scanNUL)
	for s.Scan() {
		key, value, hasValue := strings.Cut(s.Text(), "\n")
		switch {
		case key == "remote.origin.url":
			// An empty value resets the list, as git reads it since 2.46.
			// git 2.44 and 2.45 keep it as a URL of its own, which no fetch
			// can use, so after one of those git would try the URL before it.
			switch {
			case value == "":
				origin, haveOrigin = "", false
			case !haveOrigin:
				origin, haveOrigin = value, true
			}
		case key == "extensions.partialclone":
			partial = true
		case strings.HasSuffix(key, ".promisor"):
			partial = partial || discoverTrue(value, hasValue)
		}
	}
	if err := s.Err(); err != nil {
		return "", false, fmt.Errorf("read git config output: %w", err)
	}
	return origin, partial, nil
}

// discoverTrue reads a boolean config value as git does: a key with no value
// is true; false, no, off, the empty string and integer zero are false; and
// true, yes, on and any other integer are true. A value git rejects, such as
// "maybe", is true too: that errs on the side of skipping line facts with
// one warning rather than failing on every file.
func discoverTrue(value string, hasValue bool) bool {
	if !hasValue {
		return true
	}
	switch strings.ToLower(value) {
	case "false", "no", "off", "":
		return false
	}
	n, err := strconv.ParseInt(value, 0, 64)
	return err != nil || n != 0
}

// discoverRef is one line of the for-each-ref output discoverWorkTreeBranch
// reads.
type discoverRef struct {
	symref string // the ref a symbolic ref points at; "" for a plain ref
	sha    string
	typ    string // the type of the object the ref resolves to
}

// discoverWorkTreeBranch resolves a work tree's default branch
// (discovery.md#default-branch, ADR 0009). It is the branch origin/HEAD
// points at; else main, then master, as upstream had them at the last
// fetch (refs/remotes/origin/main, refs/remotes/origin/master); else the
// local main, then master; else the current HEAD. One for-each-ref answers
// all but the last.
//
// Head is the commit the chosen ref points at, so whenever the clone has a
// remote-tracking ref for the branch, Head is upstream's commit as of the
// last fetch, not a local branch's, which may lag behind it, hold commits
// that never landed, or not exist (git clone -b). Two clones in the same
// state get the same Head whether or not they have origin/HEAD, which git
// leaves out after git init and git push -u. Analysis never fetches, so a
// stale clone gives a stale report either way.
//
// for-each-ref leaves out an origin/HEAD whose target is gone, as when
// upstream renamed its default branch and a prune deleted the old one. A
// symbolic-ref, run only when origin/HEAD is not listed, tells that case
// from a missing origin/HEAD, and it is an error rather than a fall-through
// to a branch upstream no longer has (rule 4).
func discoverWorkTreeBranch(ctx context.Context, git domain.GitRunner, dir string) (branch, head string, err error) {
	const originHEAD = "refs/remotes/origin/HEAD"
	fallback := []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}
	out, err := discoverGit(ctx, git, dir, nil, slices.Concat([]string{"for-each-ref",
		"--format=%(refname)%00%(symref)%00%(objectname)%00%(objecttype)", originHEAD}, fallback)...)
	if err != nil {
		return "", "", err
	}
	refs := make(map[string]discoverRef, 1+len(fallback))
	s := newScanner(bytes.NewReader(out), bufio.ScanLines)
	for s.Scan() {
		f := strings.Split(s.Text(), "\x00")
		if len(f) != 4 {
			return "", "", fmt.Errorf("git for-each-ref: unexpected line %q", s.Text())
		}
		// A pattern also matches refs below it, such as refs/heads/main/x
		// when there is no refs/heads/main; the map keeps exact names.
		refs[f[0]] = discoverRef{symref: f[1], sha: f[2], typ: f[3]}
	}
	if err := s.Err(); err != nil {
		return "", "", fmt.Errorf("read git for-each-ref output: %w", err)
	}
	r, listed := refs[originHEAD]
	if listed && r.symref != "" {
		return discoverCommit(discoverBranchName(r.symref), r)
	}
	if !listed {
		// --quiet: a missing or non-symbolic ref exits 1 with no output.
		out, err := discoverGit(ctx, git, dir, []int{1}, "symbolic-ref", "--quiet", originHEAD)
		if err != nil {
			return "", "", err
		}
		if target := strings.TrimSuffix(string(out), "\n"); target != "" {
			return "", "", fmt.Errorf("origin/HEAD points at %s, which does not exist: upstream renamed or deleted its default branch (git remote set-head origin --auto repairs it)",
				strings.TrimPrefix(target, "refs/remotes/"))
		}
	}
	for _, ref := range fallback {
		if r, ok := refs[ref]; ok {
			return discoverCommit(discoverBranchName(ref), r)
		}
	}
	return discoverHEAD(ctx, git, dir)
}

// discoverCommit checks that the ref chosen for the default branch points at
// a commit, and returns the branch and that commit.
func discoverCommit(branch string, r discoverRef) (string, string, error) {
	if r.typ != "commit" {
		return "", "", fmt.Errorf("default branch %s points at a %s, not a commit", branch, r.typ)
	}
	if !discoverIsSHA(r.sha) {
		return "", "", fmt.Errorf("default branch %s: git printed %q, not a full SHA", branch, r.sha)
	}
	return branch, r.sha, nil
}

// discoverHEAD resolves HEAD: the default branch of a bare repository, which
// stillhere sync re-points at the upstream default branch, and a work tree's
// last resort. The branch is HEAD's target with refs/heads/ removed, which
// git symbolic-ref --short also prints unless a tag has the same name; it is
// "HEAD" when HEAD is detached. A branch with no commit yet, as in a new or
// empty repository, is an error.
func discoverHEAD(ctx context.Context, git domain.GitRunner, dir string) (branch, head string, err error) {
	// --quiet: a detached HEAD exits 1 with no output instead of failing.
	out, err := discoverGit(ctx, git, dir, []int{1}, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", "", err
	}
	branch = "HEAD"
	if ref := strings.TrimSuffix(string(out), "\n"); ref != "" {
		branch = discoverBranchName(ref)
	}
	// --verify --quiet: a HEAD that names no commit exits 1 with no output.
	out, err = discoverGit(ctx, git, dir, []int{1}, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return "", "", err
	}
	head = strings.TrimSuffix(string(out), "\n")
	switch {
	case head == "" && branch == "HEAD":
		return "", "", errors.New("HEAD does not resolve to a commit")
	case head == "":
		return "", "", fmt.Errorf("default branch %s has no commits", branch)
	case !discoverIsSHA(head):
		return "", "", fmt.Errorf("default branch %s: git printed %q, not a full SHA", branch, head)
	}
	return branch, head, nil
}

// discoverBranchName shortens a full ref name to a branch name:
// refs/heads/main and refs/remotes/origin/main are both main.
func discoverBranchName(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/remotes/origin/", "refs/remotes/", "refs/"} {
		if name, ok := strings.CutPrefix(ref, prefix); ok {
			return name
		}
	}
	return ref
}

// discoverIsSHA reports whether s is a full object name: 40 hex digits
// (SHA-1) or 64 (SHA-256), in lowercase as git prints them.
func discoverIsSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// discoverName is a repository's name: its directory's basename without a
// trailing .git, so a mirror widget.git and a clone widget share it.
func discoverName(path string) string {
	base := filepath.Base(path)
	if name := strings.TrimSuffix(base, ".git"); name != "" {
		return name
	}
	return base
}

// discoverKey is a repository's dedupe and storage key: its scrubbed origin
// URL, or "local:" plus its name when it has no origin or the origin does
// not parse. redact.Unparsed is never a key, so two repositories whose
// remotes do not parse are not taken for one by that alone. Two such
// repositories with the same name do share a key, so the second is skipped
// as a duplicate: a known limitation of the key, which discovery.md (rule 5)
// states.
func discoverKey(remote, name string) string {
	if remote == "" || remote == redact.Unparsed {
		return "local:" + name
	}
	return remote
}

// discoverDedupe keeps one repository per Key (rule 5): the one with the
// lexicographically first path. Every other path is a duplicate-repo
// warning naming both paths and the shared remote. discoverDistinct has
// already made one directory reached twice into one candidate, so two
// repositories here are always two directories. The result is sorted by Key.
func discoverDedupe(repos []domain.Repo) ([]domain.Repo, []domain.Warning) {
	slices.SortFunc(repos, func(a, b domain.Repo) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Path, b.Path))
	})
	var (
		kept     []domain.Repo
		warnings []domain.Warning
	)
	for _, r := range repos {
		if n := len(kept); n > 0 && kept[n-1].Key == r.Key {
			warnings = append(warnings, discoverDuplicate(kept[n-1], r))
			continue
		}
		kept = append(kept, r)
	}
	return kept, warnings
}

// discoverDuplicate is the warning for skipped, a duplicate of kept. Its
// message holds only the two local paths and the scrubbed remote, or the
// key when there is no usable remote, as in
// "skipped /abs/b; same remote as /abs/a (https://github.com/o/r.git)".
func discoverDuplicate(kept, skipped domain.Repo) domain.Warning {
	msg := fmt.Sprintf("skipped %s; same remote as %s (%s)", skipped.Path, kept.Path, kept.Remote)
	if kept.Key != kept.Remote {
		msg = fmt.Sprintf("skipped %s; same name as %s and neither has a usable origin (%s)", skipped.Path, kept.Path, kept.Key)
	}
	return domain.Warning{Code: discoverDuplicateRepo, Message: msg, Repo: kept.Key}
}

// discoverHealth warns about each kept repository whose clone cannot give
// every fact (discovery.md#clone-health). The collectors read Shallow and
// Partial; the warnings tell the reader why facts are missing.
func discoverHealth(repos []domain.Repo) []domain.Warning {
	var warnings []domain.Warning
	for _, r := range repos {
		if r.Shallow {
			warnings = append(warnings, domain.Warning{
				Code:    discoverShallowClone,
				Message: r.Path + " is a shallow clone: no blame facts, and history stops at the shallow boundary (git fetch --unshallow fixes it)",
				Repo:    r.Key,
			})
		}
		if r.Partial {
			warnings = append(warnings, domain.Warning{
				Code:    discoverPartialClone,
				Message: r.Path + " is a partial clone: commit facts only, no line facts, because blobs it lacks are never fetched",
				Repo:    r.Key,
			})
		}
	}
	return warnings
}

// discoverFailure records why path gave no repository. The message is
// scrubbed and cut to one stored line, because it may carry git's stderr.
func discoverFailure(path string, err error) domain.FileError {
	return domain.FileError{Path: path, Message: redact.Message(err.Error())}
}

// discoverGit runs one discovery command in dir under the Analysis profile,
// with the discovery timeout. Every output is a few lines, so it is
// buffered.
func discoverGit(ctx context.Context, git domain.GitRunner, dir string, okExit []int, args ...string) ([]byte, error) {
	return git.Run(ctx, domain.GitCmd{Dir: dir, Args: args, Timeout: discoverTimeout, OKExit: okExit})
}
