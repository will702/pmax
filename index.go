package main

// colgrep index: filename + text excerpts + embeddings in one compact binary
// file (~$STATE/index.bin). Incremental by (path, mtime, size). Falls back to
// keyword search when the MLX sidecar is down — colgrep always answers.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	indexMagic   = "PMAXIDX"
	indexVersion = 1
	embedBatch   = 32
	maxFileScan  = 20 << 20 // skip contents above 20 MB (name still indexed)
	excerptLen   = 1200
)

type indexRecord struct {
	Path    string
	MtimeNS int64
	Size    int64
	Excerpt string
	Vec     []float32 // nil = not embedded (sidecar was down at build time)
}

type searchHit struct {
	Path  string
	Score float64 // 0..1-ish
}

func indexPath() string { return filepath.Join(CfgState, "index.bin") }

// ── binary format ───────────────────────────────────────────────────────────

func loadIndex() map[string]*indexRecord {
	out := map[string]*indexRecord{}
	f, err := os.Open(indexPath())
	if err != nil {
		return out
	}
	defer f.Close()
	var magic [7]byte
	if _, err := f.Read(magic[:]); err != nil || string(magic[:]) != indexMagic {
		return out
	}
	var ver, dim, count uint32
	binary.Read(f, binary.LittleEndian, &ver)
	if ver != indexVersion {
		return out
	}
	binary.Read(f, binary.LittleEndian, &dim)
	binary.Read(f, binary.LittleEndian, &count)
	readStr := func() string {
		var n uint32
		binary.Read(f, binary.LittleEndian, &n)
		b := make([]byte, n)
		f.Read(b)
		return string(b)
	}
	for i := uint32(0); i < count; i++ {
		rec := &indexRecord{}
		rec.Path = readStr()
		binary.Read(f, binary.LittleEndian, &rec.MtimeNS)
		binary.Read(f, binary.LittleEndian, &rec.Size)
		rec.Excerpt = readStr()
		var hasVec uint8
		binary.Read(f, binary.LittleEndian, &hasVec)
		if hasVec == 1 {
			vec := make([]float32, dim)
			binary.Read(f, binary.LittleEndian, vec)
			rec.Vec = vec
		}
		out[rec.Path] = rec
	}
	return out
}

func saveIndex(records map[string]*indexRecord) error {
	var buf bytes.Buffer
	buf.WriteString(indexMagic)
	dim := uint32(0)
	for _, r := range records {
		if len(r.Vec) > 0 {
			dim = uint32(len(r.Vec))
			break
		}
	}
	binary.Write(&buf, binary.LittleEndian, uint32(indexVersion))
	binary.Write(&buf, binary.LittleEndian, dim)
	binary.Write(&buf, binary.LittleEndian, uint32(len(records)))
	writeStr := func(s string) {
		binary.Write(&buf, binary.LittleEndian, uint32(len(s)))
		buf.WriteString(s)
	}
	for _, r := range records {
		writeStr(r.Path)
		binary.Write(&buf, binary.LittleEndian, r.MtimeNS)
		binary.Write(&buf, binary.LittleEndian, r.Size)
		writeStr(r.Excerpt)
		if len(r.Vec) > 0 {
			binary.Write(&buf, binary.LittleEndian, uint8(1))
			binary.Write(&buf, binary.LittleEndian, r.Vec)
		} else {
			binary.Write(&buf, binary.LittleEndian, uint8(0))
		}
	}
	tmp := indexPath() + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath())
}

// ── text extraction ─────────────────────────────────────────────────────────

var skipDirNames = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true,
	"venv": true, "Library": true, ".Trash": true, ".cache": true,
}

func pdftotextBin() string {
	for _, c := range []string{"/opt/homebrew/bin/pdftotext", "/usr/local/bin/pdftotext"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("pdftotext"); err == nil {
		return p
	}
	return ""
}

func excerptFor(path string) string {
	st, err := os.Stat(path)
	if err != nil || st.Size() > maxFileScan {
		return ""
	}
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		if bin := pdftotextBin(); bin != "" {
			out, err := exec.Command(bin, "-q", "-l", "3", path, "-").Output()
			if err == nil {
				s := strings.TrimSpace(string(out))
				if len(s) > excerptLen {
					s = s[:excerptLen]
				}
				return s
			}
		}
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if !isPrintable(s) {
		return ""
	}
	if len(s) > excerptLen {
		s = s[:excerptLen]
	}
	return s
}

// ── build ───────────────────────────────────────────────────────────────────

func cmdIndex(args []string) int {
	full := len(args) > 0 && args[0] == "--full"
	records := loadIndex()
	if full {
		records = map[string]*indexRecord{}
	}

	seen := map[string]bool{}
	var changed []*indexRecord
	for _, root := range Cfg.IndexRoots {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if skipDirNames[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != root) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			seen[path] = true
			if old, ok := records[path]; ok && old.MtimeNS == info.ModTime().UnixNano() && old.Size == info.Size() {
				return nil // unchanged
			}
			rec := &indexRecord{
				Path: path, MtimeNS: info.ModTime().UnixNano(), Size: info.Size(),
				Excerpt: excerptFor(path),
			}
			records[path] = rec
			changed = append(changed, rec)
			return nil
		})
	}
	// Drop vanished paths.
	for p := range records {
		if !seen[p] {
			delete(records, p)
		}
	}

	// Embed changed records in batches (skip silently when sidecar is down).
	embedded, skippedVec := 0, 0
	if len(changed) > 0 && aiUp() {
		for i := 0; i < len(changed); i += embedBatch {
			end := i + embedBatch
			if end > len(changed) {
				end = len(changed)
			}
			batch := changed[i:end]
			texts := make([]string, len(batch))
			for j, r := range batch {
				texts[j] = filepath.Base(r.Path) + "\n" + r.Excerpt
			}
			vecs, err := embedTexts(texts)
			if err != nil {
				skippedVec += len(batch)
				continue
			}
			for j, r := range batch {
				if j < len(vecs) {
					r.Vec = vecs[j]
					embedded++
				}
			}
		}
	} else if len(changed) > 0 {
		skippedVec = len(changed)
	}

	if err := saveIndex(records); err != nil {
		die("cannot write index: %v", err)
	}
	fmt.Printf("Index: %d file(s) (%d changed, %d embedded", len(records), len(changed), embedded)
	if skippedVec > 0 {
		fmt.Printf(", %d without vectors — sidecar down, keyword mode", skippedVec)
	}
	fmt.Println(")")
	if len(changed) > 0 && skippedVec == len(changed) && !aiUp() {
		fmt.Println("Tip: pmax ai ensure && pmax index  → full semantic mode")
	}
	return 0
}

// ── search ──────────────────────────────────────────────────────────────────

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// searchIndex: semantic when possible, keyword otherwise; blends both.
func searchIndex(query string, topK int) []searchHit {
	records := loadIndex()
	if len(records) == 0 {
		return nil
	}
	type scored struct {
		path         string
		score        float64
		hasVec       bool
	}
	var list []scored

	var qvec []float32
	semantic := aiUp()
	if semantic {
		if v, err := embedTexts([]string{query}); err == nil && len(v) > 0 {
			qvec = v[0]
		} else {
			semantic = false
		}
	}

	q := strings.ToLower(query)
	terms := strings.Fields(q)
	for _, r := range records {
		var s float64
		name := strings.ToLower(filepath.Base(r.Path))
		if semantic && len(r.Vec) > 0 && len(qvec) > 0 {
			s = cosine(qvec, r.Vec)
		}
		// keyword blend: name hits weigh more than excerpt hits
		kw := 0.0
		if strings.Contains(name, q) {
			kw += 0.6
		}
		lowEx := strings.ToLower(r.Excerpt)
		for _, t := range terms {
			if strings.Contains(name, t) {
				kw += 0.2
			}
			if lowEx != "" && strings.Contains(lowEx, t) {
				kw += 0.05
			}
		}
		if kw > 1 {
			kw = 1
		}
		score := s
		if !semantic {
			score = kw
		} else {
			score = 0.8*s + 0.2*kw
		}
		if score <= 0.01 {
			continue
		}
		list = append(list, scored{r.Path, score, len(r.Vec) > 0})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	if len(list) > topK {
		list = list[:topK]
	}
	hits := make([]searchHit, 0, len(list))
	for _, l := range list {
		hits = append(hits, searchHit{Path: l.path, Score: l.score})
	}
	return hits
}

// cmdColgrep: `pmax colgrep "query"` (also reached via the `colgrep` symlink).
func cmdColgrep(args []string) int {
	interactive := false
	var qparts []string
	for _, a := range args {
		if a == "-i" || a == "--interactive" {
			interactive = true
		} else {
			qparts = append(qparts, a)
		}
	}
	if len(qparts) == 0 {
		die(`usage: colgrep "natural language query" [-i]`)
	}
	query := strings.Join(qparts, " ")
	hits := searchIndex(query, 20)
	if len(hits) == 0 {
		fmt.Println("No matches. (re)build the index: pmax index")
		return 0
	}
	mode := "semantic"
	if !aiUp() {
		mode = "keyword (degraded — sidecar down)"
	}
	fmt.Printf("colgrep [%s] %q\n", mode, query)
	var lines []string
	for _, h := range hits {
		line := fmt.Sprintf("%6.1f%%  %s", h.Score*100, h.Path)
		fmt.Println(line)
		lines = append(lines, line)
	}
	if interactive {
		if _, err := exec.LookPath("fzf"); err != nil {
			fmt.Println("(fzf not installed — brew install fzf for -i picker)")
			return 0
		}
		cmd := exec.Command("fzf", "--query", query, "--height", "40%")
		var in bytes.Buffer
		in.WriteString(strings.Join(lines, "\n"))
		cmd.Stdin = &in
		if out, err := cmd.Output(); err == nil {
			chosen := strings.TrimSpace(string(out))
			if i := strings.Index(chosen, "  "); i >= 0 {
				path := strings.TrimSpace(chosen[i:])
				exec.Command("open", "-R", path).Run()
			}
		}
	}
	return 0
}
