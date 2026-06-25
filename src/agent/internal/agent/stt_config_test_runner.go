package agent

import (
	"context"
	"errors"
	"strings"
)

type STTTranscriptionTestRequest struct {
	Provider        string
	Language        string
	APIKey          string
	Model           string
	BaseURL         string
	AppID           string
	SecretID        string
	SecretKey       string
	Region          string
	EngineModelType string
	WAVData         []byte
}

type STTTranscriptionTestResult struct {
	Provider   string `json:"provider"`
	Transcript string `json:"transcript"`
}

func RunSTTTranscriptionTest(ctx context.Context, cfg Config, req STTTranscriptionTestRequest) (STTTranscriptionTestResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return STTTranscriptionTestResult{}, err
	}

	applySTTTranscriptionTestRequest(&cfg, req)
	if strings.TrimSpace(cfg.STT.Provider) == "" {
		return STTTranscriptionTestResult{}, errors.New("stt.provider is required")
	}
	if len(req.WAVData) == 0 {
		return STTTranscriptionTestResult{Provider: cfg.STT.Provider}, errors.New("audio data is required")
	}

	client, err := NewSTTClientFromConfig(cfg)
	if err != nil {
		return STTTranscriptionTestResult{Provider: cfg.STT.Provider}, err
	}
	transcript, err := client.TranscribeWAV(req.WAVData)
	result := STTTranscriptionTestResult{
		Provider:   cfg.STT.Provider,
		Transcript: strings.TrimSpace(transcript),
	}
	if err != nil {
		return result, err
	}
	if result.Transcript == "" {
		return result, errors.New("STT returned empty transcript")
	}
	return result, nil
}

func applySTTTranscriptionTestRequest(cfg *Config, req STTTranscriptionTestRequest) {
	if cfg == nil {
		return
	}
	if provider := strings.TrimSpace(req.Provider); provider != "" {
		cfg.STT.Provider = provider
	}
	if language := strings.TrimSpace(req.Language); language != "" {
		cfg.STT.Language = language
	}
	if req.APIKey != "" {
		cfg.STT.APIKey = req.APIKey
	}
	if req.Model != "" {
		cfg.STT.Model = req.Model
	}
	if req.BaseURL != "" {
		cfg.STT.BaseURL = req.BaseURL
	}
	if req.AppID != "" {
		cfg.STT.AppID = req.AppID
	}
	if req.SecretID != "" {
		cfg.STT.SecretID = req.SecretID
	}
	if req.SecretKey != "" {
		cfg.STT.SecretKey = req.SecretKey
	}
	if req.Region != "" {
		cfg.STT.Region = req.Region
	}
	if req.EngineModelType != "" {
		cfg.STT.EngineModelType = req.EngineModelType
	}
}
