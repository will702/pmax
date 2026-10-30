package main

// Tests for the dream/autotune loop: replay scoring, candidate derivation,
// keep-or-revert budget behavior, and the tuned-proposal queue.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedHistory writes a signals.tsv describing a user who consistently
// approves Sponsorin* → Sponsorship and rejects name-dupe deletions.
func seedHistory(t *testing.T, approvals int) {
	t.Helper()
	f, _ := os.Create(filepath.Join(CfgState, "learning", "signals.tsv"))
	defer f.Close()
	for i := 0; i < approvals; i++ {
		fprintf(f, "2026-10-01T10:00:00\tapproved\tMOVE\tlearned-rule\t%s/Sponsorin-%d.pdf\t%s/Sponsorin-%d.pdf\n",
			UserHome+"/Downloads", i, UserHome+"/Documents/03-Archive/Sponsorship", i)
	}
	for i := 0; i < 3; i++ {
		fprintf(f, "2026-10-01T11:00:00\trejected\tDELETE\tname-dupe of 'x'\t%s/whatever (1).pdf\t-\n",
			UserHome+"/Downloads")
	}
}

func fprintf(f *os.File, format string, a ...any) {
	if _, err := f.WriteString(fmt.Sprintf(format, a...)); err != nil {
		panic(err)
	}
}

func TestReplayScoreRewardsAgreement(t *testing.T) {
	sandbox(t)
	seedHistory(t, 6)
	history := replayHistory()
	if len(history) != 9 {
		t.Fatalf("expected 9 replayable decisions, got %d", len(history))
	}

	good := rule{Pattern: "Sponsorin*", Destination: UserHome + "/Documents/03-Archive/Sponsorship"}
	bad := rule{Pattern: "Sponsorin*", Destination: UserHome + "/Pictures/Wrong"}
	empty := []rule{}

	if replayScore([]rule{good}, history) <= replayScore([]rule{bad}, history) {
		t.Error("rule agreeing with approvals should outscore a contradicting one")
	}
	if replayScore(empty, history) != 0 {
		t.Error("empty rule set must score 0")
	}
	// parsimony: the same rule duplicated must not improve the score
	if replayScore([]rule{good, good}, history) >= replayScore([]rule{good}, history) {
		t.Error("duplicate rules must be penalized by parsimony")
	}
}

func TestDeriveCandidatesFromHistory(t *testing.T) {
	sandbox(t)
	seedHistory(t, 6)
	cands := deriveCandidates(replayHistory())
	if len(cands) == 0 {
		t.Fatal("no candidates derived from clear approval pattern")
	}
	found := false
	for _, c := range cands {
		if strings.HasPrefix(c.rule.Pattern, "Sponsorin") &&
			c.rule.Destination == UserHome+"/Documents/03-Archive/Sponsorship" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a Sponsorin* → Sponsorship candidate, got %+v", cands[:min(3, len(cands))])
	}
	if !destinationAllowed(cands[0].rule.Destination) {
		t.Error("derived candidate escaped the destination whitelist")
	}
}

func TestAutotuneKeepsOnlyReplayGains(t *testing.T) {
	sandbox(t)
	seedHistory(t, 6)
	quiet(t, func() { cmdAutotune([]string{"--minutes", "1"}) })

	props := loadTunedProposals()
	if len(props) == 0 {
		t.Fatal("autotune found no keepers despite a clear approval pattern")
	}
	if !strings.HasPrefix(props[0].Pattern, "Sponsorin") {
		t.Errorf("top survivor = %+v, expected Sponsorin*", props[0])
	}
	// experiments log must exist and record keeps/reverts
	data, err := os.ReadFile(filepath.Join(CfgState, "learning", "experiments.tsv"))
	if err != nil || len(data) == 0 {
		t.Error("experiment log missing")
	}
	// autotune must not touch rules.learned directly — human gate only
	if _, err := os.Stat(learnedPath()); !os.IsNotExist(err) {
		t.Error("autotune wrote to rules.learned — SAFETY VIOLATION (adoption must stay human)")
	}
}

func TestAutotuneNeedsHistory(t *testing.T) {
	sandbox(t)
	quiet(t, func() { cmdAutotune([]string{"--minutes", "1"}) })
	if _, err := os.Stat(filepath.Join(CfgState, "learning", "proposals.json")); err == nil {
		t.Error("autotune tuned with (almost) no history")
	}
}

func TestTunedProposalsRespectWhitelist(t *testing.T) {
	sandbox(t)
	os.MkdirAll(filepath.Join(CfgState, "learning"), 0o755)
	os.WriteFile(filepath.Join(CfgState, "learning", "proposals.json"),
		[]byte(`[{"pattern":"*","destination":"`+UserHome+`/.Trash","reason":"evil","confidence":1,"replay_delta":9}]`), 0o644)
	props := loadTunedProposals()
	if len(props) != 0 {
		t.Fatal("proposals.json with out-of-whitelist destination survived the loader — SAFETY VIOLATION")
	}
}
