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

func prepareManagedPythonPaths(root, tmp string) error {
	root = filepath.Clean(root)
	tmp = filepath.Clean(tmp)
	if err := ensureManagedPythonDirectory(root, managedPythonRootMode); err != nil {
		return fmt.Errorf("create managed python directory %q: %w", root, err)
	}
	if err := ensureManagedPythonDirectory(tmp, managedPythonTmpMode); err != nil {
		return fmt.Errorf("create managed python temporary directory %q: %w", tmp, err)
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
