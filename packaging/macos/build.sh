#!/usr/bin/env bash
# Builds the macOS package: an app bundle holding the agent and dufs, wrapped in a pkg that
# installs it into /Applications and starts the service.
#
# The bundle is what makes the local network prompt possible: the reason string lives in
# Info.plist, and a bare binary has nowhere to put one.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
version=${VERSION:-0.0.0}
dufs_version=$(cat "$root/packaging/dufs.version")
out=${OUT:-$root/dist}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app="$work/root/anywhere-file.app"
mkdir -p "$app/Contents/MacOS" "$out"

sed "s/@VERSION@/$version/g" "$here/Info.plist" >"$app/Contents/Info.plist"

# Universal binaries, because a package is downloaded once and both kinds of Mac are still
# in use. Go cross-compiles either way, so this costs a second build and no toolchain.
for arch in amd64 arm64; do
	GOOS=darwin GOARCH=$arch go build -C "$root" -o "$work/agent-$arch" ./device/agent
done
lipo -create -output "$app/Contents/MacOS/agent" "$work/agent-amd64" "$work/agent-arm64"

for pair in amd64:x86_64 arm64:aarch64; do
	triple="${pair#*:}-apple-darwin"
	curl -sSLo "$work/dufs-${pair%%:*}.tar.gz" \
		"https://github.com/sigoden/dufs/releases/download/v${dufs_version}/dufs-v${dufs_version}-${triple}.tar.gz"
	tar xzf "$work/dufs-${pair%%:*}.tar.gz" -C "$work" dufs
	mv "$work/dufs" "$work/dufs-${pair%%:*}"
done
lipo -create -output "$app/Contents/MacOS/dufs" "$work/dufs-amd64" "$work/dufs-arm64"

cp "$here/uninstall" "$app/Contents/MacOS/uninstall"
chmod 755 "$app/Contents/MacOS/"*

# Settings baked in at build time, for a package that will be installed by double-clicking
# and has no environment to read.
if [ -n "${AGENT_ENV:-}" ]; then
	for kv in $AGENT_ENV; do echo "$kv"; done >"$app/Contents/MacOS/agent.env"
fi

pkg="$out/anywhere-file-$version.pkg"
pkgbuild --quiet --root "$work/root" --install-location /Applications \
	--identifier io.anywhere-file.agent --version "$version" \
	--scripts "$here/scripts" "$pkg"
echo "$pkg"
