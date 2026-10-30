package main

// pmax core: paths, config, logging, action staging, manifests, quarantine,
// learning signals, notifications, and shell-style glob matching.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Version = "1.0.0"

// ── Global state ────────────────────────────────────────────────────────────

var (
	UserHome   string
	CfgHome    string // PMAX_HOME  — config dir  (~/.config/pmax)
	CfgState   string // PMAX_STATE — state dir    (~/.local/state/pmax)
	StageOnly  bool
	Cfg        Config
	currentRun string // manifest run id, set on first executed action
)

// Config holds every tunable. File format: `KEY=value` lines, `#` comments,
// `~` and `$HOME` expanded, optional quotes.
type Config struct {
	StaleDays                int
	QuarantineRetentionDays  int
	DupesMinSizeMB           int
	AllowBatchApprove        bool
	NotifySound              string
	DestInbox                string
	DestArchive              string
	DestInstallers          string
	DestStale                string
	DestScreenshots          string
	DestProjects             string
	DestScatter              string
	Zones                    []string
	KeepList                 []string
	RemindMorningHour        int
	RemindEveningHour        int
	RemindWeeklyDay          int
	RemindWeeklyHour         int
	WatcherHour              int
	AIVenv                   string
	AIPort                   int
	AIEmbedModel             string
	AIChatModel              string
	IndexRoots               []string
	LayaEnabled              string // auto | true | false
	LayaMinConfidence        float64
	LayaTypeLabels           []string
	LearnedMaxRules          int
	LearnedAllowedPrefixes   []string
}

func defaults() Config {
	h := UserHome
	return Config{
		StaleDays:               30,
		QuarantineRetentionDays: 14,
		DupesMinSizeMB:          1,
		AllowBatchApprove:       false,
		NotifySound:             "Glass",
		DestInbox:               h + "/Documents/02-Resources/Inbox",
		DestArchive:             h + "/Documents/03-Archive",
		DestInstallers:         h + "/Documents/03-Archive/Installers",
		DestStale:               h + "/Documents/03-Archive/Downloads-Stale",
		DestScreenshots:         h + "/Pictures/Screenshots",
		DestProjects:            h + "/projects",
		DestScatter:             h + "/Documents/02-Resources/Scatter",
		Zones:                   []string{h + "/Desktop", h + "/Downloads"},
		KeepList: []string{
			h + "/Downloads/productivity-mac-max*",
			h + "/projects*", h + "/Documents/*", h + "/Pictures/*",
			h + "/Music/*", h + "/Movies/*", h + "/Library*", h + "/Applications*",
			h + "/Public*", h + "/Certificates*", h + "/Alif*", h + "/TahuAI*", h + "/.*",
		},
		RemindMorningHour: 9,
		RemindEveningHour: 21,
		RemindWeeklyDay:   0,
		RemindWeeklyHour:  18,
		WatcherHour:       3,
		AIVenv:            h + "/.local/state/pmax/venv",
		AIPort:            8765,
		AIEmbedModel:      "mlx-community/bge-small-en-v1.5",
		AIChatModel:       "mlx-community/Qwen2.5-3B-Instruct-4bit",
		IndexRoots:        []string{h + "/Desktop", h + "/Downloads", h + "/Documents", h + "/projects", h + "/Alif"},
		LayaEnabled:       "auto",
		LayaMinConfidence: 0.90,
		LayaTypeLabels:    []string{"Documents", "Receipts-Invoices", "Certificates", "Tickets", "Forms", "Code-Archive", "Media", "Notes", "Unsorted"},
		LearnedMaxRules:   50,
		LearnedAllowedPrefixes: []string{
			h + "/Documents/", h + "/Pictures/", h + "/Music/", h + "/Movies/",
		},
	}
}

// expand tilde and $HOME inside config values.
func expandHome(s string) string {
	s = strings.ReplaceAll(s, "$HOME", UserHome)
	if strings.HasPrefix(s, "~") {
		s = UserHome + s[1:]
	}
	return s
}

func initCore(stageOnly bool) error {
	h, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	UserHome = h
	CfgHome = envOr("PMAX_HOME", h+"/.config/pmax")
	CfgState = envOr("PMAX_STATE", h+"/.local/state/pmax")
	StageOnly = stageOnly

	Cfg = defaults()
	if err := loadConfigFile(filepath.Join(CfgHome, "pmax.conf")); err != nil {
		return err
	}

	for _, d := range []string{
		CfgHome,
		CfgState,
		CfgState + "/logs",
		CfgState + "/staged",
		CfgState + "/manifests",
		CfgState + "/quarantine",
		CfgState + "/learning",
		CfgState + "/run",
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", d, err)
		}
	}
	return nil
}

func loadConfigFile(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		v = expandHome(v)
		setConfig(k, v)
	}
	return sc.Err()
}

func setConfig(k, v string) {
	switch k {
	case "STALE_DAYS":
		Cfg.StaleDays = atoi(v)
	case "QUARANTINE_RETENTION_DAYS":
		Cfg.QuarantineRetentionDays = atoi(v)
	case "DUPES_MIN_SIZE_MB":
		Cfg.DupesMinSizeMB = atoi(v)
	case "ALLOW_BATCH_APPROVE":
		Cfg.AllowBatchApprove = v == "true"
	case "NOTIFY_SOUND":
		Cfg.NotifySound = v
	case "DEST_INBOX":
		Cfg.DestInbox = v
	case "DEST_ARCHIVE":
		Cfg.DestArchive = v
	case "DEST_INSTALLERS":
		Cfg.DestInstallers = v
	case "DEST_STALE":
		Cfg.DestStale = v
	case "DEST_SCREENSHOTS":
		Cfg.DestScreenshots = v
	case "DEST_PROJECTS":
		Cfg.DestProjects = v
	case "DEST_SCATTER":
		Cfg.DestScatter = v
	case "PMAX_ZONES":
		Cfg.Zones = fields(v)
	case "PMAX_KEEP_LIST":
		Cfg.KeepList = fields(v)
	case "REMIND_MORNING_HOUR":
		Cfg.RemindMorningHour = atoi(v)
	case "REMIND_EVENING_HOUR":
		Cfg.RemindEveningHour = atoi(v)
	case "REMIND_WEEKLY_DAY":
		Cfg.RemindWeeklyDay = atoi(v)
	case "REMIND_WEEKLY_HOUR":
		Cfg.RemindWeeklyHour = atoi(v)
	case "WATCHER_HOUR":
		Cfg.WatcherHour = atoi(v)
	case "AI_VENV":
		Cfg.AIVenv = v
	case "AI_PORT":
		Cfg.AIPort = atoi(v)
	case "AI_EMBED_MODEL":
		Cfg.AIEmbedModel = v
	case "AI_CHAT_MODEL":
		Cfg.AIChatModel = v
	case "INDEX_ROOTS":
		Cfg.IndexRoots = fields(v)
	case "LAYA_ENABLED":
		Cfg.LayaEnabled = v
	case "LAYA_MIN_CONFIDENCE":
		Cfg.LayaMinConfidence = atof(v)
	case "LAYA_TYPE_LABELS":
		Cfg.LayaTypeLabels = fields(v)
	case "LEARNED_MAX_RULES":
		Cfg.LearnedMaxRules = atoi(v)
	case "LEARNED_ALLOWED_PREFIXES":
		Cfg.LearnedAllowedPrefixes = fields(v)
	}
}

func fields(s string) []string {
	return strings.Fields(s)
}
func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// ── Logging ─────────────────────────────────────────────────────────────────

func logf(level, format string, a ...any) {
	f, err := os.OpenFile(CfgState+"/logs/pmax.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), level, fmt.Sprintf(format, a...))
}

func die(format string, a ...any) {
	logf("ERROR", format, a...)
	fmt.Fprintf(os.Stderr, "pmax: "+format+"\n", a...)
	os.Exit(1)
}

// ── Formatting helpers ──────────────────────────────────────────────────────

func fmtSize(b int64) string {
	const kb, mb, gb = 1 << 10, 1 << 20, 1 << 30
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/gb)
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/mb)
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/kb)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func daysOld(path string) int {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(time.Since(st.ModTime()).Hours() / 24)
}

// ── Shell-style glob (unlike filepath.Match, `*` crosses "/") ───────────────

func globMatch(pattern, s string) bool {
	// classic backtracking wildcard matcher: * ? [..]
	px, sx, starPx, starSx, matched := 0, 0, -1, 0, false
	for sx < len(s) {
		if px < len(pattern) {
			switch pattern[px] {
			case '*':
				starPx = px
				starSx = sx
				px++
				continue
			case '?':
				if s[sx] != '/' || true { // '?' matches any char incl. '/'
					px++
					sx++
					continue
				}
			case '[':
				if i := strings.IndexByte(pattern[px:], ']'); i > 0 {
					set := pattern[px+1 : px+i]
					if strings.ContainsRune(set, rune(s[sx])) {
						px += i + 1
						sx++
						continue
					}
				}
			default:
				if pattern[px] == s[sx] {
					px++
					sx++
					continue
				}
			}
		}
		if starPx >= 0 && starSx < len(s) {
			starSx++
			sx = starSx
			px = starPx + 1
			matched = true
			continue
		}
		return false
	}
	_ = matched
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// ── Keep-list / zone guards ─────────────────────────────────────────────────

func isProtected(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return true
	}
	for _, pat := range Cfg.KeepList {
		if globMatch(pat, path) {
			return true
		}
	}
	return false
}

func inZone(path string) bool {
	for _, z := range Cfg.Zones {
		if path == z || strings.HasPrefix(path, z+"/") {
			return true
		}
	}
	return false
}

// ── Action staging ──────────────────────────────────────────────────────────

// Action is one proposed mutation. Kind: "MOVE" | "DELETE" (DELETE = quarantine).
type Action struct {
	Kind    string
	Src     string
	Dst     string
	Size    int64
	AgeDays int
	Reason  string
	Flag    string // ok | review
}

// Plan is a staged, on-disk proposal set (TSV, human-inspectable).
type Plan struct {
	Path    string
	Actions []Action
}

func (p *Plan) Count() int { return len(p.Actions) }

func (p *Plan) Save() error {
	var b strings.Builder
	b.WriteString("ACTION\tSRC\tDST\tSIZE\tAGE\tREASON\tFLAG\n")
	for _, a := range p.Actions {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n", a.Kind, a.Src, a.Dst, a.Size, a.AgeDays, a.Reason, a.Flag)
	}
	return os.WriteFile(p.Path, []byte(b.String()), 0o644)
}

func newPlan(prefix string) *Plan {
	p := &Plan{Path: filepath.Join(CfgState, "staged",
		fmt.Sprintf("%s-%s-%d.tsv", prefix, time.Now().Format("20060102-150405"), os.Getpid()))}
	return p
}

// Stage appends an action to the plan (creating it on first use).
func (p *Plan) Stage(a Action) *Plan {
	if a.Flag == "" {
		a.Flag = "ok"
	}
	if st, err := os.Stat(a.Src); err == nil && !st.IsDir() {
		a.Size = st.Size()
	}
	a.AgeDays = daysOld(a.Src)
	p.Actions = append(p.Actions, a)
	return p
}

func loadPlans(prefix string) []*Plan {
	matches, _ := filepath.Glob(filepath.Join(CfgState, "staged", prefix+"-*.tsv"))
	var plans []*Plan
	for _, m := range matches {
		if pl := loadPlan(m); pl != nil && pl.Count() > 0 {
			plans = append(plans, pl)
		} else if pl != nil && pl.Count() == 0 {
			os.Remove(m) // drop empty husks
		}
	}
	return plans
}

func loadPlan(path string) *Plan {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	p := &Plan{Path: path}
	for i, line := range splitLines(string(data)) {
		if i == 0 && strings.HasPrefix(line, "ACTION\t") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		p.Actions = append(p.Actions, Action{
			Kind: f[0], Src: f[1], Dst: f[2],
			Size: int64(atoi(f[3])), AgeDays: atoi(f[4]),
			Reason: f[5], Flag: f[6],
		})
	}
	return p
}

func (p *Plan) Summary() string {
	mv, del, rev := 0, 0, 0
	for _, a := range p.Actions {
		switch {
		case a.Flag == "review":
			rev++
		case a.Kind == "MOVE":
			mv++
		default:
			del++
		}
	}
	return fmt.Sprintf("%s: %d move(s), %d quarantine(s), %d flagged", filepath.Base(p.Path), mv, del, rev)
}

// ── Manifest + run ids ──────────────────────────────────────────────────────

func runID() string {
	if currentRun == "" {
		currentRun = fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), os.Getpid())
	}
	return currentRun
}

// record appends an executed action to the run's manifest (for undo).
func record(kind, src, dst string) {
	path := filepath.Join(CfgState, "manifests", runID()+".tsv")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\n", kind, src, dst)
}

func latestManifest() string {
	matches, _ := filepath.Glob(filepath.Join(CfgState, "manifests", "*.tsv"))
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// ── Quarantine ──────────────────────────────────────────────────────────────

func quarantineDst(runid, src string) string {
	return filepath.Join(CfgState, "quarantine", runid, strings.ReplaceAll(src, "/", "_"))
}

// purgeQuarantine removes quarantined files past retention (called by watcher).
func purgeQuarantine() int {
	root := filepath.Join(CfgState, "quarantine")
	cutoff := time.Now().AddDate(0, 0, -Cfg.QuarantineRetentionDays)
	purged := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			if os.Remove(path) == nil {
				purged++
			}
		}
		return nil
	})
	// Drop empty run dirs.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			p := filepath.Join(root, e.Name())
			if empty, _ := isEmptyDir(p); empty {
				os.Remove(p)
			}
		}
	}
	if purged > 0 {
		logf("INFO", "quarantine purge removed %d file(s)", purged)
	}
	return purged
}

func isEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0, err
}

// ── Learning signals ────────────────────────────────────────────────────────

// signal records a decision event for the improve loop.
// event: staged | approved | rejected | undone | learned | learned_rollback
func signal(event, action, rule, src, dst string) {
	f, err := os.OpenFile(filepath.Join(CfgState, "learning", "signals.tsv"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\t%s\n",
		time.Now().Format("2006-01-02T15:04:05"), event, action, rule, src, dst)
}

// ── Notifications ───────────────────────────────────────────────────────────

func notify(title, body string) {
	quote := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	script := fmt.Sprintf(`display notification "%s" with title "%s" sound name "%s"`,
		quote(body), quote(title), Cfg.NotifySound)
	if err := exec.Command("/usr/bin/osascript", "-e", script).Run(); err != nil {
		fmt.Printf("%s — %s\n", title, body)
		exec.Command("/usr/bin/say", "pmax reminder").Start()
	}
	logf("NOTIFY", "%s: %s", title, body)
}
