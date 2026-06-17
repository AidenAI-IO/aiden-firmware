package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type SearchConfig struct {
	Provider string `toml:"provider,omitempty"`
	APIKey   string `toml:"api_key,omitempty"`
}

type ToolProxyConfig struct {
	Enabled  bool   `toml:"enabled"`
	Endpoint string `toml:"endpoint"`
}

const (
	searchProviderDuckDuckGo = "duckduckgo"
	searchProviderTavily     = "tavily"
	searchProviderBrave      = "brave"

	braveSearchAPIKeyEnv = "BRAVE_SEARCH_API_KEY"
)

func (s SearchConfig) ProviderOrDefault() string {
	return normalizeSearchProvider(s.Provider)
}

func normalizeSearchProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	if normalized == "" {
		return searchProviderDuckDuckGo
	}
	alias := strings.NewReplacer("_", "-", " ", "-").Replace(normalized)
	switch alias {
	case searchProviderDuckDuckGo:
		return searchProviderDuckDuckGo
	case searchProviderTavily:
		return searchProviderTavily
	case searchProviderBrave, "brave-search", "brave-free":
		return searchProviderBrave
	}
	return normalized
}

func searchAPIKeyOrEnv(configured string, envKeys ...string) string {
	if key := strings.TrimSpace(configured); key != "" {
		return key
	}
	for _, envKey := range envKeys {
		if key := strings.TrimSpace(os.Getenv(envKey)); key != "" {
			return key
		}
	}
	return ""
}

// AudioArchiveConfig controls optional archival of voice recordings as WAV files.
type AudioArchiveConfig struct {
	Enabled     bool   `toml:"enabled"`
	MaxFiles    int    `toml:"max_files,omitempty"`
	MaxSizeMB   int    `toml:"max_size_mb,omitempty"`
	StoragePath string `toml:"storage_path,omitempty"`
}

// MaxFilesOrDefault returns MaxFiles if positive, else 500.
func (c AudioArchiveConfig) MaxFilesOrDefault() int {
	if c.MaxFiles <= 0 {
		return defaultAudioArchiveMaxFiles
	}
	return c.MaxFiles
}

// MaxSizeMBOrDefault returns MaxSizeMB if positive, else 100.
func (c AudioArchiveConfig) MaxSizeMBOrDefault() int {
	if c.MaxSizeMB <= 0 {
		return defaultAudioArchiveMaxSizeMB
	}
	return c.MaxSizeMB
}

// StoragePathOrDefault returns StoragePath if non-empty, else "/userdata/audio".
func (c AudioArchiveConfig) StoragePathOrDefault() string {
	path := strings.TrimSpace(c.StoragePath)
	if path == "" {
		return defaultAudioArchiveStoragePath
	}
	return path
}

type Config struct {
	Model                     ModelConfig        `toml:"model"`
	ModelText                 ModelConfig        `toml:"model_text,omitempty"` // Override for STT-then-text mode
	TTS                       TTSConfig          `toml:"tts,omitempty"`
	STT                       STTConfig          `toml:"stt,omitempty"`
	HID                       HIDConfig          `toml:"hid"`
	Device                    DeviceConfig       `toml:"device,omitempty"`
	Audio                     AudioConfig        `toml:"audio,omitempty"`
	AudioArchive              AudioArchiveConfig `toml:"audio_archive,omitempty"`
	Benchmark                 BenchmarkConfig    `toml:"benchmark,omitempty"`
	Search                    SearchConfig       `toml:"search,omitempty"`
	ToolProxy                 ToolProxyConfig    `toml:"tool_proxy,omitempty"`
	Instruction               string             `toml:"instruction"`
	AdditionalPrompt          string             `toml:"additional_prompt,omitempty"`
	InputMode                 string             `toml:"input_mode,omitempty"`   // "text", "audio", "stt"
	TriggerMode               string             `toml:"trigger_mode,omitempty"` // "manual", "wakeup"
	VADBackend                string             `toml:"vad_backend,omitempty"`  // "rknn", "cpu"
	VADModelPath              string             `toml:"vad_model_path,omitempty"`
	VADHelperPath             string             `toml:"vad_helper_path,omitempty"`
	VADSpeechThreshold        float64            `toml:"vad_speech_threshold,omitempty"`
	SilenceMs                 int                `toml:"silence_ms,omitempty"`
	MinSpeechMs               int                `toml:"min_speech_ms,omitempty"`
	VoiceSessionEnabled       *bool              `toml:"voice_session_enabled,omitempty"`
	VoiceFollowupTimeoutMs    int                `toml:"voice_followup_timeout_ms,omitempty"`
	VoiceFirstTurnTimeoutMs   int                `toml:"voice_first_turn_timeout_ms,omitempty"`
	VoiceMaxTurns             int                `toml:"voice_max_turns,omitempty"`
	VoiceInterruptOnWakeup    *bool              `toml:"voice_interrupt_on_wakeup,omitempty"`
	VoiceStreamingTTSEnabled  *bool              `toml:"voice_streaming_tts_enabled,omitempty"`
	VoiceToolCallSpeech       *bool              `toml:"voice_tool_call_speech,omitempty"`
	VoiceMaxResponseTokens    int                `toml:"voice_max_response_tokens,omitempty"`
	MaxIterations             int                `toml:"max_iterations,omitempty"`
	ForceSimpleLoop           bool               `toml:"force_simple_loop,omitempty"`
	ScreenshotKeepN           int                `toml:"screenshot_keep_n,omitempty"`
	ScreenshotPruneInterval   int                `toml:"screenshot_prune_interval,omitempty"`
	ScreenStableTimeoutMs     int                `toml:"screen_stable_timeout_ms,omitempty"`
	ScreenStableMs            int                `toml:"screen_stable_ms,omitempty"`
	ScreenStableDiffThreshold float64            `toml:"screen_stable_diff_threshold,omitempty"`
	SkillsDirs                []string           `toml:"skills_dirs"`
	BundledSkillsDir          string             `toml:"bundled_skills_dir,omitempty"`
	SkillMergeModel           SkillMergeModel    `toml:"-"`
	Telemetry                 TelemetryConfig    `toml:"telemetry,omitempty"`
	ConfigDir                 string             `toml:"-"`
}

type TelemetryConfig struct {
	Enabled           *bool    `toml:"enabled,omitempty"`
	Provider          string   `toml:"provider,omitempty"`
	BaseURL           string   `toml:"base_url,omitempty"`
	PublicKey         string   `toml:"public_key,omitempty"`
	SecretKey         string   `toml:"secret_key,omitempty"`
	UploadScreenshots *bool    `toml:"upload_screenshots,omitempty"`
	UploadTimeoutSec  int      `toml:"upload_timeout_sec,omitempty"`
	MaxRetry          int      `toml:"max_retry,omitempty"`
	Tags              []string `toml:"tags,omitempty"`
	Environment       string   `toml:"environment,omitempty"`
}

type TTSConfig struct {
	Provider    string  `toml:"provider"` // "minimax-ws", "fish-audio", "alicloud", "volcengine"
	APIKey      string  `toml:"api_key,omitempty"`
	Model       string  `toml:"model,omitempty"`
	VoiceID     string  `toml:"voice_id,omitempty"`
	Emotion     string  `toml:"emotion,omitempty"`
	Speed       float64 `toml:"speed,omitempty"`
	ReferenceID string  `toml:"reference_id,omitempty"` // Fish Audio voice reference ID

	// Credentials lets you store per-provider settings so the app can switch
	// providers at runtime without losing each one's API key/voice. Keys are
	// matched case-insensitively against the provider name passed to switch.
	//
	// Example agent.toml:
	//   [tts]
	//   provider = "minimax-ws"
	//   api_key = "<minimax-key>"   # used as fallback for any provider
	//
	//   [tts.credentials.fish-audio]
	//   api_key = "<fish-key>"
	//   voice_id = "<fish-reference-id>"
	//
	//   [tts.credentials.cartesia]
	//   api_key = "<cartesia-key>"
	//   voice_id = "<cartesia-voice>"
	Credentials map[string]TTSProviderCredentials `toml:"credentials,omitempty"`
}

// TTSProviderCredentials holds per-provider override settings.
// Any field left blank falls back to the top-level [tts] values.
type TTSProviderCredentials struct {
	APIKey      string  `toml:"api_key,omitempty"`
	VoiceID     string  `toml:"voice_id,omitempty"`
	Emotion     string  `toml:"emotion,omitempty"`
	Model       string  `toml:"model,omitempty"`
	Speed       float64 `toml:"speed,omitempty"`
	ReferenceID string  `toml:"reference_id,omitempty"`
}

type STTConfig struct {
	Provider        string `toml:"provider"` // "openai", "openai-whisper", "openrouter", "tencent", "tencent_asr"
	APIKey          string `toml:"api_key,omitempty"`
	Model           string `toml:"model,omitempty"`
	BaseURL         string `toml:"base_url,omitempty"`
	SecretID        string `toml:"secret_id,omitempty"`
	SecretKey       string `toml:"secret_key,omitempty"`
	Region          string `toml:"region,omitempty"`
	EngineModelType string `toml:"engine_model_type,omitempty"`
}

type AudioConfig struct {
	Socket     string `toml:"socket,omitempty"`
	SampleRate int    `toml:"sample_rate,omitempty"`
	Channels   int    `toml:"channels,omitempty"`
	BitWidth   int    `toml:"bit_width,omitempty"`
}

// BenchmarkConfig configures the benchmark management endpoints in the agent
// (migrated from config_web). All fields are optional; empty strings fall back
// to runtime defaults.
type BenchmarkConfig struct {
	// JudgeModel is the OpenRouter model name passed to runner.main as
	// --judge-model. Defaults to "bytedance-seed/seed-2.0-lite" when empty.
	JudgeModel string `toml:"judge_model,omitempty"`
	// APIKey is exported as OPENROUTER_API_KEY for benchmark judge calls.
	APIKey string `toml:"api_key,omitempty"`
	// Dir overrides the auto-detected benchmark root. When empty, the
	// agent probes -benchmark-dir flag, AIDEN_BENCHMARK_DIR env,
	// /userdata/agent/benchmark, then <cwd>/benchmark.
	Dir string `toml:"benchmark_dir,omitempty"`
}

type ProxyConfig struct {
	HTTPProxy  string
	HTTPSProxy string
	AllProxy   string
	NoProxy    string
}

const DefaultNoProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"

func (p ProxyConfig) HasProxyURL() bool {
	return strings.TrimSpace(p.HTTPProxy) != "" ||
		strings.TrimSpace(p.HTTPSProxy) != "" ||
		strings.TrimSpace(p.AllProxy) != ""
}

func (p ProxyConfig) WithDefaults() ProxyConfig {
	if p.HasProxyURL() && strings.TrimSpace(p.NoProxy) == "" {
		p.NoProxy = DefaultNoProxy
	}
	return p
}

func ProxyConfigFromEnvironment() ProxyConfig {
	p := ProxyConfig{
		HTTPProxy:  firstEnv("HTTP_PROXY", "http_proxy"),
		HTTPSProxy: firstEnv("HTTPS_PROXY", "https_proxy"),
		AllProxy:   firstEnv("ALL_PROXY", "all_proxy"),
		NoProxy:    firstEnv("NO_PROXY", "no_proxy"),
	}
	return p.WithDefaults()
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func (p ProxyConfig) IsZero() bool {
	return strings.TrimSpace(p.HTTPProxy) == "" &&
		strings.TrimSpace(p.HTTPSProxy) == "" &&
		strings.TrimSpace(p.AllProxy) == "" &&
		strings.TrimSpace(p.NoProxy) == ""
}

func (p ProxyConfig) Validate() error {
	for name, value := range map[string]string{
		"http_proxy":  p.HTTPProxy,
		"https_proxy": p.HTTPSProxy,
		"all_proxy":   p.AllProxy,
	} {
		if err := validateProxyURL(value); err != nil {
			return fmt.Errorf("proxy.%s: %w", name, err)
		}
	}
	return nil
}

func (a AudioConfig) SocketOrDefault() string {
	if a.Socket != "" {
		return a.Socket
	}
	return defaultAudioSocket
}

func (a AudioConfig) SampleRateOrDefault() int {
	if a.SampleRate > 0 {
		return a.SampleRate
	}
	return defaultAudioSampleRate
}

func (a AudioConfig) ChannelsOrDefault() int {
	if a.Channels > 0 {
		return a.Channels
	}
	return defaultAudioChannels
}

func (a AudioConfig) BitWidthOrDefault() int {
	if a.BitWidth > 0 {
		return a.BitWidth
	}
	return defaultAudioBitWidth
}

type HIDConfig struct {
	KeyboardDevice string `toml:"keyboard_device,omitempty"`
	MouseDevice    string `toml:"mouse_device,omitempty"`
	FrameSocket    string `toml:"frame_socket,omitempty"`
	// PointerMode selects the hid.usb1 report format: "absolute" (iOS AssistiveTouch)
	// or "touchscreen" (Android HID digitizer).
	PointerMode string `toml:"pointer_mode,omitempty"`
}

type DeviceConfig struct {
	Backend          string   `toml:"backend,omitempty"`
	BridgeURL        string   `toml:"bridge_url,omitempty"`
	BridgeTokenFile  string   `toml:"bridge_token_file,omitempty"`
	ControlTokenFile string   `toml:"control_token_file,omitempty"`
	ToolAllowlist    []string `toml:"tool_allowlist,omitempty"`
}

func (d DeviceConfig) BackendOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(d.Backend)) {
	case "", "hdmi", "hardware":
		return "hdmi"
	case "mobilegym":
		return "mobilegym"
	default:
		return strings.ToLower(strings.TrimSpace(d.Backend))
	}
}

func (h HIDConfig) KeyboardDeviceOrDefault() string {
	if h.KeyboardDevice != "" {
		return h.KeyboardDevice
	}
	return defaultKeyboardDevice
}

func (h HIDConfig) MouseDeviceOrDefault() string {
	if h.MouseDevice != "" {
		return h.MouseDevice
	}
	return defaultMouseDevice
}

func (h HIDConfig) FrameSocketOrDefault() string {
	if h.FrameSocket != "" {
		return h.FrameSocket
	}
	return defaultFrameServiceSocket
}

func (h HIDConfig) PointerModeOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(h.PointerMode)) {
	case "touchscreen":
		return "touchscreen"
	default:
		return "absolute"
	}
}

func (h HIDConfig) PointerTouchscreen() bool {
	return h.PointerModeOrDefault() == "touchscreen"
}

type ModelConfig struct {
	Provider          string  `toml:"provider"`
	Model             string  `toml:"model"`
	BaseURL           string  `toml:"base_url,omitempty"`
	APIKey            string  `toml:"api_key,omitempty"`
	TokenEnv          string  `toml:"token_env,omitempty"`
	Temperature       float64 `toml:"temperature,omitempty"`
	MaxResponseTokens int     `toml:"max_response_tokens,omitempty"`
	// These override static model metadata; zero means use the registry/fallback.
	ContextWindow        int      `toml:"context_window,omitempty"`
	ModelMaxOutputTokens int      `toml:"model_max_output_tokens,omitempty"`
	Responses            []string `toml:"responses,omitempty"`
}

// AgentConfig is used internally by the runtime prompt builder.
type AgentConfig struct {
	Instruction      string
	AdditionalPrompt string
	RuntimeContext   string
	ForceSimpleLoop  bool
}

// MemoryConfig is used internally by the memory manager.
type MemoryConfig struct {
	Type       string
	WindowSize int
	MemoryKey  string
}

func LoadConfigFromDir(configDir string) (Config, error) {
	return loadConfigFromDir(configDir, LoadConfig)
}

// LoadRuntimeConfigFromDir loads the daemon-facing runtime config from a config
// directory. It preserves the historic agent.toml -> agent.json lookup while
// returning a config with runtime defaults resolved.
func LoadRuntimeConfigFromDir(configDir string) (Config, error) {
	return loadConfigFromDir(configDir, LoadRuntimeConfig)
}

type configFileLoader func(string) (Config, error)

func loadConfigFromDir(configDir string, load configFileLoader) (Config, error) {
	configPath := filepath.Join(configDir, "agent.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Fallback to .json for backward compatibility
		configPath = filepath.Join(configDir, "agent.json")
	}

	cfg, err := load(configPath)
	if err != nil {
		return Config{}, err
	}

	// Store the config directory for logger and other uses
	cfg.ConfigDir = configDir

	skillsDir := filepath.Join(configDir, "skills")
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
		cfg.SkillsDirs = []string{skillsDir}
	} else {
		cfg.SkillsDirs = []string{}
	}

	if cfg.BundledSkillsDir == "" {
		cfg.BundledSkillsDir = resolveBundledSkillsDir()
	}

	return cfg, nil
}

func resolveBundledSkillsDir() string {
	if v := os.Getenv("AIDEN_BUNDLED_SKILLS_DIR"); v != "" {
		return v
	}
	for _, dir := range bundledSkillsDirCandidates() {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

func bundledSkillsDirCandidates() []string {
	return []string{"/oem/usr/share/aiden/skills"}
}

func LoadConfig(path string) (Config, error) {
	var cfg Config

	if _, err := decodeConfigFile(path, &cfg); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// LoadRuntimeConfig loads a config file over runtime defaults and returns the
// effective config used by the daemon. Optional speech providers remain opt-in:
// their provider fields are not inherited from DefaultConfig unless the raw
// config explicitly sets them.
func LoadRuntimeConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	metadata, err := decodeConfigFile(path, &cfg)
	if err != nil {
		return Config{}, err
	}

	applyRuntimeOptionalProviderDefaults(&cfg, metadata)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyRuntimeOptionalProviderDefaults(cfg *Config, metadata toml.MetaData) {
	if cfg == nil {
		return
	}

	if !metadata.IsDefined("tts", "provider") || strings.TrimSpace(cfg.TTS.Provider) == "" {
		cfg.TTS = TTSConfig{}
	} else if normalizeTTSProvider(cfg.TTS.Provider) != defaultTTSProvider {
		clearDefaultTTSProviderFields(cfg, metadata)
	}
	if !metadata.IsDefined("stt", "provider") || strings.TrimSpace(cfg.STT.Provider) == "" {
		cfg.STT = STTConfig{}
	} else if !usesDefaultSTTModel(cfg.STT.Provider) && !metadata.IsDefined("stt", "model") {
		cfg.STT.Model = ""
	}
}

func normalizeTTSProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func clearDefaultTTSProviderFields(cfg *Config, metadata toml.MetaData) {
	if !metadata.IsDefined("tts", "model") {
		cfg.TTS.Model = ""
	}
	if !metadata.IsDefined("tts", "voice_id") {
		cfg.TTS.VoiceID = ""
	}
	if !metadata.IsDefined("tts", "emotion") {
		cfg.TTS.Emotion = ""
	}
	if !metadata.IsDefined("tts", "reference_id") {
		cfg.TTS.ReferenceID = ""
	}
	if !metadata.IsDefined("tts", "speed") {
		cfg.TTS.Speed = 0
	}
}

func usesDefaultSTTModel(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", defaultSTTProvider:
		return true
	default:
		return false
	}
}

// LoadResolvedConfig loads a config file over the canonical defaults and
// returns the effective values used by config-editing surfaces. Missing files
// are treated as "all defaults" so first-boot config pages can render before
// agent.toml has been created.
func LoadResolvedConfig(path string) (Config, error) {
	resolvedPath, exists, err := resolveConfigPath(path)
	if err != nil {
		return Config{}, err
	}

	cfg := DefaultConfig()
	if exists {
		if _, err := decodeConfigFile(resolvedPath, &cfg); err != nil {
			return Config{}, err
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func resolveConfigPath(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" {
		return "", false, errors.New("config path is required")
	}

	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return path, true, nil
		}

		tomlPath := filepath.Join(path, "agent.toml")
		if _, err := os.Stat(tomlPath); err == nil {
			return tomlPath, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("read config: %w", err)
		}

		jsonPath := filepath.Join(path, "agent.json")
		if _, err := os.Stat(jsonPath); err == nil {
			return jsonPath, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("read config: %w", err)
		}

		return tomlPath, false, nil
	}
	if os.IsNotExist(err) {
		return path, false, nil
	}
	return "", false, fmt.Errorf("read config: %w", err)
}

func decodeConfigFile(path string, cfg *Config) (toml.MetaData, error) {
	var metadata toml.MetaData

	// Determine format by file extension
	if strings.HasSuffix(path, ".toml") {
		var err error
		if metadata, err = toml.DecodeFile(path, cfg); err != nil {
			return toml.MetaData{}, fmt.Errorf("decode TOML config: %w", err)
		}
	} else {
		_, err := os.Stat(path)
		if err != nil {
			return toml.MetaData{}, fmt.Errorf("read config: %w", err)
		}
		return toml.MetaData{}, fmt.Errorf("JSON format is deprecated, please use TOML format: %s", path)
	}

	if err := applyLegacyModelMaxTokens(path, metadata, cfg); err != nil {
		return toml.MetaData{}, err
	}
	return metadata, nil
}

func applyLegacyModelMaxTokens(path string, metadata toml.MetaData, cfg *Config) error {
	needsModel := metadata.IsDefined("model", "max_tokens") &&
		!metadata.IsDefined("model", "max_response_tokens")
	needsModelText := metadata.IsDefined("model_text", "max_tokens") &&
		!metadata.IsDefined("model_text", "max_response_tokens")
	if !needsModel && !needsModelText {
		return nil
	}

	var raw map[string]interface{}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return fmt.Errorf("decode legacy TOML fields: %w", err)
	}
	if needsModel {
		value, err := legacyModelMaxTokens(raw, "model")
		if err != nil {
			return err
		}
		cfg.Model.MaxResponseTokens = value
	}
	if needsModelText {
		value, err := legacyModelMaxTokens(raw, "model_text")
		if err != nil {
			return err
		}
		cfg.ModelText.MaxResponseTokens = value
	}
	return nil
}

func legacyModelMaxTokens(raw map[string]interface{}, section string) (int, error) {
	table, ok := raw[section].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("%s.max_tokens is defined but %s is not a TOML table", section, section)
	}
	value, ok := table["max_tokens"]
	if !ok {
		return 0, fmt.Errorf("%s.max_tokens is defined but could not be decoded", section)
	}
	tokens, ok := value.(int64)
	if !ok {
		return 0, fmt.Errorf("%s.max_tokens must be an integer", section)
	}
	return int(tokens), nil
}

func (c Config) Validate() error {
	switch c.Search.ProviderOrDefault() {
	case searchProviderDuckDuckGo:
	case searchProviderTavily:
		if searchAPIKeyOrEnv(c.Search.APIKey) == "" {
			return errors.New("search.api_key is required when search.provider=tavily")
		}
	case searchProviderBrave:
		if searchAPIKeyOrEnv(c.Search.APIKey, braveSearchAPIKeyEnv) == "" {
			return errors.New("search.api_key or BRAVE_SEARCH_API_KEY is required when search.provider=brave")
		}
	default:
		return fmt.Errorf("invalid search.provider: %s (expected duckduckgo, brave, or tavily)", c.Search.Provider)
	}

	if strings.TrimSpace(c.Model.Provider) == "" {
		return errors.New("model.provider is required")
	}
	if strings.TrimSpace(c.Model.Model) == "" && strings.ToLower(c.Model.Provider) != "fake" {
		return errors.New("model.model is required")
	}
	backend := c.Device.BackendOrDefault()
	switch backend {
	case "hdmi", "mobilegym":
	default:
		return fmt.Errorf("invalid device.backend: %s (expected hdmi or mobilegym)", c.Device.Backend)
	}
	if backend == "mobilegym" && strings.TrimSpace(c.Device.ControlTokenFile) == "" {
		return errors.New("device.control_token_file is required when device.backend=mobilegym")
	}
	if c.Model.MaxResponseTokens < 0 {
		return fmt.Errorf("model.max_response_tokens must be >= 0, got %d", c.Model.MaxResponseTokens)
	}
	if c.Model.ContextWindow < 0 {
		return fmt.Errorf("model.context_window must be >= 0, got %d", c.Model.ContextWindow)
	}
	if c.Model.ModelMaxOutputTokens < 0 {
		return fmt.Errorf("model.model_max_output_tokens must be >= 0, got %d", c.Model.ModelMaxOutputTokens)
	}
	if c.ModelText.ContextWindow < 0 {
		return fmt.Errorf("model_text.context_window must be >= 0, got %d", c.ModelText.ContextWindow)
	}
	if c.ModelText.ModelMaxOutputTokens < 0 {
		return fmt.Errorf("model_text.model_max_output_tokens must be >= 0, got %d", c.ModelText.ModelMaxOutputTokens)
	}
	if c.ModelText.MaxResponseTokens < 0 {
		return fmt.Errorf("model_text.max_response_tokens must be >= 0, got %d", c.ModelText.MaxResponseTokens)
	}

	// Validate input_mode
	if strings.TrimSpace(c.InputMode) != "" {
		mode := strings.ToLower(strings.TrimSpace(c.InputMode))
		if mode != "text" && mode != "audio" && mode != "stt" {
			return fmt.Errorf("invalid input_mode: %s (expected text, audio, or stt)", c.InputMode)
		}

		// Validate STT config if in stt mode
		if mode == "stt" {
			if strings.TrimSpace(c.STT.Provider) == "" {
				return errors.New("stt.provider is required when input_mode=stt")
			}
		}

		// Validate TTS config if not in text mode
		if mode != "text" && strings.TrimSpace(c.TTS.Provider) == "" {
			return errors.New("tts.provider is required when input_mode is audio or stt")
		}

		if mode != "text" {
			if c.Audio.SampleRate != 0 && c.Audio.SampleRate < 8000 {
				return fmt.Errorf("audio.sample_rate must be at least 8000 when set, got %d", c.Audio.SampleRate)
			}
			if c.Audio.Channels != 0 && c.Audio.Channels != 1 {
				return fmt.Errorf("audio.channels must be 1 when input_mode is audio or stt, got %d", c.Audio.Channels)
			}
			if c.Audio.BitWidth != 0 && c.Audio.BitWidth != 16 {
				return fmt.Errorf("audio.bit_width must be 16 when input_mode is audio or stt, got %d", c.Audio.BitWidth)
			}
		}
	}

	if strings.TrimSpace(c.TriggerMode) != "" {
		triggerMode := strings.ToLower(strings.TrimSpace(c.TriggerMode))
		if triggerMode != "manual" && triggerMode != "wakeup" {
			return fmt.Errorf("invalid trigger_mode: %s (expected manual or wakeup)", c.TriggerMode)
		}
		effectiveInputMode := strings.ToLower(strings.TrimSpace(c.InputModeOrDefault()))
		if triggerMode == "wakeup" && effectiveInputMode != "audio" && effectiveInputMode != "stt" {
			return fmt.Errorf("incompatible trigger_mode %q with input_mode %q: wakeup requires input_mode audio or stt", c.TriggerMode, c.InputMode)
		}
	}

	if _, err := normalizeVADBackend(c.VADBackend); err != nil {
		return err
	}
	if c.VADSpeechThreshold != 0 && (c.VADSpeechThreshold < 0 || c.VADSpeechThreshold > 1) {
		return fmt.Errorf("vad_speech_threshold must be in [0,1] when set, got %v", c.VADSpeechThreshold)
	}

	if c.VoiceFollowupTimeoutMs < 0 {
		return fmt.Errorf("voice_followup_timeout_ms must be >= 0, got %d", c.VoiceFollowupTimeoutMs)
	}
	if c.VoiceFirstTurnTimeoutMs < 0 {
		return fmt.Errorf("voice_first_turn_timeout_ms must be >= 0, got %d", c.VoiceFirstTurnTimeoutMs)
	}
	if c.VoiceMaxTurns < 0 {
		return fmt.Errorf("voice_max_turns must be >= 0, got %d", c.VoiceMaxTurns)
	}
	if c.VoiceMaxResponseTokens < 0 {
		return fmt.Errorf("voice_max_response_tokens must be >= 0, got %d", c.VoiceMaxResponseTokens)
	}
	if c.ScreenshotKeepN < 0 {
		return fmt.Errorf("screenshot_keep_n must be >= 0, got %d", c.ScreenshotKeepN)
	}
	if c.ScreenshotPruneInterval < 0 {
		return fmt.Errorf("screenshot_prune_interval must be >= 0, got %d", c.ScreenshotPruneInterval)
	}
	if c.ScreenStableTimeoutMs < 0 {
		return fmt.Errorf("screen_stable_timeout_ms must be >= 0, got %d", c.ScreenStableTimeoutMs)
	}
	if c.ScreenStableMs < 0 {
		return fmt.Errorf("screen_stable_ms must be >= 0, got %d", c.ScreenStableMs)
	}
	if c.ScreenStableDiffThreshold < 0 {
		return fmt.Errorf("screen_stable_diff_threshold must be >= 0, got %g", c.ScreenStableDiffThreshold)
	}

	if c.MaxIterations < -1 {
		return fmt.Errorf("max_iterations must be >= -1 (-1 means unlimited), got %d", c.MaxIterations)
	}

	switch strings.ToLower(strings.TrimSpace(c.HID.PointerMode)) {
	case "", "absolute", "touchscreen":
	default:
		return fmt.Errorf("invalid hid.pointer_mode: %s (expected absolute or touchscreen)", c.HID.PointerMode)
	}

	if err := c.Telemetry.Validate(); err != nil {
		return err
	}

	return nil
}

func (t TelemetryConfig) Validate() error {
	if !t.EnabledOrDefault() {
		return nil
	}
	if strings.TrimSpace(t.BaseURL) == "" {
		return errors.New("telemetry.base_url is required when telemetry.enabled=true")
	}
	if strings.TrimSpace(t.PublicKey) == "" {
		return errors.New("telemetry.public_key is required when telemetry.enabled=true")
	}
	if strings.TrimSpace(t.SecretKey) == "" {
		return errors.New("telemetry.secret_key is required when telemetry.enabled=true")
	}
	switch t.ProviderOrDefault() {
	case "langfuse":
	default:
		return fmt.Errorf("invalid telemetry.provider: %s (expected langfuse)", t.Provider)
	}
	if t.UploadTimeoutSec < 0 {
		return fmt.Errorf("telemetry.upload_timeout_sec must be >= 0, got %d", t.UploadTimeoutSec)
	}
	if t.MaxRetry < 0 {
		return fmt.Errorf("telemetry.max_retry must be >= 0, got %d", t.MaxRetry)
	}
	return nil
}

func (t TelemetryConfig) EnabledOrDefault() bool {
	if t.Enabled != nil {
		return *t.Enabled
	}
	return false
}

func (t TelemetryConfig) ProviderOrDefault() string {
	if strings.TrimSpace(t.Provider) != "" {
		return strings.ToLower(strings.TrimSpace(t.Provider))
	}
	return "langfuse"
}

func (t TelemetryConfig) UploadScreenshotsOrDefault() bool {
	if t.UploadScreenshots != nil {
		return *t.UploadScreenshots
	}
	return true
}

func (t TelemetryConfig) UploadTimeoutOrDefault() time.Duration {
	if t.UploadTimeoutSec > 0 {
		return time.Duration(t.UploadTimeoutSec) * time.Second
	}
	return 30 * time.Second
}

func (t TelemetryConfig) MaxRetryOrDefault() int {
	if t.MaxRetry > 0 {
		return t.MaxRetry
	}
	return 2
}

func (t TelemetryConfig) EnvironmentOrDefault() string {
	if strings.TrimSpace(t.Environment) != "" {
		return strings.TrimSpace(t.Environment)
	}
	return "default"
}

// InputModeOrDefault returns the input mode or "text" as default
func (c Config) InputModeOrDefault() string {
	mode := strings.TrimSpace(c.InputMode)
	if mode == "" {
		return defaultInputMode
	}
	return strings.ToLower(mode)
}

// TriggerModeOrDefault returns the trigger mode or "manual" as default
func (c Config) TriggerModeOrDefault() string {
	mode := strings.TrimSpace(c.TriggerMode)
	if mode == "" {
		return defaultTriggerMode
	}
	return strings.ToLower(mode)
}

func (c Config) VADBackendOrDefault() string {
	backend, err := normalizeVADBackend(c.VADBackend)
	if err != nil {
		return defaultVADBackend
	}
	return backend
}

func (c Config) VoiceSessionEnabledOrDefault() bool {
	if c.VoiceSessionEnabled != nil {
		return *c.VoiceSessionEnabled
	}
	return true
}

func (c Config) VoiceFollowupTimeoutOrDefault() time.Duration {
	if c.VoiceFollowupTimeoutMs > 0 {
		return time.Duration(c.VoiceFollowupTimeoutMs) * time.Millisecond
	}
	return time.Duration(defaultVoiceFollowupTimeoutMs) * time.Millisecond
}

func (c Config) VoiceFirstTurnTimeoutOrDefault() time.Duration {
	if c.VoiceFirstTurnTimeoutMs > 0 {
		return time.Duration(c.VoiceFirstTurnTimeoutMs) * time.Millisecond
	}
	return time.Duration(defaultVoiceFirstTurnTimeoutMs) * time.Millisecond
}

func (c Config) VoiceInterruptOnWakeupOrDefault() bool {
	if c.VoiceInterruptOnWakeup != nil {
		return *c.VoiceInterruptOnWakeup
	}
	return true
}

func (c Config) VoiceStreamingTTSEnabledOrDefault() bool {
	if c.VoiceStreamingTTSEnabled != nil {
		return *c.VoiceStreamingTTSEnabled
	}
	return true
}

func (c Config) VoiceToolCallSpeechOrDefault() bool {
	if c.VoiceToolCallSpeech != nil {
		return *c.VoiceToolCallSpeech
	}
	return true
}

func (c Config) VoiceMaxResponseTokensOrDefault() int {
	if c.VoiceMaxResponseTokens > 0 {
		return c.VoiceMaxResponseTokens
	}
	return defaultVoiceMaxResponseTokens
}

func (c Config) ScreenshotPruningOrDefault() ScreenshotPruningConfig {
	return ScreenshotPruningConfig{
		KeepN:    c.ScreenshotKeepN,
		Interval: c.ScreenshotPruneInterval,
	}.WithDefaults()
}

func (c Config) ScreenStableDefaults() ScreenStableDefaults {
	return ScreenStableDefaults{
		TimeoutMs:     c.ScreenStableTimeoutMs,
		StableMs:      c.ScreenStableMs,
		DiffThreshold: c.ScreenStableDiffThreshold,
	}
}
