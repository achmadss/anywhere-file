#!/usr/bin/env bash
# Builds the Linux packages: a tarball that installs under the user's home with no root,
# and a deb for the distributions that take one. Both carry the agent and dufs.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
version=${VERSION:-0.0.0}
dufs_version=$(cat "$root/packaging/dufs.version")
out=${OUT:-$root/dist}
arches=${ARCHES:-amd64 arm64}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"

for arch in $arches; do
	case $arch in
	amd64) triple=x86_64-unknown-linux-musl ;;
	arm64) triple=aarch64-unknown-linux-musl ;;
	*)
		echo "no dufs build for $arch" >&2
		exit 1
		;;
	esac

	name="anywhere-file-$version-linux-$arch"
	dir="$work/$name"
	mkdir -p "$dir"
	GOOS=linux GOARCH=$arch go build -C "$root" -o "$dir/agent" ./device/agent
	curl -sSLo "$work/dufs.tar.gz" \
		"https://github.com/sigoden/dufs/releases/download/v${dufs_version}/dufs-v${dufs_version}-${triple}.tar.gz"
	tar xzf "$work/dufs.tar.gz" -C "$dir" dufs
	cp "$here/install.sh" "$here/uninstall.sh" "$here/anywhere-file.xml" "$dir/"
	# Settings baked in at build time, for a package that will be installed without a
	# shell to read an environment from.
	if [ -n "${AGENT_ENV:-}" ]; then
		for kv in $AGENT_ENV; do echo "$kv"; done >"$dir/agent.env"
	fi
	tar czf "$out/$name.tar.gz" -C "$work" "$name"
	echo "$out/$name.tar.gz"

	if ! command -v dpkg-deb >/dev/null 2>&1; then
		echo "no dpkg-deb here, so no deb for $arch" >&2
		continue
	fi
	# The binaries live together under /usr/lib, and /usr/bin gets a link. That keeps dufs
	# out of the way of a dufs the distribution may have packaged, and next to the agent,
	# which is where the agent looks for it.
	deb="$work/deb-$arch"
	mkdir -p "$deb/DEBIAN" "$deb/usr/lib/anywhere-file" "$deb/usr/bin" "$deb/usr/lib/firewalld/services"
	cp "$dir/agent" "$dir/dufs" "$deb/usr/lib/anywhere-file/"
	if [ -f "$dir/agent.env" ]; then cp "$dir/agent.env" "$deb/usr/lib/anywhere-file/"; fi
	cp "$here/anywhere-file.xml" "$deb/usr/lib/firewalld/services/"
	ln -sf ../lib/anywhere-file/agent "$deb/usr/bin/anywhere-file-agent"
	cp "$here/postinst" "$here/prerm" "$deb/DEBIAN/"
	chmod 755 "$deb/DEBIAN/postinst" "$deb/DEBIAN/prerm"
	cat >"$deb/DEBIAN/control" <<CONTROL
Package: anywhere-file
Version: $version
Section: net
Priority: optional
Architecture: $arch
Maintainer: anywhere-file <noreply@anywhere-file.io>
Description: serve this PC's applications on the LAN and from anywhere
 The agent holds this PC's identity, announces it on the local network and serves the
 applications it is configured for, starting with dufs for files. Enrolled with a server,
 it also keeps one outbound connection open so the same applications are reachable from
 anywhere, with no inbound port and no fixed address.
CONTROL
	dpkg-deb --build --root-owner-group -Zgzip "$deb" "$out/anywhere-file_${version}_${arch}.deb" >/dev/null
	echo "$out/anywhere-file_${version}_${arch}.deb"
done
