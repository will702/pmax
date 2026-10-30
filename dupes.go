package main

// dupes: checksum-level duplicate finder. Groups by size, hashes candidates
// (SHA-256, parallel workers), proposes quarantining every copy except the
// newest of each identical set. Approval-gated as always.

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type sizedFile struct {
	path string
	size int64
	mtimeNS int64
	hash  string
}

func cmdDupes(args []string) int {
	dir, minMB := UserHome+"/Downloads", Cfg.DupesMinSizeMB
	for i := 0; i < len(args); i++ {
		if args[i] == "--min-size" && i+1 < len(args) {
			minMB = atoi(args[i+1])
			i++
		} else {
			dir = args[i]
		}
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		die("not a directory: %s", dir)
	}

	// 1) Loose files of at least minMB.
	var cands []sizedFile
	for _, f := range looseFiles(dir) {
		if isProtected(f) {
			continue
		}
		st, err := os.Stat(f)
		if err != nil || st.Size() < int64(minMB)<<20 {
			continue
		}
		cands = append(cands, sizedFile{path: f, size: st.Size(), mtimeNS: st.ModTime().UnixNano()})
	}

	// 2) Keep only sizes that occur more than once.
	bySize := map[int64][]sizedFile{}
	for _, c := range cands {
		bySize[c.size] = append(bySize[c.size], c)
	}
	var toHash []sizedFile
	for _, group := range bySize {
		if len(group) > 1 {
			toHash = append(toHash, group...)
		}
	}
	if len(toHash) == 0 {
		fmt.Printf("No same-size candidates ≥ %d MB in %s. ✓\n", minMB, dir)
		return 0
	}

	// 3) Hash in parallel.
	var wg sync.WaitGroup
	ch := make(chan *sizedFile, len(toHash))
	workers := 4
	if len(toHash) < workers {
		workers = len(toHash)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range ch {
				c.hash = hashFile(c.path)
			}
		}()
	}
	for i := range toHash {
		ch <- &toHash[i]
	}
	close(ch)
	wg.Wait()

	// 4) Group by hash; keep newest of each set, stage the rest.
	byHash := map[string][]sizedFile{}
	for _, c := range toHash {
		if c.hash != "" {
			byHash[c.hash] = append(byHash[c.hash], c)
		}
	}
	plan := newPlan("plan")
	for _, group := range byHash {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].mtimeNS > group[j].mtimeNS })
		keep := group[0]
		for _, dup := range group[1:] {
			plan.Stage(Action{Kind: "DELETE", Src: dup.path,
				Reason: fmt.Sprintf("checksum-dupe of '%s' (identical SHA-256)", filepath.Base(keep.path))})
		}
	}

	if plan.Count() == 0 {
		fmt.Printf("No checksum duplicates ≥ %d MB in %s. ✓\n", minMB, dir)
		return 0
	}
	plan.Save()
	logf("INFO", "dupes staged %d action(s) [%s]", plan.Count(), dir)
	fmt.Printf("Staged %d duplicate-quarantine proposal(s) (newest copy of each set is kept).\n", plan.Count())
	return approvePlan(plan)
}

func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
