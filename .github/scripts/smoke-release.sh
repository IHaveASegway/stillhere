#!/usr/bin/env bash
# smoke-release.sh checks what "goreleaser release --snapshot --clean" left in
# dist/ against what a release promises, so ci.yml finds a broken release on
# the change that broke it rather than on the next tag:
#
#   - an archive per OS and architecture, each holding the binary, LICENSE
#     and README.md, each matching the checksums file;
#   - a cask that depends on Homebrew's git;
#   - a binary for this machine that prints its version and the shell
#     completions the cask generates at install;
#   - an image per platform that runs "stillhere version", as a non-root
#     user, with git 2.44 or newer.
#
# Run it from the repository root after a snapshot. It needs jq and, for
# the images, docker (with QEMU for a platform that is not the host's).
set -euo pipefail

fail() {
	echo "smoke-release: FAIL: $*" >&2
	exit 1
}

ok() {
	echo "ok: $*"
}

[[ -f dist/metadata.json && -f dist/artifacts.json ]] ||
	fail "no dist/metadata.json or dist/artifacts.json: run goreleaser release --snapshot --clean first"

version=$(jq -r .version dist/metadata.json)
artifacts() { jq -r --arg type "$1" '.[] | select(.type == $type) | .name' dist/artifacts.json; }

# Archives and checksums.
checksums=$(artifacts Checksum)
[[ -n $checksums ]] || fail "no checksums file"
for os in darwin linux windows; do
	for arch in amd64 arm64; do
		ext=tar.gz
		bin=stillhere
		if [[ $os == windows ]]; then
			ext=zip
			bin=stillhere.exe
		fi
		archive=stillhere_${version}_${os}_${arch}.${ext}
		[[ -f dist/$archive ]] || fail "missing archive $archive"
		grep -q "  $archive\$" "dist/$checksums" || fail "$archive is not in $checksums"
		if [[ $ext == zip ]]; then
			contents=$(unzip -Z1 "dist/$archive" | sort | tr '\n' ' ')
		else
			contents=$(tar -tzf "dist/$archive" | sort | tr '\n' ' ')
		fi
		[[ $contents == "LICENSE README.md $bin " ]] ||
			fail "$archive holds [$contents], want [LICENSE README.md $bin]"
	done
done
if command -v sha256sum >/dev/null; then
	(cd dist && sha256sum --check --quiet "$checksums")
else
	(cd dist && shasum -a 256 --check --quiet "$checksums")
fi
ok "6 archives, each with the binary, LICENSE and README.md, all matching $checksums"

# The cask.
cask=dist/homebrew/Casks/stillhere.rb
[[ -f $cask ]] || fail "missing $cask"
grep -A1 'depends_on formula:' "$cask" | grep -q '"git"' || fail "$cask does not depend on git"
ok "the cask depends on Homebrew's git"

# The binary for this machine.
goos=$(go env GOOS)
goarch=$(go env GOARCH)
native=$(jq -r --arg os "$goos" --arg arch "$goarch" \
	'.[] | select(.type == "Binary" and .goos == $os and .goarch == $arch) | .path' dist/artifacts.json)
[[ -x $native ]] || fail "no $goos/$goarch binary in dist"
first=$("$native" version | head -n 1)
[[ $first == "stillhere $version" ]] || fail "$native version printed '$first', want 'stillhere $version'"
for shell in bash zsh fish; do
	"$native" completion "$shell" | grep -q stillhere || fail "$native completion $shell printed no completion"
done
ok "$native prints 'stillhere $version' and bash, zsh and fish completions"

# The images: one per platform, loaded into the local daemon by the snapshot.
for platform in linux/amd64 linux/arm64; do
	image=$(jq -r --arg platform "$platform" --arg tag "$version-${platform#linux/}" \
		'.[] | select(.type == "Docker Image" and (.extra.Platforms | index($platform)) and (.name | endswith(":" + $tag))) | .name' \
		dist/artifacts.json)
	[[ -n $image ]] || fail "no $platform image tagged $version"
	first=$(docker run --rm --platform "$platform" "$image" version | head -n 1)
	[[ $first == "stillhere $version" ]] || fail "$image version printed '$first', want 'stillhere $version'"
	uid=$(docker run --rm --platform "$platform" --entrypoint id "$image" -u)
	[[ $uid != 0 ]] || fail "$image runs as root"
	gitv=$(docker run --rm --platform "$platform" --entrypoint git "$image" version | awk '{ print $3 }')
	awk -v v="$gitv" 'BEGIN { split(v, p, "."); exit !(p[1] > 2 || (p[1] == 2 && p[2] >= 44)) }' ||
		fail "$image ships git $gitv, older than the 2.44 floor"
	ok "$image prints 'stillhere $version', runs as uid $uid, ships git $gitv"
done
