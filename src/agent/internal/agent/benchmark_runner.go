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

type benchmarkLaunchSpec struct {
	Suite    string
	SuiteDir string
	Kind     string
}

// launchBenchmarkRunner double-forks a sh script that runs the Python benchmark
// runner. Writes the runner pid + log file and passes state.json to the Python
// runner so progress updates include current/total task counts while running.
func (s *Server) launchBenchmarkRunner(spec benchmarkLaunchSpec, judgeModel, openRouterAPIKey, agentModel string) error {
	if judgeModel == "" {
		judgeModel = defaultBenchmarkJudgeModel
	}
	benchmarkDir := s.benchmarkDir
	if benchmarkDir == "" {
		benchmarkDir = "/userdata/agent/benchmark"
	}
	statePath := s.benchmarkStateFile()
	logPath := s.benchmarkLogFile("aiden")
	runnerArgs := ""
	if spec.Kind == "unit" {
		if spec.SuiteDir != "" {
			runnerArgs = "unit --suite-dir " + shellQuote(spec.SuiteDir) + " --agent-url http://127.0.0.1:8080 "
		} else {
			runnerArgs = "unit --suite " + shellQuote(spec.Suite) + " --agent-url http://127.0.0.1:8080 "
		}
	} else {
		runnerArgs = "run --suite " + shellQuote(spec.Suite) + " --agent-url http://127.0.0.1:8080 --judge-model " + shellQuote(judgeModel) + " --agent-model " + shellQuote(agentModel) + " --state-file " + shellQuote(statePath) + " "
	}
	script := fmt.Sprintf(`cd %s && `+
		`export OPENROUTER_API_KEY=%s && `+
		`(`+
		`echo 'Starting benchmark...' > %s; `+
		`python3 -c 'import sys;sys.path.insert(0,".");from runner.main import cli;sys.exit(cli())' `+
		`%s`+
		`>> %s 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > /tmp/benchmark_runner.pid; `+
		`wait $runner_pid; rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > %s) &`,
		shellQuote(benchmarkDir), shellQuote(openRouterAPIKey), shellQuote(logPath), runnerArgs, shellQuote(logPath), shellQuote(statePath))
	return exec.Command("sh", "-c", script).Start()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *Server) benchmarkStateFile() string {
	if s.benchmarkStatePath != "" {
		return s.benchmarkStatePath
	}
	if s.benchmarkDir != "" {
		return filepath.Join(s.benchmarkDir, "state.json")
	}
	return "/userdata/agent/benchmark/state.json"
}

func (s *Server) benchmarkLogFile(mode string) string {
	if s.benchmarkLogPath != "" {
		return s.benchmarkLogPath
	}
	if mode == "mobilegym" {
		return "/tmp/mobilegym_run.log"
	}
	return "/tmp/benchmark_run.log"
}

func (s *Server) writeBenchmarkRunError(w http.ResponseWriter, mode string, status int, message string) {
	if logPath := s.benchmarkLogFile(mode); logPath != "" {
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			_, _ = fmt.Fprintf(f, "ERROR: %s\n", message)
			_ = f.Close()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) handleBenchmarkRun(w http.ResponseWriter, r *http.Request) {
	if s.benchmarkDir == "" {
		s.writeBenchmarkRunError(w, "aiden", http.StatusServiceUnavailable, "benchmark directory not configured")
		return
	}
	var req struct {
		Suite     string `json:"suite"`
		SuiteDir  string `json:"suite_dir"`
		SuiteType string `json:"suite_type"`
		Mode      string `json:"mode"`
		Parallel  int    `json:"parallel"`
		Limit     int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeBenchmarkRunError(w, "aiden", http.StatusBadRequest, "invalid request")
		return
	}
	if req.Mode == "" {
		req.Mode = "aiden"
	}
	statePath := s.benchmarkStateFile()

	switch req.Mode {
	case "aiden":
		if req.Suite == "" && req.SuiteDir == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "missing suite or suite_dir field")
			return
		}
		if req.Suite != "" && req.SuiteDir != "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "exactly one of suite or suite_dir is required")
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

		launch := s.benchmarkLauncher
		if launch == nil {
			launch = s.launchBenchmarkRunner
		}
		apiKey := ""
		judge := ""
		agentModel := ""
		if s.runtime != nil {
			apiKey = s.runtime.config.Benchmark.APIKey
			judge = s.runtime.config.Benchmark.JudgeModel
			agentModel = s.runtime.config.Model.Model
		}
		if spec.Kind == "benchmark" && strings.TrimSpace(apiKey) == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "benchmark judge api_key is not configured")
			return
		}
		stateJSON, _ := json.Marshal(map[string]any{
			"status": "running",
			"mode":   "aiden",
			"suite":  stateSuite,
			"model":  agentModel,
		})
		if err := os.WriteFile(statePath, stateJSON, 0o644); err != nil {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, err.Error())
			return
		}
		if err := launch(spec, judge, apiKey, agentModel); err != nil {
			_ = os.WriteFile(statePath, []byte(`{"status":"idle"}`), 0o644)
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, "launch failed: "+err.Error())
			return
		}
	case "mobilegym":
		if req.Suite == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "missing suite field")
			return
		}
		if req.SuiteDir != "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "suite_dir is not supported for mobilegym mode")
			return
		}
		if req.Parallel < 1 {
			req.Parallel = 1
		}
		launch := s.benchmarkMobileGymLauncher
		if launch == nil {
			launch = s.launchMobileGymRunner
		}
		if err := launch(req.Suite, req.SuiteType, req.Parallel, req.Limit); err != nil {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, "mobilegym launch failed: "+err.Error())
			return
		}
		stateJSON, _ := json.Marshal(map[string]any{
			"status":     "running",
			"mode":       "mobilegym",
			"suite":      req.Suite,
			"suite_type": req.SuiteType,
			"parallel":   req.Parallel,
		})
		if err := os.WriteFile(statePath, stateJSON, 0o644); err != nil {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, fmt.Sprintf("unknown mode %q", req.Mode))
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
