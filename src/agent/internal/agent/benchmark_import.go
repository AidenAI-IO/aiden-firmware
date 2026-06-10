package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const benchmarkImportMaxBytes = 256 * 1024

func sanitizeSuiteName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) suiteLock(name string) *sync.Mutex {
	v, _ := s.benchmarkSuiteLocks.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func validateSuiteWithPython(path string) error {
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("cd /userdata/agent/benchmark && python3 -c 'import sys; sys.path.insert(0, \".\"); from runner.suite import load_suite; load_suite(\"%s\")' 2>&1", path))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("validation failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Server) handleBenchmarkImport(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name string `json:"name"`
		JSON string `json:"json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON request"}`, http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.JSON == "" {
		http.Error(w, `{"error":"name and json fields required"}`, http.StatusBadRequest)
		return
	}
	safe := sanitizeSuiteName(body.Name)
	if safe == "" || len(safe) > 64 {
		http.Error(w, `{"error":"name must be 1-64 chars of [A-Za-z0-9_-]"}`, http.StatusBadRequest)
		return
	}
	if len(body.JSON) > benchmarkImportMaxBytes {
		http.Error(w, `{"error":"suite JSON exceeds 256 KB"}`, http.StatusBadRequest)
		return
	}
	var probe any
	if err := json.Unmarshal([]byte(body.JSON), &probe); err != nil {
		http.Error(w, `{"error":"suite is not valid JSON"}`, http.StatusBadRequest)
		return
	}

	lock := s.suiteLock(safe)
	lock.Lock()
	defer lock.Unlock()

	tmpPath := filepath.Join(os.TempDir(), "suite_import_"+safe+".json")
	if err := os.WriteFile(tmpPath, []byte(body.JSON), 0o644); err != nil {
		http.Error(w, `{"error":"failed to open temp file"}`, http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpPath)

	validator := s.benchmarkSuiteValidator
	if validator == nil {
		validator = validateSuiteWithPython
	}
	if err := validator(tmpPath); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	destDir := filepath.Join(s.benchmarkDir, "suites", "custom")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	destPath := filepath.Join(destDir, safe+".json")
	if err := os.Rename(tmpPath, destPath); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to save suite: %s"}`, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"ok": true, "name": safe, "path": destPath}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleBenchmarkDelete(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}
	safe := sanitizeSuiteName(body.Name)
	if safe == "" {
		http.Error(w, `{"error":"invalid name"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(s.benchmarkDir, "suites", "custom", safe+".json")
	if err := os.Remove(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
