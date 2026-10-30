package main

// report: clutter dashboard + doctor. Read-only.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type zoneStats struct {
	DeskFiles, DeskShots      int
	DeskSize                  string
	DLFiles, DLStale          int
	DLPartial, DLDupName      int
	DLSize                    string
	HomeStrayDirs             int
	PendingActions            int
	Quarantined               int
}

func gatherStats() zoneStats {
	s := zoneStats{}
	s.DeskFiles = len(looseFiles(UserHome + "/Desktop"))
	for _, f := range looseFiles(UserHome + "/Desktop") {
		if isScreenshot(filepath.Base(f)) {
			s.DeskShots++
		}
	}
	s.DeskSize = dirSize(UserHome + "/Desktop")
	s.DLFiles = len(looseFiles(UserHome + "/Downloads"))
	s.DLSize = dirSize(UserHome + "/Downloads")
	for _, f := range looseFiles(UserHome + "/Downloads") {
		name := filepath.Base(f)
		if strings.HasSuffix(name, ".crdownload") || strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".download") {
			s.DLPartial++
		}
		if daysOld(f) > Cfg.StaleDays {
			s.DLStale++
		}
		if _, ok := dedupeBase(name); ok {
			s.DLDupName++
		}
	}
	s.HomeStrayDirs = len(homeStrays())
	for _, p := range loadPlans("plan-*") {
		s.PendingActions += p.Count()
	}
	filepath.WalkDir(filepath.Join(CfgState, "quarantine"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			s.Quarantined++
		}
		return nil
	})
	return s
}

// clutterScore: 0 (spotless) .. 10 (chaos) — transparent weights, shown in report.
func (s zoneStats) clutterScore() int {
	n := 0
	switch {
	case s.DeskShots > 20:
		n += 2
	case s.DeskShots > 0:
		n++
	}
	switch {
	case s.DeskFiles > 30:
		n += 2
	case s.DeskFiles > 5:
		n++
	}
	switch {
	case s.DLStale > 100:
		n += 2
	case s.DLStale > 10:
		n++
	}
	if s.DLPartial > 0 {
		n++
	}
	if s.DLDupName > 30 {
		n++
	}
	if s.HomeStrayDirs > 50 {
		n++
	}
	if n > 10 {
		n = 10
	}
	return n
}

func cmdReport(args []string) int {
	big := len(args) > 0 && args[0] == "--big"
	s := gatherStats()
	score := s.clutterScore()

	fmt.Println("╭──────────────────────────────────────────────╮")
	fmt.Printf("│  PMAX report — %s\n", nowStamp())
	fmt.Println("╰──────────────────────────────────────────────╯")
	verdict := "(chaos — pmax organize && pmax sweep)"
	switch {
	case score <= 1:
		verdict = "(spotless — keep it up)"
	case score <= 3:
		verdict = "(lightly lived-in)"
	case score <= 6:
		verdict = "(drifting — run a sweep)"
	}
	fmt.Printf("  Clutter score: %d/10  %s\n\n", score, verdict)
	fmt.Printf("  %-24s %6s   %8s\n", "ZONE", "ITEMS", "SIZE")
	fmt.Printf("  %-24s %6d   %8s\n", "Desktop (loose files)", s.DeskFiles, s.DeskSize)
	fmt.Printf("  %-24s %6d\n", "  └ screenshots", s.DeskShots)
	fmt.Printf("  %-24s %6d   %8s\n", "Downloads (loose)", s.DLFiles, s.DLSize)
	fmt.Printf("  %-24s %6d\n", "  └ stale >"+fmt.Sprint(Cfg.StaleDays)+"d", s.DLStale)
	fmt.Printf("  %-24s %6d\n", "  └ stalled partials", s.DLPartial)
	fmt.Printf("  %-24s %6d\n", "  └ dupe-pattern names", s.DLDupName)
	fmt.Printf("  %-24s %6d\n\n", "Home-root stray dirs", s.HomeStrayDirs)
	fmt.Printf("  Awaiting your approval: %d action(s) staged  |  quarantined: %d file(s)\n\n",
		s.PendingActions, s.Quarantined)

	if big {
		fmt.Println("  Top space consumers (Desktop / Downloads / home root):")
		type du struct{ size int64; path string }
		var all []du
		for _, root := range []string{UserHome + "/Desktop", UserHome + "/Downloads"} {
			entries, _ := os.ReadDir(root)
			for _, e := range entries {
				p := filepath.Join(root, e.Name())
				all = append(all, du{dirSizeBytes(p), p})
			}
		}
		entries, _ := os.ReadDir(UserHome)
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "Library" {
				continue
			}
			p := filepath.Join(UserHome, e.Name())
			all = append(all, du{dirSizeBytes(p), p})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].size > all[j].size })
		for i, d := range all {
			if i >= 15 {
				break
			}
			fmt.Printf("    %8s  %s\n", fmtSize(d.size), d.path)
		}
		fmt.Println()
		fmt.Println("  Tip: cloned/duplicated trees (e.g. shared data) are merged manually —")
		fmt.Println("  pmax never merges or deletes directories on its own.")
	}
	return 0
}

func dirSizeBytes(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// ── doctor ──────────────────────────────────────────────────────────────────

func cmdDoctor(args []string) int {
	ok, warn, fail := 0, 0, 0
	chk := func(status, label, detail string) {
		switch status {
		case "ok":
			fmt.Printf("  ✓ %s — %s\n", label, detail)
			ok++
		case "warn":
			fmt.Printf("  ! %s — %s\n", label, detail)
			warn++
		default:
			fmt.Printf("  ✗ %s — %s\n", label, detail)
			fail++
		}
	}
	fmt.Printf("pmax doctor — %s\n", Version)

	if path, err := exec.LookPath("pmax"); err == nil {
		chk("ok", "pmax on PATH", path)
	} else {
		chk("warn", "pmax on PATH", "run ./install.sh or use the built binary")
	}
	if _, err := os.Stat(filepath.Join(CfgHome, "pmax.conf")); err == nil {
		chk("ok", "config", filepath.Join(CfgHome, "pmax.conf"))
	} else {
		chk("warn", "config", "copy config/pmax.conf.example to "+CfgHome+"/pmax.conf")
	}
	if st, err := os.Stat(CfgState); err == nil && st.IsDir() {
		chk("ok", "state dir writable", CfgState)
	} else {
		chk("fail", "state dir writable", CfgState)
	}
	if _, err := exec.LookPath("osascript"); err == nil {
		chk("ok", "osascript (notifications)", "/usr/bin/osascript")
	} else {
		chk("fail", "osascript (notifications)", "missing")
	}

	// launchd agents
	agents := 0
	if out, err := exec.Command("launchctl", "list").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "com.pmax.") {
				agents++
			}
		}
	}
	if agents >= 4 {
		chk("ok", "launchd agents", fmt.Sprintf("%d loaded", agents))
	} else {
		chk("warn", "launchd agents", fmt.Sprintf("%d/4 loaded — run ./install.sh (reminders need them)", agents))
	}

	// quarantine hygiene (report only; the watcher purges)
	old := 0
	cutoffDays := Cfg.QuarantineRetentionDays
	filepath.WalkDir(filepath.Join(CfgState, "quarantine"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && daysOld(path) > cutoffDays {
			old++
		}
		return nil
	})
	if old > 0 {
		chk("warn", "quarantine", fmt.Sprintf("%d file(s) past %dd retention — watcher will purge", old, cutoffDays))
	} else {
		chk("ok", "quarantine", fmt.Sprintf("within retention (%d files)", gatherStats().Quarantined))
	}

	// AI sidecar
	if aiUp() {
		chk("ok", "MLX sidecar", fmt.Sprintf("http://127.0.0.1:%d/health", Cfg.AIPort))
	} else if venvPython() != "" {
		chk("warn", "MLX sidecar", "venv present, server down — pmax ai start (colgrep degraded)")
	} else {
		chk("warn", "MLX sidecar", "not set up — pmax ai setup (optional)")
	}
	if layaReady() {
		chk("ok", "laya", "typed decisions available")
	} else if Cfg.LayaEnabled == "true" {
		chk("warn", "laya", "enabled but not importable — pmax ai setup")
	} else {
		chk("ok", "laya", "not installed (optional)")
	}

	fmt.Printf("  ── %d ok, %d warn, %d fail\n", ok, warn, fail)
	if fail > 0 {
		return 1
	}
	return 0
}
