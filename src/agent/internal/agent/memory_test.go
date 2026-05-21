package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestMemoryManagerClearRemovesFilesystemMemoryArtifacts(t *testing.T) {
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

	if err := manager.Clear(ctx, "default"); err != nil {
		t.Fatalf("Clear() error = %v", err)
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
