// pmax — Mac productivity framework.
// Every move/deletion is staged first and executed only after per-item approval.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const usage = `pmax — Mac productivity framework (approve-everything safety)

  pmax report [--big]          clutter score, sizes, staleness; --big = space hogs
  pmax organize [zone] [--smart]  stage sorting for desktop|downloads, then review
  pmax sweep [zone]            stage junk proposals (.DS_Store, .crdownload, stale, dupes)
  pmax dupes [dir]             stage checksum-duplicate quarantines (all but newest)
  pmax projects [report|move]  home-root consolidation into ~/projects/
  pmax pending                 list staged-but-unapproved actions
  pmax approve [plan]          review a staged plan, item by item
  pmax review                  review only flagged/ambiguous items
  pmax undo [--run id]         reverse the last approved run
  pmax remind [kind|test]      send a briefing notification
  pmax ai setup|start|stop|status|ensure   MLX sidecar lifecycle
  pmax index [--full]          (re)build the colgrep embedding index
  pmax colgrep "query" [-i]    semantic file search (also: colgrep "query")
  pmax ask "..."               semantic find + AI summary
  pmax improve                 self-improvement: propose learned rules for your approval
  pmax dream                   replay your approve/reject history; rank candidate rules
  pmax autotune [--minutes N]  budgeted keep-or-revert rule search in the dream world
  pmax doctor                  health check
  pmax watcher                 03:00 agent entry: stage-only scan + notify (never executes)

Global: --stage-only (never execute; used by the watcher agent)
        PMAX_HOME / PMAX_STATE env override config/state dirs (used by tests)

Safety: every MOVE/DELETE is approved one item at a time. There is no bulk apply.
`

func main() {
	// Multicall: a symlink named `colgrep` routes to semantic search.
	if filepath.Base(os.Args[0]) == "colgrep" {
		os.Exit(run(append([]string{"colgrep"}, os.Args[1:]...)))
	}
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	stageOnly := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--stage-only":
			stageOnly = true
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		case "-v", "--version":
			fmt.Printf("pmax %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
			return 0
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Print(usage)
		return 0
	}

	if err := initCore(stageOnly); err != nil {
		fmt.Fprintln(os.Stderr, "pmax:", err)
		return 1
	}

	cmd := rest[0]
	args = rest[1:]
	switch cmd {
	case "help":
		fmt.Print(usage)
	case "doctor":
		return cmdDoctor(args)
	case "report":
		return cmdReport(args)
	case "organize":
		return cmdOrganize(args)
	case "sweep":
		return cmdSweep(args)
	case "dupes":
		return cmdDupes(args)
	case "projects":
		return cmdProjects(args)
	case "pending":
		return cmdPending()
	case "approve":
		return cmdApprove(args)
	case "review":
		return cmdReview()
	case "undo":
		return cmdUndo(args)
	case "remind":
		return cmdRemind(args)
	case "watcher":
		return cmdWatcher(args)
	case "ai":
		return cmdAI(args)
	case "index":
		return cmdIndex(args)
	case "colgrep":
		return cmdColgrep(args)
	case "ask":
		return cmdAsk(args)
	case "improve":
		return cmdImprove(args)
	case "dream":
		return cmdDream(args)
	case "autotune":
		return cmdAutotune(args)
	case "version":
		fmt.Printf("pmax %s\n", Version)
	default:
		fmt.Fprintf(os.Stderr, "pmax: unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		return 1
	}
	return 0
}

// ── tiny stdlib helpers shared across command files ─────────────────────────

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
