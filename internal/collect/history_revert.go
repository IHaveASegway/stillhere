package collect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// historyRevertGrep is the pattern git log --grep matches, as an ERE: a
// line that starts with "This reverts commit <hex>", as git revert writes
// it (week-collector.md#subjects-and-reverts).
const historyRevertGrep = "^This reverts commit [0-9a-f]{7,64}"

// historyRevertLine is historyRevertGrep with the SHA captured, matched
// per line of a message, to read the SHA each revert names. A revert of a
// merge continues "..., reversing changes made to <sha>.", and only the
// first SHA is the reverted commit.
var historyRevertLine = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,64})`)

// readReverts links each revert commit of the window to the commit it
// names. It searches the messages of every commit in a window landing's
// map, the landings included, and links the commit whose message carries
// the line (#43: commit grain), so the change metrics can leave out a
// revert and the commit it reverts and still score the rest of the landing
// that brought them. It never walks --first-parent: a pull request merged
// with a merge commit, GitHub's default, lands as a merge whose message
// names no revert, while the revert commit sits on the side branch the
// merge brought in. A squash, rebase or direct revert is a landing that is
// its own revert commit.
//
// It is a query of its own because a message body cannot share a stream
// with numstat: body lines can look like numstat rows. It prints no diff
// and no trailers, so it reads no attribute and needs none of the diff
// options, and -E beats a repository's grep.patternType (verified on
// 2.50.1). It reads the whole message (%B), not only the body (%b), because
// --grep searches the subject too. A full SHA is kept as written, whether or
// not the commit exists here. An abbreviated one is resolved to the one
// commit it names with git cat-file --batch-check, and dropped when it
// names none or several.
func (h *historyRun) readReverts(ctx context.Context) error {
	mapped := make(map[string]bool) // the commits of the window landings' maps
	var want []string
	for _, landing := range h.landings {
		for _, sha := range h.maps[landing] {
			mapped[sha] = true
			want = append(want, sha)
		}
	}
	if len(want) == 0 {
		return nil
	}
	slices.Sort(want)

	args := []string{"log", "--no-walk", "--stdin", "-E", "--grep=" + historyRevertGrep,
		"-z", "--no-color", "--encoding=UTF-8", "--pretty=format:%x00%H%x1f%B", "--"}
	cmd := domain.GitCmd{Dir: h.repo.Path, Args: args, Stdin: historyStdin(want)}

	type link struct{ commit, name string }
	var links []link
	err := h.git.Stream(ctx, cmd, func(r io.Reader) error {
		s := newScanner(r, scanNUL)
		for s.Scan() {
			if s.Text() == "" {
				continue
			}
			sha, message, ok := strings.Cut(s.Text(), "\x1f")
			if !ok || !historySHA(sha) {
				return errors.New("malformed revert record")
			}
			if !mapped[sha] {
				return fmt.Errorf("git log --no-walk printed commit %s, which it was not given", sha)
			}
			for _, m := range historyRevertLine.FindAllStringSubmatch(message, -1) {
				links = append(links, link{commit: sha, name: m[1]})
			}
		}
		return s.Err()
	})
	if err != nil {
		return fmt.Errorf("reading reverts: %w", err)
	}

	var short []string
	for _, l := range links {
		if len(l.name) != len(h.repo.Head) {
			short = append(short, l.name)
		}
	}
	full, err := h.resolveCommits(ctx, short)
	if err != nil {
		return err
	}
	for _, l := range links {
		name := l.name
		if len(name) != len(h.repo.Head) {
			if name = full[name]; name == "" {
				continue
			}
		}
		h.reverts = append(h.reverts, domain.Revert{SHA: l.commit, Reverted: name})
	}
	slices.SortFunc(h.reverts, func(a, b domain.Revert) int {
		return cmp.Or(strings.Compare(a.SHA, b.SHA), strings.Compare(a.Reverted, b.Reverted))
	})
	h.reverts = slices.Compact(h.reverts)
	return nil
}

// resolveCommits maps each abbreviated SHA to the full SHA of the one
// commit it names, with one git cat-file --batch-check. Names that match no
// commit, or several, are absent from the result.
func (h *historyRun) resolveCommits(ctx context.Context, names []string) (map[string]string, error) {
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		return nil, nil
	}
	var in strings.Builder
	for _, n := range names {
		in.WriteString(n + "^{commit}\n") // hex only: historyRevertLine matched it
	}
	out, err := h.git.Run(ctx, domain.GitCmd{
		Dir:   h.repo.Path,
		Args:  []string{"cat-file", "--batch-check=%(objectname) %(objecttype)"},
		Stdin: strings.NewReader(in.String()),
	})
	if err != nil {
		return nil, fmt.Errorf("resolving reverted commits: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(names) {
		return nil, fmt.Errorf("resolving reverted commits: git cat-file answered %d of %d names", len(lines), len(names))
	}
	full := make(map[string]string, len(names))
	for i, line := range lines {
		// "<full sha> commit", or "<name>^{commit} missing" or "... ambiguous".
		if sha, kind, _ := strings.Cut(line, " "); kind == "commit" && historySHA(sha) {
			full[names[i]] = sha
		}
	}
	return full, nil
}
