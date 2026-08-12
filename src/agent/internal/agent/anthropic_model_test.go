package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
		Stream bool `json:"stream"`
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
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_stream","name":"tap","input":{}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"12}"}}`,
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

func TestAnthropicProviderUsesEnvironmentFallbacks(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://relay.example.test")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "environment-token")
	t.Setenv("ANTHROPIC_API_KEY", "")

	manager := NewModelManager(ModelConfig{Provider: "anthropic", Model: "claude-test"}, ProxyConfig{})
	built, err := manager.build(manager.config)
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
	built, err := manager.build(manager.config)
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
