#!/usr/bin/env bash
# Installs the headless half as a per-user launchd agent, no root, no admin prompt.
# The plist is generated here so it carries an absolute path to the built binary.
set -euo pipefail
cd "$(dirname "$0")/.."

LABEL=dev.anywherefile.spike.headless
BIN="$PWD/target/release/headless"
ADDR="${ADDR:-/tmp/rfm-spike-headless.json}"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

[ -x "$BIN" ] || { echo "build first: cargo build --release"; exit 1; }
mkdir -p "$HOME/Library/LaunchAgents"

cat >"$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array><string>$BIN</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>SPIKE_ADDR_FILE</key><string>$ADDR</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>/tmp/$LABEL.out.log</string>
  <key>StandardErrorPath</key><string>/tmp/$LABEL.err.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$UID" "$PLIST"
launchctl print "gui/$UID/$LABEL" | sed -n '1,12p'
echo "installed $PLIST"
