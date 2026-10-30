#!/bin/bash
# pmax installer — builds the Go binary, installs to ~/.local/bin (already on
# your PATH), sets up config + sidecar scripts, and loads the 4 launchd agents.
# The MLX/laya AI layer stays opt-in: run `pmax ai setup` when you want it.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/.local/bin"
CONF_DIR="$HOME/.config/pmax"
STATE_DIR="$HOME/.local/state/pmax"
AGENTS_SRC="$ROOT/agents"
AGENTS_DST="$HOME/Library/LaunchAgents"
UID_N=$(id -u)

say() { printf '✓ %s\n' "$*"; }

# 1) Build (Go toolchain required for install only; the binary is standalone).
echo "→ building pmax…"
cd "$ROOT"
go build -trimpath -ldflags "-s -w" -o "$ROOT/build/pmax" .
mkdir -p "$BIN_DIR" "$CONF_DIR" "$STATE_DIR"/{logs,staged,sidecar,run}
install -m 0755 "$ROOT/build/pmax" "$BIN_DIR/pmax"
ln -sf "$BIN_DIR/pmax" "$BIN_DIR/colgrep"
say "installed $BIN_DIR/pmax (+ colgrep multicall link)"

# 2) Config (never overwrite an existing one).
if [ ! -f "$CONF_DIR/pmax.conf" ]; then
  install -m 0644 "$ROOT/config/pmax.conf.example" "$CONF_DIR/pmax.conf"
  say "config created at $CONF_DIR/pmax.conf"
else
  say "config already exists — left untouched ($CONF_DIR/pmax.conf)"
fi

# 3) Sidecar scripts (python runs from the venv created by `pmax ai setup`).
install -m 0755 "$ROOT"/sidecar/*.py "$STATE_DIR/sidecar/"
say "sidecar scripts installed to $STATE_DIR/sidecar"

# 4) launchd agents — observation-only by design (see README).
for agent in com.pmax.morning com.pmax.evening com.pmax.weekly com.pmax.watcher; do
  sed -e "s#__PMAX_BIN__#$BIN_DIR/pmax#g" -e "s#__PMAX_STATE__#$STATE_DIR#g" \
    "$AGENTS_SRC/$agent.plist" > "$AGENTS_DST/$agent.plist"
  launchctl bootout "gui/$UID_N/$agent" 2>/dev/null || true
  launchctl bootstrap "gui/$UID_N" "$AGENTS_DST/$agent.plist"
done
say "4 launchd agents loaded (morning 9:00 · evening 21:00 · weekly Sun 18:00 · watcher 03:00 stage-only)"

# 5) Health check.
echo
"$BIN_DIR/pmax" doctor || true
echo
echo "Next steps:"
echo "  pmax remind test      # allow the notification when macOS asks"
echo "  pmax report           # see the damage"
echo "  pmax organize         # stage + review proposals (nothing moves without your y)"
echo "  pmax ai setup         # optional: MLX + laya (colgrep, smart mode, improve)"
