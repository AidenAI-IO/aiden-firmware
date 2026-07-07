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
