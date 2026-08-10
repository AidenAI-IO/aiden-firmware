package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	pythonTemporaryRetention = 24 * time.Hour
	pythonVersionRetention   = 7 * 24 * time.Hour
)

type PythonUserBaseCleaner struct {
	root         string
	priority     int
	versionQuery managedPythonVersionQuery
	now          func() time.Time
}

type pythonCleanupCandidate struct {
	path string
	size uint64
}

func NewPythonUserBaseCleaner(root string, priority int, versionQuery managedPythonVersionQuery) *PythonUserBaseCleaner {
	return &PythonUserBaseCleaner{
		root:         root,
		priority:     priority,
		versionQuery: versionQuery,
		now:          time.Now,
	}
}

func (c *PythonUserBaseCleaner) Name() string { return "python_userbase" }

func (c *PythonUserBaseCleaner) Priority() int { return c.priority }

func (c *PythonUserBaseCleaner) EstimateReclaimable(ctx context.Context) (uint64, error) {
	candidates, err := c.collectCandidates(ctx, false)
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
	return c.clean(ctx, false)
}

func (c *PythonUserBaseCleaner) ForceClean(ctx context.Context) (uint64, error) {
	return c.clean(ctx, true)
}

func (c *PythonUserBaseCleaner) clean(ctx context.Context, force bool) (uint64, error) {
	candidates, err := c.collectCandidates(ctx, force)
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

func (c *PythonUserBaseCleaner) collectCandidates(ctx context.Context, force bool) ([]pythonCleanupCandidate, error) {
	paths, err := resolveManagedPythonPaths(ctx, c.root, c.versionQuery)
	if err != nil {
		return nil, err
	}
	now := c.now()
	if err := ensureManagedPythonPaths(paths, now); err != nil {
		return nil, err
	}

	candidates, err := collectPythonTemporaryCandidates(ctx, paths.Tmp, now.Add(-pythonTemporaryRetention))
	if err != nil {
		return nil, err
	}
	versionCandidates, err := collectPythonVersionCandidates(ctx, paths, now.Add(-pythonVersionRetention), force)
	if err != nil {
		return nil, err
	}
	return append(candidates, versionCandidates...), nil
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
		if info.Mode()&os.ModeSymlink != 0 {
			continue
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

func collectPythonVersionCandidates(ctx context.Context, paths managedPythonPaths, cutoff time.Time, force bool) ([]pythonCleanupCandidate, error) {
	entries, err := os.ReadDir(paths.Root)
	if err != nil {
		return nil, fmt.Errorf("read managed python root: %w", err)
	}
	activeName := filepath.Base(paths.UserBase)
	var candidates []pythonCleanupCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if name == activeName || len(name) < 3 || name[:2] != "py" || !managedPythonVersionPattern.MatchString(name[2:]) {
			continue
		}
		path := filepath.Join(paths.Root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect managed python version path %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		if !force && !info.ModTime().Before(cutoff) {
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
