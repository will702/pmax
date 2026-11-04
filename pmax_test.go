package main

// pmax test suite — runs entirely inside t.TempDir() sandboxes. Nothing here
// touches the real home. The core assertions encode the safety guarantee:
// approving item N moves ONLY item N; rejection touches nothing; undo restores
// byte-for-byte; stage-only never mutates; learned rules can never target
// deletion paths.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── sandbox ─────────────────────────────────────────────────────────────────

func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	UserHome = home
	CfgHome = filepath.Join(home, ".config", "pmax")
	CfgState = filepath.Join(home, ".local", "state", "pmax")
	StageOnly = false
	currentRun = ""
	Cfg = defaults()
	Cfg.AIBackend = "off"                         // never talk to a real ~/ai server
	notifyHook = func(string, string) {}         // never post real notifications
	Cfg.AIVenv = filepath.Join(CfgState, "venv") // keep away from real venv
	for _, d := range []string{CfgHome, CfgState, CfgState + "/logs", CfgState + "/staged",
		CfgState + "/manifests", CfgState + "/quarantine", CfgState + "/learning", CfgState + "/run"} {
		os.MkdirAll(d, 0o755)
	}
	for _, d := range []string{"Desktop", "Downloads", "Documents", "Pictures", "projects"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	return home
}

func write(t *testing.T, path, content string, ageDays int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if ageDays > 0 {
		past := time.Now().AddDate(0, 0, -ageDays)
		os.Chtimes(path, past, past)
	}
}

// withStdin runs fn with piped answers (non-TTY per-item approval path).
func withStdin(t *testing.T, answers string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	go func() {
		w.WriteString(answers)
		w.Close()
	}()
	defer r.Close()
	fn()
}

// quiet swallows command stdout during tests.
func quiet(t *testing.T, fn func()) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	_, _ = ioCopy(r)
}

func ioCopy(f *os.File) (string, error) {
	var buf bytes.Buffer
	b := make([]byte, 4096)
	for {
		n, err := f.Read(b)
		buf.Write(b[:n])
		if err != nil {
			return buf.String(), nil
		}
	}
}

func fixtures(t *testing.T) {
	t.Helper()
	home := sandbox(t)
	write(t, filepath.Join(home, "Desktop", "Screenshot 2026-10-24 at 09.11.02.png"), "png", 6)
	write(t, filepath.Join(home, "Desktop", "notes.txt"), "hello", 0)
	write(t, filepath.Join(home, "Desktop", ".DS_Store"), "junk", 0)
	write(t, filepath.Join(home, "Downloads", "paper.pdf"), "pdf-bytes", 3)
	write(t, filepath.Join(home, "Downloads", "paper (1).pdf"), "pdf-bytes", 3)
	write(t, filepath.Join(home, "Downloads", "stalled.crdownload"), "partial", 21)
	write(t, filepath.Join(home, "Downloads", "oldfile.zip"), "z", 45)
}

// ── unit: glob / guards / classification ───────────────────────────────────

func TestGlobMatch(t *testing.T) {
	cases := []struct{ pat, s string; want bool }{
		{"/a/*", "/a/b/c/d.txt", true},      // * crosses / (unlike filepath.Match)
		{"/a/Documents/*", "/a/Documents/x/y.pdf", true},
		{"/a/other/*", "/a/Documents/x.pdf", false},
		{"/a/prod*", "/a/productivity-mac-max", true},
		{"/a/.*", "/a/.zshrc", true},
		{"Screenshot *", "Screenshot 2026 at 1.png", true},
		{"*.pdf", "a.pdf", true},
		{"*.pdf", "a.txt", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestIsProtected(t *testing.T) {
	sandbox(t)
	if !isProtected(filepath.Join(UserHome, "Documents", "x.pdf")) {
		t.Error("Documents must be protected")
	}
	if !isProtected(filepath.Join(UserHome, ".ssh")) {
		t.Error("dotdirs must be protected")
	}
	if isProtected(filepath.Join(UserHome, "Desktop", "screenshot.png")) {
		t.Error("Desktop files must be fair game")
	}
}

func TestDedupeBase(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"paper (1).pdf", "paper.pdf"},
		{"GAME copy.zip", "GAME.zip"},
		{"v2 copy 2.md", "v2.md"},
		{"plain.pdf", ""},
	} {
		got, ok := dedupeBase(c.in)
		if (c.want == "") != (!ok) || got != c.want && c.want != "" {
			t.Errorf("dedupeBase(%q)=%q,%v want %q", c.in, got, ok, c.want)
		}
	}
}

func TestTypeFor(t *testing.T) {
	if typeFor("a.PDF") != "Documents" || typeFor("b.pptx") != "Presentations" ||
		typeFor("c.dmg") != "Installers" || typeFor("weird.zzz") != "Other" {
		t.Fatal("typeFor misclassified")
	}
	if !isScreenshot("Screenshot 2026-10-24 at 09.11.02.png") || isScreenshot("cat.png") {
		t.Fatal("isScreenshot wrong")
	}
}

// ── safety: selective approval moves ONLY the approved item ────────────────

func TestSelectiveApproval(t *testing.T) {
	fixtures(t)
	desk := filepath.Join(UserHome, "Desktop")
	shot := filepath.Join(desk, "Screenshot 2026-10-24 at 09.11.02.png")
	notes := filepath.Join(desk, "notes.txt")

	plan := newPlan("plan")
	stageZoneOrganize(plan, desk, "desktop")
	if plan.Count() != 2 {
		t.Fatalf("expected 2 staged actions (screenshot + notes), got %d", plan.Count())
	}
	if !strings.Contains(plan.Actions[0].Dst, "Pictures/Screenshots/") {
		t.Errorf("screenshot should go to Pictures/Screenshots, got %s", plan.Actions[0].Dst)
	}
	plan.Save()

	// Answer: n to screenshot, y to notes, then EOF.
	withStdin(t, "n\ny\n", func() {
		quiet(t, func() { approvePlan(plan) })
	})

	if _, err := os.Stat(shot); err != nil {
		t.Error("screenshot moved despite 'n' — SAFETY VIOLATION")
	}
	if _, err := os.Stat(notes); err == nil {
		t.Error("notes not moved despite 'y'")
	}
	if dest := filepath.Join(Cfg.DestInbox, "Documents", "notes.txt"); !fileExists(dest) {
		t.Error("notes not at Inbox/Documents/notes.txt")
	}

	// undo restores byte-for-byte
	quiet(t, func() { cmdUndo(nil) })
	if !fileExists(notes) {
		t.Error("undo did not restore notes.txt")
	}
}

func TestRejectionTouchesNothing(t *testing.T) {
	fixtures(t)
	plan := newPlan("plan")
	stageZoneSweep(plan, filepath.Join(UserHome, "Downloads"), "downloads")
	before := map[string]bool{}
	for _, f := range []string{"paper.pdf", "paper (1).pdf", "stalled.crdownload", "oldfile.zip"} {
		before[f] = fileExists(filepath.Join(UserHome, "Downloads", f))
	}
	plan.Save()
	withStdin(t, "n\nn\nn\nn\nn\nn\n", func() {
		quiet(t, func() { approvePlan(plan) })
	})
	for f, had := range before {
		if had != fileExists(filepath.Join(UserHome, "Downloads", f)) {
			t.Errorf("%s changed after all-no run", f)
		}
	}
}

func TestEOFStopsExecution(t *testing.T) {
	fixtures(t)
	plan := newPlan("plan")
	stageZoneSweep(plan, filepath.Join(UserHome, "Downloads"), "downloads")
	plan.Save()
	// Empty stdin (launchd-like): nothing may move.
	withStdin(t, "", func() {
		quiet(t, func() { approvePlan(plan) })
	})
	if !fileExists(filepath.Join(UserHome, "Downloads", "stalled.crdownload")) {
		t.Error("EOF stdin executed an action — SAFETY VIOLATION")
	}
	// And the plan should still hold its rows (nothing decided).
	if p := loadPlan(plan.Path); p == nil || p.Count() == 0 {
		t.Error("plan consumed on EOF")
	}
}

func TestSweepStagesRightThings(t *testing.T) {
	fixtures(t)
	plan := newPlan("plan")
	stageZoneSweep(plan, filepath.Join(UserHome, "Downloads"), "downloads")
	stageZoneSweep(plan, filepath.Join(UserHome, "Desktop"), "desktop")

	var sawCrdownload, sawDSStore, sawStale, sawDupe, sawFlagged bool
	for _, a := range plan.Actions {
		switch {
		case strings.HasSuffix(a.Src, ".crdownload"):
			sawCrdownload = true
		case strings.HasSuffix(a.Src, ".DS_Store"):
			sawDSStore = true
		case strings.HasSuffix(a.Src, "oldfile.zip"):
			sawStale = a.Reason == "stale/>30d"
		case strings.HasSuffix(a.Src, "paper (1).pdf"):
			sawDupe = true
			sawFlagged = a.Flag == "ok" // base paper.pdf exists
		}
	}
	if !sawCrdownload || !sawDSStore || !sawStale || !sawDupe || !sawFlagged {
		t.Fatalf("sweep missed categories: cr=%v ds=%v stale=%v dupe=%v flagged=%v",
			sawCrdownload, sawDSStore, sawStale, sawDupe, sawFlagged)
	}
}

func TestDupeFlaggedWhenOriginalMissing(t *testing.T) {
	home := sandbox(t)
	write(t, filepath.Join(home, "Downloads", "only (1).pdf"), "x", 0)
	plan := newPlan("plan")
	stageZoneSweep(plan, filepath.Join(home, "Downloads"), "downloads")
	if plan.Count() != 1 || plan.Actions[0].Flag != "review" {
		t.Fatalf("expected 1 review-flagged action, got %+v", plan.Actions)
	}
}

// ── stage-only / watcher can never mutate ───────────────────────────────────

func TestStageOnlyNeverMutates(t *testing.T) {
	fixtures(t)
	StageOnly = true
	defer func() { StageOnly = false }()
	// Even with ASSUME_YES piped, stage-only must not execute.
	t.Setenv("PMAX_ASSUME_YES", "1")
	withStdin(t, "y\ny\ny\n", func() {
		quiet(t, func() { cmdOrganize([]string{"desktop"}) })
	})
	if !fileExists(filepath.Join(UserHome, "Desktop", "Screenshot 2026-10-24 at 09.11.02.png")) {
		t.Error("stage-only mode moved a file — SAFETY VIOLATION")
	}
	// something must be staged for later review
	total := 0
	for _, p := range loadPlans("plan-*") {
		total += p.Count()
	}
	if total == 0 {
		t.Error("stage-only staged nothing")
	}
}

func TestWatcherOnlyStages(t *testing.T) {
	fixtures(t)
	quiet(t, func() { cmdWatcher(nil) })
	shot := filepath.Join(UserHome, "Desktop", "Screenshot 2026-10-24 at 09.11.02.png")
	if !fileExists(shot) {
		t.Error("watcher moved a file — SAFETY VIOLATION")
	}
	total := 0
	for _, p := range loadPlans("plan-*") {
		total += p.Count()
	}
	if total == 0 {
		t.Error("watcher staged nothing")
	}
}

// ── quarantine retention ────────────────────────────────────────────────────

func TestQuarantinePurgeRespectsRetention(t *testing.T) {
	sandbox(t)
	qdir := filepath.Join(CfgState, "quarantine", "run-x")
	os.MkdirAll(qdir, 0o755)
	fresh := filepath.Join(qdir, "fresh.txt")
	stale := filepath.Join(qdir, "stale.txt")
	os.WriteFile(fresh, []byte("f"), 0o644)
	os.WriteFile(stale, []byte("s"), 0o644)
	old := time.Now().AddDate(0, 0, -(Cfg.QuarantineRetentionDays + 3))
	os.Chtimes(stale, old, old)

	purgeQuarantine()
	if fileExists(stale) {
		t.Error("expired quarantine file not purged")
	}
	if !fileExists(fresh) {
		t.Error("fresh quarantine file purged too early")
	}
}

// ── delete goes to quarantine and undo brings it back ──────────────────────

func TestDeleteIsQuarantineAndUndoable(t *testing.T) {
	home := sandbox(t)
	target := filepath.Join(home, "Downloads", "stalled.crdownload")
	write(t, target, "partial-data", 10)

	plan := newPlan("plan")
	plan.Stage(Action{Kind: "DELETE", Src: target, Reason: "test"})
	plan.Save()
	withStdin(t, "y\n", func() { quiet(t, func() { approvePlan(plan) }) })

	if fileExists(target) {
		t.Fatal("DELETE did not remove the file")
	}
	// must exist somewhere under quarantine
	var found string
	filepath.WalkDir(filepath.Join(CfgState, "quarantine"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("quarantined file not found in quarantine")
	}
	quiet(t, func() { cmdUndo(nil) })
	data, _ := os.ReadFile(target)
	if string(data) != "partial-data" {
		t.Errorf("undo did not restore byte-for-byte: %q", data)
	}
}

// ── learned rules: classification-only, prefix-guarded ──────────────────────

func TestLearnedRuleDestinationGuard(t *testing.T) {
	home := sandbox(t)
	// A "malicious" learned rule pointing outside allowed roots must be ignored.
	os.WriteFile(learnedPath(), []byte("*.secret\t"+home+"/.Trash\n"), 0o644)
	if _, ok := learnedDest(filepath.Join(home, "Downloads", "a.secret")); ok {
		t.Error("learned rule with disallowed destination was applied — SAFETY VIOLATION")
	}
	os.WriteFile(learnedPath(), []byte("Sponsorin*\t"+home+"/Documents/03-Archive/Sponsorship\n"), 0o644)
	dest, ok := learnedDest(filepath.Join(home, "Downloads", "Sponsorin.id.pdf"))
	if !ok || dest != home+"/Documents/03-Archive/Sponsorship" {
		t.Errorf("legit learned rule not applied: %v %q", ok, dest)
	}
}

func TestParseProposalsFilters(t *testing.T) {
	home := sandbox(t)
	good := `{"proposals":[{"pattern":"Sponsorin*","destination":"` + home + `/Documents/03-Archive/Sponsorship","reason":"user keeps rejecting these","confidence":0.9}]}`
	doc, err := parseProposals(good)
	if err != nil || len(doc.Proposals) != 1 {
		t.Fatalf("valid proposal rejected: %v %+v", err, doc)
	}
	// fenced variant
	fenced := "```json\n" + good + "\n```"
	if _, err := parseProposals(fenced); err != nil {
		t.Fatalf("fenced JSON rejected: %v", err)
	}
	// prose is discarded
	if _, err := parseProposals("Sure! Here are some rules: ..."); err == nil {
		t.Fatal("prose accepted as proposals — SAFETY VIOLATION")
	}
	// deletion-adjacent destination filtered out
	bad := `{"proposals":[{"pattern":"*","destination":"` + home + `/.Trash","reason":"x","confidence":1}]}`
	doc, _ = parseProposals(bad)
	if len(doc.Proposals) != 0 {
		t.Fatal("destination outside allowed roots survived the filter")
	}
}

// ── colgrep index (keyword mode, no sidecar) ────────────────────────────────

func TestIndexKeywordSearch(t *testing.T) {
	home := sandbox(t)
	write(t, filepath.Join(home, "Desktop", "thesis-figures.pdf"), "figure 3.1 seismic", 0)
	write(t, filepath.Join(home, "Desktop", "Screenshot today.png"), "png", 0)
	quiet(t, func() { cmdIndex(nil) })

	hits := searchIndex("thesis figures", 5)
	if len(hits) == 0 {
		t.Fatal("keyword search found nothing")
	}
	if !strings.Contains(hits[0].Path, "thesis-figures.pdf") {
		t.Errorf("expected thesis-figures.pdf first, got %s", hits[0].Path)
	}
}

// ── collision-safe moves ────────────────────────────────────────────────────

func TestCollisionSafeMove(t *testing.T) {
	home := sandbox(t)
	src := filepath.Join(home, "Downloads", "dup-name.txt")
	write(t, src, "new", 0)
	existing := filepath.Join(home, "Documents", "02-Resources", "Inbox", "Documents", "dup-name.txt")
	write(t, existing, "old", 0)

	plan := newPlan("plan")
	plan.Stage(Action{Kind: "MOVE", Src: src, Dst: existing, Reason: "t"})
	plan.Save()
	withStdin(t, "y\n", func() { quiet(t, func() { approvePlan(plan) }) })

	if got, _ := os.ReadFile(existing); string(got) != "old" {
		t.Error("existing file was overwritten — SAFETY VIOLATION")
	}
	if !fileExists(strings.TrimSuffix(existing, ".txt") + ".pmax1.txt") {
		t.Error("collision suffix missing")
	}
}

// ── projects classification ─────────────────────────────────────────────────

func TestProjectsReportAndProtectedDirs(t *testing.T) {
	home := sandbox(t)
	os.MkdirAll(filepath.Join(home, "stockpro-v2", ".git"), 0o755)
	os.MkdirAll(filepath.Join(home, "numa-design-w1"), 0o755) // empty -> EMPTY
	os.MkdirAll(filepath.Join(home, "Documents"), 0o755)      // protected
	if len(homeStrays()) != 2 {
		t.Fatalf("expected 2 strays, got %v", homeStrays())
	}
	if classifyDir(filepath.Join(home, "stockpro-v2")) != "GIT" {
		t.Error("git dir misclassified")
	}
	if c := classifyDir(filepath.Join(home, "numa-design-w1")); c != "EMPTY" {
		t.Errorf("empty dir classified %s", c)
	}
}

// ── clutter score ───────────────────────────────────────────────────────────

func TestClutterScoreMonotonic(t *testing.T) {
	clean := zoneStats{}
	dirty := zoneStats{DeskShots: 86, DeskFiles: 90, DLStale: 242, DLPartial: 12, DLDupName: 85, HomeStrayDirs: 104}
	if clean.clutterScore() != 0 {
		t.Error("clean home scored > 0")
	}
	if dirty.clutterScore() < 8 {
		t.Errorf("the surveyed mess should score high, got %d", dirty.clutterScore())
	}
}

// Symlinks (e.g. Desktop shortcuts into Google Drive) must never be staged.
func TestSymlinksNeverStaged(t *testing.T) {
	home := sandbox(t)
	write(t, filepath.Join(home, "Desktop", "real.txt"), "x", 0)
	// cloud-style symlink target
	os.Symlink(filepath.Join(home, "Documents"), filepath.Join(home, "Desktop", "CloudShortcut"))
	plan := newPlan("plan")
	stageZoneOrganize(plan, filepath.Join(home, "Desktop"), "desktop")
	for _, a := range plan.Actions {
		if strings.Contains(a.Src, "CloudShortcut") {
			t.Fatal("symlink staged for organize — SAFETY VIOLATION")
		}
	}
	if plan.Count() != 1 {
		t.Fatalf("expected only the real file staged, got %d", plan.Count())
	}
	// and not in sweep either
	swp := newPlan("plan")
	stageZoneSweep(swp, filepath.Join(home, "Desktop"), "desktop")
	for _, a := range swp.Actions {
		if strings.Contains(a.Src, "CloudShortcut") {
			t.Fatal("symlink staged for sweep — SAFETY VIOLATION")
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
