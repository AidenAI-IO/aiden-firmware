package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	managedPythonRoot                = "/userdata/agent/python"
	managedPythonVersionQueryTimeout = 5 * time.Second
)

var managedPythonVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

type managedPythonPaths struct {
	Root     string
	UserBase string
	Tmp      string
}

type managedPythonVersionQuery func(context.Context) (string, error)

func queryRunningPythonVersion(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	queryCtx, cancel := context.WithTimeout(ctx, managedPythonVersionQueryTimeout)
	defer cancel()

	output, err := exec.CommandContext(queryCtx, "python3", "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')").Output()
	if err != nil {
		if contextErr := queryCtx.Err(); contextErr != nil {
			return "", contextErr
		}
		return "", err
	}
	return string(output), nil
}

func resolveManagedPythonPaths(ctx context.Context, root string, query managedPythonVersionQuery) (managedPythonPaths, error) {
	if query == nil {
		return managedPythonPaths{}, fmt.Errorf("python version query is required")
	}
	version, err := query(ctx)
	if err != nil {
		return managedPythonPaths{}, fmt.Errorf("query python version: %w", err)
	}
	version = strings.TrimSpace(version)
	if !managedPythonVersionPattern.MatchString(version) {
		return managedPythonPaths{}, fmt.Errorf("invalid python major.minor version %q", version)
	}

	root = filepath.Clean(root)
	return managedPythonPaths{
		Root:     root,
		UserBase: filepath.Join(root, "py"+version),
		Tmp:      filepath.Join(root, "tmp"),
	}, nil
}

func prepareManagedPythonPaths(ctx context.Context, root string, query managedPythonVersionQuery) (managedPythonPaths, error) {
	paths, err := resolveManagedPythonPaths(ctx, root, query)
	if err != nil {
		return managedPythonPaths{}, err
	}
	if err := ensureManagedPythonPaths(paths, time.Now()); err != nil {
		return managedPythonPaths{}, err
	}
	return paths, nil
}

func ensureManagedPythonPaths(paths managedPythonPaths, now time.Time) error {
	for _, path := range []string{paths.Root, paths.UserBase, paths.Tmp} {
		if err := ensureManagedPythonDirectory(path); err != nil {
			return fmt.Errorf("create managed python directory %q: %w", path, err)
		}
	}
	if err := os.Chtimes(paths.UserBase, now, now); err != nil {
		return fmt.Errorf("touch active python directory %q: %w", paths.UserBase, err)
	}
	return nil
}

func ensureManagedPythonDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o755); err != nil {
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
	return nil
}
