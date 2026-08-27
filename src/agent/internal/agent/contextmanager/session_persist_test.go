package contextmanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveCurrentSessionAtomicallyReplacesFile(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionPath := filepath.Join(sessionFolder, ".current_session")
	if err := os.WriteFile(sessionPath, []byte("old-session"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	before, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("Stat() before save error = %v", err)
	}

	const sessionID = "new-session"
	if err := saveCurrentSession(sessionFolder, sessionID); err != nil {
		t.Fatalf("saveCurrentSession() error = %v", err)
	}

	after, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("Stat() after save error = %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("saveCurrentSession() updated the existing file instead of atomically replacing it")
	}
	if got := fetchCurrentSession(sessionFolder); got != sessionID {
		t.Fatalf("fetchCurrentSession() = %q, want %q", got, sessionID)
	}
	if got := after.Mode().Perm(); got != 0o644 {
		t.Fatalf(".current_session permissions = %o, want 644", got)
	}

	entries, err := os.ReadDir(sessionFolder)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".current_session-") {
			t.Errorf("temporary file was not removed: %s", entry.Name())
		}
	}
}
