package agent

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"

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
	model := &rawStreamingModel{content: "streamed answer", chunks: []string{"streamed ", "answer"}}
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
	if got := provider.texts(); len(got) != 1 || got[0] != "streamed answer" {
		t.Fatalf("provider texts = %#v, want streamed answer", got)
	}
}

func TestAudioDialogRunAgentTurnStreamsFinalAnswer(t *testing.T) {
	model := &rawStreamingModel{
		content: `{"final_answer":"已完成设置，当前音量是 42。\n\n完整回答保留给屏幕。"}`,
		chunks: []string{
			`{"final_answer":"已完成设置`,
			`，当前音量是 42。\n\n完整回答保留给屏幕。"}`,
		},
	}
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
		t.Fatal("SpeechStreamed = false, want true when speech streamed")
	}
	if result.Output != "已完成设置，当前音量是 42。\n\n完整回答保留给屏幕。" {
		t.Fatalf("Output = %q", result.Output)
	}
	if result.SpeechText != "" {
		t.Fatalf("SpeechText = %q", result.SpeechText)
	}
	if got := provider.texts(); len(got) != 1 || got[0] != "已完成设置，当前音量是 42。\n\n完整回答保留给屏幕。" {
		t.Fatalf("provider texts = %#v", got)
	}
}

func TestAudioDialogInterruptOutputStopsActiveStreamingTTS(t *testing.T) {
	model := newBlockingStreamingModel("old answer", "ignored stale suffix", "final answer")
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	streamingEnabled := true
	provider := newInterruptibleAudioTTSProvider("dialog-provider", 48000, false)
	audioOps := &recordedAudioOps{}
	dialog := &AudioDialog{
		config: Config{
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 48000},
			VoiceStreamingTTSEnabled: &streamingEnabled,
		},
		audioClient: NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, audioOps)),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
	}

	resultCh := make(chan audioDialogRunResult, 1)
	go func() {
		result, err := dialog.RunAgentTurn(context.Background(), TurnInput{InputText: "hello"}, runtime)
		resultCh <- audioDialogRunResult{result: result, err: err}
	}()

	waitForTestSignal(t, provider.firstWriteDone(), "streaming TTS playback to start")
	dialog.InterruptOutput()
	model.release()

	turnResult := waitForAudioDialogRunResult(t, resultCh)
	if turnResult.err != nil {
		t.Fatalf("RunAgentTurn() error = %v", turnResult.err)
	}
	if turnResult.result.SpeechStreamed {
		t.Fatal("SpeechStreamed = true, want false after streaming TTS interrupt")
	}
	if got := provider.texts(); len(got) != 1 || got[0] != "old answer" {
		t.Fatalf("provider texts = %#v, want only the pre-interrupt stream chunk", got)
	}
	if got := provider.closeCalls(); got != 0 {
		t.Fatalf("stream Close calls = %d, want 0 after interrupt", got)
	}
	if got := audioOps.countOp("stop_playback"); got != 1 {
		t.Fatalf("stop_playback count = %d, want 1", got)
	}
	if got := audioOps.finalChunkCountAfterFirstStop(); got != 0 {
		t.Fatalf("final write_play_chunk count after stop = %d, want 0 after interrupt", got)
	}
}

func TestAudioDialogProcessUtteranceSpeaksFinalAnswerWhenStreamingTTSInterrupted(t *testing.T) {
	model := newBlockingStreamingModel("old answer", "ignored stale suffix", "final answer")
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	streamingEnabled := true
	provider := newInterruptibleAudioTTSProvider("dialog-provider", 48000, false)
	dialog := &AudioDialog{
		config: Config{
			InputMode:                "audio",
			Model:                    ModelConfig{Provider: "fake"},
			Audio:                    AudioConfig{SampleRate: 48000},
			VoiceStreamingTTSEnabled: &streamingEnabled,
		},
		audioClient: NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, &recordedAudioOps{})),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- dialog.ProcessUtterance(context.Background(), []int16{1, 2}, runtime)
	}()

	waitForTestSignal(t, provider.firstWriteDone(), "streaming TTS playback to start")
	dialog.InterruptOutput()
	model.release()

	if err := waitForError(t, errCh); err != nil {
		t.Fatalf("ProcessUtterance() error = %v", err)
	}
	if got := provider.texts(); len(got) != 2 || got[0] != "old answer" || got[1] != "final answer" {
		t.Fatalf("provider texts = %#v, want interrupted stream then normal final Speak", got)
	}
}

func TestAudioDialogInterruptOutputStopsBackgroundToolSpeech(t *testing.T) {
	provider := newInterruptibleAudioTTSProvider("dialog-provider", 48000, true)
	audioOps := &recordedAudioOps{}
	dialog := &AudioDialog{
		config: Config{
			Model: ModelConfig{Provider: "fake"},
			Audio: AudioConfig{SampleRate: 48000},
		},
		audioClient: NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, audioOps)),
		ttsManager:  ttsmodule.NewProviderManager(provider, nil),
	}

	done := make(chan struct{})
	go func() {
		dialog.SpeakToolDescription("tool is running")
		close(done)
	}()

	waitForTestSignal(t, provider.firstWriteDone(), "tool TTS playback to start")
	dialog.InterruptOutput()
	waitForTestSignal(t, done, "tool TTS to stop")

	if got := audioOps.countOp("stop_playback"); got != 1 {
		t.Fatalf("stop_playback count = %d, want 1", got)
	}
	if got := audioOps.finalChunkCount(); got != 0 {
		t.Fatalf("final write_play_chunk count = %d, want 0 after tool speech interrupt", got)
	}
}

func TestRuntimeRunStreamsFinalAnswerToWriter(t *testing.T) {
	model := &rawStreamingModel{
		content: `{"final_answer":"完整回答保留给屏幕。"}`,
		chunks: []string{
			`{"final_answer":"完整`,
			`回答保留给屏幕。"}`,
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	var stream strings.Builder

	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "hello",
		StreamWriter:      NewJSONFieldOrPlainStreamWriter(&stream, "final_answer"),
		StreamFinalChunks: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stream.String() != "完整回答保留给屏幕。" {
		t.Fatalf("stream = %q", stream.String())
	}
	if result.Output != "完整回答保留给屏幕。" {
		t.Fatalf("Output = %q", result.Output)
	}
	if result.SpeechText != "" {
		t.Fatalf("SpeechText = %q", result.SpeechText)
	}
}

type rawStreamingModel struct {
	content string
	chunks  []string
}

func (m *rawStreamingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return m.content, nil
}

func (m *rawStreamingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	if callOptions.StreamingFunc != nil {
		for _, chunk := range m.chunks {
			if err := callOptions.StreamingFunc(ctx, []byte(chunk)); err != nil {
				return nil, err
			}
		}
	}
	return contentResponse(m.content), nil
}

type audioDialogRunResult struct {
	result RunResult
	err    error
}

type blockingStreamingModel struct {
	firstChunk  string
	secondChunk string
	content     string
	released    chan struct{}
	releaseOnce sync.Once
}

func newBlockingStreamingModel(firstChunk, secondChunk, content string) *blockingStreamingModel {
	return &blockingStreamingModel{
		firstChunk:  firstChunk,
		secondChunk: secondChunk,
		content:     content,
		released:    make(chan struct{}),
	}
}

func (m *blockingStreamingModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return m.content, nil
}

func (m *blockingStreamingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	if callOptions.StreamingFunc != nil {
		if err := callOptions.StreamingFunc(ctx, []byte(m.firstChunk)); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.released:
		}
		if m.secondChunk != "" {
			if err := callOptions.StreamingFunc(ctx, []byte(m.secondChunk)); err != nil {
				return nil, err
			}
		}
	}
	return contentResponse(m.content), nil
}

func (m *blockingStreamingModel) release() {
	m.releaseOnce.Do(func() {
		close(m.released)
	})
}

type interruptibleAudioTTSProvider struct {
	name        string
	pcmBytes    int
	blockWrites bool
	firstWrite  chan struct{}
	writeOnce   sync.Once
	closeCount  atomic.Int32
	mu          sync.Mutex
	seen        []string
}

func newInterruptibleAudioTTSProvider(name string, pcmBytes int, blockWrites bool) *interruptibleAudioTTSProvider {
	return &interruptibleAudioTTSProvider{
		name:        name,
		pcmBytes:    pcmBytes,
		blockWrites: blockWrites,
		firstWrite:  make(chan struct{}),
	}
}

func (p *interruptibleAudioTTSProvider) Name() string { return p.name }

func (p *interruptibleAudioTTSProvider) Capabilities() ttsmodule.Capabilities {
	return ttsmodule.Capabilities{}
}

func (p *interruptibleAudioTTSProvider) BeginStream(ctx context.Context, sink ttsmodule.AudioSink) (ttsmodule.StreamSession, error) {
	return &interruptibleAudioTTSSession{provider: p, ctx: ctx, sink: sink}, nil
}

func (p *interruptibleAudioTTSProvider) Close() error { return nil }

func (p *interruptibleAudioTTSProvider) firstWriteDone() <-chan struct{} {
	return p.firstWrite
}

func (p *interruptibleAudioTTSProvider) signalFirstWrite() {
	p.writeOnce.Do(func() {
		close(p.firstWrite)
	})
}

func (p *interruptibleAudioTTSProvider) texts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *interruptibleAudioTTSProvider) closeCalls() int {
	return int(p.closeCount.Load())
}

type interruptibleAudioTTSSession struct {
	provider *interruptibleAudioTTSProvider
	ctx      context.Context
	sink     ttsmodule.AudioSink
	err      error
}

func (s *interruptibleAudioTTSSession) WriteText(text string) error {
	s.provider.mu.Lock()
	s.provider.seen = append(s.provider.seen, text)
	s.provider.mu.Unlock()

	if s.provider.pcmBytes > 0 {
		if err := s.sink.WritePCM(make([]byte, s.provider.pcmBytes)); err != nil {
			s.err = err
			s.provider.signalFirstWrite()
			return err
		}
	}
	s.provider.signalFirstWrite()

	if s.provider.blockWrites {
		<-s.ctx.Done()
		s.err = s.ctx.Err()
		return s.err
	}
	return nil
}

func (s *interruptibleAudioTTSSession) Flush() error { return nil }

func (s *interruptibleAudioTTSSession) Close() error {
	s.provider.closeCount.Add(1)
	if err := s.sink.Drain(s.ctx); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *interruptibleAudioTTSSession) Err() error { return s.err }

type recordedAudioOps struct {
	mu  sync.Mutex
	ops []audioRequest
}

func startRecordedTTSPlaybackAudioSocket(t *testing.T, ops *recordedAudioOps) string {
	t.Helper()
	if ops == nil {
		ops = &recordedAudioOps{}
	}
	return startFakeAudioServiceSocket(t, func(req audioRequest) (audioResponse, []byte) {
		ops.append(req)
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

func (r *recordedAudioOps) append(req audioRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, req)
}

func (r *recordedAudioOps) countOp(op string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, req := range r.ops {
		if req.Op == op {
			count++
		}
	}
	return count
}

func (r *recordedAudioOps) finalChunkCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, req := range r.ops {
		if req.Op == "write_play_chunk" && req.IsFinal {
			count++
		}
	}
	return count
}

func (r *recordedAudioOps) finalChunkCountAfterFirstStop() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	seenStop := false
	for _, req := range r.ops {
		if req.Op == "stop_playback" {
			seenStop = true
			continue
		}
		if seenStop && req.Op == "write_play_chunk" && req.IsFinal {
			count++
		}
	}
	return count
}

func waitForTestSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForAudioDialogRunResult(t *testing.T, ch <-chan audioDialogRunResult) audioDialogRunResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RunAgentTurn")
		return audioDialogRunResult{}
	}
}

func waitForError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for operation")
		return nil
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
