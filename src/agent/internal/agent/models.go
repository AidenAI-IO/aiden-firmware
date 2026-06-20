package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	"github.com/tmc/langchaingo/llms/ollama"
)

type ModelResolver interface {
	Get() (llms.Model, error)
	CallOptions() []chains.ChainCallOption
	// Spec returns capabilities (context window, max output) for the configured
	// model. Implementations return a zero-value ModelSpec for unknown models;
	// callers must fall back to a configured default.
	Spec() ModelSpec
}

type ModelManager struct {
	config ModelConfig
	proxy  ProxyConfig
	model  llms.Model

	specMu                    sync.Mutex
	providerSpec              ModelSpec
	providerSpecLoaded        bool
	providerSpecFetchStarted  bool
	metadataHTTPClient        *http.Client
	providerMetadataCachePath string
	rawHTTPLogDir             string
}

type ModelManagerOption func(*ModelManager)

func WithProviderModelMetadataCachePath(path string) ModelManagerOption {
	return func(m *ModelManager) {
		m.providerMetadataCachePath = strings.TrimSpace(path)
	}
}

func WithLLMRawHTTPLogDir(path string) ModelManagerOption {
	return func(m *ModelManager) {
		m.rawHTTPLogDir = strings.TrimSpace(path)
	}
}

func NewModelManager(config ModelConfig, proxy ProxyConfig, opts ...ModelManagerOption) *ModelManager {
	m := &ModelManager{config: config, proxy: proxy}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
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
	if m.config.MaxResponseTokens > 0 {
		options = append(options, chains.WithMaxTokens(m.config.MaxResponseTokens))
	}
	return options
}

func (m *ModelManager) Spec() ModelSpec {
	spec, _ := LookupModelSpec(m.config.Provider, m.config.Model)

	explicitContextWindow := m.config.ContextWindow > 0
	explicitMaxOutput := m.config.ModelMaxOutputTokens > 0
	if explicitContextWindow {
		spec.ContextWindow = m.config.ContextWindow
	}
	if explicitMaxOutput {
		spec.MaxOutput = m.config.ModelMaxOutputTokens
	}

	if m.needsProviderModelMetadataForSpec(spec) {
		providerSpec := m.cachedProviderModelSpec()
		if !explicitContextWindow && spec.ContextWindow <= 0 && providerSpec.ContextWindow > 0 {
			spec.ContextWindow = providerSpec.ContextWindow
		}
		if !explicitMaxOutput && spec.MaxOutput <= 0 && providerSpec.MaxOutput > 0 {
			spec.MaxOutput = providerSpec.MaxOutput
		}
	}
	return spec
}

func (m *ModelManager) build(cfg ModelConfig) (llms.Model, error) {
	switch strings.ToLower(cfg.Provider) {
	case "openai":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return newOpenAICompatibleModel(baseURL, cfg.Model, resolveToken(cfg), newRetryHTTPClient(m.proxy), m.openAICompatibleOptions(cfg)...), nil
	case "openrouter":
		token := resolveToken(cfg)
		if token == "" {
			return nil, fmt.Errorf("missing the OpenRouter API key, set it in the %s environment variable", cfg.TokenEnv)
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://openrouter.ai/api/v1"
		}
		return newOpenAICompatibleModel(baseURL, cfg.Model, token, newRetryHTTPClient(m.proxy), m.openAICompatibleOptions(cfg)...), nil
	case "ollama":
		options := []ollama.Option{ollama.WithModel(cfg.Model), ollama.WithHTTPClient(newProxyHTTPClient(m.proxy))}
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

func (m *ModelManager) openAICompatibleOptions(cfg ModelConfig) []openAICompatibleModelOption {
	if !cfg.LogRawHTTP || strings.TrimSpace(m.rawHTTPLogDir) == "" {
		return nil
	}
	return []openAICompatibleModelOption{
		withOpenAICompatibleRawHTTPLogger(newLLMRawHTTPLogger(m.rawHTTPLogDir)),
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

// retryTransport retries transient HTTP and transport failures with backoff.
type retryTransport struct {
	wrapped        http.RoundTripper
	maxRetries     int
	retryDelayBase time.Duration
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
	maxAttempts := t.maxRetries + 1

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err = t.wrapped.RoundTrip(req)
		if err != nil {
			if attempt == maxAttempts-1 || !shouldRetryTransportError(req.Context(), err) {
				if attempt > 0 {
					log.Printf("[WARN] [http-retry] giving up after attempt %d/%d: %v", attempt+1, maxAttempts, err)
				}
				return resp, err
			}
			log.Printf("[WARN] [http-retry] transport error on attempt %d/%d: %v", attempt+1, maxAttempts, err)
			if waitErr := t.waitBeforeRetry(req.Context(), attempt+1, maxAttempts); waitErr != nil {
				return resp, waitErr
			}
			continue
		}

		if !shouldRetryHTTPStatus(resp.StatusCode) {
			return resp, nil
		}

		// Read error body for logging, then close
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Printf("[WARN] [http-retry] got retryable status %d on attempt %d/%d: %s", resp.StatusCode, attempt+1, maxAttempts, string(errBody))

		// On last attempt, return a reconstructed response
		if attempt == maxAttempts-1 {
			resp.Body = io.NopCloser(bytes.NewReader(errBody))
			return resp, nil
		}
		if waitErr := t.waitBeforeRetry(req.Context(), attempt+1, maxAttempts); waitErr != nil {
			return nil, waitErr
		}
	}

	return resp, err
}

func (t *retryTransport) waitBeforeRetry(ctx context.Context, retryNumber, maxAttempts int) error {
	delay := time.Duration(retryNumber) * t.retryDelayBase
	log.Printf("[INFO] [http-retry] retrying attempt %d/%d after %v", retryNumber+1, maxAttempts, delay)
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func shouldRetryTransportError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return true
}

func shouldRetryHTTPStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func newRetryHTTPClient(proxy ProxyConfig) *http.Client {
	return &http.Client{
		Transport: &retryTransport{
			wrapped:        newProxyTransport(proxy),
			maxRetries:     5,
			retryDelayBase: 2 * time.Second,
		},
	}
}
