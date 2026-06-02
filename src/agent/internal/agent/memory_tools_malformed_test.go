package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecallSessionChunksToolToleratesMalformedLLMInput verifies the fix for
// the exact failure mode from production logs:
//   Tool call: name=recall_session_chunks input={"tags": "[]", "limit": "3"}
//   Tool error: decode recall_session_chunks input: json: cannot unmarshal string into Go struct field ChunkRecallQuery.tags of type []string
func TestRecallSessionChunksToolToleratesMalformedLLMInput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(root, "session"))

	// Set up some session history.
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_1",
		Type:    "user_input",
		Role:    "user",
		Content: "打开 Gmail",
		AppName: "Gmail",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_2",
		Type:    "screen_context",
		Role:    "screen",
		Content: "Snoozed folder selected",
		AppName: "Gmail",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := session.Compress(ctx, CompressOption{
		Tags:     []string{"Gmail", "navigation"},
		Entities: []string{"Gmail"},
		Summary:  "User opened Gmail, landed on Snoozed folder",
	}); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	tool := NewRecallSessionChunksTool(session)

	// The exact malformed payload from the production logs: stringified array and int.
	malformedInput := `{"tags": "[]", "limit": "3"}`
	out, err := tool.Call(ctx, malformedInput)
	if err != nil {
		t.Fatalf("Call(%q) error = %v; expected tolerant decode to succeed", malformedInput, err)
	}

	// Verify we got a valid response (at least one chunk should match).
	if !strings.Contains(out, `"chunk_id"`) || !strings.Contains(out, `"results"`) {
		t.Errorf("Call(%q) returned %q; expected valid results JSON", malformedInput, out)
	}
}

func TestRecallMemoryToolToleratesMalformedLLMInput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_test",
		Type:             "preference",
		Priority:         80,
		Confidence:       0.9,
		Tags:             []string{"test"},
		Entities:         []string{"TestApp"},
		Title:            "Test preference",
		Content:          "This is a test memory.",
		EvidenceExcerpts: []string{"User said: test"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	tool := NewRecallMemoryTool(store)

	// Stringified arrays and int.
	malformedInput := `{"tags":"[\"test\"]","types":"[\"preference\"]","limit":"5"}`
	out, err := tool.Call(ctx, malformedInput)
	if err != nil {
		t.Fatalf("Call(%q) error = %v; expected tolerant decode to succeed", malformedInput, err)
	}

	if !strings.Contains(out, `"mem_test"`) || !strings.Contains(out, `"results"`) {
		t.Errorf("Call(%q) returned %q; expected valid results JSON", malformedInput, out)
	}
}

func TestRecallDeviceMemoryToolToleratesMalformedLLMInput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewDeviceMemoryStore(filepath.Join(root, "device"))

	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:       "dev_test",
		Type:     "procedure",
		Title:    "Test procedure",
		Content:  "Test device memory.",
		DeviceID: "default",
		Tags:     []string{"test"},
		Entities: []string{"TestDevice"},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	tool := NewRecallDeviceMemoryTool(store)

	// Stringified arrays and int.
	malformedInput := `{"terms":"[\"test\"]","tags":"[]","limit":"3"}`
	out, err := tool.Call(ctx, malformedInput)
	if err != nil {
		t.Fatalf("Call(%q) error = %v; expected tolerant decode to succeed", malformedInput, err)
	}

	if !strings.Contains(out, `"results"`) {
		t.Errorf("Call(%q) returned %q; expected valid results JSON", malformedInput, out)
	}
}
