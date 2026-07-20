package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestReasoningContentStreaming(t *testing.T) {
	// Simulate kimi-k3 streaming: reasoning_content deltas followed by content deltas
	streamEvents := []string{
		`data: {"id":"1","choices":[{"delta":{"reasoning_content":"Let me think"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"reasoning_content":" about this"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"reasoning_content":"..."}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"The answer"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":" is 42"}}]}`,
		`data: {"id":"1","choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"reasoning_tokens":30}}`,
		`data: [DONE]`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range streamEvents {
			w.Write([]byte(event + "\n"))
		}
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())

	var streamedContent strings.Builder
	resp, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "test message"),
		},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			streamedContent.Write(chunk)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}

	// Verify reasoning content is captured
	if resp.Choices[0].ReasoningContent != "Let me think about this..." {
		t.Errorf("Expected reasoning_content='Let me think about this...', got %q", resp.Choices[0].ReasoningContent)
	}

	// Verify final content is correct
	if resp.Choices[0].Content != "The answer is 42" {
		t.Errorf("Expected content='The answer is 42', got %q", resp.Choices[0].Content)
	}

	// Verify only content (not reasoning) was streamed to TTS
	if streamedContent.String() != "The answer is 42" {
		t.Errorf("Expected streamed content='The answer is 42', got %q", streamedContent.String())
	}

	// Verify reasoning tokens are captured in generation info
	reasoningTokens, ok := resp.Choices[0].GenerationInfo["reasoning_tokens"]
	if !ok {
		t.Fatal("Expected reasoning_tokens in generation info")
	}
	if reasoningTokens != 30 {
		t.Errorf("Expected reasoning_tokens=30, got %v", reasoningTokens)
	}

	// Verify time to first reasoning metric
	if _, ok := resp.Choices[0].GenerationInfo["llm_time_to_first_reasoning_ms"]; !ok {
		t.Error("Expected llm_time_to_first_reasoning_ms metric")
	}

	// Verify reasoning chunks count
	reasoningChunks, ok := resp.Choices[0].GenerationInfo["llm_stream_reasoning_chunks"]
	if !ok || reasoningChunks != 3 {
		t.Errorf("Expected llm_stream_reasoning_chunks=3, got %v", reasoningChunks)
	}
}
