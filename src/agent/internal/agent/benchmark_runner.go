package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// launchBenchmarkRunner double-forks a sh script that runs the Python benchmark
// runner. Mirrors config_web.cpp implementation so the runner side stays unchanged.
// Writes /tmp/benchmark_runner.pid + /tmp/benchmark_run.log + updates state.json.
func (s *Server) launchBenchmarkRunner(suitePath, judgeModel, openRouterAPIKey string) error {
	if judgeModel == "" {
		judgeModel = "bytedance-seed/seed-2.0-lite"
	}
	script := fmt.Sprintf(`cd /userdata/agent/benchmark && `+
		`export OPENROUTER_API_KEY=%s && `+
		`(`+
		`echo 'Starting benchmark...' > /tmp/benchmark_run.log; `+
		`python3 -c 'import sys;sys.path.insert(0,".");from runner.main import cli;sys.exit(cli())' `+
		`run --suite %s `+
		`--agent-url http://127.0.0.1:8080 `+
		`--judge-model %s `+
		`--state-file /userdata/agent/benchmark/state.json `+
		`>> /tmp/benchmark_run.log 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > /tmp/benchmark_runner.pid; `+
		`wait $runner_pid; rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > /userdata/agent/benchmark/state.json) &`,
		shellQuote(openRouterAPIKey), shellQuote(suitePath), shellQuote(judgeModel))
	return exec.Command("sh", "-c", script).Start()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *Server) handleBenchmarkRun(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		http.Error(w, `{"error":"benchmark directory not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Suite string `json:"suite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Suite == "" {
		http.Error(w, `{"error":"missing suite field"}`, http.StatusBadRequest)
		return
	}
	statePath := s.benchmarkStatePath
	if statePath == "" {
		statePath = filepath.Join(s.benchmarkDir, "state.json")
	}
	state := fmt.Sprintf(`{"status":"running","suite":%q}`, req.Suite)
	if err := os.WriteFile(statePath, []byte(state), 0o644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	launch := s.benchmarkLauncher
	if launch == nil {
		launch = s.launchBenchmarkRunner
	}
	apiKey := ""
	judge := ""
	if s.runtime != nil {
		apiKey = s.runtime.config.Model.APIKey
		judge = s.runtime.config.Benchmark.JudgeModel
	}
	if err := launch(req.Suite, judge, apiKey); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"launch failed: %s"}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"status":"running"}`))
}
