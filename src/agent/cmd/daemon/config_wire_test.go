package main

import (
	"strings"
	"testing"
)

// These tests exercise the real config_web <-> agent wire contract: the JSON
// shape produced by config_web.cpp's config_to_json() (snake_case keys, agent
// settings nested under "agent", search reporting only has_api_key). The
// PascalCase fixtures in config_commands_test.go validate Config.Validate()
// directly and never touch this decode path, so a contract drift between the
// C++ serializer and the Go decoder would pass there while silently accepting
// every invalid config in production. checkConfig() is the guard against that.

func checkWire(t *testing.T, payload string) ValidationResult {
	t.Helper()
	result, err := checkConfig(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("checkConfig returned decode error for valid JSON: %v", err)
	}
	return result
}

// TestConfigCheck_WireFormatContract is the regression test for the bug where
// agent.Config (TOML-tagged only) was decoded straight from the snake_case /
// nested wire format, dropping every field and validating an empty config.
// A minimal valid config in real wire format must pass, and invalid values in
// their real wire positions must be rejected.
func TestConfigCheck_WireFormatContract(t *testing.T) {
	t.Run("minimal valid config passes", func(t *testing.T) {
		payload := `{
			"model":{"provider":"openai","model":"gpt-4"},
			"search":{"provider":"duckduckgo"},
			"agent":{},
			"hid":{"pointer_mode":"absolute"}
		}`
		result := checkWire(t, payload)
		if !result.Valid {
			t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
		}
	})

	// The core regression: these invalid values live in their real wire
	// positions (nested under "agent" / "hid", snake_case). Before the DTO fix
	// every one of these was silently dropped and the config validated as valid.
	invalidCases := []struct {
		name        string
		payload     string
		wantInField string // substring expected in the offending field/message
	}{
		{
			name: "invalid pointer_mode nested under hid",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"hid":{"pointer_mode":"joystick"},"agent":{}}`,
			wantInField: "pointer_mode",
		},
		{
			name: "vad_speech_threshold out of range nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"vad_speech_threshold":1.5}}`,
			wantInField: "vad_speech_threshold",
		},
		{
			name: "max_iterations below -1 nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"max_iterations":-5}}`,
			wantInField: "max_iterations",
		},
		{
			name: "invalid input_mode nested under agent",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"duckduckgo"},
				"agent":{"input_mode":"bogus"}}`,
			wantInField: "input_mode",
		},
		{
			name: "missing model provider",
			payload: `{"model":{"model":"gpt-4"},
				"search":{"provider":"duckduckgo"},"agent":{}}`,
			wantInField: "model.provider",
		},
		{
			name: "invalid search provider",
			payload: `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"google"},"agent":{}}`,
			wantInField: "search.provider",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			result := checkWire(t, tc.payload)
			if result.Valid {
				t.Fatalf("expected valid=false for %s, but config was accepted", tc.name)
			}
			joined := result.Errors[0].Field + " " + result.Errors[0].Message
			if !strings.Contains(joined, tc.wantInField) {
				t.Errorf("expected error to reference %q, got field=%q message=%q",
					tc.wantInField, result.Errors[0].Field, result.Errors[0].Message)
			}
		})
	}
}

// TestConfigCheck_WireSearchHasAPIKey verifies the has_api_key contract: the
// web UI never echoes the stored secret, so a provider that requires a key
// must pass when has_api_key=true and fail when it is false/absent.
func TestConfigCheck_WireSearchHasAPIKey(t *testing.T) {
	for _, provider := range []string{"brave", "tavily"} {
		t.Run(provider+" with has_api_key=true passes", func(t *testing.T) {
			payload := `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"` + provider + `","has_api_key":true},"agent":{}}`
			result := checkWire(t, payload)
			if !result.Valid {
				t.Fatalf("expected valid=true, got errors: %+v", result.Errors)
			}
		})

		t.Run(provider+" with has_api_key=false fails", func(t *testing.T) {
			payload := `{"model":{"provider":"openai","model":"gpt-4"},
				"search":{"provider":"` + provider + `","has_api_key":false},"agent":{}}`
			result := checkWire(t, payload)
			if result.Valid {
				t.Fatalf("expected valid=false when %s key absent, but config was accepted", provider)
			}
		})
	}
}

func TestConfigCheck_WireForceSimpleLoopMapsToAgentConfig(t *testing.T) {
	dto := webConfigDTO{
		Model:  modelDTO{Provider: "openai", Model: "gpt-4"},
		Search: searchDTO{Provider: "duckduckgo"},
		Agent:  agentDTO{ForceSimpleLoop: true},
	}
	cfg := dto.toAgentConfig()
	if !cfg.ForceSimpleLoop {
		t.Fatal("force_simple_loop was not mapped onto agent.Config")
	}
}

// TestConfigCheck_WireTelemetryNested verifies telemetry validation runs
// against the nested "telemetry" wire object.
func TestConfigCheck_WireTelemetryNested(t *testing.T) {
	payload := `{"model":{"provider":"openai","model":"gpt-4"},
		"search":{"provider":"duckduckgo"},
		"telemetry":{"enabled":true},"agent":{}}`
	result := checkWire(t, payload)
	if result.Valid {
		t.Fatal("expected valid=false when telemetry.enabled=true but base_url missing")
	}
	if !strings.Contains(result.Errors[0].Field, "telemetry") {
		t.Errorf("expected telemetry field, got %q", result.Errors[0].Field)
	}
}

// TestConfigCheck_InvalidJSON ensures malformed input is reported as a decode
// error rather than silently validating a zero-value config.
func TestConfigCheck_InvalidJSON(t *testing.T) {
	_, err := checkConfig(strings.NewReader("not json"))
	if err == nil {
		t.Fatal("expected decode error for malformed JSON, got nil")
	}
}
