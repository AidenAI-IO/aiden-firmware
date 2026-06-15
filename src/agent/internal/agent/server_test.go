package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"

	ttsmodule "aiden-agent/internal/agent/tts"
)

type stubSTTClient struct {
	transcript string
	inputs     [][]byte
}

func (s *stubSTTClient) TranscribeWAV(wavData []byte) (string, error) {
	s.inputs = append(s.inputs, append([]byte(nil), wavData...))
	return s.transcript, nil
}

func firstMessageOfType(messages []Message, messageType string) (Message, bool) {
	for _, message := range messages {
		if message.Type == messageType {
			return message, true
		}
	}
	return Message{}, false
}

func TestServerHandleChatReturnsToolHistory(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。"}`, "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	body := bytes.NewBufferString(`{"message":"当前音量是多少？"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Response != "The current audio volume is 42." {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) != 6 {
		t.Fatalf("expected 6 history entries for default-mode planner tool flow, got %d", len(resp.History))
	}

	if resp.History[0].Type != "user" || resp.History[0].Content != "当前音量是多少？" {
		t.Fatalf("unexpected first history message: %#v", resp.History[0])
	}
	roleOutput, ok := firstMessageOfType(resp.History, "role_output")
	if !ok || roleOutput.Role != "planner" {
		t.Fatalf("expected planner role_output in history: %#v", resp.History)
	}
	toolCall, ok := firstMessageOfType(resp.History, "tool_call")
	if !ok || toolCall.ToolName != "audio_volume" || toolCall.ToolInput != "{}" {
		t.Fatalf("unexpected tool_call message: %#v", resp.History)
	}
	if toolCall.Description != "我先读取当前音量。" || toolCall.Content != "我先读取当前音量。" {
		t.Fatalf("unexpected tool_call description: %#v", toolCall)
	}
	toolResult, ok := firstMessageOfType(resp.History, "tool_result")
	if !ok || toolResult.ToolName != "audio_volume" || toolResult.Content != `{"volume":42}` {
		t.Fatalf("unexpected tool_result message: %#v", resp.History)
	}
	assistant, ok := firstMessageOfType(resp.History, "assistant")
	if !ok || assistant.Content != "The current audio volume is 42." {
		t.Fatalf("unexpected assistant message: %#v", resp.History)
	}
}

func TestServerPersistsChatHistoryWithEpisodeReference(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	model := &scriptedModel{
		responses: roleDirectResponses("已完成"),
	}
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:                configDir,
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Answer directly.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
			VoiceToolCallSpeech:      &streamingDisabled,
		},
		&testModelResolver{model: model},
		NewMemoryManager(memoryDir),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"做一个任务"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assistant, ok := firstMessageOfType(resp.History, "assistant")
	if !ok || assistant.EpisodeID == "" {
		t.Fatalf("assistant missing episode reference: %#v", resp.History)
	}
	if resp.History[0].EpisodeID != assistant.EpisodeID {
		t.Fatalf("user and assistant episode ids differ: %#v", resp.History)
	}

	reloaded := NewServer(runtime, ":0", "")
	historyReq := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	historyRec := httptest.NewRecorder()
	reloaded.handleHistory(historyRec, historyReq)
	if historyRec.Code != http.StatusOK {
		t.Fatalf("unexpected history status: %d body=%s", historyRec.Code, historyRec.Body.String())
	}
	var restored []Message
	if err := json.NewDecoder(historyRec.Body).Decode(&restored); err != nil {
		t.Fatalf("decode restored history: %v", err)
	}
	restoredAssistant, ok := firstMessageOfType(restored, "assistant")
	if !ok || restoredAssistant.EpisodeID != assistant.EpisodeID {
		t.Fatalf("restored assistant missing episode reference: %#v", restored)
	}

	episodeReq := httptest.NewRequest(http.MethodGet, "/api/episodes/"+assistant.EpisodeID, nil)
	episodeRec := httptest.NewRecorder()
	server.handleEpisodes(episodeRec, episodeReq)
	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}
	var episodeResp EpisodeResponse
	if err := json.NewDecoder(episodeRec.Body).Decode(&episodeResp); err != nil {
		t.Fatalf("decode episode response: %v", err)
	}
	if episodeResp.Episode.ID != assistant.EpisodeID || len(episodeResp.Episode.Events) == 0 {
		t.Fatalf("unexpected episode response: %#v", episodeResp.Episode)
	}
}

func TestServerHandleChatStreamsRoleToolAndAssistantMessages(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。"}`, "The current audio volume is 42."),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": &stubTool{
				name:        "audio_volume",
				description: "Get the current audio playback volume.",
				output:      `{"volume":42}`,
			},
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"当前音量是多少？"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("X-Aiden-Stream", "ndjson")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q, want application/x-ndjson", contentType)
	}

	var events []ChatStreamEvent
	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for scanner.Scan() {
		var event ChatStreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode stream event %q: %v", scanner.Text(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}

	var sawPlanner, sawToolCall, sawToolResult, sawAssistant, sawDone bool
	for _, event := range events {
		if event.Type == "message" && event.Message != nil {
			switch event.Message.Type {
			case "role_output":
				if event.Message.Role == "planner" {
					sawPlanner = true
				}
			case "tool_call":
				sawToolCall = event.Message.ToolName == "audio_volume"
			case "tool_result":
				sawToolResult = event.Message.ToolName == "audio_volume" && event.Message.Content == `{"volume":42}`
			case "assistant":
				sawAssistant = event.Message.Content == "The current audio volume is 42."
			}
		}
		if event.Type == "done" && event.Response == "The current audio volume is 42." {
			sawDone = true
		}
	}
	if !sawPlanner || !sawToolCall || !sawToolResult || !sawAssistant || !sawDone {
		t.Fatalf("missing expected stream events: planner=%v tool_call=%v tool_result=%v assistant=%v done=%v events=%#v",
			sawPlanner, sawToolCall, sawToolResult, sawAssistant, sawDone, events)
	}
}

func TestServerHandleChatStreamTagsHistoryWithRequestID(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("你好！"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"hello","request_id":"web-req-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("X-Aiden-Stream", "ndjson")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	history := server.historySnapshot()
	user, ok := firstMessageOfType(history, "user")
	if !ok {
		t.Fatalf("missing user history: %#v", history)
	}
	if user.RequestID != "web-req-1" {
		t.Fatalf("user request_id = %q, want web-req-1", user.RequestID)
	}
	assistant, ok := firstMessageOfType(history, "assistant")
	if !ok {
		t.Fatalf("missing assistant history: %#v", history)
	}
	if assistant.RequestID != "web-req-1" {
		t.Fatalf("assistant request_id = %q, want web-req-1", assistant.RequestID)
	}
}

func TestServerSpeakToolDescriptionUsesTTS(t *testing.T) {
	provider := &recordingTTSProvider{name: "server-provider"}
	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}, Audio: AudioConfig{SampleRate: 16000}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
	}

	server.speakToolDescription(context.Background(), " 我先读取当前音量。 ")

	if got := provider.texts(); len(got) != 1 || got[0] != "我先读取当前音量。" {
		t.Fatalf("unexpected TTS texts: %#v", got)
	}
}

func TestServerHandleChatDoesNotWaitForToolDescriptionTTS(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。"}`, "The current audio volume is 42."),
	}
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": &stubTool{
				name:        "audio_volume",
				description: "Get the current audio playback volume.",
				output:      `{"volume":42}`,
			},
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")
	provider := &blockingTTSProvider{started: make(chan struct{}), blockText: "我先读取当前音量。"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient("/tmp/audio.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"当前音量是多少？"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		server.handleChat(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("handleChat waited for tool description TTS")
	}
	select {
	case <-provider.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("tool description TTS was not started")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServerHandleChatSkipsToolDescriptionTTSWhenDisabled(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。"}`, "The current audio volume is 42."),
	}
	streamingDisabled := false
	toolSpeechDisabled := false
	runtime := NewRuntimeWithDeps(
		Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
			VoiceToolCallSpeech:      &toolSpeechDisabled,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": &stubTool{
				name:        "audio_volume",
				description: "Get the current audio playback volume.",
				output:      `{"volume":42}`,
			},
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")
	provider := &blockingTTSProvider{started: make(chan struct{}), blockText: "我先读取当前音量。"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient("/tmp/audio.sock")

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"当前音量是多少？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-provider.started:
		t.Fatal("tool description TTS started despite voice_tool_call_speech=false")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestServerHandleChatUsesRequestContextForRun(t *testing.T) {
	model := &cancelAwareModel{started: make(chan struct{}), seen: make(chan error, 1)}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"hello"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		server.handleChat(rec, req)
		close(done)
	}()

	select {
	case <-model.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runtime did not start model call")
	}
	cancel()

	select {
	case err := <-model.seen:
		if err == nil {
			t.Fatal("model saw nil context error, want request cancellation")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runtime did not receive request context cancellation")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleChat did not return after request cancellation")
	}
}

func TestServerHandleChatCancelCancelsActiveRun(t *testing.T) {
	server := &Server{activeRuns: make(map[string]context.CancelFunc)}
	ctx, cancel := context.WithCancel(context.Background())
	server.registerActiveRun("req-1", cancel)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/cancel", bytes.NewBufferString(`{"request_id":" req-1 "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChatCancel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("active run was not canceled")
	}

	var resp ChatCancelResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "canceled" || resp.RequestID != "req-1" {
		t.Fatalf("unexpected cancel response: %#v", resp)
	}
}

func TestServerHandleChatSteerQueuesAndCancelsPendingMessage(t *testing.T) {
	server := &Server{
		activeRuns:    make(map[string]context.CancelFunc),
		pendingSteers: make(map[string]pendingSteerMessage),
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun("req-1", cancel)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/steer", bytes.NewBufferString(`{"request_id":" req-1 ","message":" change direction "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChatSteer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ChatSteerResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "pending" || resp.RequestID != "req-1" || resp.Steer == nil || resp.Steer.Content != "change direction" {
		t.Fatalf("unexpected steer response: %#v", resp)
	}
	steer, ok := server.consumePendingSteer("req-1")
	if !ok || steer.Content != "change direction" {
		t.Fatalf("unexpected consumed steer: %#v ok=%v", steer, ok)
	}
	if _, ok := server.consumePendingSteer("req-1"); ok {
		t.Fatal("pending steer was not consumed exactly once")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/chat/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"cancel this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.handleChatSteer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected requeue status: %d body=%s", rec.Code, rec.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/chat/steer/cancel", bytes.NewBufferString(`{"request_id":"req-1"}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	server.handleChatSteerCancel(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("unexpected cancel status: %d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	var cancelResp ChatSteerResponse
	if err := json.NewDecoder(cancelRec.Body).Decode(&cancelResp); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if cancelResp.Status != "canceled" || cancelResp.RequestID != "req-1" {
		t.Fatalf("unexpected cancel response: %#v", cancelResp)
	}
	if _, ok := server.consumePendingSteer("req-1"); ok {
		t.Fatal("canceled steer was still pending")
	}
}

func TestServerHandleChatSteerRejectsNonRunningRequest(t *testing.T) {
	server := &Server{activeRuns: make(map[string]context.CancelFunc)}
	req := httptest.NewRequest(http.MethodPost, "/api/chat/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChatSteer(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebUISteerModeControlsArePresent(t *testing.T) {
	for _, want := range []string{
		"/api/chat/steer",
		"/api/chat/steer/cancel",
		"async function submitSteerMessage()",
		"sendBtn.textContent = isLoading ? 'Steer' : 'Send';",
		"id=\"pendingSteer\"",
	} {
		if !strings.Contains(webUI, want) {
			t.Fatalf("web UI missing %q", want)
		}
	}
}

func TestServerHandleChatAsyncDuplicateRequestIDDoesNotAppendHistory(t *testing.T) {
	server := &Server{
		activeRuns:     make(map[string]context.CancelFunc),
		pendingResults: map[string]*chatPendingResult{"req-1": {}},
		history:        make([]Message, 0),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"hello","request_id":" req-1 "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := server.historySnapshot(); len(got) != 0 {
		t.Fatalf("duplicate request appended history: %#v", got)
	}
}

func TestServerHandleChatStreamDuplicateRequestIDDoesNotAppendHistory(t *testing.T) {
	server := &Server{
		activeRuns: make(map[string]context.CancelFunc),
		history:    make([]Message, 0),
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun("req-1", cancel)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"hello","request_id":" req-1 "}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("X-Aiden-Stream", "ndjson")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := server.historySnapshot(); len(got) != 0 {
		t.Fatalf("duplicate stream request appended history: %#v", got)
	}
}

func TestServerSpeakToolDescriptionUsesCallerContext(t *testing.T) {
	provider := &blockingTTSProvider{started: make(chan struct{}), blockText: "我先读取当前音量。"}
	server := &Server{
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient("/tmp/audio.sock"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		server.speakToolDescription(ctx, "我先读取当前音量。")
		close(done)
	}()

	select {
	case <-provider.started:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("tool description TTS was not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("tool description TTS did not stop after caller context cancellation")
	}
}

type cancelAwareModel struct {
	started chan struct{}
	seen    chan error
	once    sync.Once
}

func (m *cancelAwareModel) GenerateContent(ctx context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	m.once.Do(func() {
		if m.started != nil {
			close(m.started)
		}
	})
	<-ctx.Done()
	err := ctx.Err()
	m.seen <- err
	return nil, err
}

func (m *cancelAwareModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

type blockingTTSProvider struct {
	started   chan struct{}
	blockText string
	once      sync.Once
}

func (p *blockingTTSProvider) Name() string { return "blocking" }

func (p *blockingTTSProvider) Capabilities() ttsmodule.Capabilities {
	return ttsmodule.Capabilities{SupportedSampleRates: []int{16000}}
}

func (p *blockingTTSProvider) BeginStream(ctx context.Context, sink ttsmodule.AudioSink) (ttsmodule.StreamSession, error) {
	return &blockingTTSSession{ctx: ctx, provider: p}, nil
}

func (p *blockingTTSProvider) Close() error { return nil }

type blockingTTSSession struct {
	ctx      context.Context
	provider *blockingTTSProvider
}

func (s *blockingTTSSession) WriteText(text string) error {
	if text == s.provider.blockText {
		s.provider.once.Do(func() { close(s.provider.started) })
	}
	return nil
}

func (s *blockingTTSSession) Flush() error { return nil }

func (s *blockingTTSSession) Close() error {
	<-s.ctx.Done()
	return s.ctx.Err()
}

func (s *blockingTTSSession) Err() error {
	return s.ctx.Err()
}

func TestServerHistoryEndpointIncludesToolMessages(t *testing.T) {
	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		history: []Message{
			{Type: "user", Content: "hello"},
			{Type: "tool_call", ToolName: "screenshot", ToolInput: "{}"},
			{Type: "tool_result", ToolName: "screenshot", Content: `{"width":100}`},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	server.handleHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var history []Message
	if err := json.NewDecoder(rec.Body).Decode(&history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(history))
	}
	if history[1].Type != "tool_call" || history[2].Type != "tool_result" {
		t.Fatalf("unexpected history payload: %#v", history)
	}
}

func TestServerHandleClearRemovesRuntimeMemory(t *testing.T) {
	storageDir := t.TempDir()
	memoryManager := NewMemoryManager(storageDir)
	handle, err := memoryManager.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := handle.History.SetMessages(context.Background(), []llms.ChatMessage{
		llms.HumanChatMessage{Content: "记一下，以后处理蓝海报销App超过100元必须先确认。"},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := memoryManager.Save(context.Background(), "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session", "events.jsonl")); err != nil {
		t.Fatalf("expected session events before clear: %v", err)
	}

	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}},
			&testModelResolver{model: &scriptedModel{}},
			memoryManager,
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		history: []Message{{Type: "user", Content: "hello"}},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/clear", nil)
	rec := httptest.NewRecorder()
	server.handleClear(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(server.history) != 0 {
		t.Fatalf("expected web history to be cleared, got %#v", server.history)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session")); !os.IsNotExist(err) {
		t.Fatalf("expected session memory to be removed, stat err = %v", err)
	}
}

func TestServerHandleSkillsReloadMarksDirty(t *testing.T) {
	storageDir := t.TempDir()
	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(storageDir),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
	}

	if server.runtime.skillsDirty {
		t.Fatalf("expected skillsDirty=false initially")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/skills/reload", nil)
	rec := httptest.NewRecorder()
	server.handleSkillsReload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !server.runtime.skillsDirty {
		t.Fatalf("expected skillsDirty=true after reload request")
	}
}

func TestServerHandleSkillsReloadRejectsGet(t *testing.T) {
	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(t.TempDir()),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/skills/reload", nil)
	rec := httptest.NewRecorder()
	server.handleSkillsReload(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestServerHandleChatWithAudioAttachmentUsesSTT(t *testing.T) {
	stt := &stubSTTClient{transcript: "你好，帮我总结一下"}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("已处理")}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := &Server{
		runtime:   runtime,
		history:   make([]Message, 0),
		sttClient: stt,
	}

	payload, err := json.Marshal(ChatRequest{
		Attachments: []MessageAttachment{{
			Kind:     AttachmentKindAudio,
			Name:     "recording.wav",
			MIMEType: "audio/wav",
			Data:     base64.StdEncoding.EncodeToString([]byte("RIFFtest")),
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(stt.inputs) != 1 {
		t.Fatalf("expected 1 STT invocation, got %d", len(stt.inputs))
	}

	var resp ChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Response != "已处理" {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) != 3 {
		t.Fatalf("expected 3 history entries for default-mode direct finish, got %d", len(resp.History))
	}
	if resp.History[0].Content != "你好，帮我总结一下" {
		t.Fatalf("expected transcript as user content, got %#v", resp.History[0])
	}
	if len(resp.History[0].Attachments) != 1 || resp.History[0].Attachments[0].Transcript != "你好，帮我总结一下" {
		t.Fatalf("expected transcript on audio attachment, got %#v", resp.History[0].Attachments)
	}
	if _, ok := firstMessageOfType(resp.History, "role_output"); !ok {
		t.Fatalf("expected role output messages in history: %#v", resp.History)
	}
	assistant, ok := firstMessageOfType(resp.History, "assistant")
	if !ok || assistant.Content != "已处理" {
		t.Fatalf("unexpected assistant message: %#v", resp.History)
	}
}

func TestServerDeviceAudioRecordingEndpointsReturnWAVAttachment(t *testing.T) {
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	var readCount int32
	var startPlaybackCount int32
	var writePlayChunkCount int32
	var healthCount int32

	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_playback":
			atomic.AddInt32(&startPlaybackCount, 1)
			return audioResponse{Status: "OK", SessionID: stringUint64(7)}, nil
		case "write_play_chunk":
			atomic.AddInt32(&writePlayChunkCount, 1)
			return audioResponse{Status: "OK"}, nil
		case "health":
			atomic.AddInt32(&healthCount, 1)
			return audioResponse{
				Status:           "OK",
				RecordingActive:  false,
				PlaybackActive:   false,
				RecordSessions:   0,
				PlaybackSessions: 0,
			}, nil
		case "start_recording":
			if req.SampleRate != 16000 || req.Channels != 1 || req.BitWidth != 16 {
				t.Errorf("unexpected recording format: %#v", req)
			}
			return audioResponse{Status: "OK", SessionID: stringUint64(42)}, nil
		case "read_record_chunk":
			count := atomic.AddInt32(&readCount, 1)
			if count == 1 {
				return audioResponse{Status: "OK"}, []byte{1, 0, 2, 0}
			}
			<-stopCh
			return audioResponse{Status: "OK", EndOfStream: true}, nil
		case "stop_recording":
			stopOnce.Do(func() { close(stopCh) })
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})

	runtime := NewRuntimeWithDeps(
		Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
			},
		},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	startReq := httptest.NewRequest(http.MethodPost, "/api/audio/record/start", nil)
	startRec := httptest.NewRecorder()
	server.handleAudioRecordStart(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("unexpected start status: %d body=%s", startRec.Code, startRec.Body.String())
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&startPlaybackCount) > 0 &&
			atomic.LoadInt32(&writePlayChunkCount) > 0 &&
			atomic.LoadInt32(&healthCount) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&startPlaybackCount) == 0 ||
		atomic.LoadInt32(&writePlayChunkCount) == 0 ||
		atomic.LoadInt32(&healthCount) == 0 {
		t.Fatalf("expected prompt sound playback flow to call start_playback/write_play_chunk/health, got start=%d write=%d health=%d",
			startPlaybackCount, writePlayChunkCount, healthCount)
	}

	stopReq := httptest.NewRequest(http.MethodPost, "/api/audio/record/stop", nil)
	stopRec := httptest.NewRecorder()
	server.handleAudioRecordStop(stopRec, stopReq)
	if stopRec.Code != http.StatusOK {
		t.Fatalf("unexpected stop status: %d body=%s", stopRec.Code, stopRec.Body.String())
	}

	var resp AudioRecordStopResponse
	if err := json.NewDecoder(stopRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if resp.Attachment.Kind != AttachmentKindAudio || resp.Attachment.MIMEType != "audio/wav" {
		t.Fatalf("unexpected attachment metadata: %#v", resp.Attachment)
	}
	wavData, err := base64.StdEncoding.DecodeString(resp.Attachment.Data)
	if err != nil {
		t.Fatalf("decode wav payload: %v", err)
	}
	if !bytes.HasPrefix(wavData, []byte("RIFF")) || !bytes.Contains(wavData[:44], []byte("WAVE")) {
		t.Fatalf("expected wav payload, got %q", string(wavData[:12]))
	}
	if len(wavData) != 48 {
		t.Fatalf("expected 2 PCM16 samples in WAV, got %d bytes", len(wavData))
	}
}

func TestServerDeviceAudioRecordingStartRecoversStaleSession(t *testing.T) {
	stopCh := make(chan struct{})
	var stopOnce sync.Once

	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_playback":
			return audioResponse{Status: "OK", SessionID: stringUint64(7)}, nil
		case "write_play_chunk":
			return audioResponse{Status: "OK"}, nil
		case "health":
			return audioResponse{Status: "OK"}, nil
		case "start_recording":
			return audioResponse{Status: "OK", SessionID: stringUint64(42)}, nil
		case "read_record_chunk":
			select {
			case <-stopCh:
				return audioResponse{Status: "OK", EndOfStream: true}, nil
			default:
				return audioResponse{Status: "OK"}, []byte{1, 0}
			}
		case "stop_recording":
			stopOnce.Do(func() { close(stopCh) })
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})

	runtime := NewRuntimeWithDeps(
		Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{Socket: socketPath, SampleRate: 16000},
		},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")
	server.webRecording = &webAudioRecording{
		sessionID:  99,
		sampleRate: 16000,
		done:       make(chan struct{}),
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/audio/record/start", nil)
	startRec := httptest.NewRecorder()
	server.handleAudioRecordStart(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("unexpected start status after stale recovery: %d body=%s", startRec.Code, startRec.Body.String())
	}
	if server.webRecording == nil || server.webRecording.sessionID != 42 {
		t.Fatalf("webRecording = %#v, want new session 42", server.webRecording)
	}
}

func TestServerToolCatalogEndpoint(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	rec := httptest.NewRecorder()

	server.handleToolCatalog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ToolCatalogResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(resp.Tools))
	}
	if resp.Tools[0].Name != "shell" {
		t.Fatalf("unexpected tool descriptor: %#v", resp.Tools[0])
	}
	if resp.Tools[0].HTTP.Path != "/api/tools/shell" {
		t.Fatalf("unexpected tool path: %#v", resp.Tools[0].HTTP)
	}
	if resp.Tools[0].ExampleInput != `{"command":"pwd"}` {
		t.Fatalf("unexpected example input: %q", resp.Tools[0].ExampleInput)
	}
}

func TestServerToolInvokeEndpointAcceptsStructuredJSON(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      `{"status":"ok"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	body := bytes.NewBufferString(`{"input":{"command":"pwd"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/shell", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleToolInvoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != `{"command":"pwd"}` {
		t.Fatalf("unexpected tool input: %#v", tool.inputs)
	}

	var resp ToolInvokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Tool.Name != "shell" || resp.RawInput != `{"command":"pwd"}` || resp.Output != `{"status":"ok"}` || resp.IsError {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestServerToolInvokeUsesUnifiedExecutionAndNormalizesInput(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	body := bytes.NewBufferString(`{"raw_input":"{\"command\":\"pwd\"}\nObservation:"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/shell", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleToolInvoke(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != `{"command":"pwd"}` {
		t.Fatalf("unexpected tool input: %#v", tool.inputs)
	}

	var resp ToolInvokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RawInput != `{"command":"pwd"}` {
		t.Fatalf("raw input = %q, want normalized input", resp.RawInput)
	}
}

func TestServerMobileGymEpisodeStartEnd(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "control.token")
	if err := os.WriteFile(tokenPath, []byte("control-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Model:  ModelConfig{Provider: "fake"},
		Device: DeviceConfig{Backend: "mobilegym", ControlTokenFile: tokenPath},
	}
	runtime := NewRuntimeWithDeps(cfg, &testModelResolver{model: &scriptedModel{}}, NewMemoryManager(""), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}), NewSkillIndex())
	server := NewServer(runtime, "127.0.0.1:0", "")

	body := strings.NewReader(`{"episode_id":"ep1","bridge_url":"http://127.0.0.1:19001","bridge_token":"tok"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/mobilegym/episode/start", body)
	req.Header.Set("Authorization", "Bearer control-token")
	rec := httptest.NewRecorder()
	server.handleMobileGymEpisodeStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	if session, ok := runtime.mobileGym.Get(); !ok || session.EpisodeID != "ep1" {
		t.Fatalf("session = %#v ok=%v", session, ok)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/mobilegym/episode/end", strings.NewReader(`{"episode_id":"ep1"}`))
	req.Header.Set("Authorization", "Bearer control-token")
	rec = httptest.NewRecorder()
	server.handleMobileGymEpisodeEnd(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("end status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := runtime.mobileGym.Get(); ok {
		t.Fatal("session still active after end")
	}
}

func TestServerMobileGymEpisodeEndpointsRequireControlToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "control.token")
	if err := os.WriteFile(tokenPath, []byte("control-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Model:  ModelConfig{Provider: "fake"},
		Device: DeviceConfig{Backend: "mobilegym", ControlTokenFile: tokenPath},
	}
	runtime := NewRuntimeWithDeps(cfg, &testModelResolver{model: &scriptedModel{}}, NewMemoryManager(""), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}), NewSkillIndex())
	server := NewServer(runtime, "127.0.0.1:0", "")

	for _, tt := range []struct {
		name     string
		auth     string
		wantCode int
	}{
		{name: "missing", wantCode: http.StatusUnauthorized},
		{name: "wrong", auth: "Bearer wrong", wantCode: http.StatusUnauthorized},
		{name: "correct", auth: "Bearer control-token", wantCode: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/mobilegym/episode/start", strings.NewReader(`{"episode_id":"ep1","bridge_url":"http://127.0.0.1:19001","bridge_token":"tok"}`))
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			server.handleMobileGymEpisodeStart(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

func TestMobileGymControlTokenNotInToolCatalog(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "control.token")
	if err := os.WriteFile(tokenPath, []byte("control-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Model:  ModelConfig{Provider: "fake"},
		Device: DeviceConfig{Backend: "mobilegym", ControlTokenFile: tokenPath},
	}
	runtime := NewRuntimeWithDeps(cfg, &testModelResolver{model: &scriptedModel{}}, NewMemoryManager(""), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}), NewSkillIndex())

	catalogJSON, err := json.Marshal(runtime.ToolDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(catalogJSON), "control-token") {
		t.Fatalf("tool catalog contains control token: %s", catalogJSON)
	}
	for _, skill := range runtime.HTTPToolSkills("http://127.0.0.1:8080") {
		if strings.Contains(skill.Markdown, "control-token") {
			t.Fatalf("tool skill markdown contains control token: %s", skill.Markdown)
		}
	}
}

func TestServerDoesNotExposeActivateSkillOverHTTP(t *testing.T) {
	index := NewSkillIndex()
	index.skills["planner"] = &SkillDefinition{
		Name:         "planner",
		Description:  "Planning skill",
		Instructions: "Plan before acting.",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)
	server := NewServer(runtime, ":0", "")

	catalogReq := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	catalogRec := httptest.NewRecorder()
	server.handleToolCatalog(catalogRec, catalogReq)
	if catalogRec.Code != http.StatusOK {
		t.Fatalf("unexpected catalog status: %d body=%s", catalogRec.Code, catalogRec.Body.String())
	}
	if bytes.Contains(catalogRec.Body.Bytes(), []byte("activate_skill")) {
		t.Fatalf("activate_skill should not be advertised over HTTP: %s", catalogRec.Body.String())
	}

	body := bytes.NewBufferString(`{"input":"planner"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/activate_skill", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleToolInvoke(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected activate_skill HTTP invoke to be blocked, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServerDoesNotExposeSkillManageOverHTTP(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"skill_manage": NewSkillManageTool(t.TempDir(), ""),
			"skill_list":   NewSkillListTool(t.TempDir()),
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	catalogReq := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	catalogRec := httptest.NewRecorder()
	server.handleToolCatalog(catalogRec, catalogReq)
	if catalogRec.Code != http.StatusOK {
		t.Fatalf("unexpected catalog status: %d body=%s", catalogRec.Code, catalogRec.Body.String())
	}
	if bytes.Contains(catalogRec.Body.Bytes(), []byte("skill_manage")) {
		t.Fatalf("skill_manage should not be advertised over HTTP: %s", catalogRec.Body.String())
	}
	if !bytes.Contains(catalogRec.Body.Bytes(), []byte("skill_list")) {
		t.Fatalf("expected non-mutating skill tool to remain exposed: %s", catalogRec.Body.String())
	}

	invokeReq := httptest.NewRequest(http.MethodPost, "/api/tools/skill_manage", bytes.NewBufferString(`{"raw_input":"{}"}`))
	invokeRec := httptest.NewRecorder()
	server.handleToolInvoke(invokeRec, invokeReq)
	if invokeRec.Code != http.StatusNotFound {
		t.Fatalf("expected skill_manage HTTP invoke to be blocked, got %d body=%s", invokeRec.Code, invokeRec.Body.String())
	}
}

func TestServerToolSkillsEndpointReturnsGeneratedSkills(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0", "")

	req := httptest.NewRequest(http.MethodGet, "https://device.example/api/tool-skills", nil)
	req.Header.Set("X-Forwarded-Host", "203.0.113.57:8080")
	req.Header.Set("X-Forwarded-Proto", "http")
	rec := httptest.NewRecorder()

	server.handleToolSkills(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ToolSkillsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Skills) != 1 {
		t.Fatalf("expected exactly 1 generated skill, got %d", len(resp.Skills))
	}

	skill := resp.Skills[0]
	if skill.Name != "aiden-http-tool-suite" {
		t.Fatalf("unexpected skill name: %#v", skill)
	}
	if len(skill.ToolNames) != 1 || skill.ToolNames[0] != "shell" {
		t.Fatalf("unexpected tool list: %#v", skill.ToolNames)
	}
	if !bytes.Contains([]byte(skill.Markdown), []byte("/api/tools/{tool_name}")) {
		t.Fatalf("unexpected skill markdown: %q", skill.Markdown)
	}
	if !bytes.Contains([]byte(skill.Markdown), []byte("http://203.0.113.57:8080")) {
		t.Fatalf("expected forwarded base URL in markdown: %q", skill.Markdown)
	}
	if !bytes.Contains([]byte(skill.Markdown), []byte("NO_PROXY")) {
		t.Fatalf("expected proxy guidance in markdown: %q", skill.Markdown)
	}
}

func TestRequestBaseURLPrefersForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://local.invalid/api/tool-skills", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Header.Set("X-Forwarded-Host", "device.example:8443, proxy.local")

	got := requestBaseURL(req)
	if got != "https://device.example:8443" {
		t.Fatalf("requestBaseURL = %q, want %q", got, "https://device.example:8443")
	}
}

func TestWebUIRedactsScreenshotBase64Payloads(t *testing.T) {
	required := []string{
		"redactToolPayloadForDisplay(JSON.parse(value))",
		"clone.data = '[base64 screenshot omitted: ' + byteLabel + ']'",
		"function isScreenshotPayload(value)",
	}
	for _, snippet := range required {
		if !strings.Contains(webUI, snippet) {
			t.Fatalf("webUI missing screenshot redaction snippet %q", snippet)
		}
	}
	if strings.Contains(webUI, "toolName !== 'screenshot'") {
		t.Fatalf("webUI still limits screenshot parsing to only the screenshot tool")
	}
}

func startFakeAudioServiceSocket(t *testing.T, handler func(audioRequest) (audioResponse, []byte)) string {
	t.Helper()

	socketDir, err := os.MkdirTemp("/tmp", "aiden-audio-test-*")
	if err != nil {
		t.Fatalf("create fake audio socket dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(socketDir)
	})

	socketPath := filepath.Join(socketDir, "audio.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on fake audio socket: %v", err)
	}
	t.Cleanup(func() {
		listener.Close()
	})

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()

				msg, err := readUdsMessage(conn)
				if err != nil {
					t.Errorf("fake audio read request: %v", err)
					return
				}

				var req audioRequest
				if err := json.Unmarshal([]byte(msg.HeaderJSON), &req); err != nil {
					t.Errorf("fake audio decode request: %v", err)
					return
				}

				resp, payload := handler(req)
				respHeader, err := json.Marshal(resp)
				if err != nil {
					t.Errorf("fake audio encode response: %v", err)
					return
				}
				if err := writeUdsMessage(conn, udsMessage{HeaderJSON: string(respHeader), Payload: payload}); err != nil {
					t.Errorf("fake audio write response: %v", err)
					return
				}
			}()
		}
	}()

	return socketPath
}

func TestMessageJSONWithVoiceFields(t *testing.T) {
	msg := Message{
		Type:            "user",
		Content:         "Hello",
		Timestamp:       time.Now(),
		Source:          "voice",
		AudioFile:       "/userdata/audio/msg_123.wav",
		AudioDurationMs: 2500,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Source != "voice" {
		t.Errorf("Source: got %q, want %q", decoded.Source, "voice")
	}
	if decoded.AudioFile != "/userdata/audio/msg_123.wav" {
		t.Errorf("AudioFile: got %q, want %q", decoded.AudioFile, "/userdata/audio/msg_123.wav")
	}
	if decoded.AudioDurationMs != 2500 {
		t.Errorf("AudioDurationMs: got %d, want %d", decoded.AudioDurationMs, 2500)
	}
}

func TestMessageJSONOmitsEmptyVoiceFields(t *testing.T) {
	msg := Message{
		Type:      "user",
		Content:   "Hello",
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}

	if _, exists := raw["source"]; exists {
		t.Errorf("source field should be omitted when empty")
	}
	if _, exists := raw["audio_file"]; exists {
		t.Errorf("audio_file field should be omitted when empty")
	}
	if _, exists := raw["audio_duration_ms"]; exists {
		t.Errorf("audio_duration_ms field should be omitted when zero")
	}
}

func TestServerHandleEventsSSE(t *testing.T) {
	// Create minimal server
	cfg := Config{ConfigDir: t.TempDir()}
	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	server := NewServer(runtime, "127.0.0.1:0", "")

	// Create request
	req := httptest.NewRequest("GET", "/api/events", nil)
	rec := httptest.NewRecorder()

	// Start handler in goroutine (it blocks on SSE stream)
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleEvents(rec, req)
	}()

	// Give handler time to set headers
	time.Sleep(50 * time.Millisecond)

	// Verify SSE headers
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want %q", ct, "text/event-stream")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q, want %q", cc, "no-cache")
	}
	if conn := rec.Header().Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection: got %q, want %q", conn, "keep-alive")
	}

	// Broadcast a test message
	server.eventBroadcaster.Broadcast(Message{
		Type:    "user",
		Content: "SSE test",
	})

	time.Sleep(50 * time.Millisecond)

	// Verify message was sent
	body := rec.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("Response should contain SSE data, got: %s", body)
	}
	if !strings.Contains(body, "SSE test") {
		t.Errorf("Response should contain message content, got: %s", body)
	}
}

func TestServerHistoryStoreBroadcastsToSSE(t *testing.T) {
	cfg := Config{ConfigDir: t.TempDir()}
	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	server := NewServer(runtime, "127.0.0.1:0", "")

	// Subscribe to SSE
	ch := server.eventBroadcaster.Subscribe()
	defer server.eventBroadcaster.Unsubscribe(ch)

	// Append message to history store
	testMsg := Message{
		Type:    "user",
		Content: "broadcast test",
	}
	if err := server.historyStore.Append(context.Background(), testMsg); err != nil {
		t.Fatal(err)
	}

	// Verify broadcast received
	select {
	case received := <-ch:
		if received.Content != "broadcast test" {
			t.Errorf("got content %q, want %q", received.Content, "broadcast test")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for broadcast")
	}
}

func TestServerHandleAudioFileServes(t *testing.T) {
	tmpDir := t.TempDir()
	audioDir := filepath.Join(tmpDir, "audio")
	if err := os.MkdirAll(audioDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test WAV file
	testFile := filepath.Join(audioDir, "msg_123.wav")
	if err := os.WriteFile(testFile, []byte("WAV content"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		ConfigDir: tmpDir,
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			StoragePath: audioDir,
		},
	}
	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	server := NewServer(runtime, "127.0.0.1:0", "")

	req := httptest.NewRequest("GET", "/api/audio/msg_123.wav", nil)
	rec := httptest.NewRecorder()
	server.handleAudioFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type: got %q, want %q", ct, "audio/wav")
	}
	if body := rec.Body.String(); body != "WAV content" {
		t.Errorf("Body: got %q, want %q", body, "WAV content")
	}
}

func TestServerHandleAudioFilePathTraversalBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	audioDir := filepath.Join(tmpDir, "audio")
	if err := os.MkdirAll(audioDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create file outside audio dir
	secretFile := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		ConfigDir: tmpDir,
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			StoragePath: audioDir,
		},
	}
	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	server := NewServer(runtime, "127.0.0.1:0", "")

	// Attempt path traversal
	req := httptest.NewRequest("GET", "/api/audio/../secret.txt", nil)
	rec := httptest.NewRecorder()
	server.handleAudioFile(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Path traversal: got status %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestServerHandleAudioFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	audioDir := filepath.Join(tmpDir, "audio")
	if err := os.MkdirAll(audioDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		ConfigDir: tmpDir,
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			StoragePath: audioDir,
		},
	}
	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	server := NewServer(runtime, "127.0.0.1:0", "")

	req := httptest.NewRequest("GET", "/api/audio/nonexistent.wav", nil)
	rec := httptest.NewRecorder()
	server.handleAudioFile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Not found: got status %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServerHandleAudioFileSymlinkBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	audioDir := filepath.Join(tmpDir, "audio")
	if err := os.MkdirAll(audioDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create file outside audio dir
	secretFile := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("secret data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink in audio dir pointing to secret
	symlinkPath := filepath.Join(audioDir, "msg_symlink.wav")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Skip("Symlink not supported:", err)
	}

	cfg := Config{
		ConfigDir: tmpDir,
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			StoragePath: audioDir,
		},
	}
	runtime, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	server := NewServer(runtime, "127.0.0.1:0", "")

	req := httptest.NewRequest("GET", "/api/audio/msg_symlink.wav", nil)
	rec := httptest.NewRecorder()
	server.handleAudioFile(rec, req)

	// Should return 403 Forbidden
	if rec.Code != http.StatusForbidden {
		t.Errorf("Symlink attack: got status %d, want %d (body: %s)", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	// Verify secret not leaked
	body := rec.Body.String()
	if strings.Contains(body, "secret data") {
		t.Error("Symlink attack succeeded - secret data leaked")
	}
}
