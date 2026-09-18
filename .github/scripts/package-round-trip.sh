#!/usr/bin/env bash
# #124's acceptance, as far as a runner can carry it: build the package for this OS,
# install it, and check that the service answers with nobody having started it, that the
# uninstall stops it, and that installing again is the same device. The reboot is the one
# part of the acceptance that needs a real machine.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
export VERSION=${VERSION:-0.0.0}
# The runner has no keychain to unlock, no multicast and no server, so the package is built
# with the settings that leave only the gateway. A customer's package is built with none.
export AGENT_ENV="RFM_AGENT_ADDR=$RFM_AGENT_ADDR RFM_AGENT_KEYSTORE=$RFM_AGENT_KEYSTORE RFM_AGENT_MDNS=$RFM_AGENT_MDNS RFM_AGENT_TUNNEL=$RFM_AGENT_TUNNEL"

case "$(uname -s)" in
Darwin)
	pkg=$("$root/packaging/macos/build.sh" | tail -1)
	agent=/Applications/anywhere-file.app/Contents/MacOS/agent
	dufs=/Applications/anywhere-file.app/Contents/MacOS/dufs
	install_it() { sudo installer -pkg "$pkg" -target /; }
	remove_it() { /Applications/anywhere-file.app/Contents/MacOS/uninstall; }
	;;
Linux)
	deb=$(ARCHES=amd64 "$root/packaging/linux/build.sh" | grep '\.deb$')
	agent=/usr/lib/anywhere-file/agent
	dufs=/usr/lib/anywhere-file/dufs
	install_it() { sudo dpkg -i "$deb"; }
	remove_it() { sudo dpkg -r anywhere-file; }
	;;
MINGW* | MSYS* | CYGWIN*)
	msi=$("$root/packaging/windows/build.sh" | tail -1)
	agent="/c/Program Files/anywhere-file/agent.exe"
	dufs="/c/Program Files/anywhere-file/dufs.exe"
	# MSYS_NO_PATHCONV because this shell would otherwise turn msiexec's /i into a path.
	install_it() { MSYS_NO_PATHCONV=1 msiexec.exe /i "$(cygpath -w "$msi")" /quiet /norestart; }
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

echo "== install"
install_it
up
first=$(device_id)
echo "device id: $first"
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

echo "== uninstall"
remove_it
down
if [ -x "$agent" ]; then
	echo "$agent is still there after the uninstall"
	exit 1
fi
if declare -f firewall_rules >/dev/null; then
	powershell -NoProfile -Command "New-NetFirewallRule -DisplayName 'anywhere-file stray' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 7433 | Out-Null"
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

remove_it
down
echo "the package installs, uninstalls and reinstalls without the PC changing identity"
