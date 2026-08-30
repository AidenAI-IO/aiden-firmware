package realtimevoice

import (
	"context"
	"errors"
	"testing"
)

type managedTestProvider struct {
	session Session
}

func (p managedTestProvider) Open(context.Context, SessionConfig) (Session, error) {
	return p.session, nil
}

type managedTestSession struct {
	info       SessionInfo
	events     chan Event
	errs       chan error
	done       chan struct{}
	sentAudio  []byte
	commits    int
	interrupts int
	toolCalls  int
	texts      int
	responses  int
	replays    int
}

func newManagedTestSession(rate int) *managedTestSession {
	return &managedTestSession{
		info:   newPCM16SessionInfo("native-1", rate, rate, Capabilities{}),
		events: make(chan Event, 8),
		errs:   make(chan error),
		done:   make(chan struct{}),
	}
}

func (s *managedTestSession) Info() SessionInfo     { return s.info }
func (s *managedTestSession) Events() <-chan Event  { return s.events }
func (s *managedTestSession) Errors() <-chan error  { return s.errs }
func (s *managedTestSession) Done() <-chan struct{} { return s.done }
func (s *managedTestSession) Close() error          { return nil }
func (s *managedTestSession) SendAudio(_ context.Context, pcm []byte) error {
	s.sentAudio = append(s.sentAudio, pcm...)
	return nil
}
func (s *managedTestSession) Commit(context.Context) error {
	s.commits++
	return nil
}
func (s *managedTestSession) Interrupt(context.Context) error {
	s.interrupts++
	return nil
}
func (s *managedTestSession) SendToolResult(context.Context, string, string) error {
	s.toolCalls++
	return nil
}
func (s *managedTestSession) SendText(context.Context, string) error {
	s.texts++
	return nil
}
func (s *managedTestSession) CreateResponse(context.Context) error {
	s.responses++
	return nil
}
func (s *managedTestSession) ReplayContext(context.Context, []ContextItem) error {
	s.replays++
	return nil
}

type managedBaseSession struct {
	info   SessionInfo
	events chan Event
	errs   chan error
	done   chan struct{}
}

func newManagedBaseSession() *managedBaseSession {
	return &managedBaseSession{
		info:   newPCM16SessionInfo("base-1", 16000, 16000, Capabilities{}),
		events: make(chan Event),
		errs:   make(chan error),
		done:   make(chan struct{}),
	}
}

func (s *managedBaseSession) Info() SessionInfo                       { return s.info }
func (s *managedBaseSession) Events() <-chan Event                    { return s.events }
func (s *managedBaseSession) Errors() <-chan error                    { return s.errs }
func (s *managedBaseSession) Done() <-chan struct{}                   { return s.done }
func (s *managedBaseSession) SendAudio(context.Context, []byte) error { return nil }
func (s *managedBaseSession) Close() error                            { return nil }

func TestManagedSessionHidesProviderMediaFormat(t *testing.T) {
	raw := newManagedTestSession(24000)
	registry := NewProviderRegistry()
	registry.Register("test", func(ProviderConfig) Provider { return managedTestProvider{session: raw} })
	device := AudioFormat{Encoding: "pcm_s16le", SampleRate: 16000, Channels: 1, BitDepth: 16}
	session, err := registry.Open(context.Background(), "test", ProviderConfig{}, SessionConfig{}, DeviceMediaConfig{Input: device, Output: device})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	info := session.Info()
	if info.InputSampleRate != 16000 || info.OutputSampleRate != 16000 {
		t.Fatalf("public rates = %d/%d, want device 16000/16000", info.InputSampleRate, info.OutputSampleRate)
	}
	if info.ProviderInputAudioFormat.SampleRate != 24000 || info.ProviderOutputAudioFormat.SampleRate != 24000 {
		t.Fatalf("provider formats = %+v/%+v, want native 24000", info.ProviderInputAudioFormat, info.ProviderOutputAudioFormat)
	}
	if !info.Capabilities.CanCommitInputTurn || !info.Capabilities.CanInterruptResponse ||
		!info.Capabilities.CanSendToolResult || !info.Capabilities.CanSendText || !info.Capabilities.CanReplayContext {
		t.Fatalf("managed capabilities = %+v, want all test capabilities", info.Capabilities)
	}

	input := make([]byte, 320)
	if err := session.SendAudio(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(raw.sentAudio) <= len(input) {
		t.Fatalf("provider input bytes = %d, want 16 to 24 kHz expansion from %d", len(raw.sentAudio), len(input))
	}

	raw.events <- Event{Kind: EventResponseStarted}
	raw.events <- Event{Kind: EventAudio, PCM: make([]byte, 480)}
	close(raw.events)
	if event := <-session.Events(); event.Kind != EventResponseStarted {
		t.Fatalf("first event = %s, want response_started", event.Kind)
	}
	event := <-session.Events()
	if event.Kind != EventAudio || len(event.PCM) >= 480 || len(event.PCM) == 0 {
		t.Fatalf("output event = kind:%s bytes:%d, want resampled audio below 480 bytes", event.Kind, len(event.PCM))
	}
}

func TestManagedSessionNormalizesOperations(t *testing.T) {
	raw := newManagedTestSession(16000)
	session, err := newManagedSession(raw, DeviceMediaConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx := context.Background()
	if err := session.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.SendToolResult(ctx, "call-1", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := session.SendText(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := session.CreateResponse(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.ReplayContext(ctx, []ContextItem{{Role: "user", Content: "prior"}}); err != nil {
		t.Fatal(err)
	}
	if raw.commits != 1 || raw.interrupts != 1 || raw.toolCalls != 1 || raw.texts != 1 || raw.responses != 1 || raw.replays != 1 {
		t.Fatalf("forwarded calls = %+v", raw)
	}
}

func TestManagedSessionReportsUnsupportedCapability(t *testing.T) {
	raw := newManagedBaseSession()
	session, err := newManagedSession(raw, DeviceMediaConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Interrupt(context.Background()); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("interrupt error = %v, want ErrUnsupportedCapability", err)
	}
	if session.Info().Capabilities.CanInterruptResponse {
		t.Fatal("base session unexpectedly reports interruption support")
	}
}

func TestRegistryOpenRejectsMissingConfiguredToolCapability(t *testing.T) {
	registry := NewProviderRegistry()
	registry.Register("base", func(ProviderConfig) Provider { return managedTestProvider{session: newManagedBaseSession()} })
	_, err := registry.Open(context.Background(), "base", ProviderConfig{}, SessionConfig{Tools: []Tool{{Name: "clock"}}}, DeviceMediaConfig{})
	if err == nil {
		t.Fatal("registry Open accepted configured tools without tool-result support")
	}
}

func TestManagedSessionCoalescesProgressiveUserTranscriptsPerTurn(t *testing.T) {
	raw := newManagedTestSession(16000)
	session, err := newManagedSession(raw, DeviceMediaConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	raw.events <- Event{Kind: EventSpeechStarted}
	raw.events <- Event{Kind: EventTranscriptFinal, Role: "user", ItemID: "item-1", Text: "今天是几"}
	raw.events <- Event{Kind: EventTranscriptFinal, Role: "user", ItemID: "item-1", Text: "今天是几号？"}
	raw.events <- Event{Kind: EventTranscriptFinal, Role: "user", ItemID: "item-1", Text: "今天是几号？"}
	raw.events <- Event{Kind: EventResponseStarted, ResponseID: "response-1"}
	close(raw.events)

	var events []Event
	for event := range session.Events() {
		events = append(events, event)
	}
	if len(events) != 3 {
		t.Fatalf("managed events = %+v, want speech, one transcript, response", events)
	}
	if events[0].Kind != EventSpeechStarted || events[1].Kind != EventTranscriptFinal || events[2].Kind != EventResponseStarted {
		t.Fatalf("managed event order = %+v", events)
	}
	if events[1].Text != "今天是几号？" {
		t.Fatalf("coalesced transcript = %q, want final progressive text", events[1].Text)
	}
}
