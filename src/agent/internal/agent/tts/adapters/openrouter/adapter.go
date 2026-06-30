// Package openrouter implements a TTS adapter for OpenRouter's HTTP API.
//
// OpenRouter exposes a unified /audio/speech endpoint that routes to various
// TTS providers (OpenAI, Microsoft, Google, etc.). This adapter uses
// response_format=pcm to receive raw PCM s16le audio, avoiding the need for
// external audio decoding libraries.
//
// Text is buffered until a sentence boundary before synthesis, matching the
// pattern used by the minimax adapter.
package openrouter

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"

	"aiden-agent/internal/agent/tts"
)

const (
	ProviderName    = "openrouter"
	defaultEndpoint = "https://openrouter.ai/api/v1"
	defaultModel    = "openai/tts-1"
	defaultVoice    = "alloy"
)

func init() {
	tts.Register(ProviderName, New)
}

// Adapter is the OpenRouter TTS provider.
type Adapter struct {
	apiKey   string
	model    string
	voice    string
	endpoint string
	speed    float64
	proxy    tts.ProxyConfig
}

var _ tts.TTSProvider = (*Adapter)(nil)

// New constructs an OpenRouter TTS provider from the unified config.
func New(cfg tts.ProviderConfig) (tts.TTSProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openrouter: api_key is required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	model := defaultModel
	if m, ok := cfg.Extra["model"].(string); ok && m != "" {
		model = m
	}
	voice := cfg.Voice
	if voice == "" {
		voice = defaultVoice
	}
	speed := cfg.SpeedRatio
	if speed == 0 {
		speed = 1.0
	}
	return &Adapter{
		apiKey:   cfg.APIKey,
		model:    model,
		voice:    voice,
		endpoint: endpoint,
		speed:    speed,
		proxy:    cfg.Proxy,
	}, nil
}

func (a *Adapter) Name() string { return ProviderName }

func (a *Adapter) Capabilities() tts.Capabilities {
	return tts.Capabilities{
		SupportsContextContinuation: false,
		SupportedSampleRates:        []int{24000},
		RegionRestricted:            true,
	}
}

func (a *Adapter) Close() error { return nil }

func (a *Adapter) BeginStream(ctx context.Context, sink tts.AudioSink) (tts.StreamSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	httpClient := http.DefaultClient
	if a.proxy.AllProxy != "" || a.proxy.HTTPSProxy != "" || a.proxy.HTTPProxy != "" {
		httpClient = buildProxyHTTPClient(a.proxy)
	}
	return &session{
		ctx:        ctx,
		adapter:    a,
		sink:       sink,
		httpClient: httpClient,
		textBuffer: new(bytes.Buffer),
	}, nil
}

func buildProxyHTTPClient(p tts.ProxyConfig) *http.Client {
	proxyURL := p.AllProxy
	if proxyURL == "" {
		proxyURL = p.HTTPSProxy
	}
	if proxyURL == "" {
		proxyURL = p.HTTPProxy
	}
	if proxyURL == "" {
		return http.DefaultClient
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(_ *http.Request) (*url.URL, error) {
		return url.Parse(proxyURL)
	}
	return &http.Client{Transport: transport}
}
