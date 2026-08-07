package agent

import (
	"aiden-agent/internal/agent/model"
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
	"github.com/tmc/langchaingo/llms/ollama"
)

// Moonshot Kimi OpenAI-compatible endpoints. "kimi" targets the global site and
// "kimi-cn" targets the mainland China site.
const (
	moonshotGlobalBaseURL  = "https://api.moonshot.ai/v1"
	moonshotCNBaseURL      = "https://api.moonshot.cn/v1"
	fakeModelContextWindow = 1_000_000
)

// Volcengine Ark (火山方舟) OpenAI-compatible endpoint for the Doubao models.
// Ark also exposes an Anthropic-protocol endpoint at /api/compatible, which this
// repo does not use: every Ark model here is reached through the shared
// openAICompatibleModel.
const arkBeijingBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

type ModelManager struct {
	config ModelConfig
	proxy  ProxyConfig
	model  llms.Model

	specMu                    sync.Mutex
	providerSpec              model.ModelSpec
	providerSpecLoaded        bool
	providerSpecFetchStarted  bool
	metadataHTTPClient        *http.Client
	providerMetadataCachePath string
	rawHTTPLogDir             string
	rawHTTPLogSessionID       func() string
	storageMu                 sync.RWMutex
	storageMonitor            *StorageMonitor
}

func (m *ModelManager) SetStorageMonitor(monitor *StorageMonitor) {
	if m == nil {
		return
	}
	m.storageMu.Lock()
	m.storageMonitor = monitor
	m.storageMu.Unlock()
	if model, ok := m.model.(*openAICompatibleModel); ok && model.rawLogger != nil {
		model.rawLogger.SetStorageMonitor(monitor)
	}
}

func (m *ModelManager) currentStorageMonitor() *StorageMonitor {
	if m == nil {
		return nil
	}
	m.storageMu.RLock()
	defer m.storageMu.RUnlock()
	return m.storageMonitor
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

func (m *ModelManager) SetRawHTTPLogSessionIDProvider(provider func() string) {
	m.rawHTTPLogSessionID = provider
	if model, ok := m.model.(*openAICompatibleModel); ok && model.rawLogger != nil {
		model.rawLogger.SetSessionIDProvider(provider)
	}
}

// activeSessionID resolves the current session id via the configured provider.
// It reads m.rawHTTPLogSessionID at call time so the value is picked up even
// when the provider is set after the model is built.
func (m *ModelManager) activeSessionID() string {
	if m.rawHTTPLogSessionID == nil {
		return ""
	}
	return m.rawHTTPLogSessionID()
}

func (m *ModelManager) get() (llms.Model, error) {
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

func (m *ModelManager) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	model, err := m.get()
	if err != nil {
		return nil, err
	}
	return model.GenerateContent(ctx, messages, options...)
}

func (m *ModelManager) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	model, err := m.get()
	if err != nil {
		return "", err
	}
	return model.Call(ctx, prompt, options...)
}

func (m *ModelManager) CallOptions() []chains.ChainCallOption {
	options := make([]chains.ChainCallOption, 0, 2)
	if m.config.Temperature != nil {
		options = append(options, chains.WithTemperature(*m.config.Temperature))
	}
	if m.config.MaxResponseTokens > 0 {
		options = append(options, chains.WithMaxTokens(m.config.MaxResponseTokens))
	}
	return options
}

func (m *ModelManager) Spec() model.ModelSpec {
	spec, _ := LookupModelSpec(m.config.Provider, m.config.Model)

	explicitContextWindow := m.config.ContextWindow > 0
	explicitMaxOutput := m.config.ModelMaxOutputTokens > 0
	if explicitContextWindow {
		spec.ContextWindow = m.config.ContextWindow
	}
	if explicitMaxOutput {
		spec.MaxOutput = m.config.ModelMaxOutputTokens
	}
	if !explicitContextWindow && strings.EqualFold(m.config.Provider, "fake") && spec.ContextWindow <= 0 {
		spec.ContextWindow = fakeModelContextWindow
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

	spec.Provider = m.config.Provider
	spec.Name = m.config.Model

	return spec
}

func (m *ModelManager) build(cfg ModelConfig) (llms.Model, error) {
	definition, ok := lookupModelProviderDefinition(cfg.Provider)
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
	return definition.build(m, cfg)
}

func (m *ModelManager) buildOpenAICompatibleModel(cfg ModelConfig, defaultBaseURL string) llms.Model {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return newOpenAICompatibleModel(baseURL, cfg.Model, resolveToken(cfg), newRetryHTTPClient(m.proxy), m.openAICompatibleOptions(cfg)...)
}

func (m *ModelManager) buildOpenRouterModel(cfg ModelConfig) (llms.Model, error) {
	token := resolveToken(cfg)
	if token == "" {
		if env := strings.TrimSpace(cfg.TokenEnv); env != "" {
			return nil, fmt.Errorf("missing the OpenRouter API key, set it in the %s environment variable", env)
		}
		return nil, fmt.Errorf("missing the OpenRouter API key, set api_key or token_env on the provider record")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	opts := append(m.openAICompatibleOptions(cfg),
		withOpenAICompatibleSessionSticky(m.activeSessionID),
		withOpenAICompatibleRouterMetadata(),
		withOpenAICompatibleOpenRouterReasoning())
	if m.cachedOpenRouterPromptCachePolicy().UsesExplicitCacheControl() {
		opts = append(opts, withOpenAICompatibleExplicitPromptCache())
	}
	return newOpenAICompatibleModel(baseURL, cfg.Model, token, newRetryHTTPClient(m.proxy), opts...), nil
}

func (m *ModelManager) buildOllamaModel(cfg ModelConfig) (llms.Model, error) {
	options := []ollama.Option{ollama.WithModel(cfg.Model), ollama.WithHTTPClient(newProxyHTTPClient(m.proxy))}
	if cfg.BaseURL != "" {
		options = append(options, ollama.WithServerURL(cfg.BaseURL))
	}
	return ollama.New(options...)
}

func openRouterExplicitPromptCacheSupported(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "~")
	if idx := strings.Index(model, ":"); idx >= 0 {
		model = model[:idx]
	}
	switch {
	case strings.HasPrefix(model, "anthropic/"):
		return true
	}
	switch model {
	case "deepseek/deepseek-v3.2",
		"qwen/qwen3-max",
		"qwen/qwen-plus",
		"qwen/qwen3.6-plus",
		"qwen/qwen3-coder-plus",
		"qwen/qwen3-coder-flash":
		return true
	default:
		return false
	}
}

func (m *ModelManager) openAICompatibleOptions(cfg ModelConfig) []openAICompatibleModelOption {
	var opts []openAICompatibleModelOption
	if cfg.LogRawHTTP && strings.TrimSpace(m.rawHTTPLogDir) != "" {
		logger := newLLMRawHTTPLogger(m.rawHTTPLogDir, "")
		logger.SetSessionIDProvider(m.rawHTTPLogSessionID)
		logger.SetStorageMonitor(m.currentStorageMonitor())
		opts = append(opts, withOpenAICompatibleRawHTTPLogger(logger))
	}
	if cfg.ReasoningEffort != "" {
		opts = append(opts, withOpenAICompatibleReasoningEffort(cfg.ReasoningEffort))
	}
	if cfg.Temperature != nil {
		opts = append(opts, withOpenAICompatibleTemperature(cfg.Temperature))
	}
	return opts
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
