package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestConfigValidateAcceptsSTTWakeup(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax-cn"},
		STT:         STTConfig{Provider: "openai-whisper"},
		InputMode:   " stt ",
		TriggerMode: " wakeup ",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.InputModeOrDefault(); got != "stt" {
		t.Fatalf("InputModeOrDefault() = %q, want stt", got)
	}
	if got := cfg.TriggerModeOrDefault(); got != "wakeup" {
		t.Fatalf("TriggerModeOrDefault() = %q, want wakeup", got)
	}
}

func TestConfigValidateRejectsRemovedAudioMode(t *testing.T) {
	cfg := Config{
		Model:     ModelConfig{Provider: "fake"},
		TTS:       TTSConfig{Provider: "minimax-cn"},
		InputMode: "audio",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid input_mode error for removed audio mode")
	}
	if !strings.Contains(err.Error(), "audio mode has been removed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigInputModeDefaultContract(t *testing.T) {
	const want = "text"

	if got := DefaultConfig().InputMode; got != want {
		t.Fatalf("DefaultConfig().InputMode = %q, want %q", got, want)
	}
	if got := (Config{}).InputModeOrDefault(); got != want {
		t.Fatalf("Config{}.InputModeOrDefault() = %q, want %q", got, want)
	}

	var metaDefault any
	for _, section := range ConfigMeta().Sections {
		if section.Name != "agent" {
			continue
		}
		for _, field := range section.Fields {
			if field.Key == "input_mode" {
				metaDefault = field.Default
			}
		}
	}
	if metaDefault != want {
		t.Fatalf("ConfigMeta agent.input_mode default = %#v, want %q", metaDefault, want)
	}
}

func TestLoadRuntimeConfigParsesTerminationPolicyOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	contents := `
[model]
provider = "fake"

[termination_policy]
enabled = false
max_seconds = 12.5
repeat_action_limit = 7
same_result_limit = 8
screen_unchanged_limit = 9
soft_notice_stall_score = 10
restrict_tools_stall_score = 11
terminate_stall_score = 12
parse_failure_limit = 13
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	policy := cfg.TerminationPolicy
	if policy.Enabled == nil || *policy.Enabled {
		t.Fatalf("termination policy enabled = %#v, want false", policy.Enabled)
	}
	if policy.MaxSeconds != 12.5 || policy.RepeatActionLimit != 7 || policy.SameResultLimit != 8 ||
		policy.ScreenUnchangedLimit != 9 || policy.SoftNoticeStallScore != 10 ||
		policy.RestrictToolsStallScore != 11 || policy.TerminateStallScore != 12 ||
		policy.ParseFailureLimit != 13 {
		t.Fatalf("termination policy overrides = %#v", policy)
	}
}

func TestLoadRuntimeConfigParsesStorageOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	contents := `
[model]
provider = "fake"

[storage]
enabled = true
root_path = "/mnt/device"
check_interval_seconds = 42
warning_threshold_mb = 80
critical_threshold_mb = 20
emergency_threshold_mb = 6
recovery_hysteresis_mb = 9

[storage.degraded_mode]
disable_llm_http_log = false
disable_audio_archive = false
disable_session_archive = false
max_agent_log_mb = 3

[storage.cleanup]
enabled = false
llm_http_log_retention_days = [9, 2]
audio_archive_retention_days = [14]
session_archive_retention_days = [45]
cleanup_retry_interval_seconds = 75
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	storage := cfg.Storage
	if storage.RootPath != "/mnt/device" || storage.CheckIntervalSeconds != 42 || storage.WarningThresholdMB != 80 ||
		storage.CriticalThresholdMB != 20 || storage.EmergencyThresholdMB != 6 || storage.RecoveryHysteresisMB != 9 {
		t.Fatalf("storage overrides = %+v", storage)
	}
	if storage.DegradedMode.DisableLLMHTTPLog || storage.DegradedMode.DisableAudioArchive || storage.DegradedMode.DisableSessionArchive || storage.DegradedMode.MaxAgentLogMB != 3 {
		t.Fatalf("storage degraded mode overrides = %+v", storage.DegradedMode)
	}
	if storage.Cleanup.Enabled || !reflect.DeepEqual(storage.Cleanup.LLMHTTPLogRetentionDays, []int{9, 2}) ||
		!reflect.DeepEqual(storage.Cleanup.AudioArchiveRetentionDays, []int{14}) ||
		!reflect.DeepEqual(storage.Cleanup.SessionArchiveRetentionDays, []int{45}) || storage.Cleanup.CleanupRetryIntervalSeconds != 75 {
		t.Fatalf("storage cleanup overrides = %+v", storage.Cleanup)
	}
}

func TestConfigRejectsInvalidTerminationPolicyThresholdOrder(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{Provider: "fake"},
		TerminationPolicy: TerminationPolicyConfig{
			SoftNoticeStallScore:    5,
			RestrictToolsStallScore: 4,
			TerminateStallScore:     6,
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "soft_notice < restrict_tools < terminate") {
		t.Fatalf("Validate() error = %v, want ordered termination-policy threshold error", err)
	}
}

func TestBundledSkillsDirCandidatesUseOEMOnly(t *testing.T) {
	want := []string{"/oem/usr/share/aiden/skills"}
	if got := bundledSkillsDirCandidates(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bundledSkillsDirCandidates() = %#v, want %#v", got, want)
	}
}

func TestConfigScreenshotPruningDefaultsAndOverrides(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "fake"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	pruning := cfg.ScreenshotPruningOrDefault()
	if pruning.KeepN != 3 || pruning.Interval != 2 {
		t.Fatalf("default screenshot pruning = %#v, want keep_n=3 interval=2", pruning)
	}

	cfg.ScreenshotKeepN = 5
	cfg.ScreenshotPruneInterval = 40
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with screenshot pruning overrides error = %v", err)
	}
	pruning = cfg.ScreenshotPruningOrDefault()
	if pruning.KeepN != 5 || pruning.Interval != 40 {
		t.Fatalf("configured screenshot pruning = %#v, want keep_n=5 interval=40", pruning)
	}
}

func TestConfigScreenStableDefaults(t *testing.T) {
	cfg := Config{
		ScreenStableTimeoutMs:     7000,
		ScreenStableMs:            800,
		ScreenStableDiffThreshold: 2.5,
	}
	defaults := cfg.ScreenStableDefaults().Resolved()
	if defaults.TimeoutMs != 7000 || defaults.StableMs != 800 || defaults.DiffThreshold != 2.5 {
		t.Fatalf("resolved defaults = %#v, want timeout=7000 stable=800 diff=2.5", defaults)
	}
}

func TestConfigTodoReminderToolCallsDefaultsAndOverrides(t *testing.T) {
	cfg := Config{}
	if got := cfg.TodoReminderToolCallsOrDefault(); got != 3 {
		t.Fatalf("TodoReminderToolCallsOrDefault() = %d, want 3", got)
	}

	cfg.TodoReminderToolCalls = 2
	if got := cfg.TodoReminderToolCallsOrDefault(); got != 2 {
		t.Fatalf("TodoReminderToolCallsOrDefault() override = %d, want 2", got)
	}

	cfg.TodoReminderToolCalls = 0
	if got := cfg.TodoReminderToolCallsOrDefault(); got != 3 {
		t.Fatalf("TodoReminderToolCallsOrDefault() zero = %d, want 3", got)
	}
}

func TestLoadConfigParsesModelSpecOverrides(t *testing.T) {
	configDir := t.TempDir()
	config := `
custom_instruction = "test"

[model]
provider = "openrouter"
model = "vendor/test-model"
max_response_tokens = 1000
context_window = 64000
model_max_output_tokens = 4096
`
	if err := os.WriteFile(configDir+"/agent.toml", []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigFromDir(configDir)
	if err != nil {
		t.Fatalf("LoadConfigFromDir() error = %v", err)
	}
	if cfg.Model.ContextWindow != 64_000 {
		t.Errorf("ContextWindow = %d, want 64_000", cfg.Model.ContextWindow)
	}
	if cfg.Model.MaxResponseTokens != 1_000 {
		t.Errorf("MaxResponseTokens = %d, want 1_000", cfg.Model.MaxResponseTokens)
	}
	if cfg.Model.ModelMaxOutputTokens != 4_096 {
		t.Errorf("ModelMaxOutputTokens = %d, want 4_096", cfg.Model.ModelMaxOutputTokens)
	}
}

func TestLoadConfigParsesLegacyModelMaxTokens(t *testing.T) {
	configDir := t.TempDir()
	config := `
custom_instruction = "test"

[model]
provider = "openrouter"
model = "vendor/test-model"
max_tokens = 777
`
	if err := os.WriteFile(configDir+"/agent.toml", []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigFromDir(configDir)
	if err != nil {
		t.Fatalf("LoadConfigFromDir() error = %v", err)
	}
	if cfg.Model.MaxResponseTokens != 777 {
		t.Errorf("MaxResponseTokens = %d, want legacy max_tokens value 777", cfg.Model.MaxResponseTokens)
	}
}

func TestLoadConfigPrefersMaxResponseTokensOverLegacyMaxTokens(t *testing.T) {
	configDir := t.TempDir()
	config := `
custom_instruction = "test"

[model]
provider = "openrouter"
model = "vendor/test-model"
max_response_tokens = 1000
max_tokens = 777
`
	if err := os.WriteFile(configDir+"/agent.toml", []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigFromDir(configDir)
	if err != nil {
		t.Fatalf("LoadConfigFromDir() error = %v", err)
	}
	if cfg.Model.MaxResponseTokens != 1_000 {
		t.Errorf("MaxResponseTokens = %d, want canonical max_response_tokens value 1_000", cfg.Model.MaxResponseTokens)
	}
}

func TestLoadRuntimeConfigFromDirAppliesRuntimeDefaultsWithoutActivatingSpeech(t *testing.T) {
	configDir := t.TempDir()
	config := `
[model]
provider = "fake"
`
	if err := os.WriteFile(filepath.Join(configDir, "agent.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfigFromDir(configDir)
	if err != nil {
		t.Fatalf("LoadRuntimeConfigFromDir() error = %v", err)
	}

	if cfg.InputMode != defaultInputMode {
		t.Fatalf("InputMode = %q, want runtime default %q", cfg.InputMode, defaultInputMode)
	}
	if cfg.Audio.Socket != defaultAudioSocket {
		t.Fatalf("Audio.Socket = %q, want runtime default %q", cfg.Audio.Socket, defaultAudioSocket)
	}
	if cfg.HID.FrameSocket != defaultFrameServiceSocket {
		t.Fatalf("HID.FrameSocket = %q, want runtime default %q", cfg.HID.FrameSocket, defaultFrameServiceSocket)
	}
	if cfg.VoiceMaxResponseTokens != defaultVoiceMaxResponseTokens {
		t.Fatalf("VoiceMaxResponseTokens = %d, want runtime default %d",
			cfg.VoiceMaxResponseTokens, defaultVoiceMaxResponseTokens)
	}
	if !cfg.Model.LogRawHTTP {
		t.Fatal("Model.LogRawHTTP = false, want runtime default true")
	}
	if cfg.Log.LLMHTTPRetentionDays != defaultLLMHTTPLogRetentionDays {
		t.Fatalf("Log.LLMHTTPRetentionDays = %d, want runtime default %d",
			cfg.Log.LLMHTTPRetentionDays, defaultLLMHTTPLogRetentionDays)
	}
	if cfg.ScreenStableTimeoutMs != defaultStableWaitTimeoutMs {
		t.Fatalf("ScreenStableTimeoutMs = %d, want runtime default %d",
			cfg.ScreenStableTimeoutMs, defaultStableWaitTimeoutMs)
	}
	if cfg.TTS.Provider != "" {
		t.Fatalf("TTS.Provider = %q, want empty when text mode did not configure TTS", cfg.TTS.Provider)
	}
	if cfg.STT.Provider != "" {
		t.Fatalf("STT.Provider = %q, want empty when text mode did not configure STT", cfg.STT.Provider)
	}
}

func TestLoadRuntimeConfigCanDisableRawHTTPLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
[model]
provider = "fake"
log_raw_http = false
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if cfg.Model.LogRawHTTP {
		t.Fatal("Model.LogRawHTTP = true, want explicit false to disable raw HTTP logging")
	}
}

func TestLoadRuntimeConfigCanOverrideLLMHTTPRetentionDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
[model]
provider = "fake"

[log]
llm_http_retention_days = 14
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if cfg.Log.LLMHTTPRetentionDaysOrDefault() != 14 {
		t.Fatalf("LLMHTTPRetentionDaysOrDefault() = %d, want 14", cfg.Log.LLMHTTPRetentionDaysOrDefault())
	}
}

func TestConfigValidateRejectsNegativeLLMHTTPRetentionDays(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{Provider: "fake"},
		Log:   LogConfig{LLMHTTPRetentionDays: -1},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want log.llm_http_retention_days rejection")
	}
	if !strings.Contains(err.Error(), "log.llm_http_retention_days") {
		t.Fatalf("Validate() error = %v, want log.llm_http_retention_days", err)
	}
}

func TestLoadRuntimeConfigIgnoresLegacyInstructionField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
instruction = "legacy field should be ignored"

[model]
provider = "fake"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if cfg.Instruction != defaultInstruction {
		t.Fatalf("Instruction = %q, want built-in default because legacy instruction is ignored", cfg.Instruction)
	}
}

func TestLoadRuntimeConfigEmptyCustomInstructionUsesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
custom_instruction = ""

[model]
provider = "fake"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if cfg.Instruction != defaultInstruction {
		t.Fatalf("Instruction = %q, want built-in default for empty custom_instruction", cfg.Instruction)
	}
}

func TestLoadRuntimeConfigKeepsSpeechProvidersOptIn(t *testing.T) {
	dir := t.TempDir()

	unconfiguredPath := filepath.Join(dir, "unconfigured.toml")
	if err := os.WriteFile(unconfiguredPath, []byte(`
[model]
provider = "fake"

[stt]

[tts]
`), 0o644); err != nil {
		t.Fatalf("write unconfigured config: %v", err)
	}
	unconfigured, err := LoadRuntimeConfig(unconfiguredPath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig(unconfigured) error = %v", err)
	}
	if unconfigured.STT.Provider != "" {
		t.Fatalf("unconfigured STT.Provider = %q, want empty", unconfigured.STT.Provider)
	}
	if unconfigured.TTS.Provider != "" {
		t.Fatalf("unconfigured TTS.Provider = %q, want empty", unconfigured.TTS.Provider)
	}

	keyOnlyPath := filepath.Join(dir, "key-only.toml")
	if err := os.WriteFile(keyOnlyPath, []byte(`
[model]
provider = "fake"

[stt]
api_key = "stt-key"

[tts]
api_key = "tts-key"
`), 0o644); err != nil {
		t.Fatalf("write key-only config: %v", err)
	}
	keyOnly, err := LoadRuntimeConfig(keyOnlyPath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig(key-only) error = %v", err)
	}
	if keyOnly.STT.Provider != "" {
		t.Fatalf("key-only STT.Provider = %q, want empty", keyOnly.STT.Provider)
	}
	if keyOnly.TTS.Provider != "" {
		t.Fatalf("key-only TTS.Provider = %q, want empty", keyOnly.TTS.Provider)
	}

	explicitPath := filepath.Join(dir, "explicit.toml")
	if err := os.WriteFile(explicitPath, []byte(`
[model]
provider = "fake"

[stt]
provider = "openai-whisper"
api_key = "stt-key"

[tts]
provider = "minimax-ws"
api_key = "tts-key"
`), 0o644); err != nil {
		t.Fatalf("write explicit config: %v", err)
	}
	explicit, err := LoadRuntimeConfig(explicitPath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig(explicit) error = %v", err)
	}
	if explicit.STT.Provider != defaultSTTProvider {
		t.Fatalf("explicit STT.Provider = %q, want default %q", explicit.STT.Provider, defaultSTTProvider)
	}
	if explicit.STT.Model != defaultSTTModel {
		t.Fatalf("explicit STT.Model = %q, want default %q", explicit.STT.Model, defaultSTTModel)
	}
	if explicit.TTS.Provider != defaultTTSProvider {
		t.Fatalf("explicit TTS.Provider = %q, want default %q", explicit.TTS.Provider, defaultTTSProvider)
	}
	if explicit.TTS.VoiceID != defaultTTSVoiceID {
		t.Fatalf("explicit TTS.VoiceID = %q, want default %q", explicit.TTS.VoiceID, defaultTTSVoiceID)
	}
}

func TestLoadRuntimeConfigDoesNotCarryDefaultSpeechFieldsAcrossProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(`
[model]
provider = "fake"

[stt]
provider = "openrouter"
api_key = "stt-key"

[tts]
provider = "volcengine"
api_key = "tts-key"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadRuntimeConfig(path)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if cfg.STT.Model != "" {
		t.Fatalf("STT.Model = %q, want empty so provider-specific defaults can apply", cfg.STT.Model)
	}
	if cfg.TTS.VoiceID != "" {
		t.Fatalf("TTS.VoiceID = %q, want empty so provider-specific defaults can apply", cfg.TTS.VoiceID)
	}
	if cfg.TTS.Emotion != "" {
		t.Fatalf("TTS.Emotion = %q, want empty so provider-specific defaults can apply", cfg.TTS.Emotion)
	}
	if cfg.TTS.Speed != 0 {
		t.Fatalf("TTS.Speed = %v, want zero so provider-specific defaults can apply", cfg.TTS.Speed)
	}
}

func TestLoadRuntimeConfigRejectsVoiceModeWithoutExplicitSpeechProviders(t *testing.T) {
	dir := t.TempDir()

	sttPath := filepath.Join(dir, "stt.toml")
	if err := os.WriteFile(sttPath, []byte(`
input_mode = "stt"

[model]
provider = "fake"
`), 0o644); err != nil {
		t.Fatalf("write stt config: %v", err)
	}
	_, err := LoadRuntimeConfig(sttPath)
	if err == nil || !strings.Contains(err.Error(), "stt.provider") {
		t.Fatalf("LoadRuntimeConfig(stt) error = %v, want stt.provider validation error", err)
	}

	audioPath := filepath.Join(dir, "audio.toml")
	if err := os.WriteFile(audioPath, []byte(`
input_mode = "audio"

[model]
provider = "fake"
`), 0o644); err != nil {
		t.Fatalf("write audio config: %v", err)
	}
	_, err = LoadRuntimeConfig(audioPath)
	if err == nil || !strings.Contains(err.Error(), "audio mode has been removed") {
		t.Fatalf("LoadRuntimeConfig(audio) error = %v, want removed audio mode error", err)
	}
}

func TestConfigValidateRejectsNegativeModelSpecOverrides(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "fake", MaxResponseTokens: -1}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "model.max_response_tokens") {
		t.Fatalf("expected model.max_response_tokens validation error, got %v", err)
	}

	cfg = Config{Model: ModelConfig{Provider: "fake", ContextWindow: -1}}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "model.context_window") {
		t.Fatalf("expected model.context_window validation error, got %v", err)
	}

	cfg = Config{Model: ModelConfig{Provider: "fake", ModelMaxOutputTokens: -1}}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "model.model_max_output_tokens") {
		t.Fatalf("expected model.model_max_output_tokens validation error, got %v", err)
	}

	cfg = Config{
		Model:     ModelConfig{Provider: "fake"},
		ModelText: ModelConfig{ContextWindow: -1},
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "model_text.context_window") {
		t.Fatalf("expected model_text.context_window validation error, got %v", err)
	}

	cfg = Config{
		Model:     ModelConfig{Provider: "fake"},
		ModelText: ModelConfig{ModelMaxOutputTokens: -1},
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "model_text.model_max_output_tokens") {
		t.Fatalf("expected model_text.model_max_output_tokens validation error, got %v", err)
	}

	cfg = Config{
		Model:     ModelConfig{Provider: "fake"},
		ModelText: ModelConfig{MaxResponseTokens: -1},
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "model_text.max_response_tokens") {
		t.Fatalf("expected model_text.max_response_tokens validation error, got %v", err)
	}
}

func TestConfigValidateRejectsNegativeScreenStableSettings(t *testing.T) {
	cfg := Config{
		Model:                 ModelConfig{Provider: "fake"},
		ScreenStableTimeoutMs: -1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "screen_stable_timeout_ms") {
		t.Fatalf("expected screen_stable_timeout_ms validation error, got %v", err)
	}

	cfg = Config{
		Model:          ModelConfig{Provider: "fake"},
		ScreenStableMs: -1,
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "screen_stable_ms") {
		t.Fatalf("expected screen_stable_ms validation error, got %v", err)
	}

	cfg = Config{
		Model:                     ModelConfig{Provider: "fake"},
		ScreenStableDiffThreshold: -0.1,
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "screen_stable_diff_threshold") {
		t.Fatalf("expected screen_stable_diff_threshold validation error, got %v", err)
	}
}

func TestSearchProviderDefaultsAndAliases(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{provider: "", want: searchProviderDuckDuckGo},
		{provider: " DuckDuckGo ", want: searchProviderDuckDuckGo},
		{provider: " Brave Search ", want: searchProviderBrave},
		{provider: "brave_search", want: searchProviderBrave},
		{provider: "brave-search", want: searchProviderBrave},
		{provider: "brave-free", want: searchProviderBrave},
		{provider: " tavily ", want: searchProviderTavily},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := (SearchConfig{Provider: tt.provider}).ProviderOrDefault()
			if got != tt.want {
				t.Fatalf("ProviderOrDefault() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigValidateAcceptsBraveSearchWithAPIKey(t *testing.T) {
	t.Setenv(braveSearchAPIKeyEnv, "")

	cfg := Config{
		Model:  ModelConfig{Provider: "fake"},
		Search: SearchConfig{Provider: "brave", APIKey: "BSA-token"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidateAcceptsBraveSearchWithEnvAPIKey(t *testing.T) {
	t.Setenv(braveSearchAPIKeyEnv, "BSA-env-token")

	cfg := Config{
		Model:  ModelConfig{Provider: "fake"},
		Search: SearchConfig{Provider: "brave"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidateRejectsBraveSearchWithoutAPIKey(t *testing.T) {
	t.Setenv(braveSearchAPIKeyEnv, "")

	cfg := Config{
		Model:  ModelConfig{Provider: "fake"},
		Search: SearchConfig{Provider: "brave"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected missing Brave Search API key error")
	}
	if !strings.Contains(err.Error(), braveSearchAPIKeyEnv) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidateRejectsNegativeScreenshotPruning(t *testing.T) {
	cfg := Config{
		Model:           ModelConfig{Provider: "fake"},
		ScreenshotKeepN: -1,
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "screenshot_keep_n") {
		t.Fatalf("expected screenshot_keep_n validation error, got %v", err)
	}

	cfg = Config{
		Model:                   ModelConfig{Provider: "fake"},
		ScreenshotPruneInterval: -1,
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "screenshot_prune_interval") {
		t.Fatalf("expected screenshot_prune_interval validation error, got %v", err)
	}
}

func TestProxyConfigFromEnvironment(t *testing.T) {
	t.Setenv("http_proxy", "http://proxy.example:18080")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("NO_PROXY", "")

	proxy := ProxyConfigFromEnvironment()
	if proxy.HTTPProxy != "http://proxy.example:18080" {
		t.Fatalf("HTTPProxy = %q", proxy.HTTPProxy)
	}
	if proxy.NoProxy != DefaultNoProxy {
		t.Fatalf("NoProxy = %q, want default", proxy.NoProxy)
	}
}

func TestProxyConfigFromEnvironmentPrefersUppercase(t *testing.T) {
	t.Setenv("http_proxy", "http://lower.example:18080")
	t.Setenv("HTTP_PROXY", "http://upper.example:18080")
	t.Setenv("https_proxy", "http://lower.example:18081")
	t.Setenv("HTTPS_PROXY", "http://upper.example:18081")
	t.Setenv("all_proxy", "http://lower.example:18082")
	t.Setenv("ALL_PROXY", "http://upper.example:18082")
	t.Setenv("no_proxy", "lower.example")
	t.Setenv("NO_PROXY", "upper.example")

	proxy := ProxyConfigFromEnvironment()
	if proxy.HTTPProxy != "http://upper.example:18080" {
		t.Fatalf("HTTPProxy = %q, want uppercase value", proxy.HTTPProxy)
	}
	if proxy.HTTPSProxy != "http://upper.example:18081" {
		t.Fatalf("HTTPSProxy = %q, want uppercase value", proxy.HTTPSProxy)
	}
	if proxy.AllProxy != "http://upper.example:18082" {
		t.Fatalf("AllProxy = %q, want uppercase value", proxy.AllProxy)
	}
	if proxy.NoProxy != "upper.example" {
		t.Fatalf("NoProxy = %q, want uppercase value", proxy.NoProxy)
	}
}

func TestProxyConfigFromEnvironmentPreservesRawWhitespace(t *testing.T) {
	t.Setenv("HTTP_PROXY", " http://proxy.example:18080")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", " example.com ")
	t.Setenv("no_proxy", "")

	proxy := ProxyConfigFromEnvironment()
	if proxy.HTTPProxy != " http://proxy.example:18080" {
		t.Fatalf("HTTPProxy = %q, want raw env value", proxy.HTTPProxy)
	}
	if proxy.NoProxy != " example.com " {
		t.Fatalf("NoProxy = %q, want raw env value", proxy.NoProxy)
	}
}

func TestConfigValidateRejectsInvalidTriggerMode(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax-cn"},
		STT:         STTConfig{Provider: "openai-whisper"},
		InputMode:   "stt",
		TriggerMode: "gpio",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid trigger_mode error")
	}
	if !strings.Contains(err.Error(), "invalid trigger_mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigVADBackendDefaultsAndValidation(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "fake"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.VADBackendOrDefault(); got != "rknn" {
		t.Fatalf("VADBackendOrDefault() = %q, want rknn", got)
	}

	cfg.VADBackend = " cpu "
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with cpu backend error = %v", err)
	}
	if got := cfg.VADBackendOrDefault(); got != "cpu" {
		t.Fatalf("VADBackendOrDefault() = %q, want cpu", got)
	}
	if got := DefaultVADHelperPathForBackend("cpu"); got != "/oem/usr/bin/cpu_vad" {
		t.Fatalf("DefaultVADHelperPathForBackend(cpu) = %q", got)
	}
	if got := ResolveVADHelperPath("cpu", DefaultVADHelperPath()); got != "/oem/usr/bin/cpu_vad" {
		t.Fatalf("ResolveVADHelperPath(cpu, rknn default) = %q", got)
	}
	if got := ResolveVADHelperPath("cpu", "/custom/vad"); got != "/custom/vad" {
		t.Fatalf("ResolveVADHelperPath(cpu, custom) = %q", got)
	}

	cfg.VADBackend = "npu"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid vad_backend error")
	}
	if !strings.Contains(err.Error(), "invalid vad_backend") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeviceConfigBackendDefaultsToHDMI(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "fake"}}
	if got := cfg.Device.BackendOrDefault(); got != "hdmi" {
		t.Fatalf("BackendOrDefault() = %q, want hdmi", got)
	}
}

func TestLoadConfigRejectsUnknownDeviceBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	content := `
custom_instruction = "test"

[model]
provider = "fake"

[device]
backend = "bogus"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "invalid device.backend") {
		t.Fatalf("LoadConfig() error = %v, want invalid device.backend", err)
	}
}

func TestConfigValidateRejectsInvalidVADSpeechThreshold(t *testing.T) {
	for _, threshold := range []float64{-0.1, 1.1} {
		cfg := Config{
			Model:              ModelConfig{Provider: "fake"},
			VADSpeechThreshold: threshold,
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("Validate() error = nil for threshold %v, want error", threshold)
		}
		if !strings.Contains(err.Error(), "vad_speech_threshold") {
			t.Fatalf("Validate() error = %v, want vad_speech_threshold", err)
		}
	}
}

func TestConfigValidateRejectsWakeupForTextInput(t *testing.T) {
	tests := []struct {
		name      string
		inputMode string
	}{
		{name: "default text", inputMode: ""},
		{name: "explicit text", inputMode: " text "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Model:       ModelConfig{Provider: "fake"},
				InputMode:   tt.inputMode,
				TriggerMode: " wakeup ",
			}

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected incompatible trigger_mode/input_mode error")
			}
			if !strings.Contains(err.Error(), "incompatible trigger_mode") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigValidateRequiresTTSForSTTWakeup(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		STT:         STTConfig{Provider: "openai-whisper"},
		TTS:         TTSConfig{Provider: "   "},
		InputMode:   "stt",
		TriggerMode: "wakeup",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected missing TTS provider error")
	}
	if !strings.Contains(err.Error(), "tts.provider is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidateRejectsUnsupportedAudioFormatForVoiceInput(t *testing.T) {
	tests := []struct {
		name    string
		audio   AudioConfig
		wantErr string
	}{
		{
			name:    "stereo",
			audio:   AudioConfig{Channels: 2},
			wantErr: "audio.channels must be 1",
		},
		{
			name:    "eight bit",
			audio:   AudioConfig{BitWidth: 8},
			wantErr: "audio.bit_width must be 16",
		},
		{
			name:    "too low sample rate",
			audio:   AudioConfig{SampleRate: 1},
			wantErr: "audio.sample_rate must be at least 8000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Model:       ModelConfig{Provider: "fake"},
				TTS:         TTSConfig{Provider: "minimax-cn"},
				STT:         STTConfig{Provider: "openai-whisper"},
				Audio:       tt.audio,
				InputMode:   "stt",
				TriggerMode: "wakeup",
			}

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected audio format validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestVoiceSessionConfigDefaults(t *testing.T) {
	cfg := Config{}

	if cfg.VoiceFollowupEnabledOrDefault() {
		t.Fatal("VoiceFollowupEnabledOrDefault() = true, want false")
	}
	if cfg.VoiceFirstTurnTimeoutOrDefault() != 10*time.Second {
		t.Fatalf("VoiceFirstTurnTimeoutOrDefault() = %s, want 10s", cfg.VoiceFirstTurnTimeoutOrDefault())
	}
	if cfg.VoiceFollowupTimeoutOrDefault() != 5*time.Second {
		t.Fatalf("VoiceFollowupTimeoutOrDefault() = %s, want 5s", cfg.VoiceFollowupTimeoutOrDefault())
	}
	if !cfg.VoiceInterruptOnWakeupOrDefault() {
		t.Fatal("VoiceInterruptOnWakeupOrDefault() = false, want true")
	}
	if !cfg.VoiceStreamingTTSEnabledOrDefault() {
		t.Fatal("VoiceStreamingTTSEnabledOrDefault() = false, want true")
	}
	if !cfg.VoiceToolCallSpeechOrDefault() {
		t.Fatal("VoiceToolCallSpeechOrDefault() = false, want true")
	}
	if !cfg.VoiceProgressSpeechEnabledOrDefault() {
		t.Fatal("VoiceProgressSpeechEnabledOrDefault() = false, want true")
	}
	if cfg.VoiceMaxResponseTokensOrDefault() != 300 {
		t.Fatalf("VoiceMaxResponseTokensOrDefault() = %d, want 300", cfg.VoiceMaxResponseTokensOrDefault())
	}
}

func TestVoiceSessionConfigOverrides(t *testing.T) {
	followupEnabled := true
	interruptDisabled := false
	streamingDisabled := false
	toolSpeech := false
	progressSpeechDisabled := false
	cfg := Config{
		VoiceFollowupEnabled:       &followupEnabled,
		VoiceFirstTurnTimeoutMs:    1234,
		VoiceFollowupTimeoutMs:     5678,
		VoiceInterruptOnWakeup:     &interruptDisabled,
		VoiceStreamingTTSEnabled:   &streamingDisabled,
		VoiceToolCallSpeech:        &toolSpeech,
		VoiceProgressSpeechEnabled: &progressSpeechDisabled,
		VoiceMaxResponseTokens:     123,
	}

	if !cfg.VoiceFollowupEnabledOrDefault() {
		t.Fatal("VoiceFollowupEnabledOrDefault() = false, want true")
	}
	if cfg.VoiceFirstTurnTimeoutOrDefault() != 1234*time.Millisecond {
		t.Fatalf("VoiceFirstTurnTimeoutOrDefault() = %s, want 1234ms", cfg.VoiceFirstTurnTimeoutOrDefault())
	}
	if cfg.VoiceFollowupTimeoutOrDefault() != 5678*time.Millisecond {
		t.Fatalf("VoiceFollowupTimeoutOrDefault() = %s, want 5678ms", cfg.VoiceFollowupTimeoutOrDefault())
	}
	if cfg.VoiceInterruptOnWakeupOrDefault() {
		t.Fatal("VoiceInterruptOnWakeupOrDefault() = true, want false")
	}
	if cfg.VoiceStreamingTTSEnabledOrDefault() {
		t.Fatal("VoiceStreamingTTSEnabledOrDefault() = true, want false")
	}
	if cfg.VoiceToolCallSpeechOrDefault() {
		t.Fatal("VoiceToolCallSpeechOrDefault() = true, want false")
	}
	if cfg.VoiceProgressSpeechEnabledOrDefault() {
		t.Fatal("VoiceProgressSpeechEnabledOrDefault() = true, want false")
	}
	if cfg.VoiceMaxResponseTokensOrDefault() != 123 {
		t.Fatalf("VoiceMaxResponseTokensOrDefault() = %d, want 123", cfg.VoiceMaxResponseTokensOrDefault())
	}
}

func TestVoiceSessionConfigValidationRejectsNegativeValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "negative followup timeout",
			cfg: Config{
				Model:                  ModelConfig{Provider: "fake"},
				VoiceFollowupTimeoutMs: -1,
			},
			want: "voice_followup_timeout_ms must be >= 0",
		},
		{
			name: "negative first turn timeout",
			cfg: Config{
				Model:                   ModelConfig{Provider: "fake"},
				VoiceFirstTurnTimeoutMs: -1,
			},
			want: "voice_first_turn_timeout_ms must be >= 0",
		},
		{
			name: "negative max turns",
			cfg: Config{
				Model:         ModelConfig{Provider: "fake"},
				VoiceMaxTurns: -1,
			},
			want: "voice_max_turns must be >= 0",
		},
		{
			name: "negative voice max response tokens",
			cfg: Config{
				Model:                  ModelConfig{Provider: "fake"},
				VoiceMaxResponseTokens: -1,
			},
			want: "voice_max_response_tokens must be >= 0",
		},
		{
			name: "negative todo reminder tool calls",
			cfg: Config{
				Model:                 ModelConfig{Provider: "fake"},
				TodoReminderToolCalls: -1,
			},
			want: "todo_reminder_tool_calls must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want contains %q", err, tt.want)
			}
		})
	}
}

func TestAudioArchiveConfigDefaults(t *testing.T) {
	configContent := `
[audio_archive]
enabled = true
storage_path = "/userdata/audio"
`

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "agent.toml")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	var cfg Config
	if _, err := toml.DecodeFile(configFile, &cfg); err != nil {
		t.Fatal(err)
	}

	if !cfg.AudioArchive.Enabled {
		t.Error("AudioArchive.Enabled should be true")
	}
	if cfg.AudioArchive.StoragePath != "/userdata/audio" {
		t.Errorf("StoragePath: got %q, want %q", cfg.AudioArchive.StoragePath, "/userdata/audio")
	}

	// Test defaults for unspecified fields
	if cfg.AudioArchive.MaxFilesOrDefault() != 500 {
		t.Errorf("MaxFiles default: got %d, want %d", cfg.AudioArchive.MaxFilesOrDefault(), 500)
	}
	if cfg.AudioArchive.MaxSizeMBOrDefault() != 100 {
		t.Errorf("MaxSizeMB default: got %d, want %d", cfg.AudioArchive.MaxSizeMBOrDefault(), 100)
	}
}

func TestLoadRuntimeConfigEnablesAudioArchiveByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "agent.toml")
	if err := os.WriteFile(configFile, []byte(`[model]
provider = "fake"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadRuntimeConfig(configFile)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if !cfg.AudioArchive.Enabled {
		t.Fatal("AudioArchive.Enabled = false, want true for runtime defaults")
	}
	if got, want := cfg.AudioArchive.StoragePathOrDefault(), "/userdata/audio"; got != want {
		t.Fatalf("AudioArchive.StoragePathOrDefault() = %q, want %q", got, want)
	}
}

func TestAudioArchiveStoragePathOrDefaultTrimsWhitespace(t *testing.T) {
	if got := (AudioArchiveConfig{StoragePath: "  /tmp/audio  "}).StoragePathOrDefault(); got != "/tmp/audio" {
		t.Fatalf("StoragePathOrDefault() = %q, want trimmed path", got)
	}
	if got := (AudioArchiveConfig{StoragePath: "  \t  "}).StoragePathOrDefault(); got != defaultAudioArchiveStoragePath {
		t.Fatalf("StoragePathOrDefault() = %q, want default path", got)
	}
}

func TestAudioArchiveConfigExplicitValues(t *testing.T) {
	configContent := `
[audio_archive]
enabled = false
max_files = 1000
max_size_mb = 200
storage_path = "/custom/path"
`

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "agent.toml")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	var cfg Config
	if _, err := toml.DecodeFile(configFile, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.AudioArchive.Enabled {
		t.Error("AudioArchive.Enabled should be false")
	}
	if cfg.AudioArchive.MaxFilesOrDefault() != 1000 {
		t.Errorf("MaxFiles: got %d, want %d", cfg.AudioArchive.MaxFilesOrDefault(), 1000)
	}
	if cfg.AudioArchive.MaxSizeMBOrDefault() != 200 {
		t.Errorf("MaxSizeMB: got %d, want %d", cfg.AudioArchive.MaxSizeMBOrDefault(), 200)
	}
	if cfg.AudioArchive.StoragePath != "/custom/path" {
		t.Errorf("StoragePath: got %q, want %q", cfg.AudioArchive.StoragePath, "/custom/path")
	}
}

func TestLoadConfig_LiveActivitySection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	body := `
[model]
provider = "openrouter"
api_key = "x"
model = "y"

[live_activity]
relay_url = "https://relay.example.com"
relay_api_key = "relay-secret"
board_id = "board-001"
bundle_id = "com.example.aiden"
environment = "sandbox"
team_id = "TEAMID1234"
key_id = "KEYID12345"
private_key_path = "/tmp/AuthKey.p8"
timeout_sec = 3
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LiveActivity.EnabledOrDefault() {
		t.Fatal("LiveActivity.EnabledOrDefault() = false, want true")
	}
	if !cfg.LiveActivity.RelayConfigured() {
		t.Fatal("LiveActivity.RelayConfigured() = false, want true")
	}
	if cfg.LiveActivity.RelayURL != "https://relay.example.com" || cfg.LiveActivity.RelayAPIKey != "relay-secret" {
		t.Fatalf("relay config = %#v, want configured URL/key", cfg.LiveActivity)
	}
	if cfg.LiveActivity.BoardIDOrDefault() != "board-001" {
		t.Fatalf("relay board_id = %q, want board-001", cfg.LiveActivity.BoardIDOrDefault())
	}
	if cfg.LiveActivity.APNsTopic() != "com.example.aiden.push-type.liveactivity" {
		t.Fatalf("APNsTopic() = %q", cfg.LiveActivity.APNsTopic())
	}
	if cfg.LiveActivity.TimeoutOrDefault() != 3*time.Second {
		t.Fatalf("TimeoutOrDefault() = %s, want 3s", cfg.LiveActivity.TimeoutOrDefault())
	}
}

func TestLoadRuntimeConfigFromDirGeneratesLiveActivityBoardID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	body := `
[model]
provider = "fake"

[live_activity]
relay_url = "https://relay.example.com"
board_id = "default"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadRuntimeConfigFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	boardID := cfg.LiveActivity.BoardIDOrDefault()
	if boardID == "" || boardID == "default" || !strings.HasPrefix(boardID, "board-") {
		t.Fatalf("generated board_id = %q, want non-default board-*", boardID)
	}
	data, err := os.ReadFile(filepath.Join(dir, liveActivityBoardIDFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != boardID {
		t.Fatalf("persisted board_id = %q, want %q", strings.TrimSpace(string(data)), boardID)
	}

	cfg, err = LoadRuntimeConfigFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.LiveActivity.BoardIDOrDefault(); got != boardID {
		t.Fatalf("reloaded board_id = %q, want persisted %q", got, boardID)
	}
}

func TestLoadOrCreateLiveActivityBoardIDConcurrent(t *testing.T) {
	dir := t.TempDir()
	const workers = 16
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			boardID, err := loadOrCreateLiveActivityBoardID(dir)
			if err != nil {
				errs <- err
				return
			}
			ids <- boardID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("concurrent board_id = %q, want %q", id, first)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, liveActivityBoardIDFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != first {
		t.Fatalf("persisted board_id = %q, want %q", got, first)
	}
}

func TestLiveActivityTimeoutDefaultsAndValidation(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "fake"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with default live activity timeout error = %v", err)
	}
	if got := cfg.LiveActivity.TimeoutOrDefault(); got != 10*time.Second {
		t.Fatalf("TimeoutOrDefault() = %s, want 10s", got)
	}

	cfg.LiveActivity.TimeoutSec = -1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "live_activity.timeout_sec") {
		t.Fatalf("Validate() error = %v, want live_activity.timeout_sec validation error", err)
	}
}

func TestLiveActivityRelayURLValidation(t *testing.T) {
	cases := []string{
		"http://relay.example.com",
		"https://user:pass@relay.example.com",
		"https://relay.example.com?token=abc",
		"https://relay.example.com/#fragment",
	}
	for _, relayURL := range cases {
		cfg := Config{
			Model:        ModelConfig{Provider: "fake"},
			LiveActivity: LiveActivityConfig{RelayURL: relayURL},
		}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "live_activity.relay_url") {
			t.Fatalf("Validate() with relay_url %q error = %v, want live_activity.relay_url validation error", relayURL, err)
		}
	}
}
