package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestMemoryManagerSaveWritesSessionEvents(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if err := handle.History.SetMessages(ctx, []llms.ChatMessage{
		llms.HumanChatMessage{Content: "hello"},
		llms.AIChatMessage{Content: "hi"},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) != 2 {
		t.Fatalf("expected 2 session events, got %d", len(events))
	}
	if events[0].Type != "user_input" || events[0].Role != "user" || events[0].Content != "hello" {
		t.Fatalf("unexpected first event: %#v", events[0])
	}
	if events[1].Type != "assistant_output" || events[1].Role != "assistant" || events[1].Content != "hi" {
		t.Fatalf("unexpected second event: %#v", events[1])
	}
}

func TestMemoryManagerSaveCreatesSessionMetadataOnFirstWrite(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := handle.History.SetMessages(ctx, []llms.ChatMessage{
		llms.HumanChatMessage{Content: "hello"},
		llms.AIChatMessage{Content: "hi"},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}

	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	metadata := readSessionMetadataForTest(t, filepath.Join(storageDir, "session", sessionMetadataFileName))
	rotation, err := manager.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}
	if rotation.ClosedSessionID != metadata.SessionID {
		t.Fatalf("ClosedSessionID = %q, want active session_id %q", rotation.ClosedSessionID, metadata.SessionID)
	}
}

func TestMemoryManagerSaveSnapshotCreatesSessionMetadataOnFirstWrite(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)

	if err := manager.SaveSnapshot(ctx, "default", []MessageRecord{
		{Role: string(llms.ChatMessageTypeHuman), Content: "hello"},
		{Role: string(llms.ChatMessageTypeAI), Content: "hi"},
	}); err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}

	metadata := readSessionMetadataForTest(t, filepath.Join(storageDir, "session", sessionMetadataFileName))
	rotation, err := manager.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}
	if rotation.ClosedSessionID != metadata.SessionID {
		t.Fatalf("ClosedSessionID = %q, want active session_id %q", rotation.ClosedSessionID, metadata.SessionID)
	}
}

func TestSnapshotAppendStartFindsExistingSuffixOverlap(t *testing.T) {
	human := func(content string) MessageRecord {
		return MessageRecord{Role: "human", Content: content}
	}
	ai := func(content string) MessageRecord {
		return MessageRecord{Role: "ai", Content: content}
	}

	tests := []struct {
		name     string
		existing []MessageRecord
		records  []MessageRecord
		want     int
	}{
		{
			name:     "existing prefix of snapshot",
			existing: []MessageRecord{human("first"), ai("first answer")},
			records:  []MessageRecord{human("first"), ai("first answer"), human("second")},
			want:     2,
		},
		{
			name:     "snapshot prefix already persisted",
			existing: []MessageRecord{human("first"), ai("first answer"), human("second")},
			records:  []MessageRecord{human("first"), ai("first answer")},
			want:     2,
		},
		{
			name:     "existing suffix overlaps snapshot prefix",
			existing: []MessageRecord{human("failed request"), human("current request"), ai("current answer")},
			records:  []MessageRecord{human("current request"), ai("current answer")},
			want:     2,
		},
		{
			name:     "partial overlap appends only new tail",
			existing: []MessageRecord{human("old"), human("current request"), ai("current answer")},
			records:  []MessageRecord{human("current request"), ai("current answer"), human("next request")},
			want:     2,
		},
		{
			name:     "no overlap",
			existing: []MessageRecord{human("old")},
			records:  []MessageRecord{human("new")},
			want:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snapshotAppendStart(tt.existing, tt.records); got != tt.want {
				t.Fatalf("snapshotAppendStart() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSessionEventsStripScreenshotData(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "buffer"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	screenshotJSON := `{"width":1080,"height":2400,"format":"jpeg","size":54321,"data":"dGVzdC1iYXNlNjQtc2NyZWVuc2hvdC1wYXlsb2FkCg=="}`
	if err := handle.History.SetMessages(ctx, []llms.ChatMessage{
		llms.HumanChatMessage{Content: "take screenshot"},
		llms.ToolChatMessage{Content: screenshotJSON},
		llms.AIChatMessage{Content: "screenshot taken"},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) != 3 {
		t.Fatalf("expected 3 session events, got %d", len(events))
	}
	toolEvent := events[1]
	if toolEvent.Type != "tool_result" {
		t.Fatalf("expected tool_result, got %s", toolEvent.Type)
	}
	if strings.Contains(toolEvent.Content, "dGVzdC1iYXNlNjQtc2NyZWVuc2hvdC1wYXlsb2FkCg==") {
		t.Fatalf("screenshot base64 data should be stripped: %s", toolEvent.Content)
	}
	if strings.Contains(toolEvent.Content, `"data"`) {
		t.Fatalf("data field should be omitted: %s", toolEvent.Content)
	}
	if !strings.Contains(toolEvent.Content, `"width":1080`) {
		t.Fatalf("metadata should be preserved: %s", toolEvent.Content)
	}
	if !strings.Contains(toolEvent.Content, `"height":2400`) {
		t.Fatalf("metadata should be preserved: %s", toolEvent.Content)
	}

	snapshotData, err := os.ReadFile(filepath.Join(storageDir, "default.json"))
	if err != nil {
		t.Fatalf("read legacy memory snapshot: %v", err)
	}
	var snapshotRecords []MessageRecord
	if err := json.Unmarshal(snapshotData, &snapshotRecords); err != nil {
		t.Fatalf("decode legacy memory snapshot: %v", err)
	}
	if len(snapshotRecords) != 3 {
		t.Fatalf("expected 3 snapshot records, got %d", len(snapshotRecords))
	}
	snapshotToolContent := snapshotRecords[1].Content
	if strings.Contains(snapshotToolContent, "dGVzdC1iYXNlNjQtc2NyZWVuc2hvdC1wYXlsb2FkCg==") {
		t.Fatalf("legacy snapshot should strip screenshot base64 data: %s", snapshotData)
	}
	if strings.Contains(snapshotToolContent, `"data"`) {
		t.Fatalf("legacy snapshot should omit data field: %s", snapshotData)
	}
	if !strings.Contains(snapshotToolContent, `"width":1080`) {
		t.Fatalf("legacy snapshot should preserve metadata: %s", snapshotData)
	}
}

func TestSessionMemoryAppendEventStripsScreenshotData(t *testing.T) {
	ctx := context.Background()
	session := NewSessionMemoryStore(t.TempDir())

	screenshotJSON := `{"width":640,"height":480,"format":"jpeg","size":12345,"data":"YW5vdGhlci1iYXNlNjQtcGF5bG9hZAo=","action_output":"tap succeeded"}`
	eventID, err := session.AppendEvent(ctx, SessionEvent{
		Type:    "tool_result",
		Role:    "tool",
		Content: screenshotJSON,
	})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if eventID == "" {
		t.Fatal("AppendEvent() returned empty ID")
	}

	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	stored := events[0]
	if strings.Contains(stored.Content, "YW5vdGhlci1iYXNlNjQtcGF5bG9hZAo=") {
		t.Fatalf("screenshot base64 data should be stripped: %s", stored.Content)
	}
	if strings.Contains(stored.Content, `"data"`) {
		t.Fatalf("data field should be omitted: %s", stored.Content)
	}
	if !strings.Contains(stored.Content, `"width":640`) {
		t.Fatalf("metadata should be preserved: %s", stored.Content)
	}
	if !strings.Contains(stored.Content, `"action_output":"tap succeeded"`) {
		t.Fatalf("action_output should be preserved: %s", stored.Content)
	}
}

func TestMemoryManagerRestoresFromSessionEventsWhenSnapshotMissing(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if err := handle.History.SetMessages(ctx, []llms.ChatMessage{
		llms.HumanChatMessage{Content: "remember this"},
		llms.AIChatMessage{Content: "stored"},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := os.Remove(filepath.Join(storageDir, "default.json")); err != nil {
		t.Fatalf("remove default snapshot: %v", err)
	}

	reloaded := NewMemoryManager(storageDir)
	reloadedHandle, err := reloaded.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() reload error = %v", err)
	}
	messages, err := reloadedHandle.History.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 restored messages, got %d", len(messages))
	}
	if messages[0].GetContent() != "remember this" || messages[1].GetContent() != "stored" {
		t.Fatalf("unexpected restored messages: %#v", messages)
	}
}

func TestMemoryManagerRestoresOnlyConversationEventsFromRuntimeSession(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	events := []SessionEvent{
		{Type: "user_input", Role: "user", Content: "original request"},
		{Type: runEventToolCall, Role: "tool", ToolName: "tap", ToolInput: `{"x":10}`, Content: "tap button"},
		{Type: "tool_result", Role: "tool", ToolName: "tap", ToolInput: `{"x":10}`, Content: "tap ok"},
		{Type: runEventTodoUpdate, Role: "system", Content: "current todo"},
		{Type: "steer", Role: "user", Content: "updated instruction"},
		{Type: "assistant_output", Role: "assistant", Content: "done"},
	}
	for _, event := range events {
		if _, err := session.AppendEvent(ctx, event); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", event.Type, err)
		}
	}

	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	messages, err := handle.History.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected 3 restored conversation messages, got %d: %#v", len(messages), messages)
	}
	if messages[0].GetContent() != "original request" ||
		messages[1].GetContent() != "updated instruction" ||
		messages[2].GetContent() != "done" {
		t.Fatalf("unexpected restored conversation messages: %#v", messages)
	}
}

func TestMemoryManagerSaveDoesNotRegressEventCountWithRuntimeEvents(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "first request",
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent(user) error = %v", err)
	}
	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "planner_decision",
		Role:    string(RolePlanner),
		Content: `{"objective":"first request","plan":["answer"]}`,
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent(planner) error = %v", err)
	}
	if err := manager.AppendMessagesWithMetadata(ctx, "default", []MessageRecord{
		{Role: string(llms.ChatMessageTypeAI), Content: "first answer"},
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendMessagesWithMetadata(ai) error = %v", err)
	}
	if err := handle.History.SetMessages(ctx, []llms.ChatMessage{
		llms.HumanChatMessage{Content: "first request"},
		llms.AIChatMessage{Content: "first answer"},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}

	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "second request",
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent(second user) error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) != 4 {
		t.Fatalf("expected 4 session events, got %d: %#v", len(events), events)
	}
	for i, event := range events {
		want := i + 1
		if event.Sequence != want {
			t.Fatalf("event %d sequence = %d, want %d; events=%#v", i, event.Sequence, want, events)
		}
	}
}

func TestMemoryManagerWritesRuntimeIDMetadata(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)

	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "hello",
	}, SessionEventMetadata{RuntimeID: "runtime-a"}); err != nil {
		t.Fatalf("AppendSessionEvent() error = %v", err)
	}

	eventsPath := filepath.Join(storageDir, "session", "events.jsonl")
	events := readSessionEvents(t, eventsPath)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %#v", len(events), events)
	}
	if events[0].RuntimeID != "runtime-a" {
		t.Fatalf("RuntimeID = %q, want runtime-a", events[0].RuntimeID)
	}

	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	if bytes.Contains(data, []byte(`"session_id"`)) {
		t.Fatalf("event should not write legacy session_id field: %s", data)
	}
	if !bytes.Contains(data, []byte(`"runtime_id":"runtime-a"`)) {
		t.Fatalf("event missing runtime_id field: %s", data)
	}
}

func TestMemoryManagerCreatesSessionMetadataOnFirstAppend(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)

	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "first request",
	}, SessionEventMetadata{RuntimeID: "runtime-a"}); err != nil {
		t.Fatalf("AppendSessionEvent() error = %v", err)
	}

	metadataPath := filepath.Join(storageDir, "session", sessionMetadataFileName)
	metadata := readSessionMetadataForTest(t, metadataPath)
	if metadata.SessionID == "" {
		t.Fatal("active metadata session_id is empty")
	}

	rotation, err := manager.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}
	if rotation.ClosedSessionID != metadata.SessionID {
		t.Fatalf("ClosedSessionID = %q, want original active session_id %q", rotation.ClosedSessionID, metadata.SessionID)
	}
	if filepath.Base(rotation.ArchiveDir) != metadata.SessionID {
		t.Fatalf("archive basename = %q, want original active session_id %q", filepath.Base(rotation.ArchiveDir), metadata.SessionID)
	}
}

func TestMemoryManagerAppendSessionEventContinuesExistingEventStream(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	for i := 0; i < 2; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("existing event %d", i+1),
		}); err != nil {
			t.Fatalf("AppendEvent(%d) error = %v", i, err)
		}
	}

	manager := NewMemoryManager(storageDir)
	if err := manager.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "new event",
	}, SessionEventMetadata{}); err != nil {
		t.Fatalf("AppendSessionEvent() error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) != 3 {
		t.Fatalf("expected 3 session events, got %d: %#v", len(events), events)
	}
	if events[2].Sequence != 3 {
		t.Fatalf("new event sequence = %d, want 3; events=%#v", events[2].Sequence, events)
	}
}

func TestMemoryManagerRepairsTruncatedSessionEventTail(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	now := time.Now().UTC()
	events := []SessionEvent{
		sessionEventFromRecord(MessageRecord{Role: string(llms.ChatMessageTypeHuman), Content: "hello"}, now, 0),
		sessionEventFromRecord(MessageRecord{Role: string(llms.ChatMessageTypeAI), Content: "hi"}, now, 1),
	}
	data, _, err := encodeSessionEventsJSONL(events)
	if err != nil {
		t.Fatalf("encode events: %v", err)
	}
	data = append(data, []byte(`{"event_id":"partial","type":"assistant_output","role":"assistant","content":"cut`)...)

	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	if err := os.WriteFile(eventsPath, data, 0o644); err != nil {
		t.Fatalf("write truncated events: %v", err)
	}

	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	messages, err := handle.History.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if messages[0].GetContent() != "hello" || messages[1].GetContent() != "hi" {
		t.Fatalf("unexpected restored messages: %#v", messages)
	}

	repaired, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read repaired events: %v", err)
	}
	if bytes.Contains(repaired, []byte("partial")) {
		t.Fatalf("truncated event tail was not removed: %q", repaired)
	}
	if got := readSessionEvents(t, eventsPath); len(got) != 2 {
		t.Fatalf("repaired events = %d, want 2", len(got))
	}
}

func TestSessionMemoryRecallRejectsTruncatedChunkEvidence(t *testing.T) {
	ctx := context.Background()
	session := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))
	now := time.Now().UTC()
	if _, err := session.AppendEvent(ctx, sessionEventFromRecord(MessageRecord{
		Role:    string(llms.ChatMessageTypeHuman),
		Content: "preserve this evidence",
	}, now, 0)); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	chunk, err := session.Compress(ctx, CompressOption{ChunkID: "chunk_bad", Summary: "summary"})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	chunkPath := filepath.Join(session.chunksDir(), chunk.ID+".jsonl")
	file, err := os.OpenFile(chunkPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open chunk for append: %v", err)
	}
	if _, err := file.WriteString(`{"event_id":"partial","type":"user_input","role":"user","content":"cut`); err != nil {
		file.Close()
		t.Fatalf("write truncated chunk tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close chunk: %v", err)
	}

	_, err = session.RecallChunks(ctx, ChunkRecallQuery{ChunkIDs: []string{chunk.ID}})
	if err == nil {
		t.Fatal("RecallChunks() error = nil, want truncated chunk evidence error")
	}
	if !strings.Contains(err.Error(), "decode session event") {
		t.Fatalf("RecallChunks() error = %v, want decode session event", err)
	}
}

func TestMemoryManagerSaveCompactsHotWindow(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 20
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	messages := []llms.ChatMessage{
		llms.HumanChatMessage{Content: "我是硬件产品经理，平时用中文沟通，关注开发板 agent 端到端行为。"},
		llms.AIChatMessage{Content: "我会按这个背景回答。"},
		llms.HumanChatMessage{Content: "记一下，以后处理蓝海报销App超过100元的提交或付款动作，必须先给风险摘要并等我确认。"},
		llms.AIChatMessage{Content: "已记录这个高风险操作规则。"},
	}
	for i := 0; i < 39; i++ {
		messages = append(messages, llms.HumanChatMessage{Content: "填充对话轮次"})
	}
	if err := handle.History.SetMessages(ctx, messages); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) > cfg.HotWindowEvents+1 {
		t.Fatalf("expected session events to fit hot window plus pinned root <= %d, got %d", cfg.HotWindowEvents+1, len(events))
	}

	chunks, err := NewSessionMemoryStore(filepath.Join(storageDir, "session")).RecallChunks(ctx, ChunkRecallQuery{Tags: []string{"报销"}, Entities: []string{"蓝海报销App"}, Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one compacted chunk, got %d", len(chunks))
	}
	if len(chunks[0].Evidence) == 0 {
		t.Fatalf("expected chunk to contain evidence events")
	}
}

func TestSessionMemoryCompressStoresStructuredSummaryInIndexAndRecall(t *testing.T) {
	ctx := context.Background()
	session := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))
	if _, err := session.AppendEvent(ctx, SessionEvent{EventID: "evt_1", Type: "user_input", Role: "user", Content: "我想测试 MiniCPM 作为板子的局域网 VLM"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	structured := ChunkStructuredSummary{
		Summary:          "讨论 MiniCPM 局域网 VLM 接入方案。",
		UserGoals:        []string{"测试 MiniCPM 作为板子的局域网 VLM"},
		ConfirmedFacts:   []string{"主模型仍承担语音链路"},
		Decisions:        []string{"VLM 使用独立 model_vision 配置"},
		Proposals:        []string{"screen_memory_summarizer 优先读取 model_vision"},
		OpenTasks:        []string{"实现 model_vision 配置解析"},
		RisksOrPitfalls:  []string{"不要把主 model 直接替换成 VLM"},
		MemoryCandidates: []string{"Aiden 的主语音模型和屏幕 VLM 应分离配置"},
	}
	chunk, err := session.Compress(ctx, CompressOption{ChunkID: "chunk_structured", Summary: structured.Summary, Structured: structured})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if chunk.Structured.Decisions[0] != structured.Decisions[0] {
		t.Fatalf("Compress() missing structured summary: %#v", chunk.Structured)
	}

	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{ChunkIDs: []string{"chunk_structured"}})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one recalled chunk, got %#v", chunks)
	}
	if chunks[0].Structured.OpenTasks[0] != structured.OpenTasks[0] {
		t.Fatalf("recalled chunk missing structured summary: %#v", chunks[0].Structured)
	}

	index, err := session.loadChunkIndex()
	if err != nil {
		t.Fatalf("loadChunkIndex() error = %v", err)
	}
	if len(index.Chunks) != 1 || index.Chunks[0].Structured.ConfirmedFacts[0] != structured.ConfirmedFacts[0] {
		t.Fatalf("index missing structured summary: %#v", index.Chunks)
	}
}

func TestMaintainFilesystemMemoryUsesStructuredSummarizerAndFallsBackToPlain(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 4
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg), WithStructuredSummarizeFn(func(ctx context.Context, events []SessionEvent) ChunkStructuredSummary {
		return ChunkStructuredSummary{
			Summary:        "结构化摘要主文",
			Decisions:      []string{"保留主语音模型"},
			OpenTasks:      []string{"增加 model_vision"},
			ConfirmedFacts: []string{"压缩事件数量大于热窗口"},
		}
	}))

	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var msgs []llms.ChatMessage
	for i := 0; i < 9; i++ {
		msgs = append(msgs, llms.HumanChatMessage{Content: fmt.Sprintf("message %d", i)})
	}
	if err := handle.History.SetMessages(ctx, msgs); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %#v", chunks)
	}
	if chunks[0].Summary != "结构化摘要主文" {
		t.Fatalf("expected plain summary from structured result, got %q", chunks[0].Summary)
	}
	if chunks[0].Structured == nil || chunks[0].Structured.Decisions[0] != "保留主语音模型" {
		t.Fatalf("expected structured decisions, got %#v", chunks[0].Structured)
	}
}

func TestMaintainFilesystemMemoryFallsBackWhenStructuredSummaryIsMissing(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 4
	plainCalled := false
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg), WithStructuredSummarizeFn(func(ctx context.Context, events []SessionEvent) ChunkStructuredSummary {
		return ChunkStructuredSummary{Decisions: []string{"保留主语音模型"}}
	}), WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		plainCalled = true
		return "plain fallback summary"
	}))

	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var msgs []llms.ChatMessage
	for i := 0; i < 9; i++ {
		msgs = append(msgs, llms.HumanChatMessage{Content: fmt.Sprintf("message %d", i)})
	}
	if err := handle.History.SetMessages(ctx, msgs); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !plainCalled {
		t.Fatalf("expected SummarizeFn to be called when structured summary text is missing")
	}

	chunks, err := NewSessionMemoryStore(filepath.Join(storageDir, "session")).RecallChunks(ctx, ChunkRecallQuery{Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %#v", chunks)
	}
	if chunks[0].Summary != "plain fallback summary" {
		t.Fatalf("expected plain fallback summary, got %q", chunks[0].Summary)
	}
	if chunks[0].Structured != nil {
		t.Fatalf("expected structured summary to be omitted when summary text is missing, got %#v", chunks[0].Structured)
	}
}

func TestMaintainFilesystemMemoryUsesLLMSummary(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	called := false
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 20
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg), WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		called = true
		return "LLM generated summary of " + fmt.Sprintf("%d", len(events)) + " events"
	}))

	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var msgs []llms.ChatMessage
	for i := 0; i < 41; i++ {
		msgs = append(msgs, llms.HumanChatMessage{Content: fmt.Sprintf("message %d", i)})
	}
	if err := handle.History.SetMessages(ctx, msgs); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if !called {
		t.Fatalf("expected SummarizeFn to be called during compression")
	}

	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	data, err := os.ReadFile(session.summaryPath())
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	if !strings.Contains(string(data), "LLM generated summary") {
		t.Fatalf("expected LLM summary in summary.md, got:\n%s", data)
	}
}

func TestMaintainFilesystemMemoryTriggersOnTokenThreshold(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 1000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100 // high event limit so only token threshold triggers
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))

	// Write 5 events (well under 100 event limit)
	sessionDir := filepath.Join(storageDir, "session")
	os.MkdirAll(sessionDir, 0o755)
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		})
	}

	// Set prompt tokens to 60% of context window (above 50% threshold)
	manager.SetLastPromptTokens(600)

	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	// Should have compressed despite only 5 events
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected compression to trigger from token threshold, got no chunks")
	}
}

func TestMaintainFilesystemMemorySkipsWhenBelowTokenThreshold(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 1000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100 // high event limit
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))

	sessionDir := filepath.Join(storageDir, "session")
	os.MkdirAll(sessionDir, 0o755)
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		})
	}

	// Set prompt tokens to 30% of context window (below 50% threshold)
	manager.SetLastPromptTokens(300)

	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	// Should NOT have compressed
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected no compression below token threshold, got %d chunks", len(chunks))
	}
}

func TestMaintainFilesystemMemoryTriggersOnReserveTokens(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 1000
	cfg.ReserveTokens = 200
	cfg.CompressAtPercent = 90 // high percent so only reserve triggers
	cfg.HotWindowEvents = 100
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))

	sessionDir := filepath.Join(storageDir, "session")
	os.MkdirAll(sessionDir, 0o755)
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		})
	}

	// Set prompt tokens to 810 (> contextWindow - reserve = 800)
	manager.SetLastPromptTokens(810)

	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected compression to trigger from reserve threshold, got no chunks")
	}
}

func TestMaintainFilesystemMemoryTriggersOnEventCountWhenTokenUnavailable(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 10
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))

	sessionDir := filepath.Join(storageDir, "session")
	os.MkdirAll(sessionDir, 0o755)
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	for i := 0; i < 21; i++ {
		session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		})
	}

	// LastPromptTokens not set (still 0), simulating cold start
	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected compression to trigger from event count fallback, got no chunks")
	}
}

func TestMemoryManagerTokenTracking(t *testing.T) {
	manager := NewMemoryManager("")
	manager.SetLastPromptTokens(5000)
	if got := manager.LastPromptTokens(); got != 5000 {
		t.Fatalf("expected 5000, got %d", got)
	}
}

func TestMemoryManagerTokenTrackingConcurrentAccess(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 1000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100
	manager := NewMemoryManager("", WithExtractionConfig(cfg))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			manager.SetLastPromptTokens(i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			_ = manager.shouldCompress(1)
		}
	}()
	wg.Wait()
}

func TestMemoryManagerClearAllRemovesFilesystemMemoryArtifacts(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 20
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg))
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	messages := []llms.ChatMessage{
		llms.HumanChatMessage{Content: "记一下，以后处理蓝海报销App超过100元必须先确认。"},
	}
	for i := 0; i < 40; i++ {
		messages = append(messages, llms.HumanChatMessage{Content: "填充对话轮次"})
	}
	if err := handle.History.SetMessages(ctx, messages); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session", "chunks", "index.yaml")); err != nil {
		t.Fatalf("expected session chunk index before clear: %v", err)
	}

	if err := manager.ClearAll(ctx, "default"); err != nil {
		t.Fatalf("ClearAll() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(storageDir, "session"),
		filepath.Join(storageDir, "long_term"),
		filepath.Join(storageDir, "lifecycle"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}
}

func TestMemoryManagerClearSessionPreservesLongTermMemory(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	// Create long_term and lifecycle directories with content
	longTermDir := filepath.Join(storageDir, "long_term")
	lifecycleDir := filepath.Join(storageDir, "lifecycle")
	if err := os.MkdirAll(longTermDir, 0o755); err != nil {
		t.Fatalf("MkdirAll long_term: %v", err)
	}
	if err := os.MkdirAll(lifecycleDir, 0o755); err != nil {
		t.Fatalf("MkdirAll lifecycle: %v", err)
	}
	profilePath := filepath.Join(longTermDir, "profile.md")
	if err := os.WriteFile(profilePath, []byte("# User Profile\nPrefers concise answers."), 0o644); err != nil {
		t.Fatalf("WriteFile profile.md: %v", err)
	}
	tombstonePath := filepath.Join(lifecycleDir, "tombstones.jsonl")
	if err := os.WriteFile(tombstonePath, []byte(`{"id":"mem-1","reason":"outdated"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile tombstones.jsonl: %v", err)
	}

	// Create session data via Save
	messages := []llms.ChatMessage{
		llms.HumanChatMessage{Content: "hello"},
	}
	for i := 0; i < 22; i++ {
		messages = append(messages, llms.HumanChatMessage{Content: "filler"})
	}
	if err := handle.History.SetMessages(ctx, messages); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	sessionDir := filepath.Join(storageDir, "session")
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("expected session dir to exist before clear: %v", err)
	}

	// ClearSession should remove session but preserve long_term and lifecycle
	if err := manager.ClearSession(ctx, "default"); err != nil {
		t.Fatalf("ClearSession() error = %v", err)
	}

	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("expected session dir to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("expected long_term/profile.md to be preserved: %v", err)
	}
	if _, err := os.Stat(tombstonePath); err != nil {
		t.Fatalf("expected lifecycle/tombstones.jsonl to be preserved: %v", err)
	}
}

func TestMaintainFilesystemMemorySplitsLongSingleTurnWithoutOrphanToolResult(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 8000   // clamps reserve to 4000, keep_recent to 4000
	cfg.CompressAtPercent = 50 // token-percent trigger
	cfg.HotWindowEvents = 1000 // event-count trigger must not fire
	cfg.KeepRecentTokens = 4000

	// Summarizer echoes event contents so we can assert the user goal survives.
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg),
		WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
			var b strings.Builder
			for _, e := range events {
				b.WriteString(e.Content)
				b.WriteString(" ")
			}
			return strings.TrimSpace(b.String())
		}))

	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()

	const goalMarker = "USER_GOAL_REIMBURSE_FLOW_42"
	// One single turn: a user goal, then many assistant/tool exchanges large
	// enough to blow past keep_recent_tokens so the cut lands mid-turn.
	mustAppend := func(typ, role, content string, i int) {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%03d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    typ, Role: role, Content: content,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	mustAppend("user_input", "user", goalMarker+" "+strings.Repeat("g", 100), 0)
	idx := 1
	for pair := 0; pair < 10; pair++ {
		mustAppend("assistant_output", "assistant", strings.Repeat("a", 3200), idx)
		idx++
		mustAppend("tool_result", "tool", strings.Repeat("r", 3200), idx)
		idx++
	}

	manager.SetLastPromptTokens(6000) // 75% of 8k window → triggers
	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory: %v", err)
	}

	hot := readSessionEvents(t, session.eventsPath())
	if len(hot) == 0 {
		t.Fatalf("expected hot events to remain after compaction")
	}
	if hot[0].Type == "tool_result" {
		t.Fatalf("hot window must not open on an orphan tool_result: %#v", hot[0])
	}
	if hot[0].EventID != "evt_000" || hot[0].Type != "user_input" || !strings.Contains(hot[0].Content, goalMarker) {
		t.Fatalf("root user goal must be pinned as the first hot event, got %#v", hot[0])
	}

	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one compacted chunk, got %d", len(chunks))
	}
	for _, event := range chunks[0].Evidence {
		if event.EventID == "evt_000" {
			t.Fatalf("root user goal must not be compressed into chunk evidence: %#v", event)
		}
	}
	if strings.Contains(chunks[0].Summary, goalMarker) {
		t.Fatalf("root user goal must not depend on chunk summary:\n%s", chunks[0].Summary)
	}

	// The split-turn cut metadata should be recorded on the index entry.
	index, err := session.loadChunkIndex()
	if err != nil {
		t.Fatalf("loadChunkIndex: %v", err)
	}
	if len(index.Chunks) != 1 || index.Chunks[0].CutMeta == nil {
		t.Fatalf("expected cut metadata on chunk index entry: %#v", index.Chunks)
	}
	if !index.Chunks[0].CutMeta.IsSplitTurn {
		t.Fatalf("expected split-turn cut metadata, got %#v", index.Chunks[0].CutMeta)
	}
}

func TestMaintainFilesystemMemoryPinsRootUserInputInHotWindow(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 8000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 1000
	cfg.KeepRecentTokens = 4000

	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg),
		WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
			var b strings.Builder
			for _, e := range events {
				b.WriteString(e.Content)
				b.WriteString(" ")
			}
			return strings.TrimSpace(b.String())
		}))

	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()

	const rootGoal = "ROOT_USER_GOAL_BOOK_FLIGHT_42"
	mustAppend := func(typ, role, content string, i int) {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%03d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    typ, Role: role, Content: content,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	mustAppend("user_input", "user", rootGoal, 0)
	for i := 1; i <= 12; i++ {
		mustAppend("assistant_output", "assistant", strings.Repeat("a", 3200), i)
	}

	manager.SetLastPromptTokens(6000)
	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory: %v", err)
	}

	hot := readSessionEvents(t, session.eventsPath())
	foundPinnedRoot := false
	for _, event := range hot {
		if event.EventID == "evt_000" && event.Type == "user_input" && event.Role == "user" && event.Content == rootGoal {
			foundPinnedRoot = true
			break
		}
	}
	if !foundPinnedRoot {
		t.Fatalf("root user_input must stay pinned in the hot window; hot=%#v", hot)
	}

	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one compacted chunk, got %d", len(chunks))
	}
	for _, event := range chunks[0].Evidence {
		if event.EventID == "evt_000" {
			t.Fatalf("root user_input must not be compressed into chunk evidence: %#v", event)
		}
	}
	if strings.Contains(chunks[0].Summary, rootGoal) {
		t.Fatalf("root user_input must not rely on chunk summary for recall:\n%s", chunks[0].Summary)
	}
}

// TestMaintainFilesystemMemoryChineseCountFallbackNoOrphanToolResult drives the
// realistic trigger chain for the count-based fallback: a Chinese session where
// the real prompt-token count (reported via SetLastPromptTokens) crosses the
// compression threshold, but the chars/4 byte estimate used by the token cut
// point stays under keep_recent — UTF-8 Chinese is ~3 bytes/char so len/4
// underestimates true tokens badly. That makes findSessionCutPoint report no
// cut, so planCompaction falls back to the count path. The raw count index lands
// on a tool_result; the fix must snap it to a legal boundary instead of opening
// the hot window on the orphan.
func TestMaintainFilesystemMemoryChineseCountFallbackNoOrphanToolResult(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 8000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 4 // keepCount=4 → rawCutIndex = 12-4 = 8 (a tool_result)
	cfg.KeepRecentTokens = 4000

	const goalMarker = "用户目标_报销流程_第42步"
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg),
		WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
			var b strings.Builder
			for _, e := range events {
				b.WriteString(e.Content)
				b.WriteString(" ")
			}
			return strings.TrimSpace(b.String())
		}))

	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	mustAppend := func(typ, role, content string, i int) {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%03d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    typ, Role: role, Content: content,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	// 12 short Chinese events. Each is tiny in bytes (~30-60), so the chars/4
	// estimate keeps the whole stream well under keep_recent (4000) and the
	// token cut point finds nothing. Layout puts a tool_result exactly at the
	// raw count index (8) and leaves the tail mostly tool_results so the old
	// turn-align guard would have fallen through to the raw orphan.
	mustAppend("user_input", "user", goalMarker+"，帮我处理一下", 0)
	mustAppend("assistant_output", "assistant", "好的，正在查询", 1)
	mustAppend("tool_result", "tool", "查询结果一", 2)
	mustAppend("user_input", "user", "继续下一步", 3)
	mustAppend("assistant_output", "assistant", "正在提交表单", 4)
	mustAppend("tool_result", "tool", "提交结果", 5)
	mustAppend("user_input", "user", "再确认一次", 6)
	mustAppend("assistant_output", "assistant", "正在确认", 7)
	mustAppend("tool_result", "tool", "确认中间态一", 8) // rawCutIndex = 8
	mustAppend("tool_result", "tool", "确认中间态二", 9)
	mustAppend("tool_result", "tool", "确认中间态三", 10)
	mustAppend("assistant_output", "assistant", "已完成确认", 11)

	// Real prompt token count crosses 50% of the 8k window, while the byte-based
	// estimate of all events combined stays far below keep_recent.
	manager.SetLastPromptTokens(6000)

	// Sanity-check the premise: the token cut point really must find nothing,
	// otherwise this test would exercise the token path instead of the fallback.
	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("readEvents: %v", err)
	}
	if cut := findSessionCutPoint(events, 0, len(events), cfg.KeepRecentTokens); cut.HasCut {
		t.Fatalf("premise broken: token cut found a cut at %d; test no longer exercises the count fallback", cut.FirstKeptIndex)
	}

	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory: %v", err)
	}

	hot := readSessionEvents(t, session.eventsPath())
	if len(hot) == 0 {
		t.Fatalf("expected hot events to remain after compaction")
	}
	if hot[0].Type == "tool_result" {
		t.Fatalf("hot window opened on an orphan tool_result: %#v", hot[0])
	}
	if hot[0].EventID != "evt_000" || hot[0].Type != "user_input" || !strings.Contains(hot[0].Content, goalMarker) {
		t.Fatalf("root user goal must be pinned as the first hot event, got %#v", hot[0])
	}

	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one compacted chunk, got %d", len(chunks))
	}
	for _, event := range chunks[0].Evidence {
		if event.EventID == "evt_000" {
			t.Fatalf("root user goal must not be compressed into chunk evidence: %#v", event)
		}
	}
	if strings.Contains(chunks[0].Summary, goalMarker) {
		t.Fatalf("root user goal must not depend on chunk summary:\n%s", chunks[0].Summary)
	}
}

func TestSessionRotationArchivesActiveSessionDirectory(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_rotate_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("old task event %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if _, err := session.Compress(ctx, CompressOption{
		ChunkID:  "chunk_old_session",
		Summary:  "OLD SESSION SUMMARY SENTINEL",
		Tags:     []string{"old"},
		Entities: []string{"OldApp"},
	}); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if !manager.HasCompressedHistory() {
		t.Fatal("expected active session to report compressed history before rotation")
	}
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	archiveDir, err := manager.RotateSessionEvents()
	if err != nil {
		t.Fatalf("RotateSessionEvents() error = %v", err)
	}
	if archiveDir == "" {
		t.Fatal("RotateSessionEvents() returned empty archive path")
	}

	if got := readSessionEvents(t, session.eventsPath()); len(got) != 0 {
		t.Fatalf("expected new events.jsonl to be empty, got %d events", len(got))
	}
	if manager.HasCompressedHistory() {
		t.Fatal("old summary.md must not make the new active session report compressed history")
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session", "summary.md")); !os.IsNotExist(err) {
		t.Fatalf("active summary.md should be absent after rotation, stat err = %v", err)
	}
	archived := readSessionEvents(t, filepath.Join(archiveDir, "events.jsonl"))
	if len(archived) != 5 {
		t.Fatalf("expected archive to contain 5 old events, got %d", len(archived))
	}
	if archived[0].EventID != "evt_rotate_0" || archived[4].EventID != "evt_rotate_4" {
		t.Fatalf("archived events not preserved in order: %#v", archived)
	}
	archivedSummary, err := os.ReadFile(filepath.Join(archiveDir, "summary.md"))
	if err != nil {
		t.Fatalf("read archived summary.md: %v", err)
	}
	if !strings.Contains(string(archivedSummary), "OLD SESSION SUMMARY SENTINEL") {
		t.Fatalf("archived summary missing old summary sentinel:\n%s", archivedSummary)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "chunks", "index.yaml")); err != nil {
		t.Fatalf("archived chunks/index.yaml missing: %v", err)
	}
	results, err := NewSessionMemoryStore(filepath.Join(storageDir, "session")).RecallChunks(ctx, ChunkRecallQuery{ChunkIDs: []string{"chunk_old_session"}})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("archived chunks must not be recalled from active session, got %#v", results)
	}
	messages, err := handle.History.Messages(ctx)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("conversation window should be empty after rotation, got %#v", messages)
	}
}

func TestSessionRotationDetailedCreatesActiveSessionID(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_before_rotate",
		Ts:      now.Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "old task",
	}); err != nil {
		t.Fatalf("AppendEvent() before rotate error = %v", err)
	}

	rotation, err := manager.RotateSessionEventsDetailed()
	if err != nil {
		t.Fatalf("RotateSessionEventsDetailed() error = %v", err)
	}
	if rotation.ArchiveDir == "" {
		t.Fatal("ArchiveDir is empty")
	}
	if rotation.ClosedSessionID != filepath.Base(rotation.ArchiveDir) {
		t.Fatalf("ClosedSessionID = %q, want archive basename %q", rotation.ClosedSessionID, filepath.Base(rotation.ArchiveDir))
	}
	if rotation.ActiveSessionID == "" {
		t.Fatal("ActiveSessionID is empty")
	}

	metadata := readSessionMetadataForTest(t, filepath.Join(storageDir, "session", sessionMetadataFileName))
	if metadata.SessionID != rotation.ActiveSessionID {
		t.Fatalf("active metadata session_id = %q, want %q", metadata.SessionID, rotation.ActiveSessionID)
	}
}

func TestSessionBoundaryLogsProminentNewSessionID(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	var buf bytes.Buffer
	logger := &Logger{logger: log.New(&buf, "", 0)}
	manager := NewMemoryManager(storageDir, WithMemoryLogger(logger))
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	oldTs := time.Now().UTC().Add(-time.Duration(DefaultBoundaryConfig().LongGapSeconds+1) * time.Second)
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_old_task",
		Ts:      oldTs.Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "old task",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	sessionManager := newMemoryManagerSessionManager(manager, nil)
	if _, err := sessionManager.BeginRun(ctx, SessionBeginRequest{
		AgentName: "default",
		Input:     "打开微信",
		Turn:      NewTextTurnInput("打开微信", nil),
		RuntimeID: "runtime-a",
	}); err != nil {
		t.Fatalf("BeginRun() error = %v", err)
	}

	logText := buf.String()
	if !strings.Contains(logText, "NEW SESSION STARTED") {
		t.Fatalf("log missing session banner:\n%s", logText)
	}
	if !strings.Contains(logText, "Session ID: ") {
		t.Fatalf("log missing prominent session id:\n%s", logText)
	}
	if !strings.Contains(logText, "Reason: "+BoundaryReasonTimeGapLong) {
		t.Fatalf("log missing boundary reason:\n%s", logText)
	}
	if strings.Contains(logText, "Runtime ID:") {
		t.Fatalf("session boundary log should not present runtime id as the primary marker:\n%s", logText)
	}
}

func TestSessionRotationAppendAfterRotateWritesNewEventsFile(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_before_rotate",
		Ts:      now.Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "old task",
	}); err != nil {
		t.Fatalf("AppendEvent() before rotate error = %v", err)
	}

	archiveDir, err := manager.RotateSessionEvents()
	if err != nil {
		t.Fatalf("RotateSessionEvents() error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_after_rotate",
		Ts:      now.Add(time.Second).Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "new task",
	}); err != nil {
		t.Fatalf("AppendEvent() after rotate error = %v", err)
	}

	active := readSessionEvents(t, session.eventsPath())
	if len(active) != 1 || active[0].EventID != "evt_after_rotate" {
		t.Fatalf("expected only new event in active events.jsonl, got %#v", active)
	}
	archived := readSessionEvents(t, filepath.Join(archiveDir, "events.jsonl"))
	if len(archived) != 1 || archived[0].EventID != "evt_before_rotate" {
		t.Fatalf("expected old event in archive, got %#v", archived)
	}
}

func TestSessionRotationDoesNotDeadlockWithMemoryLoad(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_before_deadlock_check",
		Ts:      now.Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "old task",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	done := make(chan error, 2)
	go func() {
		_, err := manager.RotateSessionEvents()
		done <- err
	}()
	go func() {
		_, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
		done <- err
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("concurrent operation error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("RotateSessionEvents and Get appear to be deadlocked")
		}
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitMaintenance(waitCtx); err != nil {
		t.Fatalf("WaitMaintenance() error = %v", err)
	}
}

func TestMaintainFilesystemMemorySkipsActiveWriteAfterRotation(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 4
	cfg.CountCompressAfterEvents = 8

	summaryStarted := make(chan struct{})
	releaseSummary := make(chan struct{})
	var summaryStartedOnce sync.Once
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg), WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		summaryStartedOnce.Do(func() { close(summaryStarted) })
		select {
		case <-ctx.Done():
			return ""
		case <-releaseSummary:
			return "old active summary"
		}
	}))
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_old_active_%02d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("old active %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() old error = %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- manager.maintainFilesystemMemory(ctx)
	}()
	select {
	case <-summaryStarted:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start summary")
	}
	if _, err := manager.RotateSessionEvents(); err != nil {
		t.Fatalf("RotateSessionEvents() error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_new_active",
		Ts:      now.Add(time.Minute).Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "new active",
	}); err != nil {
		t.Fatalf("AppendEvent() new error = %v", err)
	}
	close(releaseSummary)
	if err := <-done; err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	active := readSessionEvents(t, session.eventsPath())
	if len(active) != 1 || active[0].EventID != "evt_new_active" {
		t.Fatalf("maintenance wrote old session back into active events: %#v", active)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.WaitMaintenance(waitCtx); err != nil {
		t.Fatalf("WaitMaintenance() error = %v", err)
	}
}

func TestSessionMemoryRecallIgnoresPendingFiles(t *testing.T) {
	ctx := context.Background()
	session := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))
	if err := os.MkdirAll(session.rootDir, 0o755); err != nil {
		t.Fatalf("MkdirAll session dir: %v", err)
	}
	pendingPath := filepath.Join(session.rootDir, "events.pending-123.jsonl")
	pendingEvents := []SessionEvent{{
		EventID: "evt_pending_1",
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Type:    "user_input",
		Role:    "user",
		Content: "查一下明天天气",
		AppName: "WeatherApp",
	}}
	data, _, err := encodeSessionEventsJSONL(pendingEvents)
	if err != nil {
		t.Fatalf("encode pending events: %v", err)
	}
	if err := os.WriteFile(pendingPath, data, 0o644); err != nil {
		t.Fatalf("write pending file: %v", err)
	}

	results, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("pending files must not be recalled from active session, got %#v", results)
	}
}

func TestSessionMemoryRecallDoesNotLetPendingReplaceActive(t *testing.T) {
	ctx := context.Background()
	session := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))
	for i := 0; i < 3; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_active_%d", i),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("active %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() active error = %v", err)
		}
		if _, err := session.Compress(ctx, CompressOption{
			ChunkID: fmt.Sprintf("chunk_%03d", i),
			Summary: fmt.Sprintf("active summary %d", i),
		}); err != nil {
			t.Fatalf("Compress() error = %v", err)
		}
	}
	pendingPath := filepath.Join(session.rootDir, "events.pending-999.jsonl")
	data, _, err := encodeSessionEventsJSONL([]SessionEvent{{
		EventID: "evt_pending_slot",
		Type:    "user_input",
		Role:    "user",
		Content: "pending should be visible",
	}})
	if err != nil {
		t.Fatalf("encode pending: %v", err)
	}
	if err := os.WriteFile(pendingPath, data, 0o644); err != nil {
		t.Fatalf("write pending: %v", err)
	}

	results, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 2})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %#v", results)
	}
	for _, result := range results {
		if strings.HasPrefix(result.ChunkID, "pending-") {
			t.Fatalf("pending file replaced an active recall result: %#v", results)
		}
	}
}

func TestMaintainFilesystemMemoryIgnoresPendingFiles(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	if err := os.MkdirAll(session.rootDir, 0o755); err != nil {
		t.Fatalf("MkdirAll session dir: %v", err)
	}
	pendingPath := filepath.Join(session.rootDir, "events.pending-123.jsonl")
	now := time.Now().UTC()
	pendingEvents := []SessionEvent{
		{EventID: "evt_pending_1", Ts: now.Format(time.RFC3339Nano), Type: "user_input", Role: "user", Content: "打开微信"},
		{EventID: "evt_pending_2", Ts: now.Add(time.Second).Format(time.RFC3339Nano), Type: "assistant_output", Role: "assistant", Content: "已打开"},
	}
	data, _, err := encodeSessionEventsJSONL(pendingEvents)
	if err != nil {
		t.Fatalf("encode pending events: %v", err)
	}
	if err := os.WriteFile(pendingPath, data, 0o644); err != nil {
		t.Fatalf("write pending file: %v", err)
	}

	manager := NewMemoryManager(storageDir, WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		return fmt.Sprintf("pending summary with %d events", len(events))
	}))
	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("pending file should remain inert and uncompressed, stat err = %v", err)
	}
	results, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("pending file must not be compressed into active chunks, got %#v", results)
	}
	if _, err := os.Stat(filepath.Join(session.rootDir, "summary.md")); !os.IsNotExist(err) {
		t.Fatalf("pending file must not create active summary.md, stat err = %v", err)
	}
}

func readSessionEvents(t *testing.T, path string) []SessionEvent {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open session events: %v", err)
	}
	defer file.Close()

	var events []SessionEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event SessionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode session event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan session events: %v", err)
	}
	return events
}

func readSessionMetadataForTest(t *testing.T, path string) sessionMetadata {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session metadata: %v", err)
	}
	var metadata sessionMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode session metadata: %v", err)
	}
	return metadata
}

func TestFormatSessionSummaryWithWindowKeepsMaxChunks(t *testing.T) {
	existing := []byte("# Session History (compressed chunks)\n\n" +
		"Use recall_session_chunks with a chunk_id to retrieve full conversation details.\n\n" +
		"- **chunk_001**\n  Summary 1\n" +
		"- **chunk_002**\n  Summary 2\n" +
		"- **chunk_003**\n  Summary 3\n")

	newChunk := chunkIndexEntry{ID: "chunk_004", Summary: "Summary 4"}
	summary, archive := formatSessionSummaryWithWindow(existing, nil, newChunk, 3)

	if !strings.Contains(summary, "chunk_002") {
		t.Fatalf("expected summary to contain chunk_002, got:\n%s", summary)
	}
	if !strings.Contains(summary, "chunk_003") {
		t.Fatalf("expected summary to contain chunk_003, got:\n%s", summary)
	}
	if !strings.Contains(summary, "chunk_004") {
		t.Fatalf("expected summary to contain chunk_004, got:\n%s", summary)
	}
	if !strings.Contains(summary, "## Rolling Summary") {
		t.Fatalf("expected Rolling Summary section, got:\n%s", summary)
	}
	if !strings.Contains(summary, "## Recent Chunks") {
		t.Fatalf("expected Recent Chunks section, got:\n%s", summary)
	}
	recentChunksPart := summary[strings.Index(summary, "## Recent Chunks"):]
	if strings.Contains(recentChunksPart, "chunk_001") {
		t.Fatalf("expected chunk_001 NOT in Recent Chunks section, got:\n%s", summary)
	}

	if !strings.Contains(archive, "chunk_001") {
		t.Fatalf("expected archive to contain chunk_001, got:\n%s", archive)
	}
	if strings.Contains(archive, "chunk_002") {
		t.Fatalf("expected archive NOT to contain chunk_002, got:\n%s", archive)
	}
}

func TestFormatSessionSummaryWithWindowNoArchiveWhenUnderMax(t *testing.T) {
	existing := []byte("# Session History (compressed chunks)\n\n" +
		"Use recall_session_chunks with a chunk_id to retrieve full conversation details.\n\n" +
		"- **chunk_001**\n  Summary 1\n")

	newChunk := chunkIndexEntry{ID: "chunk_002", Summary: "Summary 2"}
	summary, archive := formatSessionSummaryWithWindow(existing, nil, newChunk, 10)

	if !strings.Contains(summary, "chunk_001") || !strings.Contains(summary, "chunk_002") {
		t.Fatalf("expected summary to contain both chunks, got:\n%s", summary)
	}
	if archive != "" {
		t.Fatalf("expected no archive content, got:\n%s", archive)
	}
}

func TestFormatSessionSummaryWithWindowAppendsToExistingArchive(t *testing.T) {
	existing := []byte("# Session History (compressed chunks)\n\n" +
		"Use recall_session_chunks with a chunk_id to retrieve full conversation details.\n\n" +
		"- **chunk_010**\n  Summary 10\n" +
		"- **chunk_011**\n  Summary 11\n")
	existingArchive := []byte("# Session History Archive\n\n" +
		"Older chunk summaries moved out of the active session summary.\n" +
		"Use recall_session_chunks with a chunk_id to retrieve full conversation details.\n\n" +
		"- **chunk_001**\n  Summary 1\n" +
		"- **chunk_009**\n  Summary 9\n")

	newChunk := chunkIndexEntry{ID: "chunk_012", Summary: "Summary 12"}
	summary, archive := formatSessionSummaryWithWindow(existing, existingArchive, newChunk, 2)

	if !strings.Contains(summary, "## Rolling Summary") {
		t.Fatalf("expected Rolling Summary section, got:\n%s", summary)
	}
	if !strings.Contains(summary, "## Recent Chunks") {
		t.Fatalf("expected Recent Chunks section, got:\n%s", summary)
	}
	recentChunksPart := summary[strings.Index(summary, "## Recent Chunks"):]
	if strings.Contains(recentChunksPart, "chunk_010") {
		t.Fatalf("expected chunk_010 NOT in Recent Chunks section, got:\n%s", summary)
	}
	if !strings.Contains(summary, "chunk_011") || !strings.Contains(summary, "chunk_012") {
		t.Fatalf("expected summary to contain chunk_011 and chunk_012, got:\n%s", summary)
	}

	if !strings.Contains(archive, "chunk_001") {
		t.Fatalf("expected archive to preserve chunk_001, got:\n%s", archive)
	}
	if !strings.Contains(archive, "chunk_009") {
		t.Fatalf("expected archive to preserve chunk_009, got:\n%s", archive)
	}
	if !strings.Contains(archive, "chunk_010") {
		t.Fatalf("expected archive to contain newly archived chunk_010, got:\n%s", archive)
	}
}

func TestFormatSessionSummaryWithWindowUpsertsExistingChunk(t *testing.T) {
	existing := []byte("# Session History (compressed chunks)\n\n" +
		"## Recent Chunks\n\n" +
		"- **pending-123**\n  Old summary\n")

	summary, archive := formatSessionSummaryWithWindow(existing, nil, chunkIndexEntry{ID: "pending-123", Summary: "New summary"}, 10)
	if archive != "" {
		t.Fatalf("expected no archive, got:\n%s", archive)
	}
	if strings.Count(summary, "pending-123") != 1 {
		t.Fatalf("expected pending-123 to appear once, got:\n%s", summary)
	}
	if !strings.Contains(summary, "New summary") || strings.Contains(summary, "Old summary") {
		t.Fatalf("expected summary to be updated, got:\n%s", summary)
	}
}

func TestSessionMemoryStoreCompressCreatesArchive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewSessionMemoryStore(filepath.Join(root, "session"), 2)

	for i := 0; i < 3; i++ {
		if _, err := store.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("message %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
		if _, err := store.Compress(ctx, CompressOption{
			ChunkID: fmt.Sprintf("chunk_%03d", i),
			Summary: fmt.Sprintf("Summary %d", i),
		}); err != nil {
			t.Fatalf("Compress() error = %v", err)
		}
	}

	summaryData, err := os.ReadFile(filepath.Join(root, "session", "summary.md"))
	if err != nil {
		t.Fatalf("ReadFile summary.md error = %v", err)
	}
	summary := string(summaryData)
	if !strings.Contains(summary, "## Rolling Summary") {
		t.Fatalf("expected summary.md to contain Rolling Summary section, got:\n%s", summary)
	}
	if !strings.Contains(summary, "## Recent Chunks") {
		t.Fatalf("expected summary.md to contain Recent Chunks section, got:\n%s", summary)
	}
	if !strings.Contains(summary, "chunk_000") {
		t.Fatalf("expected chunk_000 to appear in rolling summary, got:\n%s", summary)
	}
	if !strings.Contains(summary, "chunk_001") || !strings.Contains(summary, "chunk_002") {
		t.Fatalf("expected summary to contain chunk_001 and chunk_002 in recent chunks, got:\n%s", summary)
	}

	archiveData, err := os.ReadFile(filepath.Join(root, "session", "summary_archive.md"))
	if err != nil {
		t.Fatalf("ReadFile summary_archive.md error = %v", err)
	}
	archive := string(archiveData)
	if !strings.Contains(archive, "chunk_000") {
		t.Fatalf("expected archive to contain chunk_000, got:\n%s", archive)
	}
}

func TestSessionMemoryStoreRecallHitsArchivedChunks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewSessionMemoryStore(filepath.Join(root, "session"), 2)

	for i := 0; i < 4; i++ {
		if _, err := store.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("message %d", i),
			AppName: "TestApp",
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
		if _, err := store.Compress(ctx, CompressOption{
			ChunkID:  fmt.Sprintf("chunk_%03d", i),
			Summary:  fmt.Sprintf("Summary %d", i),
			Tags:     []string{"test"},
			Entities: []string{"TestApp"},
		}); err != nil {
			t.Fatalf("Compress() error = %v", err)
		}
	}

	results, err := store.RecallChunks(ctx, ChunkRecallQuery{ChunkIDs: []string{"chunk_000"}})
	if err != nil {
		t.Fatalf("RecallChunks() by ID error = %v", err)
	}
	if len(results) != 1 || results[0].ChunkID != "chunk_000" {
		t.Fatalf("expected to recall archived chunk_000, got %d results", len(results))
	}

	results, err = store.RecallChunks(ctx, ChunkRecallQuery{Tags: []string{"test"}, Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() by tags error = %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 recall results (including archived), got %d", len(results))
	}
}

func TestSummaryMaxChunksConfigDefaultAndCustom(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	if cfg.SummaryMaxChunks != 10 {
		t.Fatalf("expected default SummaryMaxChunks=10, got %d", cfg.SummaryMaxChunks)
	}
	if !cfg.SessionBoundaryEnabled {
		t.Fatalf("expected session boundary detection to be enabled by default")
	}
	if cfg.SessionBoundaryShortGapSeconds != 300 {
		t.Fatalf("expected default short gap 300s, got %d", cfg.SessionBoundaryShortGapSeconds)
	}
	if cfg.SessionBoundaryLongGapSeconds != 1800 {
		t.Fatalf("expected default long gap 1800s, got %d", cfg.SessionBoundaryLongGapSeconds)
	}

	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte(strings.Join([]string{
		"summary_max_chunks: 5",
		"session_boundary_enabled: false",
		"session_boundary_short_gap_seconds: 15",
		"session_boundary_long_gap_seconds: 120",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := LoadMemoryExtractionConfig(dir)
	if loaded.SummaryMaxChunks != 5 {
		t.Fatalf("expected loaded SummaryMaxChunks=5, got %d", loaded.SummaryMaxChunks)
	}
	if loaded.SessionBoundaryEnabled {
		t.Fatalf("expected loaded SessionBoundaryEnabled=false")
	}
	if loaded.SessionBoundaryShortGapSeconds != 15 || loaded.SessionBoundaryLongGapSeconds != 120 {
		t.Fatalf("unexpected loaded session boundary gaps: short=%d long=%d",
			loaded.SessionBoundaryShortGapSeconds, loaded.SessionBoundaryLongGapSeconds)
	}
}

func TestWithSessionBoundaryEnabledOverridesProgrammaticConfig(t *testing.T) {
	manager := NewMemoryManager(t.TempDir(), WithSessionBoundaryEnabled(false))
	if manager.extraction.SessionBoundaryEnabled {
		t.Fatalf("expected explicit programmatic override to disable session boundary")
	}

	manager = NewMemoryManager(t.TempDir(), WithSessionBoundaryEnabled(false), WithExtractionConfig(DefaultMemoryExtractionConfig()))
	if manager.extraction.SessionBoundaryEnabled {
		t.Fatalf("expected later normalization to preserve explicit disable")
	}
}

func TestSessionMemoryBackwardCompatLoadsOldIndexWithoutStructured(t *testing.T) {
	ctx := context.Background()
	sessionDir := filepath.Join(t.TempDir(), "session")
	chunksDir := filepath.Join(sessionDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll chunks: %v", err)
	}

	// Write a chunk file
	chunkData := `{"event_id":"evt_1","ts":"2026-06-01T10:00:00Z","type":"user_input","role":"user","content":"old message"}
`
	chunkPath := filepath.Join(chunksDir, "chunk_old.jsonl")
	if err := os.WriteFile(chunkPath, []byte(chunkData), 0o644); err != nil {
		t.Fatalf("WriteFile chunk: %v", err)
	}

	// Write an old index.yaml WITHOUT the structured: field
	oldIndexYAML := `version: 1
updated_at: "2026-06-01T10:00:00Z"
chunks:
  - id: chunk_old
    file: chunk_old.jsonl
    status: active
    summary: Old chunk without structured field
    tags:
      - legacy
    entities:
      - OldApp
    event_count: 1
    checksum: sha256:abc123
`
	indexPath := filepath.Join(chunksDir, "index.yaml")
	if err := os.WriteFile(indexPath, []byte(oldIndexYAML), 0o644); err != nil {
		t.Fatalf("WriteFile index: %v", err)
	}

	// Load and recall
	session := NewSessionMemoryStore(sessionDir)
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{ChunkIDs: []string{"chunk_old"}})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Summary != "Old chunk without structured field" {
		t.Fatalf("summary = %q", chunks[0].Summary)
	}
	if chunks[0].Structured != nil {
		t.Fatalf("expected nil structured for old chunk, got %#v", chunks[0].Structured)
	}
	if len(chunks[0].Evidence) != 1 || chunks[0].Evidence[0].Content != "old message" {
		t.Fatalf("unexpected evidence: %#v", chunks[0].Evidence)
	}

	// Compress a new chunk and verify old entry is preserved
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_2",
		Type:    "user_input",
		Role:    "user",
		Content: "new message",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	newChunk, err := session.Compress(ctx, CompressOption{
		ChunkID: "chunk_new",
		Summary: "New chunk",
		Structured: ChunkStructuredSummary{
			Summary:   "New chunk with structured",
			Decisions: []string{"use structured"},
		},
	})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if newChunk.Structured == nil || newChunk.Structured.Decisions[0] != "use structured" {
		t.Fatalf("new chunk missing structured: %#v", newChunk)
	}

	// Verify old chunk still loads without structured
	allChunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() all error = %v", err)
	}
	if len(allChunks) != 2 {
		t.Fatalf("expected 2 chunks after compress, got %d", len(allChunks))
	}
	var oldChunk, newChunkRecalled *ChunkRecallResult
	for i := range allChunks {
		if allChunks[i].ChunkID == "chunk_old" {
			oldChunk = &allChunks[i]
		}
		if allChunks[i].ChunkID == "chunk_new" {
			newChunkRecalled = &allChunks[i]
		}
	}
	if oldChunk == nil || newChunkRecalled == nil {
		t.Fatalf("missing old or new chunk in recall: %#v", allChunks)
	}
	if oldChunk.Structured != nil {
		t.Fatalf("old chunk grew structured field after round-trip: %#v", oldChunk.Structured)
	}
	if newChunkRecalled.Structured == nil {
		t.Fatalf("new chunk lost structured field: %#v", newChunkRecalled)
	}
}

func TestRecallSessionChunksToolReturnsStructuredFieldInJSONOutput(t *testing.T) {
	ctx := context.Background()
	sessionDir := filepath.Join(t.TempDir(), "session")
	session := NewSessionMemoryStore(sessionDir)

	// Create chunk with structured summary
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_1",
		Type:    "user_input",
		Role:    "user",
		Content: "test event",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	structured := ChunkStructuredSummary{
		Summary:        "Tool output test",
		Decisions:      []string{"decision A"},
		OpenTasks:      []string{"task B"},
		ConfirmedFacts: []string{"fact C"},
	}
	if _, err := session.Compress(ctx, CompressOption{
		ChunkID:    "chunk_tool_test",
		Summary:    structured.Summary,
		Structured: structured,
	}); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	// Create chunk without structured (old format simulation) - start fresh events
	if err := session.replaceEvents(nil); err != nil {
		t.Fatalf("replaceEvents() error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_2",
		Type:    "user_input",
		Role:    "user",
		Content: "old event",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := session.Compress(ctx, CompressOption{
		ChunkID: "chunk_old_format",
		Summary: "Old chunk no structured",
	}); err != nil {
		t.Fatalf("Compress() old error = %v", err)
	}

	// Call the tool
	tool := NewRecallSessionChunksTool(session)
	output, err := tool.Call(ctx, `{"chunk_ids":["chunk_tool_test","chunk_old_format"]}`)
	if err != nil {
		t.Fatalf("tool.Call() error = %v", err)
	}

	// Parse JSON output
	var toolResult struct {
		Results []struct {
			ChunkID    string `json:"chunk_id"`
			Summary    string `json:"summary"`
			Structured *struct {
				Summary        string   `json:"summary"`
				Decisions      []string `json:"decisions"`
				OpenTasks      []string `json:"open_tasks"`
				ConfirmedFacts []string `json:"confirmed_facts"`
			} `json:"structured,omitempty"`
			Evidence []struct {
				EventID string `json:"event_id"`
				Content string `json:"content"`
			} `json:"evidence"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &toolResult); err != nil {
		t.Fatalf("unmarshal tool output: %v\noutput: %s", err, output)
	}

	if len(toolResult.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(toolResult.Results))
	}

	// Find each chunk
	var withStructured, withoutStructured *struct {
		ChunkID    string `json:"chunk_id"`
		Summary    string `json:"summary"`
		Structured *struct {
			Summary        string   `json:"summary"`
			Decisions      []string `json:"decisions"`
			OpenTasks      []string `json:"open_tasks"`
			ConfirmedFacts []string `json:"confirmed_facts"`
		} `json:"structured,omitempty"`
		Evidence []struct {
			EventID string `json:"event_id"`
			Content string `json:"content"`
		} `json:"evidence"`
	}
	for i := range toolResult.Results {
		if toolResult.Results[i].ChunkID == "chunk_tool_test" {
			withStructured = &toolResult.Results[i]
		}
		if toolResult.Results[i].ChunkID == "chunk_old_format" {
			withoutStructured = &toolResult.Results[i]
		}
	}
	if withStructured == nil || withoutStructured == nil {
		t.Fatalf("missing chunks in tool output: %s", output)
	}

	// Verify structured chunk has all fields
	if withStructured.Structured == nil {
		t.Fatalf("chunk_tool_test missing structured in JSON output")
	}
	if withStructured.Structured.Decisions[0] != "decision A" {
		t.Fatalf("structured.Decisions = %v, want [decision A]", withStructured.Structured.Decisions)
	}
	if withStructured.Structured.OpenTasks[0] != "task B" {
		t.Fatalf("structured.OpenTasks = %v, want [task B]", withStructured.Structured.OpenTasks)
	}
	if withStructured.Structured.ConfirmedFacts[0] != "fact C" {
		t.Fatalf("structured.ConfirmedFacts = %v, want [fact C]", withStructured.Structured.ConfirmedFacts)
	}
	if len(withStructured.Evidence) != 1 || withStructured.Evidence[0].Content != "test event" {
		t.Fatalf("unexpected evidence: %#v", withStructured.Evidence)
	}

	// Verify old chunk has nil structured (omitempty skips it in JSON)
	if withoutStructured.Structured != nil {
		t.Fatalf("chunk_old_format should have nil structured in JSON output, got %#v", withoutStructured.Structured)
	}
	if withoutStructured.Summary != "Old chunk no structured" {
		t.Fatalf("summary = %q", withoutStructured.Summary)
	}
	if len(withoutStructured.Evidence) != 1 || withoutStructured.Evidence[0].Content != "old event" {
		t.Fatalf("unexpected evidence: %#v", withoutStructured.Evidence)
	}
}
