package main

// dream: offline policy evaluation — the Dream-RSI pattern applied to pmax.
//
// Your approve/reject history (learning/signals.tsv) is a recorded world.
// Instead of experimenting on live files, candidate rule-sets are scored by
// REPLAYING that history: would the rules have proposed what you actually
// accepted, and avoided what you actually rejected?
//
//     +1  rule maps a basename to the exact move you approved
//     -1  rule maps a basename to a move you already rejected
//   -0.05 per rule (parsimony: simpler rule-sets win ties)
//
// Nothing here touches a file. It is pure reading + scoring.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// decision is one replayable historical outcome.
type decision struct {
	Basename string
	Proposed string // dst that was shown to the user
	Outcome  string // approved | rejected | undone
}

// replayHistory loads replayable decisions from the signal log.
func replayHistory() []decision {
	data, err := os.ReadFile(filepath.Join(CfgState, "learning", "signals.tsv"))
	if err != nil {
		return nil
	}
	var out []decision
	for _, line := range splitLines(string(data)) {
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		event, src, dst := f[1], f[4], f[5]
		switch event {
		case "approved", "rejected":
			// skip rule-learning events (action=="rule")
			if f[2] == "rule" {
				continue
			}
			out = append(out, decision{Basename: filepath.Base(src), Proposed: dst, Outcome: event})
		case "undone":
			// an undo means the earlier approval is rescinded: count as rejected
			out = append(out, decision{Basename: filepath.Base(src), Proposed: dst, Outcome: "rejected"})
		}
	}
	return out
}

// replayScore evaluates a rule-set against the history. Higher is better.
func replayScore(rules []rule, history []decision) float64 {
	if len(rules) == 0 {
		return 0
	}
	var score float64
	for _, d := range history {
		for _, r := range rules {
			if globMatch(r.Pattern, d.Basename) {
				if r.Destination == filepath.Dir(d.Proposed) || r.Destination+"/" == filepath.Dir(d.Proposed) {
					if d.Outcome == "approved" {
						score++
					} else {
						score--
					}
				} else if d.Outcome == "rejected" {
					// would have redirected a rejected proposal somewhere else
					score += 0.5
				} else {
					// would have changed an approved destination — uncertain
					score -= 0.25
				}
				break // first matching rule wins, like organize does
			}
		}
	}
	return score - 0.05*float64(len(rules)) // parsimony
}

type rule struct {
	Pattern     string
	Destination string
}

func currentRules() []rule {
	var out []rule
	for _, l := range readLearned() {
		if strings.HasPrefix(l, "#") {
			continue
		}
		g, d, ok := strings.Cut(l, "\t")
		if ok && g != "" && destinationAllowed(d) {
			out = append(out, rule{Pattern: g, Destination: d})
		}
	}
	return out
}

// ── deterministic candidate generation from history clusters ───────────────
// No model required: group approved basenames by destination directory and
// generalize common prefixes into globs. Rejected destinations seed negative
// evidence (their globs are penalized by replay automatically).

type candidate struct {
	rule     rule
	replay   float64 // score of {currentRules + this rule} minus baseline
	evidence string
}

func deriveCandidates(history []decision) []candidate {
	base := currentRules()
	baseline := replayScore(base, history)

	// cluster: destination dir -> basenames approved there
	approvedAt := map[string][]string{}
	for _, d := range history {
		if d.Outcome == "approved" {
			dir := filepath.Dir(d.Proposed)
			approvedAt[dir] = append(approvedAt[dir], d.Basename)
		}
	}

	seen := map[string]bool{}
	var out []candidate
	dirs := sortedKeys(approvedAt)
	for _, dir := range dirs {
		if !destinationAllowed(dir) {
			continue
		}
		names := approvedAt[dir]
		for _, pat := range generalizePatterns(names) {
			key := pat + "\t" + dir
			if seen[key] {
				continue
			}
			seen[key] = true
			// skip candidates identical to an existing rule
			dup := false
			for _, r := range base {
				if r.Pattern == pat && r.Destination == dir {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			r := rule{Pattern: pat, Destination: dir}
			scored := append(append([]rule{}, base...), r)
			out = append(out, candidate{
				rule:     r,
				replay:   replayScore(scored, history) - baseline,
				evidence: fmt.Sprintf("%d approved file(s) landed in %s", len(names), dir),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].replay > out[j].replay })
	return out
}

// generalizePatterns: common-token-prefix globs from a set of basenames.
func generalizePatterns(names []string) []string {
	var pats []string
	for _, n := range names {
		stem := n
		if i := strings.LastIndex(stem, "."); i > 0 {
			stem = stem[:i]
		}
		tokens := strings.FieldsFunc(stem, func(r rune) bool {
			return r == ' ' || r == '-' || r == '_' || r == '.' || r == '(' || r == ')'
		})
		if len(tokens) == 0 {
			continue
		}
		// leading token as prefix-glob; also token+ext for tighter variants
		ext := filepath.Ext(n)
		pats = append(pats,
			tokens[0]+"*"+ext,
			"*"+tokens[len(tokens)-1]+"*"+ext,
		)
	}
	// keep only globs that match ≥2 names (evidence of a pattern, not noise)
	var out []string
	seen := map[string]bool{}
	for _, p := range pats {
		if seen[p] {
			continue
		}
		seen[p] = true
		hits := 0
		for _, n := range names {
			if globMatch(p, n) {
				hits++
			}
		}
		if hits >= 2 {
			out = append(out, p)
		}
	}
	return out
}

// ── `pmax dream` — inspect the replay landscape without proposing anything ──

func cmdDream(args []string) int {
	history := replayHistory()
	if len(history) < 5 {
		fmt.Println("Not enough history to dream on (need ≥5 decisions).")
		fmt.Println("Use pmax organize / pmax sweep a few times first — every y/n becomes replay data.")
		return 0
	}
	base := currentRules()
	baseline := replayScore(base, history)
	fmt.Printf("dream — replaying %d decision(s) from history\n", len(history))
	fmt.Printf("  current rules: %d, replay score %.2f\n\n", len(base), baseline)

	cands := deriveCandidates(history)
	if len(cands) == 0 {
		fmt.Println("No evidence-backed candidate rules derived from history. ✓")
		return 0
	}
	fmt.Println("  replay- ranked candidates (nothing is applied here):")
	for i, c := range cands {
		if i >= 10 {
			fmt.Printf("  … + %d more\n", len(cands)-10)
			break
		}
		fmt.Printf("  %+.2f  %-24s → %s\n         %s\n", c.replay, c.rule.Pattern, c.rule.Destination, c.evidence)
	}
	fmt.Println("\n  Turn the best into proposals: pmax improve   (still y/n-gated)")
	return 0
}

// ── experiment log (autotune writes here; morning briefing reads it) ───────

func logExperiment(name string, delta float64, kept bool, detail string) {
	f, err := os.OpenFile(filepath.Join(CfgState, "learning", "experiments.tsv"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%+.2f\t%v\t%s\n",
		time.Now().Format("2006-01-02T15:04:05"), name, delta, kept, detail)
}
