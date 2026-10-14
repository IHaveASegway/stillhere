package collect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// mapLandings fills h.maps with each window landing's commits: the commits
// git rev-list L^1..L prints, the landing included (docs/metrics.md#units).
// A non-merge landing maps to itself, and so does a root commit, on which
// rev-list L^1..L fails because L^1 does not exist.
//
// The spec states the map as one rev-list per landing. One walk gives the
// same answer for every landing at once, so a repository with a thousand
// merge landings starts one git process instead of a thousand:
//
//  1. git rev-list --parents N ^O^1 prints the commits reachable from the
//     newest landing N but not from the oldest landing O's first parent,
//     with their parents. (Just N when O is a root.)
//  2. The first parents from N down to O are the first-parent chain, every
//     commit of it, in the window or not.
//  3. Going up the chain from O, each chain commit C claims every commit
//     reachable from it that no older chain commit claimed: a depth-first
//     search through parents that stops at claimed commits and at commits
//     the walk left out.
//
// What C claims is exactly rev-list C^1..C. By induction, the chain commit
// below C, which is C^1, has claimed everything reachable from it inside
// the walk; reachability from C^1 is closed under parents, so the search
// from C never needs to pass a claimed commit; and the walk left out only
// commits reachable from O^1, which C^1 also reaches. Claims of chain
// commits outside the window, such as one whose clock was wrong, are
// discarded rather than given to a landing.
func (h *historyRun) mapLandings(ctx context.Context) error {
	if len(h.landings) == 0 {
		return nil
	}
	h.maps = make(map[string][]string, len(h.landings))
	merges := false
	for _, sha := range h.landings {
		h.maps[sha] = []string{sha}
		merges = merges || len(h.commits[sha].commit.Parents) > 1
	}
	if !merges {
		return nil // every landing brought in only itself
	}

	newest := h.landings[0] // git log --first-parent prints the chain from Head down
	oldest := h.commits[h.landings[len(h.landings)-1]].commit
	args := []string{"rev-list", "--parents", newest}
	if len(oldest.Parents) > 0 {
		args = append(args, "^"+oldest.Parents[0])
	}
	args = append(args, "--")

	parents := make(map[string][]string)
	err := h.git.Stream(ctx, domain.GitCmd{Dir: h.repo.Path, Args: args}, func(r io.Reader) error {
		s := newScanner(r, bufio.ScanLines)
		for s.Scan() {
			f := strings.Fields(s.Text())
			if len(f) == 0 || slices.ContainsFunc(f, func(x string) bool { return !historySHA(x) }) {
				return errors.New("malformed rev-list --parents line")
			}
			var ps []string // nil for a root
			if len(f) > 1 {
				ps = f[1:]
			}
			parents[f[0]] = ps
		}
		return s.Err()
	})
	if err != nil {
		return fmt.Errorf("mapping landings to commits: %w", err)
	}

	var chain []string
	for c := newest; ; {
		chain = append(chain, c)
		if c == oldest.SHA {
			break
		}
		ps := parents[c]
		if len(ps) == 0 {
			return fmt.Errorf("mapping landings to commits: the first-parent chain from %s does not reach %s", newest, oldest.SHA)
		}
		c = ps[0]
	}

	owner := make(map[string]string, len(parents))
	for i := len(chain) - 1; i >= 0; i-- {
		top := chain[i]
		stack := []string{top}
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, claimed := owner[c]; claimed {
				continue
			}
			owner[c] = top
			for _, p := range parents[c] {
				_, walked := parents[p]
				if _, claimed := owner[p]; walked && !claimed {
					stack = append(stack, p)
				}
			}
		}
	}

	for _, sha := range h.landings {
		h.maps[sha] = nil
	}
	for c, top := range owner {
		if _, landing := h.maps[top]; landing {
			h.maps[top] = append(h.maps[top], c)
		}
	}
	for _, sha := range h.landings {
		slices.Sort(h.maps[sha])
	}
	return nil
}
