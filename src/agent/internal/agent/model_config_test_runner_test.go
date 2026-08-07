package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunModelProviderTestUsesRuntimeProviderResolution(t *testing.T) {
	t.Setenv("AIDEN_MODEL_CONFIG_TEST_KEY", "model-config-test-secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer model-config-test-secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "hello"},
			}},
		})
	}))
	defer server.Close()

	cfg := Config{
		ModelProviders: map[string]ModelProvider{
			"work": {
				Type:    "openai",
				APIKey:  "$AIDEN_MODEL_CONFIG_TEST_KEY",
				BaseURL: server.URL + "/v1",
			},
		},
	}
	result, err := RunModelProviderTest(context.Background(), cfg, ModelProviderTestRequest{
		Provider: "work",
		Model:    "test-model",
	})
	if err != nil {
		t.Fatalf("RunModelProviderTest() error = %v", err)
	}
	if result.Provider != "openai" || result.Model != "test-model" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunModelProviderTestReportsInvalidProviderRecord(t *testing.T) {
	_, err := RunModelProviderTest(context.Background(), Config{
		ModelProviders: map[string]ModelProvider{"broken": {}},
	}, ModelProviderTestRequest{Provider: "broken", Model: "test-model"})
	if err == nil || !strings.Contains(err.Error(), "has no provider type specified") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyModelProviderTestRequestClearsStoredSamplingValuesBeforeDefaults(t *testing.T) {
	storedTemperature := 0.2
	cfg := Config{Model: ModelConfig{
		Provider:        "openai",
		Model:           "gpt-4o",
		Temperature:     &storedTemperature,
		ReasoningEffort: "high",
	}}

	err := applyModelProviderTestRequest(&cfg, ModelProviderTestRequest{
		Provider: "openai",
		Model:    "kimi-k3",
	})
	if err != nil {
		t.Fatalf("applyModelProviderTestRequest() error = %v", err)
	}
	if cfg.Model.Temperature == nil || *cfg.Model.Temperature != 1 {
		t.Fatalf("temperature = %v, want Kimi K3 default 1", cfg.Model.Temperature)
	}
	if cfg.Model.ReasoningEffort != "low" {
		t.Fatalf("reasoning_effort = %q, want Kimi K3 default low", cfg.Model.ReasoningEffort)
	}
}

func TestApplyModelProviderTestRequestKeepsExplicitSamplingValues(t *testing.T) {
	storedTemperature := 0.2
	requestedTemperature := 0.7
	cfg := Config{Model: ModelConfig{
		Provider:        "openai",
		Model:           "gpt-4o",
		Temperature:     &storedTemperature,
		ReasoningEffort: "high",
	}}

	err := applyModelProviderTestRequest(&cfg, ModelProviderTestRequest{
		Provider:           "openai",
		Model:              "kimi-k3",
		Temperature:        &requestedTemperature,
		TemperatureSet:     true,
		ReasoningEffort:    "medium",
		ReasoningEffortSet: true,
	})
	if err != nil {
		t.Fatalf("applyModelProviderTestRequest() error = %v", err)
	}
	if cfg.Model.Temperature == nil || *cfg.Model.Temperature != requestedTemperature {
		t.Fatalf("temperature = %v, want explicit %v", cfg.Model.Temperature, requestedTemperature)
	}
	if cfg.Model.ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort = %q, want explicit medium", cfg.Model.ReasoningEffort)
	}
}
