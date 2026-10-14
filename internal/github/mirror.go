package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// Timeouts of the mirror commands. A first clone of a large repository
// takes minutes; the analysis default (gitcli.DefaultTimeout, 300 s) would
// cut it off.
const (
	cloneTimeout = time.Hour
	fetchTimeout = 30 * time.Minute
	localTimeout = time.Minute // config, symbolic-ref and the checks
)

// headRefspec is the only refspec a mirror fetches: its branches. A
// --mirror clone would fetch +refs/*:refs/*, which on GitHub includes every
// pull request's refs/pull/<n>/head and merge.
const headRefspec = "+refs/heads/*:refs/heads/*"

// newCredentials returns the variables that authenticate git to the base
// URL and nowhere else: one http.<base>/.extraheader entry passed through
// git's environment-scoped config (GIT_CONFIG_COUNT, git 2.31 and later),
// the mechanism actions/checkout uses, minus its config file. Passing the
// same entry with -c would put the token in argv, which ps shows to every
// user; the environment of a process is visible to its own user only, the
// boundary the spec accepts (docs/spec/git-invocation.md#credentials).
// GIT_ASKPASS is empty so that a rejected token fails at once instead of
// reaching for a prompt.
func newCredentials(base, token string) *credentials {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return &credentials{env: []string{
		"GIT_ASKPASS=",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + base + "/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
	}}
}

// Mirror creates the bare mirror of repo at root/<owner>/<name>.git, or
// brings an existing one up to date, and returns its path. It runs exactly
// the commands of docs/spec/git-invocation.md#sync-commands-network:
//
//	git clone --bare <url> <dir>        (first sync only)
//	git -C <dir> config remote.origin.fetch +refs/heads/*:refs/heads/*
//	git -C <dir> fetch --prune origin
//	git -C <dir> symbolic-ref HEAD refs/heads/<default branch>
//
// A first clone goes into a temporary directory beside the mirror and is
// renamed into place only once complete, so an interrupted clone never
// leaves a half-written mirror where scan would find it. An existing
// directory is fetched only when it is a bare repository whose origin is
// the expected URL; anything else at that path is left alone and reported.
// HEAD is re-pointed on every sync because a fetch never moves a bare
// repository's HEAD, and GitHub is the system of record for the default
// branch.
func (s *Source) Mirror(ctx context.Context, repo domain.ForgeRepo, root string) (string, error) {
	if s.git == nil {
		return "", errors.New("github: Mirror needs Config.Git")
	}
	if !validName(repo.Owner) || !validName(repo.Name) {
		return "", fmt.Errorf("github: cannot mirror %q/%q: not a valid repository name", repo.Owner, repo.Name)
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("github: %s/%s is empty: there is nothing to mirror", repo.Owner, repo.Name)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("mirrors directory: %w", err)
	}
	ownerDir := filepath.Join(root, repo.Owner)
	if err := os.MkdirAll(ownerDir, 0o700); err != nil {
		return "", fmt.Errorf("mirrors directory: %w", err)
	}
	url := s.cloneURL(repo.Owner, repo.Name)
	dir := filepath.Join(ownerDir, repo.Name+".git")

	switch _, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		if err := s.clone(ctx, url, ownerDir, repo.Name+".git"); err != nil {
			return "", err
		}
	case err != nil:
		return "", fmt.Errorf("mirror %s: %w", dir, err)
	default:
		if err := s.checkMirror(ctx, dir, url); err != nil {
			return "", err
		}
	}

	if _, err := s.local(ctx, dir, "config", "remote.origin.fetch", headRefspec); err != nil {
		return "", err
	}
	if err := s.network(ctx, dir, fetchTimeout, "fetch", "--prune", "origin"); err != nil {
		return "", err
	}
	// symbolic-ref refuses a name that is not a valid branch (verified on
	// 2.50.1), so the API's default branch needs no check of its own here.
	if _, err := s.local(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+repo.DefaultBranch); err != nil {
		return "", err
	}
	return dir, nil
}

// clone clones url, bare, into ownerDir/name through a temporary directory.
func (s *Source) clone(ctx context.Context, url, ownerDir, name string) error {
	// A clone that a crash interrupted leaves its temporary directory
	// behind; the next clone of the same repository removes it.
	stale, _ := filepath.Glob(filepath.Join(ownerDir, "."+name+".clone-*"))
	for _, d := range stale {
		_ = os.RemoveAll(d)
	}
	tmp, err := os.MkdirTemp(ownerDir, "."+name+".clone-")
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := s.network(ctx, ownerDir, cloneTimeout, "clone", "--bare", url, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(ownerDir, name)); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	renamed = true
	return nil
}

// checkMirror refuses to fetch into a directory that is not a bare
// repository cloned from url: a user's own clone, or a mirror of another
// repository, is reported and never changed.
func (s *Source) checkMirror(ctx context.Context, dir, url string) error {
	out, err := s.local(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("mirror %s is not a git repository: %w", dir, err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("mirror %s is not a bare repository; move it away to let sync clone a mirror there", dir)
	}
	out, err = s.local(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("mirror %s has no origin: %w", dir, err)
	}
	if got := strings.TrimSpace(string(out)); got != url {
		return fmt.Errorf("mirror %s fetches from %s, not %s; move it away to let sync clone a mirror there",
			dir, redact.URL(got), url)
	}
	return nil
}

// local runs a git command that stays on the machine: no credentials.
func (s *Source) local(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return s.git.Run(ctx, domain.GitCmd{Dir: dir, Args: args, Timeout: localTimeout, Profile: domain.Sync})
}

// network runs a git command that talks to GitHub, with the credentials in
// its environment only.
func (s *Source) network(ctx context.Context, dir string, timeout time.Duration, args ...string) error {
	_, err := s.git.Run(ctx, domain.GitCmd{
		Dir:     dir,
		Args:    args,
		Timeout: timeout,
		Env:     s.creds.env,
		Profile: domain.Sync,
	})
	return err
}
