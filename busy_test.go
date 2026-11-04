package main

// Busy-aware behavior: SecondBrain drops $STATE/load.json; stale or missing
// means "unknown" and PMAX behaves normally. Tidy never bypasses the approver.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLoad(t *testing.T, level string, age time.Duration) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"generated_at":     time.Now().Add(-age).Format(time.RFC3339),
		"level":            level,
		"today_booked_pct": 80,
	})
	if err := os.WriteFile(filepath.Join(CfgState, "load.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureNotify swaps the notifier so tests never pop real notifications.
func captureNotify(t *testing.T) *[]string {
	t.Helper()
	var got []string
	notifyHook = func(title, body string) { got = append(got, title+" | "+body) }
	t.Cleanup(func() { notifyHook = nil })
	return &got
}

func TestIsScreenshotCleanShot(t *testing.T) {
	for name, want := range map[string]bool{
		"Screenshot 2026-01-01 at 10.00.png":   true,
		"CleanShot 2026-01-01 at 10.00@2x.png": true,
		"my CleanShot notes.txt":               false,
		"report.pdf":                           false,
	} {
		if got := isScreenshot(name); got != want {
			t.Errorf("isScreenshot(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSweepStagesOldInstallersOnly(t *testing.T) {
	home := sandbox(t)
	dl := filepath.Join(home, "Downloads")
	write(t, filepath.Join(dl, "old.dmg"), "x", 5)
	write(t, filepath.Join(dl, "fresh.dmg"), "x", 1)
	write(t, filepath.Join(dl, "old.pkg"), "x", 4)
	write(t, filepath.Join(dl, "notes.txt"), "x", 5)
	p := newPlan("plan")
	stageZoneSweep(p, dl, "downloads")
	got := map[string]Action{}
	for _, a := range p.Actions {
		got[filepath.Base(a.Src)] = a
	}
	for _, name := range []string{"old.dmg", "old.pkg"} {
		a, ok := got[name]
		if !ok || a.Kind != "DELETE" || !strings.HasPrefix(a.Reason, "installer/") {
			t.Errorf("%s should be staged as an installer quarantine, got %+v", name, a)
		}
		if a.Flag != "review" {
			t.Errorf("%s may be a vault/backup image: must be flagged for review, got %q", name, a.Flag)
		}
	}
	if _, ok := got["fresh.dmg"]; ok {
		t.Error("a 1-day-old installer must not be staged")
	}
	if _, ok := got["notes.txt"]; ok {
		t.Error("non-installers must not match the installer rule")
	}
	if fileExists(filepath.Join(dl, "old.dmg")) == false {
		t.Error("staging must never move files")
	}
}

func TestReadLoad(t *testing.T) {
	sandbox(t)
	if busyLevel() != "" {
		t.Error("missing file => unknown")
	}
	writeLoad(t, "heavy", time.Hour)
	if busyLevel() != "heavy" || !isHeavyDay() {
		t.Error("fresh heavy file should read as heavy")
	}
	writeLoad(t, "heavy", 13*time.Hour)
	if busyLevel() != "" {
		t.Error("stale file must be ignored")
	}
	writeLoad(t, "extreme", time.Hour)
	if busyLevel() != "" {
		t.Error("unknown level must be ignored")
	}
	os.WriteFile(filepath.Join(CfgState, "load.json"), []byte("{not json"), 0o644)
	if busyLevel() != "" {
		t.Error("garbage must be ignored")
	}
}

func TestWatcherHoldsNotificationOnHeavyDayButStillStages(t *testing.T) {
	fixtures(t)
	got := captureNotify(t)
	writeLoad(t, "heavy", time.Hour)
	Cfg.BusyNotifyMin = 1000
	quiet(t, func() { cmdWatcher(nil) })
	if len(*got) != 0 {
		t.Errorf("heavy day + small backlog must stay quiet, got %v", *got)
	}
	total := 0
	for _, p := range loadPlans("plan-*") {
		total += p.Count()
	}
	if total == 0 {
		t.Error("watcher must still stage proposals on a heavy day")
	}
}

func TestWatcherNotifiesNormallyWithoutLoadHint(t *testing.T) {
	fixtures(t)
	got := captureNotify(t)
	quiet(t, func() { cmdWatcher(nil) })
	if len(*got) != 1 {
		t.Errorf("expected one notification, got %v", *got)
	}
}

func TestWatcherBigBacklogBreaksThroughOnHeavyDay(t *testing.T) {
	fixtures(t)
	got := captureNotify(t)
	writeLoad(t, "heavy", time.Hour)
	Cfg.BusyNotifyMin = 1
	quiet(t, func() { cmdWatcher(nil) })
	if len(*got) != 1 {
		t.Errorf("large backlog should still notify, got %v", *got)
	}
}

func TestMorningReminderIsShortOnHeavyDay(t *testing.T) {
	fixtures(t)
	got := captureNotify(t)
	writeLoad(t, "heavy", time.Hour)
	quiet(t, func() { cmdRemind([]string{"morning"}) })
	if len(*got) != 1 || !strings.Contains((*got)[0], "pmax tidy") {
		t.Errorf("heavy-day briefing should point at pmax tidy, got %v", *got)
	}
}

// stageSized stages n DELETE rows with strictly increasing sizes in one plan.
func stageSized(t *testing.T, home string, n int) (*Plan, []string) {
	t.Helper()
	p := newPlan("plan")
	var files []string
	for i := 1; i <= n; i++ {
		f := filepath.Join(home, "Downloads", fmt.Sprintf("f%d.tmp", i))
		write(t, f, strings.Repeat("x", i*100), 40)
		files = append(files, f)
		p.Stage(Action{Kind: "DELETE", Src: f, Reason: "test"})
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	return p, files
}

func TestTidyOffersTopFiveBiggestAndEachNeedsApproval(t *testing.T) {
	home := sandbox(t)
	p, files := stageSized(t, home, 8)
	biggest, second := files[7], files[6]

	// approve the biggest, reject the second, then EOF (stop).
	withStdin(t, "y\nn\n", func() { quiet(t, func() { cmdTidy(nil) }) })

	if fileExists(biggest) {
		t.Error("approved biggest should be quarantined")
	}
	if !fileExists(second) {
		t.Error("rejected row must not be touched")
	}
	for _, f := range files[:6] {
		if !fileExists(f) {
			t.Errorf("unapproved file moved: %s", f)
		}
	}
	left := loadPlan(p.Path)
	if left == nil {
		t.Fatal("source plan vanished")
	}
	srcs := map[string]bool{}
	for _, a := range left.Actions {
		srcs[a.Src] = true
	}
	if srcs[biggest] || srcs[second] {
		t.Error("decided rows must be stripped from the source plan")
	}
	if !srcs[files[6-1]] || len(left.Actions) != 6 {
		t.Errorf("undecided top-5 rows and the other 3 must stay staged, got %d rows", len(left.Actions))
	}
	if m, _ := filepath.Glob(filepath.Join(CfgState, "staged", "tidy-*.tsv")); len(m) != 0 {
		t.Error("temporary tidy plan must be cleaned up")
	}
}

func TestTidyEOFExecutesNothing(t *testing.T) {
	home := sandbox(t)
	_, files := stageSized(t, home, 3)
	withStdin(t, "", func() { quiet(t, func() { cmdTidy(nil) }) })
	for _, f := range files {
		if !fileExists(f) {
			t.Error("EOF must not execute anything")
		}
	}
}

func TestTidySkipsReviewFlaggedRows(t *testing.T) {
	home := sandbox(t)
	f := filepath.Join(home, "Downloads", "ambiguous.pdf")
	write(t, f, "x", 10)
	p := newPlan("plan")
	p.Stage(Action{Kind: "DELETE", Src: f, Reason: "dupe", Flag: "review"})
	p.Save()
	if got := tidyCandidates(5); len(got) != 0 {
		t.Errorf("ambiguous (review) rows are never offered as quick wins: %v", got)
	}
}

func TestStatusIncludesLoad(t *testing.T) {
	sandbox(t)
	writeLoad(t, "heavy", time.Hour)
	out := captureStdout(t, func() { cmdStatus([]string{"--json"}) })
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["load"] != "heavy" {
		t.Errorf("load = %v", got["load"])
	}
}

func TestBusyLevelRejectsFutureTimestamp(t *testing.T) {
	sandbox(t)
	writeLoad(t, "heavy", -48*time.Hour) // generated "two days from now"
	if busyLevel() != "" {
		t.Error("a future timestamp must not pin the system on 'heavy' forever")
	}
}

func TestTidyRejectKeepsSiblingMoveRow(t *testing.T) {
	home := sandbox(t)
	f := filepath.Join(home, "Downloads", "x.bin")
	write(t, f, strings.Repeat("x", 500), 40)
	p := newPlan("plan")
	p.Stage(Action{Kind: "DELETE", Src: f, Reason: "stale"})
	p.Stage(Action{Kind: "MOVE", Src: f, Dst: filepath.Join(home, "Documents", "x.bin"), Reason: "organize"})
	p.Save()
	withStdin(t, "n\n", func() { quiet(t, func() { cmdTidy(nil) }) })
	left := loadPlan(p.Path)
	if left == nil || left.Count() != 1 || left.Actions[0].Kind != "MOVE" {
		t.Errorf("only the rejected DELETE row may be dropped, got %+v", left)
	}
}

func TestPlansCreatedInOneSecondDoNotCollide(t *testing.T) {
	sandbox(t)
	a, b := newPlan("plan"), newPlan("plan")
	if a.Path == b.Path {
		t.Error("two plans in the same second/process must not share a file (one would overwrite the other)")
	}
}

func TestStaleTidyFileFromKilledSessionIsCleaned(t *testing.T) {
	home := sandbox(t)
	stale := filepath.Join(CfgState, "staged", "tidy-old-1.tsv")
	os.WriteFile(stale, []byte("ACTION\tSRC\tDST\tSIZE\tAGE\tREASON\tFLAG\n"), 0o644)
	stageSized(t, home, 1)
	withStdin(t, "", func() { quiet(t, func() { cmdTidy(nil) }) })
	if fileExists(stale) {
		t.Error("leftover tidy plan from a killed session should be removed")
	}
}
