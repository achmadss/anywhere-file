#!/bin/sh
# Reverses install.sh, signing the PC out of its account first. The device key and the
# agent's own directory stay where they are.
set -eu

prefix=${PREFIX:-$HOME/.local}
dest=$prefix/lib/anywhere-file
if [ -x "$dest/agent" ]; then
	"$dest/agent" logout || echo "could not sign this PC out of its account, so remove it in the app"
	"$dest/agent" uninstall || true
fi
rm -rf "$dest" "$prefix/bin/anywhere-file-agent"
echo "the device key and the agent's directory are untouched, so installing again gets"
echo "this PC its own identity back."
