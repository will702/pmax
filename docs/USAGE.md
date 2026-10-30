# PMAX Usage Reference

See `README.md` for the big picture. This is the day-to-day cheat sheet.

## The golden rule

Every `MOVE`/`DELETE` pmax proposes is staged first. The approver shows one
item at a time and only `y` executes it:

```
[3/86] MOVE   ~/Desktop/Screenshot 2026-10-24 at 09.11.02.png  (1.2 MB, 6d old)
       → ~/Pictures/Screenshots/2026-10/
       why: screenshot
       Approve? [y]es / [n]o / [d]etails / [q]uit
```

`q` or Ctrl-D stops; the rest stays staged. `pmax pending` lists staged plans,
`pmax approve` reviews the oldest. `pmax undo` reverses the last run.

## Commands

| Command | Notes |
|---|---|
| `pmax report [--big]` | Clutter score 0–10; `--big` ranks space hogs |
| `pmax organize [desktop\|downloads] [--smart]` | Stage sorting proposals; `--smart` adds laya classification of "Other" files |
| `pmax sweep [desktop\|downloads]` | Stage junk: `.DS_Store`, stalled partials, stale (>30d), name-dupes |
| `pmax dupes [dir] [--min-size N]` | SHA-256 duplicates ≥N MB; keep newest of each set |
| `pmax projects [report\|move <name>\|files]` | Home-root consolidation, one dir at a time |
| `pmax pending` / `pmax approve [plan]` / `pmax review` | Work the staged queue; `review` = flagged items only, junk-sorted when laya is up |
| `pmax undo [--run id]` | Reverse an approved run |
| `pmax remind [morning\|evening\|weekly\|test]` | Fire a briefing now |
| `pmax doctor` | Health check |

## AI layer (after `pmax ai setup`)

| Command | Notes |
|---|---|
| `pmax ai status\|start\|stop\|ensure` | MLX sidecar lifecycle (127.0.0.1:8765, Metal) |
| `pmax index [--full]` | Embed Desktop/Downloads/Documents/projects → `index.bin` |
| `colgrep "query" [-i]` | Semantic search; keyword fallback when sidecar is down; `-i` = fzf picker → reveal in Finder |
| `pmax ask "..."` | Top hits + short AI summary |

## Self-improvement loop

```
        ┌──────────── your y/n on every proposal ────────────┐
        ▼                                                    │
  Sense (signals.tsv)  →  Dream (replay your history)  →  Autotune (budgeted
  keep-or-revert search)  →  Improve (you accept/reject rules)  →  rules.learned
        │                                                    │
        └────────── lessons.md accumulates across sessions ──┘
```

| Command | Notes |
|---|---|
| `pmax dream` | Rank candidate rules by replaying your approve/reject history — nothing applied |
| `pmax autotune [--minutes 20]` | Keep-or-revert rule search inside the replay world; survivors queued as proposals; never touches `rules.learned` directly |
| `pmax improve` | Review queued survivors (with replay evidence) + fresh model proposals, one y/n each |
| `pmax improve --rollback` | Revert the last accepted rule batch |

Scoring: `+1` rule matches an approved move, `−1` it matches a rejected one,
`+0.5` redirects a rejected proposal, `−0.25` changes an approved destination,
`−0.05` per rule (parsimony). Undo counts as rejection.

## Config

`~/.config/pmax/pmax.conf` — zones, keep-list, destinations, stale/retention
days, model ids, laya labels, learned-rule caps. `rules.learned` is a plain
editable file: one `glob<TAB>destination` per line, `#` comments allowed.

## State

```
~/.local/state/pmax/
  staged/        pending plans (TSV — human-readable)
  manifests/     per-run execution records (undo data)
  quarantine/    <run-id>/… 14-day retention
  learning/      signals.tsv · lessons.md · experiments.tsv · proposals.json
  index.bin      colgrep index
  logs/          pmax.log · ai-server.log · agent-*.log
```

## launchd agents (loaded by install.sh)

`com.pmax.morning` (9:00) · `com.pmax.evening` (21:00) · `com.pmax.weekly`
(Sun 18:00) · `com.pmax.watcher` (03:00, `--stage-only`, purges expired
quarantine). All observation-only; the watcher can never execute a move.

## Tips

- First run: `pmax report`, then `pmax organize` and just watch the prompts —
  `q` anytime. Speed runs: `pmax review` after a few days; the flagged queue
  is where ambiguity concentrates.
- The evening nudge is the habit that matters: 2 minutes of `pmax sweep`
  keeps the score under 3 forever.
- Changing reminder hours: edit `config/pmax.conf.example` hours AND the
  plists (agents bake the schedule), then re-run `./install.sh`.
