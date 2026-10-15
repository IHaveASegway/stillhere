# Git invocation

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (offline/sync split, credentials, `--source` floor) · analysis commands become tests in M0, sync commands in M2

Every git process goes through one adapter, `GitRunner.Run(ctx, repo, args, timeout)`. Call sites pass the git arguments themselves, so a reader and a test can see the exact command. A `GitRepository` type whose `Blame()`/`Log()` methods hide the flags is the weaker design: one method forgets the mailmap flag and nobody can see it.

## Analysis commands (offline)

```
git -C <repo> --no-optional-locks \
  -c core.hooksPath=/dev/null \
  -c core.fsmonitor= \
  -c core.attributesFile=/dev/null \
  -c diff.external= \
  -c log.showSignature=false \
  [-c mailmap.file=<org-mailmap>] \
  <args>
```

Environment: `GIT_TERMINAL_PROMPT=0`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_ATTR_NOSYSTEM=1`, `GIT_NO_LAZY_FETCH=1`, `GIT_ASKPASS=` (empty). Every inherited `GIT_*` variable, `SSH_ASKPASS` and `SSH_ASKPASS_REQUIRE` are removed first. Elsewhere in these specs, `git <common>` means this prefix plus this environment.

| Setting | Why |
| --- | --- |
| `core.hooksPath=/dev/null`, `core.fsmonitor=` | A clone must not run its hooks or a filesystem monitor as a side effect of being measured. |
| `diff.external=` | A repo-local external diff program must not run. The empty value does not switch external diff off: git tries to run an empty command and fails (verified on 2.50.1). `log` and `show` never run an external diff without `--ext-diff`, so the value matters only for `git diff`, which must pass `--no-ext-diff` (see call-site rules); one that forgets fails closed instead of running the repository's program. |
| `log.showSignature=false` | A repo-local `log.showSignature=true` makes `log` and `show` run `gpg.program`, which the same config names, for every signed commit (verified). |
| `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null` | Reproducibility: a machine-wide mailmap or config would make two laptops emit different reports. Repo-local `.git/config` still applies, but it cannot override command-line `-c`. |
| `core.attributesFile=/dev/null`, `GIT_ATTR_NOSYSTEM=1` | Reproducibility again: attributes are not config, so the two variables above leave the per-user file (`$XDG_CONFIG_HOME/git/attributes`, git's default `core.attributesFile`) and `/etc/gitattributes` in force. Either one can mark a path `-diff` (numstat then counts it as binary) or `linguist-generated` (verified). The repository's own `.gitattributes` and `info/attributes` still apply, in work trees and bare clones alike (verified). |
| `GIT_NO_LAZY_FETCH=1` | Blocks promisor lazy fetch on Git 2.45+ (`git --no-lazy-fetch`, 2.45.0 release notes): a partial clone's missing blobs read as missing, so classification degrades as [classification.md](classification.md) says instead of failing. The promisor remote comes from the repo's own config, the same place a credential-bearing URL lives. Git 2.44.0 ignores the variable (CI saw two connections to a fake promisor on 2026-10-14), which is why the floor is 2.45. |
| `GIT_ALLOW_PROTOCOL=none` | Defence in depth: an allow list naming no real protocol refuses every transport, including remote helpers, overriding the repository's own `protocol.<name>.allow` (git 2.12+). Whatever would make a read-only command fetch, today's lazy fetch or a path a later git adds, fails with `transport '<name>' not allowed` instead of connecting (verified on 2.50.1 with lazy fetch left on). Sync never sets it. |
| `--no-optional-locks` | Read-only commands must not take optional locks that contend with the user's own git. |
| `GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=` | Analysis must never block on a credential prompt. `GIT_TERMINAL_PROMPT=0` covers only the terminal: git first tries `GIT_ASKPASS`, then `core.askPass` from repo-local config, then `SSH_ASKPASS`, which desktop sessions and IDE terminals often export. An empty `GIT_ASKPASS` ends that search (verified). Sync leaves `GIT_ASKPASS` to its credential mechanism. |

**Call-site rules.** The prefix cannot fix these, because a more specific setting or an option of one subcommand wins over a `-c`:

- `--no-color` on every `log -p`, `show`, `diff` and `grep` whose output is parsed. A repo-local `color.diff=always` or `color.grep=always` puts ANSI escapes in that output, and `-c color.ui=never` does not override the more specific keys (verified).
- `--no-ext-diff` on every `git diff`.
- `--no-ignore-revs-file` before any `--ignore-revs-file` on `blame`. A repo-local `blame.ignoreRevsFile` otherwise adds its list to stillhere's, and blame fails outright when that file is missing; `-c blame.ignoreRevsFile=` does not clear it (verified).

Analysis never writes into a scanned repository: no fetch, no `gc`, no config writes, no index refresh. Temporary files, such as an extracted ignore-revs file, go in stillhere's own temp directory. Only sync writes, and only into the mirrors it owns.

Timeouts per command: blame 120 s per file, a repo's log 300 s. On timeout the child is killed, the file or repo records an error, and the group continues: a repository is a bulkhead.

- **What is killed.** git runs in its own process group, and a timeout or a cancellation signals the whole group. Once git is reaped, whatever is left in the group is killed too, so a helper that ignored SIGTERM (Sync) does not outlive the command. A helper that left the group with `setsid` is out of reach; it cannot hold the command either, because the runner closes its end of stdout 5 s after the timeout.
- **What is reported.** A timeout is reported as a timeout (`errors.Is(err, context.DeadlineExceeded)`), even when the reader fails first on the output the kill truncated. A cancellation is reported as one (`context.Canceled`), so a Ctrl-C stops the run while a repository's failure does not.

**Residual risk, named rather than papered over.** A `diff.<driver>.textconv` or a smudge filter named by repo-local config or `.gitattributes` can still run during `log` and `blame`. They are not disabled, because blanking attributes would break the linguist checks. `gpg.program` from repo-local config still runs for any command that verifies signatures on purpose (`--show-signature`, the `%G?` placeholders); no collector does. This is defence in depth, not a sandbox: stillhere analyses clones its user already trusts, and never analyses repositories uploaded by other people.

## Version floors

| Git | Needed for |
| --- | --- |
| 2.31 | `--diff-merges=first-parent`; `GIT_CONFIG_COUNT` (a sync credential candidate) |
| 2.40 | `git check-attr --source=<rev>`, required for bare mirrors ([classification.md](classification.md#gitattributes)) |
| 2.45 | `GIT_NO_LAZY_FETCH`, so a partial clone's missing blobs are never fetched |

The 2026-10-07 plan let the tool run on 2.31–2.43 with a weaker guarantee. `--source` now makes 2.40 a hard floor, so the recommendation is one floor, below which stillhere exits 2. It was 2.44 until CI on git 2.44.0 showed that release does not read `GIT_NO_LAZY_FETCH` (the variable and `git --no-lazy-fetch` arrived in 2.45.0), so the floor is 2.45. The Homebrew cask depends on Homebrew's git, and the Docker image ships Alpine's, both well above it. CI runs the floor version and the latest.

## Sync commands (network)

Analysis never touches the network. Only `stillhere sync github` does, and it is the only code path allowed to run these:

```
git clone --bare <https-url-without-userinfo> <mirrors>/<owner>/.stillhere-clones/<repo>.git-<random>
git -C <that clone> config stillhere.forgeId <node id from the GitHub API>
                                   (then renamed to <mirrors>/<owner>/<repo>.git)
git -C <mirror> config remote.origin.fetch '+refs/heads/*:refs/heads/*'
git -C <mirror> fetch --prune origin
git -C <mirror> symbolic-ref HEAD refs/heads/<default branch from the GitHub API>
```

Before any of these, once per sync, `git config --file <dir>/config --get stillhere.forgeId` reads the node id of every mirror under `<mirrors>` (it follows no include and needs no repository around it), and mirrors move to match the listing; see below.

- `--bare` with an explicit refspec, not `--mirror`. A mirror's refspec is `+refs/*:refs/*` (verified), which copies every ref the server advertises. On GitHub that includes `refs/pull/<n>/head` and `merge` for every pull request: verified 2026-10-14 with git 2.50.1, `ls-remote` of `cli/safeexec` advertised 13 `refs/pull/*` refs beside 1 branch and 2 tags. A plain `--bare` clone has no fetch refspec at all (verified again in M2), hence the `config` line. No collector needs tags.
- The first clone goes into `<mirrors>/<owner>/.stillhere-clones/` (mode 0700), is marked with its node id, and is renamed into place only when complete. `scan <mirrors>/<owner>` reads that group's immediate children only ([discovery.md](discovery.md#rules-in-order), rule 3), so an interrupted clone is never scanned, as it would be beside the mirror: `git clone` writes `HEAD`, `objects/` and `refs/` before it fetches, which makes a directory look like a bare repository. The directory is on the mirror's own file system, so the rename is atomic. A later clone in that owner's directory removes a leftover older than the clone timeout plus ten minutes, which no clone still running can be, so two syncs never delete each other's clone in progress.
- A mirror is its repository's by node id (`stillhere.forgeId`), not by path. An existing directory is fetched only when it is a bare repository that carries the repository's node id, or carries none and fetches from the expected URL (it then gets the id); anything else at that path is reported and never changed. A mirror that carries the id gets the URL the API reports as its `remote.origin.url` (one `git config`), so it follows a rename.
- Once per sync, before any fetch, the mirrors follow the listing (`ForgeSource.ReconcileMirrors`). The mirror of a repository that was renamed, transferred, or changed only the case of its name moves to the repository's path. Under `<mirrors>/<owner>`, the mirror of a repository the owner no longer lists (deleted, transferred to an owner this sync does not list, or hidden from the token), and a second mirror of a listed one, move to `<mirrors>/.stale/<owner>/`, outside every group directory, and back once the repository is listed again. Nothing is deleted. Without this, a renamed repository would be cloned again under its new name and its history scanned twice, because discovery keys repositories by origin URL. The moves are renames, made one at a time; two repositories that swapped names pass through `<mirrors>/.stale/.moving`. A repository whose mirror cannot be moved is not mirrored that sync, so that no second copy is cloned.
- `symbolic-ref HEAD` on every sync: `fetch` never moves a bare repo's HEAD, so a renamed upstream default branch would leave HEAD dangling. The GitHub API is the system of record for the default branch. `symbolic-ref` itself refuses a name that is not a valid branch (verified on 2.50.1).
- Never `--depth` or `--filter`: shallow and partial clones break line facts ([discovery.md](discovery.md#clone-health)).
- Timeouts: one hour for `clone`, 30 minutes for `fetch`, one minute for the local commands. The analysis default of 300 s would cut off the first clone of a large repository.
- The whole analysis prefix and environment apply to sync commands too, with two exceptions. `GIT_NO_LAZY_FETCH` is not set, because these commands fetch on purpose. `GIT_ASKPASS` is set by the credential mechanism below (to empty); `SSH_ASKPASS` is still removed, so no prompt path is left.
- `clone` and `fetch` get the credential variables; the local commands (`config`, `symbolic-ref`, `rev-parse`) get none. All of them run on the Sync profile, whose SIGTERM lets git remove its lock files when a sync is interrupted.
- Two syncs may run at once (a scheduled one and a manual one). Every move and publish is a rename, so each repository's mirror is whole or absent, and a conflict fails that repository in one of them; the next sync heals it.

## Credentials

A token is never written to a remote URL, `.git/config`, command-line arguments (visible in `ps`), logs, or reports. Clones that keep credentials in their remote URLs are a common way tokens leak, and analysis would otherwise copy them into every warning.

The candidates were two environment-only mechanisms:

1. `GIT_ASKPASS` pointing at the stillhere binary, which answers git's username and password prompts from an environment variable only it reads.
2. Environment-scoped config: `GIT_CONFIG_COUNT=1`, `GIT_CONFIG_KEY_0=http.https://github.com/.extraheader`, `GIT_CONFIG_VALUE_0=AUTHORIZATION: basic <base64 of x-access-token:TOKEN>`. Never pass this with `-c`, which puts it in argv.

**Chosen in M2 (#14): mechanism 2**, plus `GIT_ASKPASS=` (empty), so that a rejected token fails at once instead of reaching for a prompt, and `http.followRedirects=false` (`GIT_CONFIG_COUNT=2`). It is the header `actions/checkout` sends, without the config file `actions/checkout` writes it to. It needs git 2.31, below the 2.45 floor. Mechanism 1 would work too, but it gives the binary a second mode, answering git's prompts from inside git's child process: more code on the credential path, and harder to test.

The key is scoped to the GitHub URL (`http.<url>.extraheader`), but that scope alone does not keep the header on GitHub. git matches the key once, against the URL it was given, and by default (`http.followRedirects=initial`) it follows a redirect of the first request and sends the rest of the command to the new location with the header still attached. Verified on 2.50.1 (Apple Git-155, curl 8.7.1): with a server on 127.0.0.1 redirecting to one on localhost, curl dropped the header from the redirected `GET info/refs`, but git's next request, the `POST git-upload-pack`, carried it to the other host. GitHub does not redirect git across hosts, but an Enterprise host behind an SSO proxy can. Hence `http.followRedirects=false`: sync always clones and fetches the URL the API reports, which needs no redirect, and a repository renamed between the listing and the clone fails once, until the next sync lists its new name. `TestGitHubSync` checks that a redirecting server's target gets no request at all.

The same OS user can still read a process environment. That is the accepted boundary. `TestGitHubSync` verifies the mechanism end to end: it clones and fetches a mirror from a local git server (git's own `http-backend`) that refuses any request without the token, then asserts that `remote.origin.url` has no userinfo and that neither the token nor its base64 form is in any file of the mirror, its config, the logs, the facts or an error. A refused token fails the clone without a prompt and leaves no directory behind. The same code also cloned and fetched a public repository (`cli/safeexec`) from github.com with the header on 2026-10-14, which shows GitHub accepts its form; a public repository cannot show that the header is what authenticates, which is the local server's part.

## URL scrubbing

Applied before any remote string is stored, logged or printed, and to git stderr before it is stored:

- Remove userinfo: `https://user:TOKEN@github.com/o/r.git` becomes `https://github.com/o/r.git`; the same applies to `ssh://user:password@host/…`.
- Remove the query and the fragment. A remote needs neither, and a query can carry a token (`?access_token=…`).
- The scp form `git@host:path` carries no password and is kept as git printed it, as are `host:path` and local paths. A leading `user:password@` on a remote without a scheme is removed. Any other string without a scheme that contains an `@` is not kept: `https:/user:TOKEN@host` and `https//user:TOKEN@host` are malformed URLs, not scp remotes.
- If parsing fails, store `<unparsed-remote>` and never write the raw string. `<unparsed-remote>` is never a dedupe key ([discovery.md](discovery.md#rules-in-order)): a repo whose remote does not parse is keyed like one without a remote.
- In free text (git's stderr), every URL is scrubbed as above, and a `user:password@` prefix is removed from scp-like tokens too, as in `fatal: 'user:pw@host:o/r.git' does not appear to be a git repository`, and from a segment of a path. So are the userinfo, the query and the fragment of a URL whose `//` a path cleaner collapsed: a URL given where a path is expected becomes `/cwd/https:/user:TOKEN@host/r.git` or `/cwd/https:/host/r.git?access_token=TOKEN` once made absolute, and the messages about it would otherwise carry the token. Only a scheme of two characters or more counts there, because a one-letter one is a Windows drive, as git itself reads it: `C:\src\C#\app` is a path, and keeps its `#`.
- Stored git error messages are truncated to 180 characters after scrubbing, never between a character and its combining marks. Arguments kept for errors and debug logs are scrubbed the same way but not truncated.

## Identity: mailmap and bots

Git mailmap is the only identity store. stillhere never parses mailmap itself.

- Every analysis command gets `-c mailmap.file=<org-mailmap>` when `--mailmap FILE` is passed, or when `./.mailmap` exists in the directory the command started in. Git also applies the repository's own mailmap (`gitmailmap(5)`): a work-tree clone reads the checked-out `.mailmap`, and a bare mirror reads `HEAD:.mailmap` through the default `mailmap.blob` (verified). Entries from `mailmap.file` take precedence.
- Canonical key: the mailmapped email, lowercased.
- Display name: the mailmap's canonical name when it supplies one. Otherwise, the name seen most often with that email in this scan; ties go to the newest committer timestamp among commits using that exact name, then to UTF-8 bytes ascending.
- No fuzzy merging on names: different people share names and surnames. At warn level, print "N distinct emails share a display name; not merged."
- Bots: after mailmap, a case-insensitive substring match of the canonical name and email against `[bots].fragments`, or an `[ai]` agent: an email in `[ai].coauthor_emails`, or a name or email that contains an `[ai].agent_authors` fragment. Agents are never ranked rows ([ADR 0007](../adr/0007-ai-assisted-attribution.md)), so they count as bots even when no `[bots]` fragment names them. Defaults: `copilot`, `gpt-engineer`, `dependabot`, `renovate`, `[bot]`. `users.noreply.github.com` is not a bot marker, because people use it. A mailmap line that maps a bot email onto a person is the user's decision. Bots are separated in every table, never deleted.
- AI-assisted changes are a tag on a person's change, not a bot ([week-collector.md](week-collector.md#ai-assisted-changes)). An agent's commit is credited to its first human co-author, and a commit is tagged AI-assisted only when the person credited is human: an agent's commit without a human co-author stays the agent's, a bot commit ([metrics.md](../metrics.md#change-facts-shared-by-durable1-and-simplifying1)).
- Linking GitHub logins to canonical emails belongs to the GitHub sync (M2) and is specified with it.

## Logging

stderr only. Info level: repo names, counts, durations, skip reasons. Never emails, remotes with userinfo, tokens, or AI request bodies. Debug, off by default, may print emails.
