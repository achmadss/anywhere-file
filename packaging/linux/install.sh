#!/bin/sh
# Installs the agent for the user running it, under ~/.local, with no root anywhere. Any
# RFM_AGENT_X=y arguments are passed to `agent install`, which writes them into the service
# manifest.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
prefix=${PREFIX:-$HOME/.local}
dest=$prefix/lib/anywhere-file

mkdir -p "$dest" "$prefix/bin"
cp "$here/agent" "$here/dufs" "$dest/"
if [ -f "$here/agent.env" ]; then cp "$here/agent.env" "$dest/"; fi
chmod 755 "$dest/agent" "$dest/dufs"
ln -sf "$dest/agent" "$prefix/bin/anywhere-file-agent"

if [ -f "$dest/agent.env" ]; then
	while IFS= read -r line; do
		case "$line" in RFM_AGENT_*=*) set -- "$@" "$line" ;; esac
	done <"$dest/agent.env"
fi
"$dest/agent" install "$@"

echo
echo "dufs is in $dest, and the agent finds it there without it being on your PATH."
echo "If this machine runs a firewall, open TCP 7433 and UDP 5353 on the local network."
