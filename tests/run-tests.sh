#!/bin/bash
# pmax test runner — the full sandbox suite. Nothing touches your real home:
# every test runs inside t.TempDir() sandboxes with swapped PMAX_HOME/PMAX_STATE.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "── go vet"
go vet ./...

echo "── unit + integration tests (sandboxed)"
go test ./... -count=1

echo "── python sidecar syntax"
for f in sidecar/*.py; do
  python3 -m py_compile "$f" && echo "  ok  $f"
done

echo "── plist lint"
for f in agents/*.plist agents/*.plist.example; do
  plutil -lint "$f" >/dev/null && echo "  ok  $f"
done

echo
echo "All checks passed. ✓"
