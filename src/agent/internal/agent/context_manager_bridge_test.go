package agent

import (
	"testing"

	"aiden-agent/internal/agent/contextmanager"

	"github.com/tmc/langchaingo/llms"
)

func TestInitializeContextManagerStartsNewSessionWhenSystemPromptChanges(t *testing.T) {
	sessionFolder := t.TempDir()

	manager, err := InitializeContextManager("system v1", sessionFolder, nil)
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
	originalSessionID := manager.GetSessionID()

	reloaded, err := InitializeContextManager("system v2", sessionFolder, nil)
	if err != nil {
		t.Fatalf("reload freshNewContextManager() error = %v", err)
	}
	reloadedMessages := reloaded.ConvertToStandardMessageList()
	if reloaded.GetSessionID() == originalSessionID {
		t.Fatal("system prompt change reused the existing session")
	}
	if len(reloadedMessages) != 1 {
		t.Fatalf("messages = %d, want only the new system prompt", len(reloadedMessages))
	}
	if text := messageText(reloadedMessages); text != "system v2\n" {
		t.Fatalf("new session system prompt = %q, want system v2", text)
	}

	original, err := contextmanager.LoadContextManagerFromSessionID(sessionFolder, originalSessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	if text := messageText(original.ConvertToStandardMessageList()); text != "system v1\nfirst request\n" {
		t.Fatalf("original session was modified: %q", text)
	}
}

func TestInitializeContextManagerReusesSessionWhenSystemPromptMatches(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := InitializeContextManager("system", sessionFolder, nil)
	if err != nil {
		t.Fatalf("InitializeContextManager() error = %v", err)
	}
	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "hello"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	reloaded, err := InitializeContextManager("system", sessionFolder, nil)
	if err != nil {
		t.Fatalf("InitializeContextManager() reload error = %v", err)
	}
	if reloaded.GetSessionID() != manager.GetSessionID() {
		t.Fatal("unchanged system prompt started a new session")
	}
	if text := messageText(reloaded.ConvertToStandardMessageList()); text != "system\nhello\n" {
		t.Fatalf("reloaded session = %q", text)
	}
}

func TestUserMessageFromInputPreservesAttachments(t *testing.T) {
	manager, err := InitializeContextManager("system", t.TempDir(), nil)
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
