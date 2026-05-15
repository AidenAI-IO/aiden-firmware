package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestServerHandleChatReturnsToolHistory(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The current audio volume is 42.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0")

	body := bytes.NewBufferString(`{"message":"当前音量是多少？"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Response != "The current audio volume is 42." {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) != 4 {
		t.Fatalf("expected 4 history entries, got %d", len(resp.History))
	}

	if resp.History[0].Type != "user" || resp.History[0].Content != "当前音量是多少？" {
		t.Fatalf("unexpected first history message: %#v", resp.History[0])
	}
	if resp.History[1].Type != "tool_call" || resp.History[1].ToolName != "audio_volume" || resp.History[1].ToolInput != "{}" {
		t.Fatalf("unexpected tool_call message: %#v", resp.History[1])
	}
	if resp.History[2].Type != "tool_result" || resp.History[2].ToolName != "audio_volume" || resp.History[2].Content != `{"volume":42}` {
		t.Fatalf("unexpected tool_result message: %#v", resp.History[2])
	}
	if resp.History[3].Type != "assistant" || resp.History[3].Content != "The current audio volume is 42." {
		t.Fatalf("unexpected assistant message: %#v", resp.History[3])
	}
}

func TestServerHistoryEndpointIncludesToolMessages(t *testing.T) {
	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}),
			NewSkillIndex(),
		),
		history: []Message{
			{Type: "user", Content: "hello"},
			{Type: "tool_call", ToolName: "screenshot", ToolInput: "{}"},
			{Type: "tool_result", ToolName: "screenshot", Content: `{"width":100}`},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	server.handleHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var history []Message
	if err := json.NewDecoder(rec.Body).Decode(&history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(history))
	}
	if history[1].Type != "tool_call" || history[2].Type != "tool_result" {
		t.Fatalf("unexpected history payload: %#v", history)
	}
}
