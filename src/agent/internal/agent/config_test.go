package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestConfigValidateAcceptsAudioWakeup(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "minimax-ws"},
		InputMode:   " audio ",
		TriggerMode: " wakeup ",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.InputModeOrDefault(); got != "audio" {
		t.Fatalf("InputModeOrDefault() = %q, want audio", got)
	}
	if got := cfg.TriggerModeOrDefault(); got != "wakeup" {
		t.Fatalf("TriggerModeOrDefault() = %q, want wakeup", got)
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
	if pruning.KeepN != 3 || pruning.Interval != 25 {
		t.Fatalf("default screenshot pruning = %#v, want keep_n=3 interval=25", pruning)
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

func TestLoadConfigParsesModelSpecOverrides(t *testing.T) {
	configDir := t.TempDir()
	config := `
instruction = "test"

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
instruction = "test"

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
instruction = "test"

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
	if cfg.Benchmark.JudgeModel != defaultBenchmarkJudgeModel {
		t.Fatalf("Benchmark.JudgeModel = %q, want runtime default %q",
			cfg.Benchmark.JudgeModel, defaultBenchmarkJudgeModel)
	}
	if cfg.VoiceMaxResponseTokens != defaultVoiceMaxResponseTokens {
		t.Fatalf("VoiceMaxResponseTokens = %d, want runtime default %d",
			cfg.VoiceMaxResponseTokens, defaultVoiceMaxResponseTokens)
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
	if err == nil || !strings.Contains(err.Error(), "tts.provider") {
		t.Fatalf("LoadRuntimeConfig(audio) error = %v, want tts.provider validation error", err)
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
		TTS:         TTSConfig{Provider: "minimax-ws"},
		InputMode:   "audio",
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

func TestLoadConfigRejectsMobileGymDeviceBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	content := `
instruction = "test"
input_mode = "text"
max_iterations = 20
force_simple_loop = true

[model]
provider = "fake"

[device]
backend = "mobilegym"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "invalid device.backend") {
		t.Fatalf("LoadConfig() error = %v, want invalid device.backend", err)
	}
}

func TestLoadConfigRejectsUnknownDeviceBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	content := `
instruction = "test"

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

func TestConfigValidateRequiresTTSForAudioWakeup(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		TTS:         TTSConfig{Provider: "   "},
		InputMode:   "audio",
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
				TTS:         TTSConfig{Provider: "minimax-ws"},
				Audio:       tt.audio,
				InputMode:   "audio",
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

	if !cfg.VoiceSessionEnabledOrDefault() {
		t.Fatal("VoiceSessionEnabledOrDefault() = false, want true")
	}
	if cfg.VoiceFirstTurnTimeoutOrDefault() != 10*time.Second {
		t.Fatalf("VoiceFirstTurnTimeoutOrDefault() = %s, want 10s", cfg.VoiceFirstTurnTimeoutOrDefault())
	}
	if cfg.VoiceFollowupTimeoutOrDefault() != 6*time.Second {
		t.Fatalf("VoiceFollowupTimeoutOrDefault() = %s, want 6s", cfg.VoiceFollowupTimeoutOrDefault())
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
	if cfg.VoiceMaxResponseTokensOrDefault() != 400 {
		t.Fatalf("VoiceMaxResponseTokensOrDefault() = %d, want 400", cfg.VoiceMaxResponseTokensOrDefault())
	}
}

func TestVoiceSessionConfigOverrides(t *testing.T) {
	disabled := false
	interruptDisabled := false
	streamingDisabled := false
	toolSpeech := false
	cfg := Config{
		VoiceSessionEnabled:      &disabled,
		VoiceFirstTurnTimeoutMs:  1234,
		VoiceFollowupTimeoutMs:   5678,
		VoiceInterruptOnWakeup:   &interruptDisabled,
		VoiceStreamingTTSEnabled: &streamingDisabled,
		VoiceToolCallSpeech:      &toolSpeech,
		VoiceMaxResponseTokens:   123,
	}

	if cfg.VoiceSessionEnabledOrDefault() {
		t.Fatal("VoiceSessionEnabledOrDefault() = true, want false")
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

func TestLoadConfig_BenchmarkSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	body := `
[model]
provider = "openrouter"
api_key = "x"
model = "y"

[benchmark]
judge_model = "custom/model-v1"
benchmark_dir = "/tmp/bench"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Benchmark.JudgeModel != "custom/model-v1" {
		t.Errorf("JudgeModel = %q", cfg.Benchmark.JudgeModel)
	}
	if cfg.Benchmark.Dir != "/tmp/bench" {
		t.Errorf("Dir = %q", cfg.Benchmark.Dir)
	}
}

func TestLoadConfig_BenchmarkDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	body := `
[model]
provider = "openrouter"
api_key = "x"
model = "y"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Benchmark.JudgeModel != "" {
		t.Errorf("expected empty JudgeModel default, got %q", cfg.Benchmark.JudgeModel)
	}
}
