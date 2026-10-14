# The stillhere container image: the static stillhere binary, git 2.44 or
# newer (the version floor, docs/spec/git-invocation.md#version-floors) and
# tini, running as an unprivileged user.
#
# GoReleaser builds this image (dockers_v2 in .goreleaser.yaml) around the
# same binaries it puts in the release archives, so every channel ships the
# same bytes: build once, then package (Humble and Farley, Continuous
# Delivery, ch. 5). Its build context holds those binaries at
# <os>/<arch>/stillhere, which is why the COPY below names TARGETPLATFORM and
# why a plain "docker build ." from the repository fails. Build it with:
#
#   go run github.com/goreleaser/goreleaser/v2@v2.18.3 release --snapshot --clean
#
# Usage, with the repositories to read mounted under /work, run as the user
# who owns them (git refuses a repository that belongs to someone else):
#
#   docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work:ro" \
#     ghcr.io/ihaveasegway/stillhere week /work

# Alpine because its git tracks upstream (2.54 in 3.24) and the image stays
# small. Pinned by digest so a rebuild cannot silently change the base;
# Dependabot (.github/dependabot.yml) proposes digest updates.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

# A pipe fails when any command in it fails, not only the last.
SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

# git: stillhere runs it for every fact it reads. tini: as PID 1 it reaps
# the orphaned processes a killed git leaves behind and forwards signals, so
# a docker stop cancels a run cleanly. The packages are not pinned: Alpine
# keeps only the latest build of each package in a stable branch, so a pin
# breaks the build at the next security fix. The build asserts the floor
# instead, and fails rather than ship a git stillhere refuses to run. The
# user has a fixed numeric UID, so a runtime that must prove it runs as
# non-root (Kubernetes runAsNonRoot) can check it.
# hadolint ignore=DL3018
RUN apk add --no-cache git tini \
 && git version | awk '{ split($3, v, "."); ok = v[1] > 2 || (v[1] == 2 && v[2] >= 44); seen = $3 } END { if (!ok) { print "git " seen " is older than the 2.44 floor"; exit 1 } }' \
 && addgroup -S -g 10001 stillhere \
 && adduser -S -D -u 10001 -G stillhere -h /home/stillhere stillhere \
 && mkdir -p /data /work \
 && chown stillhere:stillhere /data /work

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/stillhere /usr/local/bin/stillhere

# facts.db and the managed mirrors live under STILLHERE_HOME: mount a volume
# at /data to keep them between runs. /work is where stillhere looks for
# ./stillhere.toml and ./.mailmap, as it does in a shell's working directory.
ENV STILLHERE_HOME=/data
WORKDIR /work
USER 10001:10001
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/stillhere"]
