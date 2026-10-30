#!/bin/bash
# pmax uninstaller — reverses install.sh completely.
# Staged plans, manifests, quarantine and the index are KEPT by default so
# nothing in quarantine is lost; pass --purge to remove state as well.
set -euo pipefail

BIN_DIR="$HOME/.local/bin"
CONF_DIR="$HOME/.config/pmax"
STATE_DIR="$HOME/.local/state/pmax"
AGENTS_DST="$HOME/Library/LaunchAgents"
UID_N=$(id -u)

PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

for agent in com.pmax.morning com.pmax.evening com.pmax.weekly com.pmax.watcher com.pmax.ai; do
  launchctl bootout "gui/$UID_N/$agent" 2>/dev/null || true
  rm -f "$AGENTS_DST/$agent.plist"
done
echo "✓ launchd agents removed"

rm -f "$BIN_DIR/pmax" "$BIN_DIR/colgrep"
echo "✓ binaries removed ($BIN_DIR/pmax, $BIN_DIR/colgrep)"

if [ "$PURGE" = "1" ]; then
  echo "  --purge: removing state (quarantine files will be LOST — undo first if needed!)"
  rm -rf "$STATE_DIR"
  echo "✓ state removed ($STATE_DIR)"
else
  echo "! state kept at $STATE_DIR (staged plans, quarantine, index)"
  echo "  remove manually with: rm -rf $STATE_DIR"
fi
echo "✓ config kept at $CONF_DIR (remove manually if you like)"
echo "pmax uninstalled."
