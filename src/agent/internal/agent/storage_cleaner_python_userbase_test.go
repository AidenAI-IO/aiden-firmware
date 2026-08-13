package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	// Override managedPythonTmpPath for tests to use a relative tmp directory
	// instead of the hardcoded /userdata/tmp.
	managedPythonTmpPath = func(root string) string {
		return filepath.Join(root, "tmp")
	}
}

func TestPythonUserBaseCleanerNormalCleanupProtectsActiveAndRecentEntries(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	active := createPythonCleanerDirectory(t, root, "py3.11", 5, now.Add(-30*24*time.Hour))
	staleVersion := createPythonCleanerDirectory(t, root, "py3.10", 11, now.Add(-8*24*time.Hour))
	recentVersion := createPythonCleanerDirectory(t, root, "py3.9", 7, now.Add(-2*24*time.Hour))
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(tmp) error = %v", err)
	}
	staleTmp := createPythonCleanerFile(t, tmpDir, "stale.whl", 13, now.Add(-25*time.Hour))
	recentTmp := createPythonCleanerFile(t, tmpDir, "recent.whl", 17, now.Add(-time.Hour))

	cleaner := NewPythonUserBaseCleaner(root, 1, func(context.Context) (string, error) {
		return "3.11", nil
	})
	cleaner.now = func() time.Time { return now }

	estimate, err := cleaner.EstimateReclaimable(context.Background())
	if err != nil {
		t.Fatalf("EstimateReclaimable() error = %v", err)
	}
	if want := uint64(24); estimate != want {
		t.Fatalf("EstimateReclaimable() = %d, want %d", estimate, want)
	}

	freed, err := cleaner.Clean(context.Background())
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if want := uint64(24); freed != want {
		t.Fatalf("Clean() freed = %d, want %d", freed, want)
	}

	assertPythonCleanerExists(t, active)
	assertPythonCleanerMissing(t, staleVersion)
	assertPythonCleanerExists(t, recentVersion)
	assertPythonCleanerMissing(t, staleTmp)
	assertPythonCleanerExists(t, recentTmp)
	if info, statErr := os.Stat(active); statErr != nil {
		t.Fatalf("Stat(active) error = %v", statErr)
	} else if !info.ModTime().Equal(now) {
		t.Errorf("active mtime = %v, want %v", info.ModTime(), now)
	}
}

func TestPythonUserBaseCleanerForceCleanupOnlyBypassesVersionRetention(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	active := createPythonCleanerDirectory(t, root, "py3.11", 5, now.Add(-30*24*time.Hour))
	recentVersion := createPythonCleanerDirectory(t, root, "py3.10", 11, now.Add(-time.Hour))
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(tmp) error = %v", err)
	}
	recentTmp := createPythonCleanerFile(t, tmpDir, "install.whl", 13, now.Add(-time.Hour))

	cleaner := NewPythonUserBaseCleaner(root, 1, func(context.Context) (string, error) {
		return "3.11", nil
	})
	cleaner.now = func() time.Time { return now }

	freed, err := cleaner.ForceClean(context.Background())
	if err != nil {
		t.Fatalf("ForceClean() error = %v", err)
	}
	if want := uint64(11); freed != want {
		t.Fatalf("ForceClean() freed = %d, want %d", freed, want)
	}
	assertPythonCleanerExists(t, active)
	assertPythonCleanerMissing(t, recentVersion)
	assertPythonCleanerExists(t, recentTmp)
}

func TestPythonUserBaseCleanerIgnoresSymlinksAndUnknownDirectories(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	createPythonCleanerDirectory(t, root, "py3.11", 5, now.Add(-30*24*time.Hour))
	unknown := createPythonCleanerDirectory(t, root, "py3.10.backup", 7, now.Add(-30*24*time.Hour))
	target := createPythonCleanerDirectory(t, t.TempDir(), "outside", 11, now.Add(-30*24*time.Hour))
	symlink := filepath.Join(root, "py3.10")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	// Create tmp directory so cleaner doesn't error
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(tmp) error = %v", err)
	}

	cleaner := NewPythonUserBaseCleaner(root, 1, func(context.Context) (string, error) {
		return "3.11", nil
	})
	cleaner.now = func() time.Time { return now }
	if freed, err := cleaner.ForceClean(context.Background()); err != nil {
		t.Fatalf("ForceClean() error = %v", err)
	} else if freed != 0 {
		t.Fatalf("ForceClean() freed = %d, want 0", freed)
	}

	assertPythonCleanerExists(t, unknown)
	assertPythonCleanerExists(t, symlink)
	assertPythonCleanerExists(t, target)
}

func TestPythonUserBaseCleanerDoesNotFollowNestedSymlinks(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	createPythonCleanerDirectory(t, root, "py3.11", 5, now)
	staleVersion := createPythonCleanerDirectory(t, root, "py3.10", 7, now.Add(-8*24*time.Hour))
	target := createPythonCleanerDirectory(t, t.TempDir(), "outside", 11, now.Add(-30*24*time.Hour))
	symlink := filepath.Join(staleVersion, "outside-link")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	if err := os.Chtimes(staleVersion, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatalf("Chtimes(stale version) error = %v", err)
	}
	// Create tmp directory so cleaner doesn't error
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(tmp) error = %v", err)
	}

	cleaner := NewPythonUserBaseCleaner(root, 1, func(context.Context) (string, error) {
		return "3.11", nil
	})
	cleaner.now = func() time.Time { return now }
	if _, err := cleaner.Clean(context.Background()); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}

	assertPythonCleanerMissing(t, staleVersion)
	assertPythonCleanerExists(t, target)
}

func createPythonCleanerDirectory(t *testing.T, root, name string, size int, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	createPythonCleanerFile(t, path, "payload", size, modTime)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
	return path
}

func createPythonCleanerFile(t *testing.T, root, name string, size int, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
	return path
}

func assertPythonCleanerExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("Lstat(%q) error = %v, want path to exist", path, err)
	}
}

func assertPythonCleanerMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Lstat(%q) error = %v, want path to be removed", path, err)
	}
}
