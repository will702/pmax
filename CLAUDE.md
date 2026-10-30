# CLAUDE.md — working agreements for agents (and humans) editing this repo

Adapted from Andrej Karpathy's guidelines on common LLM coding pitfalls, made
specific to PMAX. Read before changing anything here.

## The non-negotiable invariants

These define the product. Any change that weakens them is wrong, no matter
how elegant:

1. **Every file move and deletion goes through the per-item approver.** No
   bulk-apply flags, no scheduled execution, no auto-approve on TTY stdin.
   EOF and empty answers stop execution (fail-safe).
2. **DELETE means quarantine**, never `os.Remove` on user files, with
   manifest-undo and retention purge handled only by the watcher.
3. **Learned/model rules are classification-only.** Destinations must pass the
   prefix whitelist at *both* accept-time and load-time (defense in depth).
4. **Zero external Go dependencies.** Stdlib only. The Python sidecar may use
   the uv venv, but `go.mod` stays dependency-free.
5. **Tests never touch the real home.** Everything runs in `t.TempDir()`
   sandboxes via the `sandbox(t)` helper.

## Think before coding

- State your assumptions before implementing; ask when a requirement is
  ambiguous (especially anything touching deletion semantics).
- PMAX runs on real people's real files. Design for "what if this file is
  someone's thesis" in every code path.

## Simplicity first

- One package, plain functions, no interfaces until a second implementation
  exists. No speculative config keys, no abstractions for imagined futures.
- If a feature needs more than ~150 lines, ask whether it belongs in pmax at
  all. The Unix heart (`organize`, `sweep`, `dupes`, `approve`, `undo`) stays
  small; the AI layer is optional spice that must degrade gracefully.

## Surgical changes

- Touch only what the task requires. No drive-by refactors, no reformatting
  untouched functions, no renaming public-ish helpers other code uses.
- Match the existing style: table-driven tests, `quiet(t, ...)` for stdout,
  `withStdin(t, ...)` for approver sessions.

## Goal-driven execution

- Convert each change into a verifiable goal FIRST: a failing test that
  encodes the safety property, then the code that passes it. The existing
  suite is the contract — extend it before extending behavior.
- After changes: `tests/run-tests.sh` must pass clean (go vet + tests +
  sidecar lint + plist lint).

## Where things live

- `core.go` paths/config/staging/manifest · `approve.go` the safety gate
- `organize/sweep/dupes/projects.go` action staging · `approve` executes
- `dream.go` replay engine · `autotune.go` budgeted rule search
- `improve.go` propose→decide loop + lessons · `ai.go` sidecar clients
- `sidecar/` Python only (MLX + laya) · `agents/` launchd · `tests/` wrapper
