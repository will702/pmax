package main

// ai: MLX sidecar lifecycle (uv venv + resident python server), HTTP clients
// for /embed and /chat, and the laya typed-decision client (subprocess).
// Everything degrades gracefully: no sidecar → keyword mode; no laya → skip.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func venvPython() string {
	p := filepath.Join(Cfg.AIVenv, "bin", "python")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// sidecarDir: installed copy first ($STATE/sidecar), repo checkout second.
func sidecarDir() string {
	for _, d := range []string{
		filepath.Join(CfgState, "sidecar"),
		filepath.Join(repoRoot(), "sidecar"),
	} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return ""
}

// repoRoot climbs from the executable looking for go.mod (dev fallback).
func repoRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	// If invoked via symlink in ~/.local/bin, resolve it.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "."
}

func aiURL(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", Cfg.AIPort, path)
}

func aiUp() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(aiURL("/health"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// ── HTTP clients ────────────────────────────────────────────────────────────

func embedTexts(texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"texts": texts})
	resp, err := http.Post(aiURL("/embed"), "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Dim        int         `json:"dim"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s", out.Error)
	}
	return out.Embeddings, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func chat(messages []chatMessage, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"messages": messages, "max_tokens": maxTokens, "temperature": 0.2,
	})
	resp, err := http.Post(aiURL("/chat"), "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Text, nil
}

// ── laya typed decisions (subprocess, batched JSONL) ───────────────────────

var layaCache *bool

func layaReady() bool {
	if Cfg.LayaEnabled == "false" {
		return false
	}
	if layaCache != nil {
		return *layaCache
	}
	ready := false
	if py := venvPython(); py != "" && sidecarDir() != "" {
		cmd := exec.Command(py, "-c", "import laya")
		if err := cmd.Run(); err == nil {
			ready = true
		}
	}
	layaCache = &ready
	return ready
}

type layaResult struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	PTrue      float64 `json:"p"`
	Confidence float64 `json:"confidence"`
	Abstained  bool    `json:"abstained"`
}

// layaRun feeds {"id","text"} JSONL to laya.py and decodes JSONL results.
func layaRun(mode, instruction, labelsJSON string, items []layaItem) ([]layaResult, error) {
	py := venvPython()
	sc := sidecarDir()
	if py == "" || sc == "" {
		return nil, fmt.Errorf("sidecar not installed")
	}
	var in bytes.Buffer
	for _, it := range items {
		b, _ := json.Marshal(it)
		in.Write(b)
		in.WriteByte('\n')
	}
	args := []string{filepath.Join(sc, "laya.py"), mode, "--instructions", instruction}
	if labelsJSON != "" {
		args = append(args, "--labels", labelsJSON)
	}
	if Cfg.LayaMinConfidence > 0 {
		args = append(args, "--min-confidence", fmt.Sprint(Cfg.LayaMinConfidence))
	}
	cmd := exec.Command(py, args...)
	cmd.Stdin = &in
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("laya: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	var results []layaResult
	for _, line := range splitLines(out.String()) {
		var r layaResult
		if json.Unmarshal([]byte(line), &r) == nil {
			results = append(results, r)
		}
	}
	return results, nil
}

type layaItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// smartClassify: laya choice over "Other"-bucket files; confident labels move
// into Inbox/<label>/; low-confidence or "Unsorted" stay flagged for review.
func smartClassify(p *Plan) {
	if !layaReady() {
		fmt.Println("  (smart mode: laya not available — skipping AI classification)")
		return
	}
	var items []layaItem
	otherIdx := map[string]int{}
	for i, a := range p.Actions {
		if a.Kind == "MOVE" && strings.HasSuffix(filepath.Dir(a.Dst), "/Other") {
			id := fmt.Sprintf("%d", i)
			items = append(items, layaItem{ID: id, Text: fileTextForAI(a.Src)})
			otherIdx[id] = i
		}
	}
	if len(items) == 0 {
		return
	}
	criteria := map[string]string{}
	for _, l := range Cfg.LayaTypeLabels {
		criteria[l] = "files that belong to " + l
	}
	labelsJSON, _ := json.Marshal(criteria)
	res, err := layaRun("classify",
		"Which category does this file belong to? Judge by filename and content excerpt.",
		string(labelsJSON), items)
	if err != nil {
		fmt.Printf("  (smart mode failed: %v — keeping rule-based destinations)\n", err)
		return
	}
	for _, r := range res {
		i, ok := otherIdx[r.ID]
		if !ok {
			continue
		}
		a := p.Actions[i]
		if r.Abstained || r.Label == "" || r.Label == "Unsorted" {
			p.Actions[i].Flag = "review"
			p.Actions[i].Reason = fmt.Sprintf("smart: ambiguous (%.0f%%) — review", r.Confidence*100)
			continue
		}
		p.Actions[i].Dst = filepath.Join(Cfg.DestInbox, r.Label, filepath.Base(a.Src))
		p.Actions[i].Reason = fmt.Sprintf("smart/laya %s (%.0f%%)", r.Label, r.Confidence*100)
	}
	fmt.Printf("  (smart mode: %d file(s) classified by laya)\n", len(res))
}

// orderFlaggedByJunkProb sorts flagged actions by P(junk), most-junk first.
// Ordering only — every decision stays with the human.
func orderFlaggedByJunkProb(p *Plan) {
	if !layaReady() || p.Count() == 0 {
		return
	}
	prob := map[string]float64{}
	var items []layaItem
	for _, a := range p.Actions {
		if a.Flag == "review" {
			items = append(items, layaItem{ID: a.Src, Text: fileTextForAI(a.Src)})
		}
	}
	if len(items) == 0 {
		return
	}
	res, err := layaRun("noul",
		"Does this file look like junk, a duplicate, or temporary clutter that is safe to discard?",
		"", items)
	if err != nil {
		return
	}
	for _, r := range res {
		prob[r.ID] = r.PTrue
	}
	// Annotate reasons with the probability, then order: flagged (junk-desc) first.
	for i := range p.Actions {
		if pr, ok := prob[p.Actions[i].Src]; ok && p.Actions[i].Flag == "review" {
			p.Actions[i].Reason += fmt.Sprintf(" [P(junk)=%.0f%%]", pr*100)
		}
	}
	var front, rest []Action
	for _, a := range p.Actions {
		if a.Flag == "review" {
			front = append(front, a)
		} else {
			rest = append(rest, a)
		}
	}
	for i := 1; i < len(front); i++ {
		for j := i; j > 0; j-- {
			if prob[front[j].Src] > prob[front[j-1].Src] {
				front[j-1], front[j] = front[j], front[j-1]
			} else {
				break
			}
		}
	}
	p.Actions = append(front, rest...)
}

// fileTextForAI builds the text laya/embedding see for a file: name + excerpt.
func fileTextForAI(path string) string {
	name := filepath.Base(path)
	if data, err := os.ReadFile(path); err == nil {
		excerpt := strings.TrimSpace(string(data))
		if len(excerpt) > 1200 {
			excerpt = excerpt[:1200]
		}
		if isPrintable(excerpt) {
			return name + "\n" + excerpt
		}
	}
	return name
}

func isPrintable(s string) bool {
	controls := 0
	for _, r := range s {
		if r < 9 || (r > 13 && r < 32) {
			controls++
		}
	}
	return controls*10 < len(s) // tolerate ~10% binary noise
}

// ── `pmax ai` command ───────────────────────────────────────────────────────

func cmdAI(args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "setup":
		return aiSetup()
	case "start":
		return aiStart(true)
	case "ensure":
		if aiUp() {
			return 0
		}
		return aiStart(false)
	case "stop":
		return aiStop()
	case "status":
		if aiUp() {
			resp, err := http.Get(aiURL("/health"))
			if err == nil {
				defer resp.Body.Close()
				var h struct {
					UptimeS    int    `json:"uptime_s"`
					Embed      bool   `json:"embed"`
					Chat       bool   `json:"chat"`
					EmbedModel string `json:"embed_model"`
					ChatModel  string `json:"chat_model"`
				}
				json.NewDecoder(resp.Body).Decode(&h)
				fmt.Printf("MLX sidecar UP on :%d (uptime %ds)\n", Cfg.AIPort, h.UptimeS)
				fmt.Printf("  embed: %v (%s)\n", h.Embed, h.EmbedModel)
				fmt.Printf("  chat:  %v (%s)\n", h.Chat, h.ChatModel)
			}
		} else if venvPython() != "" {
			fmt.Println("MLX sidecar DOWN (venv present) — pmax ai start")
		} else {
			fmt.Println("MLX sidecar NOT SET UP — pmax ai setup")
		}
		if layaReady() {
			fmt.Println("laya: ready (typed decisions)")
		} else {
			fmt.Printf("laya: %s\n", Cfg.LayaEnabled)
		}
		return 0
	default:
		die("usage: pmax ai [setup|start|stop|status|ensure]")
		return 1
	}
}

func aiSetup() int {
	// 1) uv venv
	if _, err := exec.LookPath("uv"); err != nil {
		die("uv not found — brew install uv")
	}
	fmt.Println("→ creating venv at", Cfg.AIVenv)
	if out, err := exec.Command("uv", "venv", "--allow-existing", Cfg.AIVenv).CombinedOutput(); err != nil {
		die("uv venv failed: %v\n%s", err, out)
	}
	// 2) MLX + laya packages
	fmt.Println("→ installing mlx, mlx-lm, mlx-embeddings, transformers, laya (this downloads ~2 GB of models on first load)")
	if out, err := exec.Command("uv", "pip", "install", "--python", venvPython(),
		"mlx", "mlx-lm", "mlx-embeddings", "transformers", "laya").CombinedOutput(); err != nil {
		die("uv pip install failed: %v\n%s", err, out)
	}
	// 3) install sidecar scripts into state
	if sc := sidecarDir(); sc != "" && sc != filepath.Join(CfgState, "sidecar") {
		os.MkdirAll(filepath.Join(CfgState, "sidecar"), 0o755)
		for _, f := range []string{"ai_server.py", "laya.py"} {
			data, err := os.ReadFile(filepath.Join(sc, f))
			if err == nil {
				os.WriteFile(filepath.Join(CfgState, "sidecar", f), data, 0o755)
			}
		}
		fmt.Println("→ sidecar scripts installed to", filepath.Join(CfgState, "sidecar"))
	}
	// 4) warm start (downloads models into HF cache)
	fmt.Println("→ starting server and pre-loading models (first run downloads weights)…")
	aiStart(true)
	fmt.Println("✓ MLX sidecar ready. Try: pmax index && colgrep \"thesis figures\"")
	fmt.Println("  laya typed decisions:", map[bool]string{true: "ready", false: "unavailable (organize --smart degrades gracefully)"}[layaReady()])
	return 0
}

func aiStart(verbose bool) int {
	if aiUp() {
		if verbose {
			fmt.Println("MLX sidecar already running.")
		}
		return 0
	}
	sc := sidecarDir()
	if sc == "" {
		die("sidecar scripts not found — run pmax ai setup")
	}
	py := venvPython()
	if py == "" {
		die("no venv — run pmax ai setup")
	}
	logFile, _ := os.OpenFile(filepath.Join(CfgState, "logs", "ai-server.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	cmd := exec.Command(py, filepath.Join(sc, "ai_server.py"),
		"--port", fmt.Sprint(Cfg.AIPort), "--preload")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		die("cannot start sidecar: %v", err)
	}
	os.WriteFile(filepath.Join(CfgState, "run", "ai.pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o644)
	// wait for health (model load can take a while on first run)
	if verbose {
		fmt.Print("waiting for model load")
	}
	for i := 0; i < 120; i++ {
		time.Sleep(1 * time.Second)
		if verbose && i%5 == 0 {
			fmt.Print(".")
		}
		if aiUp() {
			if verbose {
				fmt.Println(" up")
			}
			logf("INFO", "ai sidecar started (pid %d)", cmd.Process.Pid)
			return 0
		}
	}
	if verbose {
		fmt.Println(" timeout (still loading? check logs/ai-server.log)")
	}
	return 0
}

func aiStop() int {
	pidFile := filepath.Join(CfgState, "run", "ai.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Println("sidecar not running (no pid file).")
		return 0
	}
	if err := exec.Command("kill", strings.TrimSpace(string(data))).Run(); err != nil {
		fmt.Println("could not stop:", err)
		return 1
	}
	os.Remove(pidFile)
	fmt.Println("MLX sidecar stopped.")
	return 0
}

// ── `pmax ask` ──────────────────────────────────────────────────────────────

func cmdAsk(args []string) int {
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		die(`usage: pmax ask "where did I put my thesis figures?"`)
	}
	question := strings.Join(args, " ")
	hits := searchIndex(question, 8)
	if len(hits) == 0 {
		fmt.Println("No indexed files match. Try: pmax index")
		return 0
	}
	var ctx strings.Builder
	for i, h := range hits {
		fmt.Printf("  %d. %5.1f%%  %s\n", i+1, h.Score*100, h.Path)
		ctx.WriteString(h.Path + "\n")
	}
	if aiUp() {
		answer, err := chat([]chatMessage{
			{Role: "system", Content: "You are a concise file-locator assistant. Answer in 1-2 short sentences, citing the most likely paths."},
			{Role: "user", Content: "Question: " + question + "\n\nCandidate files:\n" + ctx.String()},
		}, 200)
		if err == nil && answer != "" {
			fmt.Println("\n" + answer)
		}
	} else {
		fmt.Println("\n(sidecar down — showing keyword hits only)")
	}
	return 0
}
