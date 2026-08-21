package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TemporaryMemoryCleaner removes expired or already-deleted notification
// conclusions. It never removes active Temporary Memory, so recall remains
// independent from storage pressure until the conclusion's TTL has elapsed.
type TemporaryMemoryCleaner struct {
	rootDir  string
	priority int
	now      func() time.Time
}

func NewTemporaryMemoryCleaner(rootDir string, priority int) *TemporaryMemoryCleaner {
	return &TemporaryMemoryCleaner{rootDir: rootDir, priority: priority, now: time.Now}
}

func (c *TemporaryMemoryCleaner) Name() string  { return "temporary_memory_expired" }
func (c *TemporaryMemoryCleaner) Priority() int { return c.priority }

func (c *TemporaryMemoryCleaner) EstimateReclaimable(ctx context.Context) (uint64, error) {
	candidates, err := c.collect(ctx)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, candidate := range candidates {
		total += uint64(candidate.size)
	}
	return total, nil
}

func (c *TemporaryMemoryCleaner) Clean(ctx context.Context) (uint64, error) {
	candidates, err := c.collect(ctx)
	if err != nil {
		return 0, err
	}
	var freed uint64
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return freed, err
		}
		if err := os.Remove(candidate.path); err != nil && !os.IsNotExist(err) {
			return freed, fmt.Errorf("remove temporary memory %s: %w", filepath.Base(candidate.path), err)
		}
		freed += uint64(candidate.size)
	}
	if len(candidates) > 0 {
		store := NewLongTermMemoryStore(c.rootDir)
		if err := store.RebuildIndex(ctx); err != nil {
			return freed, err
		}
	}
	return freed, nil
}

type temporaryMemoryCleanupCandidate struct {
	path string
	size int64
}

func (c *TemporaryMemoryCleaner) collect(ctx context.Context) ([]temporaryMemoryCleanupCandidate, error) {
	if c == nil || strings.TrimSpace(c.rootDir) == "" {
		return nil, nil
	}
	memoriesDir := filepath.Join(c.rootDir, "memories")
	entries, err := os.ReadDir(memoriesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	now := c.now().UTC()
	var candidates []temporaryMemoryCleanupCandidate
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return candidates, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(memoriesDir, entry.Name())
		parsed, err := readMemoryMarkdown(path)
		if err != nil {
			continue
		}
		if parsed.Item.Status != "deleted" && !memoryItemExpired(parsed.Item, now) {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			candidates = append(candidates, temporaryMemoryCleanupCandidate{path: path, size: info.Size()})
		}
	}
	return candidates, nil
}
