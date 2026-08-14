package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	pythonTemporaryRetention = 24 * time.Hour
)

type PythonUserBaseCleaner struct {
	root     string
	tmp      string
	priority int
	now      func() time.Time
}

type pythonCleanupCandidate struct {
	path string
	size uint64
}

func NewPythonUserBaseCleaner(root, tmp string, priority int) *PythonUserBaseCleaner {
	return &PythonUserBaseCleaner{
		root:     filepath.Clean(root),
		tmp:      filepath.Clean(tmp),
		priority: priority,
		now:      time.Now,
	}
}

func (c *PythonUserBaseCleaner) Name() string { return "python_userbase" }

func (c *PythonUserBaseCleaner) Priority() int { return c.priority }

func (c *PythonUserBaseCleaner) EstimateReclaimable(ctx context.Context) (uint64, error) {
	candidates, err := c.collectTemporaryCandidates(ctx)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, candidate := range candidates {
		total += candidate.size
	}
	return total, nil
}

func (c *PythonUserBaseCleaner) Clean(ctx context.Context) (uint64, error) {
	return c.cleanTemporary(ctx)
}

func (c *PythonUserBaseCleaner) ForceClean(ctx context.Context) (uint64, error) {
	// Manual force cleanup keeps the same 24-hour safety window and never removes
	// installed packages. The user base is destructive-cleaned only at Emergency.
	return c.cleanTemporary(ctx)
}

func (c *PythonUserBaseCleaner) EmergencyClean(ctx context.Context) (uint64, error) {
	rootFreed, rootErr := c.clearUserBase(ctx)
	tmpFreed, tmpErr := c.cleanTemporary(ctx)
	return rootFreed + tmpFreed, errors.Join(rootErr, tmpErr)
}

func (c *PythonUserBaseCleaner) cleanTemporary(ctx context.Context) (uint64, error) {
	candidates, err := c.collectTemporaryCandidates(ctx)
	if err != nil {
		return 0, err
	}
	var freed uint64
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return freed, err
		}
		if err := os.RemoveAll(candidate.path); err != nil {
			return freed, fmt.Errorf("remove managed python path %q: %w", candidate.path, err)
		}
		freed += candidate.size
	}
	return freed, nil
}

func (c *PythonUserBaseCleaner) collectTemporaryCandidates(ctx context.Context) ([]pythonCleanupCandidate, error) {
	if err := ensureManagedPythonDirectory(c.tmp, managedPythonTmpMode); err != nil {
		return nil, fmt.Errorf("create managed python temporary directory %q: %w", c.tmp, err)
	}
	return collectPythonTemporaryCandidates(ctx, c.tmp, c.now().Add(-pythonTemporaryRetention))
}

func (c *PythonUserBaseCleaner) clearUserBase(ctx context.Context) (uint64, error) {
	info, err := os.Lstat(c.root)
	if os.IsNotExist(err) {
		if err := ensureManagedPythonDirectory(c.root, managedPythonRootMode); err != nil {
			return 0, fmt.Errorf("recreate managed python root %q: %w", c.root, err)
		}
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect managed python root %q: %w", c.root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, fmt.Errorf("managed python root %q is not a real directory", c.root)
	}
	size, err := managedPythonPathSize(ctx, c.root, info)
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(c.root); err != nil {
		return 0, fmt.Errorf("remove managed python root %q: %w", c.root, err)
	}
	if err := ensureManagedPythonDirectory(c.root, managedPythonRootMode); err != nil {
		return size, fmt.Errorf("recreate managed python root %q: %w", c.root, err)
	}
	return size, nil
}

func collectPythonTemporaryCandidates(ctx context.Context, tmpDir string, cutoff time.Time) ([]pythonCleanupCandidate, error) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read managed python temporary directory: %w", err)
	}
	var candidates []pythonCleanupCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(tmpDir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect managed python temporary path %q: %w", path, err)
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		size, err := managedPythonPathSize(ctx, path, info)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, pythonCleanupCandidate{path: path, size: size})
	}
	return candidates, nil
}

func managedPythonPathSize(ctx context.Context, path string, info os.FileInfo) (uint64, error) {
	if !info.IsDir() {
		return uint64(info.Size()), nil
	}
	var size uint64
	err := filepath.Walk(path, func(current string, currentInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current != path && currentInfo.Mode()&os.ModeSymlink != 0 {
			size += uint64(currentInfo.Size())
			return nil
		}
		if !currentInfo.IsDir() {
			size += uint64(currentInfo.Size())
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure managed python path %q: %w", path, err)
	}
	return size, nil
}
