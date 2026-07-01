package agent

import (
	"context"
	"errors"
	"strings"

	"aiden-agent/internal/agent/tts"
)

const DefaultTTSPlaybackTestText = "test passed"

type TTSPlaybackTestRequest struct {
	Provider string
	APIKey   string
	Model    string
	VoiceID  string
	Emotion  string
	Speed    float64
	Text     string
}

type TTSPlaybackTestResult struct {
	Provider string `json:"provider"`
	Text     string `json:"text"`
	Spoke    bool   `json:"spoke"`
}

func RunTTSPlaybackTest(ctx context.Context, cfg Config, req TTSPlaybackTestRequest) (TTSPlaybackTestResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		text = DefaultTTSPlaybackTestText
	}
	applyTTSPlaybackTestRequest(&cfg, req)
	if strings.TrimSpace(cfg.TTS.Provider) == "" {
		return TTSPlaybackTestResult{Text: text}, errors.New("tts.provider is required")
	}

	provider, err := tts.New(buildTTSProviderConfig(cfg))
	if err != nil {
		return TTSPlaybackTestResult{Provider: cfg.TTS.Provider, Text: text}, err
	}
	manager := tts.NewProviderManager(provider, nil)
	defer manager.Close()

	audio := NewAudioServiceClient(cfg.Audio.SocketOrDefault())
	spoke, err := speakWithTTSManager(ctx, manager, audio, cfg, text)
	result := TTSPlaybackTestResult{
		Provider: manager.Current(),
		Text:     text,
		Spoke:    spoke,
	}
	if err != nil {
		return result, err
	}
	if !spoke {
		return result, errors.New("TTS completed without writing audio")
	}
	return result, nil
}

func applyTTSPlaybackTestRequest(cfg *Config, req TTSPlaybackTestRequest) {
	if cfg == nil {
		return
	}
	if provider := strings.TrimSpace(req.Provider); provider != "" {
		cfg.TTS.Provider = provider
	}
	// Always apply stdin values — they represent the user's current form state.
	// Empty string means the field is hidden/cleared, which should override
	// whatever is persisted in the toml file.
	cfg.TTS.Model = req.Model
	cfg.TTS.VoiceID = req.VoiceID
	cfg.TTS.Emotion = req.Emotion
	if req.APIKey != "" {
		cfg.TTS.APIKey = req.APIKey
	}
	if req.Speed > 0 {
		cfg.TTS.Speed = req.Speed
	}
}
