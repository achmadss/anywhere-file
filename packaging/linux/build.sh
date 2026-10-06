#!/usr/bin/env bash
# Builds the Linux packages: a tarball that installs under the user's home with no root,
# and a deb and an rpm for the distributions that take one. All carry the agent and dufs.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
version=${VERSION:-0.0.0}
# rpm takes no dash in a version, and sorts a tilde before the release it leads up to.
rpmversion=${version//-/\~}
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
	GOOS=linux GOARCH=$arch go build -C "$root" -ldflags "-X main.version=$version" -o "$dir/agent" ./device/agent
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

	# The binaries live together under /usr/lib, and /usr/bin gets a link. That keeps dufs
	# out of the way of a dufs the distribution may have packaged, and next to the agent,
	# which is where the agent looks for it. The deb and the rpm are made from this one tree.
	pkg="$work/pkg-$arch"
	mkdir -p "$pkg/usr/lib/anywhere-file" "$pkg/usr/bin" "$pkg/usr/lib/firewalld/services"
	cp "$dir/agent" "$dir/dufs" "$pkg/usr/lib/anywhere-file/"
	if [ -f "$dir/agent.env" ]; then cp "$dir/agent.env" "$pkg/usr/lib/anywhere-file/"; fi
	cp "$here/anywhere-file.xml" "$pkg/usr/lib/firewalld/services/"
	ln -sf ../lib/anywhere-file/agent "$pkg/usr/bin/anywhere-file-agent"

	if command -v dpkg-deb >/dev/null 2>&1; then
		mkdir -p "$pkg/DEBIAN"
		cp "$here/postinst" "$here/prerm" "$pkg/DEBIAN/"
		chmod 755 "$pkg/DEBIAN/postinst" "$pkg/DEBIAN/prerm"
		cat >"$pkg/DEBIAN/control" <<CONTROL
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
		dpkg-deb --build --root-owner-group -Zgzip "$pkg" "$out/anywhere-file_${version}_${arch}.deb" >/dev/null
		echo "$out/anywhere-file_${version}_${arch}.deb"
	else
		echo "no dpkg-deb here, so no deb for $arch" >&2
	fi

	if ! command -v rpmbuild >/dev/null 2>&1; then
		echo "no rpmbuild here, so no rpm for $arch" >&2
		continue
	fi
	case $arch in
	amd64) rpmarch=x86_64 ;;
	arm64) rpmarch=aarch64 ;;
	esac
	# The deb's scripts, called the way dpkg calls them. rpm passes a count of the copies
	# left instead of a word. An upgrade runs the new package's scripts before the old one's
	# removal scripts, so the service is stopped by %pre, as the deb's prerm does it, and the
	# old %preun does nothing. Only a real removal, a count of 0, signs the PC out.
	cat >"$work/anywhere-file.spec" <<SPEC
Name: anywhere-file
Version: $rpmversion
Release: 1
Summary: serve this PC's applications on the LAN and from anywhere
License: Proprietary
URL: https://anywhere-file.io
AutoReqProv: no
# runuser, which the scripts use to reach the user's systemd manager.
Requires: util-linux
%global debug_package %{nil}
%global __os_install_post %{nil}
%global _build_id_links none

%description
The agent holds this PC's identity, announces it on the local network and serves the
applications it is configured for, starting with dufs for files. Enrolled with a server,
it also keeps one outbound connection open so the same applications are reachable from
anywhere, with no inbound port and no fixed address.

%install
cp -a "$pkg/usr" %{buildroot}/

%pre
[ "\$1" -ge 2 ] || exit 0
set -- upgrade
$(cat "$here/prerm")

%post
$(cat "$here/postinst")

%preun
[ "\$1" -eq 0 ] || exit 0
set -- remove
$(cat "$here/prerm")

%files
%defattr(-,root,root,-)
/usr/lib/anywhere-file
/usr/bin/anywhere-file-agent
/usr/lib/firewalld/services/anywhere-file.xml
SPEC
	rpmbuild -bb --quiet --target "$rpmarch" \
		--define "_topdir $work/rpmbuild" --define "_rpmdir $out" \
		--define "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm" \
		"$work/anywhere-file.spec" >/dev/null
	echo "$out/anywhere-file-$rpmversion-1.$rpmarch.rpm"
done
