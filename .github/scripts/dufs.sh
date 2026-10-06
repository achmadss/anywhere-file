#!/usr/bin/env bash
# Downloads the dufs build the agent ships with (#124) into the working directory and puts
# it on the PATH of the steps that follow. Linux x86_64 only, the one runner that uses it.
set -euo pipefail

version=$(cat packaging/dufs.version)
name="dufs-v${version}-x86_64-unknown-linux-musl.tar.gz"
curl -sSLo dufs.tar.gz "https://github.com/sigoden/dufs/releases/download/v${version}/${name}"
tar xzf dufs.tar.gz dufs
echo "$PWD" >> "$GITHUB_PATH"
