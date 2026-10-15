#!/usr/bin/env bash
# smoke-release.sh checks what "goreleaser release --snapshot --clean" left in
# dist/ against what a release promises, so ci.yml finds a broken release on
# the change that broke it rather than on the next tag:
#
#   - an archive per OS and architecture, each holding the binary, LICENSE,
#     README.md and the bash, zsh and fish completion scripts, each matching
#     the checksums file;
#   - a cask that depends on Homebrew's git and links the archived
#     completion scripts instead of running the binary at install
#     (.goreleaser.yaml says why);
#   - a binary for this machine that prints its version;
#   - an image per platform that runs "stillhere version", as a non-root
#     user, with git 2.44 or newer.
#
# Run it from the repository root after a snapshot. It needs jq and, for
# the images, docker (with QEMU for a platform that is not the host's).
set -euo pipefail
# Byte order for sort, so archive listings compare the same in every locale.
export LC_ALL=C

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
completions="completions/_stillhere completions/stillhere.bash completions/stillhere.fish"
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
		want="LICENSE README.md $completions $bin "
		[[ $contents == "$want" ]] || fail "$archive holds [$contents], want [$want]"
	done
done
if command -v sha256sum >/dev/null; then
	(cd dist && sha256sum --check --quiet "$checksums")
else
	(cd dist && shasum -a 256 --check --quiet "$checksums")
fi
# The completion scripts are the same files in every archive. In one copy,
# each must register itself for the stillhere command, as its shell expects.
sample=dist/stillhere_${version}_linux_amd64.tar.gz
zsh=$(tar -xOzf "$sample" completions/_stillhere)
[[ ${zsh%%$'\n'*} == "#compdef stillhere" ]] || fail "completions/_stillhere does not start with '#compdef stillhere'"
bash=$(tar -xOzf "$sample" completions/stillhere.bash)
[[ $bash == *"-F __start_stillhere stillhere"* ]] || fail "completions/stillhere.bash does not register __start_stillhere"
fish=$(tar -xOzf "$sample" completions/stillhere.fish)
[[ $fish == *"complete -c stillhere "* ]] || fail "completions/stillhere.fish does not register completions for stillhere"
ok "6 archives, each with the binary, LICENSE, README.md and bash, zsh and fish completions, all matching $checksums"

# The cask.
cask=dist/homebrew/Casks/stillhere.rb
[[ -f $cask ]] || fail "missing $cask"
grep -A1 'depends_on formula:' "$cask" | grep -q '"git"' || fail "$cask does not depend on git"
for stanza in 'bash_completion "completions/stillhere.bash"' 'zsh_completion "completions/_stillhere"' \
	'fish_completion "completions/stillhere.fish"'; do
	grep -qF "$stanza" "$cask" || fail "$cask has no $stanza"
done
# Homebrew generates completions from an executable before the postflight
# hook removes the quarantine attribute, so on macOS Gatekeeper would kill
# the binary on every install.
if grep -q generate_completions_from_executable "$cask"; then
	fail "$cask runs the binary at install to generate completions"
fi
ok "the cask depends on Homebrew's git and links the archived completions"

# The binary for this machine.
goos=$(go env GOOS)
goarch=$(go env GOARCH)
native=$(jq -r --arg os "$goos" --arg arch "$goarch" \
	'.[] | select(.type == "Binary" and .goos == $os and .goarch == $arch) | .path' dist/artifacts.json)
[[ -x $native ]] || fail "no $goos/$goarch binary in dist"
first=$("$native" version | head -n 1)
[[ $first == "stillhere $version" ]] || fail "$native version printed '$first', want 'stillhere $version'"
ok "$native prints 'stillhere $version'"

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
