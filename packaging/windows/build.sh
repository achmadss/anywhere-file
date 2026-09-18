#!/usr/bin/env bash
# Builds the Windows package: an MSI holding the agent and dufs, the firewall rules the LAN
# needs, and the call that installs the service. Runs on Windows only, because WiX and
# msiexec do.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
version=${VERSION:-0.0.0}
dufs_version=$(cat "$root/packaging/dufs.version")
# Pinned, and not at the latest: WiX v7 refuses to build until someone accepts the Open
# Source Maintenance Fee licence. v5 is the last one that is free to use as it stands.
wix_version=5.0.2
out=${OUT:-$root/dist}

command -v cygpath >/dev/null || {
	echo "the MSI is built on Windows, from a shell with cygpath such as Git Bash" >&2
	exit 1
}
w() { cygpath -w "$1"; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
stage="$work/stage"
mkdir -p "$stage" "$out"

GOOS=windows GOARCH=amd64 go build -C "$root" -o "$stage/agent.exe" ./device/agent
curl -fsSLo "$work/dufs.zip" \
	"https://github.com/sigoden/dufs/releases/download/v${dufs_version}/dufs-v${dufs_version}-x86_64-pc-windows-msvc.zip"
# No unzip in Git Bash, and PowerShell is always there.
powershell -NoProfile -Command \
	"Expand-Archive -Force -Path '$(w "$work/dufs.zip")' -DestinationPath '$(w "$stage")'"

# WiX is a dotnet tool. The runner has dotnet and not WiX, and a developer machine may have
# either, so this installs it once and finds it wherever the install put it. Everything it
# says goes to stderr, because stdout here is the path of the package.
export PATH="$PATH:$(cygpath -u "${USERPROFILE:-$HOME}")/.dotnet/tools"
{
	command -v wix >/dev/null || dotnet tool install --global wix --version "$wix_version"
	wix extension add -g "WixToolset.Firewall.wixext/$wix_version"
} >&2

msi="$out/anywhere-file-$version.msi"
args=(build -arch x64 -nologo -d "Version=$version" -d "Stage=$(w "$stage")")
if [ -n "${AGENT_ENV:-}" ]; then
	args+=(-d "AgentEnv=$AGENT_ENV")
fi
args+=(-ext WixToolset.Firewall.wixext -o "$(w "$msi")" "$(w "$here/anywhere-file.wxs")")
wix "${args[@]}" >&2
echo "$msi"
