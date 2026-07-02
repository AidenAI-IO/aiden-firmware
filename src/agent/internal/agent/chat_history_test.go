package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestChatHistoryStorePersistsLightweightMessages(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	ctx := context.Background()

	if err := store.Append(ctx, Message{
		Type:      "user",
		Content:   "see attached",
		Timestamp: time.Now(),
		Attachments: []MessageAttachment{{
			Kind:     AttachmentKindImage,
			MIMEType: "image/png",
			Data:     "large-base64",
			Size:     12,
		}},
	}); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if err := store.Append(ctx, Message{
		Type:    "tool_result",
		Content: `{"format":"jpeg","width":10,"height":20,"size":30,"data":"screenshot-base64"}`,
	}); err != nil {
		t.Fatalf("Append tool result: %v", err)
	}

	messages, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if messages[0].Attachments[0].Data != "" {
		t.Fatalf("attachment data should be omitted: %#v", messages[0].Attachments[0])
	}
	if strings.Contains(messages[1].Content, "screenshot-base64") || strings.Contains(messages[1].Content, `"data"`) {
		t.Fatalf("tool result screenshot data should be omitted: %s", messages[1].Content)
	}
}

func TestChatHistoryStoreKeepsOnlyPublicMessageTypes(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	ctx := context.Background()

	for _, message := range []Message{
		{Type: "role_output", Content: "debug"},
		{Type: "episode_status", Content: "completed"},
		{Type: "assistant_output", Role: "assistant", Content: "done"},
		{Type: runEventToolCall, ToolName: "audio_volume", ToolInput: "{}"},
	} {
		if err := store.Append(ctx, message); err != nil {
			t.Fatalf("Append(%s): %v", message.Type, err)
		}
	}

	messages, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 public messages: %#v", len(messages), messages)
	}
	if messages[0].Type != "assistant" || messages[0].Role != "" || messages[0].Content != "done" {
		t.Fatalf("assistant_output was not normalized: %#v", messages[0])
	}
	if messages[1].Type != runEventToolCall || messages[1].ToolName != "audio_volume" {
		t.Fatalf("tool_call was not preserved: %#v", messages[1])
	}
}
