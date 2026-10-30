package main

// projects: classify stray dirs at $HOME and consolidate into ~/projects/
// one folder at a time, per-item approved. Warns about things that commonly
// break when a path changes.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var homeKeepDirs = map[string]bool{
	"Desktop": true, "Documents": true, "Downloads": true, "Pictures": true,
	"Music": true, "Movies": true, "Library": true, "Applications": true,
	"Public": true, "projects": true, "Alif": true, "TahuAI": true, "Certificates": true,
}

func homeStrays() []string {
	entries, err := os.ReadDir(UserHome)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || homeKeepDirs[name] {
			continue
		}
		if isProtected(filepath.Join(UserHome, name)) {
			continue
		}
		dirs = append(dirs, filepath.Join(UserHome, name))
	}
	sort.Strings(dirs)
	return dirs
}

func classifyDir(dir string) string {
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil && st.IsDir() {
		return "GIT"
	}
	for _, marker := range []string{"package.json", "pyproject.toml", "Cargo.toml",
		"go.mod", "pubspec.yaml", "Gemfile", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return "CODE"
		}
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		return "EMPTY"
	}
	// Deep-empty check (a few files max).
	n := 0
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
			if n > 3 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if n <= 3 {
		return "SPARSE"
	}
	return "WORK"
}

func dirSize(dir string) string {
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return fmtSize(total)
}

func cmdProjects(args []string) int {
	sub := "report"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "report", "":
		fmt.Printf("  %-28s %-8s %9s  %6s\n", "DIR", "CLASS", "SIZE", "AGE(d)")
		fmt.Println("  ─────────────────────────────────────────────────────")
		total := 0
		for _, d := range homeStrays() {
			total++
			fmt.Printf("  %-28s %-8s %9s  %6d\n",
				filepath.Base(d), classifyDir(d), dirSize(d), daysOld(d))
		}
		fmt.Println("  ─────────────────────────────────────────────────────")
		fmt.Printf("  %d stray dir(s) at ~. Consolidate: pmax projects move <name>\n", total)
		fmt.Println("  Loose files at ~:  pmax projects files")
		return 0

	case "move":
		if len(args) < 1 {
			die("usage: pmax projects move <dirname>")
		}
		name := args[0]
		src := filepath.Join(UserHome, name)
		if st, err := os.Stat(src); err != nil || !st.IsDir() {
			die("no such dir at home root: %s", src)
		}
		dst := filepath.Join(Cfg.DestProjects, name)
		if _, err := os.Lstat(dst); err == nil {
			die("destination already exists: %s (resolve manually)", dst)
		}

		// Pre-move warnings, surfaced inside the approval prompt.
		var warns []string
		if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
			dirty := exec.Command("git", "-C", src, "status", "--porcelain")
			if out, err := dirty.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
				warns = append(warns, "git has uncommitted changes")
			}
		}
		for _, envf := range []string{".env", ".env.local"} {
			if data, err := os.ReadFile(filepath.Join(src, envf)); err == nil && strings.Contains(string(data), src) {
				warns = append(warns, ".env references its own absolute path")
				break
			}
		}
		if _, err := os.Stat(filepath.Join(src, "node_modules")); err == nil {
			warns = append(warns, "node_modules ("+dirSize(filepath.Join(src, "node_modules"))+") moves with it")
		}
		if out, err := exec.Command("pgrep", "-fl", src).Output(); err == nil && len(out) > 0 {
			warns = append(warns, "a process may be running from this dir")
		}

		reason := "projects consolidation"
		if len(warns) > 0 {
			reason += " — WARN: " + strings.Join(warns, "; ")
		}
		plan := newPlan("plan")
		plan.Stage(Action{Kind: "MOVE", Src: src, Dst: dst, Reason: reason})
		plan.Save()
		fmt.Printf("Staged: %s → %s\n", filepath.Base(src), dst)
		return approvePlan(plan)

	case "files":
		plan := newPlan("plan")
		entries, _ := os.ReadDir(UserHome)
		for _, e := range entries {
			if e.IsDir() || e.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			f := filepath.Join(UserHome, e.Name())
			if isProtected(f) {
				continue
			}
			plan.Stage(Action{Kind: "MOVE", Src: f,
				Dst: filepath.Join(Cfg.DestScatter, e.Name()), Reason: "home-root scatter file"})
		}
		if plan.Count() == 0 {
			fmt.Println("No loose files at ~. ✓")
			return 0
		}
		plan.Save()
		fmt.Printf("Staged %d loose home-root file(s) → %s\n", plan.Count(), Cfg.DestScatter)
		return approvePlan(plan)

	default:
		die("usage: pmax projects [report|move <name>|files]")
		return 1
	}
}
