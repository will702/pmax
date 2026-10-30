package main

// autotune: the autoresearch pattern applied to pmax rules — a budgeted
// keep-or-revert loop that runs entirely in the DREAM (replay) world.
//
//   for each candidate rule (dream-ranked, model drafts optional):
//     probe   = replay(working-set + candidate)
//     keep    if probe > baseline (else revert/discard)   ← keep-or-revert
//   budget   = wall-clock minutes (default 20)            ← platform-fair
//   edit     = only rules.learned is ever proposed        ← single surface
//
// Autonomy lives in EXPLORATION; adoption stays human: survivors are written
// to learning/proposals.json and surface in `pmax improve` with their replay
// evidence for your y/n. No file is ever touched.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func cmdAutotune(args []string) int {
	minutes := 20
	for i := 0; i < len(args); i++ {
		if args[i] == "--minutes" && i+1 < len(args) {
			minutes = atoi(args[i+1])
			i++
		}
	}
	history := replayHistory()
	if len(history) < 5 {
		fmt.Println("Not enough history to tune on (need ≥5 decisions).")
		fmt.Println("Run pmax organize / pmax sweep a few times first.")
		return 0
	}

	deadline := time.Now().Add(time.Duration(minutes) * time.Minute)
	working := currentRules()
	baseline := replayScore(working, history)
	startBase := baseline

	cands := deriveCandidates(history)
	if aiUp() {
		cands = append(cands, modelCandidates(history)...)
	}
	if len(cands) == 0 {
		fmt.Println("No candidates derived from history. ✓ (nothing to tune)")
		return 0
	}

	fmt.Printf("autotune — %d candidate(s), budget %d min, replay history %d decision(s)\n",
		len(cands), minutes, len(history))

	// Greedy forward selection with keep-or-revert, capped by time + rule cap.
	ruleCap := Cfg.LearnedMaxRules - len(working)
	if ruleCap < 0 {
		ruleCap = 0
	}
	var survivors []candidate
	probed := 0
	for _, c := range cands {
		if time.Now().After(deadline) {
			logExperiment("budget-stop", 0, false, "wall clock reached")
			fmt.Println("  budget reached — stopping exploration")
			break
		}
		if len(survivors) >= ruleCap {
			logExperiment("rule-cap", 0, false, "LEARNED_MAX_RULES reached")
			break
		}
		// skip patterns already covered
		dup := false
		for _, r := range working {
			if r.Pattern == c.rule.Pattern {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		probed++
		trial := append(append([]rule{}, working...), c.rule)
		score := replayScore(trial, history)
		delta := score - baseline
		if delta > 0 {
			working = trial
			baseline = score
			survivors = append(survivors, c)
			logExperiment(c.rule.Pattern, delta, true, fmt.Sprintf("→ %s", c.rule.Destination))
			fmt.Printf("  keep  %+.2f  %-24s → %s\n", delta, c.rule.Pattern, c.rule.Destination)
		} else {
			logExperiment(c.rule.Pattern, delta, false, "reverted (no replay gain)")
		}
	}

	// Persist survivors as pre-scored proposals for `pmax improve`.
	var props []scoredProposal
	for _, s := range survivors {
		props = append(props, scoredProposal{
			Pattern: s.rule.Pattern, Destination: s.rule.Destination,
			Reason: "autotune: " + s.evidence, Confidence: 1.0, ReplayDelta: s.replay,
		})
	}
	if len(props) > 0 {
		data, _ := json.MarshalIndent(props, "", "  ")
		os.WriteFile(filepath.Join(CfgState, "learning", "proposals.json"), data, 0o644)
	}

	fmt.Printf("autotune done: %d probed, %d kept, replay %+.2f → %+.2f\n",
		probed, len(survivors), startBase, baseline)
	if len(survivors) > 0 {
		fmt.Println("Survivors staged as proposals — review with: pmax improve  (y/n per rule)")
	} else {
		fmt.Println("No candidate improved on replay — current rules stand. ✓")
	}
	return 0
}

// scoredProposal is a proposal carrying its dream (replay) evidence.
type scoredProposal struct {
	Pattern     string  `json:"pattern"`
	Destination string  `json:"destination"`
	Reason      string  `json:"reason"`
	Confidence  float64 `json:"confidence"`
	ReplayDelta float64 `json:"replay_delta"`
}

// loadTunedProposals returns autotune survivors awaiting human review.
func loadTunedProposals() []scoredProposal {
	data, err := os.ReadFile(filepath.Join(CfgState, "learning", "proposals.json"))
	if err != nil {
		return nil
	}
	var props []scoredProposal
	json.Unmarshal(data, &props)
	var clean []scoredProposal
	for _, p := range props {
		if destinationAllowed(p.Destination) && p.Pattern != "" {
			clean = append(clean, p)
		}
	}
	return clean
}

// modelCandidates: optional LLM-drafted rules, immediately dream-scored.
// Deterministic candidates come first; these are spice, not the meal.
func modelCandidates(history []decision) []candidate {
	raw, err := chat([]chatMessage{
		{Role: "system", Content: "You propose file-classification rules as STRICT JSON " +
			 "{\"proposals\":[{\"pattern\":\"glob\",\"destination\":\"abs dir\",\"reason\":\"...\",\"confidence\":0-1}]}. Classification only; destinations under ~/Documents or ~/Pictures. Max 5."},
		{Role: "user", Content: fmt.Sprintf(
			"Existing rules: %v\n\nRecent decision digest:\n%s\n\nPropose rules consistent with what the user APPROVES and avoid what they REJECT.",
			currentRules(), senseDigest())},
	}, 600)
	if err != nil {
		return nil
	}
	doc, err := parseProposals(raw)
	if err != nil {
		return nil
	}
	base := currentRules()
	baseline := replayScore(base, history)
	var out []candidate
	for _, p := range doc.Proposals {
		r := rule{Pattern: p.Pattern, Destination: p.Destination}
		trial := append(append([]rule{}, base...), r)
		out = append(out, candidate{
			rule: r, replay: replayScore(trial, history) - baseline,
			evidence: "model draft: " + p.Reason,
		})
	}
	return out
}
