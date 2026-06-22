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

	resp, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
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
	resp, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
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
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir, "test-session-1")),
	)
	_, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("现在几点了？")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)

	// Parse JSONL and check for expected content
	var foundResponse, foundRequest bool
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		body, _ := entry["body"].(string)
		if strings.Contains(body, "finish_reason") && strings.Contains(body, "tool_calls") {
			foundResponse = true
		}
		if strings.Contains(body, "现在几点了") {
			foundRequest = true
		}
	}

	if !foundResponse {
		t.Fatalf("raw HTTP log missing response:\n%s", logText)
	}
	if !foundRequest {
		t.Fatalf("raw HTTP log missing request:\n%s", logText)
	}
	if !strings.Contains(logText, `"kind":"response"`) {
		t.Fatalf("raw HTTP log missing response metadata:\n%s", logText)
	}
	if !strings.Contains(logText, `"kind":"request"`) {
		t.Fatalf("raw HTTP log missing request record:\n%s", logText)
	}
	assertRawHTTPLogIsValidJSONL(t, logText)
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
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir, "test-session-1")),
	)
	_, err := model.GenerateContent(
		contextWithRawHTTPLog(context.Background()),
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

	// Parse JSONL and check for model in request body
	var foundModel bool
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		body, _ := entry["body"].(string)
		if strings.Contains(body, `"model":"override-model"`) {
			foundModel = true
			break
		}
	}

	if !foundModel {
		t.Fatalf("raw HTTP log should use effective model in request body:\n%s", logText)
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
		contextWithRawHTTPLog(context.Background()),
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
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir, "test-session-1")),
	)
	_, err := model.GenerateContent(
		contextWithRawHTTPLog(context.Background()),
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

	// Parse JSONL and check stream body contains expected events
	var foundStreamResponse bool
	var streamBody string
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["kind"] == "response" {
			streamBody, _ = entry["body"].(string)
			foundStreamResponse = true
			break
		}
	}

	if !foundStreamResponse {
		t.Fatalf("raw streaming HTTP log missing stream response:\n%s", logText)
	}
	if strings.Contains(streamBody, "data:") {
		t.Fatalf("raw streaming HTTP log should store a readable JSON response, not raw SSE:\n%s", streamBody)
	}
	var logged struct {
		Stream  bool `json:"stream"`
		Done    bool `json:"done"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(streamBody), &logged); err != nil {
		t.Fatalf("raw streaming HTTP response body should be JSON: %v\n%s", err, streamBody)
	}
	if !logged.Stream || !logged.Done {
		t.Fatalf("raw streaming HTTP response missing stream metadata: %#v", logged)
	}
	if len(logged.Choices) != 1 {
		t.Fatalf("raw streaming HTTP response choices = %#v", logged.Choices)
	}
	if logged.Choices[0].Message.Content != "<think>\n需要查当前时间。\n</think>" {
		t.Fatalf("raw streaming HTTP response content = %q", logged.Choices[0].Message.Content)
	}
	if logged.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("raw streaming HTTP response finish_reason = %q", logged.Choices[0].FinishReason)
	}
	if !strings.Contains(logText, `"kind":"response"`) {
		t.Fatalf("raw streaming HTTP log missing metadata:\n%s", logText)
	}
	assertRawHTTPLogIsValidJSONL(t, logText)
}

func TestOpenAICompatibleModelLogsRawStreamingHTTPOnDecodeError(t *testing.T) {
	validEvent := `data: {"choices":[{"delta":{"content":"hello"}}]}`
	malformedEvent := `data: {"choices":[`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(validEvent + "\n\n"))
		w.Write([]byte(malformedEvent + "\n\n"))
	}))
	defer server.Close()

	logDir := t.TempDir()
	model := newOpenAICompatibleModel(
		server.URL,
		"test-model",
		"",
		server.Client(),
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir, "test-session-1")),
	)
	_, err := model.GenerateContent(
		contextWithRawHTTPLog(context.Background()),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("stream please")},
		}},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error { return nil }),
	)
	if err == nil || !strings.Contains(err.Error(), "decode stream event") {
		t.Fatalf("GenerateContent() error = %v, want decode stream event", err)
	}

	logText := readRawHTTPLog(t, logDir)
	var streamBody string
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["kind"] == "response" {
			streamBody, _ = entry["body"].(string)
			break
		}
	}
	var logged struct {
		Stream  bool   `json:"stream"`
		Error   string `json:"error"`
		RawSSE  string `json:"raw_sse"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(streamBody), &logged); err != nil {
		t.Fatalf("raw streaming error response body should be JSON: %v\n%s", err, streamBody)
	}
	if !logged.Stream {
		t.Fatalf("raw streaming error response missing stream marker: %#v", logged)
	}
	if !strings.Contains(logged.Error, "decode stream event") {
		t.Fatalf("raw streaming error response missing decode error: %#v", logged)
	}
	if len(logged.Choices) != 1 || logged.Choices[0].Message.Content != "hello" {
		t.Fatalf("raw streaming error response missing partial content: %#v", logged.Choices)
	}
	if !strings.Contains(logged.RawSSE, validEvent) || !strings.Contains(logged.RawSSE, malformedEvent) {
		t.Fatalf("raw streaming error response missing failed SSE events:\nlog=%s\nstream=%s", logText, streamBody)
	}
	assertRawHTTPLogIsValidJSONL(t, logText)
}

func TestOpenAICompatibleModelRawHTTPLogKeepsRunInRequestMinuteFile(t *testing.T) {
	sessionID := "session-a"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID = "session-b"
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	logDir := t.TempDir()
	logger := newLLMRawHTTPLogger(logDir, "")
	logger.SetSessionIDProvider(func() string { return sessionID })
	times := []time.Time{
		time.Date(2026, 6, 21, 9, 4, 59, 0, time.UTC),
		time.Date(2026, 6, 21, 9, 4, 59, 0, time.UTC),
		time.Date(2026, 6, 21, 9, 5, 1, 0, time.UTC),
	}
	logger.now = func() time.Time {
		if len(times) == 0 {
			return time.Date(2026, 6, 21, 9, 5, 1, 0, time.UTC)
		}
		next := times[0]
		times = times[1:]
		return next
	}

	model := newOpenAICompatibleModel(
		server.URL,
		"test-model",
		"",
		server.Client(),
		withOpenAICompatibleRawHTTPLogger(logger),
	)
	_, err := model.GenerateContent(
		contextWithRawHTTPLog(context.Background()),
		[]llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("hello")},
		}},
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error { return nil }),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(logDir, "llm-http-*.log"))
	if err != nil {
		t.Fatalf("glob raw HTTP logs: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("raw HTTP request/response should stay in one file, got %d: %#v", len(matches), matches)
	}
	if !strings.Contains(filepath.Base(matches[0]), "llm-http-202606210904-session-a.log") {
		t.Fatalf("raw HTTP file should use request minute, got %s", filepath.Base(matches[0]))
	}
	logText := readRawHTTPLog(t, logDir)
	counts := countRawHTTPKinds(t, logText)
	if counts["request"] != 1 || counts["response"] != 1 {
		t.Fatalf("raw HTTP log should contain one request and one response:\n%s", logText)
	}
}

func TestRuntimeConfiguresRawHTTPLogWithActiveMemorySessionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	rootDir := t.TempDir()
	logDir := filepath.Join(rootDir, "log")
	memoryDir := filepath.Join(rootDir, "memory")
	manager := NewModelManager(ModelConfig{
		Provider:   "openai",
		BaseURL:    server.URL,
		Model:      "test-model",
		LogRawHTTP: true,
	}, ProxyConfig{}, WithLLMRawHTTPLogDir(logDir))
	memories := NewMemoryManager(memoryDir)
	NewRuntimeWithDeps(Config{}, manager, memories, nil, nil)

	model, err := manager.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	compatible, ok := model.(*openAICompatibleModel)
	if !ok {
		t.Fatalf("model = %T, want *openAICompatibleModel", model)
	}
	compatible.rawLogger.now = func() time.Time {
		return time.Date(2026, 6, 21, 9, 4, 59, 0, time.UTC)
	}

	_, err = model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	metadata := readSessionMetadataForTest(t, filepath.Join(memoryDir, "session", sessionMetadataFileName))
	matches, err := filepath.Glob(filepath.Join(logDir, "llm-http-*.log"))
	if err != nil {
		t.Fatalf("glob raw HTTP logs: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one raw HTTP log file, got %d: %#v", len(matches), matches)
	}
	wantName := "llm-http-202606210904-" + metadata.SessionID + ".log"
	if filepath.Base(matches[0]) != wantName {
		t.Fatalf("raw HTTP file = %q, want %q", filepath.Base(matches[0]), wantName)
	}
}

// countRawHTTPKinds returns how many log entries carry each kind, so tests can
// assert the request/response pairing invariant the log viewer relies on.
func countRawHTTPKinds(t *testing.T, logText string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSONL line: %v\nline: %s", err, line)
		}
		kind, _ := entry["kind"].(string)
		counts[kind]++
	}
	return counts
}

func TestOpenAICompatibleModelLogsResponseOnTransportError(t *testing.T) {
	// Point the model at a server that is already closed so httpClient.Do
	// fails at the transport layer before any HTTP response exists.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := server.URL
	client := server.Client()
	server.Close()

	logDir := t.TempDir()
	model := newOpenAICompatibleModel(
		closedURL,
		"test-model",
		"",
		client,
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir, "test-session-1")),
	)
	_, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}

	logText := readRawHTTPLog(t, logDir)
	counts := countRawHTTPKinds(t, logText)
	if counts["request"] != 1 {
		t.Fatalf("expected 1 request entry, got %d:\n%s", counts["request"], logText)
	}
	if counts["response"] != 1 {
		t.Fatalf("expected 1 response entry on transport failure, got %d:\n%s", counts["response"], logText)
	}
	if !strings.Contains(logText, "transport error:") {
		t.Fatalf("response entry missing transport error detail:\n%s", logText)
	}
	if !strings.Contains(logText, `"status":0`) {
		t.Fatalf("transport failure should log status 0:\n%s", logText)
	}
	assertRawHTTPLogIsValidJSONL(t, logText)
}

func TestOpenAICompatibleModelSkipsRawHTTPLogWithoutContextMarker(t *testing.T) {
	// Background tasks (summarization, profile rebuilds, skill merge) share the
	// model and logger but call with a plain context. Those calls must not be
	// logged, so their requests don't interleave with the main loop's entries.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	logDir := t.TempDir()
	model := newOpenAICompatibleModel(
		server.URL,
		"test-model",
		"",
		server.Client(),
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(logDir, "test-session-1")),
	)
	// Plain context, no contextWithRawHTTPLog marker.
	_, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	if _, statErr := os.Stat(rawHTTPLogPath(logDir)); statErr == nil {
		t.Fatalf("raw HTTP log was written for a call without the context marker:\n%s", readRawHTTPLog(t, logDir))
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("unexpected error stating log path: %v", statErr)
	}
}

func TestOpenAICompatibleModelDoesNotBufferRawHTTPResponseWithoutContextMarker(t *testing.T) {
	responseBody := &failOnSecondReadBody{
		payload: []byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`),
	}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       responseBody,
			}, nil
		}),
	}

	model := newOpenAICompatibleModel(
		"https://example.test",
		"test-model",
		"",
		client,
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(t.TempDir(), "test-session-1")),
	)

	resp, err := model.GenerateContent(context.Background(), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if got := resp.Choices[0].Content; got != "ok" {
		t.Fatalf("response content = %q, want ok", got)
	}
	if responseBody.reads != 1 {
		t.Fatalf("response body reads = %d, want 1", responseBody.reads)
	}
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
	_, err = model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)

	// Parse JSONL and check for expected content
	var foundResponse bool
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		body, _ := entry["body"].(string)
		if strings.Contains(body, "finish_reason") && strings.Contains(body, "ok") {
			foundResponse = true
			break
		}
	}

	if !foundResponse {
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
	_, err = model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart("hello")},
	}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	logText := readRawHTTPLog(t, logDir)

	// Parse JSONL and check for expected content
	var foundResponse bool
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		body, _ := entry["body"].(string)
		if strings.Contains(body, "finish_reason") && strings.Contains(body, "ok") {
			foundResponse = true
			break
		}
	}

	if !foundResponse {
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
	_, err = model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
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
		contextWithRawHTTPLog(context.Background()),
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
	_, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{
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
	resp, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
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
	return filepath.Join(logDir, "llm-http-"+time.Now().Format("200601021504")+".log")
}

func assertRawHTTPLogIsValidJSONL(t *testing.T, logText string) {
	t.Helper()
	trimmed := strings.TrimSpace(logText)
	if trimmed == "" {
		t.Fatal("raw HTTP log is empty")
	}
	lines := strings.Split(trimmed, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %s", i+1, err, line)
		}
		// Check required fields
		if _, ok := entry["ts"]; !ok {
			t.Fatalf("line %d missing 'ts' field: %s", i+1, line)
		}
		if _, ok := entry["kind"]; !ok {
			t.Fatalf("line %d missing 'kind' field: %s", i+1, line)
		}
		if _, ok := entry["status"]; !ok {
			t.Fatalf("line %d missing 'status' field: %s", i+1, line)
		}
		if _, ok := entry["body"]; !ok {
			t.Fatalf("line %d missing 'body' field: %s", i+1, line)
		}
		// Validate timestamp format
		ts, ok := entry["ts"].(string)
		if !ok {
			t.Fatalf("line %d 'ts' is not a string: %s", i+1, line)
		}
		if _, err := time.Parse("15:04:05", ts); err != nil {
			t.Fatalf("line %d 'ts' format invalid (want HH:MM:SS): %v", i+1, err)
		}
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

type failOnSecondReadBody struct {
	payload []byte
	reads   int
}

func (b *failOnSecondReadBody) Read(p []byte) (int, error) {
	b.reads++
	if b.reads == 1 {
		return copy(p, b.payload), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (b *failOnSecondReadBody) Close() error {
	return nil
}

func assertEveryLineIsCompleteRecord(t *testing.T, logText string) {
	// This is now covered by assertRawHTTPLogIsValidJSONL
	t.Helper()
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

	resp, err := model.GenerateContent(contextWithRawHTTPLog(context.Background()), []llms.MessageContent{{
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
