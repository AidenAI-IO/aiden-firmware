package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterSyntheticEvents(t *testing.T) {
	events := []SessionEvent{
		{EventID: "e1", Type: "user_input", Role: "user", Content: "task"},
		{EventID: "e2", Type: "assistant_output", Role: "assistant", Content: "response"},
		{EventID: "e3", Type: "system_event", Role: "system", Source: EventSourceCompactionPrefix, Content: "Turn Context (split turn): prev summary"},
		{EventID: "e4", Type: "user_input", Role: "user", Source: EventSourcePinnedRoot, Content: "task"},
		{EventID: "e5", Type: "assistant_output", Role: "assistant", Content: "more response"},
		{EventID: "e6", Type: "system_event", Role: "system", Content: "regular system event"},
	}

	filtered := filterSyntheticEvents(events)
	if len(filtered) != 4 {
		t.Errorf("expected 4 non-synthetic events, got %d", len(filtered))
	}

	syntheticCount := 0
	for _, evt := range filtered {
		if evt.Source == EventSourceCompactionPrefix || evt.Source == EventSourcePinnedRoot {
			syntheticCount++
			t.Errorf("synthetic event %s (source=%s) should have been filtered", evt.EventID, evt.Source)
		}
	}

	// Verify specific events are kept
	keptIDs := make(map[string]bool)
	for _, evt := range filtered {
		keptIDs[evt.EventID] = true
	}
	expectedKept := []string{"e1", "e2", "e5", "e6"}
	for _, id := range expectedKept {
		if !keptIDs[id] {
			t.Errorf("expected event %s to be kept", id)
		}
	}

	// Verify synthetic events are excluded
	for _, id := range []string{"e3", "e4"} {
		if keptIDs[id] {
			t.Errorf("expected synthetic event %s to be filtered", id)
		}
	}
}

func TestFilterSyntheticEventsEmptyInput(t *testing.T) {
	filtered := filterSyntheticEvents(nil)
	if len(filtered) != 0 {
		t.Errorf("expected empty output for nil input, got %d events", len(filtered))
	}

	filtered = filterSyntheticEvents([]SessionEvent{})
	if len(filtered) != 0 {
		t.Errorf("expected empty output for empty input, got %d events", len(filtered))
	}
}

func TestFilterSyntheticEventsNoSynthetic(t *testing.T) {
	events := []SessionEvent{
		{EventID: "e1", Type: "user_input", Role: "user", Content: "task"},
		{EventID: "e2", Type: "assistant_output", Role: "assistant", Content: "response"},
	}

	filtered := filterSyntheticEvents(events)
	if len(filtered) != 2 {
		t.Errorf("expected 2 events, got %d", len(filtered))
	}
}

func TestPrependTurnPrefixContextMarksSynthetic(t *testing.T) {
	hotEvents := []SessionEvent{
		{EventID: "e1", Type: "assistant_output", Role: "assistant", Content: "partial response"},
	}

	result := prependTurnPrefixContext(hotEvents, "User asked for X")

	if len(result) != 2 {
		t.Fatalf("expected 2 events (1 synthetic + 1 original), got %d", len(result))
	}

	synthetic := result[0]
	if synthetic.Source != EventSourceCompactionPrefix {
		t.Errorf("synthetic event Source = %q, want %q", synthetic.Source, EventSourceCompactionPrefix)
	}
	if synthetic.Type != "system_event" {
		t.Errorf("synthetic event Type = %q, want %q", synthetic.Type, "system_event")
	}
	if synthetic.Role != "system" {
		t.Errorf("synthetic event Role = %q, want %q", synthetic.Role, "system")
	}
	if !strings.HasPrefix(synthetic.Content, "Turn Context (split turn):") {
		t.Errorf("synthetic event Content should start with 'Turn Context (split turn):', got %q", synthetic.Content)
	}
	if !strings.Contains(synthetic.Content, "User asked for X") {
		t.Errorf("synthetic event Content should contain prefix summary, got %q", synthetic.Content)
	}
	if !strings.HasPrefix(synthetic.EventID, "evt_split_") {
		t.Errorf("synthetic event EventID should start with 'evt_split_', got %q", synthetic.EventID)
	}
}

func TestPrependPinnedRootUserInputMarksSynthetic(t *testing.T) {
	root := SessionEvent{
		EventID: "e0",
		Type:    "user_input",
		Role:    "user",
		Content: "root task",
	}
	hotEvents := []SessionEvent{
		{EventID: "e1", Type: "assistant_output", Role: "assistant", Content: "response"},
	}

	result := prependPinnedRootUserInput(hotEvents, root)

	if len(result) != 2 {
		t.Fatalf("expected 2 events (1 pinned + 1 original), got %d", len(result))
	}

	pinned := result[0]
	if pinned.Source != EventSourcePinnedRoot {
		t.Errorf("pinned event Source = %q, want %q", pinned.Source, EventSourcePinnedRoot)
	}
	if pinned.EventID != "e0" {
		t.Errorf("pinned event should preserve EventID, got %q", pinned.EventID)
	}
	if pinned.Content != "root task" {
		t.Errorf("pinned event should preserve Content, got %q", pinned.Content)
	}

	// Original root should not be modified
	if root.Source != "" {
		t.Errorf("original root event should not be modified, Source = %q", root.Source)
	}
}

func TestMultipleCompactionRoundsDoNotNestSummaries(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()

	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))

	// Round 1: Add events and trigger first compaction
	events1 := []SessionEvent{
		{EventID: "e1", Type: "user_input", Role: "user", Content: "What is 2+2?"},
		{EventID: "e2", Type: "assistant_output", Role: "assistant", Content: "Let me calculate"},
		{EventID: "e3", Type: runEventToolCall, Role: "assistant", Content: "calc(2+2)"},
		{EventID: "e4", Type: "tool_result", Role: "tool", Content: "4"},
		{EventID: "e5", Type: "assistant_output", Role: "assistant", Content: "The answer is 4"},
		{EventID: "e6", Type: "user_input", Role: "user", Content: "What is 3+3?"},
		{EventID: "e7", Type: "assistant_output", Role: "assistant", Content: "Calculating..."},
	}

	for _, evt := range events1 {
		if _, err := session.AppendEvent(ctx, evt); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	// Simulate first compaction (split turn at e7)
	compactEvents1 := events1[:5] // Compact e1-e5
	hotEvents1 := events1[5:]     // Keep e6-e7

	// Add synthetic prefix context (simulating split turn)
	hotEvents1 = prependTurnPrefixContext(hotEvents1, "User asked about 2+2, assistant calculated")

	chunk1, err := session.compressEvents(ctx, compactEvents1, CompressOption{
		ChunkID: "chunk_round1",
		Summary: "First round: user asked 2+2, assistant answered 4",
	})
	if err != nil {
		t.Fatalf("first compressEvents() error = %v", err)
	}

	if err := session.replaceEvents(hotEvents1); err != nil {
		t.Fatalf("replaceEvents() error = %v", err)
	}

	// Round 2: Add more events and trigger second compaction
	moreEvents := []SessionEvent{
		{EventID: "e8", Type: runEventToolCall, Role: "assistant", Content: "calc(3+3)"},
		{EventID: "e9", Type: "tool_result", Role: "tool", Content: "6"},
		{EventID: "e10", Type: "assistant_output", Role: "assistant", Content: "The answer is 6"},
		{EventID: "e11", Type: "user_input", Role: "user", Content: "What is 5+5?"},
	}

	currentEvents, _ := session.readEvents(session.eventsPath())
	for _, evt := range moreEvents {
		currentEvents = append(currentEvents, evt)
	}
	if err := session.replaceEvents(currentEvents); err != nil {
		t.Fatalf("replaceEvents() error = %v", err)
	}

	// Read current events for second compaction
	allEvents, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("readEvents() error = %v", err)
	}

	// Second compaction: compact everything except last 3 events
	compactEvents2 := allEvents[:len(allEvents)-3]

	// CRITICAL: Filter synthetic events before summarizing
	filteredForSummary := filterSyntheticEvents(compactEvents2)

	// Build summary from filtered events
	summary2 := summarizeSessionEvents(filteredForSummary)

	chunk2, err := session.compressEvents(ctx, compactEvents2, CompressOption{
		ChunkID: "chunk_round2",
		Summary: summary2,
	})
	if err != nil {
		t.Fatalf("second compressEvents() error = %v", err)
	}

	// Verify that chunk2's summary doesn't contain the synthetic event's content
	if strings.Contains(chunk2.Summary, "Turn Context (split turn)") {
		t.Errorf("second round summary contains synthetic event marker, indicating recursive summarization:\n%s", chunk2.Summary)
	}

	// Verify that chunk2's summary doesn't redundantly include chunk1's summary text
	// (it's OK to have similar content, but not the exact "Turn Context" marker)
	if strings.Contains(strings.ToLower(chunk2.Summary), "turn context (split turn):") {
		t.Errorf("second round summary nested the synthetic prefix marker from round 1")
	}

	// Verify chunk1 still exists and is valid
	if chunk1.Summary == "" {
		t.Errorf("first chunk summary should not be empty")
	}
}
