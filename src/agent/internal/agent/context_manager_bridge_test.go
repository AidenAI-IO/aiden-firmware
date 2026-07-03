package agent

import (
	"strings"
	"testing"

	"aiden-agent/internal/agent/context_manager"

	"github.com/tmc/langchaingo/llms"
)

func TestPreparePlannerContextManagerReusesExistingContext(t *testing.T) {
	manager := context_manager.NewContextManager()
	preparePlannerContextManager(
		manager,
		"system v1",
		[]llms.MessageContent{
			{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("prior user")}},
			{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.TextPart("prior assistant")}},
		},
		"first request",
		nil,
	)
	preparePlannerContextManager(
		manager,
		"system v2",
		[]llms.MessageContent{
			{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("duplicate prior user")}},
		},
		"second request",
		nil,
	)

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 5 {
		t.Fatalf("messages = %d, want 5 without duplicated history", len(messages))
	}
	if text := messageText(messages[:1]); text != "system v1\n" {
		t.Fatalf("system prompt = %q, want original system v1", text)
	}
	if text := messageText(messages[1:2]); text != "prior user\n" {
		t.Fatalf("history user = %q", text)
	}
	if text := messageText(messages[2:3]); text != "prior assistant\n" {
		t.Fatalf("history assistant = %q", text)
	}
	if text := messageText(messages[3:4]); text != "first request\n" {
		t.Fatalf("first request = %q", text)
	}
	if text := messageText(messages[4:5]); text != "second request\n" {
		t.Fatalf("second request = %q", text)
	}
	if strings.Contains(messageText(messages), "duplicate prior user") {
		t.Fatalf("reused context should not re-seed history:\n%s", messageText(messages))
	}
}
