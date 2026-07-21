package agent

import (
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"

	"github.com/tmc/langchaingo/llms"
)

func TestFreshNewContextManagerSeedsSystemPromptOnlyForFreshSession(t *testing.T) {
	sessionFolder := t.TempDir()

	manager, err := InitializeContextManager("system v1", sessionFolder, nil)
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	rawMessages := manager.CloneMessageList()
	messages := contextmanager.ConvertMessageList(rawMessages)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if text := messageText(messages); text != "system v1\n" {
		t.Fatalf("system prompt = %q, want original system v1", text)
	}
	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "first request"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	reloaded, err := InitializeContextManager("system v2", sessionFolder, nil)
	if err != nil {
		t.Fatalf("reload freshNewContextManager() error = %v", err)
	}
	reloadedMessages := contextmanager.ConvertMessageList(reloaded.CloneMessageList())
	if len(reloadedMessages) != 2 {
		t.Fatalf("messages = %d, want 2", len(reloadedMessages))
	}
	if strings.Contains(messageText(reloadedMessages), "system v2") {
		t.Fatalf("reloaded context should not re-seed a second system prompt:\n%s", messageText(reloadedMessages))
	}
	if text := messageText(reloadedMessages[1:2]); text != "first request\n" {
		t.Fatalf("reloaded user message = %q", text)
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

	messages := contextmanager.ConvertMessageList(manager.CloneMessageList())
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

func TestVisualFollowupMarksScreenshotObservationSource(t *testing.T) {
	manager, err := InitializeContextManager("system", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("InitializeContextManager() error = %v", err)
	}
	msg := visualFollowupMessageFromLLMContent(manager, llms.MessageContent{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart("This image is the screenshot observation returned by the screenshot tool."),
			llms.BinaryPart("image/jpeg", []byte("jpeg-bytes")),
		},
	})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %#v", msg.Attachments)
	}
	if msg.Attachments[0].Source != contextmanager.AttachmentSourceScreenshotObservation {
		t.Fatalf("Source = %q", msg.Attachments[0].Source)
	}
}

func TestUserMessageAttachmentsRemainUnmarked(t *testing.T) {
	manager, err := InitializeContextManager("system", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("InitializeContextManager() error = %v", err)
	}
	msg := userMessageFromInput(manager, "hello", []InputAttachment{{
		Kind:     AttachmentKindImage,
		MIMEType: "image/png",
		Data:     []byte("png"),
	}})
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %#v", msg.Attachments)
	}
	if msg.Attachments[0].Source != "" {
		t.Fatalf("user upload Source = %q, want empty", msg.Attachments[0].Source)
	}
}
