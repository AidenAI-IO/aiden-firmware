package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareManagedPythonPathsCreatesManagedDirectories(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "agent", "python")
	tmp := filepath.Join(base, "tmp")

	paths, err := prepareManagedPythonPaths(root, tmp)
	if err != nil {
		t.Fatalf("prepareManagedPythonPaths() error = %v", err)
	}
	if paths.Root != root || paths.Tmp != tmp {
		t.Fatalf("prepareManagedPythonPaths() = %+v, want root=%q tmp=%q", paths, root, tmp)
	}

	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat(root) error = %v", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o755 {
		t.Fatalf("root mode = %v, want directory 0755", rootInfo.Mode())
	}

	tmpInfo, err := os.Stat(tmp)
	if err != nil {
		t.Fatalf("Stat(tmp) error = %v", err)
	}
	if !tmpInfo.IsDir() || tmpInfo.Mode().Perm() != 0o777 || tmpInfo.Mode()&os.ModeSticky == 0 {
		t.Fatalf("tmp mode = %v, want directory 01777", tmpInfo.Mode())
	}
}

func TestPrepareManagedPythonPathsRejectsSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir(target) error = %v", err)
	}
	root := filepath.Join(base, "python")
	if err := os.Symlink(target, root); err != nil {
		t.Skipf("Symlink unavailable: %v", err)
	}

	if _, err := prepareManagedPythonPaths(root, filepath.Join(base, "tmp")); err == nil {
		t.Fatal("prepareManagedPythonPaths() succeeded with symlink root, want error")
	}
}

func TestPrepareManagedPythonPathsRejectsSymlinkTemporaryDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir(target) error = %v", err)
	}
	tmp := filepath.Join(base, "tmp")
	if err := os.Symlink(target, tmp); err != nil {
		t.Skipf("Symlink unavailable: %v", err)
	}

	if _, err := prepareManagedPythonPaths(filepath.Join(base, "python"), tmp); err == nil {
		t.Fatal("prepareManagedPythonPaths() succeeded with symlink tmp, want error")
	}
}
