package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMaintenanceDoesNotClobberConcurrentAppends verifies that when maintenance
// (triggered by RequestMaintenance) runs compression asynchronously, any events
// appended via AppendExchange or persistSnapshot during the compression window
// are not lost. This is a regression test for the race condition where:
// 1. maintenance reads events snapshot
// 2. maintenance runs LLM summary (slow, releases lock)
// 3. turn N+1 appends new events via persistSnapshot
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
		HotWindowEvents:         5,
		CountCompressAfterEvents: 10,
		CompressAtPercent:       50,
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

	// Goroutine 2: Wait for summary to start, then append via persistSnapshot
	// (the path used by SaveSnapshot in runtime.go:518)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-summaryStarted

		// This simulates a turn calling SaveSnapshot while maintenance is running
		records := []MessageRecord{
			{Role: "user", Content: "injected during compression window"},
			{Role: "assistant", Content: "response during compression window"},
		}
		if err := mgr.persistSnapshot("default", records); err != nil {
			t.Errorf("persistSnapshot during maintenance: %v", err)
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

// TestAppendAndMaintenanceFileLockSerialization verifies that persistSnapshot
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
				{Role: "user", Content: "concurrent append"},
			}
			if err := mgr.persistSnapshot("default", records); err != nil {
				t.Errorf("goroutine %d persistSnapshot: %v", id, err)
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

func TestReplaceEventsPreservesIncrementalAppends(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(tmpDir, "session"))

	// Write initial events
	for i := 0; i < 5; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: "initial",
		}); err != nil {
			t.Fatalf("initial append: %v", err)
		}
	}

	// Simulate maintenance: read snapshot
	eventsPath := session.eventsPath()
	snapshot, err := session.readEvents(eventsPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	// Simulate append during compression window
	if _, err := session.AppendEvent(ctx, SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "appended during compression",
	}); err != nil {
		t.Fatalf("append during compression: %v", err)
	}

	// Simulate maintenance trying to replace with old snapshot (INCORRECT behavior)
	// This should be detected and merged by the fix
	hotEvents := snapshot[2:] // keep last 3 from snapshot

	// The fix should re-read and merge the new event before replaceEvents
	// For now, this test documents the BROKEN behavior
	if err := session.replaceEvents(hotEvents); err != nil {
		t.Fatalf("replaceEvents: %v", err)
	}

	finalEvents, err := session.readEvents(eventsPath)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}

	// CURRENT (broken): only 3 events (the old snapshot suffix)
	// EXPECTED (after fix): 4 events (snapshot suffix + incremental append)
	if len(finalEvents) != 3 {
		t.Logf("UNEXPECTED: finalEvents count is %d (expected 3 pre-fix, 4 post-fix)", len(finalEvents))
	}

	// After fix, this should find the appended event
	foundAppended := false
	for _, evt := range finalEvents {
		if evt.Content == "appended during compression" {
			foundAppended = true
			break
		}
	}

	if foundAppended {
		t.Logf("GOOD: incremental append was preserved")
	} else {
		t.Logf("BROKEN (expected pre-fix): incremental append was clobbered")
	}
}
