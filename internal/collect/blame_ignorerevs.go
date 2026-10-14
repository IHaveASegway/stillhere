package collect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// blameIgnoreRevsName is the ignore-revs file at a repository's root: the
// one GitHub's blame view honours.
const blameIgnoreRevsName = ".git-blame-ignore-revs"

// blameIgnoreRevsMax caps the ignore-revs file Blame reads. 1 MiB holds
// about 25,000 commit IDs, far more than real files list, and it keeps a
// stray large file from being buffered whole.
const blameIgnoreRevsMax = 1 << 20

// blameIgnoreRevsCode is the code of the warning Blame gives when the
// repository's own ignore-revs file was not used, or not whole.
const blameIgnoreRevsCode = "ignore-revs"

// blameIgnore is the ignore-revs list Blame resolved for one repository.
type blameIgnore struct {
	revs    domain.IgnoreRevs
	warning string // why the root file was not used, or not whole; "" when it was, or there is none
}

// blameIgnoreRevs resolves the ignore-revs list for repo
// (docs/spec/blame-collector.md#ignore-revs-per-repository): cliFile when it
// is set, else the repository's own .git-blame-ignore-revs at Head, else
// none. It parses the file with git's rules, then resolves the IDs to
// commits as git does, so that Revs is exactly the set of commits git blame
// ignores.
//
// A problem with cliFile is a *domain.UsageError: the user named the file
// and can fix it. A problem with the root file, which the user may not
// control, leaves the rest of the repository's blame intact and becomes the
// warning. Lines git would reject are skipped, because git would refuse the
// whole file and fail every blame in the repository. A file that is not a
// regular file, is over 1 MiB, or cannot be read (a partial clone may lack
// its blob) is not used. The error it returns is for the repository as a
// whole: Head's tree cannot be listed, the IDs cannot be resolved, or ctx
// ended.
func blameIgnoreRevs(ctx context.Context, git domain.GitRunner, repo domain.Repo, cliFile string) (blameIgnore, error) {
	var (
		ig  blameIgnore
		err error
	)
	if cliFile != "" {
		ig, err = blameIgnoreRevsCLI(cliFile, len(repo.Head))
	} else {
		ig, err = blameIgnoreRevsRoot(ctx, git, repo)
	}
	if err != nil || len(ig.revs.Revs) == 0 {
		return ig, err
	}
	ig.revs.Revs, err = blamePeelCommits(ctx, git, repo, ig.revs.Revs)
	if err != nil {
		return blameIgnore{}, err
	}
	return ig, nil
}

// blameIgnoreRevsCLI reads the --ignore-revs-file override.
func blameIgnoreRevsCLI(path string, hexLen int) (blameIgnore, error) {
	data, err := blameReadFile(path)
	if err != nil {
		return blameIgnore{}, &domain.UsageError{Msg: fmt.Sprintf("--ignore-revs-file: %v", err)}
	}
	revs, bad := blameParseIgnoreRevs(data, hexLen)
	if len(bad) > 0 {
		return blameIgnore{}, &domain.UsageError{Msg: fmt.Sprintf("--ignore-revs-file %s: %s", path, blameBadLines(bad, hexLen))}
	}
	return blameIgnore{revs: domain.IgnoreRevs{
		Source: domain.IgnoreRevsCLI,
		SHA256: blameSHA256(data),
		Revs:   revs,
	}}, nil
}

// blameReadFile reads a local file of at most blameIgnoreRevsMax bytes. Its
// errors name the file: os.Open's and Read's are *os.PathError values that
// already do.
func blameReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: Close cannot lose data
	data, err := io.ReadAll(io.LimitReader(f, blameIgnoreRevsMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > blameIgnoreRevsMax {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, blameIgnoreRevsMax)
	}
	return data, nil
}

// blameIgnoreRevsRoot reads the repository's own .git-blame-ignore-revs at
// Head. It asks ls-tree first, because ls-tree prints nothing and exits 0
// for a missing path, while cat-file -e and git show exit 128 both for a
// missing path and for a broken repository (verified on 2.50.1). The format
// leaves out %(objectsize): it needs the blob, so in a partial clone that
// lacks the blob, ls-tree fails outright (verified). The blob is then read
// by its object ID, which cannot change between the two commands, and
// reading stops at the 1 MiB cap.
func blameIgnoreRevsRoot(ctx context.Context, git domain.GitRunner, repo domain.Repo) (blameIgnore, error) {
	out, err := git.Run(ctx, domain.GitCmd{
		Dir: repo.Path,
		Args: []string{"ls-tree", "--full-tree",
			"--format=%(objectmode) %(objecttype) %(objectname)",
			repo.Head, "--", blameIgnoreRevsName},
	})
	if err != nil {
		return blameIgnore{}, fmt.Errorf("look for %s: %w", blameIgnoreRevsName, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return blameIgnore{}, nil // the repository has none
	}
	if len(fields) != 3 {
		return blameIgnore{}, fmt.Errorf("look for %s: unexpected ls-tree output", blameIgnoreRevsName)
	}
	mode, kind, oid := fields[0], fields[1], fields[2]
	if kind != "blob" || (mode != "100644" && mode != "100755") {
		return blameIgnoreNotUsed(fmt.Sprintf("not a regular file (mode %s)", mode)), nil
	}
	data, over, err := blameReadBlob(ctx, git, repo, oid)
	switch {
	case err != nil && ctx.Err() != nil:
		return blameIgnore{}, fmt.Errorf("read %s: %w", blameIgnoreRevsName, err)
	case err != nil:
		return blameIgnoreNotUsed("unreadable: " + err.Error()), nil
	case over:
		return blameIgnoreNotUsed(fmt.Sprintf("over the %d-byte limit", blameIgnoreRevsMax)), nil
	}
	revs, bad := blameParseIgnoreRevs(data, len(repo.Head))
	ig := blameIgnore{revs: domain.IgnoreRevs{
		Source: domain.IgnoreRevsRoot,
		SHA256: blameSHA256(data),
		Revs:   revs,
	}}
	if len(bad) > 0 {
		ig.warning = blameIgnoreRevsName + ": " + blameBadLines(bad, len(repo.Head)) + "; skipped"
	}
	return ig, nil
}

// blameIgnoreNotUsed is the outcome for a root file Blame does not use.
// The verdict leads, so it survives when the message is truncated.
func blameIgnoreNotUsed(reason string) blameIgnore {
	return blameIgnore{warning: blameIgnoreRevsName + " not used: " + reason}
}

// blameReadBlob reads the blob oid, up to blameIgnoreRevsMax bytes. over
// reports a larger blob: reading stops at the cap, and the reader's error
// stops git, so a stray huge file is never read whole.
func blameReadBlob(ctx context.Context, git domain.GitRunner, repo domain.Repo, oid string) (data []byte, over bool, err error) {
	err = git.Stream(ctx, domain.GitCmd{Dir: repo.Path, Args: []string{"cat-file", "blob", oid}},
		func(r io.Reader) error {
			var err error
			data, err = io.ReadAll(io.LimitReader(r, blameIgnoreRevsMax+1))
			if err == nil && len(data) > blameIgnoreRevsMax {
				over = true
				return errors.New("over the limit")
			}
			return err
		})
	if over {
		return nil, true, nil
	}
	return data, false, err
}

// blameParseIgnoreRevs reads an ignore-revs file with git's own rules
// (oidset_parse_file_carefully in git's oidset.c): everything from a "#" to
// the end of its line is a comment, spaces, tabs and line endings around a
// name are trimmed, empty lines are skipped, and every other line must be
// one full object ID of the repository's hash, hexLen digits in either
// case. It returns the IDs lowercased, sorted and without duplicates (nil
// when there are none), and the 1-based numbers of the lines git would
// reject.
func blameParseIgnoreRevs(data []byte, hexLen int) (revs []string, bad []int) {
	n := 0
	for line := range bytes.Lines(data) {
		n++
		if i := bytes.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = bytes.Trim(line, " \t\r\n")
		if len(line) == 0 {
			continue
		}
		id := strings.ToLower(string(line))
		if !blameIsObjectID(id, hexLen) {
			bad = append(bad, n)
			continue
		}
		revs = append(revs, id)
	}
	slices.Sort(revs)
	return slices.Compact(revs), bad
}

// blamePeelCommits returns the commits git blame ignores when an
// ignore-revs file lists ids. git resolves each ID with peel_to_commit_oid
// (git's builtin/blame.c): an annotated tag becomes the commit it tags, and
// an ID that names no commit, such as a tree, a blob or an object the
// repository lacks, is dropped without a word. One cat-file --batch-check
// over "<id>^{commit}" lines applies the same rule to every ID (verified on
// 2.50.1). The result is sorted and without duplicates, nil when no ID
// names a commit.
func blamePeelCommits(ctx context.Context, git domain.GitRunner, repo domain.Repo, ids []string) ([]string, error) {
	var in strings.Builder
	for _, id := range ids {
		in.WriteString(id)
		in.WriteString("^{commit}\n")
	}
	var commits []string
	err := git.Stream(ctx, domain.GitCmd{
		Dir:   repo.Path,
		Args:  []string{"cat-file", "--batch-check=%(objectname) %(objecttype)"},
		Stdin: strings.NewReader(in.String()),
	}, func(r io.Reader) error {
		sc := newScanner(r, bufio.ScanLines)
		for sc.Scan() {
			name, kind, _ := strings.Cut(sc.Text(), " ")
			switch {
			case kind == "commit" && blameIsObjectID(name, len(repo.Head)):
				commits = append(commits, name)
			case kind == "missing":
				// "<id>^{commit} missing": no commit, as git sees it.
			default:
				return errors.New("unexpected cat-file output")
			}
		}
		return sc.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("resolve the %d ignore-revs IDs to commits: %w", len(ids), err)
	}
	slices.Sort(commits)
	return slices.Compact(commits), nil
}

// blameIsObjectID reports whether s is a full object ID of hexLen lowercase
// hexadecimal digits: 40 for SHA-1, 64 for SHA-256.
func blameIsObjectID(s string, hexLen int) bool {
	if len(s) != hexLen {
		return false
	}
	for i := range len(s) {
		if !blameIsHex(s[i]) {
			return false
		}
	}
	return true
}

// blameBadLines says which lines of an ignore-revs file are not full
// commit IDs.
func blameBadLines(bad []int, hexLen int) string {
	if len(bad) == 1 {
		return fmt.Sprintf("line %d is not a full commit ID (%d hex digits)", bad[0], hexLen)
	}
	return fmt.Sprintf("%d lines are not full commit IDs (%d hex digits), the first at line %d", len(bad), hexLen, bad[0])
}

// blameSHA256 returns the hex SHA-256 of data.
func blameSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// blameWriteIgnoreRevs writes revs, one per line, to a new file for
// --ignore-revs-file, and returns its absolute path and a function that
// removes it. git reads the file by path, and analysis never writes into
// the repository it measures (docs/spec/git-invocation.md), so the file is
// 0600 inside a new 0700 directory under the system temp directory. The
// path must be absolute: git runs with -C <repo>, so it would resolve a
// relative one, from a relative $TMPDIR, against the repository and fail
// every blame. Writing the resolved list, rather than the file as
// committed, makes the commits git ignores exactly the ones
// IgnoreRevs.Revs records.
func blameWriteIgnoreRevs(revs []string) (path string, remove func(), err error) {
	dir, err := os.MkdirTemp("", "stillhere-blame-")
	if err != nil {
		return "", nil, fmt.Errorf("create a temp directory for the ignore-revs list: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("create a temp directory for the ignore-revs list: %w", err)
	}
	remove = func() { _ = os.RemoveAll(abs) }
	path = filepath.Join(abs, "ignore-revs")
	if err := os.WriteFile(path, []byte(strings.Join(revs, "\n")+"\n"), 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("write the ignore-revs list: %w", err)
	}
	return path, remove, nil
}
