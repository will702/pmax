package main

// The approver: EVERY move and deletion is approved one item at a time.
// No --apply, no --yes-to-all. EOF or empty answer stops execution (fail-safe).

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stdinIsTTY reports whether we can interact with a human.
func stdinIsTTY() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// execAction performs one approved action. DELETE = move into quarantine.
func execAction(a Action) error {
	if _, err := os.Lstat(a.Src); err != nil {
		return fmt.Errorf("source vanished: %s", a.Src)
	}
	if a.Kind == "DELETE" {
		q := quarantineDst(runID(), a.Src)
		if err := os.MkdirAll(filepath.Dir(q), 0o755); err != nil {
			return err
		}
		if err := os.Rename(a.Src, q); err != nil {
			return err
		}
		record("DELETE", a.Src, q)
		logf("INFO", "DELETE(approved) %s -> %s", a.Src, q)
		fmt.Printf("  ✓ quarantined: %s\n", filepath.Base(a.Src))
		return nil
	}

	dst := a.Dst
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Collision-safe: never overwrite; suffix instead.
	if _, err := os.Lstat(dst); err == nil {
		ext := filepath.Ext(dst)
		stem := strings.TrimSuffix(dst, ext)
		for i := 1; ; i++ {
			cand := fmt.Sprintf("%s.pmax%d%s", stem, i, ext)
			if _, err := os.Lstat(cand); err != nil {
				dst = cand
				break
			}
		}
		fmt.Printf("  ! name collision — landing as %s\n", filepath.Base(dst))
	}
	if err := os.Rename(a.Src, dst); err != nil {
		// Cross-device fallback (shouldn't happen within one volume).
		if copyThenRemove(a.Src, dst) != nil {
			return err
		}
	}
	record("MOVE", a.Src, dst)
	logf("INFO", "MOVE(approved) %s -> %s", a.Src, dst)
	fmt.Printf("  ✓ moved: %s → %s\n", filepath.Base(a.Src), dst)
	return nil
}

func copyThenRemove(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}

// approvePlan runs the interactive per-item approval loop over a plan.
func approvePlan(p *Plan) int {
	total := p.Count()
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf(" Reviewing %s  —  %d action(s)\n", filepath.Base(p.Path), total)
	fmt.Println(" Every action needs your explicit yes. No bulk approve.")
	fmt.Println("──────────────────────────────────────────────────────────────")

	if StageOnly {
		for i, a := range p.Actions {
			fmt.Printf("  [%d/%d] %-6s %s\n         → %s\n", i+1, total, a.Kind, a.Src, a.Dst)
		}
		fmt.Println("Stage-only mode: nothing executed. Review later with: pmax approve")
		return 0
	}

	// Non-TTY semantics: every action still needs an explicit per-item answer.
	// EOF (launchd context, closed stdin) => stop with nothing further executed.
	in := bufio.NewReader(os.Stdin)
	approved, skipped := 0, 0
	var decided []int // indexes decided this session

	for i, a := range p.Actions {
		dst := a.Dst
		if a.Kind == "DELETE" {
			dst += "  (quarantine, undo-able)"
		}
		fmt.Printf("[%d/%d] %-6s %s  (%s, %dd old)\n       → %s\n       why: %s\n",
			i+1, total, a.Kind, a.Src, fmtSize(a.Size), a.AgeDays, dst, a.Reason)

		var ans string
		if !stdinIsTTY() && os.Getenv("PMAX_ASSUME_YES") == "1" {
			// Test/bulk escape hatch: honored ONLY on non-TTY stdin.
			ans = "y"
			fmt.Println("       (auto-approved: PMAX_ASSUME_YES on non-TTY stdin)")
		} else {
			tty := stdinIsTTY()
			keys := "[y]es / [n]o / [d]etails / [q]uit"
			if tty && Cfg.AllowBatchApprove {
				keys = "[y]es / [n]o / [a]pprove rest / [d]etails / [q]uit"
			}
			if tty {
				fmt.Printf("       Approve? %s ", keys)
			}
			line, err := in.ReadString('\n')
			if err != nil && line == "" { // EOF: fail-safe stop
				fmt.Println("\nStopped (end of input). Remaining actions stay staged.")
				break
			}
			ans = strings.ToLower(strings.TrimSpace(line))
			if !tty { // piped answers: y executes, q stops, anything else skips
				if ans == "q" || ans == "quit" {
					fmt.Println("Stopped. Remaining actions stay staged.")
					break
				}
				if ans != "y" && ans != "yes" {
					ans = "n"
				}
			} else {
				switch ans {
				case "", "q", "quit":
					fmt.Println("Stopped. Remaining actions stay staged (pmax pending / pmax approve).")
				case "d", "details":
					if st, err := os.Stat(a.Src); err == nil {
						fmt.Printf("       size %s | modified %s | dir %s\n",
							fmtSize(st.Size()), st.ModTime().Format("2006-01-02 15:04"), filepath.Dir(a.Src))
					}
					fmt.Printf("       Approve? [y]es / [n]o / [q]uit ")
					line, err := in.ReadString('\n')
					if err != nil && line == "" {
						ans = "q"
						fmt.Println()
					} else {
						ans = strings.ToLower(strings.TrimSpace(line))
						if ans != "y" && ans != "yes" {
							ans = "n"
						}
					}
				case "a", "all", "approve":
					if Cfg.AllowBatchApprove {
						ans = "y"
					} else {
						ans = "n"
						fmt.Println("       (batch approve disabled in config — item skipped)")
					}
				}
			}
		}

		wasQuit := ans == "" || ans == "q" || ans == "quit"
		if wasQuit {
			break
		}

		if ans == "y" || ans == "yes" {
			if err := execAction(a); err != nil {
				fmt.Printf("  ! %v\n", err)
			} else {
				approved++
				signal("approved", a.Kind, a.Reason, a.Src, a.Dst)
			}
			decided = append(decided, i)
		} else {
			skipped++
			signal("rejected", a.Kind, a.Reason, a.Src, a.Dst)
			decided = append(decided, i)
		}
	}

	remaining := total - approved - skipped
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf(" approved: %d   skipped: %d   still staged: %d\n", approved, skipped, remaining)
	if approved > 0 {
		fmt.Println(" undo last run anytime with: pmax undo")
	}
	if remaining == 0 {
		os.Remove(p.Path)
	} else {
		rewritePlanWithout(p, decided)
	}
	fmt.Println("──────────────────────────────────────────────────────────────")
	return 0
}

// rewritePlanWithout drops the decided rows; the rest stays staged.
func rewritePlanWithout(p *Plan, dropIdx []int) {
	drop := map[int]bool{}
	for _, i := range dropIdx {
		drop[i] = true
	}
	var keep []Action
	for i, a := range p.Actions {
		if !drop[i] {
			keep = append(keep, a)
		}
	}
	p.Actions = keep
	p.Save()
}

// ── commands ────────────────────────────────────────────────────────────────

func cmdPending() int {
	plans := loadPlans("plan-*")
	if len(plans) == 0 {
		fmt.Println("No actions staged.")
		return 0
	}
	for _, p := range plans {
		fmt.Println(p.Summary())
	}
	return 0
}

func cmdApprove(args []string) int {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	for _, p := range loadPlans("plan-*") {
		if target == "" || strings.Contains(filepath.Base(p.Path), target) {
			return approvePlan(p)
		}
	}
	fmt.Println("No staged plan matches. See: pmax pending")
	return 0
}

// cmdReview: approve only FLAG=review rows, then strip decided rows from sources.
func cmdReview() int {
	review := newPlan("review")
	review.Actions = nil
	allSrcs := map[string]bool{}
	for _, p := range loadPlans("plan-*") {
		for _, a := range p.Actions {
			if a.Flag == "review" {
				review.Actions = append(review.Actions, a)
				allSrcs[a.Src] = true
			}
		}
	}
	if review.Count() == 0 {
		fmt.Println("No flagged items to review.")
		return 0
	}
	// Optional laya junk-probability ordering (display only — never a verdict).
	orderFlaggedByJunkProb(review)
	review.Save()
	approvePlan(review)

	// Rows still present in the review plan were never reached (quit): keep them
	// in their source plans. Everything else got a y/n decision: strip.
	undecided := map[string]bool{}
	if p := loadPlan(review.Path); p != nil {
		for _, a := range p.Actions {
			undecided[a.Src] = true
		}
	}
	for _, p := range loadPlans("plan-*") {
		var keep []Action
		for _, a := range p.Actions {
			if a.Flag == "review" && allSrcs[a.Src] && !undecided[a.Src] {
				continue // decided here
			}
			keep = append(keep, a)
		}
		if len(keep) != len(p.Actions) {
			p.Actions = keep
			p.Save()
		}
	}
	os.Remove(review.Path)
	return 0
}

// cmdUndo reverses an approved run from its manifest (last action first).
func cmdUndo(args []string) int {
	run := ""
	if len(args) > 0 {
		run = args[0]
		if run == "--run" && len(args) > 1 {
			run = args[1]
		}
	}
	var m string
	if run != "" {
		m = filepath.Join(CfgState, "manifests", run+".tsv")
		if _, err := os.Stat(m); err != nil {
			die("no manifest for run %s", run)
		}
	} else {
		m = latestManifest()
		if m == "" {
			fmt.Println("Nothing to undo.")
			return 0
		}
	}

	data, err := os.ReadFile(m)
	if err != nil {
		die("cannot read manifest: %v", err)
	}
	lines := splitLines(string(data))
	undone := 0
	for i := len(lines) - 1; i >= 0; i-- {
		f := strings.Split(lines[i], "\t")
		if len(f) < 3 {
			continue
		}
		kind, src, dst := f[0], f[1], f[2]
		if _, err := os.Lstat(dst); err != nil {
			fmt.Printf("  ! undo source missing (already moved or purged?): %s\n", dst)
			continue
		}
		os.MkdirAll(filepath.Dir(src), 0o755)
		if err := os.Rename(dst, src); err != nil {
			fmt.Printf("  ! could not restore %s\n", dst)
			continue
		}
		undone++
		signal("undone", kind, "manual", dst, src)
		logf("INFO", "undo %s %s -> %s", kind, dst, src)
	}
	fmt.Printf("Undone: %d action(s) from %s\n", undone, filepath.Base(m))
	os.Rename(m, filepath.Join(CfgState, "manifests",
		"undone-"+time.Now().Format("20060102-150405")+"-"+filepath.Base(m)))
	return 0
}
