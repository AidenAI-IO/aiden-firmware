package agent

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	"github.com/tmc/langchaingo/llms/ollama"
)

type ModelResolver interface {
	Get() (llms.Model, error)
	CallOptions() []chains.ChainCallOption
}

type ModelManager struct {
	config ModelConfig
	model  llms.Model
}

func NewModelManager(config ModelConfig) *ModelManager {
	return &ModelManager{config: config}
}

func (m *ModelManager) Get() (llms.Model, error) {
	if m.model != nil {
		return m.model, nil
	}

	built, err := m.build(m.config)
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	m.model = built
	return built, nil
}

func (m *ModelManager) CallOptions() []chains.ChainCallOption {
	options := make([]chains.ChainCallOption, 0, 2)
	if m.config.Temperature != 0 {
		options = append(options, chains.WithTemperature(m.config.Temperature))
	}
	if m.config.MaxTokens > 0 {
		options = append(options, chains.WithMaxTokens(m.config.MaxTokens))
	}
	return options
}

func (m *ModelManager) build(cfg ModelConfig) (llms.Model, error) {
	switch strings.ToLower(cfg.Provider) {
	case "openai":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return newOpenAICompatibleModel(baseURL, cfg.Model, resolveToken(cfg), newRetryHTTPClient()), nil
	case "openrouter":
		token := resolveToken(cfg)
		if token == "" {
			return nil, fmt.Errorf("missing the OpenRouter API key, set it in the %s environment variable", cfg.TokenEnv)
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
		return newOpenAICompatibleModel(baseURL, cfg.Model, token, newRetryHTTPClient()), nil
	case "ollama":
		options := []ollama.Option{ollama.WithModel(cfg.Model)}
		if cfg.BaseURL != "" {
			options = append(options, ollama.WithServerURL(cfg.BaseURL))
		}
		return ollama.New(options...)
	case "fake":
		return fakellm.NewFakeLLM(cfg.Responses), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}

func resolveToken(cfg ModelConfig) string {
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	if cfg.TokenEnv != "" {
		return os.Getenv(cfg.TokenEnv)
	}
	return ""
}

// retryTransport retries on 5xx errors with exponential backoff
type retryTransport struct {
	wrapped    http.RoundTripper
	maxRetries int
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Read body so we can replay it on retry
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 2 * time.Second
			log.Printf("[http-retry] attempt %d/%d, waiting %v", attempt+1, t.maxRetries+1, delay)
			time.Sleep(delay)
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err = t.wrapped.RoundTrip(req)
		if err != nil {
			continue
		}

		if resp.StatusCode < 500 {
			return resp, nil
		}

		// Read error body for logging, then close
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Printf("[http-retry] got %d: %s", resp.StatusCode, string(errBody))

		// On last attempt, return a reconstructed response
		if attempt == t.maxRetries {
			resp.Body = io.NopCloser(bytes.NewReader(errBody))
			return resp, nil
		}
	}

	return resp, err
}

func newRetryHTTPClient() *http.Client {
	return &http.Client{
		Transport: &retryTransport{
			wrapped:    http.DefaultTransport,
			maxRetries: 2,
		},
	}
}
