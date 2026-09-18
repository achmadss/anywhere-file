#!/bin/sh
# Reverses install.sh. The device key and the agent's own directory stay where they are.
set -eu

prefix=${PREFIX:-$HOME/.local}
dest=$prefix/lib/anywhere-file
[ -x "$dest/agent" ] && "$dest/agent" uninstall || true
rm -rf "$dest" "$prefix/bin/anywhere-file-agent"
echo "the device key and the agent's directory are untouched, so installing again gets"
echo "this PC its own identity back."
