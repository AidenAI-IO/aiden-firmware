package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"aiden-agent/internal/agent/tts"
)

func newTTSProviderManagerFromConfig(cfg Config, logger *Logger) (*tts.ProviderManager, error) {
	if strings.TrimSpace(cfg.TTS.Provider) == "" {
		return nil, nil
	}
	provider, err := tts.New(buildTTSProviderConfig(cfg))
	if err != nil {
		return nil, err
	}
	return tts.NewProviderManager(provider, &ttsLoggerAdapter{logger: logger}), nil
}

// buildTTSProviderConfig converts the agent-level Config into a tts.ProviderConfig.
// If cfg.TTS.Credentials contains an entry for the resolved provider, those
// per-provider values override the top-level [tts] fallbacks.
func buildTTSProviderConfig(cfg Config) tts.ProviderConfig {
	provider := cfg.TTS.Provider
	return buildTTSProviderConfigFor(cfg, provider)
}

// buildTTSProviderConfigFor resolves the config for a specific provider name,
// merging per-provider credentials over the top-level [tts] fallbacks.
func buildTTSProviderConfigFor(cfg Config, provider string) tts.ProviderConfig {
	provider = normalizeTTSProvider(provider)

	// Defaults from the top-level [tts] section.
	apiKey := cfg.TTS.APIKey
	voice := cfg.TTS.VoiceID
	emotion := cfg.TTS.Emotion
	model := cfg.TTS.Model
	speed := cfg.TTS.Speed
	referenceID := cfg.TTS.ReferenceID

	// Per-provider override. Lookup is case-insensitive.
	if creds, ok := lookupCredentials(cfg.TTS.Credentials, provider); ok {
		if creds.APIKey != "" {
			apiKey = creds.APIKey
		}
		if creds.VoiceID != "" {
			voice = creds.VoiceID
		}
		if creds.Emotion != "" {
			emotion = creds.Emotion
		}
		if creds.Model != "" {
			model = creds.Model
		}
		if creds.Speed != 0 {
			speed = creds.Speed
		}
		if creds.ReferenceID != "" {
			referenceID = creds.ReferenceID
		}
	}

	extra := map[string]any{}
	if emotion != "" {
		extra["emotion"] = emotion
	}
	if model != "" {
		extra["model"] = model
	}
	if referenceID != "" {
		extra["reference_id"] = referenceID
	}

	proxy := ProxyConfigFromEnvironment()
	return tts.ProviderConfig{
		Provider:   provider,
		APIKey:     apiKey,
		Voice:      voice,
		SampleRate: cfg.Audio.SampleRateOrDefault(),
		SpeedRatio: speed,
		Proxy: tts.ProxyConfig{
			HTTPProxy:  proxy.HTTPProxy,
			HTTPSProxy: proxy.HTTPSProxy,
			AllProxy:   proxy.AllProxy,
			NoProxy:    proxy.NoProxy,
		},
		Extra: extra,
	}
}

func ttsPlaybackFormat(cfg Config, caps tts.Capabilities) tts.AudioFormat {
	sampleRate := cfg.Audio.SampleRateOrDefault()
	if len(caps.SupportedSampleRates) == 1 {
		sampleRate = caps.SupportedSampleRates[0]
	} else if len(caps.SupportedSampleRates) > 1 && !containsInt(caps.SupportedSampleRates, sampleRate) {
		sampleRate = caps.SupportedSampleRates[0]
	}
	return tts.AudioFormat{
		SampleRate: sampleRate,
		Channels:   cfg.Audio.ChannelsOrDefault(),
		BitWidth:   cfg.Audio.BitWidthOrDefault(),
	}
}

func ttsPlaybackTargetFormat(cfg Config, source tts.AudioFormat) tts.AudioFormat {
	target := source
	if configuredRate := cfg.Audio.SampleRateOrDefault(); configuredRate > 0 {
		target.SampleRate = configuredRate
	}
	return target
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func beginManagedTTSStream(ctx context.Context, manager *tts.ProviderManager, audio *AudioServiceClient, cfg Config) (*streamSessionWriter, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	holder := manager.Holder()
	sourceFormat := ttsPlaybackFormat(cfg, holder.Capabilities())
	targetSink := tts.NewAudioServiceSink(newAudioBackend(audio), ttsPlaybackTargetFormat(cfg, sourceFormat))
	providerSink := tts.NewResamplingSink(sourceFormat, targetSink)
	session, err := holder.BeginStream(ctx, providerSink)
	if err != nil {
		return nil, err
	}
	return &streamSessionWriter{session: session, sink: targetSink}, nil
}

type ttsStreamObserver func(*streamSessionWriter) func()

func speakWithTTSManager(ctx context.Context, manager *tts.ProviderManager, audio *AudioServiceClient, cfg Config, text string) (bool, error) {
	return speakWithTTSManagerObserved(ctx, manager, audio, cfg, text, nil)
}

func speakWithTTSManagerObserved(ctx context.Context, manager *tts.ProviderManager, audio *AudioServiceClient, cfg Config, text string, observe ttsStreamObserver) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" || manager == nil || audio == nil {
		return false, nil
	}

	// Retry transient network errors
	maxRetries := 2
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Short backoff between retries
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}

		stream, err := beginManagedTTSStream(ctx, manager, audio, cfg)
		if err != nil {
			lastErr = err
			if isTransientTTSError(err) && attempt < maxRetries {
				continue
			}
			return false, err
		}
		unobserve := func() {}
		if observe != nil {
			if cleanup := observe(stream); cleanup != nil {
				unobserve = cleanup
			}
		}

		if _, err := stream.Write([]byte(text)); err != nil {
			speechStarted := stream.startedPlayback()
			var stopErr error
			if ctx.Err() != nil {
				stream.interrupt()
			}
			_ = stream.closeAndWait()
			speechStarted = speechStarted || stream.startedPlayback()
			if ctx.Err() == nil {
				stopErr = stream.stopPlayback()
			}
			unobserve()
			lastErr = errors.Join(err, stopErr)
			if stopErr == nil && isTransientTTSError(err) && attempt < maxRetries && !speechStarted {
				continue
			}
			return speechStarted, lastErr
		}

		if err := stream.closeAndWait(); err != nil {
			speechStarted := stream.startedPlayback()
			var stopErr error
			if ctx.Err() != nil {
				stream.interrupt()
			} else {
				stopErr = stream.stopPlayback()
			}
			unobserve()
			lastErr = errors.Join(err, stopErr)
			if stopErr == nil && isTransientTTSError(err) && attempt < maxRetries && !speechStarted {
				continue
			}
			return speechStarted, lastErr
		}
		unobserve()

		return stream.spokeSuccessfully(), nil
	}
	return false, lastErr
}

// isTransientTTSError checks if an error is transient and should be retried.
func isTransientTTSError(err error) bool {
	if err == nil {
		return false
	}
	if isRetryableAudioStartPlaybackError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"connection refused",
		"connection reset by peer",
		"broken pipe",
		"temporary failure",
		"temporarily unavailable",
		"i/o timeout",
		"eof",
		"dial tcp",
		"context deadline exceeded",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

// lookupCredentials does a case-insensitive map lookup so users don't have to
// match the exact registered provider casing in their toml.
func lookupCredentials(creds map[string]TTSProviderCredentials, provider string) (TTSProviderCredentials, bool) {
	if creds == nil {
		return TTSProviderCredentials{}, false
	}
	target := normalizeTTSProvider(provider)
	for k, v := range creds {
		if normalizeTTSProvider(k) == target {
			return v, true
		}
	}
	return TTSProviderCredentials{}, false
}

// ttsLoggerAdapter bridges the agent Logger to tts.Logger.
type ttsLoggerAdapter struct{ logger *Logger }

func (a *ttsLoggerAdapter) Info(format string, args ...any) {
	if a.logger != nil {
		a.logger.Info(format, args...)
	}
}

func (a *ttsLoggerAdapter) Warn(format string, args ...any) {
	if a.logger != nil {
		a.logger.Warn(format, args...)
	}
}

// streamSessionWriter wraps a tts.StreamSession to satisfy io.Writer.
// LLM streaming output is written byte-slice by byte-slice; we forward each
// fragment to the TTS session immediately.
type streamSessionWriter struct {
	mu          sync.Mutex
	session     tts.StreamSession
	sink        *tts.AudioServiceSink
	cancel      context.CancelFunc
	spoke       bool
	lastErr     error
	interrupted bool
}

func (w *streamSessionWriter) setCancel(cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancel = cancel
}

func (w *streamSessionWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	if w.interrupted {
		w.mu.Unlock()
		return len(p), nil
	}
	session := w.session
	w.mu.Unlock()

	if err := session.WriteText(string(p)); err != nil {
		w.mu.Lock()
		if w.lastErr == nil && !w.interrupted {
			w.lastErr = err
		}
		w.mu.Unlock()
	}
	// Always return success so the LLM stream is not interrupted by TTS errors.
	return len(p), nil
}

// ttsBufferResetter is implemented by TTS sessions that buffer text internally
// (e.g. sentence-boundary buffering in non-incremental providers) and can drop
// that buffer on demand.
type ttsBufferResetter interface {
	ResetBuffer()
}

// ResetBuffer discards any text buffered by the underlying TTS session but not
// yet synthesized. Used to prevent residual content from a tool-call turn from
// leaking into a later streaming turn.
func (w *streamSessionWriter) ResetBuffer() {
	w.mu.Lock()
	session := w.session
	w.mu.Unlock()
	if resetter, ok := session.(ttsBufferResetter); ok {
		resetter.ResetBuffer()
	}
}

func (w *streamSessionWriter) interrupt() {
	w.mu.Lock()
	if w.interrupted {
		w.mu.Unlock()
		return
	}
	w.interrupted = true
	w.spoke = false
	sink := w.sink
	cancel := w.cancel
	w.mu.Unlock()

	if sink != nil {
		_ = sink.Stop()
	}
	if cancel != nil {
		cancel()
	}
}

func (w *streamSessionWriter) spokeSuccessfully() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spoke && !w.interrupted
}

func (w *streamSessionWriter) startedPlayback() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sink != nil && w.sink.PCMBytes() > 0
}

func (w *streamSessionWriter) stopPlayback() error {
	w.mu.Lock()
	sink := w.sink
	w.mu.Unlock()
	if sink == nil {
		return nil
	}
	return sink.Stop()
}

// emittedSpeech reports whether callers must treat the response as already
// spoken. Once PCM reaches the playback sink, a later stream-close error must
// not cause the full response or a failure replacement to be played again.
func (w *streamSessionWriter) emittedSpeech(closeErr error) bool {
	return w.startedPlayback() || (closeErr == nil && w.spokeSuccessfully())
}

// closeAndWait flushes the session and waits for playback to drain.
func (w *streamSessionWriter) closeAndWait() error {
	w.mu.Lock()
	if w.interrupted {
		w.spoke = false
		w.mu.Unlock()
		return nil
	}
	session := w.session
	w.mu.Unlock()

	closeErr := session.Close()

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.interrupted {
		w.spoke = false
		return nil
	}
	if w.lastErr != nil {
		return w.lastErr
	}
	if closeErr != nil {
		return closeErr
	}
	if w.sink == nil || w.sink.PCMBytes() > 0 {
		w.spoke = true
	}
	return nil
}
