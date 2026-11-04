package main

// Hub-facing surface: machine-readable status/pending, and the one-row
// approver used by the SecondBrain UI. Invariant #1 still holds: one explicit
// human decision == one row. There is deliberately no multi-id form.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// rowID is a stable id for one staged row: it changes if the row changes, so a
// stale or forged request cannot approve a different action.
func rowID(planPath string, a Action) string {
	h := sha256.Sum256([]byte(filepath.Base(planPath) + "\t" + a.Kind + "\t" + a.Src + "\t" + a.Dst))
	return fmt.Sprintf("%x", h[:8])
}

func printJSON(v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return 1
	}
	fmt.Println(string(b))
	return 0
}

type jsonRow struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	Size    int64  `json:"size"`
	AgeDays int    `json:"age_days"`
	Reason  string `json:"reason"`
	Flag    string `json:"flag"`
}

type jsonPlan struct {
	Plan string    `json:"plan"`
	Rows []jsonRow `json:"rows"`
}

func pendingPlans() []jsonPlan {
	out := []jsonPlan{}
	for _, p := range loadPlans("plan-*") {
		jp := jsonPlan{Plan: filepath.Base(p.Path), Rows: []jsonRow{}}
		for _, a := range p.Actions {
			jp.Rows = append(jp.Rows, jsonRow{rowID(p.Path, a), a.Kind, a.Src, a.Dst, a.Size, a.AgeDays, a.Reason, a.Flag})
		}
		out = append(out, jp)
	}
	return out
}

func cmdPendingJSON() int {
	return printJSON(map[string]any{"plans": pendingPlans()})
}

func cmdStatus(args []string) int {
	s := gatherStats()
	lastRun := ""
	if m := latestManifest(); m != "" {
		lastRun = filepath.Base(m)
	}
	return printJSON(map[string]any{
		"version":           Version,
		"clutter_score":     s.clutterScore(),
		"pending_actions":   s.PendingActions,
		"quarantined_files": s.Quarantined,
		"desktop_files":     s.DeskFiles,
		"downloads_stale":   s.DLStale,
		"ai":                map[string]any{"up": aiUp()},
		"last_run":          lastRun,
		"load":              busyLevel(),
		"plans":             pendingPlans(),
	})
}

// decideOne resolves exactly one staged row. approve=false just drops it
// (a "no" decision); neither path ever touches a file except an approved row
// through execAction (so DELETE still means quarantine, and undo works).
func decideOne(args []string, approve bool) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: pmax approve-one|reject-one <plan-file> <row-id>")
		return 2
	}
	if StageOnly {
		fmt.Fprintln(os.Stderr, "stage-only mode: refusing to decide")
		return 1
	}
	planName, id := args[0], args[1]
	defer lockStaged()()                     // two quick UI clicks must not rewrite the plan from stale copies
	if filepath.Base(planName) != planName { // bare staged plan name only
		fmt.Fprintln(os.Stderr, "invalid plan name")
		return 1
	}
	for _, p := range loadPlans("plan-*") {
		if filepath.Base(p.Path) != planName {
			continue
		}
		for i, a := range p.Actions {
			if rowID(p.Path, a) != id {
				continue
			}
			if approve {
				// The id names a row, not the file now on disk: if something else took
				// this name since staging, refuse instead of quarantining it.
				if st, err := os.Lstat(a.Src); err == nil && !st.IsDir() && st.Size() != a.Size {
					fmt.Fprintln(os.Stderr, "  ! file changed since it was staged — re-run pmax sweep/organize")
					return 1
				}
				if err := execAction(a); err != nil {
					fmt.Fprintf(os.Stderr, "  ! %v\n", err)
					return 1 // row stays staged
				}
				signal("approved", a.Kind, a.Reason, a.Src, a.Dst)
			} else {
				signal("rejected", a.Kind, a.Reason, a.Src, a.Dst)
			}
			if p.Count() == 1 {
				os.Remove(p.Path)
			} else {
				rewritePlanWithout(p, []int{i})
			}
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "no such staged row (stale id?). See: pmax pending --json")
	return 1
}

func cmdApproveOne(args []string) int { return decideOne(args, true) }
func cmdRejectOne(args []string) int  { return decideOne(args, false) }

// lockStaged takes an exclusive lock on the staged dir; call the result to release.
func lockStaged() func() {
	f, err := os.OpenFile(filepath.Join(CfgState, "staged", ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}
