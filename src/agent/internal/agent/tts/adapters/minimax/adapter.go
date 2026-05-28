// Package minimax implements TTS adapters for Minimax.
//
// Minimax does not support true incremental text streaming: the WebSocket
// `task_continue` event must contain a complete text fragment, not partial
// tokens. This adapter buffers WriteText calls internally until a sentence
// boundary is reached, then performs a full task_start/task_continue/task_finish
// cycle for that sentence.
//
// Two transport modes:
//   - HTTP (provider name "minimax"): one HTTPS POST per sentence
//   - WebSocket (provider name "minimax-ws"): persistent ws connection,
//     reconnected as needed since server closes after task_finish.
package minimax

import "aiden-agent/internal/agent/tts"

func init() {
	tts.Register("minimax", NewHTTP)
	tts.Register("minimax-ws", NewWebSocket)
}

const (
	defaultModel      = "speech-2.8-hd"
	defaultSampleRate = 16000
	defaultChannels   = 1
	defaultBitWidth   = 16
	defaultVoiceID    = "male-qn-qingse"
	defaultEmotion    = "happy"
)

// commonConfig holds the fields shared by HTTP and WebSocket adapters.
type commonConfig struct {
	apiKey   string
	model    string
	voiceID  string
	emotion  string
	speed    float64
	proxy    tts.ProxyConfig
	endpoint string
}

func parseConfig(cfg tts.ProviderConfig, defaultEndpoint string) commonConfig {
	voiceID := cfg.Voice
	if voiceID == "" {
		voiceID = defaultVoiceID
	}
	emotion := defaultEmotion
	if e, ok := cfg.Extra["emotion"].(string); ok && e != "" {
		emotion = e
	}
	model := defaultModel
	if m, ok := cfg.Extra["model"].(string); ok && m != "" {
		model = m
	}
	speed := cfg.SpeedRatio
	if speed == 0 {
		speed = 1.0
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return commonConfig{
		apiKey:   cfg.APIKey,
		model:    model,
		voiceID:  voiceID,
		emotion:  emotion,
		speed:    speed,
		proxy:    cfg.Proxy,
		endpoint: endpoint,
	}
}

func capabilities() tts.Capabilities {
	return tts.Capabilities{
		SupportsContextContinuation: false,
		SupportedSampleRates:        []int{8000, 16000, 22050, 24000, 32000, 44100},
		RegionRestricted:            false,
	}
}
