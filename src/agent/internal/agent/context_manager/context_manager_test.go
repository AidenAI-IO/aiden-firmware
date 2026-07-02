package context_manager

import (
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
