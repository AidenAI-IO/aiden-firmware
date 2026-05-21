package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// STTClient is the interface for speech-to-text providers
type STTClient interface {
	TranscribeWAV(wavData []byte) (string, error)
}

// OpenAIWhisperSTT implements STT using OpenAI Whisper API
type OpenAIWhisperSTT struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIWhisperSTT creates a new OpenAI Whisper STT client
func NewOpenAIWhisperSTT(apiKey, model, baseURL string, httpClients ...*http.Client) *OpenAIWhisperSTT {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "whisper-1"
	}
	httpClient := http.DefaultClient
	if len(httpClients) > 0 && httpClients[0] != nil {
		httpClient = httpClients[0]
	}
	return &OpenAIWhisperSTT{
		apiKey:     apiKey,
		model:      model,
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// TranscribeWAV transcribes a WAV file to text
func (s *OpenAIWhisperSTT) TranscribeWAV(wavData []byte) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(wavData); err != nil {
		return "", fmt.Errorf("write wav data: %w", err)
	}

	// Add model field
	if err := writer.WriteField("model", s.model); err != nil {
		return "", fmt.Errorf("write model field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close writer: %w", err)
	}

	// Create request
	url := fmt.Sprintf("%s/audio/transcriptions", s.baseURL)
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Text, nil
}

// TencentASRSTT implements STT using Tencent Cloud ASR
type TencentASRSTT struct {
	secretID        string
	secretKey       string
	region          string
	engineModelType string
}

// NewTencentASRSTT creates a new Tencent ASR STT client
func NewTencentASRSTT(secretID, secretKey, region, engineModelType string) *TencentASRSTT {
	if region == "" {
		region = "ap-guangzhou"
	}
	if engineModelType == "" {
		engineModelType = "16k_zh"
	}
	return &TencentASRSTT{
		secretID:        secretID,
		secretKey:       secretKey,
		region:          region,
		engineModelType: engineModelType,
	}
}

// TranscribeWAV transcribes a WAV file to text using Tencent ASR
func (s *TencentASRSTT) TranscribeWAV(wavData []byte) (string, error) {
	// TODO: Implement Tencent ASR API call
	// This requires Tencent Cloud SDK and signature v3 authentication
	// For now, return a placeholder error
	return "", fmt.Errorf("Tencent ASR not yet implemented")
}
