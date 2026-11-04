package main

// Shared ~/ai (OpenAI-compatible) backend: routing, graceful degradation, and
// index rebuild when the embedding model changes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeLocalAI serves the three endpoints pmax uses. embedDim sets vector size.
func fakeLocalAI(t *testing.T, embedDim int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]string{"content": "model=" + req.Model}}}})
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		data := []map[string]any{}
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": make([]float32, embedDim)})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestLocalAIChatAndEmbed(t *testing.T) {
	sandbox(t)
	ts := fakeLocalAI(t, 4)
	Cfg.AIBackend, Cfg.LocalAIURL = "auto", ts.URL

	if !aiUp() {
		t.Fatal("aiUp should be true when localai answers")
	}
	out, err := chat([]chatMessage{{Role: "user", Content: "hi"}}, 10)
	if err != nil || out != "model=fast" {
		t.Errorf("chat = %q, %v", out, err)
	}
	vecs, err := embedTexts([]string{"a", "b"})
	if err != nil || len(vecs) != 2 || len(vecs[0]) != 4 {
		t.Errorf("embed = %v, %v", vecs, err)
	}
}

func TestLocalAIFallbackToNoAI(t *testing.T) {
	sandbox(t)
	Cfg.AIBackend = "auto"
	Cfg.LocalAIURL = "http://127.0.0.1:1" // nothing listening
	Cfg.AIPort = 1                        // sidecar down too
	if aiUp() {
		t.Error("no backend reachable: aiUp must be false")
	}
	if _, err := embedTexts([]string{"x"}); err == nil {
		t.Error("embed should error, not panic, with no backend")
	}
}

func TestAIBackendOffIgnoresRunningServer(t *testing.T) {
	sandbox(t)
	ts := fakeLocalAI(t, 4)
	Cfg.AIBackend, Cfg.LocalAIURL = "off", ts.URL
	if aiUp() {
		t.Error("AI_BACKEND=off must disable AI even if a server is up")
	}
}

func TestIndexRebuildsOnModelChange(t *testing.T) {
	home := sandbox(t)
	ts := fakeLocalAI(t, 2)
	Cfg.AIBackend, Cfg.LocalAIURL = "auto", ts.URL
	root := filepath.Join(home, "docs")
	write(t, filepath.Join(root, "note.txt"), "hello", 0)
	Cfg.IndexRoots = []string{root}

	// Pretend a previous index was built with the old 3-dim sidecar model.
	old := map[string]*indexRecord{}
	info, _ := os.Stat(filepath.Join(root, "note.txt"))
	p := filepath.Join(root, "note.txt")
	old[p] = &indexRecord{Path: p, MtimeNS: info.ModTime().UnixNano(), Size: info.Size(),
		Excerpt: "hello", Vec: []float32{1, 2, 3}}
	if err := saveIndex(old); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(CfgState, "index.model"), []byte("sidecar:old-model"), 0o644)

	quiet(t, func() { cmdIndex(nil) })

	got := loadIndex()[p]
	if got == nil || len(got.Vec) != 2 {
		t.Fatalf("unchanged file must be re-embedded after model change, vec=%v", got)
	}
	tag, _ := os.ReadFile(filepath.Join(CfgState, "index.model"))
	if string(tag) != embedTag() {
		t.Errorf("tag not updated: %q", tag)
	}
}

func TestNonLocalAIServerOn8080IsNotTreatedAsLocalAI(t *testing.T) {
	sandbox(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>some dev server</html>")) // 200, but not an OpenAI-style /v1/models
	}))
	defer ts.Close()
	Cfg.AIBackend, Cfg.LocalAIURL, Cfg.AIPort = "auto", ts.URL, 1
	if useLocalAI() || aiUp() {
		t.Error("an unrelated server answering 200 must not receive file excerpts")
	}
}

// brokenEmbedAI: /v1/models looks fine, but embeddings fail or come back short.
func brokenEmbedAI(t *testing.T, shortReply bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) })
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !shortReply {
			http.Error(w, "no such model", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float32{1, 2}}}})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func seedOldIndex(t *testing.T, home string, names ...string) map[string]string {
	t.Helper()
	root := filepath.Join(home, "docs")
	Cfg.IndexRoots = []string{root}
	old := map[string]*indexRecord{}
	paths := map[string]string{}
	for _, n := range names {
		p := filepath.Join(root, n)
		write(t, p, "hello "+n, 0)
		info, _ := os.Stat(p)
		old[p] = &indexRecord{Path: p, MtimeNS: info.ModTime().UnixNano(), Size: info.Size(),
			Excerpt: "hello", Vec: []float32{1, 2, 3}}
		paths[n] = p
	}
	if err := saveIndex(old); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(CfgState, "index.model"), []byte("sidecar:old-model"), 0o644)
	return paths
}

func TestFailedReembedKeepsOldVectorsAndTag(t *testing.T) {
	home := sandbox(t)
	ts := brokenEmbedAI(t, false)
	Cfg.AIBackend, Cfg.LocalAIURL = "auto", ts.URL
	paths := seedOldIndex(t, home, "a.txt")
	quiet(t, func() { cmdIndex(nil) })
	rec := loadIndex()[paths["a.txt"]]
	if rec == nil || len(rec.Vec) != 3 {
		t.Fatalf("working vectors must survive a failed re-embed, got %+v", rec)
	}
	if tag, _ := os.ReadFile(filepath.Join(CfgState, "index.model")); string(tag) != "sidecar:old-model" {
		t.Errorf("tag must not advance when re-embedding failed: %q", tag)
	}
}

func TestPartialReembedDoesNotAdvanceTag(t *testing.T) {
	home := sandbox(t)
	ts := brokenEmbedAI(t, true) // returns 1 vector for 2 inputs
	Cfg.AIBackend, Cfg.LocalAIURL = "auto", ts.URL
	seedOldIndex(t, home, "a.txt", "b.txt")
	quiet(t, func() { cmdIndex(nil) })
	if tag, _ := os.ReadFile(filepath.Join(CfgState, "index.model")); string(tag) != "sidecar:old-model" {
		t.Errorf("tag advanced although some records still have old-model vectors: %q", tag)
	}
}
