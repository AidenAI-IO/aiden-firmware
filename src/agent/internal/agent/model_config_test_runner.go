package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

type ModelProviderTestRequest struct {
	Provider                          string
	APIKey                            string
	Model                             string
	APIMode                           string
	ResponsesContextManagement        string
	ResponsesCompactThreshold         int
	ResponsesContextEditTrigger       int
	ResponsesContextEditKeep          int
	ResponsesContextEditClearThinking bool
	ResponsesTruncation               string
	ResponsesInclude                  []string
	Temperature                       *float64
	ReasoningEffort                   string
	ThinkingBudgetTokens              int
}

type ModelProviderTestResult struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func RunModelProviderTest(ctx context.Context, cfg Config, req ModelProviderTestRequest) (ModelProviderTestResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ModelProviderTestResult{}, err
	}

	if err := applyModelProviderTestRequest(&cfg, req); err != nil {
		return ModelProviderTestResult{}, err
	}
	if err := cfg.Validate(); err != nil {
		return ModelProviderTestResult{}, err
	}
	if strings.TrimSpace(cfg.Model.Provider) == "" {
		return ModelProviderTestResult{}, errors.New("model.provider is required")
	}
	if cfg.Model.Provider != "fake" && strings.TrimSpace(cfg.Model.Model) == "" {
		return ModelProviderTestResult{Provider: cfg.Model.Provider}, errors.New("model.model is required")
	}

	manager := NewModelManager(cfg.Model, ProxyConfigFromEnvironment())
	if _, err := manager.Call(ctx, "hello", llms.WithMaxTokens(4)); err != nil {
		return ModelProviderTestResult{Provider: cfg.Model.Provider, Model: cfg.Model.Model}, err
	}
	return ModelProviderTestResult{Provider: cfg.Model.Provider, Model: cfg.Model.Model}, nil
}

func applyModelProviderTestRequest(cfg *Config, req ModelProviderTestRequest) error {
	if cfg == nil {
		return nil
	}
	if provider := strings.TrimSpace(req.Provider); provider != "" {
		cfg.Model.Provider = provider
	}
	if req.APIKey != "" {
		cfg.Model.APIKey = req.APIKey
	}
	cfg.Model.Model = req.Model
	cfg.Model.APIMode = req.APIMode
	cfg.Model.ResponsesContextManagement = req.ResponsesContextManagement
	cfg.Model.ResponsesCompactThreshold = req.ResponsesCompactThreshold
	cfg.Model.ResponsesContextEditTrigger = req.ResponsesContextEditTrigger
	cfg.Model.ResponsesContextEditKeep = req.ResponsesContextEditKeep
	cfg.Model.ResponsesContextEditClearThinking = req.ResponsesContextEditClearThinking
	cfg.Model.ResponsesTruncation = req.ResponsesTruncation
	cfg.Model.ResponsesInclude = append([]string(nil), req.ResponsesInclude...)
	cfg.Model.BaseURL = ""
	cfg.Model.Temperature = nil
	if req.Temperature != nil {
		temperature := *req.Temperature
		cfg.Model.Temperature = &temperature
	}
	cfg.Model.ReasoningEffort = req.ReasoningEffort
	cfg.Model.ThinkingBudgetTokens = req.ThinkingBudgetTokens

	if err := resolveModelProvider(cfg, &cfg.Model); err != nil {
		return err
	}
	clearNonAllowedModelBaseURL(&cfg.Model)
	applyModelTemperatureDefault(&cfg.Model)
	applyModelReasoningEffortDefault(&cfg.Model)
	return nil
}
