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
	Failed   []domain.FileError // paths that gave no repository, sorted by Path: a PATH slot that is neither a repo nor a group, or a repo git cannot read or whose default branch has no commit
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
	// them the git processes discovery runs: each repository runs its
	// commands one after another, three for a work tree with origin/HEAD,
	// main or master, four for a bare repository, five for a work tree
	// that falls back to HEAD.
	discoverMaxJobs = 8
)

// discoverKind is what a directory is, as far as the filesystem can tell
// before git is asked.
type discoverKind int

const (
	discoverOther    discoverKind = iota // neither: a PATH slot like this is a group
	discoverWorkTree                     // has a .git file or directory (rule 1)
	discoverBare                         // has HEAD, objects and refs (rule 2)
)

// discoverCandidate is a directory that looks like a repository, waiting for
// git to confirm it.
type discoverCandidate struct {
	path  string // absolute
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
// only with the layout git itself looks for (a .git entry, or HEAD with
// objects and refs), and git must then place the repository exactly there.
// So a plain directory inside another work tree is never taken for a
// repository, and a candidate git cannot read is a failure rather than "not
// a repository": a directory with an unusable .git fails instead of
// becoming a group.
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
		cands     []discoverCandidate
		groupDirs []string
	)
	for _, slot := range slots {
		kind, err := discoverSlotKind(slot)
		switch {
		case err != nil:
			failed = append(failed, discoverFailure(slot, err))
		case kind == discoverOther:
			groupDirs = append(groupDirs, slot)
		default:
			cands = append(cands, discoverCandidate{path: slot, kind: kind})
		}
	}
	groups, members, memberFailed := discoverGroups(groupDirs)
	cands = append(cands, members...)
	failed = append(failed, memberFailed...)

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
	return &Discovery{Repos: kept, Groups: groups, Warnings: warnings, Failed: failed}, nil
}

// discoverSlots makes each command-line path absolute and drops repeats of
// the same absolute path, which name one slot. The result is sorted, so that
// group suffixes and duplicate winners follow path order, never argument
// order.
func discoverSlots(paths []string) (slots []string, failed []domain.FileError) {
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" {
			failed = append(failed, domain.FileError{Path: p, Message: "empty path"})
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			failed = append(failed, discoverFailure(p, err))
			continue
		}
		if !seen[abs] {
			seen[abs] = true
			slots = append(slots, abs)
		}
	}
	slices.Sort(slots)
	return slots, failed
}

// discoverSlotKind says what a PATH slot is before git is asked: a
// repository candidate, or discoverOther for a directory, which is a group.
// A path that is not a directory is an error (rule 4).
func discoverSlotKind(path string) (discoverKind, error) {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, errors.New("does not exist")
	case err != nil:
		return 0, err
	case !fi.IsDir():
		return 0, errors.New("not a directory, so neither a repository nor a group")
	}
	return discoverShape(path)
}

// discoverShape reports whether the directory dir has the layout of a
// repository: a .git entry, which a work tree, a linked worktree and a
// submodule checkout all have (rule 1, checked first), or the HEAD, objects
// and refs that git's own setup looks for in a git directory, which a bare
// repository has (rule 2).
func discoverShape(dir string) (discoverKind, error) {
	ok, err := discoverExists(filepath.Join(dir, ".git"), false)
	if err != nil {
		return 0, err
	}
	if ok {
		return discoverWorkTree, nil
	}
	for _, entry := range []struct {
		name string
		dir  bool
	}{{"HEAD", false}, {"objects", true}, {"refs", true}} {
		ok, err := discoverExists(filepath.Join(dir, entry.name), entry.dir)
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
func discoverGroups(dirs []string) ([]domain.Group, []discoverCandidate, []domain.FileError) {
	type listed struct {
		dir     string
		members []discoverCandidate
	}
	var (
		ok     []listed
		failed []domain.FileError
	)
	for _, dir := range dirs {
		members, memberFailed, err := discoverMembers(dir)
		if err != nil {
			failed = append(failed, discoverFailure(dir, err))
			continue
		}
		ok = append(ok, listed{dir, members})
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
// that look like repositories. A child whose layout cannot be read is
// reported, since it may be a repository.
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
		child := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			if e.Type()&fs.ModeSymlink == 0 {
				continue
			}
			if fi, err := os.Stat(child); err != nil || !fi.IsDir() {
				continue // a link to a file, or a dangling link
			}
		}
		kind, err := discoverShape(child)
		switch {
		case err != nil:
			failed = append(failed, discoverFailure(child, err))
		case kind != discoverOther:
			members = append(members, discoverCandidate{path: child, kind: kind})
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
// to know about it: whether it is shallow, its scrubbed origin, whether it
// is a partial clone, and its default branch and Head. The raw origin URL
// lives only in this function; the Repo holds redact.URL's form of it.
func discoverRepo(ctx context.Context, git domain.GitRunner, c discoverCandidate) (domain.Repo, error) {
	shallow, err := discoverProbe(ctx, git, c)
	if err != nil {
		return domain.Repo{}, err
	}
	origin, partial, err := discoverConfig(ctx, git, c.path)
	if err != nil {
		return domain.Repo{}, err
	}
	var branch, head string
	if c.kind == discoverBare {
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
		Bare:    c.kind == discoverBare,
		Remote:  remote,
		Branch:  branch,
		Head:    head,
		Shallow: shallow,
		Partial: partial,
	}, nil
}

// discoverProbe asks git, in one rev-parse, to confirm the candidate and
// whether it is a shallow clone, which it reports. A work tree must be the
// top of its own work tree, with an empty --show-prefix, because git walks
// up past a .git it cannot use and answers for an enclosing work tree. A
// bare repository must be its own git directory, because in a directory
// inside a bare repository git answers for that repository.
func discoverProbe(ctx context.Context, git domain.GitRunner, c discoverCandidate) (bool, error) {
	if c.kind == discoverBare {
		out, err := discoverGit(ctx, git, c.path, nil,
			"rev-parse", "--is-bare-repository", "--is-shallow-repository", "--absolute-git-dir")
		if err != nil {
			return false, err
		}
		bare, shallowAnswer, gitDir := discoverAnswers(out)
		if bare != "true" {
			return false, errors.New("a git directory that is not a bare repository; name its work tree instead")
		}
		same, err := discoverSameDir(gitDir, c.path)
		if err != nil {
			return false, err
		}
		if !same {
			return false, errors.New("inside a bare repository rather than one itself")
		}
		return shallowAnswer == "true", nil
	}
	out, err := discoverGit(ctx, git, c.path, nil,
		"rev-parse", "--is-inside-work-tree", "--is-shallow-repository", "--show-prefix")
	if err != nil {
		return false, err
	}
	inside, shallowAnswer, prefix := discoverAnswers(out)
	switch {
	case inside != "true":
		return false, errors.New("has a .git, but git sees no work tree here")
	case prefix != "":
		return false, errors.New("has a .git that git cannot use: git found an enclosing work tree instead")
	}
	return shallowAnswer == "true", nil
}

// discoverAnswers splits rev-parse output into its first two lines and the
// rest without its final newline. The last answer is a path or a prefix, so
// it is never split further.
func discoverAnswers(out []byte) (first, second, rest string) {
	s := string(out)
	first, s, _ = strings.Cut(s, "\n")
	second, s, _ = strings.Cut(s, "\n")
	return first, second, strings.TrimSuffix(s, "\n")
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
// value. Exit 1 means no key matched. origin is the first
// remote.origin.url, the one git fetches from; "" when there is none.
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
			if !haveOrigin {
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
// (discovery.md#default-branch): the branch origin/HEAD points at, if that
// ref exists and resolves; else main, if refs/heads/main exists; else
// master; else the current HEAD. One for-each-ref answers the first three,
// and it leaves out an origin/HEAD whose target is gone.
//
// Head is the commit the chosen ref points at. For the origin/HEAD rule that
// is refs/remotes/origin/<branch>, the default branch as of the last fetch,
// rather than a local branch of the same name, which may lag behind it,
// carry commits that never landed, or not exist (git clone -b). Analysis
// never fetches, so a stale clone gives a stale report either way.
func discoverWorkTreeBranch(ctx context.Context, git domain.GitRunner, dir string) (branch, head string, err error) {
	out, err := discoverGit(ctx, git, dir, nil, "for-each-ref",
		"--format=%(refname)%00%(symref)%00%(objectname)%00%(objecttype)",
		"refs/remotes/origin/HEAD", "refs/heads/main", "refs/heads/master")
	if err != nil {
		return "", "", err
	}
	refs := make(map[string]discoverRef, 3)
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
	if r, ok := refs["refs/remotes/origin/HEAD"]; ok && r.symref != "" {
		return discoverCommit(discoverBranchName(r.symref), r)
	}
	for _, name := range []string{"main", "master"} {
		if r, ok := refs["refs/heads/"+name]; ok {
			return discoverCommit(name, r)
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
// remotes do not parse are not taken for one.
func discoverKey(remote, name string) string {
	if remote == "" || remote == redact.Unparsed {
		return "local:" + name
	}
	return remote
}

// discoverDedupe keeps one repository per Key (rule 5): the one with the
// lexicographically first path. Every other path is a duplicate-repo
// warning naming both paths and the shared remote. The same path found
// twice, named directly and as a group's member, is one repository named
// twice: it is kept once, in its group, with no warning. The result is
// sorted by Key.
func discoverDedupe(repos []domain.Repo) ([]domain.Repo, []domain.Warning) {
	// named is 1 for a repository named directly, so a group's member sorts
	// before the same path named on its own.
	named := func(r domain.Repo) int {
		if r.Group == "" {
			return 1
		}
		return 0
	}
	slices.SortFunc(repos, func(a, b domain.Repo) int {
		return cmp.Or(
			cmp.Compare(a.Key, b.Key),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(named(a), named(b)),
			cmp.Compare(a.Group, b.Group),
		)
	})
	var (
		kept     []domain.Repo
		warnings []domain.Warning
	)
	for _, r := range repos {
		if n := len(kept); n > 0 && kept[n-1].Key == r.Key {
			if winner := kept[n-1]; winner.Path != r.Path {
				warnings = append(warnings, discoverDuplicate(winner, r))
			}
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
