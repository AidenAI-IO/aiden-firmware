package contextmanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArtifactStoreCleanerRemovesExpiredAndOrphanedArtifacts(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	live, err := manager.StoreArtifact("text/plain", []byte("live-result"), ArtifactMetadata{})
	if err != nil {
		t.Fatalf("StoreArtifact(live) error = %v", err)
	}
	expired, err := manager.StoreArtifact("text/plain", []byte("expired-result"), ArtifactMetadata{
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("StoreArtifact(expired) error = %v", err)
	}

	orphanStore, err := newArtifactStore(sessionFolder, "s_orphan")
	if err != nil {
		t.Fatalf("newArtifactStore(orphan) error = %v", err)
	}
	orphan, err := orphanStore.store("text/plain", []byte("orphan-result"), ArtifactMetadata{})
	if err != nil {
		t.Fatalf("orphan store() error = %v", err)
	}

	cleaner := NewArtifactStoreCleaner(sessionFolder, 1)
	estimate, err := cleaner.EstimateReclaimable(context.Background())
	if err != nil {
		t.Fatalf("EstimateReclaimable() error = %v", err)
	}
	if estimate < uint64(len("expired-result")+len("orphan-result")) {
		t.Fatalf("EstimateReclaimable() = %d, want at least expired and orphan data bytes", estimate)
	}
	freed, err := cleaner.Clean(context.Background())
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if freed < uint64(len("expired-result")+len("orphan-result")) {
		t.Fatalf("Clean() freed = %d, want at least expired and orphan data bytes", freed)
	}
	if _, err := manager.ReadArtifact(live.Ref, 0, ArtifactReadDefaultBytes); err != nil {
		t.Fatalf("live ReadArtifact() error = %v", err)
	}
	if _, err := manager.ReadArtifact(expired.Ref, 0, ArtifactReadDefaultBytes); err == nil {
		t.Fatal("expired ReadArtifact() succeeded after cleanup")
	}
	if _, err := orphanStore.read(orphan.Ref, 0, ArtifactReadDefaultBytes); err == nil {
		t.Fatal("orphan read() succeeded after cleanup")
	}
	if _, err := os.Stat(filepath.Join(sessionFolder, "s_orphan", "tool-results")); !os.IsNotExist(err) {
		t.Fatalf("orphan tool-results directory still exists, stat error = %v", err)
	}
}

func TestArtifactStoreCleanerTreatsLegacyTranscriptAsReferencedScope(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionID := "s_legacy"
	if err := os.WriteFile(filepath.Join(sessionFolder, sessionID+".jsonl"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store, err := newArtifactStore(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("newArtifactStore() error = %v", err)
	}
	stored, err := store.store("text/plain", []byte("legacy-live"), ArtifactMetadata{})
	if err != nil {
		t.Fatalf("store() error = %v", err)
	}

	cleaner := NewArtifactStoreCleaner(sessionFolder, 1)
	if _, err := cleaner.Clean(context.Background()); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if _, err := store.read(stored.Ref, 0, ArtifactReadDefaultBytes); err != nil {
		t.Fatalf("legacy referenced artifact was removed: %v", err)
	}
}

func TestArtifactStoreCleanerRemovesStaleUncommittedFiles(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	orphanData := filepath.Join(manager.artifactStore.root, "tr_uncommitted.data")
	tempFile := filepath.Join(manager.artifactStore.root, ".artifact-interrupted")
	for _, path := range []string{orphanData, tempFile} {
		if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", filepath.Base(path), err)
		}
		old := time.Now().Add(-artifactOrphanGracePeriod - time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("Chtimes(%s) error = %v", filepath.Base(path), err)
		}
	}

	cleaner := NewArtifactStoreCleaner(sessionFolder, 1)
	if _, err := cleaner.Clean(context.Background()); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	for _, path := range []string{orphanData, tempFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale file %s still exists, stat error = %v", filepath.Base(path), err)
		}
	}
}
