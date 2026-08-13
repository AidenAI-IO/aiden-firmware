package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestResolveManagedPythonPathsUsesInterpreterMajorMinor(t *testing.T) {
	root := t.TempDir()
	paths, err := resolveManagedPythonPaths(context.Background(), root, func(context.Context) (string, error) {
		return "3.11\n", nil
	})
	if err != nil {
		t.Fatalf("resolveManagedPythonPaths() error = %v", err)
	}

	if paths.Root != root {
		t.Errorf("Root = %q, want %q", paths.Root, root)
	}
	if want := filepath.Join(root, "py3.11"); paths.UserBase != want {
		t.Errorf("UserBase = %q, want %q", paths.UserBase, want)
	}
	if want := filepath.Join(root, "tmp"); paths.Tmp != want {
		t.Errorf("Tmp = %q, want %q", paths.Tmp, want)
	}
}

func TestPrepareManagedPythonPathsCreatesAndTouchesManagedDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "python")
	paths, err := prepareManagedPythonPaths(context.Background(), root, func(context.Context) (string, error) {
		return "3.12", nil
	})
	if err != nil {
		t.Fatalf("prepareManagedPythonPaths() error = %v", err)
	}

	// prepareManagedPythonPaths only creates Root and UserBase, not Tmp.
	// Tmp is a system-level shared directory that should be created by system initialization.
	for _, path := range []string{paths.Root, paths.UserBase} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat(%q) error = %v", path, statErr)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", path)
		}
	}

	if info, statErr := os.Stat(paths.UserBase); statErr != nil {
		t.Fatalf("Stat(UserBase) error = %v", statErr)
	} else if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("UserBase mtime = %v, want recently touched", info.ModTime())
	}
}

func TestResolveManagedPythonPathsRejectsUnexpectedInterpreterOutput(t *testing.T) {
	for _, output := range []string{"3", "3.11.6", "Python 3.11", "3.x", ""} {
		t.Run(output, func(t *testing.T) {
			if _, err := resolveManagedPythonPaths(context.Background(), t.TempDir(), func(context.Context) (string, error) {
				return output, nil
			}); err == nil {
				t.Fatalf("resolveManagedPythonPaths(%q) succeeded, want error", output)
			}
		})
	}
}

func TestQueryRunningPythonVersionHonorsContextDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script")
	}
	pythonPath := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(fake python3) error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := queryPythonVersion(ctx, pythonPath)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queryRunningPythonVersion() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("queryRunningPythonVersion() took %v, want prompt cancellation", elapsed)
	}
}
