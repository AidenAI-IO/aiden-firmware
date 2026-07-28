package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestResolvedWebConfigDTO_MissingFileUsesDefaults(t *testing.T) {
	dto, err := resolvedWebConfigDTO(filepath.Join(t.TempDir(), "agent.toml"))
	if err != nil {
		t.Fatalf("resolvedWebConfigDTO() error = %v", err)
	}
	if dto.HID.FrameSocket != agent.DefaultConfig().HID.FrameSocket {
		t.Fatalf("HID frame_socket = %q, want %q",
			dto.HID.FrameSocket, agent.DefaultConfig().HID.FrameSocket)
	}
	if dto.Log.LLMHTTPRetentionDays != agent.DefaultConfig().Log.LLMHTTPRetentionDaysOrDefault() {
		t.Fatalf("log.llm_http_retention_days = %d, want %d",
			dto.Log.LLMHTTPRetentionDays, agent.DefaultConfig().Log.LLMHTTPRetentionDaysOrDefault())
	}
	if dto.Agent.CustomInstruction != "" {
		t.Fatalf("custom_instruction = %q, want empty wire value for built-in runtime default", dto.Agent.CustomInstruction)
	}

	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal resolved config: %v", err)
	}
	result, err := checkConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("checkConfig(resolved config) decode error: %v", err)
	}
	if !result.Valid {
		t.Fatalf("resolved default config is invalid: %+v", result.Errors)
	}
}

func TestResolvedWebConfigDTO_PreservesCustomInstruction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
custom_instruction = "Use a deployment-specific persona."

[model]
provider = "fake"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dto, err := resolvedWebConfigDTO(path)
	if err != nil {
		t.Fatalf("resolvedWebConfigDTO() error = %v", err)
	}
	if dto.Agent.CustomInstruction != "Use a deployment-specific persona." {
		t.Fatalf("custom_instruction = %q, want custom instruction preserved", dto.Agent.CustomInstruction)
	}
}

func TestResolvedWebConfigDTO_ElidesConfiguredDefaultCustomInstruction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	body := `
custom_instruction = ` + strconv.Quote(agent.DefaultConfig().Instruction) + `

[model]
provider = "fake"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dto, err := resolvedWebConfigDTO(path)
	if err != nil {
		t.Fatalf("resolvedWebConfigDTO() error = %v", err)
	}
	if dto.Agent.CustomInstruction != "" {
		t.Fatalf("custom_instruction = %q, want empty wire value when config matches default", dto.Agent.CustomInstruction)
	}
}

func TestResolvedWebConfigDTO_IgnoresLegacyInstructionField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
instruction = "legacy field should be ignored"

[model]
provider = "fake"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dto, err := resolvedWebConfigDTO(path)
	if err != nil {
		t.Fatalf("resolvedWebConfigDTO() error = %v", err)
	}
	if dto.Agent.CustomInstruction != "" {
		t.Fatalf("custom_instruction = %q, want empty because legacy instruction is ignored", dto.Agent.CustomInstruction)
	}
}

func TestResolvedWebConfigDTO_OverlaysCurrentConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
voice_followup_enabled = true

[model]
provider = "openai"
model = "gpt-4o-mini"

[hid]
pointer_mode = "touchscreen"

[log]
llm_http_retention_days = 14
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dto, err := resolvedWebConfigDTO(path)
	if err != nil {
		t.Fatalf("resolvedWebConfigDTO() error = %v", err)
	}
	if dto.Model.Provider != "openai" || dto.Model.Model != "gpt-4o-mini" {
		t.Fatalf("model overlay = provider %q model %q", dto.Model.Provider, dto.Model.Model)
	}
	if dto.HID.PointerMode != "touchscreen" {
		t.Fatalf("hid.pointer_mode = %q, want touchscreen", dto.HID.PointerMode)
	}
	if !dto.Agent.VoiceFollowupEnabled {
		t.Fatal("agent.voice_followup_enabled = false, want true from current config")
	}
	if dto.HID.KeyboardDevice != agent.DefaultConfig().HID.KeyboardDevice {
		t.Fatalf("hid.keyboard_device = %q, want default %q",
			dto.HID.KeyboardDevice, agent.DefaultConfig().HID.KeyboardDevice)
	}
	if dto.Audio.Socket != agent.DefaultConfig().Audio.Socket {
		t.Fatalf("audio.socket = %q, want default %q",
			dto.Audio.Socket, agent.DefaultConfig().Audio.Socket)
	}
	if dto.Audio.PlaybackBackend != agent.AudioPlaybackBackendAudioService {
		t.Fatalf("audio.playback_backend = %q, want audio_service", dto.Audio.PlaybackBackend)
	}
	if dto.Log.LLMHTTPRetentionDays != 14 {
		t.Fatalf("log.llm_http_retention_days = %d, want 14", dto.Log.LLMHTTPRetentionDays)
	}
	if !dto.AudioArchive.Enabled || dto.AudioArchive.StoragePath != agent.DefaultConfig().AudioArchive.StoragePath {
		t.Fatalf("audio_archive defaults = %+v, want enabled default storage", dto.AudioArchive)
	}
}

func TestWebConfigDTOFromAgentConfig_UsesRuntimeDefaults(t *testing.T) {
	defaults := webConfigDTOFromAgentConfig(agent.Config{})
	if defaults.Search.Provider != "duckduckgo" {
		t.Fatalf("search provider = %q, want duckduckgo", defaults.Search.Provider)
	}
	if defaults.Audio.Socket == "" || defaults.Audio.SampleRate == 0 ||
		defaults.Audio.Channels == 0 || defaults.Audio.BitWidth == 0 ||
		defaults.Audio.PlaybackBackend == "" {
		t.Fatalf("audio defaults were not populated: %+v", defaults.Audio)
	}
	if defaults.AudioArchive.Enabled || defaults.AudioArchive.StoragePath == "" ||
		defaults.AudioArchive.MaxFiles == 0 || defaults.AudioArchive.MaxSizeMB == 0 {
		t.Fatalf("audio archive zero config conversion = %+v, want disabled with path and retention defaults", defaults.AudioArchive)
	}
	if defaults.HID.FrameSocket == "" || defaults.HID.KeyboardDevice == "" ||
		defaults.HID.MouseDevice == "" || defaults.HID.AndroidKeyboardDevice == "" ||
		defaults.HID.PointerMode == "" {
		t.Fatalf("hid defaults were not populated: %+v", defaults.HID)
	}
	if defaults.Log.LLMHTTPRetentionDays != agent.DefaultConfig().Log.LLMHTTPRetentionDaysOrDefault() {
		t.Fatalf("log defaults were not populated: %+v", defaults.Log)
	}
	if defaults.Agent.InputMode != "text" || defaults.Agent.TriggerMode != "manual" {
		t.Fatalf("agent mode defaults = input %q trigger %q, want text/manual",
			defaults.Agent.InputMode, defaults.Agent.TriggerMode)
	}
	if defaults.Agent.VoiceFollowupTimeoutMs == 0 ||
		defaults.Agent.VoiceFirstTurnTimeoutMs == 0 ||
		defaults.Agent.VoiceMaxResponseTokens == 0 {
		t.Fatalf("voice defaults were not populated: %+v", defaults.Agent)
	}
}

func TestWebConfigDTOFromAgentConfigDoesNotInferAudioArchiveEnabled(t *testing.T) {
	roundTrip := webConfigDTOFromAgentConfig(agent.Config{AudioArchive: agent.AudioArchiveConfig{Enabled: false}})
	if roundTrip.AudioArchive.Enabled {
		t.Fatal("AudioArchive.Enabled = true, want explicit disabled zero-value config to stay disabled")
	}
}

func TestWebConfigDTOFromAgentConfig_RedactsSearchAPIKey(t *testing.T) {
	dto := webConfigDTOFromAgentConfig(agent.Config{
		Search: agent.SearchConfig{
			Provider: "brave",
			APIKey:   "search-test-key",
		},
	})
	if dto.Search.APIKey != "" {
		t.Fatalf("search api key was exposed in DTO: %q", dto.Search.APIKey)
	}
	if !dto.Search.HasAPIKey {
		t.Fatal("search has_api_key = false, want true for stored API key")
	}
}

func TestWebConfigDTOMapsAudioArchive(t *testing.T) {
	dto := webConfigDTO{
		AudioArchive: audioArchiveDTO{
			Enabled:     false,
			MaxFiles:    42,
			MaxSizeMB:   17,
			StoragePath: "/tmp/audio-archive",
		},
	}
	cfg := dto.toAgentConfig()
	if cfg.AudioArchive.Enabled {
		t.Fatal("AudioArchive.Enabled = true, want false")
	}
	if cfg.AudioArchive.MaxFiles != 42 || cfg.AudioArchive.MaxSizeMB != 17 || cfg.AudioArchive.StoragePath != "/tmp/audio-archive" {
		t.Fatalf("AudioArchive = %+v, want DTO values", cfg.AudioArchive)
	}

	roundTrip := webConfigDTOFromAgentConfig(agent.Config{AudioArchive: cfg.AudioArchive})
	if roundTrip.AudioArchive.Enabled || roundTrip.AudioArchive.MaxFiles != 42 ||
		roundTrip.AudioArchive.MaxSizeMB != 17 || roundTrip.AudioArchive.StoragePath != "/tmp/audio-archive" {
		t.Fatalf("round-trip AudioArchive = %+v, want DTO values", roundTrip.AudioArchive)
	}
}

func TestWebConfigDTOMapsAudioPlaybackBackend(t *testing.T) {
	dto := webConfigDTO{
		Audio: audioDTO{
			Socket:          "/tmp/audio.sock",
			SampleRate:      24000,
			Channels:        1,
			BitWidth:        16,
			PlaybackBackend: agent.AudioPlaybackBackendLocal,
		},
	}
	cfg := dto.toAgentConfig()
	if cfg.Audio.PlaybackBackend != agent.AudioPlaybackBackendLocal {
		t.Fatalf("Audio.PlaybackBackend = %q, want local", cfg.Audio.PlaybackBackend)
	}
	roundTrip := webConfigDTOFromAgentConfig(agent.Config{Audio: cfg.Audio})
	if roundTrip.Audio.PlaybackBackend != agent.AudioPlaybackBackendLocal {
		t.Fatalf("round-trip audio.playback_backend = %q, want local", roundTrip.Audio.PlaybackBackend)
	}
}

func TestWebConfigDTOMapsVoiceNotifications(t *testing.T) {
	enabled := false
	tailEnabled := false
	voiceNotifications := agent.VoiceNotificationsConfig{
		Enabled:    &enabled,
		MaxPending: 6,
		ResponseTail: agent.VoiceNotificationResponseTailConfig{
			Enabled:      &tailEnabled,
			MaxItems:     1,
			MaxTextChars: 72,
		},
		Expiration: agent.VoiceNotificationExpirationConfig{
			DefaultTTLSeconds: 120,
			CodeTTLSeconds: map[string]int{
				"storage": 900,
			},
		},
	}

	dto := webConfigDTOFromAgentConfig(agent.Config{VoiceNotifications: voiceNotifications})
	cfg := dto.toAgentConfig()
	if !reflect.DeepEqual(cfg.VoiceNotifications, voiceNotifications) {
		t.Fatalf("VoiceNotifications = %#v, want %#v", cfg.VoiceNotifications, voiceNotifications)
	}
}

func TestWebConfigDTOMapsLog(t *testing.T) {
	dto := webConfigDTO{
		Model: modelDTO{Provider: "fake"},
		Log:   logDTO{LLMHTTPRetentionDays: 21},
	}

	cfg := dto.toAgentConfig()
	if cfg.Log.LLMHTTPRetentionDays != 21 {
		t.Fatalf("Log.LLMHTTPRetentionDays = %d, want 21", cfg.Log.LLMHTTPRetentionDays)
	}

	roundTrip := webConfigDTOFromAgentConfig(agent.Config{Log: agent.LogConfig{LLMHTTPRetentionDays: 21}})
	if roundTrip.Log.LLMHTTPRetentionDays != 21 {
		t.Fatalf("round-trip log.llm_http_retention_days = %d, want 21", roundTrip.Log.LLMHTTPRetentionDays)
	}
}

func TestWebConfigDTOMapsTerminationPolicy(t *testing.T) {
	enabled := false
	policy := agent.TerminationPolicyConfig{
		Enabled:                 &enabled,
		MaxSeconds:              12.5,
		RepeatActionLimit:       7,
		SameResultLimit:         8,
		ScreenUnchangedLimit:    9,
		SoftNoticeStallScore:    10,
		RestrictToolsStallScore: 11,
		TerminateStallScore:     12,
		ParseFailureLimit:       13,
	}
	dto := webConfigDTO{TerminationPolicy: policy}
	if got := dto.toAgentConfig().TerminationPolicy; !reflect.DeepEqual(got, policy) {
		t.Fatalf("TerminationPolicy = %#v, want %#v", got, policy)
	}
	if got := webConfigDTOFromAgentConfig(agent.Config{TerminationPolicy: policy}).TerminationPolicy; !reflect.DeepEqual(got, policy) {
		t.Fatalf("round-trip TerminationPolicy = %#v, want %#v", got, policy)
	}
}

func TestWebConfigDTOMapsSTTLanguage(t *testing.T) {
	dto := webConfigDTO{
		STT: sttDTO{
			Provider: "openai-whisper",
			Language: "en",
			AppID:    "12345",
		},
	}

	cfg := dto.toAgentConfig()
	if cfg.STT.Language != "en" {
		t.Fatalf("STT.Language = %q, want en", cfg.STT.Language)
	}
	if cfg.STT.AppID != "12345" {
		t.Fatalf("STT.AppID = %q, want 12345", cfg.STT.AppID)
	}

	roundTrip := webConfigDTOFromAgentConfig(agent.Config{
		STT: agent.STTConfig{
			Provider: "openai-whisper",
			Language: "zh",
			AppID:    "67890",
		},
	})
	if roundTrip.STT.Language != "zh" {
		t.Fatalf("round-trip stt.language = %q, want zh", roundTrip.STT.Language)
	}
	if roundTrip.STT.AppID != "67890" {
		t.Fatalf("round-trip stt.app_id = %q, want 67890", roundTrip.STT.AppID)
	}
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

func TestConfigCheck_InvalidMaxIterations(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		MaxIterations: -2, // Invalid: must be >= -1 (-1 means unlimited)
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for max_iterations < -1, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "max_iterations") {
		t.Errorf("expected error message to mention max_iterations, got: %s", errorMsg)
	}

	t.Logf("Invalid max_iterations error: %s", errorMsg)
}

func TestConfigCheck_UnlimitedMaxIterations(t *testing.T) {
	// -1 is the sentinel for "unlimited" and must be accepted.
	validConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		MaxIterations: -1,
	}

	if err := validConfig.Validate(); err != nil {
		t.Errorf("max_iterations=-1 (unlimited) should be valid, got: %v", err)
	}
}

func TestConfigCheck_InvalidPointerMode(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		HID: agent.HIDConfig{
			PointerMode: "joystick", // Invalid: must be absolute or touchscreen
		},
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid hid.pointer_mode, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "pointer_mode") {
		t.Errorf("expected error message to mention pointer_mode, got: %s", errorMsg)
	}

	t.Logf("Invalid pointer_mode error: %s", errorMsg)
}

func TestConfigCheck_ValidPointerModes(t *testing.T) {
	for _, mode := range []string{"", "absolute", "touchscreen", "Absolute", "TOUCHSCREEN"} {
		validConfig := agent.Config{
			Model: agent.ModelConfig{
				Provider: "openai",
				Model:    "gpt-4",
			},
			Search: agent.SearchConfig{
				Provider: "duckduckgo",
			},
			HID: agent.HIDConfig{
				PointerMode: mode,
			},
		}

		if err := validConfig.Validate(); err != nil {
			t.Errorf("pointer_mode=%q should be valid, got: %v", mode, err)
		}
	}
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
			name:          "model max response tokens error",
			errorMsg:      "model.max_response_tokens must be >= 0, got -1",
			expectedField: "model.max_response_tokens",
		},
		{
			name:          "model context window error",
			errorMsg:      "model.context_window must be >= 0, got -1",
			expectedField: "model.context_window",
		},
		{
			name:          "model max output tokens error",
			errorMsg:      "model.model_max_output_tokens must be >= 0, got -1",
			expectedField: "model.model_max_output_tokens",
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
