package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTemporaryMemoryCleanerRemovesExpiredOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "temporary")
	store := NewLongTermMemoryStore(root)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if _, err := store.AddMemory(context.Background(), MemoryItem{ID: "expired", Type: "fact", TimeScope: "temporary", Content: "old", EvidenceExcerpts: []string{"old"}, ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMemory(context.Background(), MemoryItem{ID: "active", Type: "fact", TimeScope: "temporary", Content: "current", EvidenceExcerpts: []string{"current"}, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	cleaner := NewTemporaryMemoryCleaner(root, 1)
	cleaner.now = func() time.Time { return now }
	if freed, err := cleaner.Clean(context.Background()); err != nil || freed == 0 {
		t.Fatalf("Clean() freed=%d err=%v", freed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "memories", "expired.md")); !os.IsNotExist(err) {
		t.Fatalf("expired memory still exists, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "memories", "active.md")); err != nil {
		t.Fatalf("active temporary memory was removed: %v", err)
	}
}
