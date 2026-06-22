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
	Suites   []string
	SuiteDir string
	Kind     string
}

type benchmarkSkillOptLaunchSpec struct {
	RunID             string
	Skill             string
	Backend           string
	TrainSuite        string
	ValidationSuite   string
	Budget            int
	EditBudget        int
	MinDelta          float64
	MobileGymParallel int
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
			runnerArgs := "run --suite " + shellQuote(suite) + " --agent-url http://127.0.0.1:8080 --judge-model " + shellQuote(judgeModel) + " --agent-model " + shellQuote(agentModel) + " --state-file " + shellQuote(statePath) + " --llm-analysis --analysis-model " + shellQuote(judgeModel) + " "
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
		`%s `+
		`rm -f /tmp/benchmark_runner.pid; `+
		`echo '{"status":"idle"}' > %s; `+
		`exit $runner_rc)`,
		shellQuote(benchmarkDir), shellQuote(openRouterAPIKey), shellQuote(logPath), runnerScript, shellQuote(statePath))
	return startShellScript(script)
}

func startShellScript(script string) error {
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
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
		Suite             string   `json:"suite"`
		Suites            []string `json:"suites"`
		SuiteDir          string   `json:"suite_dir"`
		SuiteType         string   `json:"suite_type"`
		Mode              string   `json:"mode"`
		Parallel          int      `json:"parallel"`
		Limit             int      `json:"limit"`
		Skill             string   `json:"skill"`
		TrainSuite        string   `json:"train_suite"`
		ValidationSuite   string   `json:"validation_suite"`
		Budget            int      `json:"budget"`
		EditBudget        int      `json:"edit_budget"`
		MinDelta          *float64 `json:"min_delta"`
		OptimizerModel    string   `json:"optimizer_model"`
		SkillOptBackend   string   `json:"skillopt_backend"`
		MobileGymParallel int      `json:"mobilegym_parallel"`
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
		if !validSkillOptSuiteForSkill(req.Skill, req.TrainSuite) || !validSkillOptSuiteForSkill(req.Skill, req.ValidationSuite) {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, fmt.Sprintf("train_suite and validation_suite must be under skillopt/%s", req.Skill))
			return
		}
		backend := strings.TrimSpace(req.SkillOptBackend)
		if backend == "" {
			backend = "device"
		}
		if !validSkillOptBackend(backend) {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, fmt.Sprintf("invalid skillopt backend %q", backend))
			return
		}
		if backend == "mobilegym" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "SkillOpt MobileGym runs on the Mac MobileGym launcher, not on the board")
			return
		}
		mobileGymParallel := req.MobileGymParallel
		if mobileGymParallel <= 0 {
			mobileGymParallel = req.Parallel
		}
		if mobileGymParallel <= 0 {
			mobileGymParallel = 1
		}
		if mobileGymParallel > 16 {
			mobileGymParallel = 16
		}
		if req.Budget <= 0 {
			req.Budget = 10
		}
		if req.EditBudget <= 0 {
			req.EditBudget = 4
		}
		minDelta := 0.03
		if req.MinDelta != nil {
			if *req.MinDelta < 0 {
				s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, "min_delta must be non-negative")
				return
			}
			minDelta = *req.MinDelta
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
		skillsDir := s.benchmarkSkillOptSkillsDirFor(req.Skill)
		if strings.TrimSpace(skillsDir) == "" {
			s.writeBenchmarkRunError(w, req.Mode, http.StatusBadRequest, fmt.Sprintf("skillopt shared skill is not configured for %s", req.Skill))
			return
		}
		optimizer := strings.TrimSpace(req.OptimizerModel)
		if optimizer == "" {
			optimizer = defaultSkillOptOptimizerModel
		}
		now := time.Now().UTC()
		runID := fmt.Sprintf("skillopt-%s-%09d", now.Format("2006-01-02_150405"), now.UnixNano()%1_000_000_000)
		spec := benchmarkSkillOptLaunchSpec{
			RunID:             runID,
			Skill:             req.Skill,
			Backend:           backend,
			TrainSuite:        req.TrainSuite,
			ValidationSuite:   req.ValidationSuite,
			Budget:            req.Budget,
			EditBudget:        req.EditBudget,
			MinDelta:          minDelta,
			MobileGymParallel: mobileGymParallel,
		}
		stateJSON, _ := json.Marshal(map[string]any{
			"status":           "running",
			"mode":             "skillopt",
			"run_id":           runID,
			"skill":            req.Skill,
			"backend":          backend,
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
		if err := launch(spec, optimizer, judge, apiKey, agentModel, skillsDir); err != nil {
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
	return skill != "" && skill != "." && skill != ".." && !strings.Contains(skill, "/") && !strings.Contains(skill, "\\") && safeSuiteSegment.MatchString(skill)
}

func validSkillOptBackend(backend string) bool {
	return backend == "device" || backend == "mobilegym"
}

func validSkillOptSuiteForSkill(skill, suite string) bool {
	return validSuiteName(suite) && strings.HasPrefix(suite, "skillopt/"+skill+"/")
}

func (s *Server) launchSkillOptRunner(spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir string) error {
	script, err := buildSkillOptLaunchScriptWithStatePath(s.benchmarkDir, s.benchmarkStateFile(), spec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir)
	if err != nil {
		return err
	}
	return startShellScript(script)
}

func buildSkillOptLaunchScript(benchmarkDir string, spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir string) (string, error) {
	return buildSkillOptLaunchScriptWithStatePath(benchmarkDir, filepath.Join(benchmarkDir, "state.json"), spec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir)
}

func buildSkillOptLaunchScriptWithStatePath(benchmarkDir, statePath string, spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, openRouterAPIKey, agentModel, skillsDir string) (string, error) {
	if sanitizeRunID(spec.RunID) != spec.RunID || spec.RunID == "" {
		return "", fmt.Errorf("invalid run_id %q", spec.RunID)
	}
	if !validSkillOptSkillName(spec.Skill) {
		return "", fmt.Errorf("invalid skill name %q", spec.Skill)
	}
	if !validSkillOptSuiteForSkill(spec.Skill, spec.TrainSuite) {
		return "", fmt.Errorf("invalid train suite %q", spec.TrainSuite)
	}
	if !validSkillOptSuiteForSkill(spec.Skill, spec.ValidationSuite) {
		return "", fmt.Errorf("invalid validation suite %q", spec.ValidationSuite)
	}
	if spec.Backend == "" {
		spec.Backend = "device"
	}
	if !validSkillOptBackend(spec.Backend) {
		return "", fmt.Errorf("invalid skillopt backend %q", spec.Backend)
	}
	if spec.MobileGymParallel <= 0 {
		spec.MobileGymParallel = 1
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
	if strings.TrimSpace(statePath) == "" {
		statePath = filepath.Join(benchmarkDir, "state.json")
	}

	envPrefix := fmt.Sprintf("export OPENROUTER_API_KEY=%s && ", shellQuote(openRouterAPIKey))
	envPrefix += "export AIDEN_BENCHMARK_LLM_ANALYSIS='1' && "
	envPrefix += fmt.Sprintf("export AIDEN_BENCHMARK_ANALYSIS_MODEL=%s && ", shellQuote(judgeModel))
	if strings.TrimSpace(skillsDir) != "" {
		envPrefix += fmt.Sprintf("export AIDEN_SKILLS_DIR=%s && ", shellQuote(skillsDir))
	}
	if strings.TrimSpace(agentModel) != "" {
		envPrefix += fmt.Sprintf("export AIDEN_MODEL=%s && ", shellQuote(agentModel))
	}

	runnerArgs := fmt.Sprintf(
		"--skill %s --backend %s --mobilegym-parallel %d --train-suite %s --validation-suite %s --budget %d --edit-budget %d --min-delta %g --optimizer-model %s --judge-model %s --agent-url http://127.0.0.1:8080 --artifact-root %s --run-id %s --output %s ",
		shellQuote(spec.Skill), shellQuote(spec.Backend), spec.MobileGymParallel,
		shellQuote(spec.TrainSuite), shellQuote(spec.ValidationSuite),
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
		`echo '{"status":"idle"}' > %s)`,
		shellQuote(benchmarkDir), envPrefix, shellQuote(logPath), runnerArgs,
		shellQuote(logPath), shellQuote(pidFile), shellQuote(pidFile), shellQuote(statePath)), nil
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
	return startShellScript(script)
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
		`export AIDEN_BENCHMARK_LLM_ANALYSIS='1' && `+
		`export AIDEN_BENCHMARK_ANALYSIS_MODEL=%s && `+
		`(`+
		`echo 'Starting MobileGym benchmark...' > /tmp/mobilegym_run.log; `+
		`PARALLEL=%d ./parallel_run.sh %s %s `+
		`>> /tmp/mobilegym_run.log 2>&1 & `+
		`runner_pid=$!; echo $runner_pid > %s; `+
		`wait $runner_pid; rm -f %s; `+
		`echo '{"status":"idle"}' > %s)`,
		shellQuote(mobileGymDockerDir), shellQuote(defaultBenchmarkJudgeModel), parallel, suiteFlag, limitFlag,
		shellQuote(pidFile), shellQuote(pidFile), shellQuote(statePath)), nil
}
