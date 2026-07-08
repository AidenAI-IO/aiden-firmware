package agent

import (
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestFreshNewContextManagerSeedsSystemPromptOnlyForFreshSession(t *testing.T) {
	sessionFolder := t.TempDir()

	manager, err := freshNewContextManager("system v1", "first request", nil, sessionFolder)
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if text := messageText(messages[:1]); text != "system v1\n" {
		t.Fatalf("system prompt = %q, want original system v1", text)
	}
	if text := messageText(messages[1:2]); text != "first request\n" {
		t.Fatalf("first request = %q", text)
	}

	reloaded, err := freshNewContextManager("system v2", "second request", nil, sessionFolder)
	if err != nil {
		t.Fatalf("reload freshNewContextManager() error = %v", err)
	}
	reloadedMessages := reloaded.ConvertToStandardMessageList()
	if len(reloadedMessages) != 3 {
		t.Fatalf("messages = %d, want 3", len(reloadedMessages))
	}
	if strings.Contains(messageText(reloadedMessages), "system v2") {
		t.Fatalf("reloaded context should not re-seed a second system prompt:\n%s", messageText(reloadedMessages))
	}
	if text := messageText(reloadedMessages[2:3]); text != "second request\n" {
		t.Fatalf("second request = %q", text)
	}
}

func TestUserMessageFromInputPreservesAttachments(t *testing.T) {
	manager, err := freshNewContextManager("system", "hello", []InputAttachment{{
		Kind:     "image",
		Name:     "screen.png",
		MIMEType: "image/png",
		Data:     []byte("data"),
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	userMessage := messages[1]
	if userMessage.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("role = %q, want human", userMessage.Role)
	}
	if len(userMessage.Parts) != 2 {
		t.Fatalf("parts = %#v, want text + binary attachment", userMessage.Parts)
	}
}
