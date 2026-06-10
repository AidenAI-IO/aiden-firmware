package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	if len(got) != 2 {
		t.Fatalf("got %d suites: %+v", len(got), got)
	}
	for _, s := range got {
		if s["name"].(string) == "mine" && s["custom"] != true {
			t.Errorf("expected mine.custom = true")
		}
		if s["name"].(string) == "perception_v1" && s["custom"] != false {
			t.Errorf("expected perception_v1.custom = false")
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
		`fetch('/benchmark/suites')`,
		`fetch('/benchmark/runs')`,
		`fetch('/benchmark/status')`,
		`/benchmark/record`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("body missing %q", marker)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("ct = %q", ct)
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

func TestHandleBenchmarkRun_WritesStateFile(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	called := false
	s := &Server{
		benchmarkDir:       root,
		benchmarkStatePath: statePath,
		benchmarkLauncher: func(suite, judge, apiKey string) error {
			called = true
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
	data, _ := os.ReadFile(statePath)
	if !strings.Contains(string(data), `"running"`) {
		t.Errorf("state.json = %s", data)
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
