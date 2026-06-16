package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	deviceOperatorTrainSuite        = "skillopt/device-operator/device_operator_train"
	deviceOperatorVerificationSuite = "skillopt/device-operator/device_operator_verification"
)

func writeBenchmarkSkill(t *testing.T, skillsDir, skill string) {
	t.Helper()
	dir := filepath.Join(skillsDir, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+skill+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBenchmarkDir_FlagWins(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveBenchmarkDir(dir, BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_ConfigDirWins(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveBenchmarkDir("", BenchmarkConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_EnvWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIDEN_BENCHMARK_DIR", dir)
	got, err := ResolveBenchmarkDir("", BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_UserdataExists(t *testing.T) {
	tmp := t.TempDir()
	userdata := filepath.Join(tmp, "userdata", "agent", "benchmark")
	if err := os.MkdirAll(userdata, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIDEN_BENCHMARK_DIR", "")
	t.Setenv("AIDEN_BENCHMARK_USERDATA_ROOT", tmp)
	got, err := ResolveBenchmarkDir("", BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != userdata {
		t.Errorf("got %q, want %q", got, userdata)
	}
}

func TestResolveBenchmarkDir_NotFound(t *testing.T) {
	t.Setenv("AIDEN_BENCHMARK_DIR", "")
	t.Setenv("AIDEN_BENCHMARK_USERDATA_ROOT", t.TempDir())
	t.Chdir(t.TempDir())
	_, err := ResolveBenchmarkDir("", BenchmarkConfig{})
	if err == nil {
		t.Errorf("expected error when no candidate exists")
	}
}

func TestHandleBenchmarkSuites_ListsJSON(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites")
	os.MkdirAll(filepath.Join(suites, "custom"), 0o755)
	os.WriteFile(filepath.Join(suites, "perception_v1.json"), []byte(`{"name":"perception_v1","tasks":[]}`), 0o644)
	os.WriteFile(filepath.Join(suites, "unit_tools.json"), []byte(`{"name":"unit_tools","kind":"unit","tasks":[]}`), 0o644)
	os.WriteFile(filepath.Join(suites, "custom", "mine.json"), []byte(`{"name":"mine","tasks":[]}`), 0o644)
	os.WriteFile(filepath.Join(suites, "._noise.json"), []byte("{}"), 0o644)

	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/suites", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 3 {
		t.Fatalf("got %d suites: %+v", len(got), got)
	}
	for _, s := range got {
		if s["name"].(string) == "mine" && s["custom"] != true {
			t.Errorf("expected mine.custom = true")
		}
		if s["name"].(string) == "perception_v1" && s["custom"] != false {
			t.Errorf("expected perception_v1.custom = false")
		}
		if s["name"].(string) == "unit_tools" && s["kind"] != "unit" {
			t.Errorf("expected unit_tools.kind = unit, got %+v", s)
		}
		if s["name"].(string) == "perception_v1" && s["kind"] != "benchmark" {
			t.Errorf("expected perception_v1.kind = benchmark, got %+v", s)
		}
	}
}

func TestHandleBenchmarkSuites_NoBenchmarkDir(t *testing.T) {
	s := &Server{benchmarkDir: ""}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/suites", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandleBenchmarkRuns_ReverseSorted(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	for _, id := range []string{"2026-05-01_010101", "2026-06-10_010101", "2026-04-15_010101"} {
		os.MkdirAll(filepath.Join(runs, id), 0o755)
	}
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkRuns(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3: %+v", len(got), got)
	}
	if got[0]["run_id"] != "2026-06-10_010101" {
		t.Errorf("first run_id = %q, want 2026-06-10_010101", got[0]["run_id"])
	}
	if got[1]["run_id"] != "2026-05-01_010101" {
		t.Errorf("second run_id = %q, want 2026-05-01_010101", got[1]["run_id"])
	}
	if got[2]["run_id"] != "2026-04-15_010101" {
		t.Errorf("third run_id = %q, want 2026-04-15_010101", got[2]["run_id"])
	}
}

func TestHandleBenchmarkRuns_Caps20(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	for i := 0; i < 25; i++ {
		os.MkdirAll(filepath.Join(runs, fmt.Sprintf("2026-06-10_%06d", i)), 0o755)
	}
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkRuns(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 20 {
		t.Errorf("got %d runs, want 20", len(got))
	}
}

func TestHandleBenchmarkRuns_IncludesModelStatusAndProgress(t *testing.T) {
	root := t.TempDir()
	runID := "2026-06-11_120000"
	runDir := filepath.Join(root, "runs", runID)
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(`{
		"suite_path":"/userdata/agent/benchmark/suites/memory_v1.json",
		"agent_model":"google/gemini-3.5-flash",
		"judge_config":{"provider":"openrouter","model":"anthropic/claude-sonnet-4"},
		"totals":{"tasks":17,"passed":12,"failed":5}
	}`), 0o644)
	os.WriteFile(filepath.Join(root, "state.json"), []byte(`{
		"status":"running",
		"run_id":"running-1",
		"suite":"memory_v1.json",
		"total":17,
		"current":1,
		"completed":0,
		"current_task":"task-1"
	}`), 0o644)

	s := &Server{
		benchmarkDir: root,
		runtime: &Runtime{config: Config{
			Model:     ModelConfig{Provider: "openrouter", Model: "google/gemini-3.5-flash"},
			Benchmark: BenchmarkConfig{JudgeModel: "bytedance-seed/seed-2.0-lite"},
		}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	s.handleBenchmarkRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) < 2 {
		t.Fatalf("expected running row plus completed run, got %+v", got)
	}
	running := got[0]
	if running["status"] != "running" || running["progress"] != "1/17" || running["model"] != "google/gemini-3.5-flash" {
		t.Fatalf("unexpected running row: %+v", running)
	}
	completed := got[1]
	if completed["status"] != "done" || completed["progress"] != "17/17" || completed["model"] != "google/gemini-3.5-flash" {
		t.Fatalf("unexpected completed row: %+v", completed)
	}
}

func TestHandleBenchmarkRuns_CompletedRunDoesNotUseJudgeModelAsAgentModel(t *testing.T) {
	root := t.TempDir()
	runID := "2026-06-11_120000"
	runDir := filepath.Join(root, "runs", runID)
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, "manifest.json"), []byte(`{
		"suite_path":"/userdata/agent/benchmark/suites/perception_v1.json",
		"judge_config":{"provider":"openrouter","model":"bytedance-seed/seed-2.0-lite"},
		"totals":{"tasks":2,"timeout":2}
	}`), 0o644)

	s := &Server{benchmarkDir: root}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	s.handleBenchmarkRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Fatalf("expected one completed run, got %+v", got)
	}
	if got[0]["model"] == "bytedance-seed/seed-2.0-lite" {
		t.Fatalf("completed run should not display judge model as agent model: %+v", got[0])
	}
}

func TestHandleBenchmarkRuns_NoBenchmarkDir(t *testing.T) {
	s := &Server{benchmarkDir: ""}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/runs", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkRuns(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandleBenchmarkReport_ServesHTML(t *testing.T) {
	root := t.TempDir()
	id := "2026-06-10_010101"
	os.MkdirAll(filepath.Join(root, "runs", id), 0o755)
	body := []byte("<html><body>Hello</body></html>")
	os.WriteFile(filepath.Join(root, "runs", id, "report.html"), body, 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/report/"+id, nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkReport(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkReport_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s := &Server{benchmarkDir: root}
	for _, p := range []string{
		"/benchmark/report/../etc/passwd",
		"/benchmark/report/foo/bar",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		s.handleBenchmarkReport(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("path %s: should not 200", p)
		}
	}
}

func TestHandleBenchmarkReport_NotFound(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/report/2026-06-10", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkReport(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleBenchmarkStatus_PassThroughIdle(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"status":"idle"}`), 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/status", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkStatus(rec, req)
	if !strings.Contains(rec.Body.String(), `"status":"idle"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestHandleBenchmarkStatus_SelfHealStaleRunning(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	os.WriteFile(statePath, []byte(`{"status":"running","suite":"x"}`), 0o644)
	old := time.Now().Add(-30 * time.Second)
	os.Chtimes(statePath, old, old)
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	os.WriteFile(pidFile, []byte("99999999"), 0o644)
	s := &Server{benchmarkDir: root, benchmarkPIDFile: pidFile}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/status", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkStatus(rec, req)
	if !strings.Contains(rec.Body.String(), `"recovered":true`) {
		t.Errorf("body = %s", rec.Body.String())
	}
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), `"idle"`) {
		t.Errorf("state.json after self-heal: %s", data)
	}
}

func TestHandleBenchmarkStatus_GracePeriod(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"status":"running"}`), 0o644)
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	os.WriteFile(pidFile, []byte("99999999"), 0o644)
	s := &Server{benchmarkDir: root, benchmarkPIDFile: pidFile}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/status", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkStatus(rec, req)
	if !strings.Contains(rec.Body.String(), `"running"`) {
		t.Errorf("expected to keep running within grace; got %s", rec.Body.String())
	}
}

func TestHandleBenchmarkLog_TailsLast64KB(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	os.WriteFile(logPath, []byte(strings.Repeat("a", 100*1024)), 0o644)
	s := &Server{benchmarkLogPath: logPath}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/log", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkLog(rec, req)
	if rec.Body.Len() > 64*1024 {
		t.Errorf("body len = %d, want <= 64KB", rec.Body.Len())
	}
	if rec.Body.Len() < 60*1024 {
		t.Errorf("body len = %d, expected ~64KB", rec.Body.Len())
	}
}

func TestHandleBenchmarkLog_MissingReturnsEmpty(t *testing.T) {
	s := &Server{benchmarkLogPath: filepath.Join(t.TempDir(), "nonexistent")}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/log", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkLog(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("code=%d len=%d", rec.Code, rec.Body.Len())
	}
}

func TestHandleBenchmarkIndex_ServesHTMLWithRouterButtons(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/benchmark", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`MOBILEGYM_LOCAL_BASE='http://127.0.0.1:4174'`,
		`MOBILEGYM_HELPER_BASE='http://127.0.0.1:4175'`,
		`<label><input type="radio" name="mode" value="skillopt"`,
		`id="skillOptConfig"`,
		`id="skillOptBackendSelect"`,
		`id="skillSelect"`,
		`id="validationSuiteSelect"`,
		`id="budgetInput"`,
		`id="editBudgetInput"`,
		`id="minDeltaInput"`,
		`function loadSkills()`,
		`function loadSkillOptTargets()`,
		`function syncSkillOptSuites()`,
		`/benchmark/skills`,
		`/benchmark/skillopt-targets`,
		`payload.mode='skillopt';`,
		`payload.skillopt_backend=`,
		`payload.mobilegym_parallel=`,
		`payload.skill=`,
		`payload.train_suite=`,
		`payload.validation_suite=`,
		`Start the Mac MobileGym launcher first`,
		`id="startLauncherBtn"`,
		`function startMobileGymLauncher()`,
		`new AbortController()`,
		`controller.abort()`,
		`signal:controller.signal`,
		`fetch(MOBILEGYM_HELPER_BASE+'/start'`,
		`benchmarkEndpoint('/benchmark/suites')`,
		`benchmarkEndpoint('/benchmark/run')`,
		`benchmarkEndpoint('/benchmark/status')`,
		`benchmarkEndpoint('/benchmark/log')`,
		`benchmarkEndpoint('/benchmark/runs')`,
		`benchmarkEndpoint('/benchmark/report/')`,
		`fetch(benchmarkEndpoint('/benchmark/status'))`,
		`<th>Status</th><th>Progress</th><th>Model</th>`,
		`progressText(r)`,
		`r.model||'—'`,
		`id="refreshBtn"`,
		`function refreshBenchmark(){loadSuites();if(getMode()!=='skillopt')loadSkills();loadRuns();loadStatus();loadLog()}`,
		`function logPanelError(context,e)`,
		`function readErrorResponse(r)`,
		`logPanelError('Start run failed',e)`,
		`logPanelError('Start unit run failed',e)`,
		`id="unitSelect"`,
		`id="runUnitBtn"`,
		`function startRunUnit()`,
		`id="selectedSuiteTags"`,
		`function configureSuiteSelect()`,
		`function renderSelectedSuiteTags()`,
		`function addSelectedSuiteKey(key)`,
		`function removeSelectedSuiteKey(key)`,
		`function selectedSuiteKeys()`,
		`function syncRunButtons(status)`,
		`payload.suites=selected.map(function(x){return x.item.path||x.key});`,
		`payload.suites=selected.map(function(x){return mobileGymSuiteName(x.item,x.key)});`,
		`x.kind==='unit'`,
		`reportCell(r)`,
		`Not ready`,
		`r.report_path`,
		`payload.suite=mobileGymSuiteName(item,key);`,
		`payload.board_url=location.origin;`,
		`/benchmark/record`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("body missing %q", marker)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("ct = %q", ct)
	}
	for _, marker := range []string{`id="limitInput"`, `payload.limit=Number(lim);`} {
		if strings.Contains(body, marker) {
			t.Errorf("body should not expose %q", marker)
		}
	}
	for _, marker := range []string{`tr.innerHTML='<td>'+r.run_id`, `+'<td>'+reportCell(r)+'</td>'`} {
		if strings.Contains(body, marker) {
			t.Errorf("history table should avoid unsafe innerHTML marker %q", marker)
		}
	}
	if strings.Contains(body, `alert(String(e))`) {
		t.Errorf("run errors should be rendered into the Live Log, not alert-only")
	}
	if strings.Contains(body, `s.multiple=mg;`) {
		t.Errorf("MobileGym suite picker should use selected tags, not native multiple select")
	}
}

func TestHandleBenchmarkSkills_ListsConfiguredSkills(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "device-operator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "device-operator", "SKILL.md"), []byte("---\nname: device-operator\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "incomplete"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		benchmarkDir: root,
		runtime: &Runtime{config: Config{
			SkillsDirs: []string{skillsDir},
		}},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/benchmark/skills", nil)
	s.handleBenchmarkSkills(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["name"] != "device-operator" {
		t.Fatalf("unexpected skills response: %+v", got)
	}
}

func TestHandleBenchmarkSkillOptTargetsDiscoversSkillScopedSuites(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "suites", "skillopt", "device-operator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "device_operator_train.json"), []byte(`{"name":"train","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "device_operator_verification.json"), []byte(`{"name":"verification","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/skillopt-targets", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkSkillOptTargets(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"skill":"device-operator"`) {
		t.Fatalf("body missing skill: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `skillopt/device-operator/device_operator_train`) {
		t.Fatalf("body missing train suite: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `skillopt/device-operator/device_operator_verification`) {
		t.Fatalf("body missing verification suite: %s", rec.Body.String())
	}
}

func TestHandleBenchmarkSkillOptTargetsIgnoresFlatSkillOptSuites(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites", "skillopt")
	if err := os.MkdirAll(suites, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suites, "device_operator_skillopt_v1.json"), []byte(`{"name":"old","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(suites, "device-operator")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "device_operator_train.json"), []byte(`{"name":"train","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodGet, "/benchmark/skillopt-targets", nil)
	rec := httptest.NewRecorder()
	s.handleBenchmarkSkillOptTargets(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "device_operator_skillopt_v1") {
		t.Fatalf("flat skillopt suite should be ignored: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `skillopt/device-operator/device_operator_train`) {
		t.Fatalf("nested skillopt suite should be present: %s", rec.Body.String())
	}
}

func TestHandleBenchmarkRun_RejectsMissingSuite(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleBenchmarkRun_SkillOptMode(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		called            bool
		skill             string
		backend           string
		trainSuite        string
		validationSuite   string
		budget            int
		editBudget        int
		minDelta          float64
		mobileGymParallel int
		apiKey            string
		skillsDir         string
	}{}
	skillsDir := filepath.Join(root, "skills")
	writeBenchmarkSkill(t, skillsDir, "device-operator")
	s := &Server{
		runtime: &Runtime{config: Config{
			Benchmark:  BenchmarkConfig{JudgeModel: "judge/model", APIKey: "sk-judge"},
			SkillsDirs: []string{skillsDir},
		}},
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkSkillOptLauncher: func(spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, apiKey, agentModel, gotSkillsDir string) error {
			captured.called = true
			captured.skill = spec.Skill
			captured.backend = spec.Backend
			captured.trainSuite = spec.TrainSuite
			captured.validationSuite = spec.ValidationSuite
			captured.budget = spec.Budget
			captured.editBudget = spec.EditBudget
			captured.minDelta = spec.MinDelta
			captured.mobileGymParallel = spec.MobileGymParallel
			captured.apiKey = apiKey
			captured.skillsDir = gotSkillsDir
			return nil
		},
	}

	body := `{"mode":"skillopt","skill":"device-operator","train_suite":"skillopt/device-operator/device_operator_train","validation_suite":"skillopt/device-operator/device_operator_verification","budget":2,"edit_budget":3,"min_delta":0.02}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !captured.called {
		t.Fatal("expected skillopt launcher invoked")
	}
	if captured.skill != "device-operator" || captured.backend != "device" || captured.trainSuite != deviceOperatorTrainSuite ||
		captured.validationSuite != deviceOperatorVerificationSuite || captured.budget != 2 ||
		captured.editBudget != 3 || captured.minDelta != 0.02 || captured.mobileGymParallel != 1 ||
		captured.apiKey != "sk-judge" || captured.skillsDir != skillsDir {
		t.Fatalf("unexpected skillopt launch args: %+v", captured)
	}
	data, _ := os.ReadFile(statePath)
	state := string(data)
	for _, want := range []string{`"status":"running"`, `"mode":"skillopt"`, `"skill":"device-operator"`, `"backend":"device"`, `"run_id":"skillopt-`} {
		if !strings.Contains(state, want) {
			t.Fatalf("state missing %s: %s", want, state)
		}
	}
}

func TestHandleBenchmarkRun_SkillOptMobileGymBackend(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	writeBenchmarkSkill(t, skillsDir, "device-operator")
	captured := struct {
		backend           string
		mobileGymParallel int
	}{}
	s := &Server{
		runtime: &Runtime{config: Config{
			Benchmark:  BenchmarkConfig{APIKey: "sk-judge"},
			SkillsDirs: []string{skillsDir},
		}},
		benchmarkDir: root,
		benchmarkSkillOptLauncher: func(spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, apiKey, agentModel, gotSkillsDir string) error {
			captured.backend = spec.Backend
			captured.mobileGymParallel = spec.MobileGymParallel
			return nil
		},
	}

	body := `{"mode":"skillopt","skill":"device-operator","skillopt_backend":"mobilegym","mobilegym_parallel":4,"train_suite":"skillopt/device-operator/device_operator_train","validation_suite":"skillopt/device-operator/device_operator_verification"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if captured.backend != "mobilegym" || captured.mobileGymParallel != 4 {
		t.Fatalf("unexpected backend args: %+v", captured)
	}
}

func TestHandleBenchmarkRun_SkillOptRejectsInvalidBackend(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	writeBenchmarkSkill(t, skillsDir, "device-operator")
	s := &Server{
		runtime: &Runtime{config: Config{
			Benchmark:  BenchmarkConfig{APIKey: "sk-judge"},
			SkillsDirs: []string{skillsDir},
		}},
		benchmarkDir: root,
		benchmarkSkillOptLauncher: func(spec benchmarkSkillOptLaunchSpec, optimizerModel, judgeModel, apiKey, agentModel, gotSkillsDir string) error {
			t.Fatal("launcher should not be called")
			return nil
		},
	}

	body := `{"mode":"skillopt","skill":"device-operator","skillopt_backend":"simulator","train_suite":"skillopt/device-operator/device_operator_train","validation_suite":"skillopt/device-operator/device_operator_verification"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkRun_SkillOptRejectsUnsafeSkills(t *testing.T) {
	for _, skill := range []string{".", "..", "../device-operator"} {
		t.Run(skill, func(t *testing.T) {
			s := &Server{benchmarkDir: t.TempDir()}
			body := fmt.Sprintf(`{"mode":"skillopt","skill":%q,"train_suite":"skillopt/device-operator/device_operator_train","validation_suite":"skillopt/device-operator/device_operator_verification"}`, skill)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(body))
			s.handleBenchmarkRun(rec, req)

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid skill name") {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleBenchmarkRun_SkillOptRejectsCrossSkillSuites(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	writeBenchmarkSkill(t, skillsDir, "device-operator")
	s := &Server{
		runtime: &Runtime{config: Config{
			Benchmark:  BenchmarkConfig{APIKey: "sk-judge"},
			SkillsDirs: []string{skillsDir},
		}},
		benchmarkDir: root,
	}
	body := `{"mode":"skillopt","skill":"device-operator","train_suite":"skillopt/planner/device_operator_train","validation_suite":"skillopt/device-operator/device_operator_verification"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must be under skillopt/device-operator") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkRun_SkillOptRequiresSharedSkillSource(t *testing.T) {
	root := t.TempDir()
	writeBenchmarkSkill(t, filepath.Join(root, "mobilegym", "config", "skills"), "device-operator")
	s := &Server{
		runtime:      &Runtime{config: Config{Benchmark: BenchmarkConfig{APIKey: "sk-judge"}}},
		benchmarkDir: root,
	}
	body := `{"mode":"skillopt","skill":"device-operator","train_suite":"skillopt/device-operator/device_operator_train","validation_suite":"skillopt/device-operator/device_operator_verification"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "skillopt shared skill is not configured") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestBenchmarkSkillOptSkillsDirForSkipsMobileGymTemplate(t *testing.T) {
	root := t.TempDir()
	writeBenchmarkSkill(t, filepath.Join(root, "mobilegym", "config", "skills"), "device-operator")
	s := &Server{benchmarkDir: root}

	if got := s.benchmarkSkillOptSkillsDirFor("device-operator"); got != "" {
		t.Fatalf("got %q, want empty shared skill dir", got)
	}
}

func TestBenchmarkSkillOptSkillsDirForUsesSharedSkill(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared-skills")
	writeBenchmarkSkill(t, shared, "device-operator")
	writeBenchmarkSkill(t, filepath.Join(root, "mobilegym", "config", "skills"), "device-operator")
	s := &Server{
		benchmarkDir: root,
		runtime: &Runtime{config: Config{
			SkillsDirs: []string{shared},
		}},
	}

	if got := s.benchmarkSkillOptSkillsDirFor("device-operator"); got != shared {
		t.Fatalf("got %q, want %q", got, shared)
	}
}

func TestHandleBenchmarkRun_SkillOptRejectsMissingSkill(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run", strings.NewReader(`{"mode":"skillopt","train_suite":"skillopt/train","validation_suite":"skillopt/validation"}`))
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkRun_WritesStateFile(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	called := false
	var gotJudge, gotAPIKey string
	s := &Server{
		runtime: &Runtime{config: Config{
			Model:     ModelConfig{APIKey: "sk-model"},
			Benchmark: BenchmarkConfig{JudgeModel: "judge/model", APIKey: "sk-judge"},
		}},
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			called = true
			gotJudge = judge
			gotAPIKey = apiKey
			if spec.Suite != "/tmp/x.json" || spec.Kind != "benchmark" {
				t.Fatalf("launch spec = %+v", spec)
			}
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(`{"suite":"/tmp/x.json"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("launcher was not invoked")
	}
	if gotJudge != "judge/model" {
		t.Errorf("judge = %q, want judge/model", gotJudge)
	}
	if gotAPIKey != "sk-judge" {
		t.Errorf("apiKey = %q, want benchmark api key", gotAPIKey)
	}
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), `"running"`) {
		t.Errorf("state.json = %s", data)
	}
}

func TestHandleBenchmarkRun_RejectsMissingJudgeAPIKey(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	logPath := filepath.Join(root, "benchmark_run.log")
	called := false
	s := &Server{
		runtime:            &Runtime{config: Config{Benchmark: BenchmarkConfig{JudgeModel: "judge/model"}}},
		benchmarkDir:       root,
		benchmarkLogPath:   logPath,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			called = true
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(`{"suite":"/tmp/x.json"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("launcher should not be invoked without benchmark api key")
	}
	stateData, err := os.ReadFile(statePath)
	if err == nil && strings.Contains(string(stateData), `"status":"running"`) {
		t.Fatalf("unexpected running state written: %s", stateData)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read state file: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected run error to be written to log: %v", err)
	}
	if !strings.Contains(string(logData), "benchmark judge api_key is not configured") {
		t.Fatalf("log missing run error: %s", logData)
	}
}

func TestLaunchBenchmarkRunner_PassesStateFileToPythonRunner(t *testing.T) {
	tmp := t.TempDir()
	capturePath := filepath.Join(tmp, "captured-script")
	fakeSh := filepath.Join(tmp, "sh")
	if err := os.WriteFile(fakeSh, []byte("#!/bin/sh\nprintf '%s' \"$2\" > \"$CAPTURE_SCRIPT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_SCRIPT", capturePath)

	s := &Server{
		benchmarkDir:       "/userdata/agent/benchmark",
		benchmarkStatePath: "/userdata/agent/benchmark/state.json",
	}
	if err := s.launchBenchmarkRunner(
		benchmarkLaunchSpec{Suite: "/userdata/agent/benchmark/suites/memory_v1.json", Kind: "benchmark"},
		"judge/model",
		"sk-test",
		"google/gemini-3.5-flash",
	); err != nil {
		t.Fatalf("launchBenchmarkRunner returned error: %v", err)
	}

	var script string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(capturePath)
		if err == nil && len(data) > 0 {
			script = string(data)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if script == "" {
		t.Fatal("fake shell did not capture launch script")
	}
	if !strings.Contains(script, "--state-file '/userdata/agent/benchmark/state.json'") {
		t.Fatalf("launch script should pass --state-file to Python runner, got: %s", script)
	}
}

func TestHandleBenchmarkRun_DoesNotWriteRunningStateWhenAidenLaunchFails(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	s := &Server{
		runtime: &Runtime{config: Config{
			Benchmark: BenchmarkConfig{APIKey: "sk-judge"},
		}},
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			return errors.New("boom")
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(`{"suite":"/tmp/x.json"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	stateData, err := os.ReadFile(statePath)
	if err == nil && strings.Contains(string(stateData), `"status":"running"`) {
		t.Fatalf("unexpected running state written: %s", stateData)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read state file: %v", err)
	}
}

func TestHandleBenchmarkRun_DoesNotWriteRunningStateWhenMobileGymLaunchFails(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	logPath := filepath.Join(root, "mobilegym_run.log")
	s := &Server{
		benchmarkDir:       root,
		benchmarkLogPath:   logPath,
		benchmarkStatePath: statePath,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			return errors.New("boom")
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(`{"suite":"clock","suite_type":"mobilegym_builtin","mode":"mobilegym"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	stateData, err := os.ReadFile(statePath)
	if err == nil && strings.Contains(string(stateData), `"status":"running"`) {
		t.Fatalf("unexpected running state written: %s", stateData)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read state file: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected mobilegym run error to be written to log: %v", err)
	}
	if !strings.Contains(string(logData), "mobilegym launch failed: boom") {
		t.Fatalf("log missing mobilegym launch error: %s", logData)
	}
}

func TestHandleBenchmarkRun_UnitSuite(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	unitSuite := filepath.Join(root, "suites", "unit_smoke.json")
	os.MkdirAll(filepath.Dir(unitSuite), 0o755)
	os.WriteFile(unitSuite, []byte(`{"kind":"unit","tasks":[]}`), 0o644)
	var got benchmarkLaunchSpec
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			got = spec
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(fmt.Sprintf(`{"suite":%q}`, unitSuite)))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if got.Suite != unitSuite || got.Kind != "unit" {
		t.Fatalf("launch spec = %+v", got)
	}
}

func TestHandleBenchmarkRun_UnitSuiteDir(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	suiteDir := filepath.Join(root, "suites", "unit")
	var got benchmarkLaunchSpec
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			got = spec
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/run",
		strings.NewReader(fmt.Sprintf(`{"suite_dir":%q}`, suiteDir)))
	rec := httptest.NewRecorder()
	s.handleBenchmarkRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if got.SuiteDir != suiteDir || got.Kind != "unit" {
		t.Fatalf("launch spec = %+v", got)
	}
}

func TestHandleBenchmarkImport_Validates(t *testing.T) {
	s := &Server{benchmarkDir: t.TempDir()}
	cases := []struct {
		body   string
		status int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"name":"","json":"{}"}`, http.StatusBadRequest},
		{`{"name":"bad/name","json":"{}"}`, http.StatusBadRequest},
		{`{"name":"ok","json":"not json"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/import", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		s.handleBenchmarkImport(rec, req)
		if rec.Code != tc.status {
			t.Errorf("body %s: status = %d, want %d", tc.body, rec.Code, tc.status)
		}
	}
}

func TestHandleBenchmarkImport_WritesCustomFile(t *testing.T) {
	root := t.TempDir()
	s := &Server{
		benchmarkDir:            root,
		benchmarkSuiteValidator: func(path string) error { return nil },
	}
	body := `{"name":"mine","json":"{\"name\":\"mine\",\"tasks\":[]}"}`
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/import", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBenchmarkImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "custom", "mine.json")); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

func TestHandleBenchmarkDelete_OnlyCustom(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites", "custom"), 0o755)
	os.WriteFile(filepath.Join(root, "suites", "custom", "mine.json"), []byte("{}"), 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/delete",
		strings.NewReader(`{"name":"mine"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "custom", "mine.json")); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestHandleBenchmarkDelete_RejectsBuiltin(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "suites"), 0o755)
	os.WriteFile(filepath.Join(root, "suites", "perception_v1.json"), []byte("{}"), 0o644)
	s := &Server{benchmarkDir: root}
	req := httptest.NewRequest(http.MethodPost, "/benchmark/suites/delete",
		strings.NewReader(`{"name":"perception_v1"}`))
	rec := httptest.NewRecorder()
	s.handleBenchmarkDelete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "perception_v1.json")); err != nil {
		t.Errorf("builtin file should still exist: %v", err)
	}
}

func TestHandleBenchmarkSuites_AidenModeOmitsBuiltins(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites")
	os.MkdirAll(suites, 0o755)
	os.WriteFile(filepath.Join(suites, "memory_v1.json"),
		[]byte(`{"name":"memory_v1","tasks":[]}`), 0o644)

	// MobileGym all_tasks.txt should be ignored in aiden mode
	mgDir := filepath.Join(root, "mobilegym", "suites")
	os.MkdirAll(mgDir, 0o755)
	os.WriteFile(filepath.Join(mgDir, "all_tasks.txt"),
		[]byte("clock.AddAlarm\nclock.CountAlarms\nalipay.CheckBalance\n"), 0o644)

	s := &Server{benchmarkDir: root}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/suites?mode=aiden", nil)
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	for _, item := range got {
		if item["type"] == "mobilegym_builtin" {
			t.Fatalf("aiden mode should not include builtins, got %+v", got)
		}
	}
	if len(got) != 1 || got[0]["name"] != "memory_v1" {
		t.Fatalf("expected only memory_v1, got %+v", got)
	}
}

func TestHandleBenchmarkSuites_MobileGymModeIncludesBuiltins(t *testing.T) {
	root := t.TempDir()
	suites := filepath.Join(root, "suites")
	os.MkdirAll(suites, 0o755)
	os.WriteFile(filepath.Join(suites, "memory_v1.json"),
		[]byte(`{"name":"memory_v1","tasks":[]}`), 0o644)

	mgDir := filepath.Join(root, "mobilegym", "suites")
	os.MkdirAll(mgDir, 0o755)
	os.WriteFile(filepath.Join(mgDir, "all_tasks.txt"),
		[]byte("clock.AddAlarm\nclock.CountAlarms\nalipay.CheckBalance\n"), 0o644)

	s := &Server{benchmarkDir: root}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/suites?mode=mobilegym", nil)
	s.handleBenchmarkSuites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)

	types := map[string]int{}
	names := map[string]int{}
	for _, item := range got {
		types[item["type"].(string)]++
		count := 0
		if v, ok := item["task_count"].(float64); ok {
			count = int(v)
		}
		names[item["name"].(string)] = count
	}
	if types["aiden"] != 1 {
		t.Fatalf("expected 1 aiden suite, got %d (%+v)", types["aiden"], got)
	}
	if types["mobilegym_builtin"] != 2 {
		t.Fatalf("expected 2 builtin suites (clock, alipay), got %d (%+v)",
			types["mobilegym_builtin"], got)
	}
	if names["clock"] != 2 || names["alipay"] != 1 {
		t.Fatalf("task counts wrong: %+v", names)
	}
}

func TestHandleBenchmarkRun_AidenModeDefault(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		called bool
		suite  string
	}{}
	s := &Server{
		runtime:            &Runtime{config: Config{Benchmark: BenchmarkConfig{APIKey: "sk-judge"}}},
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			captured.called = true
			captured.suite = spec.Suite
			return nil
		},
	}

	body := `{"suite":"memory_v1.json"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !captured.called || captured.suite != "memory_v1.json" {
		t.Fatalf("expected aiden launcher invoked with memory_v1.json, got %+v", captured)
	}
}

func TestHandleBenchmarkRun_AidenModeSuites(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		called bool
		suites []string
	}{}
	s := &Server{
		runtime:            &Runtime{config: Config{Benchmark: BenchmarkConfig{APIKey: "sk-judge"}}},
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(spec benchmarkLaunchSpec, judge, apiKey, agentModel string) error {
			captured.called = true
			captured.suites = append([]string(nil), spec.Suites...)
			return nil
		},
	}

	body := `{"suites":["memory_v1.json","perception/perception_v1.json"],"mode":"aiden"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !captured.called {
		t.Fatalf("expected aiden launcher invoked")
	}
	if strings.Join(captured.suites, ",") != "memory_v1.json,perception/perception_v1.json" {
		t.Fatalf("unexpected suites: %+v", captured.suites)
	}
}

func TestHandleBenchmarkRun_MobileGymMode(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		called    bool
		suite     string
		suiteType string
		parallel  int
		limit     int
	}{}
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			captured.called = true
			captured.suite = suite
			captured.suiteType = suiteType
			captured.parallel = parallel
			captured.limit = limit
			return nil
		},
	}

	body := `{"suite":"memory_v1","suite_type":"aiden","mode":"mobilegym","parallel":4,"limit":10}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !captured.called {
		t.Fatalf("expected mobilegym launcher invoked, got %+v", captured)
	}
	if captured.suite != "memory_v1" || captured.suiteType != "aiden" ||
		captured.parallel != 4 || captured.limit != 10 {
		t.Fatalf("unexpected mobilegym launch args: %+v", captured)
	}
}

func TestHandleBenchmarkRun_MobileGymBuiltin(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		suite     string
		suiteType string
	}{}
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			captured.suite = suite
			captured.suiteType = suiteType
			return nil
		},
	}

	body := `{"suite":"clock","suite_type":"mobilegym_builtin","mode":"mobilegym","parallel":2}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if captured.suite != "clock" || captured.suiteType != "mobilegym_builtin" {
		t.Fatalf("unexpected mobilegym launch args: %+v", captured)
	}
}

func TestHandleBenchmarkRun_MobileGymBuiltinSuites(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	captured := struct {
		suite     string
		suiteType string
	}{}
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			captured.suite = suite
			captured.suiteType = suiteType
			return nil
		},
	}

	body := `{"suites":["clock","phone_control_v1"],"suite_type":"mobilegym_builtin","mode":"mobilegym","parallel":2}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if captured.suite != "clock,phone_control_v1" || captured.suiteType != "mobilegym_builtin" {
		t.Fatalf("unexpected mobilegym launch args: %+v", captured)
	}
}

func TestHandleBenchmarkRun_MobileGymAidenSuites(t *testing.T) {
	root := t.TempDir()
	captured := struct {
		suite     string
		suiteType string
	}{}
	s := &Server{
		benchmarkDir: root,
		benchmarkMobileGymLauncher: func(suite, suiteType string, parallel, limit int) error {
			captured.suite = suite
			captured.suiteType = suiteType
			return nil
		},
	}

	body := `{"suites":["memory_v1","perception_v1"],"suite_type":"aiden","mode":"mobilegym"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/benchmark/run", strings.NewReader(body))
	s.handleBenchmarkRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if captured.suite != "memory_v1,perception_v1" || captured.suiteType != "aiden" {
		t.Fatalf("unexpected mobilegym launch args: %+v", captured)
	}
}

func TestHandleBenchmarkLog_MobileGymMode(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "mobilegym_run.log")
	os.WriteFile(logFile, []byte("hello mobilegym"), 0o644)

	s := &Server{benchmarkLogPath: logFile}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/log?mode=mobilegym", nil)
	s.handleBenchmarkLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hello mobilegym") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestHandleBenchmarkLog_SkillOptMode(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "skillopt_run.log")
	os.WriteFile(logFile, []byte("hello skillopt"), 0o644)

	s := &Server{benchmarkLogPath: logFile}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/benchmark/log?mode=skillopt", nil)
	s.handleBenchmarkLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hello skillopt") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestBuildSkillOptLaunchScript(t *testing.T) {
	script, err := buildSkillOptLaunchScript("/bench", benchmarkSkillOptLaunchSpec{
		RunID:             "skillopt-2026-06-15_120000",
		Skill:             "device-operator",
		Backend:           "mobilegym",
		TrainSuite:        deviceOperatorTrainSuite,
		ValidationSuite:   deviceOperatorVerificationSuite,
		Budget:            2,
		EditBudget:        3,
		MinDelta:          0.02,
		MobileGymParallel: 4,
	}, "optimizer/model", "judge/model", "sk-test", "agent/model", "/bench/config/skills")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"cd '/bench'",
		"export OPENROUTER_API_KEY='sk-test'",
		"export AIDEN_SKILLS_DIR='/bench/config/skills'",
		"from runner.skillopt.main import cli",
		"--skill 'device-operator'",
		"--backend 'mobilegym'",
		"--mobilegym-parallel 4",
		"--train-suite 'skillopt/device-operator/device_operator_train'",
		"--validation-suite 'skillopt/device-operator/device_operator_verification'",
		"--budget 2",
		"--edit-budget 3",
		"--min-delta 0.02",
		"--optimizer-model 'optimizer/model'",
		"--judge-model 'judge/model'",
		"--artifact-root '/bench/runs'",
		"--run-id 'skillopt-2026-06-15_120000'",
		"--output '/bench/runs/skillopt-2026-06-15_120000/best_skill.md'",
		"/tmp/skillopt_run.log",
		"/tmp/benchmark_runner.pid",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("launch script missing %q: %s", want, script)
		}
	}
}

func TestBuildSkillOptLaunchScript_RejectsUnsafeInputs(t *testing.T) {
	base := benchmarkSkillOptLaunchSpec{
		RunID:             "skillopt-2026-06-15_120000",
		Skill:             "device-operator",
		Backend:           "device",
		TrainSuite:        deviceOperatorTrainSuite,
		ValidationSuite:   deviceOperatorVerificationSuite,
		Budget:            1,
		EditBudget:        1,
		MinDelta:          0.01,
		MobileGymParallel: 1,
	}
	for _, mutate := range []func(*benchmarkSkillOptLaunchSpec){
		func(s *benchmarkSkillOptLaunchSpec) { s.Skill = "." },
		func(s *benchmarkSkillOptLaunchSpec) { s.Skill = ".." },
		func(s *benchmarkSkillOptLaunchSpec) { s.Skill = "../bad" },
		func(s *benchmarkSkillOptLaunchSpec) { s.TrainSuite = "../bad" },
		func(s *benchmarkSkillOptLaunchSpec) { s.TrainSuite = "skillopt/planner/train" },
		func(s *benchmarkSkillOptLaunchSpec) { s.ValidationSuite = "bad suite" },
		func(s *benchmarkSkillOptLaunchSpec) { s.Backend = "simulator" },
	} {
		spec := base
		mutate(&spec)
		if _, err := buildSkillOptLaunchScript("/bench", spec, "optimizer", "judge", "key", "agent", ""); err == nil {
			t.Fatalf("expected unsafe input to be rejected: %+v", spec)
		}
	}
}

func TestBuildMobileGymLaunchScript_AidenSuite(t *testing.T) {
	script, err := buildMobileGymLaunchScript("/bench", "memory_v1", "aiden", 4, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "cd '/bench/mobilegym/docker'") {
		t.Errorf("expected MobileGym docker dir under benchmarkDir, got: %s", script)
	}
	if !strings.Contains(script, "--aiden-suite 'memory_v1'") {
		t.Errorf("expected --aiden-suite flag, got: %s", script)
	}
	if !strings.Contains(script, "PARALLEL=4") {
		t.Errorf("expected PARALLEL=4, got: %s", script)
	}
	if !strings.Contains(script, "--limit 10") {
		t.Errorf("expected --limit 10, got: %s", script)
	}
	if !strings.Contains(script, "/tmp/mobilegym_runner.pid") {
		t.Errorf("expected pid file write, got: %s", script)
	}
}

func TestBuildMobileGymLaunchScript_AidenSuites(t *testing.T) {
	script, err := buildMobileGymLaunchScript("/bench", "memory_v1,perception_v1", "aiden", 4, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "--aiden-suites 'memory_v1,perception_v1'") {
		t.Errorf("expected --aiden-suites flag, got: %s", script)
	}
	if !strings.Contains(script, "--limit 10") {
		t.Errorf("expected --limit 10, got: %s", script)
	}
}

func TestBuildMobileGymLaunchScript_MobileGymBuiltin(t *testing.T) {
	script, err := buildMobileGymLaunchScript("/bench", "clock", "mobilegym_builtin", 2, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "--suite 'clock'") {
		t.Errorf("expected --suite flag, got: %s", script)
	}
	if strings.Contains(script, "--aiden-suite") {
		t.Errorf("should not have --aiden-suite for builtin: %s", script)
	}
	if strings.Contains(script, "--limit") {
		t.Errorf("limit=0 should omit --limit flag: %s", script)
	}
}

func TestBuildMobileGymLaunchScript_MobileGymBuiltins(t *testing.T) {
	script, err := buildMobileGymLaunchScript("/bench", "clock,phone_control_v1", "mobilegym_builtin", 2, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "--suites 'clock,phone_control_v1'") {
		t.Errorf("expected --suites flag, got: %s", script)
	}
	if strings.Contains(script, "--suite 'clock,phone_control_v1'") {
		t.Errorf("multiple builtins should not use singular --suite: %s", script)
	}
}

func TestBuildMobileGymLaunchScript_RejectsUnknownSuiteType(t *testing.T) {
	_, err := buildMobileGymLaunchScript("/bench", "memory_v1", "evil", 1, 0)
	if err == nil {
		t.Fatal("expected error for unknown suite_type")
	}
	if !strings.Contains(err.Error(), "unknown suite_type") {
		t.Errorf("expected suite_type error, got: %v", err)
	}
}

func TestBuildMobileGymLaunchScript_RejectsBadSuiteName(t *testing.T) {
	for _, bad := range []string{".", "..", "../etc/passwd", "foo/../bar", "foo//bar", "foo;rm -rf /", "foo bar"} {
		_, err := buildMobileGymLaunchScript("/bench", bad, "aiden", 1, 0)
		if err == nil {
			t.Errorf("expected error for suite name %q", bad)
		}
	}
}

func TestBuildMobileGymLaunchScript_AllowsNestedSuiteName(t *testing.T) {
	script, err := buildMobileGymLaunchScript("/bench", "perception/perception_v1", "aiden", 1, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "--aiden-suite 'perception/perception_v1'") {
		t.Errorf("expected nested --aiden-suite flag, got: %s", script)
	}
}

func TestBuildMobileGymLaunchScript_QuotesBenchmarkDir(t *testing.T) {
	script, err := buildMobileGymLaunchScript("/path with spaces", "memory_v1", "aiden", 1, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "'/path with spaces/mobilegym/docker'") {
		t.Errorf("benchmarkDir should be quoted, got: %s", script)
	}
}
