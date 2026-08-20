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
			return buildOpenAICompatibleModel(ctx, cfg, "https://api.openai.com/v1", responsesDialectOpenAI), nil
		},
	},
	{
		providerType:        "anthropic",
		allowsCustomBaseURL: true,
		build:               buildAnthropicModel,
	},
	{
		providerType: "kimi",
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildKimiModel(ctx, cfg, moonshotGlobalBaseURL)
		},
	},
	{
		providerType: "kimi-cn",
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildKimiModel(ctx, cfg, moonshotCNBaseURL)
		},
	},
	{
		providerType:              "volcengine",
		supportsResponses:         true,
		supportsResponsesStateful: true,
		build: func(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
			return buildOpenAICompatibleModel(ctx, cfg, arkBeijingBaseURL, responsesDialectVolcengine), nil
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

func buildOpenAICompatibleModel(ctx ModelBuildContext, cfg ModelConfig, defaultBaseURL string, dialect responsesDialect) llms.Model {
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
			dialect:                dialect,
		})
	}
	return newOpenAICompatibleModel(baseURL, cfg.Model, resolveToken(cfg), ctx.HTTPClient, openAICompatibleOptions(ctx, cfg)...)
}

func buildKimiModel(ctx ModelBuildContext, cfg ModelConfig, defaultBaseURL string) (llms.Model, error) {
	apiMode := normalizeModelAPIMode(cfg.APIMode)
	if apiMode == modelAPIModeResponses || apiMode == modelAPIModeResponsesStateful {
		return nil, fmt.Errorf("model.api_mode=%s is not supported by Moonshot Kimi; its official endpoint implements OpenAI-compatible Chat Completions, not /responses", apiMode)
	}
	return buildOpenAICompatibleModel(ctx, cfg, defaultBaseURL, responsesDialectOpenAI), nil
}

func buildOpenRouterModel(ctx ModelBuildContext, cfg ModelConfig) (llms.Model, error) {
	apiMode := normalizeModelAPIMode(cfg.APIMode)
	if apiMode == modelAPIModeResponsesStateful {
		return nil, fmt.Errorf("model.api_mode=responses_stateful is not supported by OpenRouter; its /responses endpoint is stateless and rejects store=true or previous_response_id")
	}
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
	if apiMode == modelAPIModeResponses {
		return newResponsesModel(baseURL, cfg.Model, token, ctx.HTTPClient, responsesModelOptions{
			rawLogger:              ctx.RawHTTPLogger,
			sessionIDProvider:      ctx.SessionIDProvider,
			reasoningEffort:        cfg.ReasoningEffort,
			temperature:            cfg.Temperature,
			routerMetadata:         true,
			providerManagedContext: false,
			contextManagement:      cfg.ResponsesContextManagement,
			compactThreshold:       cfg.ResponsesCompactThreshold,
			truncation:             cfg.ResponsesTruncation,
			include:                cfg.ResponsesInclude,
			dialect:                responsesDialectOpenRouter,
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
