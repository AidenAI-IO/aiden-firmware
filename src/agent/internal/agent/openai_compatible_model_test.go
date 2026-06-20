package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestOpenAICompatibleModelLogsRawHTTPWhenEnabled(t *testing.T) {
	rawResponse := `{"choices":[{"message":{"content":"<think>\n需要查当前时间。\n</think>","tool_calls":[{"id":"call_1","type":"function","function":{"name":"current_time","arguments":"{\"timezone\":\"local\"}"}}]},"finish_reason":"tool_calls"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rawResponse))
	}))
	defer server.Close()

	logDir := t.TempDir()
	model := newOpenAICompatibleModel(
		server.URL,
		"test-model",
		"",
		server.Client(),
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir)),
	)
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("现在几点了？")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)
	if !strings.Contains(logText, rawResponse) {
		t.Fatalf("raw HTTP log missing exact response:\n%s", logText)
	}
	if !strings.Contains(logText, "kind=http_response") || !strings.Contains(logText, "model=test-model") {
		t.Fatalf("raw HTTP log missing metadata:\n%s", logText)
	}
	if !strings.Contains(logText, "dir=response") {
		t.Fatalf("raw HTTP log missing direction field:\n%s", logText)
	}
	if !strings.Contains(logText, "dir=request") || !strings.Contains(logText, "kind=http_request") {
		t.Fatalf("raw HTTP log missing request record:\n%s", logText)
	}
	if !strings.Contains(logText, "现在几点了") {
		t.Fatalf("raw HTTP log missing request content:\n%s", logText)
	}
	assertRawHTTPLogHasRFC3339NanoTimestamp(t, logText)
}

func TestOpenAICompatibleModelRawHTTPLogUsesEffectiveRequestModel(t *testing.T) {
	rawResponse := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rawResponse))
	}))
	defer server.Close()

	logDir := t.TempDir()
	model := newOpenAICompatibleModel(
		server.URL,
		"configured-model",
		"",
		server.Client(),
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir)),
	)
	_, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("hello")},
		}},
		llms.WithModel("override-model"),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)
	if !strings.Contains(logText, "model=override-model") {
		t.Fatalf("raw HTTP log used wrong model metadata:\n%s", logText)
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

func TestOpenAICompatibleModelLogsRawStreamingHTTPWhenEnabled(t *testing.T) {
	firstEvent := `data: {"choices":[{"delta":{"content":"<think>\n"}}]}`
	secondEvent := `data: {"choices":[{"delta":{"content":"需要查当前时间。\n</think>"},"finish_reason":"tool_calls"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(firstEvent + "\n\n"))
		w.Write([]byte(secondEvent + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	logDir := t.TempDir()
	model := newOpenAICompatibleModel(
		server.URL,
		"test-model",
		"",
		server.Client(),
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir)),
	)
	_, err := model.GenerateContent(
		context.Background(),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("现在几点了？")},
		}},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error { return nil }),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)
	// Stream is aggregated into one line with escaped newlines
	if !findLogLineContaining(logText, firstEvent) {
		t.Fatalf("raw streaming HTTP log missing first SSE event:\n%s", logText)
	}
	if !findLogLineContaining(logText, secondEvent) {
		t.Fatalf("raw streaming HTTP log missing second SSE event:\n%s", logText)
	}
	if !findLogLineContaining(logText, "data: [DONE]") {
		t.Fatalf("raw streaming HTTP log missing [DONE] marker:\n%s", logText)
	}
	if !strings.Contains(logText, "kind=http_stream") || !strings.Contains(logText, "model=test-model") {
		t.Fatalf("raw streaming HTTP log missing metadata:\n%s", logText)
	}
	if !strings.Contains(logText, "dir=response") {
		t.Fatalf("raw streaming HTTP log missing direction field:\n%s", logText)
	}
	assertRawHTTPLogHasRFC3339NanoTimestamp(t, logText)
	assertEveryLineIsCompleteRecord(t, logText)
}

func TestModelManagerEnablesRawHTTPLoggingFromModelConfig(t *testing.T) {
	rawResponse := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rawResponse))
	}))
	defer server.Close()

	logDir := t.TempDir()
	manager := NewModelManager(ModelConfig{
		Provider:   "openai",
		Model:      "test-model",
		BaseURL:    server.URL,
		LogRawHTTP: true,
	}, ProxyConfig{}, WithLLMRawHTTPLogDir(logDir))
	model, err := manager.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	_, err = model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)
	if !strings.Contains(logText, rawResponse) {
		t.Fatalf("raw HTTP log missing response from model manager config:\n%s", logText)
	}
}

func TestModelManagerEnablesRawHTTPLoggingFromDefaultConfig(t *testing.T) {
	rawResponse := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rawResponse))
	}))
	defer server.Close()

	logDir := t.TempDir()
	cfg := DefaultConfig().Model
	cfg.Provider = "openai"
	cfg.Model = "test-model"
	cfg.BaseURL = server.URL
	manager := NewModelManager(cfg, ProxyConfig{}, WithLLMRawHTTPLogDir(logDir))
	model, err := manager.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	_, err = model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)
	if !strings.Contains(logText, rawResponse) {
		t.Fatalf("raw HTTP log missing response from default config:\n%s", logText)
	}
}

func TestModelManagerDoesNotLogRawHTTPWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	logDir := t.TempDir()
	manager := NewModelManager(ModelConfig{
		Provider: "openai",
		Model:    "test-model",
		BaseURL:  server.URL,
	}, ProxyConfig{}, WithLLMRawHTTPLogDir(logDir))
	model, err := manager.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	_, err = model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	if _, err := os.Stat(rawHTTPLogPath(logDir)); !os.IsNotExist(err) {
		t.Fatalf("raw HTTP log should not exist when disabled, err=%v", err)
	}
}

func TestOpenAICompatibleModelStreamsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"id\":\"call_3\",\"type\":\"function\",\"function\":{\"name\":\"later\",\"arguments\":\"{\\\"\"}},{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"value\\\":\\\"hello\\\"}\"}},{\"index\":2,\"function\":{\"arguments\":\"value\\\":\\\"done\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
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
	if len(resp.Choices[0].ToolCalls) != 2 {
		t.Fatalf("expected two tool calls, got %#v", resp.Choices[0].ToolCalls)
	}
	call := resp.Choices[0].ToolCalls[0]
	if call.ID != "call_1" || call.FunctionCall.Name != "echo" || call.FunctionCall.Arguments != `{"value":"hello"}` {
		t.Fatalf("unexpected streamed tool call: %#v", call)
	}
	call = resp.Choices[0].ToolCalls[1]
	if call.ID != "call_3" || call.FunctionCall.Name != "later" || call.FunctionCall.Arguments != `{"value":"done"}` {
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

func readRawHTTPLog(t *testing.T, logDir string) string {
	t.Helper()
	data, err := os.ReadFile(rawHTTPLogPath(logDir))
	if err != nil {
		t.Fatalf("read raw HTTP log: %v", err)
	}
	return string(data)
}

func rawHTTPLogPath(logDir string) string {
	matches, _ := filepath.Glob(filepath.Join(logDir, "llm-http-*.log"))
	sort.Strings(matches)
	if len(matches) > 0 {
		return matches[len(matches)-1]
	}
	return filepath.Join(logDir, "llm-http-"+time.Now().Format("20060102")+".log")
}

func assertRawHTTPLogHasRFC3339NanoTimestamp(t *testing.T, logText string) {
	t.Helper()
	match := regexp.MustCompile(`ts=([^ ]+)`).FindStringSubmatch(logText)
	if len(match) != 2 {
		t.Fatalf("raw HTTP log missing timestamp:\n%s", logText)
	}
	if _, err := time.Parse(time.RFC3339Nano, match[1]); err != nil {
		t.Fatalf("raw HTTP log timestamp = %q, want RFC3339Nano: %v", match[1], err)
	}
}

func findLogLineContaining(logText, substring string) bool {
	lines := strings.Split(logText, "\n")
	for _, line := range lines {
		if strings.Contains(line, substring) {
			return true
		}
	}
	return false
}

func assertEveryLineIsCompleteRecord(t *testing.T, logText string) {
	t.Helper()
	lines := strings.Split(logText, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "ts=") {
			t.Fatalf("log line does not start with timestamp: %s", line)
		}
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
