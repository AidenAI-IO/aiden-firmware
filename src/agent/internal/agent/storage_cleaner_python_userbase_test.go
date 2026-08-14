package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPythonUserBaseCleanerNormalCleanupOnlyRemovesOldTemporaryFiles(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	tmpDir := t.TempDir()

	// Create Python package structure (should NOT be cleaned in normal mode)
	libDir := filepath.Join(root, "lib", "python3.11", "site-packages")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(lib) error = %v", err)
	}
	packageFile := createPythonCleanerFile(t, libDir, "some_package.py", 100, now.Add(-30*24*time.Hour))

	staleTmp := createPythonCleanerFile(t, tmpDir, "stale.whl", 13, now.Add(-25*time.Hour))
	recentTmp := createPythonCleanerFile(t, tmpDir, "recent.whl", 17, now.Add(-time.Hour))

	cleaner := NewPythonUserBaseCleaner(root, tmpDir, 1)
	cleaner.now = func() time.Time { return now }

	estimate, err := cleaner.EstimateReclaimable(context.Background())
	if err != nil {
		t.Fatalf("EstimateReclaimable() error = %v", err)
	}
	if want := uint64(13); estimate != want {
		t.Fatalf("EstimateReclaimable() = %d, want %d (only stale tmp)", estimate, want)
	}

	freed, err := cleaner.Clean(context.Background())
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if want := uint64(13); freed != want {
		t.Fatalf("Clean() freed = %d, want %d", freed, want)
	}

	// Package files should be preserved
	assertPythonCleanerExists(t, packageFile)
	// Stale tmp should be removed
	assertPythonCleanerMissing(t, staleTmp)
	// Recent tmp should be preserved
	assertPythonCleanerExists(t, recentTmp)
}

func TestPythonUserBaseCleanerForceCleanupPreservesPackages(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	tmpDir := t.TempDir()

	// Create Python package structure
	libDir := filepath.Join(root, "lib", "python3.11", "site-packages")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(lib) error = %v", err)
	}
	packageFile := createPythonCleanerFile(t, libDir, "package.py", 50, now.Add(-time.Hour))

	// Create bin directory
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	binFile := createPythonCleanerFile(t, binDir, "some-cli", 10, now.Add(-time.Hour))

	staleTmp := createPythonCleanerFile(t, tmpDir, "stale.whl", 5, now.Add(-25*time.Hour))

	cleaner := NewPythonUserBaseCleaner(root, tmpDir, 1)
	cleaner.now = func() time.Time { return now }

	freed, err := cleaner.ForceClean(context.Background())
	if err != nil {
		t.Fatalf("ForceClean() error = %v", err)
	}
	if want := uint64(5); freed != want {
		t.Fatalf("ForceClean() freed = %d, want %d", freed, want)
	}

	assertPythonCleanerExists(t, root)
	assertPythonCleanerExists(t, packageFile)
	assertPythonCleanerExists(t, binFile)
	assertPythonCleanerMissing(t, staleTmp)
}

func TestPythonUserBaseCleanerEmergencyCleanupRemovesEntirePythonDirectory(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	tmpDir := t.TempDir()

	libDir := filepath.Join(root, "lib", "python3.11", "site-packages")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(lib) error = %v", err)
	}
	packageFile := createPythonCleanerFile(t, libDir, "package.py", 50, now.Add(-time.Hour))
	recentTmp := createPythonCleanerFile(t, tmpDir, "recent.whl", 5, now.Add(-time.Hour))

	cleaner := NewPythonUserBaseCleaner(root, tmpDir, 1)
	cleaner.now = func() time.Time { return now }

	freed, err := cleaner.EmergencyClean(context.Background())
	if err != nil {
		t.Fatalf("EmergencyClean() error = %v", err)
	}
	if freed != 50 {
		t.Fatalf("EmergencyClean() freed = %d, want 50", freed)
	}
	assertPythonCleanerMissing(t, root)
	assertPythonCleanerMissing(t, packageFile)
	assertPythonCleanerExists(t, recentTmp)
}

func TestPythonUserBaseCleanerIgnoresSymlinks(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	tmpDir := t.TempDir()

	staleFile := createPythonCleanerFile(t, tmpDir, "stale.whl", 11, now.Add(-25*time.Hour))
	symlinkPath := filepath.Join(tmpDir, "symlink.whl")
	if err := os.Symlink(staleFile, symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	cleaner := NewPythonUserBaseCleaner(root, tmpDir, 1)
	cleaner.now = func() time.Time { return now }

	freed, err := cleaner.Clean(context.Background())
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if want := uint64(11); freed != want {
		t.Fatalf("Clean() freed = %d, want %d", freed, want)
	}

	assertPythonCleanerMissing(t, staleFile)
	assertPythonCleanerExists(t, symlinkPath)
}

func TestPythonUserBaseCleanerEmergencyCleanupRejectsSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir(target) error = %v", err)
	}
	targetFile := createPythonCleanerFile(t, target, "package.py", 11, time.Now())
	root := filepath.Join(base, "python")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("Symlink unavailable: %v", err)
	}

	cleaner := NewPythonUserBaseCleaner(root, t.TempDir(), 1)
	if _, err := cleaner.EmergencyClean(context.Background()); err == nil {
		t.Fatal("EmergencyClean() succeeded with symlink root, want error")
	}
	assertPythonCleanerExists(t, root)
	assertPythonCleanerExists(t, targetFile)
}

func createPythonCleanerFile(t *testing.T, dir, name string, size int, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
	return path
}

func assertPythonCleanerExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		t.Errorf("path %q does not exist, want exists", path)
	}
}

func assertPythonCleanerMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("path %q exists, want removed", path)
	} else if !os.IsNotExist(err) {
		t.Errorf("Lstat(%q) error = %v, want IsNotExist", path, err)
	}
}
