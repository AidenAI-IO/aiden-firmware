package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestAnthropicStreamReportsMaxTokensTruncationWithoutRetrying(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, event := range []string{
			`data: {"type":"message_start","message":{"id":"msg_stream_trunc","usage":{"input_tokens":10}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"deep"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":1000}}`,
			`data: {"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte(event + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	m := newAnthropicModel(server.URL, "claude-opus-5", "tok", server.Client(),
		withAnthropicReasoningEffort("high"))
	_, err := m.GenerateContent(context.Background(),
		[]llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "hi")},
		llms.WithMaxTokens(64_000),
		llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want a max_tokens truncation error")
	}
	if !isAnthropicOutputBudgetError(err) {
		t.Fatalf("error = %v, want an anthropicOutputBudgetError", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1: the truncation is terminal, not a transient protocol fault", requests)
	}
}
