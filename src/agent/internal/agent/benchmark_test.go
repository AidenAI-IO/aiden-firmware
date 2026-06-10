package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBenchmarkDir_FlagWins(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveBenchmarkDir(dir, BenchmarkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBenchmarkDir_ConfigDirWins(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveBenchmarkDir("", BenchmarkConfig{Dir: dir})
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
	got, err := resolveBenchmarkDir("", BenchmarkConfig{})
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
	got, err := resolveBenchmarkDir("", BenchmarkConfig{})
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
	_, err := resolveBenchmarkDir("", BenchmarkConfig{})
	if err == nil {
		t.Errorf("expected error when no candidate exists")
	}
}
