package main

// remind: briefing notifications (called by launchd agents and by hand).
// watcher: the 03:00 agent — stage-only scan + quarantine purge + notify.
// It cannot move or delete anything by design: PMAX_STAGE_ONLY blocks the
// approve path, and the non-TTY guard refuses execution anyway.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func nowStamp() string { return time.Now().Format("2006-01-02 15:04") }

func cmdRemind(args []string) int {
	kind := "morning"
	if len(args) > 0 {
		kind = args[0]
	}
	s := gatherStats()
	switch kind {
	case "test":
		notify("PMAX — test", "Notifications work. Your Mac is about to get organized.")
	case "morning":
		body := fmt.Sprintf("Desktop: %d screenshots waiting. Downloads: %d stale, %d dupes. %d actions staged for approval.",
			s.DeskShots, s.DLStale, s.DLDupName, s.PendingActions)
		notify("PMAX — Morning Briefing", body)
		fmt.Println(body)
	case "evening":
		today := 0
		for _, z := range []string{UserHome + "/Desktop", UserHome + "/Downloads"} {
			for _, f := range looseFiles(z) {
				if st, err := os.Stat(f); err == nil && st.ModTime().After(time.Now().AddDate(0, 0, -1)) {
					today++
				}
			}
		}
		body := fmt.Sprintf("%d new file(s) on Desktop/Downloads today. 2-min tidy: pmax sweep", today)
		notify("PMAX — Evening Tidy", body)
		fmt.Println(body)
	case "weekly":
		body := fmt.Sprintf("Clutter score %d/10 · %d stray dirs at ~ · %d actions staged. Deep clean: pmax report --big && pmax improve",
			s.clutterScore(), s.HomeStrayDirs, s.PendingActions)
		notify("PMAX — Weekly Deep Clean", body)
		fmt.Println(body)
	default:
		die("usage: pmax remind [morning|evening|weekly|test]")
	}
	return 0
}

// cmdWatcher: 03:00 entry point. Stage-only: scans, purges expired quarantine,
// notifies only when something awaits approval. Never executes a move/delete.
func cmdWatcher(args []string) int {
	purged := purgeQuarantine()

	// Stage organize + sweep proposals without any approval loop.
	org := newPlan("plan")
	stageZoneOrganize(org, UserHome+"/Desktop", "desktop")
	stageZoneOrganize(org, UserHome+"/Downloads", "downloads")
	if org.Count() > 0 {
		org.Save()
	}
	swp := newPlan("plan")
	stageZoneSweep(swp, UserHome+"/Desktop", "desktop")
	stageZoneSweep(swp, UserHome+"/Downloads", "downloads")
	if swp.Count() > 0 {
		swp.Save()
	}

	total := org.Count() + swp.Count()
	logf("INFO", "watcher staged %d proposal(s), purged %d quarantined file(s)", total, purged)
	if total > 0 {
		notify("PMAX — items await your approval",
			fmt.Sprintf("%d proposal(s) staged overnight (nothing was moved). Review: pmax approve", total))
	}
	return 0
}

// pmaxConfigPath is a small helper for install documentation.
func pmaxConfigPath() string { return filepath.Join(CfgHome, "pmax.conf") }
