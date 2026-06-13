package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"

	ttsmodule "aiden-agent/internal/agent/tts"
)

func TestAudioDialogFinishManualUtterancePreservesTail(t *testing.T) {
	vad, err := NewAudioVADWithScorer(AudioVADConfig{
		SampleRate:      16000,
		SilenceMs:       650,
		MinSpeechMs:     300,
		AlwaysBuffer:    true,
		SpeechThreshold: 0.5,
	}, &sequenceScorer{probabilities: []float64{0.1}})
	if err != nil {
		t.Fatalf("NewAudioVADWithScorer() error = %v", err)
	}

	dialog := &AudioDialog{vad: vad}
	frame := make([]int16, vad.FrameSamples())
	for i := range frame {
		frame[i] = 1000
	}
	if _, err := dialog.ProcessVADFrame(frame); err != nil {
		t.Fatalf("ProcessVADFrame() error = %v", err)
	}

	got := dialog.FinishManualUtterance([]int16{7, 8, 9})
	if len(got) != len(frame)+3 {
		t.Fatalf("FinishManualUtterance() len = %d, want %d", len(got), len(frame)+3)
	}
	if got[len(got)-3] != 7 || got[len(got)-2] != 8 || got[len(got)-1] != 9 {
		t.Fatalf("FinishManualUtterance() tail = %#v, want [7 8 9]", got[len(got)-3:])
	}
}

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

	socketDir, err := os.MkdirTemp("/tmp", "aiden-audio-dialog-*")
	if err != nil {
		t.Fatalf("create temp socket dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(socketDir)
	})
	socketPath := filepath.Join(socketDir, "audio.sock")
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
		TTS:         TTSConfig{Provider: "minimax-ws", APIKey: "test-key"},
		Audio:       AudioConfig{Socket: "/tmp/audio.sock", SampleRate: 16000},
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
	if got := dialog.VADFrameSamples(); got != 512 {
		t.Fatalf("VADFrameSamples() = %d, want 512 for Silero RKNN VAD", got)
	}
}

func TestNewAudioDialogIgnoresInvalidOptionalTTS(t *testing.T) {
	dialog, err := NewAudioDialog(Config{
		Model:     ModelConfig{Provider: "fake"},
		TTS:       TTSConfig{Provider: "missing-provider", APIKey: "test-key"},
		Audio:     AudioConfig{Socket: "/tmp/audio.sock", SampleRate: 16000},
		InputMode: "audio",
	})
	if err != nil {
		t.Fatalf("NewAudioDialog() error = %v", err)
	}
	if dialog.ttsManager != nil {
		t.Fatalf("ttsManager = %#v, want nil after optional TTS init failure", dialog.ttsManager)
	}
}

func TestProcessUtteranceAudioModeSendsWAVAttachmentToRuntime(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("heard it"),
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

	provider := &recordingTTSProvider{name: "dialog-provider"}
	audioClient := NewAudioServiceClient(startTTSPlaybackAudioSocket(t))
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "audio",
			VoiceStreamingTTSEnabled: boolPtr(false),
		},
		audioClient:  audioClient,
		ttsManager:   ttsmodule.NewProviderManager(provider, nil),
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}

	if err := dialog.ProcessUtterance(context.Background(), []int16{100, -100, 200, -200}, runtime); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}
	if len(model.messages) != 1 {
		t.Fatalf("expected one default-mode planner model call, got %d", len(model.messages))
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
	if got := provider.texts(); len(got) != 1 || got[0] != "heard it" {
		t.Fatalf("unexpected TTS texts: %#v", got)
	}
}

func TestAudioDialogSpeaksToolDescriptionAsynchronously(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先检查当前音量。"}`, "当前音量是 42。"),
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
	provider := &recordingTTSProvider{name: "dialog-provider"}
	toolSpeech := true
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "audio",
			VoiceStreamingTTSEnabled: boolPtr(false),
			VoiceToolCallSpeech:      &toolSpeech,
		},
		audioClient:  NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
		ttsManager:   ttsmodule.NewProviderManager(provider, nil),
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}

	if err := dialog.ProcessUtterance(context.Background(), []int16{100, -100, 200, -200}, runtime); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}
	waitForProviderTextCount(t, provider, 2)
	texts := provider.texts()
	if len(texts) != 2 {
		t.Fatalf("expected tool description and final answer TTS, got %#v", texts)
	}
	if !containsString(texts, "我先检查当前音量。") || !containsString(texts, "当前音量是 42。") {
		t.Fatalf("unexpected TTS texts: %#v", texts)
	}
}

func TestAudioDialogDoesNotSpeakEnterSleepToolDescription(t *testing.T) {
	toolSpeech := true
	provider := &recordingTTSProvider{name: "dialog-provider"}
	dialog := &AudioDialog{
		config: Config{
			VoiceToolCallSpeech: &toolSpeech,
		},
		audioClient: NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
	}

	dialog.HandleRunEvent(context.Background(), RunEvent{
		Type:        "tool_call",
		ToolName:    "enter_sleep",
		Description: "用户让我休息，我准备进入睡眠模式。",
	})

	assertNoProviderTextWithin(t, provider, 200*time.Millisecond)
}

func TestAudioDialogStreamingSpeechErrorDoesNotHideSleepRequest(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("enter_sleep", `{"__arg1":"{\"reason\":\"user asked\"}"}`, "I will wait for the next wakeup."),
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
		audioClient: NewAudioServiceClient(filepath.Join(t.TempDir(), "missing-audio.sock")),
		ttsManager:  ttsmodule.NewProviderManager(&recordingTTSProvider{name: "dialog-provider"}, nil),
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

func TestAudioDialogPersistVoiceTurnWritesUserAndAssistant(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	dialog := &AudioDialog{
		config:       Config{Audio: AudioConfig{SampleRate: 16000}},
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	dialog.SetHistoryStore(store)

	utterance := make([]int16, 16000) // 1 second
	dialog.persistVoiceTurn(
		TurnInput{Transcript: "  hello there  "},
		RunResult{EpisodeID: "ep-123", Output: "  hi back  "},
		utterance,
	)

	messages, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(messages), messages)
	}

	user := messages[0]
	if user.Type != "user" || user.Content != "hello there" || user.Source != "voice" || user.EpisodeID != "ep-123" {
		t.Fatalf("user message = %#v", user)
	}
	if user.Timestamp.IsZero() {
		t.Fatalf("user message timestamp is zero")
	}

	assistant := messages[1]
	if assistant.Type != "assistant" || assistant.Content != "hi back" || assistant.Source != "voice" || assistant.EpisodeID != "ep-123" {
		t.Fatalf("assistant message = %#v", assistant)
	}
}

func TestAudioDialogPersistVoiceTurnNoTranscriptOnlyAssistant(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	dialog := &AudioDialog{
		config:       Config{Audio: AudioConfig{SampleRate: 16000}},
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	dialog.SetHistoryStore(store)

	utterance := make([]int16, 16000)
	dialog.persistVoiceTurn(
		TurnInput{Transcript: ""},
		RunResult{EpisodeID: "ep-456", Output: "answer"},
		utterance,
	)

	messages, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %#v", len(messages), messages)
	}
	if messages[0].Type != "assistant" || messages[0].Source != "voice" || messages[0].Content != "answer" {
		t.Fatalf("assistant message = %#v", messages[0])
	}
}

func TestAudioDialogPersistVoiceTurnNilStoreDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("persistVoiceTurn panicked with nil store: %v", r)
		}
	}()
	dialog := &AudioDialog{
		config:       Config{Audio: AudioConfig{SampleRate: 16000}},
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	utterance := make([]int16, 16000)
	dialog.persistVoiceTurn(
		TurnInput{Transcript: "anything"},
		RunResult{Output: "anything"},
		utterance,
	)
}

func TestAudioDialogPersistVoiceTurnEmptyOutputSkipsAssistant(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	dialog := &AudioDialog{
		config:       Config{Audio: AudioConfig{SampleRate: 16000}},
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	dialog.SetHistoryStore(store)

	utterance := make([]int16, 16000)
	dialog.persistVoiceTurn(
		TurnInput{Transcript: "user said this"},
		RunResult{EpisodeID: "ep-789", Output: "   "},
		utterance,
	)

	messages, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Type != "user" || messages[0].Content != "user said this" {
		t.Fatalf("user message = %#v", messages[0])
	}
}

func TestAudioDialogProcessUtteranceAppendsToHistoryStore(t *testing.T) {
	store := NewChatHistoryStore(t.TempDir())
	model := &scriptedModel{
		responses: roleDirectResponses("voice reply"),
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

	provider := &recordingTTSProvider{name: "dialog-provider"}
	audioClient := NewAudioServiceClient(startTTSPlaybackAudioSocket(t))
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "audio",
			VoiceStreamingTTSEnabled: boolPtr(false),
		},
		audioClient:  audioClient,
		ttsManager:   ttsmodule.NewProviderManager(provider, nil),
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	dialog.SetHistoryStore(store)

	if err := dialog.ProcessUtterance(context.Background(), []int16{100, -100, 200, -200}, runtime); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}

	messages, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Audio mode does not produce a transcript, so we expect only the assistant message.
	if len(messages) != 1 {
		t.Fatalf("expected 1 message (assistant only, no transcript in audio mode), got %d: %#v", len(messages), messages)
	}
	if messages[0].Type != "assistant" || messages[0].Source != "voice" || messages[0].Content != "voice reply" {
		t.Fatalf("assistant message = %#v", messages[0])
	}
}

func TestAudioDialogProcessUtteranceSavesAudioFile(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		ConfigDir: tmpDir,
		InputMode: "audio",
		Audio:     AudioConfig{SampleRate: 16000},
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			StoragePath: filepath.Join(tmpDir, "audio"),
		},
		Model: ModelConfig{Provider: "fake"},
	}

	dialog, err := NewAudioDialog(cfg)
	if err != nil {
		t.Fatal(err)
	}

	historyStore := NewChatHistoryStore(filepath.Join(tmpDir, "history"))
	dialog.SetHistoryStore(historyStore)

	// Manually call persistVoiceTurn with a transcript to test audio file saving
	utterance := make([]int16, 16000) // 1 second
	input := TurnInput{Transcript: "hello"}
	result := RunResult{EpisodeID: "ep-test", Output: "hi"}

	dialog.persistVoiceTurn(input, result, utterance)

	// Verify audio file saved
	files, err := os.ReadDir(filepath.Join(tmpDir, "audio"))
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 audio file, got %d", len(files))
	}

	if !strings.HasSuffix(files[0].Name(), ".wav") {
		t.Errorf("Audio file should be WAV: %s", files[0].Name())
	}

	// Verify message has audio_file field
	messages, err := historyStore.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var userMsg *Message
	for i := range messages {
		if messages[i].Type == "user" {
			userMsg = &messages[i]
			break
		}
	}

	if userMsg == nil {
		t.Fatal("No user message found")
	}

	if userMsg.AudioFile == "" {
		t.Error("User message should have audio_file set")
	}
	if userMsg.AudioDurationMs == 0 {
		t.Error("User message should have audio_duration_ms set")
	}

	// Verify the audio file path is absolute and exists
	if _, err := os.Stat(userMsg.AudioFile); err != nil {
		t.Errorf("Audio file should exist at %s: %v", userMsg.AudioFile, err)
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func waitForProviderTextCount(t *testing.T, provider *recordingTTSProvider, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := len(provider.texts())
		if got >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func assertNoProviderTextWithin(t *testing.T, provider *recordingTTSProvider, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		got := provider.texts()
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

	if unixListener, ok := listener.(*net.UnixListener); ok {
		_ = unixListener.SetDeadline(time.Now().Add(2 * time.Second))
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			t.Errorf("accept unix socket: %v", err)
			close(ready)
			return
		}

		msg, err := readUdsMessage(conn)
		if err != nil {
			conn.Close()
			t.Errorf("read request: %v", err)
			close(ready)
			return
		}
		var req audioRequest
		if err := json.Unmarshal([]byte(msg.HeaderJSON), &req); err != nil {
			conn.Close()
			t.Errorf("unmarshal request: %v", err)
			close(ready)
			return
		}

		resp := `{"status":"OK"}`
		if req.Op == "start_playback" {
			resp = `{"status":"OK","session_id":"7"}`
		}
		if req.Op == "health" {
			resp = `{"status":"OK","recording_active":false,"playback_active":false,"record_sessions":0,"playback_sessions":0}`
		}
		if req.Op == "start_recording" {
			resp = `{"status":"OK","session_id":"42"}`
		}
		if err := writeUdsMessage(conn, udsMessage{HeaderJSON: resp}); err != nil {
			conn.Close()
			t.Errorf("write response: %v", err)
			close(ready)
			return
		}
		conn.Close()

		if req.Op == "start_recording" {
			close(ready)
			return
		}
	}
}
