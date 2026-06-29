package agent

import (
	"context"
	"path/filepath"
	"testing"
)

// TestArchiveGeneratesFinalChunk verifies that when a session with uncompressed
// events is archived, those events are compressed into a final chunk so the
// archived session remains recallable.
func TestArchiveGeneratesFinalChunk(t *testing.T) {
	root := t.TempDir()
	mgr := NewMemoryManager(root,
		WithExtractionConfig(MemoryExtractionConfig{
			SummaryMaxChunks:  3,
			HotWindowEvents:   10,
			ContextWindow:     100000, // high threshold so compression never triggers
			CompressAtPercent: 80,
		}),
	)

	ctx := context.Background()
	agentName := "test-agent"

	// Append a few events without triggering compression
	for i := 0; i < 5; i++ {
		err := mgr.AppendSessionEvent(ctx, agentName, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: "test message",
		}, SessionEventMetadata{})
		if err != nil {
			t.Fatalf("AppendSessionEvent() error = %v", err)
		}
	}

	// Verify no chunks exist yet
	sessionStore := NewSessionMemoryStore(filepath.Join(root, "session"), 3)
	index, err := sessionStore.loadChunkIndex()
	if err == nil && len(index.Chunks) > 0 {
		t.Fatalf("expected no chunks before archive, got %d", len(index.Chunks))
	}

	// Trigger rotation/archive
	result, err := mgr.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}
	if result.ArchiveDir == "" {
		t.Fatal("expected archive dir, got empty")
	}

	// Verify the archived session has at least one chunk
	archivedStore := NewSessionMemoryStore(result.ArchiveDir, 3)
	archivedIndex, err := archivedStore.loadChunkIndex()
	if err != nil {
		t.Fatalf("loadChunkIndex from archive error = %v", err)
	}
	if len(archivedIndex.Chunks) == 0 {
		t.Fatal("expected archived session to have at least one chunk")
	}
	if archivedIndex.Chunks[0].EventCount != 5 {
		t.Errorf("expected final chunk to contain 5 events, got %d", archivedIndex.Chunks[0].EventCount)
	}

	// Verify the archived session is recallable
	hits, err := archivedStore.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected to recall at least one chunk from archive")
	}
	if len(hits[0].Evidence) != 5 {
		t.Errorf("expected chunk to contain 5 events, got %d", len(hits[0].Evidence))
	}
}

// TestArchiveWithEmptySessionSkipsCompression verifies that archiving an empty
// session (no events) does not fail or create empty chunks.
func TestArchiveWithEmptySessionSkipsCompression(t *testing.T) {
	root := t.TempDir()
	mgr := NewMemoryManager(root)

	// Rotate without any events
	result, err := mgr.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}

	// Empty sessions shouldn't produce an archive dir
	if result.ArchiveDir != "" {
		// If it did create one, verify it has no chunks
		archivedStore := NewSessionMemoryStore(result.ArchiveDir, 3)
		index, err := archivedStore.loadChunkIndex()
		if err == nil && len(index.Chunks) > 0 {
			t.Errorf("expected no chunks in empty archived session, got %d", len(index.Chunks))
		}
	}
}
