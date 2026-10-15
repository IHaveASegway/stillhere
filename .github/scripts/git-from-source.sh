#!/usr/bin/env bash
# git-from-source.sh provides the git versions CI tests stillhere against:
# the version floor, 2.44.0, and the latest release
# (docs/spec/git-invocation.md#version-floors). Both are built from the
# release tarball with the same recipe, so the version is the only thing
# the two matrix jobs vary. ci.yml caches each install by version, runner
# image and this file's hash, so a version compiles once.
#
#   git-from-source.sh resolve latest|X.Y.Z
#       Prints GitHub step outputs: version=X.Y.Z and prefix=DIR.
#   git-from-source.sh build X.Y.Z PREFIX
#       Downloads, verifies, builds and installs that release into PREFIX.
#       Needs apt-get (Debian or Ubuntu), as root or through sudo.
#
# Resolving and downloading read one source, kernel.org's release
# directory, so "latest" always names a release whose tarball and checksum
# are published.
set -euo pipefail

readonly releases=https://mirrors.edge.kernel.org/pub/software/scm/git

die() {
	echo "git-from-source: $*" >&2
	exit 1
}

# A release version: three numbers, no -rc suffix.
is_release() {
	[[ $1 =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

checksums() {
	curl -fsSL --retry 3 "$releases/sha256sums.asc"
}

resolve() {
	local want=$1 version
	if [[ $want == latest ]]; then
		version=$(checksums |
			sed -n 's/^[0-9a-f]\{64\}  git-\([0-9]*\.[0-9]*\.[0-9]*\)\.tar\.xz$/\1/p' |
			sort -V | tail -n 1)
	else
		version=$want
	fi
	is_release "$version" || die "cannot resolve a git release from '$want'"
	echo "version=$version"
	echo "prefix=$HOME/.cache/git-from-source/$version"
}

build() {
	local version=$1 prefix=$2 sudo=
	is_release "$version" || die "not a release version: '$version'"
	[[ $prefix == /* ]] || die "PREFIX must be absolute: '$prefix'"
	[[ $(id -u) == 0 ]] || sudo=sudo

	# curl and expat give git its real HTTP transport, so a test asserting
	# that analysis never connects cannot pass merely because git could not.
	$sudo apt-get update -qq
	$sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		ca-certificates curl gcc libc6-dev libcurl4-openssl-dev libexpat1-dev \
		libssl-dev make xz-utils zlib1g-dev >/dev/null

	local work
	work=$(mktemp -d)
	# shellcheck disable=SC2064 # expand $work now: it is local to build.
	trap "rm -rf '$work'" EXIT

	local tarball=git-$version.tar.xz
	curl -fsSL --retry 3 -o "$work/$tarball" "$releases/$tarball"
	local want
	want=$(checksums | awk -v f="$tarball" '$2 == f { print $1 }')
	[[ -n $want ]] || die "kernel.org lists no checksum for $tarball"
	(cd "$work" && echo "$want  $tarball" | sha256sum --check --strict -)

	tar -xJf "$work/$tarball" -C "$work"
	# Leave out what stillhere never runs: translations, the Tcl/Tk GUIs,
	# and the Perl and Python commands (send-email, svn, p4). NO_RUST: since
	# 2.55 git builds a few internals (hashing, loose objects) in Rust unless
	# told not to; the C versions behave the same and need no Cargo. Git 3.0
	# makes Rust mandatory, and the weekly run of ci.yml fails on that
	# release until this recipe installs Cargo.
	local flags=(
		prefix="$prefix"
		NO_GETTEXT=YesPlease
		NO_TCLTK=YesPlease
		NO_PERL=YesPlease
		NO_PYTHON=YesPlease
		NO_RUST=YesPlease
	)
	make -C "$work/git-$version" -j "$(nproc)" "${flags[@]}" all >/dev/null
	make -C "$work/git-$version" "${flags[@]}" install >/dev/null
	"$prefix/bin/git" version
}

case ${1-} in
resolve)
	[[ $# == 2 ]] || die "usage: $0 resolve latest|X.Y.Z"
	resolve "$2"
	;;
build)
	[[ $# == 3 ]] || die "usage: $0 build X.Y.Z PREFIX"
	build "$2" "$3"
	;;
*)
	die "usage: $0 resolve latest|X.Y.Z | build X.Y.Z PREFIX"
	;;
esac
