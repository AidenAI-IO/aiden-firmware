package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// launchBenchmarkRunner double-forks a sh script that runs the Python benchmark
// launchBenchmarkRunner double-forks a sh script that runs the Python benchmark
// runner. Writes /tmp/benchmark_runner.pid + /tmp/benchmark_run.log + updates
// state.json on completion. The Python runner no longer accepts --state-file
// (removed upstream); the Go server handles state.json bookkeeping itself in
// handleBenchmarkRun (start) and the trailing echo (end).
func (s *Server) launchBenchmarkRunner(suitePath, judgeModel, openRouterAPIKey, agentModel string) error {
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
		`--agent-model %s `+
		`>> /tmp/benchmark_run.log 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > /tmp/benchmark_runner.pid; `+
		`wait $runner_pid; rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > /userdata/agent/benchmark/state.json) &`,
		shellQuote(openRouterAPIKey), shellQuote(suitePath), shellQuote(judgeModel), shellQuote(agentModel))
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
		Suite     string `json:"suite"`
		SuiteType string `json:"suite_type"`
		Mode      string `json:"mode"`
		Parallel  int    `json:"parallel"`
		Limit     int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Suite == "" {
		http.Error(w, `{"error":"missing suite field"}`, http.StatusBadRequest)
		return
	}

	if req.Mode == "" {
		req.Mode = "aiden"
	}

	statePath := s.benchmarkStatePath
	if statePath == "" {
		statePath = filepath.Join(s.benchmarkDir, "state.json")
	}

	stateJSON, _ := json.Marshal(map[string]any{
		"status":     "running",
		"mode":       req.Mode,
		"suite":      req.Suite,
		"suite_type": req.SuiteType,
		"parallel":   req.Parallel,
	})
	if err := os.WriteFile(statePath, stateJSON, 0o644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	switch req.Mode {
	case "aiden":
		launch := s.benchmarkLauncher
		if launch == nil {
			launch = s.launchBenchmarkRunner
		}
		apiKey := ""
		judge := ""
		agentModel := ""
		if s.runtime != nil {
			apiKey = s.runtime.config.Model.APIKey
			judge = s.runtime.config.Benchmark.JudgeModel
			agentModel = s.runtime.config.Model.Model
		}
		if err := launch(req.Suite, judge, apiKey, agentModel); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"launch failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
	case "mobilegym":
		if req.Parallel < 1 {
			req.Parallel = 1
		}
		launch := s.benchmarkMobileGymLauncher
		if launch == nil {
			launch = s.launchMobileGymRunner
		}
		if err := launch(req.Suite, req.SuiteType, req.Parallel, req.Limit); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"mobilegym launch failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, fmt.Sprintf(`{"error":"unknown mode %q"}`, req.Mode), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"status":"running"}`))
}

var safeSuiteSegment = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

func validSuiteName(suite string) bool {
	if suite == "" || strings.HasPrefix(suite, "/") {
		return false
	}
	for _, part := range strings.Split(suite, "/") {
		if part == "" || part == "." || part == ".." || !safeSuiteSegment.MatchString(part) {
			return false
		}
	}
	return true
}

func (s *Server) launchMobileGymRunner(suite, suiteType string, parallel, limit int) error {
	script, err := buildMobileGymLaunchScript(s.benchmarkDir, suite, suiteType, parallel, limit)
	if err != nil {
		return err
	}
	return exec.Command("sh", "-c", script).Start()
}

func buildMobileGymLaunchScript(benchmarkDir, suite, suiteType string, parallel, limit int) (string, error) {
	if !validSuiteName(suite) {
		return "", fmt.Errorf("invalid suite name %q (must be a safe relative path)", suite)
	}

	var suiteFlag string
	switch suiteType {
	case "aiden":
		suiteFlag = fmt.Sprintf("--aiden-suite %s", shellQuote(suite))
	case "mobilegym_builtin", "":
		suiteFlag = fmt.Sprintf("--suite %s", shellQuote(suite))
	default:
		return "", fmt.Errorf("unknown suite_type %q", suiteType)
	}

	limitFlag := ""
	if limit > 0 {
		limitFlag = fmt.Sprintf("--limit %d", limit)
	}

	statePath := filepath.Join(benchmarkDir, "state.json")
	pidFile := "/tmp/mobilegym_runner.pid"

	mobileGymDockerDir := filepath.Join(benchmarkDir, "mobilegym", "docker")

	return fmt.Sprintf(`cd %s && `+
		`(`+
		`echo 'Starting MobileGym benchmark...' > /tmp/mobilegym_run.log; `+
		`PARALLEL=%d ./parallel_run.sh %s %s `+
		`>> /tmp/mobilegym_run.log 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > %s; `+
		`wait $runner_pid; rm -f %s; `+
		`echo '{"status":"idle"}' > %s) &`,
		shellQuote(mobileGymDockerDir), parallel, suiteFlag, limitFlag,
		shellQuote(pidFile), shellQuote(pidFile), shellQuote(statePath)), nil
}
