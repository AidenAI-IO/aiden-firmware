package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ResolveBenchmarkDir picks the first existing benchmark root from this
// priority order:
//  1. CLI flag value (passed in via flagValue)
//  2. AIDEN_BENCHMARK_DIR env var
//  3. cfg.Dir from agent.conf [benchmark] benchmark_dir
//  4. /userdata/agent/benchmark (overridable via AIDEN_BENCHMARK_USERDATA_ROOT for tests)
//  5. <cwd>/benchmark
func ResolveBenchmarkDir(flagValue string, cfg BenchmarkConfig) (string, error) {
	candidates := []string{}
	if flagValue != "" {
		candidates = append(candidates, flagValue)
	}
	if env := os.Getenv("AIDEN_BENCHMARK_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	if cfg.Dir != "" {
		candidates = append(candidates, cfg.Dir)
	}

	userdataRoot := os.Getenv("AIDEN_BENCHMARK_USERDATA_ROOT")
	if userdataRoot == "" {
		userdataRoot = "/"
	}
	candidates = append(candidates, filepath.Join(userdataRoot, "userdata", "agent", "benchmark"))

	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "benchmark"))
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c, nil
		}
	}

	return "", fmt.Errorf("no benchmark directory found among candidates: %v", candidates)
}

type suiteListItem struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Custom      bool   `json:"custom"`
	Type        string `json:"type"` // "aiden" | "mobilegym_builtin"
	TaskCount   int    `json:"task_count,omitempty"`
	Description string `json:"description,omitempty"`
	Concurrent  bool   `json:"concurrent"`
}

type skillListItem struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type skillOptTargetItem struct {
	Skill                    string   `json:"skill"`
	TrainSuites              []string `json:"train_suites"`
	VerificationSuites       []string `json:"verification_suites"`
	DefaultTrainSuite        string   `json:"default_train_suite,omitempty"`
	DefaultVerificationSuite string   `json:"default_verification_suite,omitempty"`
}

func benchmarkSuiteKind(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 256*1024 {
		return "benchmark"
	}
	var raw struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(data, &raw) == nil && raw.Kind == "unit" {
		return "unit"
	}
	return "benchmark"
}

func (s *Server) handleBenchmarkSuites(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "aiden"
	}

	items := []suiteListItem{}
	items = append(items, scanAidenSuites(s.benchmarkDir, mode == "mobilegym")...)
	if mode == "mobilegym" {
		items = append(items, scanMobileGymBuiltinSuites(s.benchmarkDir)...)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Type != items[j].Type {
			return items[i].Type == "aiden"
		}
		return items[i].Name < items[j].Name
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func scanAidenSuites(benchmarkDir string, concurrent bool) []suiteListItem {
	suitesDir := filepath.Join(benchmarkDir, "suites")
	var items []suiteListItem
	filepath.Walk(suitesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "._") || !strings.HasSuffix(base, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(suitesDir, path)
		items = append(items, suiteListItem{
			Name:       strings.TrimSuffix(base, ".json"),
			Path:       path,
			Kind:       benchmarkSuiteKind(path),
			Custom:     strings.HasPrefix(rel, "custom"+string(filepath.Separator)),
			Type:       "aiden",
			Concurrent: concurrent,
			TaskCount:  countAidenTasks(path),
		})
		return nil
	})
	return items
}

func countAidenTasks(suitePath string) int {
	data, err := os.ReadFile(suitePath)
	if err != nil {
		return 0
	}
	var raw struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return 0
	}
	return len(raw.Tasks)
}

func scanMobileGymBuiltinSuites(benchmarkDir string) []suiteListItem {
	allTasks := filepath.Join(benchmarkDir, "mobilegym", "suites", "all_tasks.txt")
	data, err := os.ReadFile(allTasks)
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ".", 2)
		if len(parts) != 2 {
			continue
		}
		counts[parts[0]]++
	}
	items := make([]suiteListItem, 0, len(counts))
	for name, n := range counts {
		items = append(items, suiteListItem{
			Name:       name,
			Type:       "mobilegym_builtin",
			TaskCount:  n,
			Concurrent: true,
		})
	}
	return items
}

func (s *Server) handleBenchmarkSkills(w http.ResponseWriter, r *http.Request) {
	items := scanBenchmarkSkills(s.benchmarkSkillDirs())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleBenchmarkSkillOptTargets(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	items := scanSkillOptTargets(s.benchmarkDir)
	items = mergeSkillOptTargetsWithSkills(items, scanBenchmarkSkills(s.benchmarkSkillDirs()))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func mergeSkillOptTargetsWithSkills(targets []skillOptTargetItem, skills []skillListItem) []skillOptTargetItem {
	bySkill := map[string]skillOptTargetItem{}
	for _, target := range targets {
		bySkill[target.Skill] = target
	}
	for _, skill := range skills {
		if _, exists := bySkill[skill.Name]; exists {
			continue
		}
		bySkill[skill.Name] = skillOptTargetItem{
			Skill:              skill.Name,
			TrainSuites:        []string{},
			VerificationSuites: []string{},
		}
	}
	items := make([]skillOptTargetItem, 0, len(bySkill))
	for _, item := range bySkill {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Skill < items[j].Skill })
	return items
}

func scanSkillOptTargets(benchmarkDir string) []skillOptTargetItem {
	skillOptDir := filepath.Join(benchmarkDir, "suites", "skillopt")
	entries, err := os.ReadDir(skillOptDir)
	if err != nil {
		return []skillOptTargetItem{}
	}

	items := []skillOptTargetItem{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill := entry.Name()
		suiteDir := filepath.Join(skillOptDir, skill)
		files, err := os.ReadDir(suiteDir)
		if err != nil {
			continue
		}
		item := skillOptTargetItem{Skill: skill}
		for _, file := range files {
			name := file.Name()
			if file.IsDir() || strings.HasPrefix(name, "._") || !strings.HasSuffix(name, ".json") {
				continue
			}
			base := strings.TrimSuffix(name, ".json")
			label := filepath.ToSlash(filepath.Join("skillopt", skill, base))
			lower := strings.ToLower(base)
			switch {
			case strings.Contains(lower, "train"):
				item.TrainSuites = append(item.TrainSuites, label)
			case strings.Contains(lower, "verification") || strings.Contains(lower, "validation"):
				item.VerificationSuites = append(item.VerificationSuites, label)
			}
		}
		sort.Strings(item.TrainSuites)
		sort.Strings(item.VerificationSuites)
		if len(item.TrainSuites) > 0 {
			item.DefaultTrainSuite = item.TrainSuites[0]
		}
		if len(item.VerificationSuites) > 0 {
			item.DefaultVerificationSuite = item.VerificationSuites[0]
		}
		if len(item.TrainSuites) > 0 || len(item.VerificationSuites) > 0 {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Skill < items[j].Skill })
	return items
}

func (s *Server) benchmarkSkillDirs() []string {
	seen := map[string]bool{}
	dirs := []string{}
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if env := os.Getenv("AIDEN_SKILLS_DIR"); env != "" {
		for _, dir := range strings.Split(env, string(os.PathListSeparator)) {
			add(dir)
		}
	}
	if s.runtime != nil {
		for _, dir := range s.runtime.config.SkillsDirs {
			add(dir)
		}
		if s.runtime.config.ConfigDir != "" {
			add(filepath.Join(s.runtime.config.ConfigDir, "skills"))
		}
	}
	if s.benchmarkDir != "" {
		add(filepath.Join(s.benchmarkDir, "config", "skills"))
		add(filepath.Join(s.benchmarkDir, "mobilegym", "config", "skills"))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, "src", "agent", "config", "skills"))
		add(filepath.Join(cwd, "config", "skills"))
	}
	return dirs
}

func (s *Server) benchmarkPrimarySkillsDir() string {
	for _, dir := range s.benchmarkSkillDirs() {
		if strings.TrimSpace(dir) != "" {
			return dir
		}
	}
	return ""
}

func (s *Server) benchmarkSkillOptSkillsDirFor(skill string) string {
	mobileGymTemplate := ""
	if s.benchmarkDir != "" {
		mobileGymTemplate = filepath.Clean(filepath.Join(s.benchmarkDir, "mobilegym", "config", "skills"))
	}
	for _, dir := range s.benchmarkSkillDirs() {
		clean := filepath.Clean(dir)
		if mobileGymTemplate != "" && clean == mobileGymTemplate {
			continue
		}
		if info, err := os.Stat(filepath.Join(clean, skill, "SKILL.md")); err == nil && !info.IsDir() {
			return clean
		}
	}
	return ""
}

func scanBenchmarkSkills(dirs []string) []skillListItem {
	byName := map[string]skillListItem{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			skillPath := filepath.Join(dir, name, "SKILL.md")
			if info, err := os.Stat(skillPath); err != nil || info.IsDir() {
				continue
			}
			if _, exists := byName[name]; !exists {
				byName[name] = skillListItem{Name: name, Path: skillPath}
			}
		}
	}
	items := make([]skillListItem, 0, len(byName))
	for _, item := range byName {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

type runListItem struct {
	RunID    string         `json:"run_id"`
	Suite    string         `json:"suite,omitempty"`
	Status   string         `json:"status,omitempty"`
	Progress string         `json:"progress,omitempty"`
	Model    string         `json:"model,omitempty"`
	Totals   map[string]int `json:"totals,omitempty"`
}

func (s *Server) handleBenchmarkRuns(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	runsDir := filepath.Join(s.benchmarkDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		// If runs directory doesn't exist, return empty array
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]runListItem{})
		return
	}

	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}

	// Sort in reverse chronological order (newest first)
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	// Cap at 20 most recent runs
	if len(names) > 20 {
		names = names[:20]
	}

	items := make([]runListItem, 0, len(names)+1)
	if running := s.currentBenchmarkRunListItem(); running != nil {
		items = append(items, *running)
	}
	for _, n := range names {
		item := runListItem{RunID: n, Status: "done"}
		// Read manifest.json (written by Python runner) for suite path + totals.
		manifestPath := filepath.Join(s.benchmarkDir, "runs", n, "manifest.json")
		if data, err := os.ReadFile(manifestPath); err == nil {
			var raw struct {
				SuitePath   string         `json:"suite_path"`
				Totals      map[string]int `json:"totals"`
				JudgeConfig map[string]any `json:"judge_config"`
				Model       string         `json:"model"`
				AgentModel  string         `json:"agent_model"`
				ModelName   string         `json:"model_name"`
			}
			if json.Unmarshal(data, &raw) == nil {
				if raw.SuitePath != "" {
					// Display just the basename (e.g. "perception_v1.json").
					item.Suite = filepath.Base(raw.SuitePath)
				}
				item.Totals = raw.Totals
				item.Model = benchmarkRunModel(raw.Model, raw.AgentModel, raw.ModelName)
				item.Progress = benchmarkRunProgress(raw.Totals)
			}
		}
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) currentBenchmarkRunListItem() *runListItem {
	statePath := s.benchmarkStateFile()
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var state struct {
		Status      string `json:"status"`
		RunID       string `json:"run_id"`
		Suite       string `json:"suite"`
		Total       int    `json:"total"`
		Current     int    `json:"current"`
		Completed   int    `json:"completed"`
		CurrentTask string `json:"current_task"`
		Model       string `json:"model"`
	}
	if json.Unmarshal(data, &state) != nil || state.Status != "running" {
		return nil
	}
	current := state.Current
	if current == 0 && state.Completed > 0 {
		current = state.Completed
	}
	if current == 0 && state.Total > 0 {
		current = 1
	}
	progress := "running"
	if state.Total > 0 {
		progress = fmt.Sprintf("%d/%d", current, state.Total)
	}
	model := state.Model
	if model == "" && s.runtime != nil {
		model = s.runtime.config.Model.Model
	}
	return &runListItem{
		RunID:    firstNonEmptyBenchmarkValue(state.RunID, "running"),
		Suite:    displaySuiteName(state.Suite),
		Status:   "running",
		Progress: progress,
		Model:    model,
		Totals:   map[string]int{"tasks": state.Total, "completed": state.Completed},
	}
}

func benchmarkRunModel(model, agentModel, modelName string) string {
	if model != "" {
		return model
	}
	if agentModel != "" {
		return agentModel
	}
	if modelName != "" {
		return modelName
	}
	return ""
}

func benchmarkRunProgress(totals map[string]int) string {
	if totals == nil {
		return ""
	}
	total := totals["tasks"]
	if total == 0 {
		return ""
	}
	done := totals["passed"] + totals["failed"] + totals["skipped"] + totals["judge_error"] + totals["timeout"] + totals["error"] + totals["unknown"] + totals["worker_failed"]
	if done == 0 {
		done = total
	}
	return fmt.Sprintf("%d/%d", done, total)
}

func displaySuiteName(suite string) string {
	if suite == "" {
		return ""
	}
	return filepath.Base(suite)
}

func firstNonEmptyBenchmarkValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Server) handleBenchmarkReport(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/benchmark/report/")
	safe := sanitizeRunID(id)
	if safe == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.benchmarkDir, "runs", safe, "report.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func sanitizeRunID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

const benchmarkStartupGraceSec = 10

func (s *Server) handleBenchmarkStatus(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	statePath := s.benchmarkStateFile()
	data, err := os.ReadFile(statePath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"idle"}`))
		return
	}
	body := string(data)
	if strings.Contains(body, `"running"`) {
		info, _ := os.Stat(statePath)
		ageSec := int64(-1)
		if info != nil {
			ageSec = int64(time.Since(info.ModTime()).Seconds())
		}
		past := ageSec < 0 || ageSec > benchmarkStartupGraceSec
		if past && !s.benchmarkRunnerAlive() {
			body = `{"status":"idle","recovered":true}`
			os.WriteFile(statePath, []byte(`{"status":"idle"}`), 0o644)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body))
}

func (s *Server) benchmarkRunnerAlive() bool {
	// Check both Aiden and MobileGym pid files; either being live means a runner is active.
	pidFile := s.benchmarkPIDFile
	if pidFile == "" {
		pidFile = "/tmp/benchmark_runner.pid"
	}
	pidFiles := []string{pidFile}
	if pidFile == "/tmp/benchmark_runner.pid" {
		pidFiles = append(pidFiles, "/tmp/mobilegym_runner.pid")
	}
	for _, p := range pidFiles {
		if data, err := os.ReadFile(p); err == nil {
			var pid int
			if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if err := proc.Signal(syscall.Signal(0)); err == nil {
						return true
					}
				}
			}
		}
	}
	out, err := exec.Command("sh", "-c",
		"ps -w 2>/dev/null | grep -E 'runner.main|runner.skillopt|parallel_run.sh' | grep -v grep | head -1").Output()
	if err != nil {
		return true
	}
	return len(strings.TrimSpace(string(out))) > 0
}

const benchmarkLogTailBytes = 64 * 1024

func (s *Server) handleBenchmarkLog(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	logPath := s.benchmarkLogPath
	if logPath == "" {
		if mode == "mobilegym" {
			logPath = "/tmp/mobilegym_run.log"
		} else if mode == "skillopt" {
			logPath = "/tmp/skillopt_run.log"
		} else {
			logPath = "/tmp/benchmark_run.log"
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	data, err := os.ReadFile(logPath)
	if err != nil {
		// File doesn't exist or can't read - return empty
		return
	}

	// If file is larger than 64KB, take the last 64KB
	if len(data) > benchmarkLogTailBytes {
		// Find first newline after the cut point to avoid partial lines
		start := len(data) - benchmarkLogTailBytes
		foundNewline := false
		for start < len(data) && data[start] != '\n' {
			start++
			if start-len(data)+benchmarkLogTailBytes > 1024 {
				// Searched 1KB without finding newline, just use the cut point
				start = len(data) - benchmarkLogTailBytes
				foundNewline = true
				break
			}
		}
		if !foundNewline && start < len(data) {
			start++ // Skip the newline itself
		}
		data = data[start:]
	}

	w.Write(data)
}

func (s *Server) handleBenchmarkIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(benchmarkIndexHTML))
}

func (s *Server) handleBenchmarkRecord(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(benchmarkRecordHTML))
}
