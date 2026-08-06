package agent

import (
	"strings"

	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
)

type modelProviderDefinition struct {
	providerType        string
	allowsCustomBaseURL bool
	build               func(*ModelManager, ModelConfig) (llms.Model, error)
}

var modelProviderDefinitions = []modelProviderDefinition{
	{
		providerType: "openrouter",
		build:        (*ModelManager).buildOpenRouterModel,
	},
	{
		providerType:        "openai",
		allowsCustomBaseURL: true,
		build: func(m *ModelManager, cfg ModelConfig) (llms.Model, error) {
			return m.buildOpenAICompatibleModel(cfg, "https://api.openai.com/v1"), nil
		},
	},
	{
		providerType: "kimi",
		build: func(m *ModelManager, cfg ModelConfig) (llms.Model, error) {
			return m.buildOpenAICompatibleModel(cfg, moonshotGlobalBaseURL), nil
		},
	},
	{
		providerType: "kimi-cn",
		build: func(m *ModelManager, cfg ModelConfig) (llms.Model, error) {
			return m.buildOpenAICompatibleModel(cfg, moonshotCNBaseURL), nil
		},
	},
	{
		providerType: "volcengine",
		build: func(m *ModelManager, cfg ModelConfig) (llms.Model, error) {
			return m.buildOpenAICompatibleModel(cfg, arkBeijingBaseURL), nil
		},
	},
	{
		providerType:        "ollama",
		allowsCustomBaseURL: true,
		build:               (*ModelManager).buildOllamaModel,
	},
	{
		providerType: "fake",
		build: func(_ *ModelManager, cfg ModelConfig) (llms.Model, error) {
			return fakellm.NewFakeLLM(cfg.Responses), nil
		},
	},
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

func modelProviderTypesAllowingCustomBaseURL() []string {
	types := make([]string, 0, len(modelProviderDefinitions))
	for _, definition := range modelProviderDefinitions {
		if definition.allowsCustomBaseURL {
			types = append(types, definition.providerType)
		}
	}
	return types
}
