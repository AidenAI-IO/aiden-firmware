package agent

import (
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
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
	config          ModelConfig
	proxy           ProxyConfig
	model           llms.Model
	modelMu         sync.Mutex
	bindingsMu      sync.Mutex
	runtimeBindings *ModelRuntimeBindings

	specMu                    sync.Mutex
	providerSpec              model.ModelSpec
	providerSpecLoaded        bool
	providerSpecFetchStarted  bool
	metadataHTTPClient        *http.Client
	providerMetadataCachePath string
	rawHTTPLogDir             string
}

func (m *ModelManager) SetStorageMonitor(monitor *StorageMonitor) {
	if m == nil {
		return
	}
	if monitor == nil {
		m.bindings().SetStorageWriteGate(nil)
		return
	}
	m.bindings().SetStorageWriteGate(monitor)
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
	m := &ModelManager{
		config:          config,
		proxy:           proxy,
		runtimeBindings: NewModelRuntimeBindings(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

func (m *ModelManager) SetSessionIDProvider(provider func() string) {
	if m == nil {
		return
	}
	m.bindings().SetSessionIDProvider(provider)
}

func (m *ModelManager) bindings() *ModelRuntimeBindings {
	m.bindingsMu.Lock()
	defer m.bindingsMu.Unlock()
	if m.runtimeBindings == nil {
		m.runtimeBindings = NewModelRuntimeBindings()
	}
	return m.runtimeBindings
}

func (m *ModelManager) get() (llms.Model, error) {
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	if m.model != nil {
		return m.model, nil
	}

	built, err := m.build()
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

// GenerateContentFromMessageList preserves provider-specific transcript
// metadata through the runtime's ModelManager wrapper. Responses models need
// this path for opaque stateless output replay and previous_response_id
// chaining; ordinary models fall back to the common LangChain message shape.
func (m *ModelManager) GenerateContentFromMessageList(ctx context.Context, messageList []messages.Message, options ...llms.CallOption) (*llms.ContentResponse, error) {
	model, err := m.get()
	if err != nil {
		return nil, err
	}
	if contextModel, ok := model.(interface {
		GenerateContentFromMessageList(context.Context, []messages.Message, ...llms.CallOption) (*llms.ContentResponse, error)
	}); ok {
		return contextModel.GenerateContentFromMessageList(ctx, messageList, options...)
	}
	return model.GenerateContent(ctx, messages.ConvertMessageList(messageList), options...)
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

func (m *ModelManager) build() (llms.Model, error) {
	providerType := effectiveModelProviderType(m.config.Provider, m.config.BaseURL)
	definition, ok := lookupModelProviderDefinition(providerType)
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", m.config.Provider)
	}
	return definition.build(m.buildContext(), m.config)
}

func (m *ModelManager) buildContext() ModelBuildContext {
	var logger RawHTTPLogger
	if m.config.LogRawHTTP && strings.TrimSpace(m.rawHTTPLogDir) != "" {
		logger = newLLMRawHTTPLogger(m.rawHTTPLogDir, m.bindings())
	}
	return ModelBuildContext{
		HTTPClient:        newRetryHTTPClient(m.proxy),
		OllamaHTTPClient:  newProxyHTTPClient(m.proxy),
		RawHTTPLogger:     logger,
		SessionIDProvider: m.bindings().CurrentSessionID,
		PromptCachePolicy: m.cachedOpenRouterPromptCachePolicy(),
	}
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

func openAICompatibleOptions(ctx ModelBuildContext, cfg ModelConfig) []openAICompatibleModelOption {
	var opts []openAICompatibleModelOption
	if ctx.RawHTTPLogger != nil {
		opts = append(opts, withOpenAICompatibleRawHTTPLogger(ctx.RawHTTPLogger))
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
	return resolveProviderAPIKey(cfg.APIKey)
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
