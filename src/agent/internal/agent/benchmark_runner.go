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

type benchmarkLaunchSpec struct {
	Suite    string
	SuiteDir string
	Kind     string
}

// launchBenchmarkRunner double-forks a sh script that runs the Python benchmark
// launchBenchmarkRunner double-forks a sh script that runs the Python benchmark
// runner. Writes /tmp/benchmark_runner.pid + /tmp/benchmark_run.log + updates
// state.json on completion. The Python runner no longer accepts --state-file
// (removed upstream); the Go server handles state.json bookkeeping itself in
// handleBenchmarkRun (start) and the trailing echo (end).
func (s *Server) launchBenchmarkRunner(spec benchmarkLaunchSpec, judgeModel, openRouterAPIKey string) error {
	if judgeModel == "" {
		judgeModel = "bytedance-seed/seed-2.0-lite"
	}
	runnerArgs := ""
	if spec.Kind == "unit" {
		if spec.SuiteDir != "" {
			runnerArgs = "unit --suite-dir " + shellQuote(spec.SuiteDir) + " --agent-url http://127.0.0.1:8080 "
		} else {
			runnerArgs = "unit --suite " + shellQuote(spec.Suite) + " --agent-url http://127.0.0.1:8080 "
		}
	} else {
		runnerArgs = "run --suite " + shellQuote(spec.Suite) + " --agent-url http://127.0.0.1:8080 --judge-model " + shellQuote(judgeModel) + " "
	}
	script := fmt.Sprintf(`cd /userdata/agent/benchmark && `+
		`export OPENROUTER_API_KEY=%s && `+
		`(`+
		`echo 'Starting benchmark...' > /tmp/benchmark_run.log; `+
		`python3 -c 'import sys;sys.path.insert(0,".");from runner.main import cli;sys.exit(cli())' `+
		`%s`+
		`>> /tmp/benchmark_run.log 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > /tmp/benchmark_runner.pid; `+
		`wait $runner_pid; rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > /userdata/agent/benchmark/state.json) &`,
		shellQuote(openRouterAPIKey), runnerArgs)
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
		Suite    string `json:"suite"`
		SuiteDir string `json:"suite_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Suite == "" && req.SuiteDir == "") {
		http.Error(w, `{"error":"missing suite or suite_dir field"}`, http.StatusBadRequest)
		return
	}
	if req.Suite != "" && req.SuiteDir != "" {
		http.Error(w, `{"error":"exactly one of suite or suite_dir is required"}`, http.StatusBadRequest)
		return
	}
	spec := benchmarkLaunchSpec{Suite: req.Suite, SuiteDir: req.SuiteDir, Kind: "benchmark"}
	stateSuite := req.Suite
	if req.SuiteDir != "" {
		spec.Kind = "unit"
		stateSuite = req.SuiteDir
	} else if benchmarkSuiteKind(req.Suite) == "unit" {
		spec.Kind = "unit"
	}
	statePath := s.benchmarkStatePath
	if statePath == "" {
		statePath = filepath.Join(s.benchmarkDir, "state.json")
	}
	state := fmt.Sprintf(`{"status":"running","suite":%q}`, stateSuite)
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
	if err := launch(spec, judge, apiKey); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"launch failed: %s"}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"status":"running"}`))
}
