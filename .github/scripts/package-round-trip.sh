#!/usr/bin/env bash
# #124's acceptance, as far as a runner can carry it: build the package for this OS,
# install it, and check that the service answers with nobody having started it, that the
# uninstall stops it, and that installing again is the same device. The reboot is the one
# part of the acceptance that needs a real machine.
#
# #190 comes first: the last release is installed with a folder shared, and the package
# built here goes over it. Signing in needs a server and a browser to approve in, which a
# runner has neither of, so the account is not part of it. It lives in the same agent.json
# as the shares, and the upgrade keeps that file. OLD_PACKAGE names a package file to
# upgrade from instead of the last release.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
repo=${GITHUB_REPOSITORY:-achmadss/anywhere-file}
previous=""
if [ -z "${OLD_PACKAGE:-}" ]; then
	previous=$(gh release view --repo "$repo" --json tagName --jq .tagName 2>/dev/null || true)
fi
# A package installs over another only when its version is higher, so the one built here
# is one past the last release.
if [ -z "${VERSION:-}" ] && [ -n "$previous" ]; then
	v=${previous#v}
	VERSION="${v%.*}.$((${v##*.} + 1))"
fi
export VERSION=${VERSION:-0.0.0}
# The runner has no keychain to unlock, no multicast and no server, so the package is built
# with the settings that leave only the gateway. A customer's package is built with none.
export AGENT_ENV="RFM_AGENT_ADDR=$RFM_AGENT_ADDR RFM_AGENT_KEYSTORE=$RFM_AGENT_KEYSTORE RFM_AGENT_MDNS=$RFM_AGENT_MDNS RFM_AGENT_TUNNEL=$RFM_AGENT_TUNNEL"

case "$(uname -s)" in
Darwin)
	pkg=$("$root/packaging/macos/build.sh" | tail -1)
	agent=/Applications/anywhere-file.app/Contents/MacOS/agent
	dufs=/Applications/anywhere-file.app/Contents/MacOS/dufs
	asset='*.pkg'
	install_it() { sudo installer -pkg "${1:-$pkg}" -target /; }
	remove_it() { /Applications/anywhere-file.app/Contents/MacOS/uninstall; }
	;;
Linux)
	# The runner's own architecture, so the arm64 runner installs the arm64 deb (#229).
	built=$(ARCHES=$(dpkg --print-architecture) "$root/packaging/linux/build.sh")
	deb=$(echo "$built" | grep '\.deb$')
	tarball=$(echo "$built" | grep '\.tar\.gz$')
	agent=/usr/lib/anywhere-file/agent
	dufs=/usr/lib/anywhere-file/dufs
	asset='*_amd64.deb'
	install_it() { sudo dpkg -i "${1:-$deb}"; }
	# Where `agent install` finds the user's systemd manager outside a desktop session.
	export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
	remove_it() { sudo dpkg -r anywhere-file; }
	# #223: a software centre removes it through pkexec, which sets PKEXEC_UID and no SUDO_USER.
	remove_last() { sudo env -u SUDO_USER PKEXEC_UID="$(id -u)" dpkg -r anywhere-file; }
	;;
MINGW* | MSYS* | CYGWIN*)
	# The MSI for the runner's own CPU, x64 or arm64 (#227).
	case ${RUNNER_ARCH:-X64} in
	ARM64) arch=arm64 ;;
	*) arch=amd64 ;;
	esac
	msi=$(ARCHES=$arch "$root/packaging/windows/build.sh" | tail -1)
	agent="/c/Program Files/anywhere-file/agent.exe"
	dufs="/c/Program Files/anywhere-file/dufs.exe"
	# #160: where a person starts sharing again after Quit in the tray menu.
	shortcut="/c/ProgramData/Microsoft/Windows/Start Menu/Programs/anywhere-file.lnk"
	# MSYS_NO_PATHCONV because this shell would otherwise turn msiexec's /i into a path.
	asset='*.msi'
	install_it() { MSYS_NO_PATHCONV=1 msiexec.exe /i "$(cygpath -w "${1:-$msi}")" /quiet /norestart; }
	remove_it() { MSYS_NO_PATHCONV=1 msiexec.exe /x "$(cygpath -w "$msi")" /quiet /norestart; }
	firewall_rules() {
		powershell -NoProfile -Command \
			"@(Get-NetFirewallRule -DisplayName 'anywhere-file*' -ErrorAction SilentlyContinue).Count" |
			tr -d '\r'
	}
	;;
*)
	echo "no package for $(uname -s)" >&2
	exit 1
	;;
esac

up() { "$root/.github/scripts/service-up.sh" up >/dev/null; }
down() { "$root/.github/scripts/service-up.sh" down; }
device_id() { "$agent" key | awk '/^device id:/ { print $3 }'; }
fingerprint() { "$agent" key | awk '/^fingerprint:/ { print $2 }'; }

old=${OLD_PACKAGE:-}
if [ -z "$old" ] && [ -n "$previous" ]; then
	old_dir=$(mktemp -d)
	gh release download "$previous" --repo "$repo" --pattern "$asset" --dir "$old_dir"
	old=$(ls "$old_dir"/*)
fi
if [ -z "$old" ]; then
	echo "== upgrade: skipped, there is no release yet to upgrade from"
else
	echo "== upgrade from $old to $VERSION"
	install_it "$old"
	# A release is built with no settings, and this runner needs the ones above, so the
	# service is installed again with them. The device key and agent.json stay as they are.
	# shellcheck disable=SC2086 # one VAR=value per word
	"$agent" install $AGENT_ENV
	up
	"$agent" share add "$(mktemp -d)" --name upgrade
	key_before=$(fingerprint)
	shares_before=$("$agent" share list)
	echo "fingerprint: $key_before"
	echo "$shares_before"
fi

echo "== install"
install_it
up
first=$(device_id)
if [ -n "$old" ]; then
	key_after=$(fingerprint)
	shares_after=$("$agent" share list)
	if [ "$key_after" != "$key_before" ]; then
		echo "the fingerprint is $key_after after the upgrade, and was $key_before. The PC lost its device key."
		exit 1
	fi
	if [ "$shares_after" != "$shares_before" ]; then
		printf 'the shares after the upgrade are:\n%s\nand were:\n%s\n' "$shares_after" "$shares_before"
		exit 1
	fi
	echo "the upgrade kept the device key and the shares"
fi
echo "device id: $first"
# #96: the certificate on the LAN is the one the device key signed.
AGENT="$agent" "$root/.github/scripts/lan-tls.sh"
# The package puts dufs where the agent can find it without a PATH, which is what a
# registry entry saying `dufs` depends on.
"$dufs" --version

# The rules are the reason this package needs an administrator, so they are checked.
if declare -f firewall_rules >/dev/null; then
	rules=$(firewall_rules)
	echo "firewall rules: $rules"
	if [ "$rules" != 4 ]; then
		echo "want 4 firewall rules after the install, the gateway and mDNS on two profiles"
		exit 1
	fi
fi

if [ -n "${shortcut:-}" ]; then
	[ -f "$shortcut" ] || { echo "no Start menu shortcut at $shortcut"; exit 1; }
	MSYS_NO_PATHCONV=1 schtasks.exe /Query /TN "anywhere-file-tray-$USERNAME" >/dev/null ||
		{ echo "no tray task after the install"; exit 1; }
	echo "the Start menu shortcut and the tray task are there"
fi

echo "== uninstall"
remove_it
down
if [ -n "${shortcut:-}" ] && [ -e "$shortcut" ]; then
	echo "$shortcut is still there after the uninstall"
	exit 1
fi
if [ -x "$agent" ]; then
	echo "$agent is still there after the uninstall"
	exit 1
fi
if declare -f firewall_rules >/dev/null; then
	rules=$(firewall_rules)
	if [ "$rules" != 0 ]; then
		echo "$rules firewall rules are still there after the uninstall, want none"
		exit 1
	fi
	echo "the firewall rules went with it"
fi

echo "== install again"
install_it
up
again=$(device_id)
if [ "$first" != "$again" ]; then
	echo "the device id is $again after a reinstall, and was $first. The PC lost its identity."
	exit 1
fi
echo "same device after a reinstall: $again"

if declare -f remove_last >/dev/null; then remove_last; else remove_it; fi
down

# The tarball installs under the home with its own scripts, and is the same device too.
if [ -n "${tarball:-}" ]; then
	echo "== the tarball's install.sh and uninstall.sh"
	export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}
	agent=$HOME/.local/lib/anywhere-file/agent
	unpacked=$(mktemp -d)
	tar xzf "$tarball" -C "$unpacked"
	"$unpacked"/*/install.sh
	up
	from_tarball=$(device_id)
	if [ "$first" != "$from_tarball" ]; then
		echo "the tarball's install is device $from_tarball, the deb's was $first"
		exit 1
	fi
	"$unpacked"/*/uninstall.sh
	down
	if [ -e "$agent" ]; then
		echo "$agent is still there after uninstall.sh"
		exit 1
	fi
	echo "the tarball installs and uninstalls as the same device"
fi
echo "the package installs, uninstalls and reinstalls without the PC changing identity"
