package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestOpenAICompatibleModelEncodesAudioAsInputAudio(t *testing.T) {
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text,omitempty"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url,omitempty"`
				InputAudio *struct {
					Data   string `json:"data"`
					Format string `json:"format"`
				} `json:"input_audio,omitempty"`
			} `json:"content"`
		} `json:"messages"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "token", server.Client())
	audioBytes := []byte("RIFFaudio")

	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart("transcribe"),
			llms.BinaryPart("audio/wav", audioBytes),
		},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp.Choices[0].Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp.Choices[0].Content)
	}
	if len(captured.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(captured.Messages))
	}
	if len(captured.Messages[0].Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(captured.Messages[0].Content))
	}
	if captured.Messages[0].Content[1].Type != "input_audio" {
		t.Fatalf("expected input_audio part, got %#v", captured.Messages[0].Content[1])
	}
	if captured.Messages[0].Content[1].InputAudio == nil {
		t.Fatalf("expected input_audio payload, got %#v", captured.Messages[0].Content[1])
	}
	if captured.Messages[0].Content[1].InputAudio.Format != "wav" {
		t.Fatalf("unexpected audio format: %#v", captured.Messages[0].Content[1].InputAudio.Format)
	}
	if captured.Messages[0].Content[1].InputAudio.Data != base64.StdEncoding.EncodeToString(audioBytes) {
		t.Fatalf("unexpected audio payload: %#v", captured.Messages[0].Content[1].InputAudio.Data)
	}
}

func TestOpenAICompatibleModelParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"value\":\"hello\"}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("say hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", resp.Choices)
	}
	if resp.Choices[0].ToolCalls[0].FunctionCall.Name != "echo" {
		t.Fatalf("unexpected tool call: %#v", resp.Choices[0].ToolCalls[0])
	}
}

func TestOpenAICompatibleModelStreamsContent(t *testing.T) {
	var captured struct {
		Stream bool `json:"stream"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var chunks []string
	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	resp, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("hello")},
		}},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			chunks = append(chunks, string(chunk))
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if !captured.Stream {
		t.Fatal("expected stream=true in request")
	}
	if resp.Choices[0].Content != "hello world" {
		t.Fatalf("unexpected streamed content: %#v", resp.Choices[0].Content)
	}
	if len(chunks) != 2 || chunks[0] != "hello " || chunks[1] != "world" {
		t.Fatalf("unexpected stream chunks: %#v", chunks)
	}
}

func TestOpenAICompatibleModelStreamsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"value\\\":\\\"hello\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	resp, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("say hello")},
		}},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error { return nil }),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if len(resp.Choices[0].ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", resp.Choices[0].ToolCalls)
	}
	call := resp.Choices[0].ToolCalls[0]
	if call.ID != "call_1" || call.FunctionCall.Name != "echo" || call.FunctionCall.Arguments != `{"value":"hello"}` {
		t.Fatalf("unexpected streamed tool call: %#v", call)
	}
}

func TestOpenAICompatibleModelMergesSystemMessages(t *testing.T) {
	var captured struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{
		{Role: llms.ChatMessageTypeSystem, Parts: []llms.ContentPart{llms.TextPart("System A")}},
		{Role: llms.ChatMessageTypeSystem, Parts: []llms.ContentPart{llms.TextPart("System B")}},
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("hello")}},
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	if len(captured.Messages) != 2 {
		t.Fatalf("expected 2 messages after normalization, got %#v", captured.Messages)
	}
	if captured.Messages[0].Role != "system" || captured.Messages[0].Content != "System A\n\nSystem B" {
		t.Fatalf("unexpected merged system message: %#v", captured.Messages[0])
	}
	if captured.Messages[1].Role != "user" || captured.Messages[1].Content != "hello" {
		t.Fatalf("unexpected user message: %#v", captured.Messages[1])
	}
}

func TestOpenAICompatibleModelIncludesUsageInGenerationInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	got := resp.Choices[0].GenerationInfo
	if got["prompt_tokens"] != 11 || got["completion_tokens"] != 7 || got["total_tokens"] != 18 {
		t.Fatalf("unexpected generation info: %#v", got)
	}
}

func TestModelManagerOpenRouterRetriesEOFInModelCall(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Fatalf("ReadAll body: %v", err)
		}
		if attempts == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("Hijack: %v", err)
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok after retry"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	manager := NewModelManager(ModelConfig{
		Provider: "openrouter",
		Model:    "test-model",
		APIKey:   "token",
		BaseURL:  server.URL,
	}, ProxyConfig{})
	model, err := manager.Get()
	if err != nil {
		t.Fatalf("Get model: %v", err)
	}

	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Choices[0].Content != "ok after retry" {
		t.Fatalf("unexpected response: %#v", resp.Choices[0].Content)
	}
}
