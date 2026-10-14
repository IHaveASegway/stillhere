# Security policy

## Reporting a vulnerability

Report it privately, through a GitHub security advisory: open the repository's **Security** tab and choose **Report a vulnerability**, or go straight to [the advisory form](https://github.com/IHaveASegway/stillhere/security/advisories/new). Please do not open a public issue, pull request or discussion about it.

If the form is unavailable to you, open an issue that asks for a private channel and contains no details.

A useful report says:

- what an attacker can do, and what they need first (a repository you analyse, a crafted commit, network position, local access);
- the stillhere version (`stillhere version`), the git version, and the operating system;
- the steps or a script that reproduce it, against a throwaway repository rather than real code.

## What happens next

stillhere has one maintainer, so these are intentions rather than guarantees:

- an acknowledgement within 7 days;
- an assessment, and a fix or a mitigation plan, within 30 days of that;
- coordinated disclosure: the advisory is published when a fixed release is available, or after 90 days, whichever comes first, unless we agree otherwise;
- credit in the advisory and the release notes, if you want it.

## Supported versions

stillhere is before 1.0. Fixes go into the next release only; upgrade to the latest release to receive them.

## Scope

The threat model is in [docs/architecture.md](docs/architecture.md#security-and-privacy). These are vulnerabilities:

- A credential (a token, or userinfo in a remote URL) reaching stdout, stderr, logs, a report, `facts.db`, or an MCP answer.
- Any command other than `stillhere sync` touching the network.
- git, run by stillhere, starting a program that the analysed repository configures through a hook, fsmonitor, an external diff, askpass, or signature verification ([docs/spec/git-invocation.md](docs/spec/git-invocation.md)).
- From M3: markup or script injected through author names, paths or commit subjects, and a way past the web server's sign-in or its rules on who sees ranks.
- `facts.db`, the managed mirrors or temporary files created readable by other users (they must be 0600 files in 0700 directories).
- A release artifact, the container image or the Homebrew cask that does not match what the release workflow built from the tagged commit.

These are known and accepted, so they are not vulnerabilities by themselves:

- A `textconv` driver or a smudge filter that an analysed repository configures can run while git reads it. stillhere hardens git for repositories its user already trusts; it is not a sandbox for hostile ones.
- Anything that requires write access to the user's own git configuration, stillhere binary, or `STILLHERE_HOME`.

## Verifying a download

Every release publishes `stillhere_<version>_checksums.txt` with the SHA-256 of each archive. Check an archive before running it, for example:

```sh
sha256sum --ignore-missing --check stillhere_0.1.0_checksums.txt
```
