package ota

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireE2fsprogs resolves an e2fsprogs binary for the tests that build and
// inspect real ext4 images. The production image ships them in /usr/sbin (see
// DefaultDebugfsPath); developer machines keep them wherever their package
// manager put them, so resolve the path instead of assuming the Debian layout.
func requireE2fsprogs(t *testing.T, name string) string {
	t.Helper()
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, dir := range []string{"/usr/sbin", "/sbin"} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	t.Skipf("e2fsprogs %s not on PATH or in /usr/sbin; install e2fsprogs to run the ext4 image tests", name)
	return ""
}

// The ext4 tests resolve e2fsprogs from PATH, so pin the Debian image layout
// that the production UpdaterConfig defaults depend on.
func TestDefaultE2fsprogsPathsMatchDebianImageLayout(t *testing.T) {
	if DefaultDebugfsPath != "/usr/sbin/debugfs" || DefaultE2fsckPath != "/usr/sbin/e2fsck" {
		t.Fatalf("e2fsprogs defaults = %q, %q, want the Debian /usr/sbin layout", DefaultDebugfsPath, DefaultE2fsckPath)
	}
}
