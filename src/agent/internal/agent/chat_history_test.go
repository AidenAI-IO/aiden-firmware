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

func TestChatHistoryStoreOmitsToolCallContentBeforePersistence(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	ctx := context.Background()

	var callbackMessages []Message
	store.SetOnNewMessage(func(message Message) {
		callbackMessages = append(callbackMessages, message)
	})

	if err := store.Append(ctx, Message{
		Type:      runEventToolCall,
		Content:   "我先查一下。",
		ToolName:  "weather",
		ToolInput: `{"location":"Shanghai"}`,
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("Append tool call: %v", err)
	}

	messages, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].Content != "" {
		t.Fatalf("persisted tool_call content = %q, want empty", messages[0].Content)
	}
	if messages[0].ToolName != "weather" || messages[0].ToolInput != `{"location":"Shanghai"}` {
		t.Fatalf("tool metadata was not preserved: %#v", messages[0])
	}
	if len(callbackMessages) != 1 || callbackMessages[0].Content != "" {
		t.Fatalf("callback should receive sanitized persisted message, got %#v", callbackMessages)
	}
}

func TestChatHistoryStoreNormalizesMessageTypeBeforeToolCallPersistence(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	ctx := context.Background()

	if err := store.Append(ctx, Message{
		Type:     " \t" + runEventToolCall + " \n",
		Content:  "我先查一下。",
		ToolName: "weather",
	}); err != nil {
		t.Fatalf("Append tool call: %v", err)
	}

	messages, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].Type != runEventToolCall {
		t.Fatalf("persisted message type = %q, want %q", messages[0].Type, runEventToolCall)
	}
	if messages[0].Content != "" {
		t.Fatalf("persisted tool_call content = %q, want empty", messages[0].Content)
	}
}
