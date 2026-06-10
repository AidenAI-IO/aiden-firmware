package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
