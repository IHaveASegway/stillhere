package collect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// blameIgnore is the ignore-revs file Blame resolved for one repository.
type blameIgnore struct {
	revs  domain.IgnoreRevs
	issue *domain.FileError // the root file was not used, or not whole; nil when it was
}

// blameIgnoreRevs resolves the ignore-revs file for repo
// (docs/spec/blame-collector.md#ignore-revs-per-repository): cliFile when it
// is set, else the repository's own .git-blame-ignore-revs at Head, else
// none.
//
// A problem with cliFile is a *domain.UsageError: the user named the file
// and can fix it. A problem with the root file, which the user may not
// control, leaves the rest of the repository's blame intact, and is
// reported as a FileError on the file's path. Lines git would reject are
// skipped (git would refuse the whole file, failing every blame in the
// repository), and a file that is not a regular file, or is over 1 MiB, is
// not used.
func blameIgnoreRevs(ctx context.Context, git domain.GitRunner, repo domain.Repo, cliFile string) (blameIgnore, error) {
	if cliFile != "" {
		return blameIgnoreRevsCLI(cliFile, len(repo.Head))
	}
	return blameIgnoreRevsRoot(ctx, git, repo)
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

// blameReadFile reads a local file of at most blameIgnoreRevsMax bytes.
func blameReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: Close cannot lose data
	data, err := io.ReadAll(io.LimitReader(f, blameIgnoreRevsMax+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > blameIgnoreRevsMax {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, blameIgnoreRevsMax)
	}
	return data, nil
}

// blameIgnoreRevsRoot reads the repository's own .git-blame-ignore-revs at
// Head. It asks ls-tree first, because ls-tree prints nothing and exits 0
// for a missing path, while cat-file -e and git show exit 128 both for a
// missing path and for a broken repository (verified on 2.50.1). It then
// reads the blob by its object ID, which cannot change between the two
// commands.
func blameIgnoreRevsRoot(ctx context.Context, git domain.GitRunner, repo domain.Repo) (blameIgnore, error) {
	out, err := git.Run(ctx, domain.GitCmd{
		Dir: repo.Path,
		Args: []string{"ls-tree", "--full-tree",
			"--format=%(objectmode) %(objecttype) %(objectname) %(objectsize)",
			repo.Head, "--", blameIgnoreRevsName},
	})
	if err != nil {
		return blameIgnore{}, fmt.Errorf("look for %s: %w", blameIgnoreRevsName, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return blameIgnore{}, nil // the repository has none
	}
	if len(fields) != 4 {
		return blameIgnore{}, fmt.Errorf("look for %s: unexpected ls-tree output", blameIgnoreRevsName)
	}
	mode, kind, oid := fields[0], fields[1], fields[2]
	if kind != "blob" || (mode != "100644" && mode != "100755") {
		return blameIgnore{issue: &domain.FileError{
			Path:    blameIgnoreRevsName,
			Message: fmt.Sprintf("not a regular file (mode %s); not used", mode),
		}}, nil
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return blameIgnore{}, fmt.Errorf("look for %s: unexpected ls-tree size", blameIgnoreRevsName)
	}
	if size > blameIgnoreRevsMax {
		return blameIgnore{issue: &domain.FileError{
			Path:    blameIgnoreRevsName,
			Message: fmt.Sprintf("%d bytes, over the %d-byte limit; not used", size, blameIgnoreRevsMax),
		}}, nil
	}
	data, err := git.Run(ctx, domain.GitCmd{Dir: repo.Path, Args: []string{"cat-file", "blob", oid}})
	if err != nil {
		return blameIgnore{}, fmt.Errorf("read %s: %w", blameIgnoreRevsName, err)
	}
	revs, bad := blameParseIgnoreRevs(data, len(repo.Head))
	ig := blameIgnore{revs: domain.IgnoreRevs{
		Source: domain.IgnoreRevsRoot,
		SHA256: blameSHA256(data),
		Revs:   revs,
	}}
	if len(bad) > 0 {
		ig.issue = &domain.FileError{
			Path:    blameIgnoreRevsName,
			Message: blameBadLines(bad, len(repo.Head)) + "; skipped",
		}
	}
	return ig, nil
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
// --ignore-revs-file, and returns its path and a function that removes it.
// git reads the file by path, and analysis never writes into the repository
// it measures (docs/spec/git-invocation.md), so the file is 0600 inside a
// new 0700 directory under the system temp directory. Writing the parsed
// list, rather than the file as committed, makes the commits git ignores
// exactly the ones IgnoreRevs.Revs records.
func blameWriteIgnoreRevs(revs []string) (path string, remove func(), err error) {
	dir, err := os.MkdirTemp("", "stillhere-blame-")
	if err != nil {
		return "", nil, fmt.Errorf("create a temp directory for the ignore-revs list: %w", err)
	}
	remove = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "ignore-revs")
	if err := os.WriteFile(path, []byte(strings.Join(revs, "\n")+"\n"), 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("write the ignore-revs list: %w", err)
	}
	return path, remove, nil
}
