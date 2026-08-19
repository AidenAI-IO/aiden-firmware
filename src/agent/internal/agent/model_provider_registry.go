package agent

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	"github.com/tmc/langchaingo/llms/ollama"
)

type ModelBuildContext struct {
	HTTPClient        *http.Client
	OllamaHTTPClient  *http.Client
	RawHTTPLogger     RawHTTPLogger
	SessionIDProvider func() string
	PromptCachePolicy PromptCachePolicy
}

type ModelProviderBuilder func(ModelBuildContext, ModelConfig) (llms.Model, error)

type modelProviderDefinition struct {
	providerType              string
	allowsCustomBaseURL       bool
	supportsResponses         bool
	supportsResponsesStateful bool
	hiddenFromConfigUI        bool
	build                     ModelProviderBuilder
}

var modelProviderDefinitions = []modelProviderDefinition{
	{
		providerType:      "openrouter",
		supportsResponses: true,
		build:             buildOpenRouterModel,
	},
	{
		providerType:              "openai",
		allowsCustomBaseURL:       true,
		supportsResponses:         true,
		supportsResponsesStateful: true,
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildOpenAICompatibleModel(ctx, cfg, "https://api.openai.com/v1"), nil
		},
	},
	{
		providerType:        "anthropic",
		allowsCustomBaseURL: true,
		build:               buildAnthropicModel,
	},
	{
		providerType:      "kimi",
		supportsResponses: true,
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildOpenAICompatibleModel(ctx, cfg, moonshotGlobalBaseURL), nil
		},
	},
	{
		providerType:      "kimi-cn",
		supportsResponses: true,
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildOpenAICompatibleModel(ctx, cfg, moonshotCNBaseURL), nil
		},
	},
	{
		providerType:      "volcengine",
		supportsResponses: true,
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildOpenAICompatibleModel(ctx, cfg, arkBeijingBaseURL), nil
		},
	},
	{
		providerType:        "ollama",
		allowsCustomBaseURL: true,
		build:               buildOllamaModel,
	},
	{
		providerType:              "fake",
		supportsResponses:         true,
		supportsResponsesStateful: true,
		hiddenFromConfigUI:        true,
		build: func(_ ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return fakellm.NewFakeLLM(cfg.Responses), nil
		},
	},
}

func buildOpenAICompatibleModel(ctx ModelBuildContext, cfg ModelConfig, defaultBaseURL string) llms.Model {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	apiMode := normalizeModelAPIMode(cfg.APIMode)
	if apiMode == modelAPIModeResponses || apiMode == modelAPIModeResponsesStateful {
		return newResponsesModel(baseURL, cfg.Model, resolveToken(cfg), ctx.HTTPClient, responsesModelOptions{
			rawLogger:              ctx.RawHTTPLogger,
			sessionIDProvider:      ctx.SessionIDProvider,
			reasoningEffort:        cfg.ReasoningEffort,
			temperature:            cfg.Temperature,
			providerManagedContext: apiMode == modelAPIModeResponsesStateful,
			contextManagement:      cfg.ResponsesContextManagement,
			compactThreshold:       cfg.ResponsesCompactThreshold,
			truncation:             cfg.ResponsesTruncation,
			include:                cfg.ResponsesInclude,
		})
	}
	return newOpenAICompatibleModel(baseURL, cfg.Model, resolveToken(cfg), ctx.HTTPClient, openAICompatibleOptions(ctx, cfg)...)
}

func buildOpenRouterModel(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
	token := resolveToken(cfg)
	if token == "" {
		if env, ok := providerAPIKeyEnv(cfg.APIKey); ok && env != "" {
			return nil, fmt.Errorf("missing the OpenRouter API key, set it in the %s environment variable", env)
		}
		return nil, fmt.Errorf("missing the OpenRouter API key, set api_key on the provider record")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	opts := append(openAICompatibleOptions(ctx, cfg),
		withOpenAICompatibleSessionSticky(ctx.SessionIDProvider),
		withOpenAICompatibleRouterMetadata(),
		withOpenAICompatibleOpenRouterReasoning())
	if ctx.PromptCachePolicy.UsesExplicitCacheControl() {
		opts = append(opts, withOpenAICompatibleExplicitPromptCache())
	}
	apiMode := normalizeModelAPIMode(cfg.APIMode)
	if apiMode == modelAPIModeResponses || apiMode == modelAPIModeResponsesStateful {
		return newResponsesModel(baseURL, cfg.Model, token, ctx.HTTPClient, responsesModelOptions{
			rawLogger:              ctx.RawHTTPLogger,
			sessionIDProvider:      ctx.SessionIDProvider,
			reasoningEffort:        cfg.ReasoningEffort,
			temperature:            cfg.Temperature,
			routerMetadata:         true,
			providerManagedContext: apiMode == modelAPIModeResponsesStateful,
			contextManagement:      cfg.ResponsesContextManagement,
			compactThreshold:       cfg.ResponsesCompactThreshold,
			truncation:             cfg.ResponsesTruncation,
			include:                cfg.ResponsesInclude,
		}), nil
	}
	return newOpenAICompatibleModel(baseURL, cfg.Model, token, ctx.HTTPClient, opts...), nil
}

func buildAnthropicModel(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
	if apiMode := normalizeModelAPIMode(cfg.APIMode); apiMode == modelAPIModeResponses || apiMode == modelAPIModeResponsesStateful {
		return nil, fmt.Errorf("model.api_mode=%s requires an OpenAI-compatible /responses endpoint; configure that endpoint with provider type openai", apiMode)
	}
	token, useBearerAuth := resolveAnthropicToken(cfg.APIKey)
	if token == "" {
		if env, ok := providerAPIKeyEnv(cfg.APIKey); ok && env != "" {
			return nil, fmt.Errorf("missing the Anthropic API key, set it in the %s environment variable", env)
		}
		return nil, fmt.Errorf("missing the Anthropic API key, set api_key on the provider record, ANTHROPIC_AUTH_TOKEN, or ANTHROPIC_API_KEY")
	}
	options := buildAnthropicModelOptions(ctx, cfg)
	if useBearerAuth {
		options = append(options, withAnthropicBearerAuth())
	}
	return newAnthropicModel(resolveAnthropicBaseURL(cfg.BaseURL), cfg.Model, token, ctx.HTTPClient, options...), nil
}

func buildOllamaModel(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
	if apiMode := normalizeModelAPIMode(cfg.APIMode); apiMode == modelAPIModeResponses || apiMode == modelAPIModeResponsesStateful {
		return nil, fmt.Errorf("model.api_mode=%s requires an OpenAI-compatible /responses endpoint; configure that endpoint with provider type openai", apiMode)
	}
	options := []ollama.Option{ollama.WithModel(cfg.Model), ollama.WithHTTPClient(ctx.OllamaHTTPClient)}
	if cfg.BaseURL != "" {
		options = append(options, ollama.WithServerURL(cfg.BaseURL))
	}
	return ollama.New(options...)
}

func lookupModelProviderDefinition(providerType string) (modelProviderDefinition, bool) {
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	for _, definition := range modelProviderDefinitions {
		if definition.providerType == providerType {
			return definition, true
		}
	}
	return modelProviderDefinition{}, false
}

func modelProviderTypes() []string {
	types := make([]string, 0, len(modelProviderDefinitions))
	for _, definition := range modelProviderDefinitions {
		types = append(types, definition.providerType)
	}
	return types
}

func modelProviderTypesForConfigUI() []string {
	types := make([]string, 0, len(modelProviderDefinitions))
	for _, definition := range modelProviderDefinitions {
		if !definition.hiddenFromConfigUI {
			types = append(types, definition.providerType)
		}
	}
	return types
}

func modelProviderTypesAllowingCustomBaseURLForConfigUI() []string {
	types := make([]string, 0, len(modelProviderDefinitions))
	for _, definition := range modelProviderDefinitions {
		if !definition.hiddenFromConfigUI && definition.allowsCustomBaseURL {
			types = append(types, definition.providerType)
		}
	}
	return types
}
