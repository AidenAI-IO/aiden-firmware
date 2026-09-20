package executor

import (
	"testing"

	"aiden-agent/internal/agent/messages"
)

func TestMergeConsecutiveMessagesTransform(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		result := transform.Transform([]messages.Message{})
		if len(result) != 0 {
			t.Errorf("expected empty result, got %d messages", len(result))
		}
	})

	t.Run("single message", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleUser, Content: "hello"},
		}
		result := transform.Transform(input)
		if len(result) != 1 {
			t.Errorf("expected 1 message, got %d", len(result))
		}
	})

	t.Run("merges consecutive user messages", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleUser, Content: "first"},
			{Role: messages.MessageRoleUser, Content: "second"},
			{Role: messages.MessageRoleUser, Content: "third"},
		}
		result := transform.Transform(input)
		if len(result) != 1 {
			t.Fatalf("expected 1 message, got %d", len(result))
		}
		if result[0].Role != messages.MessageRoleUser {
			t.Errorf("expected user role, got %s", result[0].Role)
		}
		expected := "first\n\nsecond\n\nthird"
		if result[0].Content != expected {
			t.Errorf("expected content %q, got %q", expected, result[0].Content)
		}
	})

	t.Run("does not merge different roles", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleUser, Content: "user message"},
			{Role: messages.MessageRoleAssistant, Content: "assistant message"},
			{Role: messages.MessageRoleUser, Content: "another user message"},
		}
		result := transform.Transform(input)
		if len(result) != 3 {
			t.Errorf("expected 3 messages, got %d", len(result))
		}
	})

	t.Run("does not merge tool result messages", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleToolResult, Content: "result 1"},
			{Role: messages.MessageRoleToolResult, Content: "result 2"},
		}
		result := transform.Transform(input)
		if len(result) != 2 {
			t.Errorf("expected 2 messages (no merge), got %d", len(result))
		}
	})

	t.Run("does not merge messages with tool calls", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleAssistant, Content: "thinking"},
			{Role: messages.MessageRoleAssistant, Content: "calling tool", ToolCalls: []messages.ToolCall{{ID: "1", Name: "test"}}},
		}
		result := transform.Transform(input)
		if len(result) != 2 {
			t.Errorf("expected 2 messages (no merge due to tool calls), got %d", len(result))
		}
	})

	t.Run("merges attachments", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleUser, Content: "first", Attachments: []messages.Attachment{{FilePath: "file1.txt", MIMEType: "text/plain"}}},
			{Role: messages.MessageRoleUser, Content: "second", Attachments: []messages.Attachment{{FilePath: "file2.txt", MIMEType: "text/plain"}}},
		}
		result := transform.Transform(input)
		if len(result) != 1 {
			t.Fatalf("expected 1 message, got %d", len(result))
		}
		if len(result[0].Attachments) != 2 {
			t.Errorf("expected 2 attachments, got %d", len(result[0].Attachments))
		}
	})

	t.Run("handles empty content in consecutive messages", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleUser, Content: "first"},
			{Role: messages.MessageRoleUser, Content: ""},
			{Role: messages.MessageRoleUser, Content: "third"},
		}
		result := transform.Transform(input)
		if len(result) != 1 {
			t.Fatalf("expected 1 message, got %d", len(result))
		}
		expected := "first\n\nthird"
		if result[0].Content != expected {
			t.Errorf("expected content %q, got %q", expected, result[0].Content)
		}
	})

	t.Run("complex scenario with multiple role changes", func(t *testing.T) {
		transform := MergeConsecutiveMessagesTransform{}
		input := []messages.Message{
			{Role: messages.MessageRoleSystem, Content: "sys1"},
			{Role: messages.MessageRoleSystem, Content: "sys2"},
			{Role: messages.MessageRoleUser, Content: "user1"},
			{Role: messages.MessageRoleUser, Content: "user2"},
			{Role: messages.MessageRoleAssistant, Content: "assistant1"},
			{Role: messages.MessageRoleAssistant, Content: "assistant2"},
			{Role: messages.MessageRoleUser, Content: "user3"},
		}
		result := transform.Transform(input)
		if len(result) != 4 {
			t.Fatalf("expected 4 messages (merged pairs), got %d", len(result))
		}
		if result[0].Role != messages.MessageRoleSystem || result[0].Content != "sys1\n\nsys2" {
			t.Errorf("system messages not merged correctly")
		}
		if result[1].Role != messages.MessageRoleUser || result[1].Content != "user1\n\nuser2" {
			t.Errorf("user messages not merged correctly")
		}
		if result[2].Role != messages.MessageRoleAssistant || result[2].Content != "assistant1\n\nassistant2" {
			t.Errorf("assistant messages not merged correctly")
		}
		if result[3].Role != messages.MessageRoleUser || result[3].Content != "user3" {
			t.Errorf("final user message incorrect")
		}
	})
}
