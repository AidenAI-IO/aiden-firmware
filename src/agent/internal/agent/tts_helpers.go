package agent

import (
	"context"
	"strings"

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
	// Honor use_websocket for Minimax by routing to the WebSocket adapter.
	if provider == "minimax" && cfg.TTS.UseWebSocket {
		provider = "minimax-ws"
	}
	return buildTTSProviderConfigFor(cfg, provider)
}

// buildTTSProviderConfigFor resolves the config for a specific provider name,
// merging per-provider credentials over the top-level [tts] fallbacks.
func buildTTSProviderConfigFor(cfg Config, provider string) tts.ProviderConfig {
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

	return tts.ProviderConfig{
		Provider:   provider,
		APIKey:     apiKey,
		Voice:      voice,
		SampleRate: cfg.Audio.SampleRateOrDefault(),
		SpeedRatio: speed,
		Proxy: tts.ProxyConfig{
			HTTPProxy:  cfg.Proxy.HTTPProxy,
			HTTPSProxy: cfg.Proxy.HTTPSProxy,
			AllProxy:   cfg.Proxy.AllProxy,
			NoProxy:    cfg.Proxy.NoProxy,
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
	sink := tts.NewAudioServiceSink(newAudioBackend(audio), ttsPlaybackFormat(cfg, holder.Capabilities()))
	session, err := holder.BeginStream(ctx, sink)
	if err != nil {
		return nil, err
	}
	return &streamSessionWriter{session: session, sink: sink}, nil
}

func speakWithTTSManager(ctx context.Context, manager *tts.ProviderManager, audio *AudioServiceClient, cfg Config, text string) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" || manager == nil || audio == nil {
		return false, nil
	}
	stream, err := beginManagedTTSStream(ctx, manager, audio, cfg)
	if err != nil {
		return false, err
	}
	if _, err := stream.Write([]byte(text)); err != nil {
		_ = stream.closeAndWait()
		return false, err
	}
	if err := stream.closeAndWait(); err != nil {
		return false, err
	}
	return stream.spoke, nil
}

// lookupCredentials does a case-insensitive map lookup so users don't have to
// match the exact registered provider casing in their toml.
func lookupCredentials(creds map[string]TTSProviderCredentials, provider string) (TTSProviderCredentials, bool) {
	if creds == nil {
		return TTSProviderCredentials{}, false
	}
	target := strings.ToLower(strings.TrimSpace(provider))
	for k, v := range creds {
		if strings.ToLower(strings.TrimSpace(k)) == target {
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
	session tts.StreamSession
	sink    *tts.AudioServiceSink
	spoke   bool
	lastErr error
}

func (w *streamSessionWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.session.WriteText(string(p)); err != nil {
		if w.lastErr == nil {
			w.lastErr = err
		}
	}
	// Always return success so the LLM stream is not interrupted by TTS errors.
	return len(p), nil
}

// closeAndWait flushes the session and waits for playback to drain.
func (w *streamSessionWriter) closeAndWait() error {
	closeErr := w.session.Close()
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
