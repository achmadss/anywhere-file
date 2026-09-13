#!/usr/bin/env bash
# Fetch the pinned iroh source and apply our patches on top.
#
# relay/src/ is gitignored: it is upstream's tree, not ours. What we version is
# IROH_VERSION plus patches/. See README.md.
set -euo pipefail

cd "$(dirname "$0")"
VERSION="$(cat IROH_VERSION)"
SRC=src

if [ -d "$SRC" ]; then
  echo "relay/src exists — remove it to re-fetch." >&2
  echo "  rm -rf relay/src && relay/apply.sh" >&2
  exit 1
fi

echo "fetching iroh $VERSION"
git clone --depth 1 --branch "$VERSION" https://github.com/n0-computer/iroh.git "$SRC"

shopt -s nullglob
patches=(patches/*.patch)
if [ ${#patches[@]} -eq 0 ]; then
  echo "no patches — relay/src is stock iroh $VERSION"
  exit 0
fi

for p in "${patches[@]}"; do
  echo "applying $p"
  git -C "$SRC" apply --3way "../$p"
done

echo "applied ${#patches[@]} patch(es) to iroh $VERSION"
