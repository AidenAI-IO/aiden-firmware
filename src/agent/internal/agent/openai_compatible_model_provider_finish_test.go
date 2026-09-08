package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestOpenAICompatibleModelReturnsProviderFinishErrorForErrorFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"resp_finish_1","choices":[{"message":{"content":""},"finish_reason":"error"}],"usage":{"prompt_tokens":17176,"completion_tokens":53,"total_tokens":17229}}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("test")},
	}})
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want ProviderFinishError")
	}
	var finishErr *ProviderFinishError
	if !errors.As(err, &finishErr) {
		t.Fatalf("GenerateContent() error type = %T, want *ProviderFinishError", err)
	}
	if finishErr.FinishReason != "error" || finishErr.Model != "test-model" || finishErr.ResponseID != "resp_finish_1" {
		t.Fatalf("ProviderFinishError = %#v", finishErr)
	}
	if !isProviderFinishError(err) {
		t.Fatal("isProviderFinishError() = false for ProviderFinishError")
	}
	if isProviderContextExceededError(err) {
		t.Fatal("isProviderContextExceededError() = true for ProviderFinishError")
	}
}

func TestOpenAICompatibleModelKeepsErrorFinishResponseWithContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":"resp_partial","choices":[{"message":{"content":"partial answer"},"finish_reason":"error"}]}`))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("test")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v, want usable partial content", err)
	}
	if resp.Choices[0].Content != "partial answer" || resp.Choices[0].StopReason != "error" {
		t.Fatalf("response = %#v", resp.Choices[0])
	}
}

func TestOpenAICompatibleModelReturnsProviderFinishErrorForErrorFinishStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"id\":\"resp_stream_error\",\"choices\":[{\"delta\":{},\"finish_reason\":\"error\"}],\"usage\":{\"prompt_tokens\":17176,\"completion_tokens\":53,\"total_tokens\":17229}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	_, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("test")},
		}},
		llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }),
	)
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want ProviderFinishError")
	}
	var finishErr *ProviderFinishError
	if !errors.As(err, &finishErr) {
		t.Fatalf("GenerateContent() error type = %T, want *ProviderFinishError", err)
	}
	if finishErr.FinishReason != "error" || finishErr.Model != "test-model" || finishErr.ResponseID != "resp_stream_error" {
		t.Fatalf("ProviderFinishError = %#v", finishErr)
	}
}

func TestOpenAICompatibleModelKeepsStreamedContentBeforeErrorFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"error\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := newOpenAICompatibleModel(server.URL, "test-model", "", server.Client())
	resp, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("test")},
		}},
		llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v, want usable partial content", err)
	}
	if resp.Choices[0].Content != "partial" || resp.Choices[0].StopReason != "error" {
		t.Fatalf("response = %#v", resp.Choices[0])
	}
}
