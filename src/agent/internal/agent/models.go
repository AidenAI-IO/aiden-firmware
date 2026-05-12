package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/llms/openai"
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
		options := []openai.Option{openai.WithModel(cfg.Model)}
		if token := resolveToken(cfg); token != "" {
			options = append(options, openai.WithToken(token))
		}
		if cfg.BaseURL != "" {
			options = append(options, openai.WithBaseURL(cfg.BaseURL))
		}
		return openai.New(options...)
	case "openrouter":
		options := []openai.Option{
			openai.WithModel(cfg.Model),
			openai.WithBaseURL("https://openrouter.ai/api/v1"),
		}
		token := resolveToken(cfg)
		if token == "" {
			return nil, fmt.Errorf("missing the OpenRouter API key, set it in the %s environment variable", cfg.TokenEnv)
		}
		options = append(options, openai.WithToken(token))
		return openai.New(options...)
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
	if cfg.Token != "" {
		return cfg.Token
	}
	if cfg.TokenEnv != "" {
		return os.Getenv(cfg.TokenEnv)
	}
	return ""
}
