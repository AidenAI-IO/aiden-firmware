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
	"time"
)

type benchmarkLaunchSpec struct {
	Suite    string
	SuiteDir string
	Kind     string
}

type benchmarkSkillOptLaunchSpec struct {
	RunID           string
	Skill           string
	TrainSuite      string
	ValidationSuite string
	Budget          int
	EditBudget      int
	MinDelta        float64
}

const defaultSkillOptOptimizerModel = "anthropic/claude-opus-4-7"

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
	if mode == "skillopt" {
		return "/tmp/skillopt_run.log"
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
		Suite           string  `json:"suite"`
		SuiteDir        string  `json:"suite_dir"`
		SuiteType       string  `json:"suite_type"`
		Mode            string  `json:"mode"`
		Parallel        int     `json:"parallel"`
		Limit           int     `json:"limit"`
		Skill           string  `json:"skill"`
		TrainSuite      string  `json:"train_suite"`
		ValidationSuite string  `json:"validation_suite"`
		Budget          int     `json:"budget"`
		EditBudget      int     `json:"edit_budget"`
		MinDelta        float64 `json:"min_delta"`
		OptimizerModel  string  `json:"optimizer_model"`
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
	case "skillopt":
		if strings.TrimSpace(req.Skill) == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "missing skill field")
			return
		}
		if strings.TrimSpace(req.TrainSuite) == "" || strings.TrimSpace(req.ValidationSuite) == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "missing train_suite or validation_suite field")
			return
		}
		if !validSkillOptSkillName(req.Skill) {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, fmt.Sprintf("invalid skill name %q", req.Skill))
			return
		}
		if !validSuiteName(req.TrainSuite) || !validSuiteName(req.ValidationSuite) {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "train_suite and validation_suite must be safe relative suite names")
			return
		}
		if req.Budget <= 0 {
			req.Budget = 10
		}
		if req.EditBudget <= 0 {
			req.EditBudget = 4
		}
		if req.MinDelta < 0 {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "min_delta must be non-negative")
			return
		}
		if req.MinDelta == 0 {
			req.MinDelta = 0.03
		}
		apiKey := ""
		judge := defaultBenchmarkJudgeModel
		agentModel := ""
		if s.runtime != nil {
			apiKey = s.runtime.config.Benchmark.APIKey
			if strings.TrimSpace(s.runtime.config.Benchmark.JudgeModel) != "" {
				judge = s.runtime.config.Benchmark.JudgeModel
			}
			agentModel = s.runtime.config.Model.Model
		}
		if strings.TrimSpace(apiKey) == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "skillopt api_key is not configured")
			return
		}
		optimizer := strings.TrimSpace(req.OptimizerModel)
		if optimizer == "" {
			optimizer = defaultSkillOptOptimizerModel
		}
		runID := "skillopt-" + time.Now().UTC().Format("2006-01-02_150405")
		spec := benchmarkSkillOptLaunchSpec{
			RunID:           runID,
			Skill:           req.Skill,
			TrainSuite:      req.TrainSuite,
			ValidationSuite: req.ValidationSuite,
			Budget:          req.Budget,
			EditBudget:      req.EditBudget,
			MinDelta:        req.MinDelta,
		}
		stateJSON, _ := json.Marshal(map[string]any{
			"status":           "running",
			"mode":             "skillopt",
			"run_id":           runID,
			"skill":            req.Skill,
			"suite":            req.TrainSuite,
			"validation_suite": req.ValidationSuite,
			"model":            agentModel,
		})
		if err := os.WriteFile(statePath, stateJSON, 0o644); err != nil {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, err.Error())
			return
		}
		launch := s.benchmarkSkillOptLauncher
		if launch == nil {
			launch = s.launchSkillOptRunner
		}
		if err := launch(spec, optimizer, judge, apiKey, agentModel, s.benchmarkPrimarySkillsDir()); err != nil {
			_ = os.WriteFile(statePath, []byte(`{"status":"idle"}`), 0o644)
			s.writeBenchmarkRunError(w, req.Mode, http.StatusInternalServerError, "skillopt launch failed: "+err.Error())
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

func validSkillOptSkillName(skill string) bool {
	return skill != "" && !strings.Contains(skill, "/") && !strings.Contains(skill, "\\") && safeSuiteSegment.MatchString(skill)
}

func (s *Server) launchSkillOptRunner(spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir string) error {
	script, err := buildSkillOptLaunchScript(s.benchmarkDir, spec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir)
	if err != nil {
		return err
	}
	return exec.Command("sh", "-c", script).Start()
}

func buildSkillOptLaunchScript(benchmarkDir string, spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir string) (string, error) {
	if sanitizeRunID(spec.RunID) != spec.RunID || spec.RunID == "" {
		return "", fmt.Errorf("invalid run_id %q", spec.RunID)
	}
	if !validSkillOptSkillName(spec.Skill) {
		return "", fmt.Errorf("invalid skill name %q", spec.Skill)
	}
	if !validSuiteName(spec.TrainSuite) {
		return "", fmt.Errorf("invalid train suite %q", spec.TrainSuite)
	}
	if !validSuiteName(spec.ValidationSuite) {
		return "", fmt.Errorf("invalid validation suite %q", spec.ValidationSuite)
	}
	if spec.Budget <= 0 {
		return "", fmt.Errorf("budget must be positive")
	}
	if spec.EditBudget <= 0 {
		return "", fmt.Errorf("edit_budget must be positive")
	}
	if spec.MinDelta < 0 {
		return "", fmt.Errorf("min_delta must be non-negative")
	}
	if optimizerModel == "" {
		optimizerModel = defaultSkillOptOptimizerModel
	}
	if judgeModel == "" {
		judgeModel = defaultBenchmarkJudgeModel
	}

	runsDir := filepath.Join(benchmarkDir, "runs")
	runDir := filepath.Join(runsDir, spec.RunID)
	logPath := "/tmp/skillopt_run.log"
	pidFile := "/tmp/benchmark_runner.pid"
	statePath := filepath.Join(benchmarkDir, "state.json")

	envPrefix := fmt.Sprintf("export OPENROUTER_API_KEY=%s && ", shellQuote(openRouterAPIKey))
	if strings.TrimSpace(skillsDir) != "" {
		envPrefix += fmt.Sprintf("export AIDEN_SKILLS_DIR=%s && ", shellQuote(skillsDir))
	}
	if strings.TrimSpace(agentModel) != "" {
		envPrefix += fmt.Sprintf("export AIDEN_MODEL=%s && ", shellQuote(agentModel))
	}

	runnerArgs := fmt.Sprintf(
		"--skill %s --train-suite %s --validation-suite %s --budget %d --edit-budget %d --min-delta %g --optimizer-model %s --judge-model %s --agent-url http://127.0.0.1:8080 --artifact-root %s --run-id %s --output %s ",
		shellQuote(spec.Skill), shellQuote(spec.TrainSuite), shellQuote(spec.ValidationSuite),
		spec.Budget, spec.EditBudget, spec.MinDelta, shellQuote(optimizerModel), shellQuote(judgeModel),
		shellQuote(runsDir), shellQuote(spec.RunID), shellQuote(filepath.Join(runDir, "best_skill.md")),
	)

	return fmt.Sprintf(`cd %s && `+
		`%s`+
		`(`+
		`echo 'Starting SkillOpt benchmark...' > %s; `+
		`python3 -c 'import sys;sys.path.insert(0,".");from runner.skillopt.main import cli;sys.exit(cli())' `+
		`%s`+
		`>> %s 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > %s; `+
		`wait $runner_pid; rm -f %s; `+
		`echo '{"status":"idle"}' > %s) &`,
		shellQuote(benchmarkDir), envPrefix, shellQuote(logPath), runnerArgs,
		shellQuote(logPath), shellQuote(pidFile), shellQuote(pidFile), shellQuote(statePath)), nil
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
