#!/usr/bin/env bash
# completions.sh writes stillhere's bash, zsh and fish completion scripts to
# completions/ for the release archives and the Homebrew cask.
# .goreleaser.yaml runs it as a before hook, from the repository root.
#
# They are generated here, at release time, so that nothing has to run the
# binary at install time. Homebrew installs a cask's artifacts in a fixed
# order by type, whatever order the cask lists them in, and it generates
# completions before the postflight hook that removes macOS's quarantine
# attribute from the unsigned binary, so Gatekeeper would kill the binary.
# Static files need nothing to run, and archive users get them too.
# GoReleaser ships its own completions the same way.
set -euo pipefail

bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT

# A build for this machine is enough: the scripts depend only on the command
# tree, not on the platform or on the version stamped into a release binary.
CGO_ENABLED=0 go build -o "$bin/stillhere" ./cmd/stillhere

rm -rf completions
mkdir completions
"$bin/stillhere" completion bash >completions/stillhere.bash
# zsh autoloads a completion function from a file that carries its name.
"$bin/stillhere" completion zsh >completions/_stillhere
"$bin/stillhere" completion fish >completions/stillhere.fish
