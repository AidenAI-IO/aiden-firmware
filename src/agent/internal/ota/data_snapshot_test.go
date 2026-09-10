package ota

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotProtectedDataCopiesOnlyExistingProtectedFiles(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "userdata")
	oldRoot := protectedDataRoot
	protectedDataRoot = dataRoot
	defer func() { protectedDataRoot = oldRoot }()
	src := filepath.Join(dataRoot, "agent", "agent.toml")
	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("input_mode=\"text\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotProtectedData(filepath.Join(root, "ota"), "v2/rollback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "userdata", "agent", "agent.toml")); err != nil {
		t.Fatal(err)
	}
}
