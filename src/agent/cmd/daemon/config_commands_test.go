package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"aiden-agent/internal/agent"
)

func TestExecuteConfigTestUsesModelRuntime(t *testing.T) {
	values, err := json.Marshal(modelDTO{Provider: "fake"})
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	result := executeConfigTest(context.Background(), agent.Config{
		Model: agent.ModelConfig{Provider: "fake", Responses: []string{"hello"}},
	}, configTestInput{Section: "model", Values: values}, "model")
	if !result.OK || len(result.Results) != 1 || result.Results[0].Check != "provider_request" {
		t.Fatalf("result = %+v", result)
	}
}

func TestModelDTOProviderTestRequestPreservesSamplingPresenceFromJSON(t *testing.T) {
	tests := []struct {
		name                string
		payload             string
		wantTemperature     *float64
		wantReasoningEffort string
	}{
		{
			name:    "omitted fields remain unset",
			payload: `{"provider":"openai","model":"kimi-k3"}`,
		},
		{
			name:    "null temperature remains unset",
			payload: `{"provider":"openai","model":"kimi-k3","temperature":null}`,
		},
		{
			name:                "explicit zero remains present",
			payload:             `{"provider":"openai","model":"kimi-k3","temperature":0,"reasoning_effort":"none"}`,
			wantTemperature:     testFloat64Ptr(0),
			wantReasoningEffort: "none",
		},
		{
			name:                "explicit nonzero values are preserved",
			payload:             `{"provider":"openai","model":"kimi-k3","temperature":0.7,"reasoning_effort":"medium"}`,
			wantTemperature:     testFloat64Ptr(0.7),
			wantReasoningEffort: "medium",
		},
		{
			name:    "explicit empty reasoning remains auto",
			payload: `{"provider":"openai","model":"kimi-k3","reasoning_effort":""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dto modelDTO
			if err := json.Unmarshal([]byte(tt.payload), &dto); err != nil {
				t.Fatalf("unmarshal model values: %v", err)
			}
			req := dto.providerTestRequest()
			if tt.wantTemperature == nil {
				if req.Temperature != nil {
					t.Fatalf("temperature request = %v, want unset", req.Temperature)
				}
			} else if req.Temperature == nil || *req.Temperature != *tt.wantTemperature {
				t.Fatalf("temperature request = %v, want %v", req.Temperature, *tt.wantTemperature)
			}
			if req.ReasoningEffort != tt.wantReasoningEffort {
				t.Fatalf("reasoning request = %q, want %q", req.ReasoningEffort, tt.wantReasoningEffort)
			}
		})
	}
}

func testFloat64Ptr(value float64) *float64 {
	return &value
}

func TestExecuteConfigTestUsesSTTRuntimeWithoutAudio(t *testing.T) {
	values, err := json.Marshal(sttDTO{Provider: "qwen-main", Language: "zh"})
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	result := executeConfigTest(context.Background(), agent.Config{
		STTProviders: map[string]agent.STTProvider{
			"qwen-main": {Type: "qwen-asr", APIKey: "test-key"},
		},
	}, configTestInput{Section: "stt", Values: values}, "stt")
	if !result.OK || len(result.Results) != 1 || result.Results[0].Check != "provider_config" {
		t.Fatalf("result = %+v", result)
	}
}

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
	if dto.HID.KeyboardLayout != agent.DefaultConfig().HID.KeyboardLayout {
		t.Fatalf("HID keyboard_layout = %q, want %q",
			dto.HID.KeyboardLayout, agent.DefaultConfig().HID.KeyboardLayout)
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
keyboard_layout = "azerty"

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
	if dto.Device.DeviceType != "iOS" {
		t.Fatalf("device.device_type = %q, want iOS (default when unset)", dto.Device.DeviceType)
	}
	if dto.HID.PointerMode != "absolute" {
		t.Fatalf("hid.pointer_mode = %q, want absolute (derived from iOS device_type)", dto.HID.PointerMode)
	}
	if dto.HID.KeyboardLayout != "azerty" {
		t.Fatalf("hid.keyboard_layout = %q, want azerty", dto.HID.KeyboardLayout)
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
	if dto.Audio.Backend != agent.AudioBackendAuto {
		t.Fatalf("audio.backend = %q, want auto", dto.Audio.Backend)
	}
	if dto.Log.LLMHTTPRetentionDays != 14 {
		t.Fatalf("log.llm_http_retention_days = %d, want 14", dto.Log.LLMHTTPRetentionDays)
	}
	if !dto.AudioArchive.Enabled || dto.AudioArchive.StoragePath != agent.DefaultConfig().AudioArchive.StoragePath {
		t.Fatalf("audio_archive defaults = %+v, want enabled default storage", dto.AudioArchive)
	}
	if !dto.QuickCapture.Enabled || dto.QuickCapture.GPIOPin != 0 || dto.QuickCapture.ScreenMemoryTTL != agent.DefaultScreenMemoryTTL {
		t.Fatalf("quick_capture defaults = %+v", dto.QuickCapture)
	}
}

func TestWebConfigDTOFromAgentConfig_UsesRuntimeDefaults(t *testing.T) {
	defaults := webConfigDTOFromAgentConfig(agent.Config{})
	if defaults.Search.Provider != "duckduckgo" {
		t.Fatalf("search provider = %q, want duckduckgo", defaults.Search.Provider)
	}
	if defaults.Audio.Socket == "" || defaults.Audio.SampleRate == 0 ||
		defaults.Audio.Channels == 0 || defaults.Audio.BitWidth == 0 ||
		defaults.Audio.Backend == "" {
		t.Fatalf("audio defaults were not populated: %+v", defaults.Audio)
	}
	if defaults.AudioArchive.Enabled || defaults.AudioArchive.StoragePath == "" ||
		defaults.AudioArchive.MaxFiles == 0 || defaults.AudioArchive.MaxSizeMB == 0 {
		t.Fatalf("audio archive zero config conversion = %+v, want disabled with path and retention defaults", defaults.AudioArchive)
	}
	if defaults.Device.DeviceType == "" {
		t.Fatalf("device defaults were not populated: %+v", defaults.Device)
	}
	if defaults.HID.FrameSocket == "" || defaults.HID.KeyboardDevice == "" ||
		defaults.HID.MouseDevice == "" || defaults.HID.AndroidKeyboardDevice == "" ||
		defaults.HID.PointerMode == "" || defaults.HID.KeyboardLayout == "" {
		t.Fatalf("hid defaults were not populated: %+v", defaults.HID)
	}
	if defaults.Log.LLMHTTPRetentionDays != agent.DefaultConfig().Log.LLMHTTPRetentionDaysOrDefault() {
		t.Fatalf("log defaults were not populated: %+v", defaults.Log)
	}
	if defaults.Agent.InputMode != "text" {
		t.Fatalf("agent input mode default = %q, want text", defaults.Agent.InputMode)
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

func TestWebConfigDTOMapsTTSReferenceID(t *testing.T) {
	const referenceID = "fish-reference-id"
	dto := webConfigDTO{TTS: ttsDTO{Provider: "fish-audio", ReferenceID: referenceID}}
	if got := dto.toAgentConfig().TTS.ReferenceID; got != referenceID {
		t.Fatalf("TTS.ReferenceID = %q, want %q", got, referenceID)
	}
	if got := webConfigDTOFromAgentConfig(agent.Config{TTS: agent.TTSConfig{Provider: "fish-audio", ReferenceID: referenceID}}).TTS.ReferenceID; got != referenceID {
		t.Fatalf("round-trip TTS.ReferenceID = %q, want %q", got, referenceID)
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

func TestWebConfigDTOFromAgentConfig_RedactsProviderCredentials(t *testing.T) {
	dto := webConfigDTOFromAgentConfig(agent.Config{
		ModelProviders: map[string]agent.ModelProvider{
			"model-main": {Type: "openai", APIKey: "model-secret"},
		},
		TTSProviders: map[string]agent.TTSProvider{
			"tts-main": {Type: "fish-audio", APIKey: "tts-secret"},
		},
		STTProviders: map[string]agent.STTProvider{
			"stt-main": {
				Type: "tencent-asr", APIKey: "stt-secret",
				SecretID: "secret-id", SecretKey: "secret-key",
			},
		},
	})

	if got := dto.ModelProviders["model-main"]; got.APIKey != "" || !got.HasAPIKey {
		t.Fatalf("model provider credential DTO = %+v, want redacted configured state", got)
	}
	if got := dto.TTSProviders["tts-main"]; got.APIKey != "" || !got.HasAPIKey {
		t.Fatalf("TTS provider credential DTO = %+v, want redacted configured state", got)
	}
	if got := dto.STTProviders["stt-main"]; got.APIKey != "" || !got.HasAPIKey ||
		got.SecretID != "" || !got.HasSecretID || got.SecretKey != "" || !got.HasSecretKey {
		t.Fatalf("STT provider credential DTO = %+v, want redacted configured state", got)
	}

	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal config DTO: %v", err)
	}
	for _, secret := range []string{"model-secret", "tts-secret", "stt-secret", "secret-id", "secret-key"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("provider secret %q leaked in JSON: %s", secret, data)
		}
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

func TestWebConfigDTOMapsAudioBackend(t *testing.T) {
	dto := webConfigDTO{
		Audio: audioDTO{
			Socket:     "/tmp/audio.sock",
			SampleRate: 24000,
			Channels:   1,
			BitWidth:   16,
			Backend:    agent.AudioBackendLocal,
		},
	}
	cfg := dto.toAgentConfig()
	if cfg.Audio.Backend != agent.AudioBackendLocal {
		t.Fatalf("Audio.Backend = %q, want local", cfg.Audio.Backend)
	}
	roundTrip := webConfigDTOFromAgentConfig(agent.Config{Audio: cfg.Audio})
	if roundTrip.Audio.Backend != agent.AudioBackendLocal {
		t.Fatalf("round-trip audio.backend = %q, want local", roundTrip.Audio.Backend)
	}

	autoDTO := webConfigDTO{
		Audio: audioDTO{
			Socket:     "/tmp/audio.sock",
			SampleRate: 24000,
			Channels:   1,
			BitWidth:   16,
			Backend:    agent.AudioBackendAuto,
		},
	}
	autoCfg := autoDTO.toAgentConfig()
	if autoCfg.Audio.Backend != agent.AudioBackendAuto {
		t.Fatalf("auto Audio.Backend = %q, want auto", autoCfg.Audio.Backend)
	}
	autoRoundTrip := webConfigDTOFromAgentConfig(agent.Config{
		Audio: autoCfg.Audio,
		HID:   agent.HIDConfig{InputBackend: "adb"},
	})
	if autoRoundTrip.Audio.Backend != agent.AudioBackendAuto {
		t.Fatalf("auto round-trip audio.backend = %q, want auto", autoRoundTrip.Audio.Backend)
	}
}

func TestWebConfigDTOAcceptsLegacyAudioPlaybackBackend(t *testing.T) {
	dto := webConfigDTO{Audio: audioDTO{LegacyPlaybackBackend: agent.AudioBackendLocal}}
	if got := dto.toAgentConfig().Audio.Backend; got != agent.AudioBackendLocal {
		t.Fatalf("Audio.Backend = %q, want local", got)
	}
	dto.Audio.Backend = agent.AudioBackendAudioService
	if got := dto.toAgentConfig().Audio.Backend; got != agent.AudioBackendAudioService {
		t.Fatalf("Audio.Backend = %q, want audio_service", got)
	}
}

func TestWebConfigDTOMapsQuickCapture(t *testing.T) {
	dto := webConfigDTO{QuickCapture: quickCaptureDTO{
		Enabled:         false,
		GPIOPin:         3,
		ScreenMemoryTTL: "14d",
	}}
	cfg := dto.toAgentConfig()
	if cfg.QuickCapture.EnabledOrDefault() || cfg.QuickCapture.GPIOPin != 3 || cfg.QuickCapture.ScreenMemoryTTL != "14d" {
		t.Fatalf("QuickCapture = %+v, want DTO values", cfg.QuickCapture)
	}

	roundTrip := webConfigDTOFromAgentConfig(agent.Config{QuickCapture: cfg.QuickCapture})
	if roundTrip.QuickCapture != dto.QuickCapture {
		t.Fatalf("round-trip QuickCapture = %+v, want %+v", roundTrip.QuickCapture, dto.QuickCapture)
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

func TestConfigCheck_InvalidDeviceType(t *testing.T) {
	invalidConfig := agent.Config{
		Model: agent.ModelConfig{
			Provider: "openai",
			Model:    "gpt-4",
		},
		Search: agent.SearchConfig{
			Provider: "duckduckgo",
		},
		Device: agent.DeviceConfig{
			DeviceType: "blackberry",
		},
	}

	err := invalidConfig.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid device.device_type, got nil")
	}

	errors := parseValidationErrors(err)
	if len(errors) == 0 {
		t.Fatal("expected at least one validation error")
	}

	errorMsg := errors[0].Message
	if !strings.Contains(errorMsg, "device_type") {
		t.Errorf("expected error message to mention device_type, got: %s", errorMsg)
	}

	t.Logf("Invalid device_type error: %s", errorMsg)
}

func TestConfigCheck_ValidDeviceTypes(t *testing.T) {
	for _, deviceType := range []string{"", "iOS", "Android", "macOS", "windows", "linux", "ios", "ANDROID", "darwin"} {
		validConfig := agent.Config{
			Model: agent.ModelConfig{
				Provider: "openai",
				Model:    "gpt-4",
			},
			Search: agent.SearchConfig{
				Provider: "duckduckgo",
			},
			Device: agent.DeviceConfig{
				DeviceType: deviceType,
			},
		}

		if err := validConfig.Validate(); err != nil {
			t.Errorf("device_type=%q should be valid, got: %v", deviceType, err)
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
			name:          "model api mode error",
			errorMsg:      "invalid model.api_mode: invalid (expected chat_completions or responses)",
			expectedField: "model.api_mode",
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
