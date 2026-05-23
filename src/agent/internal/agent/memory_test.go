package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	manager := NewMemoryManager(storageDir)
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
	for i := 0; i < 22; i++ {
		messages = append(messages, llms.HumanChatMessage{Content: "填充对话轮次"})
	}
	if err := handle.History.SetMessages(ctx, messages); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := manager.Save(ctx, "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) > 20 {
		t.Fatalf("expected session events to be compacted to hot window <= 20, got %d", len(events))
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

func TestMaintainFilesystemMemoryUsesLLMSummary(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	called := false
	manager := NewMemoryManager(storageDir, WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		called = true
		return "LLM generated summary of " + fmt.Sprintf("%d", len(events)) + " events"
	}))

	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var msgs []llms.ChatMessage
	for i := 0; i < 22; i++ {
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
	manager := NewMemoryManager(storageDir)
	handle, err := manager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	messages := []llms.ChatMessage{
		llms.HumanChatMessage{Content: "记一下，以后处理蓝海报销App超过100元必须先确认。"},
	}
	for i := 0; i < 22; i++ {
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
	if strings.Contains(summary, "chunk_001") {
		t.Fatalf("expected summary NOT to contain chunk_001, got:\n%s", summary)
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

	if strings.Contains(summary, "chunk_010") {
		t.Fatalf("expected chunk_010 to be archived out of summary, got:\n%s", summary)
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
	if strings.Contains(summary, "chunk_000") {
		t.Fatalf("expected chunk_000 to be archived out of summary.md, got:\n%s", summary)
	}
	if !strings.Contains(summary, "chunk_001") || !strings.Contains(summary, "chunk_002") {
		t.Fatalf("expected summary to contain chunk_001 and chunk_002, got:\n%s", summary)
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

	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte("summary_max_chunks: 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := LoadMemoryExtractionConfig(dir)
	if loaded.SummaryMaxChunks != 5 {
		t.Fatalf("expected loaded SummaryMaxChunks=5, got %d", loaded.SummaryMaxChunks)
	}
}
