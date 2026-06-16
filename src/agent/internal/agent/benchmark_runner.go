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
	Suites   []string
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
	runnerCommands := []string{}
	if spec.Kind == "unit" {
		runnerArgs := ""
		if spec.SuiteDir != "" {
			runnerArgs = "unit --suite-dir " + shellQuote(spec.SuiteDir) + " --agent-url http://127.0.0.1:8080 "
		} else {
			runnerArgs = "unit --suite " + shellQuote(spec.Suite) + " --agent-url http://127.0.0.1:8080 "
		}
		runnerCommands = append(runnerCommands, `python3 -c 'import sys;sys.path.insert(0,".");from runner.main import cli;sys.exit(cli())' `+runnerArgs+`>> `+shellQuote(logPath)+` 2>&1`)
	} else {
		suites := spec.Suites
		if len(suites) == 0 {
			suites = []string{spec.Suite}
		}
		for _, suite := range suites {
			runnerArgs := "run --suite " + shellQuote(suite) + " --agent-url http://127.0.0.1:8080 --judge-model " + shellQuote(judgeModel) + " --agent-model " + shellQuote(agentModel) + " --state-file " + shellQuote(statePath) + " "
			runnerCommands = append(runnerCommands, `python3 -c 'import sys;sys.path.insert(0,".");from runner.main import cli;sys.exit(cli())' `+runnerArgs+`>> `+shellQuote(logPath)+` 2>&1`)
		}
	}
	runnerScript := ""
	for i, command := range runnerCommands {
		if i > 0 {
			runnerScript += `if [ "$runner_rc" -eq 0 ]; then `
		}
		runnerScript += command + ` || runner_rc=$?; `
		if i > 0 {
			runnerScript += `fi; `
		}
	}
	script := fmt.Sprintf(`cd %s && `+
		`export OPENROUTER_API_KEY=%s && `+
		`(`+
		`echo 'Starting benchmark...' > %s; `+
		`runner_rc=0; `+
		`runner_pid=$$; echo $runner_pid > /tmp/benchmark_runner.pid; `+
		`%s; `+
		`rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > %s; `+
		`exit $runner_rc) &`,
		shellQuote(benchmarkDir), shellQuote(openRouterAPIKey), shellQuote(logPath), runnerScript, shellQuote(statePath))
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
		Suite     string   `json:"suite"`
		Suites    []string `json:"suites"`
		SuiteDir  string   `json:"suite_dir"`
		SuiteType string   `json:"suite_type"`
		Mode      string   `json:"mode"`
		Parallel  int      `json:"parallel"`
		Limit     int      `json:"limit"`
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
		suites := req.Suites
		if len(suites) == 0 && req.Suite != "" {
			suites = []string{req.Suite}
		}
		if len(suites) == 0 && req.SuiteDir == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "missing suite or suite_dir field")
			return
		}
		if len(suites) > 0 && req.SuiteDir != "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "exactly one of suite or suite_dir is required")
			return
		}
		spec := benchmarkLaunchSpec{SuiteDir: req.SuiteDir, Kind: "benchmark"}
		if len(suites) > 0 {
			spec.Suite = suites[0]
			spec.Suites = suites
		}
		stateSuite := strings.Join(suites, ",")
		if req.SuiteDir != "" {
			spec.Kind = "unit"
			stateSuite = req.SuiteDir
		} else if len(suites) == 1 && benchmarkSuiteKind(suites[0]) == "unit" {
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
			"suites": suites,
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
		suites := req.Suites
		if len(suites) == 0 && req.Suite != "" {
			suites = []string{req.Suite}
		}
		if len(suites) == 0 {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "missing suite field")
			return
		}
		if req.SuiteDir != "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "suite_dir is not supported for mobilegym mode")
			return
		}
		for _, suite := range suites {
			if !validSuiteName(suite) {
				s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, fmt.Sprintf("invalid suite name %q", suite))
				return
			}
		}
		suiteType := req.SuiteType
		if suiteType == "" {
			suiteType = "mobilegym_builtin"
		}
		if len(suites) > 1 && suiteType != "mobilegym_builtin" && suiteType != "aiden" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "multiple suites are only supported for mobilegym_builtin or aiden")
			return
		}
		if req.Parallel < 1 {
			req.Parallel = 1
		}
		launch := s.benchmarkMobileGymLauncher
		if launch == nil {
			launch = s.launchMobileGymRunner
		}
		suiteArg := strings.Join(suites, ",")
		if err := launch(suiteArg, suiteType, req.Parallel, req.Limit); err != nil {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, "mobilegym launch failed: "+err.Error())
			return
		}
		stateJSON, _ := json.Marshal(map[string]any{
			"status":     "running",
			"mode":       "mobilegym",
			"suite":      suiteArg,
			"suites":     suites,
			"suite_type": suiteType,
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

func mobileGymSuiteList(suite string) ([]string, error) {
	parts := strings.Split(suite, ",")
	suites := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !validSuiteName(part) {
			return nil, fmt.Errorf("invalid suite name %q (must be a safe relative path)", part)
		}
		suites = append(suites, part)
	}
	return suites, nil
}

func (s *Server) launchMobileGymRunner(suite, suiteType string, parallel, limit int) error {
	script, err := buildMobileGymLaunchScript(s.benchmarkDir, suite, suiteType, parallel, limit)
	if err != nil {
		return err
	}
	return exec.Command("sh", "-c", script).Start()
}

func buildMobileGymLaunchScript(benchmarkDir, suite, suiteType string, parallel, limit int) (string, error) {
	suites, err := mobileGymSuiteList(suite)
	if err != nil {
		return "", err
	}
	if len(suites) > 1 && suiteType != "mobilegym_builtin" && suiteType != "aiden" && suiteType != "" {
		return "", fmt.Errorf("multiple suites are only supported for mobilegym_builtin or aiden")
	}

	var suiteFlag string
	switch suiteType {
	case "aiden":
		if len(suites) > 1 {
			suiteFlag = fmt.Sprintf("--aiden-suites %s", shellQuote(strings.Join(suites, ",")))
		} else {
			suiteFlag = fmt.Sprintf("--aiden-suite %s", shellQuote(suites[0]))
		}
	case "mobilegym_builtin", "":
		if len(suites) > 1 {
			suiteFlag = fmt.Sprintf("--suites %s", shellQuote(strings.Join(suites, ",")))
		} else {
			suiteFlag = fmt.Sprintf("--suite %s", shellQuote(suites[0]))
		}
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
