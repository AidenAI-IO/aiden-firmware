package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestAudioDialogReadRecordChunkRequiresActiveRecording(t *testing.T) {
	dialog := &AudioDialog{}

	chunk, err := dialog.ReadRecordChunk(200)
	if err == nil {
		t.Fatal("expected error when recording is inactive")
	}
	if chunk != nil {
		t.Fatalf("expected nil chunk, got %#v", chunk)
	}
}

func TestAudioDialogStopRecordingClearsLocalStateOnStopError(t *testing.T) {
	missingSocket := filepath.Join(t.TempDir(), "missing-audio.sock")
	dialog := &AudioDialog{
		audioClient:  NewAudioServiceClient(missingSocket),
		recordActive: true,
		sessionID:    123,
	}

	if err := dialog.StopRecording(); err == nil {
		t.Fatal("expected stop recording error")
	}
	if dialog.recordActive {
		t.Fatal("recordActive should be cleared after stop attempt")
	}
	if dialog.sessionID != 0 {
		t.Fatalf("sessionID = %d, want 0", dialog.sessionID)
	}
}

func TestAudioDialogStartRecordingRetriesUntilAudioServiceAvailable(t *testing.T) {
	oldTimeout := recordingStartRetryTimeout
	oldInterval := recordingStartRetryInterval
	recordingStartRetryTimeout = 300 * time.Millisecond
	recordingStartRetryInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		recordingStartRetryTimeout = oldTimeout
		recordingStartRetryInterval = oldInterval
	})

	socketPath := filepath.Join(t.TempDir(), "audio.sock")
	ready := make(chan struct{})
	go serveDelayedStartRecording(t, socketPath, ready)

	dialog := &AudioDialog{
		config:      Config{Audio: AudioConfig{SampleRate: 16000, Channels: 1, BitWidth: 16}},
		audioClient: NewAudioServiceClient(socketPath),
	}
	if err := dialog.StartRecording(); err != nil {
		t.Fatalf("StartRecording() error = %v", err)
	}
	if !dialog.recordActive {
		t.Fatal("recordActive = false, want true")
	}
	if dialog.sessionID != 42 {
		t.Fatalf("sessionID = %d, want 42", dialog.sessionID)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("delayed audio service did not handle start_recording")
	}
}

func TestNewAudioDialogAudioWakeupUsesDirectAudioPath(t *testing.T) {
	dialog, err := NewAudioDialog(Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax"},
		Audio:       AudioConfig{Socket: "/tmp/audio.sock", SampleRate: 32000},
		InputMode:   "audio",
		TriggerMode: "wakeup",
	})
	if err != nil {
		t.Fatalf("NewAudioDialog() error = %v", err)
	}

	if dialog.sttClient != nil {
		t.Fatal("audio input mode should not create an STT client")
	}
	if dialog.audioClient.socketPath != "/tmp/audio.sock" {
		t.Fatalf("audio socket = %q, want /tmp/audio.sock", dialog.audioClient.socketPath)
	}
	if dialog.vad.alwaysBuffer {
		t.Fatal("wakeup trigger mode should use normal VAD buffering")
	}
	if got := dialog.VADFrameSamples(); got != 960 {
		t.Fatalf("VADFrameSamples() = %d, want 960 for 32kHz", got)
	}
}

func TestProcessUtteranceAudioModeSendsWAVAttachmentToRuntime(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "heard it",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use attached audio.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	tts := &fakeTTSClient{}
	audioClient := NewAudioServiceClient("/tmp/audio.sock")
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "audio",
			VoiceStreamingTTSEnabled: boolPtr(false),
		},
		audioClient: audioClient,
		ttsClient:   tts,
	}

	if err := dialog.ProcessUtterance(context.Background(), []int16{100, -100, 200, -200}, runtime); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}
	if len(model.messages) != 1 {
		t.Fatalf("expected one model call, got %d", len(model.messages))
	}

	userMessage := model.messages[0][len(model.messages[0])-1]
	var text string
	var audio []byte
	for _, part := range userMessage.Parts {
		switch p := part.(type) {
		case llms.TextContent:
			text = p.Text
		case llms.BinaryContent:
			if p.MIMEType == "audio/wav" {
				audio = p.Data
			}
		}
	}

	if !strings.Contains(text, "recording.wav") {
		t.Fatalf("expected audio attachment name in prompt text, got %q", text)
	}
	if len(audio) < 48 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		t.Fatalf("expected WAV binary attachment, got %d bytes", len(audio))
	}
	if len(tts.texts) != 1 || tts.texts[0] != "heard it" {
		t.Fatalf("unexpected TTS texts: %#v", tts.texts)
	}
	if tts.audio != audioClient {
		t.Fatal("expected TTS to receive the dialog audio client")
	}
}

func TestAudioDialogSpeaksToolDescriptionAsynchronously(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}","description":"我先检查当前音量。"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "当前音量是 42。",
				}},
			},
		},
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
	tts := &fakeTTSClient{}
	toolSpeech := true
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "audio",
			VoiceStreamingTTSEnabled: boolPtr(false),
			VoiceToolCallSpeech:      &toolSpeech,
		},
		audioClient: NewAudioServiceClient("/tmp/audio.sock"),
		ttsClient:   tts,
	}

	if err := dialog.ProcessUtterance(context.Background(), []int16{100, -100, 200, -200}, runtime); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}
	waitForTTSCount(t, tts, 2)
	if len(tts.texts) != 2 {
		t.Fatalf("expected tool description and final answer TTS, got %#v", tts.texts)
	}
	if !containsString(tts.texts, "我先检查当前音量。") || !containsString(tts.texts, "当前音量是 42。") {
		t.Fatalf("unexpected TTS texts: %#v", tts.texts)
	}
	if !containsBool(tts.deadlineSet, true) || !containsBool(tts.deadlineSet, false) {
		t.Fatalf("unexpected TTS deadline use: %#v", tts.deadlineSet)
	}
}

func TestAudioDialogDoesNotSpeakEnterSleepToolDescription(t *testing.T) {
	toolSpeech := true
	tts := &fakeTTSClient{}
	dialog := &AudioDialog{
		config: Config{
			VoiceToolCallSpeech: &toolSpeech,
		},
		audioClient: NewAudioServiceClient("/tmp/audio.sock"),
		ttsClient:   tts,
	}

	dialog.HandleRunEvent(context.Background(), RunEvent{
		Type:        "tool_call",
		ToolName:    "enter_sleep",
		Description: "用户让我休息，我准备进入睡眠模式。",
	})

	assertNoTTSCallsWithin(t, tts, 200*time.Millisecond)
}

func TestAudioDialogStreamingSpeechErrorDoesNotHideSleepRequest(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "enter_sleep",
							Arguments: `{"__arg1":"{\"reason\":\"user asked\"}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "I will wait for the next wakeup.",
				}},
			},
		},
	}
	controller := NewSleepController()
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"enter_sleep": NewEnterSleepTool(controller),
		}},
		NewSkillIndex(),
	)
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			VoiceStreamingTTSEnabled: boolPtr(true),
		},
		audioClient: NewAudioServiceClient("/tmp/audio.sock"),
		ttsClient:   &fakeTTSClient{err: errors.New("start playback failed")},
	}

	result, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "go to sleep"}, runtime)
	if err != nil {
		t.Fatalf("RunAgentTurn() error = %v", err)
	}
	if !result.SleepRequested {
		t.Fatal("SleepRequested = false, want true even when streaming speech fails")
	}
	if result.SpeechStreamed {
		t.Fatal("SpeechStreamed = true, want false when TTS playback failed before successful speech")
	}
}

type fakeTTSClient struct {
	mu          sync.Mutex
	texts       []string
	audio       *AudioServiceClient
	deadlineSet []bool
	err         error
}

func boolPtr(value bool) *bool {
	return &value
}

func waitForTTSCount(t *testing.T, tts *fakeTTSClient, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tts.mu.Lock()
		got := len(tts.texts)
		tts.mu.Unlock()
		if got >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func assertNoTTSCallsWithin(t *testing.T, tts *fakeTTSClient, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		tts.mu.Lock()
		got := append([]string(nil), tts.texts...)
		tts.mu.Unlock()
		if len(got) != 0 {
			t.Fatalf("unexpected TTS calls: %#v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsBool(values []bool, want bool) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (c *fakeTTSClient) TextToSpeechStream(ctx context.Context, text string, audio *AudioServiceClient) error {
	_, hasDeadline := ctx.Deadline()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, text)
	c.audio = audio
	c.deadlineSet = append(c.deadlineSet, hasDeadline)
	return c.err
}

func serveDelayedStartRecording(t *testing.T, socketPath string, ready chan<- struct{}) {
	t.Helper()
	time.Sleep(30 * time.Millisecond)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Errorf("listen unix socket: %v", err)
		close(ready)
		return
	}
	defer os.Remove(socketPath)
	defer listener.Close()

	conn, err := listener.Accept()
	if err != nil {
		t.Errorf("accept unix socket: %v", err)
		close(ready)
		return
	}
	defer conn.Close()

	msg, err := readUdsMessage(conn)
	if err != nil {
		t.Errorf("read request: %v", err)
		close(ready)
		return
	}
	var req audioRequest
	if err := json.Unmarshal([]byte(msg.HeaderJSON), &req); err != nil {
		t.Errorf("unmarshal request: %v", err)
		close(ready)
		return
	}
	if req.Op != "start_recording" {
		t.Errorf("op = %q, want start_recording", req.Op)
	}
	resp := `{"status":"OK","session_id":"42"}`
	if err := writeUdsMessage(conn, udsMessage{HeaderJSON: resp}); err != nil {
		t.Errorf("write response: %v", err)
	}
	close(ready)
}
