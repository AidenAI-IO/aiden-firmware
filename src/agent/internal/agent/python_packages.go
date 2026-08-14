package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	managedPythonRoot     = "/userdata/agent/python"
	managedPythonTmp      = "/userdata/tmp"
	managedPythonRootMode = os.FileMode(0o755)
	managedPythonTmpMode  = os.FileMode(0o777) | os.ModeSticky
)

type managedPythonPaths struct {
	Root string
	Tmp  string
}

func prepareManagedPythonPaths(root, tmp string) (managedPythonPaths, error) {
	paths := managedPythonPaths{
		Root: filepath.Clean(root),
		Tmp:  filepath.Clean(tmp),
	}
	if err := ensureManagedPythonPaths(paths); err != nil {
		return managedPythonPaths{}, err
	}
	return paths, nil
}

func ensureManagedPythonPaths(paths managedPythonPaths) error {
	if err := ensureManagedPythonDirectory(paths.Root, managedPythonRootMode); err != nil {
		return fmt.Errorf("create managed python directory %q: %w", paths.Root, err)
	}
	if err := ensureManagedPythonDirectory(paths.Tmp, managedPythonTmpMode); err != nil {
		return fmt.Errorf("create managed python temporary directory %q: %w", paths.Tmp, err)
	}
	return nil
}

func ensureManagedPythonDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, mode.Perm()); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path is not a real directory")
	}
	return os.Chmod(path, mode)
}
