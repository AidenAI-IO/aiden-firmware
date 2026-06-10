package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveBenchmarkDir picks the first existing benchmark root from this
// priority order:
//  1. CLI flag value (passed in via flagValue)
//  2. AIDEN_BENCHMARK_DIR env var
//  3. cfg.Dir from agent.conf [benchmark] benchmark_dir
//  4. /userdata/agent/benchmark (overridable via AIDEN_BENCHMARK_USERDATA_ROOT for tests)
//  5. <cwd>/benchmark
func resolveBenchmarkDir(flagValue string, cfg BenchmarkConfig) (string, error) {
	candidates := []string{}
	if flagValue != "" {
		candidates = append(candidates, flagValue)
	}
	if env := os.Getenv("AIDEN_BENCHMARK_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	if cfg.Dir != "" {
		candidates = append(candidates, cfg.Dir)
	}

	userdataRoot := os.Getenv("AIDEN_BENCHMARK_USERDATA_ROOT")
	if userdataRoot == "" {
		userdataRoot = "/"
	}
	candidates = append(candidates, filepath.Join(userdataRoot, "userdata", "agent", "benchmark"))

	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "benchmark"))
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c, nil
		}
	}

	return "", fmt.Errorf("no benchmark directory found among candidates: %v", candidates)
}
