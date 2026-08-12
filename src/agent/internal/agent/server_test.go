package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/screen"
	speechtext "aiden-agent/internal/agent/speech"
	ttsmodule "aiden-agent/internal/agent/tts"
)

type stubSTTClient struct {
	transcript         string
	transcribeErr      error
	inputs             [][]byte
	supportsStreaming  bool
	streamConfigs      []STTStreamConfig
	streamUploader     *stubSTTStreamUploader
	streamUploaderErr  error
	streamUploaderUsed int
}

type stubSTTStreamUploader struct {
	transcript  string
	finalizeErr error
	closeErr    error
	writes      [][]byte
	finalized   bool
	closed      bool
}

type mappingStateMutatingTool struct {
	name        string
	description string
	output      string
	screen      *screen.ScreenState
	inputs      []string
}

func (t *mappingStateMutatingTool) Name() string { return t.name }

func (t *mappingStateMutatingTool) Description() string { return t.description }

func (t *mappingStateMutatingTool) Call(_ context.Context, input string) (string, error) {
	t.inputs = append(t.inputs, input)
	if t.screen != nil {
		t.screen.UpdateActiveArea(320, 240, screen.ScreenActiveArea{})
	}
	return t.output, nil
}

func (s *stubSTTClient) TranscribeWAV(wavData []byte) (string, error) {
	s.inputs = append(s.inputs, append([]byte(nil), wavData...))
	if s.transcribeErr != nil {
		return "", s.transcribeErr
	}
	return s.transcript, nil
}

func (s *stubSTTClient) Capabilities() STTCapabilities {
	return STTCapabilities{SupportsStreamingUpload: s.supportsStreaming}
}

func (s *stubSTTClient) NewStreamingUploader(_ context.Context, cfg STTStreamConfig) (STTStreamUploader, error) {
	s.streamConfigs = append(s.streamConfigs, cfg)
	if !s.supportsStreaming {
		return nil, fmt.Errorf("streaming upload is not supported")
	}
	if s.streamUploaderErr != nil {
		return nil, s.streamUploaderErr
	}
	if s.streamUploader == nil {
		s.streamUploader = &stubSTTStreamUploader{}
	}
	s.streamUploaderUsed++
	return s.streamUploader, nil
}

func TestStubSTTClientNewStreamingUploaderRequiresStreamingCapability(t *testing.T) {
	client := &stubSTTClient{}

	_, err := client.NewStreamingUploader(context.Background(), STTStreamConfig{SampleRate: 16000, Channels: 1, BitWidth: 16})
	if err == nil {
		t.Fatal("expected streaming capability error")
	}
}

func (s *stubSTTStreamUploader) UploadPCM(pcm []byte) error {
	s.writes = append(s.writes, append([]byte(nil), pcm...))
	return nil
}

func (s *stubSTTStreamUploader) Finalize() (string, error) {
	s.finalized = true
	if s.finalizeErr != nil {
		return "", s.finalizeErr
	}
	return s.transcript, nil
}

func (s *stubSTTStreamUploader) Close() error {
	s.closed = true
	return s.closeErr
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
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, "Let me read the current volume."),
			contentResponse("The current audio volume is 42."),
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)
	defer runtime.Close()
	server := newServerForTest(runtime)

	body := bytes.NewBufferString(`{"message":"What is the current volume?"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}

	// Poll for result
	var resp ChatResultResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code != http.StatusOK {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err := json.NewDecoder(resultRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode result response: %v", err)
		}
		if resp.Status == "complete" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if resp.Status != "complete" {
		t.Fatalf("result never completed: status=%q", resp.Status)
	}
	if resp.Response != "The current audio volume is 42." {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) < 4 {
		t.Fatalf("expected at least 4 history entries for single-agent tool flow, got %d", len(resp.History))
	}

	if resp.History[0].Type != "user" || resp.History[0].Content != "What is the current volume?" {
		t.Fatalf("unexpected first history message: %#v", resp.History[0])
	}
	toolCall, ok := firstMessageOfType(resp.History, runEventToolCall)
	if !ok || toolCall.ToolName != "audio_volume" || toolCall.ToolInput != "{}" {
		t.Fatalf("unexpected tool_call message: %#v", resp.History)
	}
	if toolCall.Content != "Let me read the current volume." {
		t.Fatalf("unexpected tool_call content: %#v", toolCall)
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

func TestServerPublicHistoryOmitsLargeToolResultContent(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "shell", `{"command":"read config.py"}`),
			contentResponse("Done"),
		},
	}
	tool := &stubTool{
		name:        "shell",
		description: "Run a shell command.",
		output:      strings.Repeat(" ", 4001),
	}
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when requested.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"shell": tool}},
		NewSkillIndex(),
	)
	defer runtime.Close()
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"Read the large config"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", rec.Code, rec.Body.String())
	}
	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	var result ChatResultResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+startResp["request_id"], nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code == http.StatusOK {
			if err := json.NewDecoder(resultRec.Body).Decode(&result); err != nil {
				t.Fatalf("decode result response: %v", err)
			}
			if result.Status == "complete" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.Status != "complete" {
		t.Fatalf("result never completed: status=%q", result.Status)
	}

	const want = "[Large tool result omitted from public history (4001 chars)]"
	resultToolMessage, ok := firstMessageOfType(result.History, "tool_result")
	if !ok || resultToolMessage.Content != want {
		t.Fatalf("/api/chat/result tool result = %#v, want content %q", resultToolMessage, want)
	}

	reloaded := newServerForTest(runtime)
	historyReq := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	historyRec := httptest.NewRecorder()
	reloaded.handleHistory(historyRec, historyReq)
	if historyRec.Code != http.StatusOK {
		t.Fatalf("history status = %d body=%s", historyRec.Code, historyRec.Body.String())
	}
	var history []Message
	if err := json.NewDecoder(historyRec.Body).Decode(&history); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	historyToolMessage, ok := firstMessageOfType(history, "tool_result")
	if !ok || historyToolMessage.Content != want {
		t.Fatalf("/api/history tool result = %#v, want content %q", historyToolMessage, want)
	}
}

func TestServerPersistsChatHistoryWithEpisodeReference(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	memoryDir := filepath.Join(configDir, "memory")
	model := &scriptedModel{
		responses: roleDirectResponses("Completed"),
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
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"Do a task"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}

	// Poll for result
	var resp ChatResultResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code != http.StatusOK {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err := json.NewDecoder(resultRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode result response: %v", err)
		}
		if resp.Status == "complete" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if resp.Status != "complete" {
		t.Fatalf("result never completed: status=%q", resp.Status)
	}
	assistant, ok := firstMessageOfType(resp.History, "assistant")
	if !ok || assistant.EpisodeID == "" {
		t.Fatalf("assistant missing episode reference: %#v", resp.History)
	}
	if resp.History[0].EpisodeID != assistant.EpisodeID {
		t.Fatalf("user and assistant episode ids differ: %#v", resp.History)
	}

	reloaded := newServerForTest(runtime)
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

func TestServerRestoresSessionEventsUsingEventType(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	session := NewSessionMemoryStore(filepath.Join(configDir, "memory", "session"))
	now := time.Now().UTC()
	events := []SessionEvent{
		{
			EventID:   "evt_user",
			Ts:        now.Format(time.RFC3339Nano),
			Type:      "user_input",
			Role:      "user",
			EpisodeID: "ep_restore",
			RequestID: "req_restore",
			Content:   "换头",
		},
		{
			EventID:   "evt_role",
			Ts:        now.Add(time.Second).Format(time.RFC3339Nano),
			Type:      "role_output",
			Role:      "planner",
			EpisodeID: "ep_restore",
			RequestID: "req_restore",
			Content:   `{"can_finish":false,"needs_human_handoff":true}`,
		},
		{
			EventID:   "evt_tool",
			Ts:        now.Add(2 * time.Second).Format(time.RFC3339Nano),
			Type:      runEventToolCall,
			Role:      "assistant",
			EpisodeID: "ep_restore",
			RequestID: "req_restore",
			ToolName:  "screenshot",
			ToolInput: "{}",
			ToolError: NewToolErrorWithDetails(CodeToolExecutionFailed, "camera unavailable", map[string]any{"device": "video0"}),
			Artifacts: []InputArtifact{{
				Kind:     AttachmentKindImage,
				Name:     "screen.jpg",
				MIMEType: "image/jpeg",
				Path:     "/userdata/agent/artifacts/screen.jpg",
				Size:     1234,
				Data:     []byte("binary-image-data"),
			}},
			Content: "tool_call: screenshot input={}",
		},
		{
			EventID:   "evt_unknown_planner",
			Ts:        now.Add(3 * time.Second).Format(time.RFC3339Nano),
			Type:      "planner_decision",
			Role:      "planner",
			EpisodeID: "ep_restore",
			RequestID: "req_restore",
			Content:   `{"mode":"simple"}`,
		},
		{
			EventID:   "evt_assistant",
			Ts:        now.Add(4 * time.Second).Format(time.RFC3339Nano),
			Type:      "assistant_output",
			Role:      "assistant",
			EpisodeID: "ep_restore",
			RequestID: "req_restore",
			Content:   "请明确说明您想更换的是聊天对象的头像，还是其他内容。",
		},
	}
	for _, event := range events {
		if _, err := session.AppendEvent(context.Background(), event); err != nil {
			t.Fatalf("AppendEvent(%s) error: %v", event.EventID, err)
		}
	}

	server := &Server{logger: newTestLogger(), runtime: &Runtime{config: Config{ConfigDir: configDir}}}
	server.loadHistoryFromDisk()
	history := server.historySnapshot()
	if len(history) != 3 {
		t.Fatalf("restored history entries = %d, want 3 public messages: %#v", len(history), history)
	}

	if history[0].Type != "user" || history[0].Content != "换头" {
		t.Fatalf("user_input was not restored as user message: %#v", history[0])
	}
	if history[1].Type != runEventToolCall || history[1].ToolName != "screenshot" || history[1].ToolInput != "{}" {
		t.Fatalf("tool_call metadata not restored: %#v", history[1])
	}
	if history[1].ToolError == nil || history[1].ToolError.Code != CodeToolExecutionFailed || history[1].ToolError.Details["device"] != "video0" {
		t.Fatalf("tool_call structured error not restored: %#v", history[1].ToolError)
	}
	if len(history[1].Artifacts) != 1 || history[1].Artifacts[0].Path != "/userdata/agent/artifacts/screen.jpg" || history[1].Artifacts[0].Data != nil {
		t.Fatalf("tool_call artifacts not restored safely: %#v", history[1].Artifacts)
	}
	if history[2].Type != "assistant" || history[2].Content == "" {
		t.Fatalf("assistant_output was not restored as assistant message: %#v", history[2])
	}

	userCount := 0
	for _, msg := range history {
		if msg.Type == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("restored user message count = %d, want only the original user input: %#v", userCount, history)
	}
}

func TestServerHandleChatStreamsToolAndAssistantMessages(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"Let me read the current volume."}`, "The current audio volume is 42."),
	}
	configDir, err := os.MkdirTemp("", "aiden-server-chat-stream-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	configDir = ensureTestConfigDir(t, configDir)
	t.Cleanup(func() { _ = os.RemoveAll(configDir) })
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:   configDir,
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
	defer runtime.Close()
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"What is the current volume?"}`))
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

	var sawToolCall, sawToolResult, sawAssistant, sawDone bool
	for _, event := range events {
		if event.Type == "message" && event.Message != nil {
			switch event.Message.Type {
			case runEventToolCall:
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
	if !sawToolCall || !sawToolResult || !sawAssistant || !sawDone {
		t.Fatalf("missing expected stream events: tool_call=%v tool_result=%v assistant=%v done=%v events=%#v",
			sawToolCall, sawToolResult, sawAssistant, sawDone, events)
	}
}

func TestServerHandleChatStreamsLeadingToolSpeechWithoutDuplicatePlayback(t *testing.T) {
	const requestID = "streaming-tts-cleanup"
	toolSpeech := true
	toolContent := "<tts>Check volume.</tts>\nChecking volume."
	finalContent := "<tts>Current volume is 42.</tts>\nCurrent volume is 42."
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, toolContent),
			contentResponse(finalContent),
		},
		streamChunks: [][]string{
			{toolContent},
			{finalContent},
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: boolPtr(true),
			VoiceToolCallSpeech:      &toolSpeech,
			Audio:                    AudioConfig{SampleRate: 16000},
		}),
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
	defer runtime.Close()
	server := newServerForTest(runtime)
	provider := &flushRecordingTTSProvider{name: "server-provider"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient(startTTSPlaybackAudioSocket(t))
	time.Sleep(30 * time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"What is the current volume?","request_id":"`+requestID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("X-Aiden-Stream", "ndjson")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	waitForFlushProviderTextCount(t, provider, 2)
	time.Sleep(100 * time.Millisecond)
	texts := provider.texts()
	if len(texts) != 2 {
		writes, flushCalls := provider.activity()
		t.Fatalf("tool-event and final TTS should each play once, got %#v writes=%#v flush_calls=%d", texts, writes, flushCalls)
	}
	if countString(texts, "Check volume.") != 1 {
		t.Fatalf("tool TTS count = %d, want 1: %#v", countString(texts, "Check volume."), texts)
	}
	if countString(texts, "Current volume is 42.") != 1 {
		t.Fatalf("final TTS count = %d, want 1: %#v", countString(texts, "Current volume is 42."), texts)
	}
	if texts[0] != "Check volume." || texts[1] != "Current volume is 42." {
		t.Fatalf("TTS playback order = %#v, want tool progress before final response", texts)
	}
	if outputs := server.snapshotActiveOutputs(requestID); len(outputs) != 0 {
		t.Fatalf("active TTS outputs after streaming response = %d, want 0", len(outputs))
	}
}

func TestServerAsyncChatUnregistersStreamingTTSOutput(t *testing.T) {
	const requestID = "async-tts-cleanup"
	output := "<tts>Done.</tts>\nDone."
	model := &scriptedModel{
		responses:    roleDirectResponses(output),
		streamChunks: [][]string{{output}},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Answer directly.",
			VoiceStreamingTTSEnabled: boolPtr(true),
			Audio:                    AudioConfig{SampleRate: 16000},
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	defer runtime.Close()
	server := newServerForTest(runtime)
	server.ttsManager = ttsmodule.NewProviderManager(&recordingTTSProvider{name: "server-provider"}, nil)
	server.audioClient = NewAudioServiceClient(startTTSPlaybackAudioSocket(t))

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"finish it","request_id":"`+requestID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleChat status = %d body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code == http.StatusOK {
			var result ChatResultResponse
			if err := json.NewDecoder(resultRec.Body).Decode(&result); err == nil && result.Status == "complete" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitForServerRequestFinished(t, server, requestID)
	if outputs := server.snapshotActiveOutputs(requestID); len(outputs) != 0 {
		t.Fatalf("active TTS outputs after async response = %d, want 0", len(outputs))
	}
}

type failingStreamWriter struct {
	err error
}

func (w failingStreamWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestStreamFanoutWriterReturnsInputLengthWhenLaterWriterFails(t *testing.T) {
	writeErr := errors.New("fanout write failed")
	var first strings.Builder
	fanout := newStreamFanoutWriter(&first, failingStreamWriter{err: writeErr})

	n, err := fanout.Write([]byte("chunk"))

	if !errors.Is(err, writeErr) {
		t.Fatalf("Write() error = %v, want %v", err, writeErr)
	}
	if n != len("chunk") {
		t.Fatalf("Write() bytes = %d, want %d", n, len("chunk"))
	}
	if first.String() != "chunk" {
		t.Fatalf("first writer received %q, want chunk", first.String())
	}
}

func TestStreamFanoutWriterResetsAssistantDeltaDraftBeforeFallback(t *testing.T) {
	writeErr := errors.New("fanout write failed")
	rec := httptest.NewRecorder()
	stream, ok := newChatStreamWriter(rec)
	if !ok {
		t.Fatal("httptest recorder must support streaming")
	}
	fanout := newStreamFanoutWriter(
		newChatAssistantStreamWriter(stream, "episode-reset", "request-reset"),
		failingStreamWriter{err: writeErr},
	)

	if _, err := fanout.Write([]byte("Partial")); !errors.Is(err, writeErr) {
		t.Fatalf("first Write() error = %v, want %v", err, writeErr)
	}
	resetStreamWriterState(fanout)
	if _, err := fanout.Write([]byte("Complete answer.")); !errors.Is(err, writeErr) {
		t.Fatalf("fallback Write() error = %v, want %v", err, writeErr)
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

	resetIndex := -1
	for i, event := range events {
		if event.Type == "assistant_delta_reset" {
			if resetIndex >= 0 {
				t.Fatalf("expected one assistant_delta_reset, events=%#v", events)
			}
			resetIndex = i
		}
	}
	if resetIndex <= 0 || resetIndex >= len(events)-1 {
		t.Fatalf("assistant_delta_reset index = %d, want between deltas: %#v", resetIndex, events)
	}

	var beforeReset strings.Builder
	var afterReset strings.Builder
	for i, event := range events {
		if event.Type != "assistant_delta" {
			continue
		}
		if i < resetIndex {
			beforeReset.WriteString(event.Delta)
		} else {
			afterReset.WriteString(event.Delta)
		}
	}
	if beforeReset.String() != "Partial" {
		t.Fatalf("delta before reset = %q, want Partial", beforeReset.String())
	}
	if afterReset.String() != "Complete answer." {
		t.Fatalf("delta after reset = %q, want Complete answer.", afterReset.String())
	}
}

func TestStreamFanoutWriterReportsAnyChildEmission(t *testing.T) {
	var webDelta strings.Builder
	var speech strings.Builder

	fanout := newStreamFanoutWriter(
		&webDelta,
		speechtext.NewStreamWriter(&speech),
	)
	tracker, ok := fanout.(streamOutputTracker)
	if !ok {
		t.Fatal("fanout writer must track stream emission")
	}

	if _, err := fanout.Write([]byte("<tts>Short answer.</tts>\nComplete answer.")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if webDelta.String() != "<tts>Short answer.</tts>\nComplete answer." {
		t.Fatalf("web delta stream = %q", webDelta.String())
	}
	if speech.String() != "Short answer." {
		t.Fatalf("speech stream = %q, want Short answer.", speech.String())
	}
	if !tracker.StreamEmitted() {
		t.Fatal("fanout should report emitted when any child stream emitted")
	}
}

func TestServerEventStreamAllowsRunEventMessages(t *testing.T) {
	for _, messageType := range []string{
		"user",
		"assistant",
		runEventToolCall,
		"tool_result",
	} {
		if !shouldStreamEventMessage(Message{Type: messageType}) {
			t.Fatalf("message type %q should be streamed to web clients", messageType)
		}
	}
	for _, messageType := range []string{"role_output", "episode_status", "todo_update", "todo_closed"} {
		if shouldStreamEventMessage(Message{Type: messageType}) {
			t.Fatalf("message type %q should not be streamed to web clients", messageType)
		}
	}
}

func TestHandleCoordinateDebugTap(t *testing.T) {
	currentScreen := &screen.ScreenState{}
	currentScreen.UpdateActiveArea(1280, 720, screen.ScreenActiveArea{X: 0, Y: 72, Width: 1280, Height: 576, Valid: true})
	tool := &mappingStateMutatingTool{
		name:        "touch_gesture",
		description: "Touch gesture tool.",
		output:      `{"width":1280,"height":576,"format":"jpeg","size":4,"data":"ZmFrZQ==","action_output":"ok"}`,
		screen:      currentScreen,
	}
	toolSet := &ToolSet{
		tools: map[string]langtools.Tool{
			"touch_gesture": tool,
		},
		screen: currentScreen,
	}
	toolSet.screen.UpdatePhoneScreenInfo(screen.PhoneScreenInfo{
		NativeWidthPixels:  intPtr(1179),
		NativeHeightPixels: intPtr(2556),
	})
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
		}),
		&testModelResolver{},
		NewMemoryManager(""),
		toolSet,
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/coordinate-debug/tap", bytes.NewBufferString(`{"x":123,"y":456,"type":"double_tap"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleCoordinateDebugTap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if len(tool.inputs) != 1 {
		t.Fatalf("touch_gesture call count = %d, want 1", len(tool.inputs))
	}

	var input map[string]any
	if err := json.Unmarshal([]byte(tool.inputs[0]), &input); err != nil {
		t.Fatalf("decode tool input: %v", err)
	}
	if got := input["type"]; got != "double_tap" {
		t.Fatalf("gesture type = %#v, want double_tap", got)
	}
	if got := input["coord_space"]; got != "normalized" {
		t.Fatalf("coord_space = %#v, want normalized", got)
	}
	point, ok := input["point"].(map[string]any)
	if !ok {
		t.Fatalf("point missing or invalid: %#v", input["point"])
	}
	if point["x"] != float64(123) || point["y"] != float64(456) {
		t.Fatalf("point = %#v, want x=123 y=456", point)
	}

	var resp coordinateDebugTapResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.ActionType != "double_tap" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if resp.Screenshot == nil || resp.Screenshot.Width != 1280 || resp.Screenshot.Height != 576 || resp.Screenshot.Data != "ZmFrZQ==" {
		t.Fatalf("unexpected screenshot payload: %#v", resp.Screenshot)
	}
	if resp.Screenshot.SourceWidth != 1280 || resp.Screenshot.SourceHeight != 720 {
		t.Fatalf("unexpected screenshot source dimensions: %#v", resp.Screenshot)
	}
	if resp.Screenshot.SourceActiveArea == nil || *resp.Screenshot.SourceActiveArea != (screen.ScreenActiveArea{X: 0, Y: 72, Width: 1280, Height: 576, Valid: true}) {
		t.Fatalf("unexpected screenshot active area: %#v", resp.Screenshot.SourceActiveArea)
	}
	if resp.Screenshot.OriginalScreenWidthPixels == nil || *resp.Screenshot.OriginalScreenWidthPixels != 1179 {
		t.Fatalf("unexpected original screen width: %#v", resp.Screenshot.OriginalScreenWidthPixels)
	}
	if resp.Screenshot.OriginalScreenHeightPixels == nil || *resp.Screenshot.OriginalScreenHeightPixels != 2556 {
		t.Fatalf("unexpected original screen height: %#v", resp.Screenshot.OriginalScreenHeightPixels)
	}
	width, height, active, _, ok := toolSet.screen.ActiveAreaWithAge()
	if !ok || width != 1280 || height != 720 {
		t.Fatalf("screen state dimensions = %dx%d ok=%v, want 1280x720 true", width, height, ok)
	}
	if active != (screen.ScreenActiveArea{X: 0, Y: 72, Width: 1280, Height: 576, Valid: true}) {
		t.Fatalf("screen state active area = %+v", active)
	}
}

func TestHandleCoordinateDebugTapMapsStructuredToolErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolErr    *ToolError
		wantStatus int
	}{
		{name: "invalid input", toolErr: NewToolError(CodeInvalidArguments, "bad tap"), wantStatus: http.StatusBadRequest},
		{name: "module unavailable", toolErr: NewToolError(CodeModuleUnavailable, "hid unavailable"), wantStatus: http.StatusServiceUnavailable},
		{name: "execution failed", toolErr: NewToolError(CodeToolExecutionFailed, "hid write failed"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toolSet := &ToolSet{tools: map[string]langtools.Tool{
				"touch_gesture": &contextToolErrorStub{name: "touch_gesture", toolErr: tc.toolErr},
			}}
			runtime := NewRuntimeWithDeps(
				withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
				&testModelResolver{},
				NewMemoryManager(""),
				toolSet,
				NewSkillIndex(),
			)
			server := newServerForTest(runtime)

			req := httptest.NewRequest(http.MethodPost, "/api/coordinate-debug/tap", bytes.NewBufferString(`{"x":123,"y":456}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.handleCoordinateDebugTap(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tc.toolErr.Message) {
				t.Fatalf("body = %s, want message %q", rec.Body.String(), tc.toolErr.Message)
			}
		})
	}
}

func TestServerHandleChatStreamTagsHistoryWithRequestID(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("Hello!"),
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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
	state := server.liveActivity.Snapshot("web-req-1")
	if state == nil || state.Status != LiveActivityStatusCompleted {
		t.Fatalf("stream live activity state = %#v, want completed", state)
	}
}

func TestHandleCoordinateDebugTapRejectsInvalidType(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/coordinate-debug/tap", bytes.NewBufferString(`{"x":100,"y":200,"type":"swipe"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleCoordinateDebugTap(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, fmt.Sprintf("%q", "unsupported tap type")) {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestHandleCoordinateDebugTapRejectsNonJSONContentType(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/coordinate-debug/tap", bytes.NewBufferString(`{"x":100,"y":200,"type":"tap"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	server.handleCoordinateDebugTap(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, fmt.Sprintf("%q", "Content-Type must be application/json")) {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestHandleScreenshotJPEGCanDisableBlackBarCropping(t *testing.T) {
	frameSocket := startFakeFrameServiceSocket(t, func(req map[string]any) (string, []byte) {
		method, _ := req["method"].(string)
		if method == "health" {
			return `{"type":"response","method":"health","status":"OK","state":"RUNNING","latest_seq":1,"frame_age_ms":10}`, nil
		}
		if method != "latest_frame" {
			t.Fatalf("unexpected method: %#v", req["method"])
		}
		if format, _ := req["format"].(string); format != "raw" {
			t.Fatalf("expected raw format request when crop_black_bars=false, got %#v", req["format"])
		}
		header := `{"type":"response","method":"latest_frame","status":"OK","frame":{"seq":1,"width":2,"height":1,"pixel_format":"uyvy","stride":4,"bytes":4,"stale":false}}`
		return header, []byte{16, 128, 235, 128}
	})

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			HID:   HIDConfig{FrameSocket: frameSocket},
		}),
		&testModelResolver{},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodGet, "/api/screenshot.jpg?crop_black_bars=false", nil)
	rec := httptest.NewRecorder()

	server.handleScreenshotJPEG(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Fatalf("content-type = %q, want image/jpeg", got)
	}
	if got := rec.Header().Get("X-Frame-Width"); got != "2" {
		t.Fatalf("X-Frame-Width = %q, want 2", got)
	}
	if got := rec.Header().Get("X-Frame-Height"); got != "1" {
		t.Fatalf("X-Frame-Height = %q, want 1", got)
	}

	img, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode response jpeg: %v", err)
	}
	if bounds := img.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 1 {
		t.Fatalf("decoded bounds = %v, want 2x1", bounds)
	}
}

func TestHandleScreenshotJPEGUpdatesSharedScreenStateFromPhoneAspectRatio(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := 0; y < 9; y++ {
		for x := 5; x < 10; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 240, B: 240, A: 255})
		}
	}
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encode jpeg fixture: %v", err)
	}
	jpegData := jpegBuf.Bytes()

	frameSocket := startFakeFrameServiceSocket(t, func(req map[string]any) (string, []byte) {
		if method, _ := req["method"].(string); method == "health" {
			return `{"type":"response","method":"health","status":"OK","state":"RUNNING","latest_seq":1,"frame_age_ms":10}`, nil
		}
		if format, _ := req["format"].(string); format != "jpeg" {
			t.Fatalf("expected jpeg format request, got %#v", req["format"])
		}
		if minimalWidth, _ := req["minimal_width"].(float64); minimalWidth != 608 {
			t.Fatalf("minimal_width = %#v, want 608", req["minimal_width"])
		}
		header := fmt.Sprintf(`{"type":"response","method":"latest_frame","status":"OK","frame":{"seq":1,"width":5,"height":9,"source_width":16,"source_height":9,"crop_x":5,"crop_y":0,"crop_width":5,"crop_height":9,"pixel_format":"jpeg","stride":0,"bytes":%d,"stale":false}}`, len(jpegData))
		return header, jpegData
	})

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			HID:   HIDConfig{FrameSocket: frameSocket},
		}),
		&testModelResolver{},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{FrameSocket: frameSocket}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	envData, err := json.Marshal(PhoneEnvironment{
		Platform: "android",
		Screen: screen.PhoneScreenInfo{
			WidthPixels:        intPtr(1080),
			HeightPixels:       intPtr(1920),
			NativeWidthPixels:  intPtr(1080),
			NativeHeightPixels: intPtr(2400),
		},
	})
	if err != nil {
		t.Fatalf("marshal environment: %v", err)
	}
	if !server.bridge.handleAppEvent(BridgeCommandResponse{
		ID:     "phone_environment",
		Method: "phone_environment",
		Data:   envData,
	}) {
		t.Fatal("expected phone_environment event to be handled")
	}
	server.bridge.mu.Lock()
	server.bridge.connected = true
	server.bridge.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/screenshot.jpg", nil)
	rec := httptest.NewRecorder()

	server.handleScreenshotJPEG(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Original-Screen-Width"); got != "1080" {
		t.Fatalf("X-Original-Screen-Width = %q, want 1080", got)
	}
	if got := rec.Header().Get("X-Original-Screen-Height"); got != "2400" {
		t.Fatalf("X-Original-Screen-Height = %q, want 2400", got)
	}
	if got := rec.Header().Get("X-Original-Screen-Valid"); got != "true" {
		t.Fatalf("X-Original-Screen-Valid = %q, want true", got)
	}

	width, height, active, _, ok := runtime.tools.screen.ActiveAreaWithAge()
	if !ok {
		t.Fatal("expected shared screen state to be updated")
	}
	if width != 16 || height != 9 {
		t.Fatalf("screen dimensions = %dx%d, want 16x9", width, height)
	}
	want := screen.ScreenActiveArea{X: 5, Y: 0, Width: 5, Height: 9, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
}

func TestHandleScreenshotJPEGIncludesADBDeviceHeadersWhenFallbackUsed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell script uses /bin/sh")
	}

	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}

	tmpDir := t.TempDir()
	pngPath := filepath.Join(tmpDir, "screen.png")
	if err := os.WriteFile(pngPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", pngPath, err)
	}

	adbPath := filepath.Join(tmpDir, "adb")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"devices\" ] && [ \"$2\" = \"-l\" ]; then\n" +
		"  printf 'List of devices attached\\nserial123\\tdevice product:panther model:Pixel_7_Pro device:panther transport_id:1\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"-s\" ] && [ \"$2\" = \"serial123\" ] && [ \"$3\" = \"exec-out\" ] && [ \"$4\" = \"screencap\" ] && [ \"$5\" = \"-p\" ]; then\n" +
		"  cat \"$AIDEN_TEST_PNG\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"unexpected args: $@\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", adbPath, err)
	}

	t.Setenv("AIDEN_ADB_PATH", adbPath)
	t.Setenv("AIDEN_TEST_PNG", pngPath)
	t.Setenv("AIDEN_ADB_SERIAL", "")
	t.Setenv("ANDROID_SERIAL", "")
	defer clearAutoConfiguredADBSerial("serial123")

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			HID:   HIDConfig{FrameSocket: filepath.Join(tmpDir, "missing-frame.sock")},
		}),
		&testModelResolver{},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodGet, "/api/screenshot.jpg", nil)
	rec := httptest.NewRecorder()

	server.handleScreenshotJPEG(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Capture-Backend"); got != "adb" {
		t.Fatalf("X-Capture-Backend = %q, want adb", got)
	}
	if got := rec.Header().Get("X-Adb-Device-Valid"); got != "true" {
		t.Fatalf("X-Adb-Device-Valid = %q, want true", got)
	}
	if got := rec.Header().Get("X-Adb-Device-Serial"); got != "serial123" {
		t.Fatalf("X-Adb-Device-Serial = %q, want serial123", got)
	}
	if got := rec.Header().Get("X-Adb-Device-Name"); got != "Pixel 7 Pro" {
		t.Fatalf("X-Adb-Device-Name = %q, want Pixel 7 Pro", got)
	}
	if got := rec.Header().Get("X-Adb-Device-State"); got != "device" {
		t.Fatalf("X-Adb-Device-State = %q, want device", got)
	}

	decoded, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("jpeg.Decode() error = %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 1 {
		t.Fatalf("decoded jpeg bounds = %v, want 2x1", bounds)
	}
}

func TestHandleCoordinateDebugTapRecapturesUncroppedScreenshot(t *testing.T) {
	frameSocket := startFakeFrameServiceSocket(t, func(req map[string]any) (string, []byte) {
		if method, _ := req["method"].(string); method == "health" {
			return `{"type":"response","method":"health","status":"OK","state":"RUNNING","latest_seq":2,"frame_age_ms":10}`, nil
		}
		if format, _ := req["format"].(string); format != "raw" {
			t.Fatalf("expected raw format request when crop_black_bars=false, got %#v", req["format"])
		}
		header := `{"type":"response","method":"latest_frame","status":"OK","frame":{"seq":2,"width":2,"height":1,"pixel_format":"uyvy","stride":4,"bytes":4,"stale":false}}`
		return header, []byte{16, 128, 235, 128}
	})
	tool := &stubTool{
		name:        "touch_gesture",
		description: "Touch gesture tool.",
		output:      `{"width":1,"height":1,"format":"jpeg","size":4,"data":"ZmFrZQ==","action_output":"ok"}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			HID:   HIDConfig{FrameSocket: frameSocket},
		}),
		&testModelResolver{},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"touch_gesture": tool,
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/coordinate-debug/tap", bytes.NewBufferString(`{"x":123,"y":456,"type":"tap","crop_black_bars":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleCoordinateDebugTap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp coordinateDebugTapResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Screenshot == nil {
		t.Fatal("expected screenshot in response")
	}
	if resp.Screenshot.Width != 2 || resp.Screenshot.Height != 1 {
		t.Fatalf("unexpected screenshot dimensions: %#v", resp.Screenshot)
	}
	if resp.Screenshot.Data == "ZmFrZQ==" {
		t.Fatalf("expected recaptured screenshot instead of tool screenshot: %#v", resp.Screenshot)
	}

	jpegBytes, err := base64.StdEncoding.DecodeString(resp.Screenshot.Data)
	if err != nil {
		t.Fatalf("decode screenshot base64: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatalf("decode screenshot jpeg: %v", err)
	}
	if bounds := img.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 1 {
		t.Fatalf("decoded bounds = %v, want 2x1", bounds)
	}
}

func TestHandleCoordinateDebugTapRecapturesScreenshotWhenMappingUnavailable(t *testing.T) {
	rgb := make([]byte, 5*9*3)
	for i := range rgb {
		rgb[i] = 255
	}
	jpegData, err := encodeJPEG(rgb, 5, 9, screenshotJPEGQuality)
	if err != nil {
		t.Fatalf("encodeJPEG() error = %v", err)
	}
	frameSocket := startFakeFrameServiceSocket(t, func(req map[string]any) (string, []byte) {
		if method, _ := req["method"].(string); method == "health" {
			return `{"type":"response","method":"health","status":"OK","state":"RUNNING","latest_seq":2,"frame_age_ms":10}`, nil
		}
		if format, _ := req["format"].(string); format != "jpeg" {
			t.Fatalf("expected jpeg format request when remapping cropped screenshot, got %#v", req["format"])
		}
		header := fmt.Sprintf(`{"type":"response","method":"latest_frame","status":"OK","frame":{"seq":2,"width":5,"height":9,"source_width":16,"source_height":9,"crop_x":5,"crop_y":0,"crop_width":5,"crop_height":9,"pixel_format":"jpeg","stride":0,"bytes":%d,"stale":false}}`, len(jpegData))
		return header, jpegData
	})
	tool := &stubTool{
		name:        "touch_gesture",
		description: "Touch gesture tool.",
		output:      `{"width":1,"height":1,"format":"jpeg","size":4,"data":"ZmFrZQ==","action_output":"ok"}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			HID:   HIDConfig{FrameSocket: frameSocket},
		}),
		&testModelResolver{},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"touch_gesture": tool,
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/coordinate-debug/tap", bytes.NewBufferString(`{"x":123,"y":456,"type":"tap","crop_black_bars":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleCoordinateDebugTap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var resp coordinateDebugTapResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Screenshot == nil {
		t.Fatal("expected screenshot in response")
	}
	if resp.Screenshot.Data == "ZmFrZQ==" {
		t.Fatalf("expected recaptured screenshot instead of tool screenshot: %#v", resp.Screenshot)
	}
	if resp.Screenshot.Width != 5 || resp.Screenshot.Height != 9 {
		t.Fatalf("unexpected screenshot dimensions: %#v", resp.Screenshot)
	}
	if resp.Screenshot.SourceWidth != 16 || resp.Screenshot.SourceHeight != 9 {
		t.Fatalf("unexpected screenshot source dimensions: %#v", resp.Screenshot)
	}
	want := &screen.ScreenActiveArea{X: 5, Y: 0, Width: 5, Height: 9, Valid: true}
	if resp.Screenshot.SourceActiveArea == nil || *resp.Screenshot.SourceActiveArea != *want {
		t.Fatalf("unexpected screenshot active area: %#v", resp.Screenshot.SourceActiveArea)
	}
}

func TestServerSpeakToolContentUsesTTS(t *testing.T) {
	provider := &recordingTTSProvider{name: "server-provider"}
	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Audio: AudioConfig{SampleRate: 16000}}),
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
	}

	server.speakToolContent(context.Background(), " Let me read the current volume. ")

	if got := provider.texts(); len(got) != 1 || got[0] != "Let me read the current volume." {
		t.Fatalf("unexpected TTS texts: %#v", got)
	}
}

func TestServerAsyncChatAppendsVoiceNotificationOnlyToFinalSpeech(t *testing.T) {
	streamingDisabled := false
	model := &scriptedModel{responses: roleDirectResponses("Done.\n<tts>Done.</tts>")}
	cfg := DefaultConfig()
	cfg.Model = ModelConfig{Provider: "fake"}
	cfg.Instruction = "Answer directly."
	cfg.VoiceStreamingTTSEnabled = &streamingDisabled
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, cfg),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("runtime.Close() error = %v", err)
		}
	})
	if err := runtime.VoiceNotificationSink().Publish(context.Background(), VoiceNotificationEvent{
		Code: "storage", Severity: SeverityWarning, State: VoiceNotificationActive, DedupeKey: "storage:device",
	}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	server := newServerForTest(runtime)
	provider := &recordingTTSProvider{name: "server-provider"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient(startTTSPlaybackAudioSocket(t))

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"finish it"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleChat status = %d body=%s", rec.Code, rec.Body.String())
	}
	var start map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&start); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := start["request_id"]
	if requestID == "" {
		t.Fatal("missing request_id")
	}

	deadline := time.Now().Add(2 * time.Second)
	var result ChatResultResponse
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code == http.StatusOK {
			result = ChatResultResponse{}
			if err := json.NewDecoder(resultRec.Body).Decode(&result); err == nil && result.Status == "complete" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.Status != "complete" {
		t.Fatalf("chat result = %#v", result)
	}
	if strings.Contains(result.Response, "存储空间") {
		t.Fatalf("display response was changed by voice notification: %q", result.Response)
	}
	waitForProviderTextCount(t, provider, 1)
	if got := provider.texts(); len(got) != 1 || got[0] != "Done.另外提醒一下，设备存储空间不足。" {
		t.Fatalf("spoken texts = %#v", got)
	}
	waitForServerRequestFinished(t, server, requestID)
}

func TestServerAsyncChatSpeaksReplacementForFinalLLMFailure(t *testing.T) {
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Answer directly.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
		}),
		&testModelResolver{model: failingGenerateModel{err: errors.New("dial tcp: network is unreachable")}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("runtime.Close() error = %v", err)
		}
	})
	server := newServerForTest(runtime)
	provider := &recordingTTSProvider{name: "server-provider"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient(startTTSPlaybackAudioSocket(t))

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"finish it"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleChat status = %d body=%s", rec.Code, rec.Body.String())
	}
	var start map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&start); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := start["request_id"]
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code == http.StatusOK {
			var result ChatResultResponse
			if err := json.NewDecoder(resultRec.Body).Decode(&result); err == nil && result.Status == "error" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitForProviderTextCount(t, provider, 1)
	if got := provider.texts(); len(got) != 1 || got[0] != "当前网络不可用，暂时无法完成这个请求。" {
		t.Fatalf("spoken replacement = %#v", got)
	}
	waitForServerRequestFinished(t, server, requestID)
}

func waitForServerRequestFinished(t *testing.T, server *Server, requestID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.activeRunsMu.Lock()
		_, active := server.activeRuns[requestID]
		server.activeRunsMu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request %q was still active after final speech", requestID)
}

func TestServerHandleChatDoesNotWaitForToolContentTTSWhenEnabled(t *testing.T) {
	speech := "Read volume."
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, "Reading volume.\n<tts>"+speech+"</tts>"),
			contentResponse("The current audio volume is 42."),
		},
	}
	streamingDisabled := false
	toolSpeechEnabled := true
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
			VoiceToolCallSpeech:      &toolSpeechEnabled,
		}),
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
	defer runtime.Close()
	server := newServerForTest(runtime)
	provider := &blockingTTSProvider{started: make(chan struct{}), blockText: speech}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient("/tmp/audio.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"What is the current volume?"}`)).WithContext(ctx)
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
		t.Fatal("handleChat waited for tool content TTS")
	}
	select {
	case <-provider.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("tool content TTS was not started")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code != http.StatusOK {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var resp ChatResultResponse
		if err := json.NewDecoder(resultRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode result response: %v", err)
		}
		if resp.Status == "complete" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServerHandleChatDoesNotSpeakWaitForWakeup(t *testing.T) {
	toolSpeechEnabled := true
	streamingEnabled := true
	toolContent := "Going idle.\n<tts>Standby mode.</tts>"
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("wait_1", "wait_for_wakeup", `{"reason":"user asked"}`, toolContent),
		},
		streamChunks: [][]string{{toolContent}},
	}
	controller := NewWaitForWakeupController()
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools.",
			VoiceStreamingTTSEnabled: &streamingEnabled,
			VoiceToolCallSpeech:      &toolSpeechEnabled,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"wait_for_wakeup": NewWaitForWakeupTool(controller),
		}},
		NewSkillIndex(),
	)
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("runtime.Close() error = %v", err)
		}
	})
	server := newServerForTest(runtime)
	provider := &recordingTTSProvider{name: "server-provider"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient(startTTSPlaybackAudioSocket(t))

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"go to sleep"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}
	assertNoProviderTextWithin(t, provider, 200*time.Millisecond)
	waitForServerRequestFinished(t, server, requestID)
}

func TestServerHandleChatSkipsToolContentTTSWhenDisabled(t *testing.T) {
	toolContent := "Reading volume.\n<tts>Let me read the current volume.</tts>"
	finalContent := "<tts>The current audio volume is 42.</tts>\nThe current audio volume is 42."
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, toolContent),
			contentResponse(finalContent),
		},
		streamChunks: [][]string{{toolContent}, {finalContent}},
	}
	streamingEnabled := true
	toolSpeechDisabled := false
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:                    ModelConfig{Provider: "fake"},
			Instruction:              "Use tools when external state is requested.",
			VoiceStreamingTTSEnabled: &streamingEnabled,
			VoiceToolCallSpeech:      &toolSpeechDisabled,
		}),
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
	server := newServerForTest(runtime)
	provider := &recordingTTSProvider{name: "server-provider"}
	server.ttsManager = ttsmodule.NewProviderManager(provider, nil)
	server.audioClient = NewAudioServiceClient(startTTSPlaybackAudioSocket(t))

	req := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"message":"What is the current volume?"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}

	completed := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code != http.StatusOK {
			t.Fatalf("unexpected result status: %d body=%s", resultRec.Code, resultRec.Body.String())
		}
		var resp ChatResultResponse
		if err := json.NewDecoder(resultRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode result response: %v", err)
		}
		if resp.Status == "complete" {
			completed = true
			break
		}
		if resp.Status == "error" {
			t.Fatalf("chat request failed: %s", resp.Error)
		}
		if resp.Status == "running" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !completed {
		t.Fatalf("chat request did not complete before deadline: request_id=%s", requestID)
	}
	waitForServerRequestFinished(t, server, requestID)
	waitForProviderTextCount(t, provider, 1)
	if got := provider.texts(); len(got) != 1 || got[0] != "The current audio volume is 42." {
		t.Fatalf("spoken texts = %#v, want final answer only", got)
	}
}

func TestServerShouldSpeakToolCallRequiresTTSTag(t *testing.T) {
	toolSpeechEnabled := true
	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{VoiceToolCallSpeech: &toolSpeechEnabled, Model: ModelConfig{Provider: "fake"}}),
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
	}

	if server.shouldSpeakToolCall(RunEvent{Type: runEventToolCall, ToolName: "recall_memory", Content: "I will check your preferences first."}) {
		t.Fatal("plain recall_memory tool content should not be spoken")
	}
	if server.shouldSpeakToolCall(RunEvent{Type: runEventToolCall, ToolName: "screenshot", Content: "I will inspect the screen first."}) {
		t.Fatal("plain screenshot tool content should not be spoken")
	}
	if server.shouldSpeakToolCall(RunEvent{Type: runEventToolCall, ToolName: "audio_volume", Content: "Check the current volume."}) {
		t.Fatal("plain audio_volume tool content should not be spoken")
	}
	if !server.shouldSpeakToolCall(RunEvent{Type: runEventToolCall, ToolName: "audio_volume", Content: "Checking.\n<tts>Check the current volume.</tts>"}) {
		t.Fatal("tagged audio_volume tool content should be spoken")
	}
}

func TestServerHandleChatCancelCancelsActiveRun(t *testing.T) {
	server := &Server{logger: newTestLogger(), activeRuns: make(map[string]context.CancelFunc)}
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

func TestServerCloseCancelsActiveRunsAndOutputs(t *testing.T) {
	server := &Server{activeRuns: make(map[string]context.CancelFunc)}
	runCtx, cancelRun := context.WithCancel(context.Background())
	server.registerActiveRun("req-close", cancelRun)

	outputCtx, cancelOutput := context.WithCancel(context.Background())
	output := newActiveTTSOutput(cancelOutput)
	unregisterOutput := server.registerActiveOutput("req-close", output)
	defer unregisterOutput()

	server.Close()

	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Server.Close() did not cancel the active run")
	}
	select {
	case <-outputCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Server.Close() did not interrupt the active output")
	}

	lateRunCtx, cancelLateRun := context.WithCancel(context.Background())
	defer cancelLateRun()
	if server.registerActiveRun("req-after-close", cancelLateRun) {
		t.Fatal("Server accepted an active run after Close()")
	}
	select {
	case <-lateRunCtx.Done():
		t.Fatal("rejected late run was unexpectedly canceled by Server")
	default:
	}

	lateOutputCtx, cancelLateOutput := context.WithCancel(context.Background())
	lateOutput := newActiveTTSOutput(cancelLateOutput)
	cleanupLateOutput := server.registerActiveOutput("req-after-close", lateOutput)
	defer cleanupLateOutput()
	select {
	case <-lateOutputCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Server did not interrupt an output registered after Close()")
	}
}

func TestServerCloseStopsAcceptingAndDrainsActiveHTTPHandlers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve HTTP address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release HTTP address: %v", err)
	}

	server := &Server{
		addr:             addr,
		eventBroadcaster: NewEventBroadcaster(),
		activeRuns:       make(map[string]context.CancelFunc),
	}
	startDone := make(chan error, 1)
	go func() {
		startDone <- server.Start()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP server did not start: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := http.Get("http://" + addr + "/api/events")
	if err != nil {
		t.Fatalf("open SSE request: %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("read SSE response: %v", err)
	}

	closeDone := make(chan struct{})
	go func() {
		server.Close()
		close(closeDone)
	}()

	returnedBeforeHandlerDrained := false
	select {
	case <-closeDone:
		returnedBeforeHandlerDrained = true
	case <-time.After(50 * time.Millisecond):
	}

	if err := resp.Body.Close(); err != nil {
		t.Errorf("close SSE response: %v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Server.Close() did not finish after the active handler drained")
	}
	if returnedBeforeHandlerDrained {
		t.Error("Server.Close() returned before the active HTTP handler drained")
	}

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Server.Start() after Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Server.Start() did not return after Close()")
	}

	conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("HTTP server still accepted connections after Close()")
	}
}

func TestServerHandleChatCancelEndsDanglingLiveActivity(t *testing.T) {
	server := &Server{logger: newTestLogger(),
		activeRuns:   make(map[string]context.CancelFunc),
		liveActivity: NewLiveActivityManager(LiveActivityConfig{}, newTestLogger()),
	}
	server.liveActivity.StartTask("req-1", "External run")

	req := httptest.NewRequest(http.MethodPost, "/api/chat/cancel", bytes.NewBufferString(`{"request_id":"req-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleChatCancel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ChatCancelResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "canceled" || resp.RequestID != "req-1" {
		t.Fatalf("unexpected cancel response: %#v", resp)
	}
	state := server.liveActivity.Snapshot("req-1")
	if state == nil || state.Status != LiveActivityStatusCanceled || state.CanStop {
		t.Fatalf("live activity state = %#v, want canceled", state)
	}
}

func TestServerHandleChatCancelStopsRequestScopedTTSPlayback(t *testing.T) {
	requestID := "req-stop-1"
	audioOps := &recordedAudioOps{}
	provider := newInterruptibleAudioTTSProvider("server-provider", 48000, true)
	server := &Server{logger: newTestLogger(),
		activeRuns:  make(map[string]context.CancelFunc),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, audioOps)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun(requestID, cancel)
	done := make(chan error, 1)
	go func() {
		done <- server.speakTextForRequest(ctx, requestID, "final answer", 0)
	}()
	waitForTestSignal(t, provider.firstWriteDone(), "final TTS playback to start")
	deadline := time.Now().Add(500 * time.Millisecond)
	for audioOps.countOp("start_playback") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("final TTS playback never opened a playback session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/chat/cancel", bytes.NewBufferString(`{"request_id":"`+requestID+`"}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	server.handleChatCancel(cancelRec, cancelReq)

	if cancelRec.Code != http.StatusOK {
		t.Fatalf("unexpected cancel status: %d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	var resp ChatCancelResponse
	if err := json.NewDecoder(cancelRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if resp.Status != "canceled" || resp.RequestID != requestID {
		t.Fatalf("unexpected cancel response: %#v", resp)
	}
	if got := audioOps.countOp("stop_playback"); got != 1 {
		t.Fatalf("stop_playback count = %d, want 1", got)
	}
	if got := audioOps.finalChunkCountAfterFirstStop(); got != 0 {
		t.Fatalf("final write_play_chunk count after stop = %d, want 0", got)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("speakTextForRequest() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("request-scoped TTS did not stop after cancel")
	}
}

func TestServerHandleChatCancelStopsCompletedAsyncRequestTTSPlayback(t *testing.T) {
	requestID := "req-stop-async-complete"
	audioOps := &recordedAudioOps{}
	provider := newInterruptibleAudioTTSProvider("server-provider", 48000, true)
	server := &Server{logger: newTestLogger(),
		activeRuns:         make(map[string]context.CancelFunc),
		terminatedRequests: make(map[string]struct{}),
		ttsManager:         ttsmodule.NewProviderManager(provider, nil),
		audioClient:        NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, audioOps)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun(requestID, cancel)

	playbackDone := make(chan error, 1)
	go func() {
		playbackDone <- server.speakTextForRequest(ctx, requestID, "completed async final answer", 0)
	}()

	waitForTestSignal(t, provider.firstWriteDone(), "completed async final TTS playback to start")
	deadline := time.Now().Add(500 * time.Millisecond)
	for audioOps.countOp("start_playback") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("completed async final TTS playback never opened a playback session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	server.unregisterActiveRun(requestID)

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/chat/cancel", bytes.NewBufferString(`{"request_id":"`+requestID+`"}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	server.handleChatCancel(cancelRec, cancelReq)

	if cancelRec.Code != http.StatusOK {
		t.Fatalf("unexpected cancel status: %d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	var resp ChatCancelResponse
	if err := json.NewDecoder(cancelRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if resp.Status != "canceled" || resp.RequestID != requestID {
		t.Fatalf("unexpected cancel response: %#v", resp)
	}
	if got := audioOps.countOp("stop_playback"); got != 1 {
		t.Fatalf("stop_playback count = %d, want 1", got)
	}
	if got := audioOps.finalChunkCountAfterFirstStop(); got != 0 {
		t.Fatalf("final write_play_chunk count after stop = %d, want 0", got)
	}
	select {
	case err := <-playbackDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("speakTextForRequest() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("completed async final request-scoped TTS did not stop after cancel")
	}
}

func TestSpeakTextForRequestRefusesTerminatedRequest(t *testing.T) {
	requestID := "req-terminated"
	audioOps := &recordedAudioOps{}
	provider := newInterruptibleAudioTTSProvider("server-provider", 48000, true)
	server := &Server{logger: newTestLogger(),
		activeRuns:         make(map[string]context.CancelFunc),
		terminatedRequests: make(map[string]struct{}),
		ttsManager:         ttsmodule.NewProviderManager(provider, nil),
		audioClient:        NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, audioOps)),
	}
	server.markRequestTerminated(requestID)

	err := server.speakTextForRequest(context.Background(), requestID, "should not play", 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("speakTextForRequest() error = %v, want context canceled", err)
	}
	if got := audioOps.countOp("start_playback"); got != 0 {
		t.Fatalf("start_playback count = %d, want 0", got)
	}
}

func TestServerHandleChatCancelStopsRequestScopedStreamingTTSPlayback(t *testing.T) {
	requestID := "req-stop-streaming"
	audioOps := &recordedAudioOps{}
	provider := newInterruptibleAudioTTSProvider("server-provider", 48000, true)
	server := &Server{logger: newTestLogger(),
		activeRuns:  make(map[string]context.CancelFunc),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, audioOps)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun(requestID, cancel)

	stream, err := beginManagedTTSStream(ctx, server.ttsManager, server.currentTTSPlaybackBackend(), Config{})
	if err != nil {
		t.Fatalf("beginManagedTTSStream() error = %v", err)
	}
	output := newActiveTTSOutput(nil)
	output.setStream(stream)
	unregisterOutput := server.registerActiveOutput(requestID, output)
	defer unregisterOutput()

	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := speechtext.NewStreamWriter(stream).Write([]byte("<tts>streaming final answer</tts>"))
		writeDone <- writeErr
	}()

	waitForTestSignal(t, provider.firstWriteDone(), "request-scoped streaming TTS playback to start")
	deadline := time.Now().Add(500 * time.Millisecond)
	for audioOps.countOp("start_playback") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("streaming TTS playback never opened a playback session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/chat/cancel", bytes.NewBufferString(`{"request_id":"`+requestID+`"}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	server.handleChatCancel(cancelRec, cancelReq)

	if cancelRec.Code != http.StatusOK {
		t.Fatalf("unexpected cancel status: %d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	var resp ChatCancelResponse
	if err := json.NewDecoder(cancelRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if resp.Status != "canceled" || resp.RequestID != requestID {
		t.Fatalf("unexpected cancel response: %#v", resp)
	}
	if got := audioOps.countOp("stop_playback"); got != 1 {
		t.Fatalf("stop_playback count = %d, want 1", got)
	}
	if got := audioOps.finalChunkCountAfterFirstStop(); got != 0 {
		t.Fatalf("final write_play_chunk count after stop = %d, want 0", got)
	}
	select {
	case err := <-writeDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("stream Write() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("request-scoped streaming TTS did not stop after cancel")
	}
	if err := stream.closeAndWait(); err != nil {
		t.Fatalf("closeAndWait() error = %v", err)
	}
}

func TestServerHandleChatSteerQueuesAndCancelsPendingMessage(t *testing.T) {
	server := &Server{logger: newTestLogger(),
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

func TestServerSteerSignalRemainsObservableUntilSteerIsConsumed(t *testing.T) {
	server := &Server{
		logger:        newTestLogger(),
		activeRuns:    make(map[string]context.CancelFunc),
		pendingSteers: make(map[string]pendingSteerMessage),
		steerSignals:  make(map[string]chan struct{}),
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun("req-1", cancel)
	server.resetSteerSignal("req-1")

	req := httptest.NewRequest(http.MethodPost, "/api/chat/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"change direction"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleChatSteer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	lateSubscriber := server.steerSignalChannel("req-1")
	if lateSubscriber == nil {
		t.Fatal("late interrupt subscriber received nil after steer was signaled")
	}
	select {
	case <-lateSubscriber:
		// A subscriber installed after the HTTP request must still observe it.
	default:
		t.Fatal("late interrupt subscriber did not observe queued steer")
	}
}

func TestServerCancelPendingSteerRearmsInterruptForNextSteer(t *testing.T) {
	server := &Server{
		logger:        newTestLogger(),
		activeRuns:    make(map[string]context.CancelFunc),
		pendingSteers: make(map[string]pendingSteerMessage),
		steerSignals:  make(map[string]chan struct{}),
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.registerActiveRun("req-1", cancel)
	server.resetSteerSignal("req-1")

	queue := func(message string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/chat/steer", bytes.NewBufferString(`{"request_id":"req-1","message":"`+message+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.handleChatSteer(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("queue steer status: %d body=%s", rec.Code, rec.Body.String())
		}
	}

	queue("first")
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/chat/steer/cancel", bytes.NewBufferString(`{"request_id":"req-1"}`))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	server.handleChatSteerCancel(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel steer status: %d body=%s", cancelRec.Code, cancelRec.Body.String())
	}

	nextInterrupt := server.steerSignalChannel("req-1")
	if nextInterrupt == nil {
		t.Fatal("canceling a pending steer did not rearm the interrupt channel")
	}
	select {
	case <-nextInterrupt:
		t.Fatal("rearmed interrupt channel was already closed")
	default:
	}

	queue("second")
	select {
	case <-nextInterrupt:
		// The next steer must still interrupt the active run.
	default:
		t.Fatal("second steer did not close the rearmed interrupt channel")
	}
}

func TestServerStaleSignalDoesNotCloseRearmedChannelWithoutPendingSteer(t *testing.T) {
	server := &Server{
		pendingSteers: make(map[string]pendingSteerMessage),
		steerSignals:  make(map[string]chan struct{}),
	}
	server.resetSteerSignal("req-1")
	server.pendingSteers["req-1"] = pendingSteerMessage{RequestID: "req-1", Content: "first"}
	if !server.cancelPendingSteer("req-1") {
		t.Fatal("cancelPendingSteer() = false, want true")
	}

	// Model a delayed signal from the canceled setPendingSteer call.
	server.signalSteer("req-1")
	interrupt := server.steerSignalChannel("req-1")
	if interrupt == nil {
		t.Fatal("rearmed interrupt channel is nil")
	}
	select {
	case <-interrupt:
		t.Fatal("stale signal closed channel without a pending steer")
	default:
	}
}

func TestServerResetSignalKeepsQueuedSteerObservable(t *testing.T) {
	server := &Server{
		pendingSteers: make(map[string]pendingSteerMessage),
		steerSignals:  make(map[string]chan struct{}),
	}
	server.pendingSteers["req-1"] = pendingSteerMessage{RequestID: "req-1", Content: "second"}

	// Model a reset racing after a second steer has already been queued.
	server.resetSteerSignal("req-1")
	interrupt := server.steerSignalChannel("req-1")
	if interrupt == nil {
		t.Fatal("reset interrupt channel is nil")
	}
	select {
	case <-interrupt:
		// A queued steer must remain observable after any reset.
	default:
		t.Fatal("reset signal lost an already queued steer")
	}
}

func TestServerHandleChatSteerRejectsNonRunningRequest(t *testing.T) {
	server := &Server{logger: newTestLogger(), activeRuns: make(map[string]context.CancelFunc)}
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
		"sendBtn.textContent = currentChatRequestId ? 'Steer' : 'Send';",
		"id=\"pendingSteer\"",
	} {
		if !strings.Contains(webUI, want) {
			t.Fatalf("web UI missing %q", want)
		}
	}
}

func TestWebUIImagePasteControlsArePresent(t *testing.T) {
	for _, want := range []string{
		"const maxDraftImageAttachments = 4;",
		"inputEl.addEventListener('paste', handleComposerPaste);",
		"async function handleComposerPaste(event)",
		"await addImageFiles(files, 'pasted');",
		"Only 4 images can be attached.",
	} {
		if !strings.Contains(webUI, want) {
			t.Fatalf("web UI missing %q", want)
		}
	}
}

func TestServerHandleChatAsyncDuplicateRequestIDDoesNotAppendHistory(t *testing.T) {
	server := &Server{logger: newTestLogger(),
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
	server := &Server{logger: newTestLogger(),
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

func TestServerSpeakToolContentUsesCallerContext(t *testing.T) {
	provider := &blockingTTSProvider{started: make(chan struct{}), blockText: "Let me read the current volume."}
	server := &Server{logger: newTestLogger(),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient("/tmp/audio.sock"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		server.speakToolContent(ctx, "Let me read the current volume.")
		close(done)
	}()

	select {
	case <-provider.started:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("tool content TTS was not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("tool content TTS did not stop after caller context cancellation")
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
	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		history: []Message{
			{Type: "user", Content: "hello"},
			{Type: runEventToolCall, ToolName: "screenshot", ToolInput: "{}"},
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
	if history[1].Type != runEventToolCall || history[2].Type != "tool_result" {
		t.Fatalf("unexpected history payload: %#v", history)
	}
}

func TestServerContextDumpEndpointReturnsPlannerMessages(t *testing.T) {
	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
	}
	sessionFolder := t.TempDir()
	sessionID := "test-session"
	if err := os.MkdirAll(sessionFolder, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionFolder, ".current_session"), []byte(sessionID), 0o644); err != nil {
		t.Fatalf("WriteFile(.current_session) error = %v", err)
	}
	manager, err := contextmanager.LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	if err := manager.AppendMessage(messages.Message{
		Role:    messages.MessageRoleUser,
		Content: "hello planner",
	}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	server.runtime.contextManager = manager

	req := httptest.NewRequest(http.MethodGet, "/api/context-dump", nil)
	rec := httptest.NewRecorder()
	server.handleContextDump(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var dump contextmanager.MessageListDump
	if err := json.NewDecoder(rec.Body).Decode(&dump); err != nil {
		t.Fatalf("decode context dump: %v", err)
	}
	if dump.SessionID == "" {
		t.Fatal("expected session_id in context dump")
	}
	if len(dump.Messages) != 1 || dump.Messages[0].Content != "hello planner" {
		t.Fatalf("unexpected context dump payload: %#v", dump)
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
		llms.HumanChatMessage{Content: "Remember, expenses over 100 in the Lanhai reimbursement app must be confirmed first."},
	}); err != nil {
		t.Fatalf("SetMessages() error = %v", err)
	}
	if err := memoryManager.Save(context.Background(), "default"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session", "events.jsonl")); err != nil {
		t.Fatalf("expected session events before clear: %v", err)
	}

	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
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

func TestServerHandleSetupReturnsSuccessWithoutClearingHistory(t *testing.T) {
	server := &Server{logger: newTestLogger(),
		history: []Message{{Type: "user", Content: "hello"}},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/setup", nil)
	rec := httptest.NewRecorder()
	server.handleSetup(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK   bool `json:"ok"`
		Data struct {
			Setup bool `json:"setup"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK {
		t.Fatalf("expected ok response: %#v", got)
	}
	if got.Data.Setup {
		t.Fatalf("expected setup=false for Go agent no-op response")
	}
	if len(server.history) != 1 || server.history[0].Content != "hello" {
		t.Fatalf("setup should not clear history, got %#v", server.history)
	}
}

func TestServerHandleConcurrentReturnsSingleCapacity(t *testing.T) {
	server := &Server{logger: newTestLogger()}

	req := httptest.NewRequest(http.MethodGet, "/api/concurrent", nil)
	rec := httptest.NewRecorder()
	server.handleConcurrent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK   bool `json:"ok"`
		Data struct {
			BridgeType string `json:"bridge_type"`
			Concurrent int    `json:"concurrent"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK || got.Data.BridgeType != "go-agent" || got.Data.Concurrent != 1 {
		t.Fatalf("unexpected concurrent response: %#v", got)
	}
}

func TestServerHandleSkillsReloadMarksDirty(t *testing.T) {
	storageDir := t.TempDir()
	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
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
	server := &Server{logger: newTestLogger(),
		runtime: NewRuntimeWithDeps(
			withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
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
	stt := &stubSTTClient{transcript: "Hello, please summarize this"}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		}),
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("Completed")}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := &Server{logger: newTestLogger(),
		runtime:        runtime,
		history:        make([]Message, 0),
		sttClient:      stt,
		pendingResults: make(map[string]*chatPendingResult),
		activeRuns:     make(map[string]context.CancelFunc),
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

	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}

	// STT should have been called during input processing
	if len(stt.inputs) != 1 {
		t.Fatalf("expected 1 STT invocation, got %d", len(stt.inputs))
	}

	// Poll for result
	var resp ChatResultResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code != http.StatusOK {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err := json.NewDecoder(resultRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode result response: %v", err)
		}
		if resp.Status == "complete" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if resp.Status != "complete" {
		t.Fatalf("result never completed: status=%q", resp.Status)
	}
	if resp.Response != "Completed" {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
	if len(resp.History) < 2 {
		t.Fatalf("expected at least 2 history entries for default-mode direct finish, got %d", len(resp.History))
	}
	if resp.History[0].Content != "Hello, please summarize this" {
		t.Fatalf("expected transcript as user content, got %#v", resp.History[0])
	}
	if len(resp.History[0].Attachments) != 1 || resp.History[0].Attachments[0].Transcript != "Hello, please summarize this" {
		t.Fatalf("expected transcript on audio attachment, got %#v", resp.History[0].Attachments)
	}
	assistant, ok := firstMessageOfType(resp.History, "assistant")
	if !ok || assistant.Content != "Completed" {
		t.Fatalf("unexpected assistant message: %#v", resp.History)
	}
}

func TestDecodeMessageAttachmentsLimitsImages(t *testing.T) {
	payloads := make([]MessageAttachment, maxChatImageAttachments+1)
	for i := range payloads {
		payloads[i] = MessageAttachment{
			Kind:     AttachmentKindImage,
			Name:     fmt.Sprintf("image-%d.png", i+1),
			MIMEType: "image/png",
			Data:     base64.StdEncoding.EncodeToString([]byte{byte(i + 1)}),
		}
	}

	if decoded, history, err := decodeMessageAttachments(payloads[:maxChatImageAttachments]); err != nil {
		t.Fatalf("decodeMessageAttachments(%d images) error = %v", maxChatImageAttachments, err)
	} else if len(decoded) != maxChatImageAttachments || len(history) != maxChatImageAttachments {
		t.Fatalf("decoded=%d history=%d, want %d", len(decoded), len(history), maxChatImageAttachments)
	}

	_, _, err := decodeMessageAttachments(payloads)
	if err == nil || !strings.Contains(err.Error(), "at most 4 image attachments") {
		t.Fatalf("decodeMessageAttachments(%d images) error = %v, want image limit", len(payloads), err)
	}
}

func TestDecodeMessageAttachmentsCountsImageMIMEWithFileKind(t *testing.T) {
	payloads := make([]MessageAttachment, maxChatImageAttachments+1)
	for i := range payloads {
		payloads[i] = MessageAttachment{
			Kind:     "file",
			Name:     fmt.Sprintf("image-%d.png", i+1),
			MIMEType: "image/png",
			Data:     base64.StdEncoding.EncodeToString([]byte{byte(i + 1)}),
		}
	}

	decoded, history, err := decodeMessageAttachments(payloads[:maxChatImageAttachments])
	if err != nil {
		t.Fatalf("decodeMessageAttachments(%d file-kind images) error = %v", maxChatImageAttachments, err)
	}
	if decoded[0].Kind != AttachmentKindImage || history[0].Kind != AttachmentKindImage {
		t.Fatalf("file-kind image was not normalized: decoded=%q history=%q", decoded[0].Kind, history[0].Kind)
	}

	_, _, err = decodeMessageAttachments(payloads)
	if err == nil || !strings.Contains(err.Error(), "at most 4 image attachments") {
		t.Fatalf("decodeMessageAttachments(%d file-kind images) error = %v, want image limit", len(payloads), err)
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
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
			},
		}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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

func TestServerDeviceAudioRecordingStopIncludesStreamingTranscript(t *testing.T) {
	stopCh := make(chan struct{})
	var stopOnce sync.Once

	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_playback":
			return audioResponse{Status: "OK", SessionID: stringUint64(7)}, nil
		case "write_play_chunk", "health":
			return audioResponse{Status: "OK"}, nil
		case "start_recording":
			return audioResponse{Status: "OK", SessionID: stringUint64(42)}, nil
		case "read_record_chunk":
			select {
			case <-stopCh:
				return audioResponse{Status: "OK", EndOfStream: true}, nil
			default:
				return audioResponse{Status: "OK"}, []byte{1, 0, 2, 0}
			}
		case "stop_recording":
			stopOnce.Do(func() { close(stopCh) })
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
				Channels:   1,
				BitWidth:   16,
			},
		}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)
	server.sttClient = &stubSTTClient{
		supportsStreaming: true,
		streamUploader:    &stubSTTStreamUploader{transcript: "streaming upload result"},
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/audio/record/start", nil)
	startRec := httptest.NewRecorder()
	server.handleAudioRecordStart(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("unexpected start status: %d body=%s", startRec.Code, startRec.Body.String())
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
	if resp.Attachment.Transcript != "streaming upload result" {
		t.Fatalf("Attachment.Transcript = %q, want streaming upload result", resp.Attachment.Transcript)
	}
}

func TestServerEndWebRecordingClearsStreamingSessionOnDrainTimeout(t *testing.T) {
	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "stop_recording":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "OK"}, nil
		}
	})

	uploader := newBlockingFinalizeUploader("")
	server := &Server{logger: newTestLogger(), audioClient: NewAudioServiceClient(socketPath)}
	recording := &webAudioRecording{
		sessionID:  42,
		sampleRate: 16000,
		done:       make(chan struct{}),
		sttSession: &streamingSTTSession{uploader: uploader},
	}

	err := server.endWebRecordingWithTimeout(recording, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for audio recording to drain") {
		t.Fatalf("endWebRecordingWithTimeout() error = %v, want drain timeout", err)
	}
	select {
	case <-uploader.closed:
	case <-time.After(time.Second):
		t.Fatal("expected uploader to be closed after drain timeout")
	}
}

func TestServerEndWebRecordingReturnsFinalizeTimeout(t *testing.T) {
	oldTimeout := webRecordingStreamingSTTFinalizeTimeout
	webRecordingStreamingSTTFinalizeTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		webRecordingStreamingSTTFinalizeTimeout = oldTimeout
	})

	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "stop_recording":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "OK"}, nil
		}
	})

	done := make(chan struct{})
	close(done)
	uploader := newBlockingFinalizeUploader("")
	server := &Server{logger: newTestLogger(), audioClient: NewAudioServiceClient(socketPath)}
	recording := &webAudioRecording{
		sessionID:  42,
		sampleRate: 16000,
		done:       done,
		sttSession: &streamingSTTSession{uploader: uploader},
	}

	err := server.endWebRecordingWithTimeout(recording, time.Second)
	if !errors.Is(err, errStreamingSTTFinalizeTimeout) {
		t.Fatalf("endWebRecordingWithTimeout() error = %v, want finalize timeout", err)
	}
	select {
	case <-uploader.closed:
	case <-time.After(time.Second):
		t.Fatal("expected uploader to be closed after finalize timeout")
	}
}

func TestServerWebAudioInputModeNeverFallsBackToRemovedAudio(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		stt  STTClient
		want string
	}{
		{
			name: "explicit stt",
			cfg:  Config{InputMode: " stt "},
			want: TurnModalitySTT,
		},
		{
			name: "text with stt client",
			cfg:  Config{InputMode: "text"},
			stt:  &stubSTTClient{},
			want: TurnModalitySTT,
		},
		{
			name: "default text without stt client",
			cfg:  Config{},
			want: TurnModalityText,
		},
		{
			name: "explicit text without stt client",
			cfg:  Config{InputMode: " text "},
			want: TurnModalityText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &Server{logger: newTestLogger(),
				runtime:   &Runtime{config: tt.cfg},
				sttClient: tt.stt,
			}
			got := server.webAudioInputMode()
			if got != tt.want {
				t.Fatalf("webAudioInputMode() = %q, want %q", got, tt.want)
			}
			if got == TurnModalityAudio {
				t.Fatalf("webAudioInputMode() returned removed audio mode")
			}
		})
	}
}

func TestServerHandleChatUsesAttachmentTranscriptWithoutRetranscribing(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("Completed"),
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use transcript directly.",
			InputMode:   "stt",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	stt := &stubSTTClient{transcript: "should not be called"}
	server := &Server{logger: newTestLogger(),
		runtime:        runtime,
		history:        make([]Message, 0),
		sttClient:      stt,
		pendingResults: make(map[string]*chatPendingResult),
		activeRuns:     make(map[string]context.CancelFunc),
	}

	payload, err := json.Marshal(ChatRequest{
		Attachments: []MessageAttachment{{
			Kind:       AttachmentKindAudio,
			Name:       "recording.wav",
			MIMEType:   "audio/wav",
			Data:       base64.StdEncoding.EncodeToString([]byte("RIFFtest")),
			Transcript: "directly reused transcript",
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

	var startResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	requestID := startResp["request_id"]
	if requestID == "" {
		t.Fatalf("missing request_id in response: %#v", startResp)
	}

	// Should not have called STT since transcript was provided
	if len(stt.inputs) != 0 {
		t.Fatalf("expected attachment transcript to skip TranscribeWAV, got %d calls", len(stt.inputs))
	}

	// Poll for result
	var resp ChatResultResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resultReq := httptest.NewRequest(http.MethodGet, "/api/chat/result?request_id="+requestID, nil)
		resultRec := httptest.NewRecorder()
		server.handleChatResult(resultRec, resultReq)
		if resultRec.Code != http.StatusOK {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err := json.NewDecoder(resultRec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode result response: %v", err)
		}
		if resp.Status == "complete" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if resp.Status != "complete" {
		t.Fatalf("result never completed: status=%q", resp.Status)
	}
	if resp.History[0].Content != "directly reused transcript" {
		t.Fatalf("expected transcript as user content, got %#v", resp.History[0])
	}
}

func TestServerSTTConfigTestLiveSessionUsesStreamingTranscript(t *testing.T) {
	sentChunk := false
	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_recording":
			return audioResponse{Status: "OK", SessionID: stringUint64(41)}, nil
		case "read_record_chunk":
			timeoutMs := int(req.TimeoutMs)
			if timeoutMs <= 0 {
				t.Fatalf("unexpected timeout_ms: %d", timeoutMs)
			}
			if !sentChunk {
				sentChunk = true
				return audioResponse{Status: "OK"}, []byte{1, 0, 2, 0, 3, 0, 4, 0}
			}
			return audioResponse{Status: "OK", EndOfStream: true}, nil
		case "stop_recording":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
				Channels:   1,
				BitWidth:   16,
			},
		}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	stt := &stubSTTClient{
		supportsStreaming: true,
		streamUploader:    &stubSTTStreamUploader{transcript: "streaming upload result"},
	}
	previousFactory := newSTTClientFromConfigForLiveTest
	newSTTClientFromConfigForLiveTest = func(cfg Config) (STTClient, error) {
		if cfg.STT.Provider != "tencent-asr" {
			t.Fatalf("provider = %q, want tencent-asr", cfg.STT.Provider)
		}
		if cfg.Audio.Socket != socketPath {
			t.Fatalf("audio socket = %q, want %q", cfg.Audio.Socket, socketPath)
		}
		return stt, nil
	}
	defer func() {
		newSTTClientFromConfigForLiveTest = previousFactory
	}()

	startBody := `{"stt_values":{"provider":"tencent-asr","app_id":"app-1","secret_id":"id","secret_key":"key","region":"ap-shanghai","engine_model_type":"16k_zh"},"audio_values":{"socket":"` + socketPath + `","sample_rate":16000,"channels":1,"bit_width":16}}`
	startReq := httptest.NewRequest(http.MethodPost, "/api/config-test/stt/start", strings.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	server.handleSTTConfigTestStart(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("unexpected start status: %d body=%s", startRec.Code, startRec.Body.String())
	}

	stopReq := httptest.NewRequest(http.MethodPost, "/api/config-test/stt/stop", nil)
	stopRec := httptest.NewRecorder()
	server.handleSTTConfigTestStop(stopRec, stopReq)
	if stopRec.Code != http.StatusOK {
		t.Fatalf("unexpected stop status: %d body=%s", stopRec.Code, stopRec.Body.String())
	}

	var resp struct {
		OK         bool   `json:"ok"`
		Transcript string `json:"transcript"`
		Results    []struct {
			Check  string `json:"check"`
			Passed bool   `json:"passed"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	if err := json.NewDecoder(stopRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %#v", resp)
	}
	if resp.Transcript != "streaming upload result" {
		t.Fatalf("transcript = %q, want streaming upload result", resp.Transcript)
	}
	if len(stt.inputs) != 0 {
		t.Fatalf("expected streaming transcript to skip TranscribeWAV, got %d calls", len(stt.inputs))
	}
	if stt.streamUploaderUsed != 1 {
		t.Fatalf("stream uploader begin count = %d, want 1", stt.streamUploaderUsed)
	}
	if stt.streamUploader == nil || len(stt.streamUploader.writes) != 1 {
		t.Fatalf("stream uploader writes = %#v, want one PCM write", stt.streamUploader)
	}
	if len(resp.Results) != 1 || !strings.Contains(resp.Results[0].Detail, "streaming") {
		t.Fatalf("unexpected results: %#v", resp.Results)
	}
}

func TestServerSTTConfigTestLiveSessionFallsBackToOneShot(t *testing.T) {
	sentChunk := false
	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_recording":
			return audioResponse{Status: "OK", SessionID: stringUint64(42)}, nil
		case "read_record_chunk":
			if !sentChunk {
				sentChunk = true
				return audioResponse{Status: "OK"}, []byte{10, 0, 11, 0, 12, 0, 13, 0}
			}
			return audioResponse{Status: "OK", EndOfStream: true}, nil
		case "stop_recording":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
				Channels:   1,
				BitWidth:   16,
			},
		}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	stt := &stubSTTClient{transcript: "one-shot result"}
	previousFactory := newSTTClientFromConfigForLiveTest
	newSTTClientFromConfigForLiveTest = func(cfg Config) (STTClient, error) {
		if cfg.STT.Provider != "openai-whisper" {
			t.Fatalf("provider = %q, want openai-whisper", cfg.STT.Provider)
		}
		return stt, nil
	}
	defer func() {
		newSTTClientFromConfigForLiveTest = previousFactory
	}()

	startBody := `{"stt_values":{"provider":"openai-whisper","api_key":"sk-test","model":"whisper-1","base_url":"http://127.0.0.1:9"},"audio_values":{"socket":"` + socketPath + `","sample_rate":16000,"channels":1,"bit_width":16}}`
	startReq := httptest.NewRequest(http.MethodPost, "/api/config-test/stt/start", strings.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	server.handleSTTConfigTestStart(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("unexpected start status: %d body=%s", startRec.Code, startRec.Body.String())
	}

	stopReq := httptest.NewRequest(http.MethodPost, "/api/config-test/stt/stop", nil)
	stopRec := httptest.NewRecorder()
	server.handleSTTConfigTestStop(stopRec, stopReq)
	if stopRec.Code != http.StatusOK {
		t.Fatalf("unexpected stop status: %d body=%s", stopRec.Code, stopRec.Body.String())
	}

	var resp struct {
		OK         bool   `json:"ok"`
		Transcript string `json:"transcript"`
		Results    []struct {
			Check  string `json:"check"`
			Passed bool   `json:"passed"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	if err := json.NewDecoder(stopRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %#v", resp)
	}
	if resp.Transcript != "one-shot result" {
		t.Fatalf("transcript = %q, want one-shot result", resp.Transcript)
	}
	if len(stt.inputs) != 1 {
		t.Fatalf("TranscribeWAV calls = %d, want 1", len(stt.inputs))
	}
	if stt.streamUploaderUsed != 0 {
		t.Fatalf("stream uploader begin count = %d, want 0", stt.streamUploaderUsed)
	}
	if len(resp.Results) != 1 || !strings.Contains(resp.Results[0].Detail, "one-shot") {
		t.Fatalf("unexpected results: %#v", resp.Results)
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
		withTestConfigDir(t, Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{Socket: socketPath, SampleRate: 16000},
		}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)
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
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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

	var shell ToolDescriptor
	for _, descriptor := range resp.Tools {
		if descriptor.Name == "shell" {
			shell = descriptor
			break
		}
	}
	if shell.Name == "" {
		t.Fatalf("expected shell in tool catalog: %#v", resp.Tools)
	}
	if shell.HTTP.Path != "/api/tools/shell" {
		t.Fatalf("unexpected tool path: %#v", shell.HTTP)
	}
	if shell.ExampleInput != `{"command":"pwd"}` {
		t.Fatalf("unexpected example input: %q", shell.ExampleInput)
	}
}

func TestServerToolInvokeEndpointAcceptsStructuredJSON(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      `{"status":"ok"}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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

func TestServerToolInvokeContinuesAfterClientDisconnect(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	tool := &stubTool{
		name:        "open_app",
		description: "Open an app.",
		callFn: func(ctx context.Context, _ string) (string, error) {
			close(started)
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"open_app": tool}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/tools/open_app", bytes.NewBufferString(`{"input":{"app":"WeChat"}}`)).WithContext(requestCtx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleToolInvoke(rec, req)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool invocation did not start")
	}
	cancelRequest()
	select {
	case <-done:
		t.Fatal("tool invocation stopped when the client request was canceled")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tool invocation did not finish after release")
	}

	var resp ToolInvokeResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.IsError || resp.Output != "ok" {
		t.Fatalf("response = %#v, want successful detached execution", resp)
	}
}

func TestHTTPToolExecutionSurvivesClientDisconnectForHIDTools(t *testing.T) {
	for _, toolName := range []string{
		"keyboard_tap",
		"quick_action",
		"open_app",
		"enter_text",
		"mouse_click",
		"mouse_move",
		"mouse_scroll",
		"run_script",
		"touch_gesture",
		"wheel_nudge",
	} {
		t.Run(toolName, func(t *testing.T) {
			if !httpToolExecutionSurvivesClientDisconnect(toolName) {
				t.Fatalf("httpToolExecutionSurvivesClientDisconnect(%q) = false, want true", toolName)
			}
		})
	}
	if httpToolExecutionSurvivesClientDisconnect("screenshot") {
		t.Fatal("screenshot should keep normal client-cancellation behavior")
	}
}

func TestServerToolInvokeUsesUnifiedExecutionAndNormalizesInput(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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

func TestServerDoesNotExposeActivateSkillOverHTTP(t *testing.T) {
	index := NewSkillIndex()
	index.skills["planner"] = &SkillDefinition{
		Name:         "planner",
		Description:  "Planning skill",
		Instructions: "Plan before acting.",
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)
	server := newServerForTest(runtime)

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
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"skill_manage": NewSkillManageTool(t.TempDir(), ""),
			"skill_list":   NewSkillListTool(t.TempDir()),
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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
		t.Fatalf("expected skill_list to remain exposed: %s", catalogRec.Body.String())
	}

	invokeReq := httptest.NewRequest(http.MethodPost, "/api/tools/skill_manage", bytes.NewBufferString(`{"raw_input":"{\"action\":\"list\"}"}`))
	invokeRec := httptest.NewRecorder()
	server.handleToolInvoke(invokeRec, invokeReq)
	if invokeRec.Code != http.StatusNotFound {
		t.Fatalf("expected skill_manage HTTP invoke to be hidden, got %d body=%s", invokeRec.Code, invokeRec.Body.String())
	}
}

func TestServerToolSkillsEndpointReturnsGeneratedSkills(t *testing.T) {
	tool := &stubTool{
		name:        "shell",
		description: "Run shell commands.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}}),
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": tool,
		}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)

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
	hasShell := false
	for _, name := range skill.ToolNames {
		if name == "shell" {
			hasShell = true
			break
		}
	}
	if !hasShell {
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

func startFakeFrameServiceSocket(t *testing.T, handler func(map[string]any) (string, []byte)) string {
	t.Helper()

	socketDir, err := os.MkdirTemp("/tmp", "aiden-frame-test-*")
	if err != nil {
		t.Fatalf("create fake frame socket dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(socketDir)
	})

	socketPath := filepath.Join(socketDir, "frame.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on fake frame socket: %v", err)
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

				header, payload, err := readUDSMessage(conn)
				if err != nil {
					t.Errorf("fake frame read request: %v", err)
					return
				}
				if len(payload) != 0 {
					t.Errorf("fake frame expected empty request payload, got %d bytes", len(payload))
					return
				}

				var req map[string]any
				if err := json.Unmarshal(header, &req); err != nil {
					t.Errorf("fake frame decode request: %v", err)
					return
				}

				respHeader, respPayload := handler(req)
				if err := writeUDSMessage(conn, []byte(respHeader), respPayload); err != nil {
					t.Errorf("fake frame write response: %v", err)
					return
				}
			}()
		}
	}()

	return socketPath
}

func newBenchmarkSeedMemoryServer(t *testing.T) (*Server, string) {
	t.Helper()
	configDir := ensureTestConfigDir(t, t.TempDir())
	streamingDisabled := false
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:                configDir,
			Model:                    ModelConfig{Provider: "fake"},
			Benchmark:                BenchmarkConfig{Token: "test-benchmark-token"},
			Instruction:              "Answer directly.",
			VoiceStreamingTTSEnabled: &streamingDisabled,
			VoiceToolCallSpeech:      &streamingDisabled,
		},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(filepath.Join(configDir, "memory")),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	server := newServerForTest(runtime)
	t.Cleanup(func() { server.bridge.queue.Stop() })
	return server, configDir
}

func TestHandleBenchmarkSeedMemorySucceeds(t *testing.T) {
	server, configDir := newBenchmarkSeedMemoryServer(t)
	body := `{"id":"personamem_test_seed_1","type":"preference","title":"Test seed","content":"Seeded fixture content.","tags":["t1"],"priority":80}`
	req := httptest.NewRequest(http.MethodPost, "/api/benchmark/seed_memory", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-benchmark-token")
	rec := httptest.NewRecorder()

	server.handleBenchmarkSeedMemory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "seeded" || resp["id"] != "personamem_test_seed_1" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	memPath := filepath.Join(configDir, "memory", "long_term", "memories", "personamem_test_seed_1.md")
	data, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read seeded memory file: %v", err)
	}
	if !strings.Contains(string(data), "Seeded fixture content.") {
		t.Fatalf("memory file missing content, got: %s", string(data))
	}
	if !strings.Contains(string(data), "id: personamem_test_seed_1") {
		t.Fatalf("memory file missing fixed id, got: %s", string(data))
	}
}

func TestHandleBenchmarkSeedMemoryRequiresBenchmarkToken(t *testing.T) {
	server, _ := newBenchmarkSeedMemoryServer(t)
	body := `{"id":"personamem_test_seed_1","content":"Seeded fixture content."}`
	req := httptest.NewRequest(http.MethodPost, "/api/benchmark/seed_memory", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	server.handleBenchmarkSeedMemory(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkPhoneBridgeStateAppliesIOSPiPPolicy(t *testing.T) {
	server, _ := newBenchmarkSeedMemoryServer(t)
	body := `{
		"connected": false,
		"platform": "ios",
		"phone_id": "benchmark-ios-pip",
		"app_state": "background",
		"return_entry": "dynamic_island",
		"return_entry_available": true,
		"pip_bridge_enabled": true,
		"fgs_bridge_enabled": false
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/benchmark/phone_bridge_state", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-benchmark-token")
	rec := httptest.NewRecorder()

	server.handleBenchmarkPhoneBridgeState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	status := server.bridge.getStatus()
	if status.Platform != "ios" || status.AppState != "background" {
		t.Fatalf("unexpected bridge status: %+v", status)
	}
	if status.AppStateUpdatedAt == nil || status.PipBridgeEnabled == nil || !*status.PipBridgeEnabled {
		t.Fatalf("PiP benchmark status was not made fresh: %+v", status)
	}
}

func TestHandleBenchmarkPhoneBridgeStateRequiresBenchmarkToken(t *testing.T) {
	server, _ := newBenchmarkSeedMemoryServer(t)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/benchmark/phone_bridge_state",
		bytes.NewBufferString(`{"platform":"ios","app_state":"background"}`),
	)
	rec := httptest.NewRecorder()

	server.handleBenchmarkPhoneBridgeState(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBenchmarkPhoneBridgeStateRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "platform", body: `{"platform":"windows","app_state":"active"}`},
		{name: "app state", body: `{"platform":"ios","app_state":"suspended"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newBenchmarkSeedMemoryServer(t)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/benchmark/phone_bridge_state",
				bytes.NewBufferString(tc.body),
			)
			req.Header.Set("Authorization", "Bearer test-benchmark-token")
			rec := httptest.NewRecorder()

			server.handleBenchmarkPhoneBridgeState(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleBenchmarkSeedMemoryOverwritesSameID(t *testing.T) {
	server, configDir := newBenchmarkSeedMemoryServer(t)
	mkReq := func(content string) *http.Request {
		body := fmt.Sprintf(`{"id":"personamem_overwrite_1","type":"fact","content":%q}`, content)
		req := httptest.NewRequest(http.MethodPost, "/api/benchmark/seed_memory", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-benchmark-token")
		return req
	}

	rec := httptest.NewRecorder()
	server.handleBenchmarkSeedMemory(rec, mkReq("first content"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first seed status: %d body=%s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	server.handleBenchmarkSeedMemory(rec2, mkReq("second content"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second seed status: %d body=%s", rec2.Code, rec2.Body.String())
	}

	memPath := filepath.Join(configDir, "memory", "long_term", "memories", "personamem_overwrite_1.md")
	data, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read seeded memory file: %v", err)
	}
	if !strings.Contains(string(data), "second content") {
		t.Fatalf("expected overwrite to second content, got: %s", string(data))
	}
	if strings.Contains(string(data), "first content") {
		t.Fatalf("first content should be overwritten, still present: %s", string(data))
	}
}

func TestHandleBenchmarkSeedMemoryRejectsMissingFields(t *testing.T) {
	server, _ := newBenchmarkSeedMemoryServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"missing id", `{"content":"x"}`},
		{"missing content", `{"id":"x"}`},
		{"blank id", `{"id":" ","content":"x"}`},
		{"blank content", `{"id":"x","content":"  "}`},
		{"malformed json", `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/benchmark/seed_memory", bytes.NewBufferString(tc.body))
			req.Header.Set("Authorization", "Bearer test-benchmark-token")
			rec := httptest.NewRecorder()
			server.handleBenchmarkSeedMemory(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleBenchmarkSeedMemoryRejectsNonPost(t *testing.T) {
	server, _ := newBenchmarkSeedMemoryServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/benchmark/seed_memory", nil)
	req.Header.Set("Authorization", "Bearer test-benchmark-token")
	rec := httptest.NewRecorder()
	server.handleBenchmarkSeedMemory(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d body=%s", rec.Code, rec.Body.String())
	}
}
