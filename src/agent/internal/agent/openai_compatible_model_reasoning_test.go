package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestReasoningPolicyNoTools(t *testing.T) {
	var capturedRequest compatibleChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		response := `{
			"choices": [{
				"message": {"content": "test response"},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50,
				"total_tokens": 150,
				"reasoning_tokens": 0
			}
		}`
		w.Write([]byte(response))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())

	// Request without tools and without explicit reasoning_effort config
	// should NOT include reasoning fields (auto mode = omit from request)
	_, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "test message"),
		},
	)
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}

	// Verify reasoning fields are omitted when not explicitly configured
	if capturedRequest.Reasoning != nil {
		t.Errorf("Expected reasoning config to be nil (auto mode), got %+v", capturedRequest.Reasoning)
	}
	if capturedRequest.ReasoningEffort != "" {
		t.Errorf("Expected reasoning_effort to be empty (auto mode), got %q", capturedRequest.ReasoningEffort)
	}
}

func TestReasoningPolicyWithTools(t *testing.T) {
	var capturedRequest compatibleChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		response := `{
			"choices": [{
				"message": {"content": "test response"},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50,
				"total_tokens": 150
			}
		}`
		w.Write([]byte(response))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())

	// Request with tools - should NOT get reasoning policy
	_, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "test message"),
		},
		llms.WithTools([]llms.Tool{
			{
				Type: "function",
				Function: &llms.FunctionDefinition{
					Name:        "test_tool",
					Description: "A test tool",
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}

	// Verify reasoning policy was NOT applied
	if capturedRequest.Reasoning != nil {
		t.Error("Expected reasoning config to be nil for request with tools")
	}
	if capturedRequest.ReasoningEffort != "" {
		t.Errorf("Expected reasoning_effort to be empty, got %q", capturedRequest.ReasoningEffort)
	}
}

// captureReasoningRequest runs one GenerateContent against a stub endpoint and
// returns the decoded request body, letting the reasoning-field tests below vary
// only the model options.
func captureReasoningRequest(t *testing.T, opts ...openAICompatibleModelOption) compatibleChatRequest {
	t.Helper()

	var capturedRequest compatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client(), opts...)
	if _, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "test message")},
	); err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	return capturedRequest
}

func TestReasoningEffortWithoutOpenRouterOmitsNestedReasoning(t *testing.T) {
	// Direct providers (Volcengine Ark, OpenAI, Moonshot) only accept the
	// standard reasoning_effort field. The nested `reasoning` object is an
	// OpenRouter extension and must not leak into their requests.
	req := captureReasoningRequest(t, withOpenAICompatibleReasoningEffort("low"))

	if req.ReasoningEffort != "low" {
		t.Errorf("reasoning_effort = %q, want \"low\"", req.ReasoningEffort)
	}
	if req.Reasoning != nil {
		t.Errorf("reasoning = %+v, want nil for non-OpenRouter providers", req.Reasoning)
	}
}

func TestReasoningEffortMinimalPassesThroughVerbatim(t *testing.T) {
	// "minimal" is Ark's no-thinking level; it must reach the provider as-is
	// rather than being normalized into one of the OpenRouter levels.
	req := captureReasoningRequest(t, withOpenAICompatibleReasoningEffort("minimal"))

	if req.ReasoningEffort != "minimal" {
		t.Errorf("reasoning_effort = %q, want \"minimal\"", req.ReasoningEffort)
	}
	if req.Reasoning != nil {
		t.Errorf("reasoning = %+v, want nil for non-OpenRouter providers", req.Reasoning)
	}
}

func TestReasoningEffortWithOpenRouterSendsNestedReasoning(t *testing.T) {
	req := captureReasoningRequest(t,
		withOpenAICompatibleReasoningEffort("low"),
		withOpenAICompatibleOpenRouterReasoning(),
	)

	if req.ReasoningEffort != "low" {
		t.Errorf("reasoning_effort = %q, want \"low\"", req.ReasoningEffort)
	}
	if req.Reasoning == nil {
		t.Fatal("reasoning = nil, want the nested object for OpenRouter")
	}
	if req.Reasoning.Effort != "low" {
		t.Errorf("reasoning.effort = %q, want \"low\"", req.Reasoning.Effort)
	}
	if req.Reasoning.Exclude {
		t.Error("reasoning.exclude = true, want false for effort \"low\"")
	}
}

func TestReasoningEffortNoneExcludesReasoningForOpenRouter(t *testing.T) {
	req := captureReasoningRequest(t,
		withOpenAICompatibleReasoningEffort("none"),
		withOpenAICompatibleOpenRouterReasoning(),
	)

	if req.Reasoning == nil {
		t.Fatal("reasoning = nil, want the nested object for OpenRouter")
	}
	if !req.Reasoning.Exclude {
		t.Error("reasoning.exclude = false, want true for effort \"none\"")
	}
}

func TestReasoningTokensParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := `{
			"choices": [{
				"message": {"content": "test response"},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50,
				"total_tokens": 150,
				"reasoning_tokens": 123
			}
		}`
		w.Write([]byte(response))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())

	resp, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "test message"),
		},
	)
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}

	// Verify reasoning_tokens is in generation info
	reasoningTokens, ok := resp.Choices[0].GenerationInfo["reasoning_tokens"]
	if !ok {
		t.Fatal("Expected reasoning_tokens in generation info")
	}
	if reasoningTokens != 123 {
		t.Errorf("Expected reasoning_tokens=123, got %v", reasoningTokens)
	}
}
