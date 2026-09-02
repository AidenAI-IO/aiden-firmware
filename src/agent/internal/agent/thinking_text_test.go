package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestNormalizeTaggedThinkingTextSupportsOrphanClosingTag(t *testing.T) {
	visible, reasoning, found := normalizeTaggedThinkingText("internal plan\n</think>\n<tts>1 加 1 等于 2。</tts>\n1 加 1 等于 2。")
	if !found {
		t.Fatal("normalizeTaggedThinkingText found = false, want true")
	}
	if reasoning != "internal plan" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "internal plan")
	}
	if visible != "<tts>1 加 1 等于 2。</tts>\n1 加 1 等于 2。" {
		t.Fatalf("visible = %q", visible)
	}
}

func TestFunctionAgentParseOutputRemovesTaggedThinkingFromFinalAnswer(t *testing.T) {
	agent := &FunctionAgent{OutputKey: agentLoopOutputKey}
	_, finish, err := agent.ParseOutput(&llms.ContentResponse{Choices: []*llms.ContentChoice{{
		Content: "internal plan\n</think>\nfinal answer",
	}}})
	if err != nil {
		t.Fatalf("ParseOutput failed: %v", err)
	}
	if finish == nil {
		t.Fatal("finish = nil, want final answer")
	}
	if got := finish.ReturnValues[agentLoopOutputKey]; got != "final answer" {
		t.Fatalf("final answer = %#v", got)
	}
}

func TestOpenAICompatibleReasoningContentInContentStreamsVisibleText(t *testing.T) {
	streamEvents := []string{
		`data: {"id":"1","choices":[{"delta":{"content":"internal plan"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"\n</think>\n<tts>answer"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":".</tts>"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":" visible"}}]}`,
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
	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client(), withOpenAICompatibleReasoningEffort("medium"))
	var chunks []string
	var reasoningChunks []string
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "test")}, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
		chunks = append(chunks, string(chunk))
		return nil
	}), llms.WithStreamingReasoningFunc(func(_ context.Context, chunk, _ []byte) error {
		reasoningChunks = append(reasoningChunks, string(chunk))
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if got := resp.Choices[0].Content; got != "<tts>answer.</tts> visible" {
		t.Fatalf("content = %q", got)
	}
	if got := resp.Choices[0].ReasoningContent; got != "internal plan" {
		t.Fatalf("reasoning content = %q", got)
	}
	if strings.Join(chunks, "") != "<tts>answer.</tts> visible" {
		t.Fatalf("streamed content = %q", strings.Join(chunks, ""))
	}
	if len(chunks) < 2 {
		t.Fatalf("streamed chunks = %#v, want incremental visible output", chunks)
	}
	if got := strings.TrimSpace(strings.Join(reasoningChunks, "")); got != "internal plan" {
		t.Fatalf("streamed reasoning = %q, want internal plan", got)
	}
}

func TestOpenAICompatibleDedicatedReasoningStillStreamsContent(t *testing.T) {
	streamEvents := []string{
		`data: {"id":"1","choices":[{"delta":{"reasoning_content":"private"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":"first"}}]}`,
		`data: {"id":"1","choices":[{"delta":{"content":" second"}}]}`,
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
	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client(), withOpenAICompatibleReasoningEffort("medium"))
	var chunks []string
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "test")}, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		chunks = append(chunks, string(chunk))
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if got := strings.Join(chunks, ""); got != "first second" {
		t.Fatalf("streamed content = %q", got)
	}
	if len(chunks) != 2 {
		t.Fatalf("streamed chunks = %#v, want 2", chunks)
	}
	if got := resp.Choices[0].ReasoningContent; got != "private" {
		t.Fatalf("reasoning content = %q", got)
	}
}

func TestOpenAICompatibleReasoningCallbackWithoutContentStream(t *testing.T) {
	streamEvents := []string{
		`data: {"id":"1","choices":[{"delta":{"content":"<think>private</think>visible"}}]}`,
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
	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client(), withOpenAICompatibleReasoningEffort("medium"))
	var reasoningChunks []string
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "test")}, llms.WithStreamingReasoningFunc(func(_ context.Context, chunk, _ []byte) error {
		reasoningChunks = append(reasoningChunks, string(chunk))
		return nil
	}))
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if got := resp.Choices[0].Content; got != "visible" {
		t.Fatalf("content = %q, want visible", got)
	}
	if got := strings.Join(reasoningChunks, ""); got != "private" {
		t.Fatalf("streamed reasoning = %q, want private", got)
	}
}
