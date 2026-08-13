package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

func TestIsAnthropicModel(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"anthropic", "claude-sonnet-4", true},
		{"Anthropic", "anything", true},
		{"openrouter", "anthropic/claude-sonnet-4", true},
		{"openrouter", "openai/gpt-4o", false},
		{"openai", "gpt-4o", false},
		{"", "", false},
		{"  anthropic  ", "", true},
		{"openrouter", "  Anthropic/Claude  ", true},
	}
	for _, tc := range cases {
		got := IsAnthropicModel(tc.provider, tc.model)
		if got != tc.want {
			t.Fatalf("IsAnthropicModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestAnthropicModelAggregatesContentBlocksAndConvertsHistory(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		Model     string `json:"model"`
		System    any    `json:"system"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
		ToolChoice map[string]any `json:"tool_choice"`
		Stream     bool           `json:"stream"`
	}

	var captured capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("request path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-token" {
			t.Errorf("x-api-key = %q, want test-token", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicAPIVersion {
			t.Errorf("anthropic-version = %q, want %q", got, anthropicAPIVersion)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"stop_reason":"tool_use",
			"content":[
				{"type":"thinking","thinking":"inspect screen","signature":"sig"},
				{"type":"text","text":"I will inspect it."},
				{"type":"tool_use","id":"call_1","name":"screenshot","input":{}},
				{"type":"tool_use","id":"call_2","name":"tap","input":{"x":10,"y":20}}
			],
			"usage":{"input_tokens":120,"output_tokens":18,"cache_read_input_tokens":80}
		}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client())
	manager := NewModelManager(ModelConfig{MaxResponseTokens: 128}, ProxyConfig{})
	callOptions := chains.GetLLMCallOptions(manager.CallOptions()...)
	callOptions = append(callOptions, llms.WithTools([]llms.Tool{{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:       "tap",
			Parameters: map[string]any{"type": "object"},
		},
	}}))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system one", "system two"),
		{
			Role: llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{
				llms.TextPart("Checking."),
				llms.ToolCall{ID: "old_1", Type: "function", FunctionCall: &llms.FunctionCall{Name: "screenshot", Arguments: `{}`}},
				llms.ToolCall{ID: "old_2", Type: "function", FunctionCall: &llms.FunctionCall{Name: "tap", Arguments: `{"x":1,"y":2}`}},
			},
		},
		{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{
				llms.ToolCallResponse{ToolCallID: "old_1", Name: "screenshot", Content: "image ready"},
				llms.ToolCallResponse{ToolCallID: "old_2", Name: "tap", Content: "tap complete"},
			},
		},
		{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart("Continue"),
				llms.BinaryPart("image/png", []byte("png")),
			},
		},
	}, callOptions...)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0] == nil {
		t.Fatalf("choices = %#v, want one aggregate choice", response.Choices)
	}
	choice := response.Choices[0]
	if choice.Content != "I will inspect it." {
		t.Errorf("content = %q", choice.Content)
	}
	if choice.ReasoningContent != "inspect screen" {
		t.Errorf("reasoning content = %q", choice.ReasoningContent)
	}
	if len(choice.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want 2", choice.ToolCalls)
	}
	if got := choice.ToolCalls[1].FunctionCall.Arguments; got != `{"x":10,"y":20}` {
		t.Errorf("second tool arguments = %q", got)
	}
	if got := choice.GenerationInfo["prompt_tokens"]; got != 200 {
		t.Errorf("prompt_tokens = %#v", got)
	}
	if got := choice.GenerationInfo["cached_tokens"]; got != 80 {
		t.Errorf("cached_tokens = %#v", got)
	}

	if captured.Model != "claude-test" || captured.Stream {
		t.Errorf("captured request = %#v", captured)
	}
	if captured.MaxTokens != 128 {
		t.Errorf("max_tokens = %d, want 128", captured.MaxTokens)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("messages = %#v, want alternating assistant and merged user content", captured.Messages)
	}
	assistant := captured.Messages[0]
	if assistant.Role != "assistant" || len(assistant.Content) != 3 {
		t.Fatalf("assistant history = %#v", assistant)
	}
	user := captured.Messages[1]
	if user.Role != "user" || len(user.Content) != 4 || user.Content[3]["type"] != "image" {
		t.Fatalf("user content = %#v", user)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Name != "tap" {
		t.Fatalf("tools = %#v", captured.Tools)
	}
	if captured.ToolChoice["type"] != "auto" || captured.ToolChoice["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %#v, want serial auto tool choice", captured.ToolChoice)
	}
}

func TestAnthropicModelDisablesParallelUseForExplicitToolChoice(t *testing.T) {
	t.Parallel()

	var captured struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"call","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{}}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client())
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "echo"}}}), llms.WithToolChoice(llms.ToolChoice{
		Type:     "function",
		Function: &llms.FunctionReference{Name: "echo"},
	}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if captured.ToolChoice["type"] != "tool" || captured.ToolChoice["name"] != "echo" || captured.ToolChoice["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %#v", captured.ToolChoice)
	}
}

func TestAnthropicModelRecoversThinkingOnlyEndTurnWhenThinkingWasDisabled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"msg_misclassified",
			"content":[{"type":"thinking","thinking":"<tts>Answer.</tts>\n<final_answer>(d)</final_answer>"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":8}
		}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client())
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	choice := response.Choices[0]
	if choice.Content != "<tts>Answer.</tts>\n<final_answer>(d)</final_answer>" {
		t.Fatalf("content = %q", choice.Content)
	}
	if choice.ReasoningContent != "" {
		t.Fatalf("reasoning content = %q, want recovered content to be reclassified", choice.ReasoningContent)
	}
	if got := choice.GenerationInfo["llm_anthropic_response_recovery"]; got != "thinking_as_text" {
		t.Fatalf("recovery info = %#v", got)
	}
}

func TestAnthropicModelDoesNotExposeThinkingOnlyEndTurnWhenThinkingWasEnabled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"msg_thinking",
			"content":[{"type":"thinking","thinking":"private reasoning","signature":"sig"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":8}
		}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(),
		withAnthropicReasoningEffort("low"),
		withAnthropicProtocolRetry(0, 0),
	)
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	})
	if err == nil || !strings.Contains(err.Error(), "end_turn response has no text or tool_use content") {
		t.Fatalf("GenerateContent() error = %v, want semantic protocol error", err)
	}
}

func TestAnthropicModelRetriesToolUseWithoutToolBlock(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			_, _ = w.Write([]byte(`{
				"id":"msg_empty_tool",
				"content":[],
				"stop_reason":"tool_use",
				"usage":{"input_tokens":10,"output_tokens":1}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"msg_tool",
			"content":[{"type":"tool_use","id":"call_1","name":"echo","input":{"value":"ok"}}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(1, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "echo"}}}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want semantic retry", attempts)
	}
	if len(response.Choices[0].ToolCalls) != 1 || response.Choices[0].ToolCalls[0].FunctionCall.Name != "echo" {
		t.Fatalf("tool calls = %#v", response.Choices[0].ToolCalls)
	}
}

func TestAnthropicModelRecoversTextMisclassifiedAsToolUse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"msg_text_tool_reason",
			"content":[{"type":"text","text":"done"}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(0, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	choice := response.Choices[0]
	if choice.Content != "done" || choice.StopReason != "end_turn" {
		t.Fatalf("choice = %#v, want recovered end_turn text", choice)
	}
	if got := choice.GenerationInfo["llm_anthropic_response_recovery"]; got != "tool_use_as_end_turn" {
		t.Fatalf("recovery = %#v", got)
	}
}

func TestAnthropicModelRetriesTextMisclassifiedAsToolUseWhenToolsAreAvailable(t *testing.T) {
	t.Parallel()

	attempts := 0
	toolChoiceTypes := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var request struct {
			ToolChoice map[string]any `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		toolChoiceTypes = append(toolChoiceTypes, fmt.Sprint(request.ToolChoice["type"]))
		if attempts == 1 {
			_, _ = w.Write([]byte(`{
				"id":"msg_text_tool_reason",
				"content":[{"type":"text","text":"I will take a screenshot now."}],
				"stop_reason":"tool_use",
				"usage":{"input_tokens":10,"output_tokens":8}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"msg_tool",
			"content":[{"type":"tool_use","id":"call_1","name":"screenshot","input":{}}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(1, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "inspect the screen"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "screenshot"}}}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 2 || len(response.Choices[0].ToolCalls) != 1 {
		t.Fatalf("attempts = %d, tool calls = %#v", attempts, response.Choices[0].ToolCalls)
	}
	if !slices.Equal(toolChoiceTypes, []string{"auto", "any"}) {
		t.Fatalf("tool choice types = %#v, want auto then any", toolChoiceTypes)
	}
}

func TestAnthropicModelDefaultRetriesRepeatedEmptyToolUseResponses(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 4 {
			_, _ = w.Write([]byte(`{"id":"msg_empty_tool","content":[],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_tool","content":[{"type":"tool_use","id":"call_1","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client())
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "echo"}}}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want four attempts", attempts)
	}
	if len(response.Choices[0].ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.Choices[0].ToolCalls)
	}
}

func TestAnthropicModelFallsBackToScreenshotAfterRepeatedEmptyToolUseResponses(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = w.Write([]byte(`{"id":"msg_empty_tool","content":[],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":1}}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(2, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "inspect the screen"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "screenshot"}}}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want three attempts", attempts)
	}
	choice := response.Choices[0]
	if len(choice.ToolCalls) != 1 || choice.ToolCalls[0].FunctionCall.Name != "screenshot" {
		t.Fatalf("tool calls = %#v, want synthetic screenshot", choice.ToolCalls)
	}
	if got := choice.GenerationInfo["llm_anthropic_response_recovery"]; got != "synthetic_screenshot_tool_use" {
		t.Fatalf("recovery = %#v", got)
	}
}

func TestAnthropicModelDoesNotSynthesizeUnavailableScreenshot(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg_empty_tool","content":[],"stop_reason":"tool_use","usage":{}}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(0, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "use the echo tool"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "echo"}}}))
	if err == nil || !strings.Contains(err.Error(), "tool_use stop_reason has no valid tool_use content") {
		t.Fatalf("GenerateContent() error = %v, want protocol error", err)
	}
}

func TestAnthropicModelStreamsRecoveredThinkingOnlyContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_misclassified","usage":{"input_tokens":10}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"<tts>Answer.</tts>"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	var streamed strings.Builder
	var streamedReasoning strings.Builder
	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client())
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		streamed.Write(chunk)
		return nil
	}), llms.WithStreamingReasoningFunc(func(_ context.Context, chunk []byte, _ []byte) error {
		streamedReasoning.Write(chunk)
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if streamed.String() != "<tts>Answer.</tts>" {
		t.Fatalf("streamed = %q", streamed.String())
	}
	if streamedReasoning.Len() != 0 {
		t.Fatalf("streamed reasoning = %q, want misclassified content hidden until recovery", streamedReasoning.String())
	}
	if response.Choices[0].Content != streamed.String() || response.Choices[0].ReasoningContent != "" {
		t.Fatalf("choice = %#v", response.Choices[0])
	}
}

func TestAnthropicModelDoesNotRecoverSignedThinkingStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_signed","usage":{"input_tokens":10}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private reasoning"}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	var streamed strings.Builder
	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(0, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		streamed.Write(chunk)
		return nil
	}))
	if err == nil || !strings.Contains(err.Error(), "end_turn response has no text or tool_use content") {
		t.Fatalf("GenerateContent() error = %v, want signed thinking protocol error", err)
	}
	if streamed.Len() != 0 {
		t.Fatalf("streamed = %q, want no signed thinking disclosure", streamed.String())
	}
}

func TestAnthropicModelRetriesEmptyStreamedEndTurn(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"msg_empty","usage":{"input_tokens":10}}}`,
				``,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
				``,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n")))
			return
		}
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_ok","usage":{"input_tokens":10}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicProtocolRetry(1, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 2 || response.Choices[0].Content != "ok" {
		t.Fatalf("attempts = %d, choice = %#v", attempts, response.Choices[0])
	}
}

func TestAnthropicModelStreamsTextAndToolArguments(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_stream","role":"assistant","model":"claude-test","usage":{"input_tokens":25,"cache_creation_input_tokens":10}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_stream","name":"tap","input":{}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"12}"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":1}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	var streamed strings.Builder
	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client())
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		streamed.Write(chunk)
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if streamed.String() != "Hello world" {
		t.Errorf("streamed = %q", streamed.String())
	}
	choice := response.Choices[0]
	if choice.Content != "Hello world" || len(choice.ToolCalls) != 1 {
		t.Fatalf("choice = %#v", choice)
	}
	if got := choice.ToolCalls[0].FunctionCall.Arguments; got != `{"x":12}` {
		t.Errorf("arguments = %q", got)
	}
	if choice.GenerationInfo["prompt_tokens"] != 35 || choice.GenerationInfo["completion_tokens"] != 9 {
		t.Errorf("generation info = %#v", choice.GenerationInfo)
	}
	if _, ok := choice.GenerationInfo["llm_time_to_first_content_ms"]; !ok {
		t.Errorf("generation info missing time to first content: %#v", choice.GenerationInfo)
	}
}

func TestAnthropicModelRetriesEarlyStreamingOverload(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_retry","usage":{}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(1, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := response.Choices[0].Content; got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
}

func TestAnthropicModelClassifiesStreamingOverloadAfterOutputWithoutRetry(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_overload","usage":{}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			``,
			`event: error`,
			`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(1, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	var providerErr *ProviderHTTPError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want ProviderHTTPError", err, err)
	}
	if providerErr.StatusCode != 529 || providerErr.ProviderCode != "overloaded_error" {
		t.Fatalf("provider error = %#v", providerErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no retry after streamed output", attempts)
	}
}

func TestAnthropicModelRetriesIncompleteStreamBeforeOutput(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"msg_incomplete","usage":{}}}`,
				``,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_incomplete","name":"tap","input":{}}}`,
				``,
				`data: {"type":"content_block_stop","index":0}`,
				``,
			}, "\n")))
			return
		}
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_complete","usage":{}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(1, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want retry before output", attempts)
	}
	if got := response.Choices[0].Content; got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
}

func TestAnthropicModelRejectsIncompleteStreamAfterOutputWithoutRetry(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_incomplete","usage":{}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	var streamed strings.Builder
	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(1, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		streamed.Write(chunk)
		return nil
	}))
	if err == nil || !strings.Contains(err.Error(), "ended before message_stop") {
		t.Fatalf("GenerateContent() error = %v, want missing message_stop protocol error", err)
	}
	if streamed.String() != "partial" {
		t.Fatalf("streamed = %q, want partial", streamed.String())
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no retry after output", attempts)
	}
}

func TestAnthropicModelRejectsMessageStopWithOpenContentBlock(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_open_block","usage":{}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_open","name":"tap","input":{}}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(0, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err == nil || !strings.Contains(err.Error(), "message_stop received with open content block 0") {
		t.Fatalf("GenerateContent() error = %v, want open block protocol error", err)
	}
}

func TestAnthropicModelRejectsMessageStopBeforeMessageDelta(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_missing_delta","usage":{}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(0, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err == nil || !strings.Contains(err.Error(), "message_stop received before message_delta") {
		t.Fatalf("GenerateContent() error = %v, want missing message_delta protocol error", err)
	}
}

func TestAnthropicModelRejectsContentBlockAfterMessageDelta(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_late_block","usage":{}}}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"late"}}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(0, 0))
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err == nil || !strings.Contains(err.Error(), "content_block_start received after message_delta") {
		t.Fatalf("GenerateContent() error = %v, want late block protocol error", err)
	}
}

func TestAnthropicModelAcceptsMultipleMessageDeltas(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_multiple_delta","usage":{"input_tokens":3}}}`,
			``,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
			``,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			``,
			`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":2}}`,
			``,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicStreamRetry(0, 0))
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	choice := response.Choices[0]
	if choice.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", choice.StopReason)
	}
	if got := choice.GenerationInfo["completion_tokens"]; got != 2 {
		t.Fatalf("completion tokens = %#v, want cumulative value 2", got)
	}
}

func TestAnthropicModelLogsResponseOnEarlyStreamFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_orphan","usage":{}}}`,
			``,
			`data: {"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"orphan"}}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	logDir := t.TempDir()
	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(),
		withAnthropicRawHTTPLogger(newTestLLMRawHTTPLogger(logDir, "test-session")),
		withAnthropicStreamRetry(0, time.Second),
	)
	_, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err == nil || !strings.Contains(err.Error(), "missing block 7") {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)
	counts := countRawHTTPKinds(t, logText)
	if counts["request"] != 1 || counts["response"] != 1 {
		t.Fatalf("raw HTTP log should contain one request/response pair:\n%s", logText)
	}
	if !strings.Contains(logText, "missing block 7") || !strings.Contains(logText, "content_block_delta") {
		t.Fatalf("response log missing failure detail or raw SSE:\n%s", logText)
	}
}

func TestAnthropicProviderUsesEnvironmentFallbacks(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://relay.example.test")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "environment-token")
	t.Setenv("ANTHROPIC_API_KEY", "")

	manager := NewModelManager(ModelConfig{Provider: "anthropic", Model: "claude-test"}, ProxyConfig{})
	built, err := manager.build()
	if err != nil {
		t.Fatalf("build() error = %v", err)
	}
	model, ok := built.(*anthropicModel)
	if !ok {
		t.Fatalf("built model type = %T", built)
	}
	if model.baseURL != "https://relay.example.test/v1" {
		t.Errorf("base URL = %q", model.baseURL)
	}
	if model.token != "environment-token" {
		t.Errorf("token did not resolve from ANTHROPIC_AUTH_TOKEN")
	}
	if !model.useBearerAuth {
		t.Error("ANTHROPIC_AUTH_TOKEN should use bearer authentication")
	}
}

func TestNormalizeAnthropicBaseURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://api.anthropic.com":        "https://api.anthropic.com/v1",
		"https://api.anthropic.com/":       "https://api.anthropic.com/v1",
		"https://api.anthropic.com/v1":     "https://api.anthropic.com/v1",
		"https://gateway.example/proxy/v1": "https://gateway.example/proxy/v1",
	}
	for input, want := range cases {
		if got := normalizeAnthropicBaseURL(input); got != want {
			t.Errorf("normalizeAnthropicBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAnthropicProviderAuthTokenReferenceUsesBearer(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "environment-token")
	manager := NewModelManager(ModelConfig{
		Provider: "anthropic",
		Model:    "claude-test",
		APIKey:   "$ANTHROPIC_AUTH_TOKEN",
	}, ProxyConfig{})
	built, err := manager.build()
	if err != nil {
		t.Fatalf("build() error = %v", err)
	}
	model := built.(*anthropicModel)
	if !model.useBearerAuth {
		t.Error("$ANTHROPIC_AUTH_TOKEN should use bearer authentication")
	}
}

func TestAnthropicModelMapsReasoningEffortToAdaptiveThinking(t *testing.T) {
	t.Parallel()

	var captured struct {
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		OutputConfig *struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicReasoningEffort("medium"))
	if _, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}); err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if captured.Thinking == nil || captured.Thinking.Type != "adaptive" {
		t.Fatalf("thinking = %#v", captured.Thinking)
	}
	if captured.OutputConfig == nil || captured.OutputConfig.Effort != "medium" {
		t.Fatalf("output config = %#v", captured.OutputConfig)
	}
}

func TestAnthropicModelOmitsAdaptiveThinkingForToolRequests(t *testing.T) {
	t.Parallel()

	var captured struct {
		Thinking     any `json:"thinking"`
		OutputConfig any `json:"output_config"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"call","name":"echo","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	model := newAnthropicModel(server.URL, "claude-test", "test-token", server.Client(), withAnthropicReasoningEffort("high"))
	if _, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "hello"),
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "echo"}}})); err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if captured.Thinking != nil || captured.OutputConfig != nil {
		t.Fatalf("tool request sent adaptive thinking: %#v", captured)
	}
}

func TestAnthropicModelLiveRelay(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	if baseURL == "" || token == "" {
		t.Skip("Anthropic relay environment is not configured")
	}

	modelName := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL"))
	if modelName == "" {
		modelName = "claude-sonnet-4-6"
	}
	client := &http.Client{Timeout: 30 * time.Second}
	model := newAnthropicModel(baseURL, modelName, token, client,
		withAnthropicBearerAuth(), withAnthropicReasoningEffort("low"))
	var streamed strings.Builder
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "Reply tersely."),
		llms.TextParts(llms.ChatMessageTypeHuman, "Use the echo tool with value ok."),
	}, llms.WithMaxTokens(128), llms.WithTools([]llms.Tool{{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "echo",
			Description: "Echo a short value.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []string{"value"},
			},
		},
	}}), llms.WithToolChoice(llms.ToolChoice{
		Type:     "function",
		Function: &llms.FunctionReference{Name: "echo"},
	}), llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		streamed.Write(chunk)
		return nil
	}))
	if err != nil {
		t.Fatalf("live relay GenerateContent() error = %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0] == nil {
		t.Fatalf("live choices = %#v", response.Choices)
	}
	choice := response.Choices[0]
	if len(choice.ToolCalls) != 1 || choice.ToolCalls[0].FunctionCall == nil || choice.ToolCalls[0].FunctionCall.Name != "echo" {
		t.Fatalf("live tool calls = %#v", choice.ToolCalls)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(choice.ToolCalls[0].FunctionCall.Arguments), &arguments); err != nil {
		t.Fatalf("live tool arguments = %q: %v", choice.ToolCalls[0].FunctionCall.Arguments, err)
	}
	if arguments["value"] != "ok" {
		t.Fatalf("live tool arguments = %#v", arguments)
	}
	if _, ok := choice.GenerationInfo["prompt_tokens"]; !ok {
		t.Fatalf("live generation info = %#v", choice.GenerationInfo)
	}
	t.Logf("live model=%s streamed_chars=%d stop_reason=%s", modelName, streamed.Len(), choice.StopReason)
}
