package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
