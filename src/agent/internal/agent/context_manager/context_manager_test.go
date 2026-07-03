package context_manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestConvertStandardMessageToContextManagerMessage_Assistant(t *testing.T) {
	message := ConvertChoiceToContextManagerMessage(llms.ContentChoice{
		Content: "hello",
	})
	if message.Role != MessageRoleAssistant {
		t.Fatalf("role = %q, want %q", message.Role, MessageRoleAssistant)
	}
	if message.Content != "hello" {
		t.Fatalf("content = %q, want %q", message.Content, "hello")
	}
}

func TestConvertStandardMessageToContextManagerMessage_ToolCall(t *testing.T) {
	message := ConvertChoiceToContextManagerMessage(llms.ContentChoice{
		Content: "发送测试文本。",
		ToolCalls: []llms.ToolCall{{
			ID:   "call_1",
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      "echo",
				Arguments: `{"input":"hello"}`,
			},
		}},
	})
	if message.Role != MessageRoleToolCall {
		t.Fatalf("role = %q, want %q", message.Role, MessageRoleToolCall)
	}
	if message.Content != "发送测试文本。" {
		t.Fatalf("content = %q, want assistant preamble only", message.Content)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %#v, want one entry", message.ToolCalls)
	}
	if message.ToolCalls[0].ID != "call_1" || message.ToolCalls[0].Name != "echo" || message.ToolCalls[0].Arguments != `{"input":"hello"}` {
		t.Fatalf("tool_calls = %#v", message.ToolCalls)
	}
}

func TestConvertStandardMessageToContextManagerMessage_NormalizesInvalidToolCallArguments(t *testing.T) {
	rawArguments := `{"type": "tap", "point": {"x":}`
	message := ConvertChoiceToContextManagerMessage(llms.ContentChoice{
		ToolCalls: []llms.ToolCall{{
			ID:   "call_1",
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      "touch_gesture",
				Arguments: rawArguments,
			},
		}},
	})

	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %#v, want one entry", message.ToolCalls)
	}
	want := mustMarshalToolInput(t, rawArguments)
	if message.ToolCalls[0].Arguments != want {
		t.Fatalf("arguments = %q, want valid JSON wrapper", message.ToolCalls[0].Arguments)
	}
}

func TestConvertStandardMessageToContextManagerMessage_ToolCallWithoutContent(t *testing.T) {
	message := ConvertChoiceToContextManagerMessage(llms.ContentChoice{
		ToolCalls: []llms.ToolCall{{
			ID:   "call_1",
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      "screenshot",
				Arguments: `{}`,
			},
		}},
	})
	if message.Role != MessageRoleToolCall {
		t.Fatalf("role = %q, want %q", message.Role, MessageRoleToolCall)
	}
	if message.Content != "" {
		t.Fatalf("content = %q, want empty", message.Content)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call_1" || message.ToolCalls[0].Name != "screenshot" {
		t.Fatalf("tool_calls = %#v", message.ToolCalls)
	}
}

func TestConvertStandardMessageToContextManagerMessage_FuncCallFallback(t *testing.T) {
	message := ConvertChoiceToContextManagerMessage(llms.ContentChoice{
		FuncCall: &llms.FunctionCall{
			Name:      "echo",
			Arguments: `{"value":"hello"}`,
		},
	})
	if message.Role != MessageRoleToolCall {
		t.Fatalf("role = %q, want %q", message.Role, MessageRoleToolCall)
	}
	if message.Content != "" {
		t.Fatalf("content = %q, want empty", message.Content)
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Name != "echo" {
		t.Fatalf("tool_calls = %#v", message.ToolCalls)
	}
}

func TestConvertToStandardMessageList_ToolCalls(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role:    MessageRoleToolCall,
		Content: "发送测试文本。",
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "echo",
			Arguments: `{"input":"hello"}`,
		}},
	})

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != llms.ChatMessageTypeAI {
		t.Fatalf("role = %v, want AI", messages[0].Role)
	}
	if len(messages[0].Parts) != 2 {
		t.Fatalf("parts = %#v, want text + tool call", messages[0].Parts)
	}
	text, ok := messages[0].Parts[0].(llms.TextContent)
	if !ok || text.Text != "发送测试文本。" {
		t.Fatalf("text part = %#v", messages[0].Parts[0])
	}
	toolCall, ok := messages[0].Parts[1].(llms.ToolCall)
	if !ok || toolCall.ID != "call_1" || toolCall.FunctionCall == nil || toolCall.FunctionCall.Name != "echo" {
		t.Fatalf("tool call part = %#v", messages[0].Parts[1])
	}
}

func TestConvertToStandardMessageList_NormalizesInvalidToolCallArguments(t *testing.T) {
	rawArguments := `{"type": "tap", "point": {"x":}`
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role: MessageRoleToolCall,
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "touch_gesture",
			Arguments: rawArguments,
		}},
	})

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 || len(messages[0].Parts) != 1 {
		t.Fatalf("messages = %#v, want one tool call message", messages)
	}
	toolCall, ok := messages[0].Parts[0].(llms.ToolCall)
	if !ok || toolCall.FunctionCall == nil {
		t.Fatalf("tool call part = %#v", messages[0].Parts[0])
	}
	want := mustMarshalToolInput(t, rawArguments)
	if toolCall.FunctionCall.Arguments != want {
		t.Fatalf("arguments = %q, want valid JSON wrapper", toolCall.FunctionCall.Arguments)
	}
}

func mustMarshalToolInput(t *testing.T, input string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"input": input})
	if err != nil {
		t.Fatalf("marshal expected input wrapper: %v", err)
	}
	return string(encoded)
}

func TestConvertToStandardMessageList_ToolResults(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role: MessageRoleToolResult,
		ToolResults: []ToolResult{{
			ToolCallID: "call_1",
			Name:       "echo",
			Content:    `{"output":"hello"}`,
		}},
	})

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != llms.ChatMessageTypeTool {
		t.Fatalf("role = %v, want Tool", messages[0].Role)
	}
	if len(messages[0].Parts) != 1 {
		t.Fatalf("parts = %#v, want one tool result", messages[0].Parts)
	}
	toolResponse, ok := messages[0].Parts[0].(llms.ToolCallResponse)
	if !ok || toolResponse.ToolCallID != "call_1" || toolResponse.Name != "echo" || toolResponse.Content != `{"output":"hello"}` {
		t.Fatalf("tool result part = %#v", messages[0].Parts[0])
	}
}

func TestConvertStandardMessageToContextManagerMessage_ReasoningContent(t *testing.T) {
	message := ConvertChoiceToContextManagerMessage(llms.ContentChoice{
		ReasoningContent: "thinking",
		Content:          "answer",
	})
	want := "thinking\nanswer"
	if message.Content != want {
		t.Fatalf("content = %q, want %q", message.Content, want)
	}
}

func TestContextManagerReset(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{Role: MessageRoleSystem, Content: "system v1"})
	manager.AppendMessage(Message{Role: MessageRoleUser, Content: "hello"})

	manager.Reset()
	if !manager.IsEmpty() {
		t.Fatal("reset context manager should be empty")
	}
}

func TestStoreAttachmentPersistsMetadataOnly(t *testing.T) {
	manager := NewContextManager()
	stored, err := manager.StoreAttachment("image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("StoreAttachment() error = %v", err)
	}
	if stored.FilePath == "" || stored.FileSize != 9 || stored.MIMEType != "image/png" {
		t.Fatalf("stored attachment = %#v", stored)
	}

	manager.AppendMessage(Message{
		Role:        MessageRoleUser,
		Content:     "see image",
		Attachments: []Attachment{stored},
	})

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("messages = %#v, want text + binary parts", messages)
	}
	binaryPart, ok := messages[0].Parts[1].(llms.BinaryContent)
	if !ok || string(binaryPart.Data) != "png-bytes" {
		t.Fatalf("binary part = %#v", messages[0].Parts[1])
	}
}

func TestContextManagerResetRemovesStoredAttachments(t *testing.T) {
	manager := NewContextManager()
	stored, err := manager.StoreAttachment("image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("StoreAttachment() error = %v", err)
	}
	root := manager.attachmentStore.root
	if _, err := os.Stat(stored.FilePath); err != nil {
		t.Fatalf("stored attachment missing before reset: %v", err)
	}

	manager.Reset()

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("attachment root after reset: err=%v, want not exist", err)
	}
}

func TestContextManagerForkRetainsAttachmentStoreUntilAllManagersReset(t *testing.T) {
	manager := NewContextManager()
	first, err := manager.StoreAttachment("image/png", []byte("first"))
	if err != nil {
		t.Fatalf("StoreAttachment(first) error = %v", err)
	}
	manager.AppendMessage(Message{
		Role:        MessageRoleUser,
		Content:     "see first",
		Attachments: []Attachment{first},
	})
	root := manager.attachmentStore.root

	fork := manager.Fork()
	second, err := fork.StoreAttachment("image/png", []byte("second"))
	if err != nil {
		t.Fatalf("StoreAttachment(second) error = %v", err)
	}
	if first.FilePath == second.FilePath {
		t.Fatalf("fork store overwrote first attachment path %q", first.FilePath)
	}
	firstData, err := os.ReadFile(first.FilePath)
	if err != nil || string(firstData) != "first" {
		t.Fatalf("first attachment data = %q err=%v", firstData, err)
	}
	secondData, err := os.ReadFile(second.FilePath)
	if err != nil || string(secondData) != "second" {
		t.Fatalf("second attachment data = %q err=%v", secondData, err)
	}

	manager.Reset()
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("attachment root should remain while fork is alive: %v", err)
	}
	messages := fork.ConvertToStandardMessageList()
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("fork messages = %#v, want text + binary parts", messages)
	}
	binaryPart, ok := messages[0].Parts[1].(llms.BinaryContent)
	if !ok || string(binaryPart.Data) != "first" {
		t.Fatalf("fork binary part = %#v", messages[0].Parts[1])
	}

	fork.Reset()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("attachment root after fork reset: err=%v, want not exist", err)
	}
}

func TestContextManagerCloseReleasesForkAttachmentStore(t *testing.T) {
	manager := NewContextManager()
	stored, err := manager.StoreAttachment("image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("StoreAttachment() error = %v", err)
	}
	root := filepath.Dir(stored.FilePath)

	fork := manager.Fork()
	manager.Reset()
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("attachment root should remain while fork is alive: %v", err)
	}

	if err := fork.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := fork.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("attachment root after fork close: err=%v, want not exist", err)
	}
}

func TestConvertToStandardMessageListLoadsAttachmentFromFilePath(t *testing.T) {
	manager := NewContextManager()
	filePath := t.TempDir() + "/manual.png"
	if err := os.WriteFile(filePath, []byte("manual-bytes"), 0o644); err != nil {
		t.Fatalf("write attachment file: %v", err)
	}
	manager.AppendMessage(Message{
		Role:    MessageRoleUser,
		Content: "see image",
		Attachments: []Attachment{{
			MIMEType: "image/png",
			FileSize: 12,
			FilePath: filePath,
		}},
	})

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("messages = %#v, want text + binary parts", messages)
	}
	binaryPart, ok := messages[0].Parts[1].(llms.BinaryContent)
	if !ok || string(binaryPart.Data) != "manual-bytes" {
		t.Fatalf("binary part = %#v", messages[0].Parts[1])
	}
}

func TestConvertToStandardMessageListReportsMissingAttachment(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role:    MessageRoleUser,
		Content: "see image",
		Attachments: []Attachment{{
			MIMEType: "image/png",
			FileSize: 12,
			FilePath: t.TempDir() + "/missing.png",
		}},
	})

	messages := manager.ConvertToStandardMessageList()
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("messages = %#v, want text + omitted attachment notice", messages)
	}
	textPart, ok := messages[0].Parts[1].(llms.TextContent)
	if !ok || !strings.Contains(textPart.Text, "Attachment omitted") {
		t.Fatalf("missing attachment part = %#v", messages[0].Parts[1])
	}
}
