package agent

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tmc/langchaingo/llms"

	ttsmodule "aiden-agent/internal/agent/tts"
)

func TestTTSPlaybackFormatUsesProviderOnlySampleRate(t *testing.T) {
	format := ttsPlaybackFormat(Config{Audio: AudioConfig{SampleRate: 16000}}, ttsmodule.Capabilities{
		SupportedSampleRates: []int{24000},
	})
	if format.SampleRate != 24000 {
		t.Fatalf("SampleRate = %d, want provider-only 24000", format.SampleRate)
	}
	if format.Channels != 1 || format.BitWidth != 16 {
		t.Fatalf("unexpected PCM format: %#v", format)
	}
}

func TestManagedTTSResamplesProviderOnly24kToConfiguredPlaybackRate(t *testing.T) {
	provider := &formatCheckingTTSProvider{
		name:       "provider-only-24k",
		caps:       ttsmodule.Capabilities{SupportedSampleRates: []int{24000}},
		wantFormat: ttsmodule.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16},
	}

	var startRate atomic.Uint32
	socketPath := startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_playback":
			startRate.Store(req.SampleRate)
			return audioResponse{Status: "OK", SessionID: stringUint64(7)}, nil
		case "write_play_chunk":
			return audioResponse{Status: "OK"}, nil
		case "health":
			return audioResponse{Status: "OK", PlaybackSessions: 0}, nil
		case "stop_playback":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})

	server := &Server{
		runtime: NewRuntimeWithDeps(
			Config{Model: ModelConfig{Provider: "fake"}, Audio: AudioConfig{SampleRate: 16000}},
			&testModelResolver{model: &scriptedModel{}},
			NewMemoryManager(""),
			NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
			NewSkillIndex(),
		),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
		audioClient: NewAudioServiceClient(socketPath),
	}

	if err := server.speakText(context.Background(), "hello", 0); err != nil {
		t.Fatalf("speakText() error = %v", err)
	}
	if got := startRate.Load(); got != 16000 {
		t.Fatalf("start_playback sample_rate = %d, want configured playback rate 16000", got)
	}
}

func TestStreamSessionWriterDoesNotReportSpokenWhenCloseFails(t *testing.T) {
	w := &streamSessionWriter{session: &failingStreamSession{closeErr: context.Canceled}}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.closeAndWait(); err == nil {
		t.Fatal("closeAndWait() error = nil, want failure")
	}
	if w.spoke {
		t.Fatal("spoke = true, want false when stream close failed")
	}
}

func TestHandleTTSSettingsPostInitializesManagerWhenAbsent(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	server := &Server{runtime: runtime}

	req := httptest.NewRequest(http.MethodPost, "/api/settings/tts", bytes.NewBufferString(`{"provider":"minimax-ws","api_key":"test-key"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handleTTSSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if server.ttsManager == nil || server.ttsManager.Current() != "minimax-ws" {
		t.Fatalf("manager = %#v, want initialized minimax-ws manager", server.ttsManager)
	}
}

func TestServerSpeakTextUsesProviderManager(t *testing.T) {
	provider := &recordingTTSProvider{name: "test-current-provider"}
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

	if err := server.speakText(context.Background(), "hello", 0); err != nil {
		t.Fatalf("speakText() error = %v", err)
	}
	if got := provider.texts(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("provider texts = %#v, want hello", got)
	}
}

func TestNewAudioDialogInitializesProviderManager(t *testing.T) {
	dialog, err := NewAudioDialog(Config{
		Model: ModelConfig{Provider: "fake"},
		TTS: TTSConfig{
			Provider: "alicloud",
			APIKey:   "test-key",
		},
		Audio: AudioConfig{Socket: "/tmp/audio.sock", SampleRate: 16000},
	})
	if err != nil {
		t.Fatalf("NewAudioDialog() error = %v", err)
	}
	if dialog.ttsManager == nil {
		t.Fatal("ttsManager = nil, want alicloud manager")
	}
	if dialog.ttsManager.Current() != "alicloud" {
		t.Fatalf("ttsManager current = %q, want alicloud", dialog.ttsManager.Current())
	}
}

func TestTTSProviderAliasesAreNotRegistered(t *testing.T) {
	for _, provider := range []string{"fish", "qwen", "qwen-tts"} {
		_, err := ttsmodule.New(ttsmodule.ProviderConfig{Provider: provider, APIKey: "test-key"})
		if err == nil {
			t.Fatalf("tts.New(%q) succeeded, want unsupported provider", provider)
		}
	}
}

func TestMinimaxHTTPProviderIsNotRegistered(t *testing.T) {
	_, err := ttsmodule.New(ttsmodule.ProviderConfig{Provider: "minimax", APIKey: "test-key"})
	if err == nil {
		t.Fatal("tts.New(minimax) succeeded, want unsupported provider")
	}
	if containsProviderName(ttsmodule.AvailableProviders(), "minimax") {
		t.Fatalf("AvailableProviders() includes minimax: %#v", ttsmodule.AvailableProviders())
	}
}

func TestMinimaxWebSocketTTSProviderIsRegistered(t *testing.T) {
	provider, err := ttsmodule.New(ttsmodule.ProviderConfig{Provider: "minimax-ws", APIKey: "test-key"})
	if err != nil {
		t.Fatalf("tts.New(minimax-ws) error = %v", err)
	}
	defer provider.Close()
	if provider.Name() != "minimax-ws" {
		t.Fatalf("provider.Name() = %q, want minimax-ws", provider.Name())
	}
	if !containsProviderName(ttsmodule.AvailableProviders(), "minimax-ws") {
		t.Fatalf("AvailableProviders() missing minimax-ws: %#v", ttsmodule.AvailableProviders())
	}
}

func TestVolcengineTTSProviderIsRegistered(t *testing.T) {
	provider, err := ttsmodule.New(ttsmodule.ProviderConfig{
		Provider: "volcengine",
		APIKey:   "test-key",
		Voice:    "test-speaker",
	})
	if err != nil {
		t.Fatalf("tts.New(volcengine) error = %v", err)
	}
	defer provider.Close()
	if provider.Name() != "volcengine" {
		t.Fatalf("provider.Name() = %q, want volcengine", provider.Name())
	}
	if !containsProviderName(ttsmodule.AvailableProviders(), "volcengine") {
		t.Fatalf("AvailableProviders() missing volcengine: %#v", ttsmodule.AvailableProviders())
	}
}

func containsProviderName(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestAudioDialogRunAgentTurnStreamsThroughProviderManager(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: "streamed answer"}}}}}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	streamingEnabled := true
	provider := &recordingTTSProvider{name: "dialog-provider"}
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 16000},
			VoiceStreamingTTSEnabled: &streamingEnabled,
		},
		audioClient: NewAudioServiceClient(startTTSPlaybackAudioSocket(t)),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
	}

	result, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "hello"}, runtime)
	if err != nil {
		t.Fatalf("RunAgentTurn() error = %v", err)
	}
	if !result.SpeechStreamed {
		t.Fatal("SpeechStreamed = false, want true when provider manager streamed audio")
	}
	if got := provider.texts(); len(got) != 1 || got[0] != "chunk:streamed answer" {
		t.Fatalf("provider texts = %#v, want streamed chunk", got)
	}
}

type failingStreamSession struct {
	closeErr error
}

func (s *failingStreamSession) WriteText(string) error { return nil }
func (s *failingStreamSession) Flush() error           { return nil }
func (s *failingStreamSession) Close() error           { return s.closeErr }
func (s *failingStreamSession) Err() error             { return s.closeErr }

type formatCheckingTTSProvider struct {
	name       string
	caps       ttsmodule.Capabilities
	wantFormat ttsmodule.AudioFormat
}

func (p *formatCheckingTTSProvider) Name() string { return p.name }

func (p *formatCheckingTTSProvider) Capabilities() ttsmodule.Capabilities { return p.caps }

func (p *formatCheckingTTSProvider) BeginStream(ctx context.Context, sink ttsmodule.AudioSink) (ttsmodule.StreamSession, error) {
	format := sink.Format()
	if format != p.wantFormat {
		return nil, fmt.Errorf("sink format = %#v, want %#v", format, p.wantFormat)
	}
	return &formatCheckingTTSSession{sink: sink}, nil
}

func (p *formatCheckingTTSProvider) Close() error { return nil }

type formatCheckingTTSSession struct {
	sink ttsmodule.AudioSink
	err  error
}

func (s *formatCheckingTTSSession) WriteText(string) error { return nil }

func (s *formatCheckingTTSSession) Flush() error { return nil }

func (s *formatCheckingTTSSession) Close() error {
	if err := s.sink.WritePCM([]byte{0, 0, 1, 0, 2, 0}); err != nil {
		s.err = err
		return err
	}
	if err := s.sink.Drain(context.Background()); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *formatCheckingTTSSession) Err() error { return s.err }

type recordingTTSProvider struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (p *recordingTTSProvider) Name() string { return p.name }

func (p *recordingTTSProvider) Capabilities() ttsmodule.Capabilities {
	return ttsmodule.Capabilities{SupportedSampleRates: []int{16000}}
}

func (p *recordingTTSProvider) BeginStream(ctx context.Context, sink ttsmodule.AudioSink) (ttsmodule.StreamSession, error) {
	return &recordingTTSSession{provider: p, sink: sink}, nil
}

func (p *recordingTTSProvider) Close() error { return nil }

func (p *recordingTTSProvider) texts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

type recordingTTSSession struct {
	provider *recordingTTSProvider
	sink     ttsmodule.AudioSink
	buf      bytes.Buffer
	err      error
}

func (s *recordingTTSSession) WriteText(text string) error {
	_, _ = s.buf.WriteString(text)
	return nil
}

func (s *recordingTTSSession) Flush() error { return nil }

func (s *recordingTTSSession) Close() error {
	text := s.buf.String()
	if text != "" {
		s.provider.mu.Lock()
		s.provider.seen = append(s.provider.seen, text)
		s.provider.mu.Unlock()
		if err := s.sink.WritePCM([]byte{0, 0}); err != nil {
			s.err = err
			return err
		}
	}
	if err := s.sink.Drain(context.Background()); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *recordingTTSSession) Err() error { return s.err }

func startTTSPlaybackAudioSocket(t *testing.T) string {
	t.Helper()
	return startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		switch req.Op {
		case "start_playback":
			return audioResponse{Status: "OK", SessionID: stringUint64(7)}, nil
		case "write_play_chunk":
			return audioResponse{Status: "OK"}, nil
		case "health":
			return audioResponse{Status: "OK", PlaybackSessions: 0}, nil
		case "stop_playback":
			return audioResponse{Status: "OK"}, nil
		default:
			return audioResponse{Status: "INTERNAL_ERROR"}, nil
		}
	})
}
