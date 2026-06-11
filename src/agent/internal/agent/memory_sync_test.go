package agent

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMemoryManagerSyncsStateAfterCompaction verifies that after session
// compaction, both eventCount and in-memory History are synced to match
// the compressed hot window on disk.
func TestMemoryManagerSyncsStateAfterCompaction(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()

	cfg := MemoryExtractionConfig{
		HotWindowEvents:          5,
		CountCompressAfterEvents: 10,
		CompressAtPercent:        50,
		SummaryMaxChunks:         10,
		ReserveTokens:            1000,
		KeepRecentTokens:         2000,
	}

	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))
	_, err := manager.Get("default", MemoryConfig{Type: "buffer"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	// Build a long conversation via AppendExchange (the real workflow)
	for i := 0; i < 10; i++ {
		if err := manager.AppendExchange(ctx, "default", "user message", "assistant response"); err != nil {
			t.Fatalf("AppendExchange() error = %v", err)
		}
	}

	// Verify events were written
	eventsPath := filepath.Join(storageDir, "session", "events.jsonl")
	eventsBefore := readSessionEvents(t, eventsPath)
	if len(eventsBefore) != 20 {
		t.Fatalf("expected 20 events before compaction, got %d", len(eventsBefore))
	}

	// Record state before compaction
	manager.mu.Lock()
	eventCountBefore := manager.eventCount["default"]
	handle := manager.handles["default"]
	manager.mu.Unlock()

	if eventCountBefore != 20 {
		t.Fatalf("eventCount before compaction = %d, want 20", eventCountBefore)
	}

	// Trigger compaction
	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	// Verify hot window was created
	eventsAfter := readSessionEvents(t, eventsPath)
	if len(eventsAfter) >= 20 {
		t.Fatalf("expected hot window < 20 events, got %d", len(eventsAfter))
	}
	t.Logf("Hot window size after compaction: %d events", len(eventsAfter))

	// Verify eventCount was synced
	manager.mu.Lock()
	eventCountAfter := manager.eventCount["default"]
	manager.mu.Unlock()

	if eventCountAfter != len(eventsAfter) {
		t.Errorf("eventCount = %d, want %d (hot window size)", eventCountAfter, len(eventsAfter))
	}

	// Verify in-memory History was synced
	memMessages, err := handle.History.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(memMessages) != len(eventsAfter) {
		t.Errorf("in-memory History has %d messages, want %d (hot window size)", len(memMessages), len(eventsAfter))
	}

	// Verify we can append new events without index confusion
	if err := manager.AppendExchange(ctx, "default", "new user input", "new response"); err != nil {
		t.Fatalf("AppendExchange() after compaction error = %v", err)
	}

	// Verify new events were appended correctly (2 new events: user + assistant)
	finalEvents := readSessionEvents(t, eventsPath)
	expectedCount := len(eventsAfter) + 2
	if len(finalEvents) != expectedCount {
		t.Errorf("after appending 1 exchange, got %d events, want %d", len(finalEvents), expectedCount)
	}
	if finalEvents[len(finalEvents)-1].Content != "new response" {
		t.Errorf("last event content = %q, want %q", finalEvents[len(finalEvents)-1].Content, "new response")
	}

	// Verify eventCount was updated correctly
	manager.mu.Lock()
	finalEventCount := manager.eventCount["default"]
	manager.mu.Unlock()

	if finalEventCount != len(finalEvents) {
		t.Errorf("final eventCount = %d, want %d", finalEventCount, len(finalEvents))
	}
}
