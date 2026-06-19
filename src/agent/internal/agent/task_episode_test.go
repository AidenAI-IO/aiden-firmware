package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTaskEpisodeStoreAddEpisodeOmitsToolCallContentBeforePersistence(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	store := NewTaskEpisodeStore(rootDir)

	if _, err := store.AddEpisode(ctx, TaskEpisode{
		ID:        "ep_tool_call_content",
		Status:    "active",
		StartedAt: "2026-06-19T12:45:00Z",
		EndedAt:   "2026-06-19T12:45:10Z",
		UserGoal:  "记住我在上海",
		Outcome:   TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{
				EventID:   "evt_tool",
				Type:      runEventToolCall,
				Role:      "assistant",
				ToolName:  "save_memory",
				ToolInput: `{"memory":"用户住在上海"}`,
				Content:   "好的，已经记下来了，你住在上海。",
			},
		},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	events, err := readEpisodeEvents(filepath.Join(rootDir, "2026", "ep_tool_call_content", "events.jsonl"))
	if err != nil {
		t.Fatalf("readEpisodeEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Content != "" {
		t.Fatalf("persisted tool_call content = %q, want empty", events[0].Content)
	}
	if events[0].ToolName != "save_memory" || events[0].ToolInput != `{"memory":"用户住在上海"}` {
		t.Fatalf("tool metadata was not preserved: %#v", events[0])
	}
}

func TestTaskEpisodeStoreAppendEpisodeEventOmitsToolCallContentBeforePersistence(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	store := NewTaskEpisodeStore(rootDir)
	episode := TaskEpisode{
		ID:        "ep_running_tool_call_content",
		Status:    "running",
		StartedAt: "2026-06-19T12:45:00Z",
		UserGoal:  "查询天气",
	}

	if _, err := store.StartEpisode(ctx, episode); err != nil {
		t.Fatalf("StartEpisode() error = %v", err)
	}
	if err := store.AppendEpisodeEvent(ctx, episode, TaskEpisodeEvent{
		EventID:   "evt_tool",
		Type:      runEventToolCall,
		Role:      "assistant",
		ToolName:  "weather",
		ToolInput: `{"location":"Shanghai"}`,
		Content:   "我先查一下。",
	}, 1); err != nil {
		t.Fatalf("AppendEpisodeEvent() error = %v", err)
	}

	events, err := readEpisodeEvents(filepath.Join(rootDir, "2026", "ep_running_tool_call_content", "events.jsonl"))
	if err != nil {
		t.Fatalf("readEpisodeEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Content != "" {
		t.Fatalf("persisted tool_call content = %q, want empty", events[0].Content)
	}
	if events[0].ToolName != "weather" || events[0].ToolInput != `{"location":"Shanghai"}` {
		t.Fatalf("tool metadata was not preserved: %#v", events[0])
	}
}
