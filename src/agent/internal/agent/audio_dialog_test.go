package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
			Model:     ModelConfig{Provider: "fake"},
			Audio:     AudioConfig{SampleRate: 16000},
			InputMode: "audio",
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

type fakeTTSClient struct {
	texts []string
	audio *AudioServiceClient
}

func (c *fakeTTSClient) TextToSpeechStream(ctx context.Context, text string, audio *AudioServiceClient) error {
	c.texts = append(c.texts, text)
	c.audio = audio
	return nil
}
