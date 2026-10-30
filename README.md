# PMAX — Mac Productivity Framework

A single Go binary (plus an optional MLX/laya AI sidecar) that keeps your Mac
decluttered with one hard guarantee:

> **Every deletion and every move is approved by you, one item at a time.**
> There is no `--apply`, no "yes to all" (off by default), no scheduled
> execution. Anything "deleted" goes to a 14-day quarantine and `pmax undo`
> reverses it.

Built for the mess it was born into: 86 loose screenshots on Desktop, 491
files in Downloads (85 dupes, 12 stalled `.crdownload`), 100+ project folders
at `~`, and a `~/projects/` sitting empty.

## Install

```sh
./install.sh          # builds Go binary → ~/.local/bin/pmax (+ colgrep), loads 4 launchd agents
pmax remind test      # allow the notification when macOS asks
pmax report           # see the damage
```

No `.zshrc` changes — `~/.local/bin` is already on your PATH.

## Daily use

```sh
pmax report            # clutter score 0–10 + zone stats
pmax organize          # stage Desktop+Downloads sorting → review each item (y/n)
pmax sweep             # stage junk (.DS_Store, stalled, stale, name-dupes) → review
pmax dupes             # checksum duplicates (SHA-256), keep-newest proposals
pmax projects          # consolidate ~/stray-dirs → ~/projects/ one by one
pmax review            # handle only the ambiguous/flagged items
pmax undo              # reverse the last approved run
```

Every action command prints a per-item prompt:

```
[3/86] MOVE   ~/Desktop/Screenshot 2026-10-24 at 09.11.02.png  (1.2 MB, 6d old)
       → ~/Pictures/Screenshots/2026-10/
       why: screenshot
       Approve? [y]es / [n]o / [d]etails / [q]uit
```

`n` skips, `q` leaves the rest staged (`pmax pending`, `pmax approve`), EOF
stops everything. Only `y` executes — recorded for undo.

## Reminders (launchd, observation-only)

| When | What |
|---|---|
| 09:00 daily | Morning briefing: screenshots waiting, stale count, staged actions |
| 21:00 daily | Evening tidy: today's new files + 2-minute sweep nudge |
| Sun 18:00 | Weekly deep-clean: clutter score + `pmax improve` prompt |
| 03:00 daily | Watcher: scans, **stages** proposals, purges expired quarantine — never executes |

## AI layer (opt-in): MLX + laya

```sh
pmax ai setup          # uv venv + mlx/mlx-lm/mlx-embeddings + laya (~2 GB models)
pmax index             # embed Desktop/Downloads/Documents/projects → index.bin
colgrep "thesis figures"   # semantic search; falls back to rg-style keyword mode
pmax ask "where is my invoice from telkomsel?"
pmax organize --smart  # laya classifies "Other" files (abstains → review flag)
pmax improve           # self-improvement loop (below)
```

All inference is **Apple-Silicon native** (MLX on Metal, warm resident
sidecar on `127.0.0.1:8765`). Everything degrades gracefully without it.

**[laya](https://github.com/NandhaKishorM/laya)** (open-source Jev alternative)
makes single-pass typed decisions — `choice`, `score`, `noul` (yes/no) — with
abstention. PMAX uses it where a 3B model is overkill: classifying files in
`organize --smart`, ordering the review queue by P(junk), and double-gating
`improve` proposals.

## Recursive self-improvement

```
Sense (signals.tsv: approved/rejected/undone per rule)
  → Dream (replay your history; score candidate rule-sets offline)   ← Dream-RSI
  → Autotune (budgeted keep-or-revert search in the replay world)    ← autoresearch
  → Propose (survivors + Qwen drafts, STRICT JSON validated)
  → Gate (laya noul safety check + structural guards + lessons.md)   ← harness2
  → Decide (you accept/reject each candidate — nothing auto-applies)
  → Act (learned rules shape future PROPOSALS, still approval-gated)
  → re-Sense ↩
```

- `pmax dream` — rank candidates by replaying your actual y/n history (+1
  agreement with approvals, −1 with rejections, parsimony penalty)
- `pmax autotune --minutes 20` — overnight-style keep-or-revert rule search;
  survivors queue up as pre-scored proposals; `rules.learned` is only ever
  written by your explicit `y` in `pmax improve`
- `lessons.md` accumulates avoid/prefer lessons across sessions and steers
  future proposals

Guardrails: classification-only (can never propose deletes), destination
prefix whitelist (checked at accept-time AND load-time), max 50 rules FIFO,
`pmax improve --rollback`. See `docs/USAGE.md` for the full loop.

## Agent guidelines

`CLAUDE.md` encodes the working agreements for any coding agent (or human)
editing this repo: the five non-negotiable invariants (per-item approval,
quarantine-only deletes, classification-only rules, zero Go deps, sandboxed
tests) plus Karpathy's think-before-coding / simplicity / surgical-changes /
goal-driven-execution principles.

## Safety model

1. Stage → approve per item. No bulk paths.
2. DELETE = quarantine (`~/.local/state/pmax/quarantine/<run>/`), 14-day retention.
3. Manifest per run → `pmax undo`.
4. Keep-list: dotfiles, Documents, Pictures, projects, this repo — never touched.
5. Watcher agent runs with `--stage-only`; the approval path requires answers.
6. `PMAX_ASSUME_YES` is honored only on non-TTY stdin (tests); terminal humans always get asked.

## Layout

```
bin via ~/.local/bin: pmax, colgrep (multicall)
~/.config/pmax/     pmax.conf, rules.learned
~/.local/state/pmax/ logs, staged plans, manifests, quarantine, index.bin,
                     learning/signals.tsv, sidecar/, venv/ (opt-in)
```

## Tests

```sh
tests/run-tests.sh    # go vet + sandboxed go tests + sidecar/plist lint
```

Core safety assertions: approving item N moves only item N; all-`n` changes
nothing; EOF executes nothing; stage-only/watcher never mutate; undo restores
byte-for-byte; learned rules cannot target deletion paths.

## Uninstall

```sh
./uninstall.sh        # keeps state/quarantine by default; --purge removes it
```
