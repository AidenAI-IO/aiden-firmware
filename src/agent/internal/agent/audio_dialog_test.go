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

	ttsmodule "aiden-agent/internal/agent/tts"
)

func firstAudioDialogTestMessageOfType(messages []Message, messageType string) (Message, bool) {
	for _, message := range messages {
		if message.Type == messageType {
			return message, true
		}
	}
	return Message{}, false
}

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
	uploader := newBlockingFinalizeUploader("")
	dialog := &AudioDialog{
		audioClient:  NewAudioServiceClient(missingSocket),
		recordActive: true,
		sessionID:    123,
		recordSTT:    &streamingSTTSession{uploader: uploader},
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
	select {
	case <-uploader.closed:
	case <-time.After(time.Second):
		t.Fatal("expected streaming session to be closed on stop error")
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

func TestAudioDialogStartRecordingRollsBackOnVADResetFailure(t *testing.T) {
	var (
		opMu sync.Mutex
		ops  []string
	)
	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		opMu.Lock()
		ops = append(ops, req.Op)
		opMu.Unlock()
		switch req.Op {
		case "start_recording":
			return audioResponse{Status: "OK", SessionID: stringUint64(42)}, nil
		default:
			return audioResponse{Status: "OK"}, nil
		}
	})

	scorer := &sequenceScorer{resetErr: errors.New("vad helper crashed")}
	vad, err := NewAudioVADWithScorer(AudioVADConfig{
		SampleRate:      16000,
		SilenceMs:       650,
		MinSpeechMs:     300,
		AlwaysBuffer:    true,
		SpeechThreshold: 0.5,
	}, scorer)
	if err != nil {
		t.Fatalf("NewAudioVADWithScorer() error = %v", err)
	}

	sttClient := &stubSTTClient{
		supportsStreaming: true,
		streamUploader:    &stubSTTStreamUploader{},
	}
	dialog := &AudioDialog{
		config: Config{
			InputMode: "stt",
			Audio:     AudioConfig{Socket: socketPath, SampleRate: 16000, Channels: 1, BitWidth: 16},
		},
		audioClient: NewAudioServiceClient(socketPath),
		sttClient:   sttClient,
		vad:         vad,
	}

	if err := dialog.StartRecording(); err == nil {
		t.Fatal("expected StartRecording error when VAD reset fails")
	}

	if dialog.recordActive {
		t.Fatal("recordActive should be false after VAD failure")
	}
	if dialog.sessionID != 0 {
		t.Fatalf("sessionID = %d, want 0 after VAD failure", dialog.sessionID)
	}
	if dialog.recordReader != nil {
		t.Fatal("recordReader should be nil after VAD failure")
	}
	if got := dialog.currentRecordSTT(); got != nil {
		t.Fatal("recordSTT should be nil after VAD failure")
	}
	if sttClient.streamUploaderUsed != 0 {
		t.Fatalf("streamUploaderUsed = %d, want 0; streaming session should not be opened when VAD reset fails", sttClient.streamUploaderUsed)
	}
	opMu.Lock()
	defer opMu.Unlock()
	for _, op := range ops {
		if op == "start_recording" {
			t.Fatalf("audio service received %q before VAD validated; ops = %v", op, ops)
		}
	}
}

func TestAudioDialogPrepareTurnInputUsesStreamingTranscriptFromRecording(t *testing.T) {
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
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
			return audioResponse{Status: "OK"}, nil
		}
	})

	sttClient := &stubSTTClient{
		supportsStreaming: true,
		streamUploader:    &stubSTTStreamUploader{transcript: "streaming result"},
	}
	dialog := &AudioDialog{
		config: Config{
			InputMode: "stt",
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
				Channels:   1,
				BitWidth:   16,
			},
		},
		audioClient:  NewAudioServiceClient(socketPath),
		sttClient:    sttClient,
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}

	if err := dialog.StartRecording(); err != nil {
		t.Fatalf("StartRecording() error = %v", err)
	}
	chunk, err := dialog.ReadRecordChunk(200)
	if err != nil {
		t.Fatalf("ReadRecordChunk() error = %v", err)
	}
	if chunk == nil || len(chunk.PCM) == 0 {
		t.Fatalf("expected PCM chunk, got %#v", chunk)
	}
	if err := dialog.StopRecording(); err != nil {
		t.Fatalf("StopRecording() error = %v", err)
	}

	input, err := dialog.PrepareTurnInput([]int16{1, 2})
	if err != nil {
		t.Fatalf("PrepareTurnInput() error = %v", err)
	}
	if input.Transcript != "streaming result" {
		t.Fatalf("Transcript = %q, want streaming result", input.Transcript)
	}
	if len(sttClient.inputs) != 0 {
		t.Fatalf("expected streaming transcript to skip TranscribeWAV, got %d calls", len(sttClient.inputs))
	}
	if sttClient.streamUploaderUsed != 1 {
		t.Fatalf("stream uploader begin count = %d, want 1", sttClient.streamUploaderUsed)
	}
	if sttClient.streamUploader == nil || len(sttClient.streamUploader.writes) != 1 {
		t.Fatalf("stream uploader writes = %#v, want one PCM write", sttClient.streamUploader)
	}
}

func TestAudioDialogStopRecordingFallsBackToOneShotWhenStreamingFinalizeTimesOut(t *testing.T) {
	oldTimeout := audioDialogStreamingSTTFinalizeTimeout
	audioDialogStreamingSTTFinalizeTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		audioDialogStreamingSTTFinalizeTimeout = oldTimeout
	})

	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "stop_recording":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "OK"}, nil
		}
	})

	uploader := newBlockingFinalizeUploader("")
	sttClient := &stubSTTClient{transcript: "one-shot result"}
	dialog := &AudioDialog{
		config: Config{
			InputMode: "stt",
			Audio: AudioConfig{
				Socket:     socketPath,
				SampleRate: 16000,
			},
		},
		audioClient:  NewAudioServiceClient(socketPath),
		sttClient:    sttClient,
		recordActive: true,
		sessionID:    42,
		recordSTT:    &streamingSTTSession{uploader: uploader},
	}

	if err := dialog.StopRecording(); err != nil {
		t.Fatalf("StopRecording() error = %v", err)
	}

	input, err := dialog.PrepareTurnInput([]int16{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("PrepareTurnInput() error = %v", err)
	}
	if input.Transcript != "one-shot result" {
		t.Fatalf("Transcript = %q, want one-shot result", input.Transcript)
	}
	if len(sttClient.inputs) != 1 {
		t.Fatalf("expected fallback one-shot STT call, got %d", len(sttClient.inputs))
	}
	select {
	case <-uploader.closed:
	case <-time.After(time.Second):
		t.Fatal("expected uploader to be closed after finalize timeout")
	}
}

func TestNewAudioDialogSTTWakeupCreatesSTTClient(t *testing.T) {
	dialog, err := NewAudioDialog(Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax-cn", APIKey: "test-key"},
		STT:         STTConfig{Provider: "openai-whisper"},
		Audio:       AudioConfig{Socket: "/tmp/audio.sock", SampleRate: 16000},
		InputMode:   "stt",
		TriggerMode: "wakeup",
	})
	if err != nil {
		t.Fatalf("NewAudioDialog() error = %v", err)
	}

	if dialog.sttClient == nil {
		t.Fatal("stt input mode should create an STT client")
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
		STT:       STTConfig{Provider: "openai-whisper"},
		Audio:     AudioConfig{Socket: "/tmp/audio.sock", SampleRate: 16000},
		InputMode: "stt",
	})
	if err != nil {
		t.Fatalf("NewAudioDialog() error = %v", err)
	}
	if dialog.ttsManager != nil {
		t.Fatalf("ttsManager = %#v, want nil after optional TTS init failure", dialog.ttsManager)
	}
}

func TestProcessUtteranceSpeaksTaggedTTSOutput(t *testing.T) {
	output := "Setup completed, current volume is 42.\n\n- Read volume\n- Confirm status\n\nThis detailed description is reserved for the screen.\n<tts>Setup completed, current volume is 42.</tts>"
	expectedSpeech := "Setup completed, current volume is 42."
	model := &scriptedModel{
		responses: roleDirectResponses(output),
	}
	store := NewChatHistoryStore(t.TempDir())
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	provider := &recordingTTSProvider{name: "dialog-provider"}
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "stt",
			VoiceStreamingTTSEnabled: boolPtr(false),
		},
		sttClient:    &stubSTTClient{transcript: "check volume"},
		audioClient:  NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
		ttsManager:   ttsmodule.NewProviderManager(provider, nil),
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	dialog.SetHistoryStore(store)

	if err := dialog.ProcessUtterance(context.Background(), []int16{100, -100, 200, -200}, runtime); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}

	texts := provider.texts()
	if len(texts) != 1 {
		t.Fatalf("unexpected TTS texts: %#v", texts)
	}
	if texts[0] != expectedSpeech {
		t.Fatalf("TTS should use result output directly, got %q want %q", texts[0], expectedSpeech)
	}

	messages, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	assistant, ok := firstMessageOfType(messages, "assistant")
	if !ok {
		t.Fatalf("assistant history missing: %#v", messages)
	}
	if !strings.Contains(assistant.Content, "This detailed description is reserved for the screen") {
		t.Fatalf("history should keep full output, got %q", assistant.Content)
	}
}

func TestAudioDialogSpeaksToolContentAsynchronously(t *testing.T) {
	toolSpeech := true
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, "Checking volume.\n<tts>Check volume.</tts>"),
			contentResponse("Current volume is 42.\n<tts>Current volume is 42.</tts>"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:               ModelConfig{Provider: "fake"},
			Instruction:         "Use tools when external state is requested.",
			VoiceToolCallSpeech: &toolSpeech,
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
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			InputMode:                "stt",
			VoiceStreamingTTSEnabled: boolPtr(false),
			VoiceToolCallSpeech:      &toolSpeech,
		},
		sttClient:    &stubSTTClient{transcript: "check volume"},
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
		t.Fatalf("expected tool content and final answer TTS, got %#v", texts)
	}
	if !containsString(texts, "Check volume.") || !containsString(texts, "Current volume is 42.") {
		t.Fatalf("unexpected TTS texts: %#v", texts)
	}
}

func TestAudioDialogDoesNotSpeakWaitForWakeupToolContent(t *testing.T) {
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
		Type:     runEventToolCall,
		ToolName: toolWaitForWakeup,
		Content:  "Preparing to return to waiting for wakeup state.",
	})

	assertNoProviderTextWithin(t, provider, 200*time.Millisecond)
}

func TestAudioDialogSpeaksToolCallContent(t *testing.T) {
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
		Type:     runEventToolCall,
		ToolName: "audio_volume",
		Content:  "Reading volume.\n<tts>Read current volume.</tts>",
	})

	waitForProviderTextCount(t, provider, 1)
	if got := provider.texts(); len(got) != 1 || got[0] != "Read current volume." {
		t.Fatalf("unexpected TTS texts: %#v", got)
	}
}

func TestAudioDialogDoesNotSpeakToolCallWithoutTTSTag(t *testing.T) {
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
		Type:     runEventToolCall,
		ToolName: "recall_memory",
		Content:  "I will check your preferences first.",
	})

	assertNoProviderTextWithin(t, provider, 200*time.Millisecond)
}

func TestAudioDialogDoesNotSpeakToolCallWithoutContent(t *testing.T) {
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
		Type:     runEventToolCall,
		ToolName: "audio_volume",
	})

	assertNoProviderTextWithin(t, provider, 200*time.Millisecond)
}

func TestAudioDialogProgressSpeechDisabledDoesNotSpeakTodoUpdate(t *testing.T) {
	disabled := false
	spoken := make(chan string, 1)
	dialog := &AudioDialog{
		config: Config{VoiceProgressSpeechEnabled: &disabled},
	}
	dialog.setProgressSpeaker(newProgressSpeaker(func(ctx context.Context, text string) error {
		spoken <- text
		return nil
	}, progressSpeakerConfig{Delay: time.Millisecond, MinInterval: -1}))

	todo := TodoState{
		Mode:      TodoModePlanned,
		Revision:  1,
		CurrentID: "todo-1",
		Items: []TodoItem{{
			ID:     "todo-1",
			Text:   "Inspect current screen",
			Status: TodoInProgress,
			Source: TodoSourceCommittedPlan,
		}},
	}
	dialog.HandleRunEvent(context.Background(), RunEvent{
		Type:           runEventTodoUpdate,
		Content:        "Inspect current screen",
		Todo:           &todo,
		SpeechEligible: true,
	})

	select {
	case text := <-spoken:
		t.Fatalf("progress speech was scheduled despite config=false: %q", text)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProgressSpeechTextForEventOnlySpeaksInProgressCurrentItem(t *testing.T) {
	todo := TodoState{
		Mode:      TodoModePlanned,
		Revision:  1,
		CurrentID: "step-1",
		Items: []TodoItem{{
			ID:     "step-1",
			Text:   "Inspect current screen",
			Status: TodoDone,
			Source: TodoSourceCommittedPlan,
		}},
	}
	if text, ok := progressSpeechTextForEvent(RunEvent{Type: runEventTodoUpdate, Content: "Inspect current screen", Todo: &todo, SpeechEligible: true}); ok {
		t.Fatalf("done todo should not be spoken, got %q", text)
	}
	todo.Items[0].Status = TodoInProgress
	if text, ok := progressSpeechTextForEvent(RunEvent{Type: runEventTodoUpdate, Content: "Inspect current screen", Todo: &todo}); ok {
		t.Fatalf("in-progress todo without speech eligibility should not be spoken, got %q", text)
	}
	text, ok := progressSpeechTextForEvent(RunEvent{Type: runEventTodoUpdate, Content: "Inspect current screen", Todo: &todo, SpeechEligible: true})
	if !ok || text != "Inspect current screen" {
		t.Fatalf("in-progress todo speech = %q ok=%v", text, ok)
	}
}

func TestProgressSpeakerLatestWins(t *testing.T) {
	spoken := make(chan string, 2)
	speaker := newProgressSpeaker(func(ctx context.Context, text string) error {
		spoken <- text
		return nil
	}, progressSpeakerConfig{Delay: 20 * time.Millisecond, MinInterval: -1})
	speaker.Schedule(context.Background(), "first")
	time.Sleep(5 * time.Millisecond)
	speaker.Schedule(context.Background(), "second")

	select {
	case text := <-spoken:
		if text != "second" {
			t.Fatalf("spoken text = %q, want latest value", text)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("progress speaker did not speak latest pending text")
	}
	select {
	case text := <-spoken:
		t.Fatalf("unexpected extra progress speech: %q", text)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProgressSpeakerCancelDropsPending(t *testing.T) {
	spoken := make(chan string, 1)
	speaker := newProgressSpeaker(func(ctx context.Context, text string) error {
		spoken <- text
		return nil
	}, progressSpeakerConfig{Delay: 30 * time.Millisecond, MinInterval: -1})
	speaker.Schedule(context.Background(), "pending")
	speaker.Cancel()

	select {
	case text := <-spoken:
		t.Fatalf("pending progress speech was not canceled: %q", text)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestAudioDialogStreamingSpeechErrorDoesNotHideWaitForWakeupRequest(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("wait_for_wakeup", `{"__arg1":"{\"reason\":\"user asked\"}"}`, "I will wait for the next wakeup."),
	}
	controller := NewWaitForWakeupController()
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"wait_for_wakeup": NewWaitForWakeupTool(controller),
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
	if !result.WaitForWakeupRequested {
		t.Fatal("WaitForWakeupRequested = false, want true even when streaming speech fails")
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
			InputMode:                "stt",
			VoiceStreamingTTSEnabled: boolPtr(false),
		},
		sttClient:    &stubSTTClient{transcript: "voice question"},
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
	userMessage, ok := firstAudioDialogTestMessageOfType(messages, "user")
	if !ok || userMessage.Source != "voice" || userMessage.Modality != TurnModalitySTT || userMessage.Content != "voice question" {
		t.Fatalf("user voice message = %#v in %#v", userMessage, messages)
	}
	if len(userMessage.Attachments) != 0 {
		t.Fatalf("user voice message should not include audio attachments: %#v", userMessage.Attachments)
	}
	roleOutput, ok := firstAudioDialogTestMessageOfType(messages, "role_output")
	if !ok || roleOutput.Source != "voice" || roleOutput.Content != "voice reply" {
		t.Fatalf("role_output message = %#v in %#v", roleOutput, messages)
	}
	assistantMessage, ok := firstAudioDialogTestMessageOfType(messages, "assistant")
	if !ok || assistantMessage.Source != "voice" || assistantMessage.Content != "voice reply" {
		t.Fatalf("assistant message = %#v in %#v", assistantMessage, messages)
	}
}

func TestAudioDialogRunAgentTurnAppendsRunEventsToVoiceHistory(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("echo", `{"__arg1":"{}"}`, "voice tool reply"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": &staticTool{name: "echo", output: `{"ok":true}`},
		}},
		NewSkillIndex(),
	)
	dialog := &AudioDialog{
		config: Config{
			Model: ModelConfig{Provider: "fake"},
		},
	}
	var messages []Message
	dialog.SetHistoryAppender(func(message Message) {
		messages = append(messages, message)
	})

	if _, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "use echo"}, runtime); err != nil {
		t.Fatalf("RunAgentTurn() error = %v", err)
	}

	for _, wantType := range []string{"role_output", runEventToolCall, "tool_result"} {
		message, ok := firstAudioDialogTestMessageOfType(messages, wantType)
		if !ok {
			t.Fatalf("missing voice history message type %q: %#v", wantType, messages)
		}
		if message.Source != "voice" {
			t.Fatalf("%s Source = %q, want voice", wantType, message.Source)
		}
	}
}

func TestAudioDialogRunAgentTurnConsumesQueuedSteer(t *testing.T) {
	firstCallStarted := make(chan struct{})
	releaseFirstCall := make(chan struct{})
	model := &blockingFirstCallModel{
		firstCallStarted: firstCallStarted,
		releaseFirstCall: releaseFirstCall,
		responses: []*llms.ContentResponse{
			contentResponse("first answer"),
			contentResponse("steered answer"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Answer directly.",
			ForceSimpleLoop: true,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	dialog := &AudioDialog{
		config: Config{
			Model: ModelConfig{Provider: "fake"},
		},
	}
	var messages []Message
	dialog.SetHistoryAppender(func(message Message) {
		messages = append(messages, message)
	})

	resultCh := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "original"}, runtime)
		resultCh <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-firstCallStarted:
	case <-time.After(time.Second):
		t.Fatal("first model call did not start")
	}
	if !dialog.QueueSteer(TurnInput{InputText: "change direction", Transcript: "change direction"}) {
		t.Fatal("QueueSteer returned false while voice run was active")
	}
	close(releaseFirstCall)

	var runResult struct {
		result RunResult
		err    error
	}
	select {
	case runResult = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("RunAgentTurn did not finish")
	}
	if runResult.err != nil {
		t.Fatalf("RunAgentTurn() error = %v", runResult.err)
	}
	if runResult.result.Output != "steered answer" {
		t.Fatalf("Output = %q, want steered answer", runResult.result.Output)
	}
	steerMessage, ok := firstAudioDialogTestMessageOfType(messages, "steer")
	if !ok {
		t.Fatalf("missing steer history message: %#v", messages)
	}
	if steerMessage.Content != "change direction" {
		t.Fatalf("steer content = %q, want change direction", steerMessage.Content)
	}
	if !strings.HasPrefix(steerMessage.RequestID, "voice-") {
		t.Fatalf("steer request_id = %q, want voice-*", steerMessage.RequestID)
	}
	if dialog.QueueSteer(TurnInput{InputText: "too late"}) {
		t.Fatal("QueueSteer returned true after voice run completed")
	}
}

func TestAudioDialogRejectsSteerAfterRuntimeFinishesBeforeVoiceHistoryPersist(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("final answer\n<tts>final answer</tts>")}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Answer directly.",
			ForceSimpleLoop: true,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	dialog := &AudioDialog{
		config: Config{
			Model: ModelConfig{Provider: "fake"},
		},
	}

	assistantAppendStarted := make(chan struct{})
	releaseAssistantAppend := make(chan struct{})
	dialog.SetHistoryAppender(func(message Message) {
		if message.Type != "assistant" {
			return
		}
		close(assistantAppendStarted)
		<-releaseAssistantAppend
	})

	resultCh := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := dialog.RunVoiceTurnWithContext(context.Background(), TurnInput{InputText: "original"}, nil, runtime, VoiceTurnContext{})
		resultCh <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-assistantAppendStarted:
	case <-time.After(time.Second):
		t.Fatal("assistant history append did not start")
	}
	if dialog.QueueSteer(TurnInput{InputText: "too late", Transcript: "too late"}) {
		t.Fatal("QueueSteer returned true after runtime finished consuming steer")
	}
	close(releaseAssistantAppend)

	select {
	case runResult := <-resultCh:
		if runResult.err != nil {
			t.Fatalf("RunVoiceTurnWithContext() error = %v", runResult.err)
		}
		if runResult.result.Output != "final answer\n<tts>final answer</tts>" {
			t.Fatalf("Output = %q, want final answer", runResult.result.Output)
		}
	case <-time.After(time.Second):
		t.Fatal("RunVoiceTurnWithContext did not finish")
	}
}

func TestAudioDialogBeginSteerInterruptWaitsForQueuedCorrection(t *testing.T) {
	firstCallStarted := make(chan struct{})
	releaseFirstCall := make(chan struct{})
	model := &blockingFirstCallModel{
		firstCallStarted: firstCallStarted,
		releaseFirstCall: releaseFirstCall,
		responses: []*llms.ContentResponse{
			contentResponse("stale answer"),
			contentResponse("corrected answer"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Answer directly.",
			ForceSimpleLoop: true,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	dialog := &AudioDialog{
		config: Config{
			Model: ModelConfig{Provider: "fake"},
		},
	}

	resultCh := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "original"}, runtime)
		resultCh <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-firstCallStarted:
	case <-time.After(time.Second):
		t.Fatal("first model call did not start")
	}
	if !dialog.BeginSteerInterrupt() {
		t.Fatal("BeginSteerInterrupt returned false while voice run was active")
	}
	if !dialog.QueueSteer(TurnInput{InputText: "change direction", Transcript: "change direction"}) {
		t.Fatal("QueueSteer returned false for interrupted voice run")
	}
	close(releaseFirstCall)

	var runResult struct {
		result RunResult
		err    error
	}
	select {
	case runResult = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("RunAgentTurn did not finish")
	}
	if runResult.err != nil {
		t.Fatalf("RunAgentTurn() error = %v", runResult.err)
	}
	if runResult.result.Output != "corrected answer" {
		t.Fatalf("Output = %q, want corrected answer", runResult.result.Output)
	}
	if dialog.ResumeSteerInterrupt() {
		t.Fatal("ResumeSteerInterrupt returned true after interrupted steer was consumed")
	}
}

func TestAudioDialogRunVoiceTurnDoesNotPersistUserWhenVoiceRunActive(t *testing.T) {
	dialog := &AudioDialog{
		config: Config{
			Model:        ModelConfig{Provider: "fake"},
			Audio:        AudioConfig{SampleRate: 16000},
			AudioArchive: AudioArchiveConfig{Enabled: false},
		},
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	var messages []Message
	dialog.SetHistoryAppender(func(message Message) {
		messages = append(messages, message)
	})

	if !dialog.beginVoiceRunControl("existing-request") {
		t.Fatal("beginVoiceRunControl returned false for setup")
	}
	defer dialog.endVoiceRunControl("existing-request")

	_, err := dialog.RunVoiceTurnWithContext(
		context.Background(),
		TurnInput{InputText: "late correction", Transcript: "late correction"},
		[]int16{100, -100},
		nil,
		VoiceTurnContext{RuntimeContext: "voice interruption"},
	)
	if err == nil || !strings.Contains(err.Error(), "voice run already active") {
		t.Fatalf("RunVoiceTurnWithContext() error = %v, want voice run already active", err)
	}
	if len(messages) != 0 {
		t.Fatalf("persisted messages = %#v, want none when voice run is already active", messages)
	}
}

func TestAudioDialogRunVoiceTurnPersistsUserBeforeRunEvents(t *testing.T) {
	configDir := t.TempDir()
	model := &scriptedModel{
		responses: roleToolResponses("echo", `{"__arg1":"{}"}`, "voice tool reply"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:       configDir,
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Use tools when requested.",
			ForceSimpleLoop: true,
		},
		&testModelResolver{model: model},
		NewMemoryManager(filepath.Join(configDir, "memory")),
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": &staticTool{name: "echo", output: `{"ok":true}`},
		}},
		NewSkillIndex(),
	)
	dialog := &AudioDialog{
		config: Config{
			Model:        ModelConfig{Provider: "fake"},
			Audio:        AudioConfig{SampleRate: 16000},
			AudioArchive: AudioArchiveConfig{Enabled: false},
		},
		audioArchive: NewAudioArchiveManager(AudioArchiveConfig{Enabled: false}),
	}
	var messages []Message
	dialog.SetHistoryAppender(func(message Message) {
		messages = append(messages, message)
	})

	_, err := dialog.RunVoiceTurn(
		context.Background(),
		TurnInput{InputText: "use echo", Transcript: "use echo"},
		[]int16{100, -100, 200, -200},
		runtime,
	)
	if err != nil {
		t.Fatalf("RunVoiceTurn() error = %v", err)
	}
	if len(messages) < 5 {
		t.Fatalf("expected user, run events, and assistant messages, got %#v", messages)
	}
	if messages[0].Type != "user" {
		t.Fatalf("first message type = %q, want user in %#v", messages[0].Type, messages)
	}
	if messages[len(messages)-1].Type != "assistant" {
		t.Fatalf("last message type = %q, want assistant in %#v", messages[len(messages)-1].Type, messages)
	}
	for _, wantType := range []string{runEventToolCall, "tool_result", "role_output"} {
		if _, ok := firstAudioDialogTestMessageOfType(messages, wantType); !ok {
			t.Fatalf("missing voice history message type %q: %#v", wantType, messages)
		}
	}
	episodeID := messages[0].EpisodeID
	if episodeID == "" {
		t.Fatalf("user message missing episode id: %#v", messages[0])
	}
	for _, message := range messages {
		if message.Source != "voice" {
			t.Fatalf("message source = %q, want voice: %#v", message.Source, message)
		}
		if message.EpisodeID != episodeID {
			t.Fatalf("message episode id = %q, want %q in %#v", message.EpisodeID, episodeID, messages)
		}
	}
}

func TestAudioDialogProcessUtteranceSavesAudioFile(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		ConfigDir: tmpDir,
		InputMode: "stt",
		STT:       STTConfig{Provider: "openai-whisper"},
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

type blockingFirstCallModel struct {
	firstCallStarted chan struct{}
	releaseFirstCall chan struct{}
	responses        []*llms.ContentResponse
	callCount        int
}

func (m *blockingFirstCallModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	call := m.callCount
	m.callCount++
	if call == 0 {
		close(m.firstCallStarted)
		select {
		case <-m.releaseFirstCall:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if call >= len(m.responses) {
		return contentResponse(""), nil
	}
	return m.responses[call], nil
}

func (m *blockingFirstCallModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	resp, err := m.GenerateContent(ctx, nil, options...)
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0] == nil {
		return "", nil
	}
	return resp.Choices[0].Content, nil
}

func TestAudioDialogRunScriptUsesConfiguredTTS(t *testing.T) {
	configDir := t.TempDir()
	scriptsDir := filepath.Join(configDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeRunScriptTestFile(t, scriptsDir, "demo.jsonl", `{"tts":"脚本语音"}`)

	model := &scriptedModel{
		responses: roleToolResponses("run_script", `{"file":"demo.jsonl"}`, "脚本完成。"),
	}
	cfg := Config{
		ConfigDir:                configDir,
		Model:                    ModelConfig{Provider: "fake"},
		Audio:                    AudioConfig{SampleRate: 16000},
		InputMode:                "stt",
		VoiceStreamingTTSEnabled: boolPtr(false),
		VoiceMaxResponseTokens:   64,
	}
	runtime := NewRuntimeWithDeps(
		cfg,
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSetFromConfig(cfg, ProxyConfig{}, nil),
		NewSkillIndex(),
	)
	provider := &recordingTTSProvider{name: "dialog-provider"}
	dialog := &AudioDialog{
		config:      cfg,
		audioClient: NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
	}

	result, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "run demo"}, runtime)
	if err != nil {
		t.Fatalf("RunAgentTurn() error = %v", err)
	}
	if result.Output != "脚本完成。" {
		t.Fatalf("output = %q, want 脚本完成。", result.Output)
	}
	waitForProviderTextCount(t, provider, 1)
	if got := provider.texts(); !containsString(got, "脚本语音") {
		t.Fatalf("provider texts = %#v, want script tts text", got)
	}
}

func TestAudioDialogConfigureRuntimeToolsDoesNotOverwriteSharedSpeaker(t *testing.T) {
	configDir := t.TempDir()
	scriptsDir := filepath.Join(configDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeRunScriptTestFile(t, scriptsDir, "demo.jsonl", `{"tts":"server voice"}`)

	tools := NewBuiltinToolSetFromConfig(Config{ConfigDir: configDir}, ProxyConfig{}, nil)
	spoken := make(chan string, 1)
	tools.SetRunScriptSpeaker(func(_ context.Context, text string) error {
		spoken <- text
		return nil
	})
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir, Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		tools,
		NewSkillIndex(),
	)

	dialog := &AudioDialog{}
	_ = dialog.ConfigureRuntimeTools(context.Background(), runtime)

	runScript, ok := tools.Get("run_script")
	if !ok {
		t.Fatal("run_script tool missing")
	}
	out, err := runScript.Call(context.Background(), `{"file":"demo.jsonl"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	select {
	case got := <-spoken:
		if got != "server voice" {
			t.Fatalf("spoken text = %q, want server voice", got)
		}
	case <-time.After(time.Second):
		t.Fatal("server-installed speaker was not called")
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
