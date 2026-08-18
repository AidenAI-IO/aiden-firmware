package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiden-agent/internal/agent/messages"

	"github.com/tmc/langchaingo/llms"
)

func TestResponsesModelUsesLocalContextAndStoreFalse(t *testing.T) {
	var captured struct {
		Model             string               `json:"model"`
		Input             []responsesInputItem `json:"input"`
		Tools             []responsesTool      `json:"tools"`
		ParallelToolCalls bool                 `json:"parallel_tool_calls"`
		Store             bool                 `json:"store"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},{"type":"function_call","call_id":"call_1","name":"tap","arguments":"{\"x\":10}"}],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16,"input_tokens_details":{"cached_tokens":3}}}`))
	}))
	defer server.Close()

	model := newResponsesModel(server.URL+"/v1", "test-model", "token", server.Client(), responsesModelOptions{})
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		{Role: llms.ChatMessageTypeSystem, Parts: []llms.ContentPart{llms.TextPart("system")}},
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}},
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.TextPart("thinking"), llms.ToolCall{ID: "old_call", Type: "function", FunctionCall: &llms.FunctionCall{Name: "screenshot", Arguments: "{}"}}}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{llms.ToolCallResponse{ToolCallID: "old_call", Name: "screenshot", Content: "image ready"}}},
	}, llms.WithTools([]llms.Tool{{Type: "function", Function: &llms.FunctionDefinition{Name: "tap", Parameters: map[string]any{"type": "object"}}}}))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if captured.Model != "test-model" || captured.ParallelToolCalls {
		t.Fatalf("request metadata = %#v", captured)
	}
	if len(captured.Input) != 5 {
		t.Fatalf("input item count = %d, want 5: %#v", len(captured.Input), captured.Input)
	}
	if captured.Input[3].Type != "function_call" || captured.Input[4].Type != "function_call_output" {
		t.Fatalf("tool items = %#v", captured.Input[3:5])
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Name != "tap" || captured.Tools[0].Type != "function" {
		t.Fatalf("tools = %#v", captured.Tools)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Content != "done" || len(resp.Choices[0].ToolCalls) != 1 {
		t.Fatalf("response = %#v", resp)
	}
	if got := resp.Choices[0].ToolCalls[0].FunctionCall.Arguments; got != `{"x":10}` {
		t.Fatalf("tool arguments = %q", got)
	}
	if got := resp.Choices[0].GenerationInfo["cached_tokens"]; got != 3 {
		t.Fatalf("cached tokens = %#v", got)
	}
}

func TestResponsesModelSendsStoreFalse(t *testing.T) {
	var raw map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[]}`))
	}))
	defer server.Close()
	model := newResponsesModel(server.URL, "test-model", "", server.Client(), responsesModelOptions{}).(*responsesModel)
	if _, err := model.GenerateContent(context.Background(), []llms.MessageContent{{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}}}); err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	store, ok := raw["store"].(bool)
	if !ok || store {
		t.Fatalf("store = %#v, want explicit false", raw["store"])
	}
}

func TestResponsesModelStreamsTextAndFunctionArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !request.Stream {
			t.Errorf("stream = false, want true")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n",
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n",
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"tap\"}}\n",
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"x\\\":\"}\n",
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"10}\"}\n",
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"}}\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n",
			"\n",
		}, "")))
	}))
	defer server.Close()
	model := newResponsesModel(server.URL, "test-model", "", server.Client(), responsesModelOptions{})
	var streamed string
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}}}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		streamed += string(chunk)
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if streamed != "hello" || resp.Choices[0].Content != "hello" {
		t.Fatalf("streamed=%q response=%q", streamed, resp.Choices[0].Content)
	}
	if len(resp.Choices[0].ToolCalls) != 1 || resp.Choices[0].ToolCalls[0].FunctionCall.Name != "tap" {
		t.Fatalf("tool calls = %#v", resp.Choices[0].ToolCalls)
	}
	if got := resp.Choices[0].ToolCalls[0].FunctionCall.Arguments; got != `{"x":10}` {
		t.Fatalf("arguments = %q", got)
	}
	reasoningItems, ok := resp.Choices[0].GenerationInfo["responses_reasoning_items"].([]json.RawMessage)
	if !ok || len(reasoningItems) != 1 || !strings.Contains(string(reasoningItems[0]), `"encrypted_content":"opaque"`) {
		t.Fatalf("streaming reasoning items = %#v", resp.Choices[0].GenerationInfo["responses_reasoning_items"])
	}
}

func TestModelAPIModeValidation(t *testing.T) {
	if got := normalizeModelAPIMode(""); got != modelAPIModeChatCompletions {
		t.Fatalf("empty mode = %q", got)
	}
	if got := normalizeModelAPIMode("responses"); got != modelAPIModeResponses {
		t.Fatalf("responses mode = %q", got)
	}
	cfg := Config{
		ModelProviders: map[string]ModelProvider{"gateway": {Type: "openai", BaseURL: "https://gateway.example.test/v1"}},
		Model:          ModelConfig{Provider: "gateway", Model: "test-model", APIMode: "responses"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected a named OpenAI-compatible endpoint: %v", err)
	}
	if err := (Config{Model: ModelConfig{Provider: "anthropic", Model: "claude-test", APIMode: "responses"}}).Validate(); err == nil || !strings.Contains(err.Error(), "OpenAI-compatible /responses endpoint") {
		t.Fatalf("Validate() error = %v, want native transport compatibility error", err)
	}
}

func TestConvertResponsesInputPreservesMixedContentOrderAndAudioShape(t *testing.T) {
	input, err := convertResponsesInput([]llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart("before"),
			llms.ImageURLContent{URL: "https://example.test/image.png", Detail: "low"},
			llms.TextPart("between"),
			llms.BinaryPart("audio/wav", []byte("audio")),
			llms.TextPart("after"),
		},
	}})
	if err != nil {
		t.Fatalf("convertResponsesInput: %v", err)
	}
	if len(input) != 1 {
		t.Fatalf("input = %#v, want one message", input)
	}
	parts, ok := input[0].Content.([]responsesInputContent)
	if !ok {
		t.Fatalf("content = %#v, want rich content parts", input[0].Content)
	}
	if len(parts) != 5 {
		t.Fatalf("content parts = %#v, want five", parts)
	}
	if parts[0].Type != "input_text" || parts[0].Text != "before" ||
		parts[1].Type != "input_image" || parts[1].ImageURL != "https://example.test/image.png" || parts[1].Detail != "low" ||
		parts[2].Type != "input_text" || parts[2].Text != "between" ||
		parts[3].Type != "input_audio" || parts[3].InputAudio == nil || parts[3].InputAudio.Data == "" || parts[3].InputAudio.Format != "wav" ||
		parts[4].Type != "input_text" || parts[4].Text != "after" {
		t.Fatalf("content parts = %#v", parts)
	}
}

func TestConvertResponsesInputDoesNotAliasFlushedRichContent(t *testing.T) {
	input, err := convertResponsesInput([]llms.MessageContent{{
		Role: llms.ChatMessageTypeAI,
		Parts: []llms.ContentPart{
			llms.TextPart("before"),
			llms.ImageURLContent{URL: "https://example.test/first.png"},
			llms.ToolCall{ID: "call_1", Type: "function", FunctionCall: &llms.FunctionCall{Name: "tap", Arguments: "{}"}},
			llms.ImageURLContent{URL: "https://example.test/second.png"},
		},
	}})
	if err != nil {
		t.Fatalf("convertResponsesInput: %v", err)
	}
	if len(input) != 3 {
		t.Fatalf("input = %#v, want rich content, tool call, rich content", input)
	}
	first, ok := input[0].Content.([]responsesInputContent)
	if !ok || len(first) != 2 || first[0].Text != "before" || first[1].ImageURL != "https://example.test/first.png" {
		t.Fatalf("first rich item = %#v", input[0])
	}
	if input[1].Type != "function_call" || input[1].CallID != "call_1" {
		t.Fatalf("tool item = %#v", input[1])
	}
	last, ok := input[2].Content.([]responsesInputContent)
	if !ok || len(last) != 1 || last[0].ImageURL != "https://example.test/second.png" {
		t.Fatalf("second rich item = %#v", input[2])
	}
}

func TestResponsesModelStreamsWhenOnlyReasoningCallbackIsSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !request.Stream {
			t.Errorf("stream = false, want true")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"think\"}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	var reasoning string
	model := newResponsesModel(server.URL, "test-model", "", server.Client(), responsesModelOptions{})
	if _, err := model.GenerateContent(context.Background(), []llms.MessageContent{{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}}}, llms.WithStreamingReasoningFunc(func(_ context.Context, chunk []byte, _ []byte) error {
		reasoning += string(chunk)
		return nil
	})); err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if reasoning != "think" {
		t.Fatalf("reasoning = %q, want think", reasoning)
	}
}

func TestResponsesModelStreamsLargeReasoningItem(t *testing.T) {
	const scannerLimit = 1024 * 1024
	largeEncryptedContent := strings.Repeat("x", scannerLimit+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_large","type":"reasoning","encrypted_content":"` + largeEncryptedContent + `"}}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	model := newResponsesModel(server.URL, "test-model", "", server.Client(), responsesModelOptions{})
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}}}, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	reasoningItems, ok := resp.Choices[0].GenerationInfo["responses_reasoning_items"].([]json.RawMessage)
	if !ok || len(reasoningItems) != 1 || !strings.Contains(string(reasoningItems[0]), largeEncryptedContent) {
		t.Fatalf("streaming reasoning items = %#v", resp.Choices[0].GenerationInfo["responses_reasoning_items"])
	}
}

func TestResponsesModelReplaysOpaqueReasoningFromLocalContext(t *testing.T) {
	requestCount := 0
	var secondInput []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if requestCount == 1 {
			secondInput = request.Input
		}
		requestCount++
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer server.Close()

	model := newResponsesModel(server.URL, "test-model", "", server.Client(), responsesModelOptions{}).(*responsesModel)
	response, err := model.GenerateContent(context.Background(), []llms.MessageContent{{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}}})
	if err != nil {
		t.Fatalf("first GenerateContent: %v", err)
	}
	contextMessage := messages.ConvertChoiceToContextManagerMessage(*response.Choices[0])
	if len(contextMessage.ResponsesReasoningItems) != 1 {
		t.Fatalf("stored reasoning items = %#v", contextMessage.ResponsesReasoningItems)
	}
	if _, err := model.GenerateContentFromMessageList(context.Background(), []messages.Message{contextMessage}); err != nil {
		t.Fatalf("second GenerateContent: %v", err)
	}
	if len(secondInput) == 0 || !strings.Contains(string(secondInput[0]), `"type":"reasoning"`) || !strings.Contains(string(secondInput[0]), `"encrypted_content":"opaque"`) {
		t.Fatalf("second input = %s, want opaque reasoning item", secondInput)
	}
}
