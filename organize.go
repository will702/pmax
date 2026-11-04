package main

// organize: stage sorting proposals for Desktop/Downloads into PARA homes.
// Learned rules apply first, then screenshots, then extension map.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func typeFor(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "pdf", "doc", "docx", "txt", "rtf", "md", "markdown", "pages", "epub":
		return "Documents"
	case "ppt", "pptx", "key":
		return "Presentations"
	case "xls", "xlsx", "csv", "tsv", "numbers":
		return "Spreadsheets"
	case "png", "jpg", "jpeg", "heic", "gif", "webp", "svg", "tiff", "bmp", "avif":
		return "Images"
	case "mp4", "mov", "avi", "mkv", "webm", "m4v":
		return "Videos"
	case "mp3", "wav", "aac", "flac", "m4a", "ogg":
		return "Audio"
	case "zip", "rar", "7z", "tar", "gz", "bz2", "xz", "dmg", "pkg", "ipa", "exe", "msi":
		return "Installers"
	case "fig", "sketch", "psd", "ai", "afdesign":
		return "Design"
	default:
		return "Other"
	}
}

func isScreenshot(name string) bool {
	for _, p := range []string{"Screenshot ", "Screen Shot ", "Screenshot", "Screen Shot", "Cuplikan Layar", "CleanShot "} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// learnedDest: rules.learned lines of `glob<TAB>destination-dir`.
// Destinations outside LEARNED_ALLOWED_PREFIXES are rejected here too.
func learnedDest(path string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(CfgHome, "rules.learned"))
	if err != nil {
		return "", false
	}
	base := filepath.Base(path)
	for _, line := range splitLines(string(data)) {
		if strings.HasPrefix(line, "#") {
			continue
		}
		glob, dest, ok := strings.Cut(line, "\t")
		if !ok || dest == "" {
			continue
		}
		allowed := false
		for _, p := range Cfg.LearnedAllowedPrefixes {
			if strings.HasPrefix(dest, p) {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		if globMatch(glob, base) {
			return dest, true
		}
	}
	return "", false
}

func monthDir(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return time.Now().Format("2006-01")
	}
	return st.ModTime().Format("2006-01")
}

func looseFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		// Symlinks are protected: they may point into cloud storage or system
		// locations — pmax never stages, moves, or deletes them.
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files
}

func stageZoneOrganize(p *Plan, zone, label string) {
	for _, f := range looseFiles(zone) {
		if isProtected(f) {
			continue
		}
		reason := "organize/" + label
		var dst string
		if d, ok := learnedDest(f); ok {
			dst, reason = filepath.Join(d, filepath.Base(f)), "learned-rule"
		} else if isScreenshot(filepath.Base(f)) {
			dst = filepath.Join(Cfg.DestScreenshots, monthDir(f), filepath.Base(f))
			reason = "screenshot"
		} else {
			dst = filepath.Join(Cfg.DestInbox, typeFor(filepath.Base(f)), filepath.Base(f))
		}
		p.Stage(Action{Kind: "MOVE", Src: f, Dst: dst, Reason: reason})
	}
}

func cmdOrganize(args []string) int {
	zone, smart := "all", false
	for _, a := range args {
		if a == "--smart" {
			smart = true
		} else {
			zone = a
		}
	}
	plan := newPlan("plan")
	switch zone {
	case "desktop":
		stageZoneOrganize(plan, UserHome+"/Desktop", "desktop")
	case "downloads":
		stageZoneOrganize(plan, UserHome+"/Downloads", "downloads")
	default:
		stageZoneOrganize(plan, UserHome+"/Desktop", "desktop")
		stageZoneOrganize(plan, UserHome+"/Downloads", "downloads")
	}

	// --smart: run laya typed-decisions over generic "Other" files.
	if smart {
		smartClassify(plan)
	}

	if plan.Count() == 0 {
		fmt.Println("Nothing to organize — zones are clear of loose files. ✓")
		return 0
	}
	plan.Save()
	logf("INFO", "organize staged %d action(s) [%s smart=%v]", plan.Count(), zone, smart)
	fmt.Printf("Staged %d sorting proposal(s) for %s.\n", plan.Count(), zone)
	return approvePlan(plan)
}
