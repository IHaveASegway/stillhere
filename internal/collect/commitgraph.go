package collect

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// ChangedPathFilters reports whether repo has a commit-graph with
// changed-path Bloom filters (#56): git's own index, which lets blame skip
// the commits that did not touch the file it reads. Blame on a clone
// without one took 2.85 times as long on the same facts (#56), and a fresh
// git clone has none. Sync writes one into every mirror; a scan never
// writes into a clone, so a scan whose blame was slow can only say how
// (app's commit-graph hint).
//
// It asks git where the graph lives (rev-parse --git-path, which follows a
// relocated object directory) and reads the graph's chunk table of
// contents, the first few hundred bytes of one file, for the BIDX chunk
// that holds the filters (gitformat-commit-graph(5)). For a chain of
// graphs it reads the newest one, the last line of the chain file, which
// holds the commits written last. A missing or unreadable graph is false,
// never an error: the answer only decides whether to print a hint.
func ChangedPathFilters(ctx context.Context, git domain.GitRunner, repo domain.Repo) (bool, error) {
	out, err := git.Run(ctx, domain.GitCmd{
		Dir: repo.Path,
		Args: []string{"rev-parse",
			"--git-path", "objects/info/commit-graph",
			"--git-path", "objects/info/commit-graphs/commit-graph-chain"},
	})
	if err != nil {
		return false, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return false, nil
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(repo.Path, p)
	}
	single, chain := abs(lines[0]), abs(lines[1])
	if commitGraphHasFilters(single) {
		return true, nil
	}
	newest := commitGraphNewest(chain)
	if newest == "" {
		return false, nil
	}
	return commitGraphHasFilters(filepath.Join(filepath.Dir(chain), "graph-"+newest+".graph")), nil
}

// commitGraphNewest returns the hash of the newest graph a commit-graph
// chain file lists, its last line, or "" when there is none.
func commitGraphNewest(chain string) string {
	f, err := os.Open(chain)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	last := ""
	sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			last = line
		}
	}
	// A graph's name is its hash in hex; anything else is not one.
	if last == "" || strings.Trim(last, "0123456789abcdef") != "" {
		return ""
	}
	return last
}

// commitGraphHasFilters reports whether the commit-graph file at path holds
// a BIDX chunk. The file starts with the signature "CGPH", a version byte,
// a hash version byte, the number of chunks C and the number of base
// graphs, then a table of C + 1 entries of a 4-byte chunk id and an 8-byte
// offset, the last with id 0.
func commitGraphHasFilters(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head[:4], []byte("CGPH")) {
		return false
	}
	table := make([]byte, 12*(int(head[6])+1))
	if _, err := io.ReadFull(f, table); err != nil {
		return false
	}
	for i := 0; i+4 <= len(table); i += 12 {
		if bytes.Equal(table[i:i+4], []byte("BIDX")) {
			return true
		}
	}
	return false
}
