package contextmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func newTestContextManager(t *testing.T) *ContextManager {
	t.Helper()

	sessionFolder := t.TempDir()
	sessionID := newSessionID()
	if err := saveCurrentSession(sessionFolder, sessionID); err != nil {
		t.Fatalf("saveCurrentSession() error = %v", err)
	}

	manager, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	return manager
}

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
	manager := newTestContextManager(t)
	manager.AppendMessage(Message{
		Role:    MessageRoleToolCall,
		Content: "发送测试文本。",
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "echo",
			Arguments: `{"input":"hello"}`,
		}},
	})

	messages := ConvertMessageList(manager.CloneMessageList())
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
	manager := newTestContextManager(t)
	manager.AppendMessage(Message{
		Role: MessageRoleToolCall,
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "touch_gesture",
			Arguments: rawArguments,
		}},
	})

	messages := ConvertMessageList(manager.CloneMessageList())
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
	manager := newTestContextManager(t)
	manager.AppendMessage(Message{
		Role: MessageRoleToolCall,
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "echo",
			Arguments: `{}`,
		}},
	})
	manager.AppendMessage(Message{
		Role: MessageRoleToolResult,
		ToolResults: []ToolResult{{
			ToolCallID: "call_1",
			Name:       "echo",
			Content:    `{"output":"hello"}`,
		}},
	})

	messages := ConvertMessageList(manager.CloneMessageList())
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[1].Role != llms.ChatMessageTypeTool {
		t.Fatalf("role = %v, want Tool", messages[1].Role)
	}
	if len(messages[1].Parts) != 1 {
		t.Fatalf("parts = %#v, want one tool result", messages[1].Parts)
	}
	toolResponse, ok := messages[1].Parts[0].(llms.ToolCallResponse)
	if !ok || toolResponse.ToolCallID != "call_1" || toolResponse.Name != "echo" || toolResponse.Content != `{"output":"hello"}` {
		t.Fatalf("tool result part = %#v", messages[1].Parts[0])
	}
}

func TestNewContextManagerFromMessageListPersistsMessages(t *testing.T) {
	sessionFolder := t.TempDir()
	want := []Message{
		{Role: MessageRoleSystem, Content: "system prompt"},
		{
			Role:    MessageRoleToolCall,
			Content: "checking",
			ToolCalls: []ToolCall{{
				ID:        "call_1",
				Name:      "echo",
				Arguments: `{"input":"hello"}`,
			}},
		},
		{
			Role: MessageRoleToolResult,
			ToolResults: []ToolResult{{
				ToolCallID: "call_1",
				Name:       "echo",
				Content:    `{"output":"hello"}`,
			}},
		},
	}

	manager, err := NewContextManagerFromMessageList(sessionFolder, want)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}

	reloaded, err := LoadContextManagerFromSessionID(sessionFolder, manager.GetSessionID())
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}

	if got := reloaded.CloneMessageList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded messages = %#v, want %#v", got, want)
	}
}

func TestAppendMessageRepairsPersistedOrphanToolCallBeforeNextUser(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionID := newSessionID()
	if err := saveCurrentSession(sessionFolder, sessionID); err != nil {
		t.Fatalf("saveCurrentSession() error = %v", err)
	}
	manager, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	if err := manager.AppendMessage(Message{
		Role:    MessageRoleToolCall,
		Content: "Checking the device.",
		ToolCalls: []ToolCall{{
			ID:        "call_orphan",
			Name:      "slow",
			Arguments: `{}`,
		}},
	}); err != nil {
		t.Fatalf("AppendMessage(tool call) error = %v", err)
	}

	reloaded, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("reload context manager error = %v", err)
	}
	if err := reloaded.AppendMessage(Message{Role: MessageRoleUser, Content: "continue"}); err != nil {
		t.Fatalf("AppendMessage(user) error = %v", err)
	}

	dump := reloaded.MessageListDump()
	if len(dump.Messages) != 3 {
		t.Fatalf("messages = %#v, want tool call, interrupted result, user", dump.Messages)
	}
	if dump.Messages[1].Role != MessageRoleToolResult || len(dump.Messages[1].ToolResults) != 1 {
		t.Fatalf("repair message = %#v, want one tool result", dump.Messages[1])
	}
	if result := dump.Messages[1].ToolResults[0]; result.ToolCallID != "call_orphan" || result.Content != interruptedToolResultContent {
		t.Fatalf("repair result = %#v", result)
	}
	if dump.Messages[2].Role != MessageRoleUser {
		t.Fatalf("last message = %#v, want user", dump.Messages[2])
	}

	messages := ConvertMessageList(reloaded.CloneMessageList())
	if len(messages) != 3 || messages[0].Role != llms.ChatMessageTypeAI || messages[1].Role != llms.ChatMessageTypeTool || messages[2].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("provider messages = %#v, want assistant/tool/user", messages)
	}
}

func TestAppendMessageRepairsToolCallBeforeHookExpandedUserBatch(t *testing.T) {
	manager := newTestContextManager(t)
	if err := manager.AppendMessage(Message{
		Role: MessageRoleToolCall,
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "first",
			Arguments: `{}`,
		}},
	}); err != nil {
		t.Fatalf("AppendMessage(tool call) error = %v", err)
	}
	manager.AddAppendMessageHooks([]AppendMessageHook{func(message Message) AppendMessageHookResult {
		if message.Role != MessageRoleUser {
			return AppendMessageHookResult{Message: &message}
		}
		return AppendMessageHookResult{
			Before:  []Message{{Role: MessageRoleState, Content: "device state"}},
			Message: &message,
		}
	}})
	if err := manager.AppendMessage(Message{Role: MessageRoleUser, Content: "continue"}); err != nil {
		t.Fatalf("AppendMessage(user) error = %v", err)
	}

	dump := manager.MessageListDump()
	if len(dump.Messages) != 4 {
		t.Fatalf("messages = %#v, want tool call, result, state, user", dump.Messages)
	}
	if got := dump.Messages[1].ToolResults[0]; got.ToolCallID != "call_1" || got.Content != interruptedToolResultContent {
		t.Fatalf("repair result = %#v", got)
	}
	if dump.Messages[2].Role != MessageRoleState || dump.Messages[3].Role != MessageRoleUser {
		t.Fatalf("tail messages = %#v, want state then user", dump.Messages[2:])
	}
}

func TestAppendMessagePairsLegacyMissingToolCallIDByName(t *testing.T) {
	manager := newTestContextManager(t)
	if err := manager.AppendMessage(Message{
		Role: MessageRoleToolCall,
		ToolCalls: []ToolCall{{
			Name:      "echo",
			Arguments: `{}`,
		}},
	}); err != nil {
		t.Fatalf("AppendMessage(tool call) error = %v", err)
	}
	if err := manager.AppendMessage(Message{
		Role: MessageRoleToolResult,
		ToolResults: []ToolResult{{
			ToolCallID: "call_legacy",
			Name:       "echo",
			Content:    "ok",
		}},
	}); err != nil {
		t.Fatalf("AppendMessage(tool result) error = %v", err)
	}

	messages := ConvertMessageList(manager.CloneMessageList())
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want paired call and result", messages)
	}
	call, ok := messages[0].Parts[0].(llms.ToolCall)
	if !ok || call.ID != "ctx_tool_call_0_0" {
		t.Fatalf("tool call = %#v, want stable fallback ID", messages[0])
	}
	response, ok := messages[1].Parts[0].(llms.ToolCallResponse)
	if !ok || response.ToolCallID != call.ID {
		t.Fatalf("tool response = %#v, want ID %q", messages[1], call.ID)
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

func TestContextManagerAppendMessageHookModifiesMessage(t *testing.T) {
	manager := newTestContextManager(t)
	manager.AddAppendMessageHooks([]AppendMessageHook{func(message Message) AppendMessageHookResult {
		message.Content = strings.ToUpper(message.Content)
		return AppendMessageHookResult{Message: &message}
	}})

	manager.AppendMessage(Message{Role: MessageRoleUser, Content: "hello"})

	dump := manager.MessageListDump()
	if len(dump.Messages) != 1 {
		t.Fatalf("messages = %#v, want one entry", dump.Messages)
	}
	if dump.Messages[0].Content != "HELLO" {
		t.Fatalf("content = %q, want %q", dump.Messages[0].Content, "HELLO")
	}
}

func TestContextManagerAppendMessageHookInjectsBeforeAndAfter(t *testing.T) {
	manager := newTestContextManager(t)
	manager.AddAppendMessageHooks([]AppendMessageHook{func(message Message) AppendMessageHookResult {
		modified := message
		modified.Content = "core:" + message.Content
		return AppendMessageHookResult{
			Before:  []Message{{Role: MessageRoleNotice, Content: "before"}},
			Message: &modified,
			After:   []Message{{Role: MessageRoleNotice, Content: "after"}},
		}
	}})

	manager.AppendMessage(Message{Role: MessageRoleUser, Content: "hello"})

	dump := manager.MessageListDump()
	if got, want := len(dump.Messages), 3; got != want {
		t.Fatalf("message count = %d, want %d", got, want)
	}
	if dump.Messages[0].Content != "before" || dump.Messages[1].Content != "core:hello" || dump.Messages[2].Content != "after" {
		t.Fatalf("messages = %#v", dump.Messages)
	}
}

func TestContextManagerAppendMessageHookCanDropOriginalMessage(t *testing.T) {
	manager := newTestContextManager(t)
	manager.AddAppendMessageHooks([]AppendMessageHook{func(message Message) AppendMessageHookResult {
		return AppendMessageHookResult{
			Before: []Message{{Role: MessageRoleSystem, Content: "replacement"}},
		}
	}})

	manager.AppendMessage(Message{Role: MessageRoleUser, Content: "hello"})

	dump := manager.MessageListDump()
	if len(dump.Messages) != 1 {
		t.Fatalf("messages = %#v, want one replacement entry", dump.Messages)
	}
	if dump.Messages[0].Role != MessageRoleSystem || dump.Messages[0].Content != "replacement" {
		t.Fatalf("message = %#v", dump.Messages[0])
	}
}

func TestContextManagerConcurrentAppendWithStatefulHook(t *testing.T) {
	manager := newTestContextManager(t)
	var hookMu sync.Mutex
	var hookCalls int
	manager.AddAppendMessageHook(func(message Message) AppendMessageHookResult {
		hookMu.Lock()
		hookCalls++
		hookMu.Unlock()
		return AppendMessageHookResult{Message: &message}
	})

	const appends = 32
	var wg sync.WaitGroup
	for range appends {
		wg.Go(func() {
			if err := manager.AppendMessage(Message{Role: MessageRoleUser, Content: "hello"}); err != nil {
				t.Errorf("AppendMessage() error = %v", err)
			}
		})
	}
	wg.Wait()

	hookMu.Lock()
	gotHookCalls := hookCalls
	hookMu.Unlock()
	if gotHookCalls != appends {
		t.Fatalf("hook calls = %d, want %d", gotHookCalls, appends)
	}
	if got := len(manager.MessageListDump().Messages); got != appends {
		t.Fatalf("message count = %d, want %d", got, appends)
	}
}

func standardMessageText(message llms.MessageContent) string {
	var parts []string
	for _, part := range message.Parts {
		if text, ok := part.(llms.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestStoreAttachmentPersistsMetadataOnly(t *testing.T) {
	manager := newTestContextManager(t)
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

	messages := ConvertMessageList(manager.CloneMessageList())
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("messages = %#v, want text + binary parts", messages)
	}
	binaryPart, ok := messages[0].Parts[1].(llms.BinaryContent)
	if !ok || string(binaryPart.Data) != "png-bytes" {
		t.Fatalf("binary part = %#v", messages[0].Parts[1])
	}
}

func TestScreenshotAttachmentIDIsExposedAndReadable(t *testing.T) {
	manager := newTestContextManager(t)
	stored, err := manager.StoreAttachment("image/jpeg", []byte("jpeg-bytes"))
	if err != nil {
		t.Fatalf("StoreAttachment() error = %v", err)
	}
	stored.Source = AttachmentSourceScreenshotObservation
	if err := manager.AppendMessage(Message{
		Role:        MessageRoleUser,
		Content:     "screenshot",
		Attachments: []Attachment{stored},
	}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	attachmentID := filepath.Base(stored.FilePath)
	messages := ConvertMessageList(manager.CloneMessageList())
	if len(messages) != 1 || len(messages[0].Parts) != 3 {
		t.Fatalf("messages = %#v, want text + attachment ID + binary", messages)
	}
	label, ok := messages[0].Parts[1].(llms.TextContent)
	if !ok || label.Text != "[screenshot_attachment_id="+attachmentID+"]" {
		t.Fatalf("attachment label = %#v", messages[0].Parts[1])
	}
	data, err := manager.ReadScreenshotAttachment(attachmentID)
	if err != nil {
		t.Fatalf("ReadScreenshotAttachment() error = %v", err)
	}
	if string(data) != "jpeg-bytes" {
		t.Fatalf("attachment data = %q", data)
	}
}

func TestReadScreenshotAttachmentRejectsUnregisteredReferences(t *testing.T) {
	manager := newTestContextManager(t)
	stored, err := manager.StoreAttachment("image/jpeg", []byte("user-image"))
	if err != nil {
		t.Fatalf("StoreAttachment() error = %v", err)
	}
	if err := manager.AppendMessage(Message{Role: MessageRoleUser, Attachments: []Attachment{stored}}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	for _, attachmentID := range []string{filepath.Base(stored.FilePath), "../outside.jpg", "missing.jpg"} {
		if _, err := manager.ReadScreenshotAttachment(attachmentID); err == nil {
			t.Fatalf("ReadScreenshotAttachment(%q) succeeded", attachmentID)
		}
	}
}

func TestLoadContextManagerFromSessionIDReloadsMessages(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionID := newSessionID()
	if err := saveCurrentSession(sessionFolder, sessionID); err != nil {
		t.Fatalf("saveCurrentSession() error = %v", err)
	}
	manager, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}

	manager.AppendMessage(Message{Role: MessageRoleSystem, Content: "system"})
	manager.AppendMessage(Message{Role: MessageRoleUser, Content: "hello"})

	reloaded, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}

	dump := reloaded.MessageListDump()
	if got, want := dump.SessionID, sessionID; got != want {
		t.Fatalf("session id = %q, want %q", got, want)
	}
	if got, want := len(dump.Messages), 2; got != want {
		t.Fatalf("message count = %d, want %d", got, want)
	}
	if dump.Messages[0].Content != "system" || dump.Messages[1].Content != "hello" {
		t.Fatalf("unexpected reloaded messages = %#v", dump.Messages)
	}

	sessionFile := filepath.Join(sessionFolder, sessionID+".jsonl")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("session file missing: %v", err)
	}
}

func TestConvertToStandardMessageListLoadsAttachmentFromFilePath(t *testing.T) {
	manager := newTestContextManager(t)
	filePath := filepath.Join(t.TempDir(), "manual.png")
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

	messages := ConvertMessageList(manager.CloneMessageList())
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("messages = %#v, want text + binary parts", messages)
	}
	binaryPart, ok := messages[0].Parts[1].(llms.BinaryContent)
	if !ok || string(binaryPart.Data) != "manual-bytes" {
		t.Fatalf("binary part = %#v", messages[0].Parts[1])
	}
}

func TestConvertMessageListUsesProvidedSlice(t *testing.T) {
	manager := newTestContextManager(t)
	stored, err := manager.StoreAttachment("image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("StoreAttachment() error = %v", err)
	}
	stored.Source = AttachmentSourceScreenshotObservation
	if err := manager.AppendMessage(Message{
		Role:        MessageRoleUser,
		Content:     "caption",
		Attachments: []Attachment{stored},
	}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	cloned := manager.CloneMessageList()
	if len(cloned) != 1 || len(cloned[0].Attachments) != 1 {
		t.Fatalf("clone = %#v", cloned)
	}
	if cloned[0].Attachments[0].Source != AttachmentSourceScreenshotObservation {
		t.Fatalf("Source = %q", cloned[0].Attachments[0].Source)
	}

	// Ephemeral prune: drop attachment, keep text — ConvertMessageList must not read the file.
	cloned[0].Attachments = nil
	cloned[0].Content = "caption\n[Image omitted]"
	converted := ConvertMessageList(cloned)
	if len(converted) != 1 {
		t.Fatalf("converted len = %d", len(converted))
	}
	for _, part := range converted[0].Parts {
		if _, ok := part.(llms.BinaryContent); ok {
			t.Fatalf("expected no binary parts after ephemeral prune, got %#v", converted[0].Parts)
		}
	}
	dump := manager.MessageListDump()
	if len(dump.Messages) != 1 || len(dump.Messages[0].Attachments) != 1 {
		t.Fatalf("persisted dump mutated: %#v", dump)
	}
}

func TestConvertToStandardMessageListReportsMissingAttachment(t *testing.T) {
	manager := newTestContextManager(t)
	manager.AppendMessage(Message{
		Role:    MessageRoleUser,
		Content: "see image",
		Attachments: []Attachment{{
			MIMEType: "image/png",
			FileSize: 12,
			FilePath: filepath.Join(t.TempDir(), "missing.png"),
		}},
	})

	messages := ConvertMessageList(manager.CloneMessageList())
	if len(messages) != 1 || len(messages[0].Parts) != 2 {
		t.Fatalf("messages = %#v, want text + omitted attachment notice", messages)
	}
	textPart, ok := messages[0].Parts[1].(llms.TextContent)
	if !ok || !strings.Contains(textPart.Text, "Attachment omitted") {
		t.Fatalf("missing attachment part = %#v", messages[0].Parts[1])
	}
}
