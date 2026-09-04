package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// shippedConfigPaths are the agent.toml files published with the firmware.
// overlay/userdata is what the firmware container task rsyncs onto the device;
// src/agent/config/agent.toml is a symlink to it, so this one path covers both
// the device config and the documented example. Users read it as a statement
// about the defaults, so it may not contradict DefaultConfig().
var shippedConfigPaths = []string{
	filepath.Join("overlay", "userdata", "agent", "agent.toml"),
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// TestShippedConfigsAgreeWithDefaults pins every value the shipped agent.toml
// files spell out to the canonical default. A value that differs is a silent
// trap: the config web UI shows the file's value but labels the field with the
// DefaultConfig() one, and a config lacking the section resolves to something
// else again.
func TestShippedConfigsAgreeWithDefaults(t *testing.T) {
	repoRoot := repoRootForTest(t)
	defaults := DefaultConfig()

	for _, relative := range shippedConfigPaths {
		path := filepath.Join(repoRoot, relative)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", relative, err)
		}
		shipped, err := LoadResolvedConfig(path)
		if err != nil {
			t.Fatalf("load %s: %v", relative, err)
		}

		// Fields whose defaults are resolved at load time rather than in
		// DefaultConfig() are compared through their accessors.
		if got, want := shipped.QuickCapture.GPIOPin, defaults.QuickCapture.GPIOPin; got != want {
			t.Errorf("%s: quick_capture.gpio_pin = %d, DefaultConfig() = %d", relative, got, want)
		}
		if got, want := shipped.QuickCapture.EnabledOrDefault(), defaults.QuickCapture.EnabledOrDefault(); got != want {
			t.Errorf("%s: quick_capture.enabled = %v, DefaultConfig() = %v", relative, got, want)
		}
		if got, want := shipped.QuickCapture.ScreenMemoryTTLOrDefault(), defaults.QuickCapture.ScreenMemoryTTLOrDefault(); got != want {
			t.Errorf("%s: quick_capture.screen_memory_ttl = %q, DefaultConfig() = %q", relative, got, want)
		}
		if got, want := shipped.InputModeOrDefault(), defaults.InputModeOrDefault(); got != want {
			t.Errorf("%s: input_mode = %q, DefaultConfig() = %q", relative, got, want)
		}
	}
}
