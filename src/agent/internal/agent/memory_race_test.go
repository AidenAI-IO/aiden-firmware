package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMaintenanceDoesNotClobberConcurrentAppends verifies that when maintenance
// (triggered by RequestMaintenance) runs compression asynchronously, any events
// appended via AppendExchange or syncSessionRecords during the compression window
// are not lost. This is a regression test for the race condition where:
// 1. maintenance reads events snapshot
// 2. maintenance runs LLM summary (slow, releases lock)
// 3. turn N+1 appends new events via syncSessionRecords
// 4. maintenance replaceEvents with old snapshot → new events clobbered
func TestMaintenanceDoesNotClobberConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Track when summarize is called so we can inject append during its execution
	summaryStarted := make(chan struct{})
	summaryDone := make(chan struct{})

	mockSummarize := func(ctx context.Context, events []SessionEvent) string {
		close(summaryStarted)
		// Simulate LLM delay during which appends can happen
		time.Sleep(200 * time.Millisecond)
		close(summaryDone)
		return "Mock summary"
	}

	cfg := MemoryExtractionConfig{
		HotWindowEvents:          5,
		CountCompressAfterEvents: 10,
		CompressAtPercent:        50,
	}

	mgr := NewMemoryManager(
		tmpDir,
		WithExtractionConfig(cfg),
		WithSummarizeFn(mockSummarize),
	)

	// Prefill events to trigger compression (needs > CountCompressAfterEvents)
	session := NewSessionMemoryStore(filepath.Join(tmpDir, "session"))
	for i := 0; i < 12; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: "prefill event",
		}); err != nil {
			t.Fatalf("prefill AppendEvent: %v", err)
		}
	}

	// Set lastPromptTokens high enough to trigger compression
	mgr.SetLastPromptTokens(100000)

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine 1: Trigger async maintenance
	go func() {
		defer wg.Done()
		mgr.RequestMaintenance()
	}()

	// Goroutine 2: Wait for summary to start, then append via syncSessionRecords.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-summaryStarted

		// This simulates a turn synchronizing history while maintenance is running.
		records := []MessageRecord{
			{Role: "user", Content: "injected during compression window"},
			{Role: "assistant", Content: "response during compression window"},
		}
		if err := mgr.syncSessionRecords("default", records); err != nil {
			t.Errorf("syncSessionRecords during maintenance: %v", err)
		}
	}()

	wg.Wait()
	<-summaryDone

	// Wait for maintenance to complete
	if err := mgr.WaitMaintenance(ctx); err != nil {
		t.Fatalf("WaitMaintenance: %v", err)
	}

	// Verify: injected events must still be present
	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("readEvents after maintenance: %v", err)
	}

	foundInjected := false
	for _, evt := range events {
		if evt.Content == "injected during compression window" {
			foundInjected = true
			break
		}
	}

	if !foundInjected {
		t.Errorf("injected event was clobbered by maintenance; final event count=%d", len(events))
		for i, evt := range events {
			t.Logf("  [%d] %s: %s", i, evt.Type, evt.Content)
		}
	}
}

// TestAppendAndMaintenanceFileLockSerialization verifies that syncSessionRecords
// and maintainFilesystemMemory properly serialize access to events.jsonl via
// FileLock, preventing torn reads/writes.
func TestAppendAndMaintenanceFileLockSerialization(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// No LLM summarize, just test lock serialization
	cfg := MemoryExtractionConfig{
		HotWindowEvents:          3,
		CountCompressAfterEvents: 6,
		CompressAtPercent:        50,
	}

	mgr := NewMemoryManager(tmpDir, WithExtractionConfig(cfg))

	session := NewSessionMemoryStore(filepath.Join(tmpDir, "session"))
	for i := 0; i < 8; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: "initial event",
		}); err != nil {
			t.Fatalf("initial AppendEvent: %v", err)
		}
	}

	mgr.SetLastPromptTokens(100000)

	// Spawn multiple goroutines doing concurrent appends and maintenance
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			records := []MessageRecord{
				{Role: "user", Content: fmt.Sprintf("concurrent append %d", id)},
			}
			if err := mgr.syncSessionRecords("default", records); err != nil {
				t.Errorf("goroutine %d syncSessionRecords: %v", id, err)
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr.RequestMaintenance()
	}()

	wg.Wait()

	if err := mgr.WaitMaintenance(ctx); err != nil {
		t.Fatalf("WaitMaintenance: %v", err)
	}

	// Verify file is not corrupted (readEvents would error on malformed JSON)
	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("readEvents after concurrent access: %v", err)
	}

	// After compression, event count may be reduced (hot window < original count).
	// The key invariant is: file integrity preserved, no corruption, no partial writes.
	if len(events) == 0 {
		t.Errorf("expected non-zero events after concurrent access, got 0")
	}

	t.Logf("final event count: %d (compression may have reduced from initial 8)", len(events))
}
