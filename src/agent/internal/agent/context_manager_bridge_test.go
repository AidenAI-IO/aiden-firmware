package agent

import (
	"testing"

	"aiden-agent/internal/agent/contextmanager"

	"github.com/tmc/langchaingo/llms"
)

func TestFreshNewContextManagerSeedsSystemPromptOnlyForFreshSession(t *testing.T) {
	sessionFolder := t.TempDir()

	manager, err := InitializeContextManager(contextmanager.SystemPrompt{StablePrefix: "system v1"}, sessionFolder, nil)
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if text := messageText(messages); text != "system v1\n" {
		t.Fatalf("system prompt = %q, want original system v1", text)
	}
	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "first request"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	reloaded, err := InitializeContextManager(contextmanager.SystemPrompt{StablePrefix: "system v2"}, sessionFolder, nil)
	if err != nil {
		t.Fatalf("reload freshNewContextManager() error = %v", err)
	}
	reloadedMessages := reloaded.ConvertToStandardMessageList()
	if len(reloadedMessages) != 2 {
		t.Fatalf("messages = %d, want 2", len(reloadedMessages))
	}
	if text := messageText(reloadedMessages[:1]); text != "system v2\n" {
		t.Fatalf("reloaded context should use the current system prompt, got %q", text)
	}
	if text := messageText(reloadedMessages[1:2]); text != "first request\n" {
		t.Fatalf("reloaded user message = %q", text)
	}
}

func TestInitializeContextManagerPreservesStableAndDynamicSystemParts(t *testing.T) {
	manager, err := InitializeContextManager(contextmanager.SystemPrompt{
		StablePrefix: "stable cache prefix",
		DynamicTail:  "dynamic locale tail",
	}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("InitializeContextManager() error = %v", err)
	}

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("system message parts = %#v, want stable + dynamic", messages)
	}
	stable, stableOK := messages[0].Parts[0].(llms.TextContent)
	dynamic, dynamicOK := messages[0].Parts[1].(llms.TextContent)
	if !stableOK || stable.Text != "stable cache prefix" || !dynamicOK || dynamic.Text != "dynamic locale tail" {
		t.Fatalf("system parts = %#v, want stable then dynamic", messages[0].Parts)
	}
}

func TestUserMessageFromInputPreservesAttachments(t *testing.T) {
	manager, err := InitializeContextManager(contextmanager.SystemPrompt{StablePrefix: "system"}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	if err := manager.AppendMessage(userMessageFromInput(manager, "hello", []InputAttachment{{
		Kind:     "image",
		Name:     "screen.png",
		MIMEType: "image/png",
		Data:     []byte("data"),
	}})); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
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
