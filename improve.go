package main

// improve: the recursive self-improvement loop.
//
//   Sense    — every approved/rejected/undone action lands in signals.tsv
//   Propose  — Qwen (MLX sidecar) drafts candidate glob→destination rules as
//              STRICT JSON; malformed output is discarded, never executed
//   Gate     — laya noul double-checks each proposal is classification-only
//   Decide   — YOU accept/reject each candidate; accepted lines go to
//              rules.learned (max N, FIFO eviction, prefix-guarded)
//   Act      — learned rules shape future *proposals* (still approval-gated)
//              whose outcomes feed signals → the loop recurses

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type proposal struct {
	Pattern     string  `json:"pattern"`
	Destination string  `json:"destination"`
	Reason      string  `json:"reason"`
	Confidence  float64 `json:"confidence"`
}

type proposalsDoc struct {
	Proposals []proposal `json:"proposals"`
}

func learnedPath() string { return filepath.Join(CfgHome, "rules.learned") }

func readLearned() []string {
	data, err := os.ReadFile(learnedPath())
	if err != nil {
		return nil
	}
	return splitLines(string(data))
}

func countLearnedRules() int {
	n := 0
	for _, l := range readLearned() {
		if !strings.HasPrefix(l, "#") && strings.Contains(l, "\t") {
			n++
		}
	}
	return n
}

// destinationAllowed: learned rules may only classify into safe prefixes.
func destinationAllowed(dest string) bool {
	for _, p := range Cfg.LearnedAllowedPrefixes {
		if strings.HasPrefix(dest, p) {
			return true
		}
	}
	return false
}

// sense: summarize recent signals into a compact human/model-readable digest.
func senseDigest() string {
	data, err := os.ReadFile(filepath.Join(CfgState, "learning", "signals.tsv"))
	if err != nil {
		return "(no signals yet — approve/reject a few actions first)"
	}
	lines := splitLines(string(data))
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	counts := map[string]int{}
	ruleEvents := map[string]int{}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) < 4 {
			continue
		}
		counts[f[1]]++ // approved | rejected | undone
		if f[3] != "" {
			ruleEvents[f[1]+":"+f[3]]++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "recent decisions: %d approved, %d rejected, %d undone\n",
		counts["approved"], counts["rejected"], counts["undone"])
	keys := make([]string, 0, len(ruleEvents))
	for k := range ruleEvents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i >= 20 {
			b.WriteString("…\n")
			break
		}
		fmt.Fprintf(&b, "  %s × %d\n", k, ruleEvents[k])
	}
	return b.String()
}

// sampleFilenames: a taste of current clutter for grounded proposals.
func sampleFilenames() string {
	var names []string
	for _, z := range []string{UserHome + "/Desktop", UserHome + "/Downloads"} {
		for _, f := range looseFiles(z) {
			names = append(names, filepath.Base(f))
			if len(names) >= 60 {
				break
			}
		}
	}
	return strings.Join(names, "\n")
}

func cmdImprove(args []string) int {
	for _, a := range args {
		if a == "--rollback" {
			return improveRollback()
		}
	}

	accepted, offered := 0, 0
	batchID := time.Now().Format("20060102-150405")
	signalsBefore := countSignals()

	// ── Phase 1: dream-tuned survivors from `pmax autotune` (no model needed;
	// they already carry replay evidence from your decision history).
	if tuned := loadTunedProposals(); len(tuned) > 0 {
		fmt.Printf("╭─ %d dream-tuned candidate(s) (autotune survivors, replay-scored) ─╮\n", len(tuned))
		var remaining []scoredProposal
		for i, p := range tuned {
			fmt.Printf("[%d/%d] %s  →  %s\n       %s  [replay %+.2f]\n",
				i+1, len(tuned), p.Pattern, p.Destination, p.Reason, p.ReplayDelta)
			offered++
			if offerProposal(proposal{Pattern: p.Pattern, Destination: p.Destination,
				Reason: p.Reason, Confidence: p.Confidence}, batchID, "tuned") {
				accepted++
			} else {
				remaining = append(remaining, p) // not decided (EOF/quit) — keep for later
			}
		}
		saveTunedProposals(remaining)
		fmt.Println("╰───────────────────────────────────────────────────────────╯")
	}

	// ── Phase 2: fresh model proposals (MLX sidecar), grounded in signals +
	// accumulated lessons (harness2-style cross-session memory).
	if aiUp() {
		var rules []string
		for _, l := range readLearned() {
			if !strings.HasPrefix(l, "#") {
				rules = append(rules, l)
			}
		}
		sys := "You are the rule-improvement engine of a file-organization tool. " +
			"You reply with STRICT JSON only, no prose, no markdown fences. " +
			"Schema: {\"proposals\":[{\"pattern\":\"glob on filename\",\"destination\":\"absolute directory under ~/Documents or ~/Pictures\",\"reason\":\"short evidence-based reason\",\"confidence\":0.0-1.0}]} " +
			"Rules must be CLASSIFICATION ONLY: propose where files should live; NEVER propose deleting, quarantining, or moving anything outside the allowed destination roots. " +
			"Respect the LESSONS: never re-propose what the user has repeatedly rejected. " +
			"Propose at most 5 rules. If the evidence does not support any new rule, return {\"proposals\":[]}."
		usr := fmt.Sprintf(
			"CURRENT LEARNED RULES (glob<TAB>destination):\n%s\n\n"+
				"RECENT DECISION SIGNALS:\n%s\n\n"+
				"LESSONS FROM PAST SESSIONS:\n%s\n\n"+
				"REPRESENTATIVE FILENAMES:\n%s\n\n"+
				"Propose new glob→destination rules that would better match the user's actual accept/reject behavior.",
			strings.Join(rules, "\n"), senseDigest(), loadLessons(), sampleFilenames())

		raw, err := chat([]chatMessage{{Role: "system", Content: sys}, {Role: "user", Content: usr}}, 700)
		if err != nil {
			fmt.Println("chat failed:", err)
		} else if doc, err := parseProposals(raw); err != nil {
			fmt.Println("Model output failed JSON validation — discarded (nothing applied).")
			logf("INFO", "improve: invalid model output: %.200s", raw)
		} else if len(doc.Proposals) == 0 {
			fmt.Println("No new model rules proposed — insufficient evidence. ✓")
		} else {
			// Dream-rank before showing: replay evidence beats vibes.
			history := replayHistory()
			base := currentRules()
			baseScore := replayScore(base, history)
			type sp struct {
				p proposal
				d float64
			}
			var ranked []sp
			for _, p := range doc.Proposals {
				trial := append(append([]rule{}, base...), rule{Pattern: p.Pattern, Destination: p.Destination})
				ranked = append(ranked, sp{p, replayScore(trial, history) - baseScore})
			}
			for i := 1; i < len(ranked); i++ {
				for j := i; j > 0 && ranked[j].d > ranked[j-1].d; j-- {
					ranked[j-1], ranked[j] = ranked[j], ranked[j-1]
				}
			}
			fmt.Printf("╭─ %d model candidate(s), dream-ranked ─╮\n", len(ranked))
			for i, r := range ranked {
				fmt.Printf("[%d/%d] %s  →  %s\n       %s (model %.0f%%, replay %+.2f)\n",
					i+1, len(ranked), r.p.Pattern, r.p.Destination, r.p.Reason, r.p.Confidence*100, r.d)
				offered++
				if offerProposal(r.p, batchID, "model") {
					accepted++
				}
			}
			fmt.Println("╰────────────────────────────────────────╯")
		}
	} else if len(loadTunedProposals()) == 0 {
		fmt.Println("MLX sidecar down and no tuned proposals queued.")
		fmt.Println("Try: pmax ai ensure   (or run pmax autotune first — it needs no model)")
	}

	deriveLessons(signalsBefore)
	fmt.Printf("accepted %d/%d offered rule(s). Rules file: %s\n", accepted, offered, learnedPath())
	if accepted > 0 {
		fmt.Println("Undo a batch later: pmax improve --rollback")
	}
	return 0
}

// offerProposal runs the shared y/n gate for one candidate rule.
// Returns true only if explicitly accepted. EOF/quit returns false without deciding.
func offerProposal(p proposal, batchID, source string) bool {
	// Structural guardrails — independent of any model.
	if p.Pattern == "" || !destinationAllowed(p.Destination) {
		fmt.Println("       ✗ rejected by structural guard (empty pattern or bad destination)")
		signal("rejected", "rule", p.Pattern, p.Pattern, p.Destination)
		return false
	}
	if _, err := os.Stat(p.Destination); err != nil {
		fmt.Printf("       note: destination does not exist yet (created on first use)\n")
	}

	// laya second gate (when available): classification-only sanity. Display
	// signal only — never a verdict.
	if layaReady() {
		res, err := layaRun("noul",
			"Is this rule proposal purely about classifying files into folders, with no deletion or risky operation, and reasonably grounded in the given reason?",
			"", []layaItem{{ID: p.Pattern, Text: p.Pattern + " -> " + p.Destination + "\nReason: " + p.Reason}})
		if err == nil && len(res) > 0 {
			fmt.Printf("       laya safety-gate: P(safe)=%.0f%%", res[0].PTrue*100)
			if res[0].Abstained || res[0].PTrue < 0.6 {
				fmt.Print("  [low — look closely]")
			}
			fmt.Println()
		}
	}

	if StageOnly {
		fmt.Println("       (stage-only: recorded as a proposal, not applied)")
		return false
	}
	fmt.Printf("       Accept into rules.learned? [y]es / [n]o ")
	var ans string
	if n, _ := fmt.Scanln(&ans); n == 0 {
		return false // EOF/empty — not decided
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	if ans == "y" || ans == "yes" {
		if err := appendLearned(p, batchID); err != nil {
			fmt.Println("       !", err)
			return false
		}
		signal("learned", "rule", p.Pattern, p.Pattern, p.Destination)
		fmt.Println("       ✓ learned (shapes future proposals; file moves stay y/n-gated)")
		return true
	}
	signal("rejected", "rule", p.Pattern, p.Pattern, p.Destination)
	return false
}

func saveTunedProposals(props []scoredProposal) {
	path := filepath.Join(CfgState, "learning", "proposals.json")
	if len(props) == 0 {
		os.Remove(path)
		return
	}
	data, _ := json.MarshalIndent(props, "", "  ")
	os.WriteFile(path, data, 0o644)
}

// ── lessons (harness2-style persistent cross-session memory) ────────────────

func lessonsPath() string { return filepath.Join(CfgState, "learning", "lessons.md") }

func loadLessons() string {
	data, err := os.ReadFile(lessonsPath())
	if err != nil {
		return "(none yet — they accumulate as you accept/reject)"
	}
	return string(data)
}

func countSignals() int {
	data, err := os.ReadFile(filepath.Join(CfgState, "learning", "signals.tsv"))
	if err != nil {
		return 0
	}
	return len(splitLines(string(data)))
}

// deriveLessons converts this session's fresh signals into short lessons:
// rejection clusters become "avoid" lines, approval clusters become "prefer".
func deriveLessons(before int) {
	data, err := os.ReadFile(filepath.Join(CfgState, "learning", "signals.tsv"))
	if err != nil {
		return
	}
	lines := splitLines(string(data))
	if len(lines) <= before {
		return
	}
	fresh := lines[before:]
	counts := map[string][2]int{} // reason -> [approved, rejected]
	for _, l := range fresh {
		f := strings.Split(l, "\t")
		if len(f) < 4 || f[2] != "rule" {
			continue
		}
		var idx int
		if f[1] == "rejected" {
			idx = 1
		} else if f[1] == "learned" {
			idx = 0
		} else {
			continue
		}
		v := counts[f[3]]
		v[idx]++
		counts[f[3]] = v
	}
	var out []string
	for reason, c := range counts {
		switch {
		case c[1] >= 2:
			out = append(out, fmt.Sprintf("- AVOID: user rejected %d× rule/pattern '%s'", c[1], reason))
		case c[0] >= 2:
			out = append(out, fmt.Sprintf("- PREFER: user accepted %d× rule/pattern '%s'", c[0], reason))
		}
	}
	if len(out) == 0 {
		return
	}
	f, err := os.OpenFile(lessonsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n# session %s\n%s\n", time.Now().Format("2006-01-02 15:04"), strings.Join(out, "\n"))
	fmt.Printf("lessons updated: %d new line(s) → %s\n", len(out), lessonsPath())
}

// parseProposals enforces STRICT JSON (optionally fenced) — defense against
// the model emitting prose or anything executable.
func parseProposals(raw string) (*proposalsDoc, error) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "json"))
	}
	var doc proposalsDoc
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		return nil, err
	}
	// hard filters
	var clean []proposal
	for _, p := range doc.Proposals {
		if p.Pattern == "" || p.Destination == "" {
			continue
		}
		if !destinationAllowed(p.Destination) {
			continue
		}
		clean = append(clean, p)
	}
	doc.Proposals = clean
	return &doc, nil
}

// appendLearned writes one rule, evicting oldest beyond LEARNED_MAX_RULES.
func appendLearned(p proposal, batchID string) error {
	f, err := os.OpenFile(learnedPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "# batch %s\n%s\t%s\n", batchID, p.Pattern, p.Destination)

	if countLearnedRules() > Cfg.LearnedMaxRules {
		evictOldestRules(Cfg.LearnedMaxRules)
	}
	return nil
}

// evictOldestRules keeps only the newest N rules (FIFO).
func evictOldestRules(keep int) {
	lines := readLearned()
	var rules []int
	for i, l := range lines {
		if !strings.HasPrefix(l, "#") && strings.Contains(l, "\t") {
			rules = append(rules, i)
		}
	}
	excess := len(rules) - keep
	if excess <= 0 {
		return
	}
	drop := map[int]bool{}
	for _, i := range rules[:excess] {
		drop[i] = true
	}
	var out []string
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	os.WriteFile(learnedPath(), []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// improveRollback removes the most recent accepted batch.
func improveRollback() int {
	lines := readLearned()
	last := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "# batch ") {
			last = i
		}
	}
	if last < 0 {
		fmt.Println("No learned-rule batches to roll back.")
		return 0
	}
	removed := lines[last:]
	os.WriteFile(learnedPath(), []byte(strings.Join(lines[:last], "\n")+"\n"), 0o644)
	fmt.Printf("Rolled back last batch (%d line(s)):\n", len(removed))
	for _, l := range removed {
		fmt.Println("  ", l)
	}
	signal("learned_rollback", "rule", "batch", "", "")
	return 0
}
