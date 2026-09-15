package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryManagerClearAllRemovesEveryPersistedMemoryPlane(t *testing.T) {
	memoryDir := t.TempDir()
	for _, name := range []string{
		"long_term", "temporary", "device", "episodes", "lifecycle",
		"notifications", "session", "session_archive",
	} {
		path := filepath.Join(memoryDir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "data"), []byte("memory"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	extractionPath := filepath.Join(memoryDir, "extraction.yaml")
	if err := os.WriteFile(extractionPath, []byte("config"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := NewMemoryManager(memoryDir).ClearAll(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"long_term", "temporary", "device", "episodes", "lifecycle",
		"notifications", "session", "session_archive",
	} {
		if _, err := os.Stat(filepath.Join(memoryDir, name)); !os.IsNotExist(err) {
			t.Fatalf("memory plane %q still exists or stat failed: %v", name, err)
		}
	}
	if data, err := os.ReadFile(extractionPath); err != nil || string(data) != "config" {
		t.Fatalf("memory configuration changed: %q %v", data, err)
	}
}
