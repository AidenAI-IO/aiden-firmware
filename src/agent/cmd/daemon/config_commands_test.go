package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"aiden-agent/internal/agent"
)

func TestConfigCheck_ValidConfig(t *testing.T) {
	validConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
	}

	configJSON, err := json.Marshal(validConfig)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}

	var output bytes.Buffer
	result := ValidationResult{}

	// Manually test validation
	if err := validConfig.Validate(); err != nil {
		t.Errorf("valid config should not have errors: %v", err)
	}

	// Test parseValidationErrors with nil
	errors := parseValidationErrors(nil)
	if len(errors) != 0 {
		t.Errorf("expected 0 errors for nil error, got %d", len(errors))
	}

	// Verify JSON serialization
	encoder := json.NewEncoder(&output)
	result.Valid = true
	result.Errors = []ValidationError{}
	if err := encoder.Encode(result); err != nil {
		t.Fatalf("failed to encode result: %v", err)
	}

	var decoded ValidationResult
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}

	if !decoded.Valid {
		t.Errorf("expected valid=true, got valid=false")
	}

	if len(decoded.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(decoded.Errors))
	}

	t.Logf("Valid config JSON: %s", string(configJSON))
}

func TestConfigCheck_InvalidSearchProvider(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "google", // Invalid provider
		},
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid search provider, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	// Check that the error mentions the search provider
	errorMsg := errors[0].Message
	if !strings.Contains(strings.ToLower(errorMsg), "search") &&
		!strings.Contains(strings.ToLower(errorMsg), "provider") {
		t.Errorf("expected error message to mention search provider, got: %s", errorMsg)
	}

	// Check that field is extracted
	if errors[0].Field != "search.provider" && errors[0].Field != "" {
		t.Logf("field extraction: got %q, expected %q", errors[0].Field, "search.provider")
	}

	t.Logf("Invalid provider error: %s", errorMsg)
}

func TestConfigCheck_InvalidVADThreshold(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		VADSpeechThreshold: 1.5, // Invalid: must be in [0, 1]
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid VAD threshold, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "vad_speech_threshold") {
		t.Errorf("expected error message to mention vad_speech_threshold, got: %s", errorMsg)
	}

	t.Logf("Invalid VAD threshold error: %s", errorMsg)
}

func TestConfigCheck_MissingModelProvider(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			// Provider missing
			Model: "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing model provider, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "model.provider") {
		t.Errorf("expected error message to mention model.provider, got: %s", errorMsg)
	}

	t.Logf("Missing model provider error: %s", errorMsg)
}

func TestConfigCheck_InvalidInputMode(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		InputMode: "invalid_mode", // Invalid: must be text/audio/stt
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid input mode, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "input_mode") {
		t.Errorf("expected error message to mention input_mode, got: %s", errorMsg)
	}

	t.Logf("Invalid input mode error: %s", errorMsg)
}

func TestConfigCheck_NegativeVoiceMaxTurns(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		VoiceMaxTurns: -5, // Invalid: must be >= 0
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative voice_max_turns, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "voice_max_turns") {
		t.Errorf("expected error message to mention voice_max_turns, got: %s", errorMsg)
	}

	t.Logf("Negative voice_max_turns error: %s", errorMsg)
}

func TestParseValidationErrors_ExtractsField(t *testing.T) {
	testCases := []struct {
		name          string
		errorMsg      string
		expectedField string
	}{
		{
			name:          "search provider error",
			errorMsg:      "invalid search.provider: google (expected duckduckgo, brave, or tavily)",
			expectedField: "search.provider",
		},
		{
			name:          "model provider error",
			errorMsg:      "model.provider is required",
			expectedField: "model.provider",
		},
		{
			name:          "vad threshold error",
			errorMsg:      "vad_speech_threshold must be in [0,1] when set, got 1.5",
			expectedField: "vad_speech_threshold",
		},
		{
			name:          "telemetry base_url error",
			errorMsg:      "telemetry.base_url is required when telemetry.enabled=true",
			expectedField: "telemetry.base_url",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a dummy error from the message
			err := &validationError{msg: tc.errorMsg}
			errors := parseValidationErrors(err)
			if len(errors) == 0 {
				t.Fatal("expected at least one error")
			}

			if errors[0].Field != tc.expectedField {
				t.Errorf("expected field %q, got %q", tc.expectedField, errors[0].Field)
			}

			if errors[0].Message != tc.errorMsg {
				t.Errorf("expected message %q, got %q", tc.errorMsg, errors[0].Message)
			}
		})
	}
}

// validationError is a helper for testing
type validationError struct {
	msg string
}

func (e *validationError) Error() string {
	return e.msg
}

func TestValidationResult_JSONFormat(t *testing.T) {
	result := ValidationResult{
		Valid: false,
		Errors: []ValidationError{
			{
				Field:   "search.provider",
				Message: "invalid provider 'google'",
			},
			{
				Field:   "vad_speech_threshold",
				Message: "must be in [0,1]",
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	var decoded ValidationResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if decoded.Valid != result.Valid {
		t.Errorf("expected valid=%v, got valid=%v", result.Valid, decoded.Valid)
	}

	if len(decoded.Errors) != len(result.Errors) {
		t.Fatalf("expected %d errors, got %d", len(result.Errors), len(decoded.Errors))
	}

	for i, err := range decoded.Errors {
		if err.Field != result.Errors[i].Field {
			t.Errorf("error[%d]: expected field=%q, got field=%q", i, result.Errors[i].Field, err.Field)
		}
		if err.Message != result.Errors[i].Message {
			t.Errorf("error[%d]: expected message=%q, got message=%q", i, result.Errors[i].Message, err.Message)
		}
	}

	t.Logf("JSON output: %s", string(data))
}
