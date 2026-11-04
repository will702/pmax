package main

// Busy-aware behavior. SecondBrain writes $STATE/load.json whenever you open
// the app (level = how booked the next 24h are). Missing, stale (>12h) or
// invalid means "unknown" and pmax behaves exactly as before.
//
// `pmax tidy` is the heavy-day escape hatch: the 5 biggest safe wins, each
// still approved one at a time through the normal approver.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const loadMaxAge = 12 * time.Hour

func loadPath() string { return filepath.Join(CfgState, "load.json") }

// busyLevel returns "light" | "normal" | "heavy", or "" when unknown.
func busyLevel() string {
	data, err := os.ReadFile(loadPath())
	if err != nil {
		return ""
	}
	var h struct {
		Level       string `json:"level"`
		GeneratedAt string `json:"generated_at"`
	}
	if json.Unmarshal(data, &h) != nil {
		return ""
	}
	at, err := time.Parse(time.RFC3339, h.GeneratedAt)
	if err != nil || time.Since(at) > loadMaxAge || at.After(time.Now().Add(5*time.Minute)) {
		return ""
	}
	switch h.Level {
	case "light", "normal", "heavy":
		return h.Level
	}
	return ""
}

func isHeavyDay() bool { return busyLevel() == "heavy" }

// tidyCandidates: up to n unambiguous (flag ok) staged rows, biggest first,
// one per source file (organize and sweep may both stage the same file).
func tidyCandidates(n int) []Action {
	var all []Action
	seen := map[string]bool{}
	for _, p := range loadPlans("plan-*") {
		for _, a := range p.Actions {
			if a.Flag == "review" || seen[a.Src] {
				continue
			}
			if _, err := os.Lstat(a.Src); err != nil {
				continue
			}
			seen[a.Src] = true
			all = append(all, a)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Size > all[j].Size })
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// cmdTidy: a 2-minute declutter. Same per-item approver as everything else.
func cmdTidy(args []string) int {
	picks := tidyCandidates(5)
	if len(picks) == 0 {
		fmt.Println("Nothing staged to tidy. ✓")
		return 0
	}
	if old, _ := filepath.Glob(filepath.Join(CfgState, "staged", "tidy-*.tsv")); len(old) > 0 {
		for _, f := range old { // leftovers from a killed session
			os.Remove(f)
		}
	}
	tidy := newPlan("tidy") // not "plan-*": stays invisible to pending/status
	tidy.Actions = picks
	tidy.Save()
	approvePlan(tidy)

	// Rows still in the tidy plan were never reached: leave them staged.
	// Everything else got a decision: strip it from the real plans.
	// Match the exact row (kind+src+dst): rejecting one proposal for a file must not
	// silently drop a different proposal (e.g. a MOVE) for the same file.
	key := func(a Action) string { return a.Kind + "\t" + a.Src + "\t" + a.Dst }
	undecided := map[string]bool{}
	if p := loadPlan(tidy.Path); p != nil {
		for _, a := range p.Actions {
			undecided[key(a)] = true
		}
	}
	picked := map[string]bool{}
	for _, a := range picks {
		picked[key(a)] = true
	}
	for _, p := range loadPlans("plan-*") {
		var keep []Action
		for _, a := range p.Actions {
			if picked[key(a)] && !undecided[key(a)] {
				continue
			}
			keep = append(keep, a)
		}
		if len(keep) != len(p.Actions) {
			p.Actions = keep
			p.Save()
		}
	}
	os.Remove(tidy.Path)
	return 0
}
