package realtimevoice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"aiden-agent/internal/agent/tts"
)

// ErrUnsupportedCapability is returned when a caller invokes an operation
// that the selected provider does not implement. Callers should normally use
// SessionInfo.Capabilities to avoid unsupported operations.
var ErrUnsupportedCapability = errors.New("realtime voice capability is unsupported")

// DeviceMediaConfig is the PCM contract between Aiden's audio device and the
// managed realtime session. Provider-native formats stay behind this seam.
type DeviceMediaConfig struct {
	Input  AudioFormat
	Output AudioFormat
}

// Conversation is Aiden's provider-neutral realtime interface. Optional wire
// operations are represented by Capabilities and return
// ErrUnsupportedCapability when unavailable.
type Conversation interface {
	Session
	TurnCommitter
	ResponseInterrupter
	ToolResultSender
	TextSession
	ContextReplayer
}

// Open constructs the selected provider adapter and wraps it with Aiden's
// media and capability normalization.
func (r *ProviderRegistry) Open(
	ctx context.Context,
	name string,
	providerConfig ProviderConfig,
	sessionConfig SessionConfig,
	media DeviceMediaConfig,
) (Conversation, error) {
	provider, err := r.New(name, providerConfig)
	if err != nil {
		return nil, err
	}
	raw, err := provider.Open(ctx, sessionConfig)
	if err != nil {
		return nil, err
	}
	managed, err := newManagedSession(raw, media)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	capabilities := managed.Info().Capabilities
	if capabilities.ClientSideTurnDetection && !capabilities.CanCommitInputTurn {
		_ = managed.Close()
		return nil, fmt.Errorf("realtime provider %s requires client-side turn detection without input commit support", name)
	}
	if len(sessionConfig.Tools) > 0 && !capabilities.CanSendToolResult {
		_ = managed.Close()
		return nil, fmt.Errorf("realtime provider %s cannot send configured tool results", name)
	}
	if capabilities.ExplicitToolContinuation && !capabilities.CanSendText {
		_ = managed.Close()
		return nil, fmt.Errorf("realtime provider %s requires explicit response continuation without text response support", name)
	}
	return managed, nil
}

type managedSession struct {
	raw             Session
	info            SessionInfo
	events          chan Event
	stop            chan struct{}
	closeOnce       sync.Once
	closeErr        error
	inputMu         sync.Mutex
	inputResampler  *tts.PCM16MonoResampler
	outputResampler *tts.PCM16MonoResampler
	outputSpec      *pcmResamplerSpec
}

type pcmResamplerSpec struct {
	source AudioFormat
	target AudioFormat
}

func newManagedSession(raw Session, media DeviceMediaConfig) (*managedSession, error) {
	if raw == nil {
		return nil, errors.New("realtime voice provider returned a nil session")
	}
	rawInfo := raw.Info()
	providerInput := normalizedPCMFormat(rawInfo.InputFormatOrDefault(16000))
	providerOutput := normalizedPCMFormat(rawInfo.OutputFormatOrDefault(24000))
	deviceInput := requestedDeviceFormat(media.Input, providerInput)
	deviceOutput := requestedDeviceFormat(media.Output, providerOutput)
	inputSpec, err := mediaResamplerSpec("input", deviceInput, providerInput)
	if err != nil {
		return nil, err
	}
	outputSpec, err := mediaResamplerSpec("output", providerOutput, deviceOutput)
	if err != nil {
		return nil, err
	}

	capabilities := rawInfo.Capabilities
	_, capabilities.CanCommitInputTurn = raw.(TurnCommitter)
	_, capabilities.CanInterruptResponse = raw.(ResponseInterrupter)
	_, capabilities.CanSendToolResult = raw.(ToolResultSender)
	_, capabilities.CanSendText = raw.(TextSession)
	_, capabilities.CanReplayContext = raw.(ContextReplayer)
	info := rawInfo
	info.InputAudioFormat = deviceInput
	info.OutputAudioFormat = deviceOutput
	info.InputSampleRate = deviceInput.SampleRate
	info.OutputSampleRate = deviceOutput.SampleRate
	info.ProviderInputAudioFormat = providerInput
	info.ProviderOutputAudioFormat = providerOutput
	info.Capabilities = capabilities

	sourceEvents := raw.Events()
	eventBuffer := cap(sourceEvents)
	if eventBuffer < 1 {
		eventBuffer = 64
	}
	s := &managedSession{
		raw:        raw,
		info:       info,
		events:     make(chan Event, eventBuffer),
		stop:       make(chan struct{}),
		outputSpec: outputSpec,
	}
	if inputSpec != nil {
		s.inputResampler = inputSpec.newResampler()
	}
	if outputSpec != nil {
		s.outputResampler = outputSpec.newResampler()
	}
	go s.forwardEvents(sourceEvents)
	return s, nil
}

func normalizedPCMFormat(format AudioFormat) AudioFormat {
	if strings.TrimSpace(format.Encoding) == "" {
		format.Encoding = "pcm_s16le"
	}
	return format
}

func requestedDeviceFormat(requested, provider AudioFormat) AudioFormat {
	if strings.TrimSpace(requested.Encoding) == "" {
		requested.Encoding = provider.Encoding
	}
	if requested.SampleRate <= 0 {
		requested.SampleRate = provider.SampleRate
	}
	if requested.Channels <= 0 {
		requested.Channels = provider.Channels
	}
	if requested.BitDepth <= 0 {
		requested.BitDepth = provider.BitDepth
	}
	return normalizedPCMFormat(requested)
}

func mediaResamplerSpec(direction string, source, target AudioFormat) (*pcmResamplerSpec, error) {
	if !pcm16Mono(source) || !pcm16Mono(target) {
		return nil, fmt.Errorf("realtime %s media conversion only supports mono PCM16: source=%s target=%s", direction, formatLabel(source), formatLabel(target))
	}
	if source.SampleRate == target.SampleRate {
		return nil, nil
	}
	return &pcmResamplerSpec{source: source, target: target}, nil
}

func pcm16Mono(format AudioFormat) bool {
	if format.SampleRate <= 0 || format.Channels != 1 || format.BitDepth != 16 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(format.Encoding)) {
	case "", "pcm", "pcm16", "pcm_s16le", "audio/pcm":
		return true
	default:
		return false
	}
}

func formatLabel(format AudioFormat) string {
	return fmt.Sprintf("%s/%d/%d/%d", format.Encoding, format.SampleRate, format.Channels, format.BitDepth)
}

func (s *pcmResamplerSpec) newResampler() *tts.PCM16MonoResampler {
	return tts.NewPCM16MonoResampler(s.source.SampleRate, s.target.SampleRate)
}

func (s *managedSession) Info() SessionInfo     { return s.info }
func (s *managedSession) Events() <-chan Event  { return s.events }
func (s *managedSession) Errors() <-chan error  { return s.raw.Errors() }
func (s *managedSession) Done() <-chan struct{} { return s.raw.Done() }
func (s *managedSession) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	s.inputMu.Lock()
	if s.inputResampler != nil {
		pcm = s.inputResampler.Write(pcm)
	}
	s.inputMu.Unlock()
	if len(pcm) == 0 {
		return nil
	}
	return s.raw.SendAudio(ctx, pcm)
}

func (s *managedSession) Commit(ctx context.Context) error {
	capability, ok := s.raw.(TurnCommitter)
	if !ok {
		return unsupportedCapability("commit input turn")
	}
	return capability.Commit(ctx)
}

func (s *managedSession) Interrupt(ctx context.Context) error {
	capability, ok := s.raw.(ResponseInterrupter)
	if !ok {
		return unsupportedCapability("interrupt response")
	}
	return capability.Interrupt(ctx)
}

func (s *managedSession) SendToolResult(ctx context.Context, id, output string) error {
	capability, ok := s.raw.(ToolResultSender)
	if !ok {
		return unsupportedCapability("send tool result")
	}
	return capability.SendToolResult(ctx, id, output)
}

func (s *managedSession) SendText(ctx context.Context, text string) error {
	capability, ok := s.raw.(TextSession)
	if !ok {
		return unsupportedCapability("send text")
	}
	return capability.SendText(ctx, text)
}

func (s *managedSession) CreateResponse(ctx context.Context) error {
	capability, ok := s.raw.(TextSession)
	if !ok {
		return unsupportedCapability("create response")
	}
	return capability.CreateResponse(ctx)
}

func (s *managedSession) ReplayContext(ctx context.Context, items []ContextItem) error {
	capability, ok := s.raw.(ContextReplayer)
	if !ok {
		return unsupportedCapability("replay context")
	}
	return capability.ReplayContext(ctx, items)
}

func unsupportedCapability(name string) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedCapability, name)
}

func (s *managedSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.closeErr = s.raw.Close()
	})
	return s.closeErr
}

func (s *managedSession) forwardEvents(source <-chan Event) {
	defer close(s.events)
	var pendingUserTranscript *Event
	flushUserTranscript := func() bool {
		if pendingUserTranscript == nil {
			return true
		}
		event := *pendingUserTranscript
		pendingUserTranscript = nil
		return s.forwardEvent(event)
	}

	for event := range source {
		if event.Kind == EventTranscriptFinal && event.Role == "user" {
			pendingUserTranscript = coalesceUserTranscript(pendingUserTranscript, event)
			continue
		}
		if event.Kind == EventResponseStarted || event.Kind == EventSpeechStarted || event.Kind == EventClosed {
			if !flushUserTranscript() {
				return
			}
		}
		if event.Kind == EventResponseStarted && s.outputSpec != nil {
			s.outputResampler = s.outputSpec.newResampler()
		}
		if event.Kind == EventAudio && s.outputResampler != nil {
			event.PCM = s.outputResampler.Write(event.PCM)
			if len(event.PCM) == 0 {
				continue
			}
		}
		if !s.forwardEvent(event) {
			return
		}
	}
	flushUserTranscript()
}

func (s *managedSession) forwardEvent(event Event) bool {
	select {
	case s.events <- event:
		return true
	case <-s.stop:
		return false
	}
}

func coalesceUserTranscript(pending *Event, next Event) *Event {
	next.Text = strings.TrimSpace(next.Text)
	if next.Text == "" {
		return pending
	}
	if pending == nil {
		copy := next
		return &copy
	}
	current := strings.TrimSpace(pending.Text)
	switch {
	case next.Text == current, strings.HasPrefix(current, next.Text):
		return pending
	case strings.HasPrefix(next.Text, current):
		copy := next
		return &copy
	default:
		copy := next
		copy.Text = strings.TrimSpace(current + " " + next.Text)
		return &copy
	}
}

var _ Conversation = (*managedSession)(nil)
