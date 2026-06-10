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
	Name   string `json:"name"`
	Path   string `json:"path"`
	Custom bool   `json:"custom"`
}

func (s *Server) handleBenchmarkSuites(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	suitesDir := filepath.Join(s.benchmarkDir, "suites")
	var items []suiteListItem
	err := filepath.Walk(suitesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "._") || !strings.HasSuffix(base, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(suitesDir, path)
		items = append(items, suiteListItem{
			Name:   strings.TrimSuffix(base, ".json"),
			Path:   path,
			Custom: strings.HasPrefix(rel, "custom"+string(filepath.Separator)),
		})
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to list suites: %s"}`, err), http.StatusInternalServerError)
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type runListItem struct {
	RunID  string         `json:"run_id"`
	Suite  string         `json:"suite,omitempty"`
	Totals map[string]int `json:"totals,omitempty"`
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

	items := make([]runListItem, 0, len(names))
	for _, n := range names {
		item := runListItem{RunID: n}
		// TODO: Extract suite and totals from report.html if needed
		// For now, totals remain empty as per the task specification
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
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
	statePath := filepath.Join(s.benchmarkDir, "state.json")
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
	pidFile := s.benchmarkPIDFile
	if pidFile == "" {
		pidFile = "/tmp/benchmark_runner.pid"
	}
	if data, err := os.ReadFile(pidFile); err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					return true
				}
			}
		}
	}
	out, err := exec.Command("sh", "-c",
		"ps -w 2>/dev/null | grep -F 'runner.main' | grep -v grep | head -1").Output()
	if err != nil {
		return true
	}
	return len(strings.TrimSpace(string(out))) > 0
}

const benchmarkLogTailBytes = 64 * 1024

func (s *Server) handleBenchmarkLog(w http.ResponseWriter, r *http.Request) {
	logPath := s.benchmarkLogPath
	if logPath == "" {
		logPath = "/tmp/benchmark_run.log"
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
