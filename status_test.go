package main

// Tests for the hub-facing surface: status/pending JSON and the one-row
// approver (approve-one / reject-one). Safety properties first.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stagedTwo stages two DELETE rows in one plan and returns the plan + files.
func stagedTwo(t *testing.T, home string) (*Plan, string, string) {
	t.Helper()
	a := filepath.Join(home, "Downloads", "a.tmp")
	b := filepath.Join(home, "Downloads", "b.tmp")
	write(t, a, "A", 40)
	write(t, b, "B", 40)
	p := newPlan("plan")
	p.Stage(Action{Kind: "DELETE", Src: a, Reason: "test"})
	p.Stage(Action{Kind: "DELETE", Src: b, Reason: "test"})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	return p, a, b
}

func TestApproveOneExecutesExactlyOne(t *testing.T) {
	home := sandbox(t)
	p, a, b := stagedTwo(t, home)
	id := rowID(p.Path, p.Actions[0])

	quiet(t, func() {
		if rc := cmdApproveOne([]string{filepath.Base(p.Path), id}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if fileExists(a) {
		t.Error("approved file should have been quarantined")
	}
	if !fileExists(b) {
		t.Error("untouched row's file must remain")
	}
	left := loadPlan(p.Path)
	if left == nil || left.Count() != 1 || left.Actions[0].Src != b {
		t.Errorf("exactly the other row should stay staged: %+v", left)
	}
}

func TestApproveOneRejectsStaleID(t *testing.T) {
	home := sandbox(t)
	p, a, b := stagedTwo(t, home)
	quiet(t, func() {
		if rc := cmdApproveOne([]string{filepath.Base(p.Path), "deadbeefdeadbeef"}); rc == 0 {
			t.Error("bogus row id must fail")
		}
	})
	if !fileExists(a) || !fileExists(b) {
		t.Error("nothing may move on a bad id")
	}
}

func TestApproveOneRefusesPathTraversal(t *testing.T) {
	home := sandbox(t)
	p, a, _ := stagedTwo(t, home)
	id := rowID(p.Path, p.Actions[0])
	quiet(t, func() {
		if rc := cmdApproveOne([]string{"../staged/" + filepath.Base(p.Path), id}); rc == 0 {
			t.Error("plan arg must be a bare staged plan name")
		}
	})
	if !fileExists(a) {
		t.Error("file moved despite invalid plan arg")
	}
}

func TestApproveOneHonorsStageOnly(t *testing.T) {
	home := sandbox(t)
	StageOnly = true
	p, a, _ := stagedTwo(t, home)
	id := rowID(p.Path, p.Actions[0])
	quiet(t, func() {
		if rc := cmdApproveOne([]string{filepath.Base(p.Path), id}); rc == 0 {
			t.Error("stage-only must refuse")
		}
	})
	if !fileExists(a) {
		t.Error("stage-only mutated a file")
	}
}

func TestApproveOneUndoable(t *testing.T) {
	home := sandbox(t)
	p, a, _ := stagedTwo(t, home)
	id := rowID(p.Path, p.Actions[0])
	quiet(t, func() {
		cmdApproveOne([]string{filepath.Base(p.Path), id})
		cmdUndo(nil)
	})
	if b, err := os.ReadFile(a); err != nil || string(b) != "A" {
		t.Errorf("undo should restore byte-for-byte, got %q err=%v", b, err)
	}
}

func TestRejectOneTouchesNoFile(t *testing.T) {
	home := sandbox(t)
	p, a, b := stagedTwo(t, home)
	id := rowID(p.Path, p.Actions[0])
	quiet(t, func() {
		if rc := cmdRejectOne([]string{filepath.Base(p.Path), id}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !fileExists(a) || !fileExists(b) {
		t.Error("reject must not touch files")
	}
	if left := loadPlan(p.Path); left == nil || left.Count() != 1 {
		t.Errorf("rejected row should be dropped from the plan: %+v", left)
	}
}

func TestRowIDStableAndDistinct(t *testing.T) {
	a := Action{Kind: "MOVE", Src: "/x/1", Dst: "/y/1"}
	b := Action{Kind: "MOVE", Src: "/x/2", Dst: "/y/2"}
	if rowID("/s/plan-1.tsv", a) != rowID("/s/plan-1.tsv", a) {
		t.Error("id must be deterministic")
	}
	if rowID("/s/plan-1.tsv", a) == rowID("/s/plan-1.tsv", b) {
		t.Error("distinct rows need distinct ids")
	}
}

func TestPendingJSONShape(t *testing.T) {
	home := sandbox(t)
	p, _, _ := stagedTwo(t, home)
	var out string
	out = captureStdout(t, func() { cmdPendingJSON() })
	var got struct {
		Plans []struct {
			Plan string `json:"plan"`
			Rows []struct {
				ID, Kind, Src string
			} `json:"rows"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Plans) != 1 || got.Plans[0].Plan != filepath.Base(p.Path) || len(got.Plans[0].Rows) != 2 {
		t.Errorf("unexpected shape: %s", out)
	}
	if got.Plans[0].Rows[0].ID != rowID(p.Path, p.Actions[0]) {
		t.Error("row id must match rowID()")
	}
}

func TestStatusJSONShape(t *testing.T) {
	home := sandbox(t)
	stagedTwo(t, home)
	out := captureStdout(t, func() { cmdStatus([]string{"--json"}) })
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	for _, k := range []string{"version", "clutter_score", "pending_actions", "quarantined_files", "ai", "last_run"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q in %s", k, out)
		}
	}
	if got["pending_actions"].(float64) != 2 {
		t.Errorf("pending_actions = %v", got["pending_actions"])
	}
}

// captureStdout returns what fn printed (unlike quiet, which discards it).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { s, _ := ioCopy(r); done <- s }()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestApproveOneRefusesFileThatChangedSinceStaging(t *testing.T) {
	home := sandbox(t)
	p, a, _ := stagedTwo(t, home)
	id := rowID(p.Path, p.Actions[0])
	// A different file now occupies the staged name.
	write(t, a, "a completely different, much longer file", 0)
	quiet(t, func() {
		if rc := cmdApproveOne([]string{filepath.Base(p.Path), id}); rc == 0 {
			t.Error("must refuse when the file no longer matches what was staged")
		}
	})
	if b, _ := os.ReadFile(a); !strings.Contains(string(b), "different") {
		t.Error("the replacement file must be untouched")
	}
}

func TestConcurrentDecisionsOnOnePlanDoNotLoseUpdates(t *testing.T) {
	home := sandbox(t)
	p, a, b := stagedTwo(t, home)
	ids := []string{rowID(p.Path, p.Actions[0]), rowID(p.Path, p.Actions[1])}
	name := filepath.Base(p.Path)
	done := make(chan struct{}, 2)
	quiet(t, func() {
		for _, id := range ids {
			go func(id string) { cmdApproveOne([]string{name, id}); done <- struct{}{} }(id)
		}
		<-done
		<-done
	})
	if fileExists(a) || fileExists(b) {
		t.Error("both approved files should be quarantined")
	}
	if left := loadPlan(p.Path); left != nil && left.Count() != 0 {
		t.Errorf("a decided row came back from the dead: %+v", left.Actions)
	}
}
