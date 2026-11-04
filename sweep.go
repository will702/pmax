package main

// sweep: stage junk proposals — .DS_Store, stalled partials, stale downloads,
// name-pattern duplicates. Quarantine-backed, approval-gated.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dupePattern = regexp.MustCompile(`^(.*) (copy [0-9]+|copy|\([0-9]+\))(\.[^.]*)?$`)

// dedupeBase returns the base name if `name` is a "file (1).ext"-style dupe.
func dedupeBase(name string) (string, bool) {
	if m := dupePattern.FindStringSubmatch(name); m != nil {
		base := m[1] + m[3]
		if base != name {
			return base, true
		}
	}
	return "", false
}

func stageZoneSweep(p *Plan, zone, label string) {
	// 1) .DS_Store clutter (depth 2)
	walkDepth(zone, 2, func(path string, d os.DirEntry) {
		if !d.IsDir() && d.Name() == ".DS_Store" {
			p.Stage(Action{Kind: "DELETE", Src: path, Reason: "junk/.DS_Store"})
		}
	})

	for _, f := range looseFiles(zone) {
		if isProtected(f) {
			continue
		}
		name := filepath.Base(f)
		switch {
		// 2) Stalled browser partial downloads
		case strings.HasSuffix(name, ".crdownload") ||
			strings.HasSuffix(name, ".part") ||
			strings.HasSuffix(name, ".download"):
			p.Stage(Action{Kind: "DELETE", Src: f, Reason: "junk/stalled-download"})

		// 2b) Installers you already ran (Downloads only)
		case label == "downloads" && isInstaller(name) && daysOld(f) > Cfg.InstallerDays:
			// .dmg can be a vault or backup image: flag for review, never a quick win.
			p.Stage(Action{Kind: "DELETE", Src: f, Flag: "review",
				Reason: fmt.Sprintf("installer/>%dd", Cfg.InstallerDays)})

		// 3) Stale loose files (Downloads only)
		case label == "downloads" && daysOld(f) > Cfg.StaleDays:
			p.Stage(Action{Kind: "DELETE", Src: f,
				Reason: fmt.Sprintf("stale/>%dd", Cfg.StaleDays)})

		// 4) Name-pattern duplicates ("file (1).pdf" next to "file.pdf")
		default:
			if base, ok := dedupeBase(name); ok {
				flag := "ok"
				if _, err := os.Stat(filepath.Join(zone, base)); err != nil {
					flag = "review" // original missing — ambiguous
				}
				p.Stage(Action{Kind: "DELETE", Src: f,
					Reason: "name-dupe of '" + base + "'", Flag: flag})
			}
		}
	}
}

func isInstaller(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".dmg" || ext == ".pkg"
}

// walkDepth visits files up to maxDepth levels under root.
func walkDepth(root string, maxDepth int, fn func(path string, d os.DirEntry)) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fn(path, d)
		return nil
	})
}

func cmdSweep(args []string) int {
	zone := "all"
	if len(args) > 0 {
		zone = args[0]
	}
	plan := newPlan("plan")
	switch zone {
	case "desktop":
		stageZoneSweep(plan, UserHome+"/Desktop", "desktop")
	case "downloads":
		stageZoneSweep(plan, UserHome+"/Downloads", "downloads")
	default:
		stageZoneSweep(plan, UserHome+"/Desktop", "desktop")
		stageZoneSweep(plan, UserHome+"/Downloads", "downloads")
	}
	if plan.Count() == 0 {
		fmt.Println("No junk found — nothing to sweep. ✓")
		return 0
	}
	plan.Save()
	logf("INFO", "sweep staged %d action(s) [%s]", plan.Count(), zone)
	fmt.Printf("Staged %d junk proposal(s) for %s (quarantine, never hard delete).\n", plan.Count(), zone)
	return approvePlan(plan)
}
