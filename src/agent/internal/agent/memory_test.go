package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
