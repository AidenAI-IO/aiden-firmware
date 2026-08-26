package agentpath

import (
	"path/filepath"
	"testing"
)

func TestContextManagerSessionFoldersAreRoleSeparated(t *testing.T) {
	configDir := t.TempDir()
	backend := ContextManagerSessionFolder(configDir)
	user := UserContextManagerSessionFolder(configDir)
	if backend != filepath.Join(configDir, "sessions", "backend") {
		t.Fatalf("backend session folder = %q", backend)
	}
	if user != filepath.Join(configDir, "sessions", "user") {
		t.Fatalf("user session folder = %q", user)
	}
	if backend == user {
		t.Fatal("backend and user session folders must differ")
	}
}
