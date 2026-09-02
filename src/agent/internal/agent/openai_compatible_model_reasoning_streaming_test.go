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
	var streamedReasoning strings.Builder
	resp, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "test message"),
		},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			streamedContent.Write(chunk)
			return nil
		}),
		llms.WithStreamingReasoningFunc(func(_ context.Context, reasoning, _ []byte) error {
			streamedReasoning.Write(reasoning)
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
	if streamedReasoning.String() != "Let me think about this..." {
		t.Errorf("Expected streamed reasoning='Let me think about this...', got %q", streamedReasoning.String())
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

func TestOpenAICompatibleAutoModeFiltersTaggedThinkingIncrementally(t *testing.T) {
	streamEvents := []string{
		`data: {"id":"1","choices":[{"delta":{"content":"ordinary "}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"text"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"<thi"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"nk>private"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":" reasoning</thin"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"king>visible"}}]}`,
		`data: {"id":"1","choices":[{"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range streamEvents {
			_, _ = w.Write([]byte(event + "\n"))
		}
	}))
	defer server.Close()

	// Empty effort means auto mode. Tagged thinking must still be removed from
	// the visible stream, while ordinary untagged content remains incremental.
	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())
	var visible []string
	var reasoning []string
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "test"),
	}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		visible = append(visible, string(chunk))
		return nil
	}), llms.WithStreamingReasoningFunc(func(_ context.Context, chunk, _ []byte) error {
		reasoning = append(reasoning, string(chunk))
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if got := resp.Choices[0].Content; got != "ordinary textvisible" {
		t.Fatalf("content = %q, want ordinary textvisible", got)
	}
	if got := resp.Choices[0].ReasoningContent; got != "private reasoning" {
		t.Fatalf("reasoning content = %q, want private reasoning", got)
	}
	if got := strings.Join(visible, ""); got != "ordinary textvisible" {
		t.Fatalf("visible stream = %#v (%q), want ordinary textvisible", visible, got)
	}
	if len(visible) < 2 {
		t.Fatalf("visible stream = %#v, want incremental ordinary output", visible)
	}
	if got := strings.Join(reasoning, ""); got != "private reasoning" {
		t.Fatalf("reasoning stream = %q, want private reasoning", got)
	}
}

func TestOpenAICompatibleAutoModeHandlesSplitOrphanClosingTag(t *testing.T) {
	streamEvents := []string{
		`data: {"id":"1","choices":[{"delta":{"content":"private plan</thi"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"nk>final answer"}}]}`,
		`data: {"id":"1","choices":[{"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range streamEvents {
			_, _ = w.Write([]byte(event + "\n"))
		}
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())
	var reasoning strings.Builder
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, "test"),
	}, llms.WithStreamingReasoningFunc(func(_ context.Context, chunk, _ []byte) error {
		reasoning.Write(chunk)
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if got := resp.Choices[0].Content; got != "final answer" {
		t.Fatalf("content = %q, want final answer", got)
	}
	if got := resp.Choices[0].ReasoningContent; got != "private plan" {
		t.Fatalf("reasoning content = %q, want private plan", got)
	}
	if got := strings.TrimSpace(reasoning.String()); got != "private plan" {
		t.Fatalf("reasoning stream = %q, want private plan", got)
	}
}
