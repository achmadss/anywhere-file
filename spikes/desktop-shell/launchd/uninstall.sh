#!/usr/bin/env bash
set -u
LABEL=dev.anywherefile.spike.headless
launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/$LABEL.plist" "/tmp/$LABEL".*.log /tmp/rfm-spike-headless.json
launchctl print "gui/$UID/$LABEL" >/dev/null 2>&1 && echo "still loaded" || echo "removed"
