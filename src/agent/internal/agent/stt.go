package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

type STTCapabilities struct {
	SupportsStreamingUpload bool
}

type STTStreamConfig struct {
	SampleRate int
	Channels   int
	BitWidth   int
}

type STTStreamUploader interface {
	UploadPCM(pcm []byte) error
	Finalize() (string, error)
	Close() error
}

// STTClient is the interface for speech-to-text providers
type STTClient interface {
	Capabilities() STTCapabilities
	TranscribeWAV(wavData []byte) (string, error)
	NewStreamingUploader(ctx context.Context, cfg STTStreamConfig) (STTStreamUploader, error)
}

// OpenAIWhisperSTT implements STT using OpenAI Whisper API
type OpenAIWhisperSTT struct {
	apiKey     string
	model      string
	baseURL    string
	language   string
	httpClient *http.Client
}

// NewOpenAIWhisperSTT creates a new OpenAI Whisper STT client
func NewOpenAIWhisperSTT(apiKey, model, baseURL, language string, httpClients ...*http.Client) *OpenAIWhisperSTT {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = defaultSTTModel
	}
	httpClient := http.DefaultClient
	if len(httpClients) > 0 && httpClients[0] != nil {
		httpClient = httpClients[0]
	}
	return &OpenAIWhisperSTT{
		apiKey:     apiKey,
		model:      model,
		baseURL:    baseURL,
		language:   language,
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

	// Add language field if specified
	if s.language != "" {
		if err := writer.WriteField("language", s.language); err != nil {
			return "", fmt.Errorf("write language field: %w", err)
		}
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

func (s *OpenAIWhisperSTT) Capabilities() STTCapabilities {
	return STTCapabilities{}
}

func (s *OpenAIWhisperSTT) NewStreamingUploader(_ context.Context, _ STTStreamConfig) (STTStreamUploader, error) {
	return nil, fmt.Errorf("provider %q does not support streaming upload", "openai-whisper")
}
