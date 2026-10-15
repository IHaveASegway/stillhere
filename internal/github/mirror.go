package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// forgeIDKey is the key, in a mirror's own git config, that holds the node
// id of the repository the mirror belongs to. A rename or a transfer keeps
// the node id and changes the path, so ReconcileMirrors finds a mirror by
// this key and never by its path. It is written with git config, a local
// command that carries no credential.
const forgeIDKey = "stillhere.forgeId"

// stillhere's own directories among the mirrors. A GitHub owner name never
// starts with a dot, so staleDir cannot be an owner's directory; clonesDir
// sits in an owner's directory, where it cannot be a mirror either, because
// every mirror's name ends in ".git".
const (
	// staleDir, root/.stale/<owner>/<name>.git, holds the mirrors of
	// repositories their owner no longer lists. It is outside every owner
	// directory, so scan <mirrors>/<owner> never reads them.
	staleDir = ".stale"
	// clonesDir, root/<owner>/.stillhere-clones/<name>.git-*, holds the
	// clones in progress. scan reads a group's immediate children only
	// (docs/spec/discovery.md, rule 3), so it never finds a half-written
	// clone, and the directory is on the mirror's own filesystem, so the
	// rename that publishes a clone is atomic.
	clonesDir = ".stillhere-clones"
)

// staleClone is the age past which a clone's temporary directory is a
// crash's leftover, which the next clone in that owner's directory removes.
// No clone runs longer than cloneTimeout, so a younger directory may be
// another sync's clone in progress and is left alone.
const staleClone = cloneTimeout + 10*time.Minute

// nodeID matches the node ids a mirror may record: GitHub's, which are
// base64 or a type prefix and base64url ("R_kgDO..."). An id that starts
// with "-" could pass for an option on git's command line, and so could
// not be written.
var nodeID = regexp.MustCompile(`^[A-Za-z0-9_+/=][A-Za-z0-9_+/=-]*$`)

func validID(id string) bool { return nodeID.MatchString(id) }

// newCredentials returns the variables that authenticate git to the base
// URL and nowhere else: one http.<base>/.extraheader entry passed through
// git's environment-scoped config (GIT_CONFIG_COUNT, git 2.31 and later),
// the mechanism actions/checkout uses, minus its config file. Passing the
// same entry with -c would put the token in argv, which ps shows to every
// user; the environment of a process is visible to its own user only, the
// boundary the spec accepts (docs/spec/git-invocation.md#credentials).
// GIT_ASKPASS is empty so that a rejected token fails at once instead of
// reaching for a prompt.
//
// git matches http.<url>.* against the URL it was given, once. When a
// server redirects the first request to another host, git sends the rest of
// the command there, header and all (verified on 2.50.1: the POST after a
// redirected info/refs carried the token to the other host). So git follows
// no redirect. Sync always clones the URL the API reports, which needs
// none; a repository renamed between the listing and the clone fails once,
// and the next sync lists its new name.
func newCredentials(base, token string) *credentials {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return &credentials{env: []string{
		"GIT_ASKPASS=",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http." + base + "/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
		"GIT_CONFIG_KEY_1=http.followRedirects",
		"GIT_CONFIG_VALUE_1=false",
	}}
}

// Mirror creates the bare mirror of repo at root/<owner>/<name>.git, or
// brings an existing one up to date, and returns its path. It runs exactly
// the commands of docs/spec/git-invocation.md#sync-commands-network:
//
//	git clone --bare <url> <tmp>                  (first sync only)
//	git -C <tmp> config stillhere.forgeId <id>    (first sync only)
//	git -C <dir> config remote.origin.fetch +refs/heads/*:refs/heads/*
//	git -C <dir> fetch --prune origin
//	git -C <dir> symbolic-ref HEAD refs/heads/<default branch>
//
// A first clone goes into root/<owner>/.stillhere-clones and is renamed
// into place only once complete and marked with repo's node id, so an
// interrupted clone never leaves a half-written mirror where scan would
// find it. An existing directory is fetched only when it is a bare
// repository that carries repo's node id, or, made before mirrors carried
// one, fetches from the expected URL; anything else at that path is left
// alone and reported. A mirror that carries the node id fetches from the URL
// the API reports, so after a rename it follows the new name. HEAD is
// re-pointed on every sync because a fetch never moves a bare repository's
// HEAD, and GitHub is the system of record for the default branch.
//
// ReconcileMirrors, which runs first, has already moved a renamed or
// transferred repository's mirror to this path.
func (s *Source) Mirror(ctx context.Context, repo domain.ForgeRepo, root string) (string, error) {
	if s.git == nil {
		return "", errors.New("github: Mirror needs Config.Git")
	}
	if !validName(repo.Owner) || !validName(repo.Name) {
		return "", fmt.Errorf("github: cannot mirror %q/%q: not a valid repository name", repo.Owner, repo.Name)
	}
	if !validID(repo.ID) {
		return "", fmt.Errorf("github: cannot mirror %s/%s: %q is not a node id", repo.Owner, repo.Name, repo.ID)
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
		if err := s.clone(ctx, repo, url, ownerDir); err != nil {
			return "", err
		}
	case err != nil:
		return "", fmt.Errorf("mirror %s: %w", dir, err)
	default:
		if err := s.checkMirror(ctx, dir, repo, url); err != nil {
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

// clone clones repo from url, bare, into ownerDir/<name>.git through a
// temporary directory under ownerDir/.stillhere-clones.
func (s *Source) clone(ctx context.Context, repo domain.ForgeRepo, url, ownerDir string) error {
	tmpRoot := filepath.Join(ownerDir, clonesDir)
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	s.removeLeftovers(tmpRoot, time.Now())
	tmp, err := os.MkdirTemp(tmpRoot, repo.Name+".git-")
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := s.network(ctx, tmpRoot, cloneTimeout, "clone", "--bare", url, tmp); err != nil {
		return err
	}
	if _, err := s.local(ctx, tmp, "config", forgeIDKey, repo.ID); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(ownerDir, repo.Name+".git")); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	renamed = true
	return nil
}

// removeLeftovers removes the temporary directories in tmpRoot that a
// crashed clone left behind: those older than staleClone, which no clone in
// progress can be. A younger one may be another sync's clone, running now.
// now is the wall clock: like a timeout, an age of files on disk is
// housekeeping and never becomes a fact, so --now does not apply.
func (s *Source) removeLeftovers(tmpRoot string, now time.Time) {
	entries, err := os.ReadDir(tmpRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleClone {
			continue
		}
		path := filepath.Join(tmpRoot, e.Name())
		if err := os.RemoveAll(path); err != nil {
			s.log.Warn("mirror: cannot remove a crashed clone's leftover", "path", path, "error", err)
		}
	}
}

// checkMirror refuses to fetch into a directory that is not a bare
// repository of repo: a user's own clone, or the mirror of another
// repository, is reported and never changed. A mirror of repo whose origin
// is not url, because its repository was renamed or transferred since the
// clone, fetches from url from now on: GitHub is the system of record for
// the URL. A bare repository without a node id that fetches from url is
// such a mirror made before mirrors carried one, and gets repo's.
func (s *Source) checkMirror(ctx context.Context, dir string, repo domain.ForgeRepo, url string) error {
	out, err := s.local(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("mirror %s is not a git repository: %w", dir, err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("mirror %s is not a bare repository; move it away to let sync clone a mirror there", dir)
	}
	id, err := s.configValue(ctx, dir, forgeIDKey)
	if err != nil {
		return err
	}
	origin, err := s.configValue(ctx, dir, "remote.origin.url")
	if err != nil {
		return err
	}
	switch {
	case id == repo.ID && origin == url:
	case id == repo.ID:
		if _, err := s.local(ctx, dir, "config", "remote.origin.url", url); err != nil {
			return err
		}
		s.log.Info("mirror: following the repository's new URL", "mirror", dir, "url", url)
	case id == "" && origin == url:
		if _, err := s.local(ctx, dir, "config", forgeIDKey, repo.ID); err != nil {
			return err
		}
	case id == "":
		return fmt.Errorf("mirror %s fetches from %s, not %s, and is not a mirror sync made; move it away to let sync clone one there",
			dir, redact.URL(origin), url)
	default:
		return fmt.Errorf("mirror %s is the mirror of another repository (node id %s), not of %s/%s (%s); move it away to let sync clone one there",
			dir, id, repo.Owner, repo.Name, repo.ID)
	}
	return nil
}

// configValue returns the value of key in the repository at dir, or "" when
// it has none.
func (s *Source) configValue(ctx context.Context, dir, key string) (string, error) {
	out, err := s.git.Run(ctx, domain.GitCmd{
		Dir:     dir,
		Args:    []string{"config", "--get", key},
		Timeout: localTimeout,
		OKExit:  []int{1}, // the key is not set
		Profile: domain.Sync,
	})
	if err != nil {
		return "", fmt.Errorf("mirror %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
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
