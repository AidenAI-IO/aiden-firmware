package agent

import (
	"aiden-agent/internal/agent/executor"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type SearchConfig struct {
	Provider string `toml:"provider,omitempty"`
	APIKey   string `toml:"api_key,omitempty"`
}

type EnvironmentBridgeConfig struct {
	Enabled         bool     `toml:"-"` // Only set via CLI, not config file
	Endpoint        string   `toml:"-"` // Only set via CLI, not config file
	Tools           []string `toml:"-"` // Only set via CLI, not config file
	BenchmarkTaskID string   `toml:"-"` // Only set via CLI, not config file
}

type BenchmarkConfig struct {
	Token string `toml:"-"` // Only set via benchmark runner CLI flags, never from config file
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

// ExplicitStoragePath returns the storage path only when it opts out of
// storage-manager routing, else "". DefaultConfig seeds StoragePath with the
// built-in default and the config portal persists resolved values, so a stored
// "/userdata/audio" is indistinguishable from "never configured" — and it names
// the same directory the storage manager already uses as the audio eMMC tier.
// Treat it as unset; only a non-default path pins recordings to a fixed
// directory.
func (c AudioArchiveConfig) ExplicitStoragePath() string {
	path := strings.TrimSpace(c.StoragePath)
	if path == defaultAudioArchiveStoragePath {
		return ""
	}
	return path
}

// StorageConfig tunes the optional microSD data store managed by
// StorageManager. The storage mode itself is not configurable: a usable
// card enables the SD migration tier; otherwise governed data remains on eMMC.
type StorageConfig struct {
	MountPoint    string `toml:"mount_point,omitempty"`
	Device        string `toml:"device,omitempty"`
	MinCardFreeMB int    `toml:"min_card_free_mb,omitempty"`
	// Watermarks for the background migration of older governed data from
	// eMMC to the SD card: migration starts when eMMC free space drops
	// below MigrateStartFreePct and stops once it reaches MigrateStopFreePct.
	MigrateStartFreePct int `toml:"migrate_start_free_pct,omitempty"`
	MigrateStopFreePct  int `toml:"migrate_stop_free_pct,omitempty"`

	MonitorEnabled       bool                      `toml:"monitor_enabled"`
	RootPath             string                    `toml:"root_path,omitempty"`
	CheckIntervalSeconds int                       `toml:"check_interval_seconds,omitempty"`
	WarningThresholdMB   uint64                    `toml:"warning_threshold_mb,omitempty"`
	CriticalThresholdMB  uint64                    `toml:"critical_threshold_mb,omitempty"`
	EmergencyThresholdMB uint64                    `toml:"emergency_threshold_mb,omitempty"`
	RecoveryHysteresisMB uint64                    `toml:"recovery_hysteresis_mb,omitempty"`
	DegradedMode         StorageDegradedModeConfig `toml:"degraded_mode,omitempty"`
	Cleanup              StorageCleanupConfig      `toml:"cleanup,omitempty"`
}

func (c StorageConfig) MonitorConfig() StorageMonitorConfig {
	return StorageMonitorConfig{
		Enabled:              c.MonitorEnabled,
		RootPath:             c.RootPath,
		CheckIntervalSeconds: c.CheckIntervalSeconds,
		WarningThresholdMB:   c.WarningThresholdMB,
		CriticalThresholdMB:  c.CriticalThresholdMB,
		EmergencyThresholdMB: c.EmergencyThresholdMB,
		RecoveryHysteresisMB: c.RecoveryHysteresisMB,
		DegradedMode:         c.DegradedMode,
		Cleanup:              c.Cleanup,
	}
}

// MountPointOrDefault returns MountPoint if non-empty, else "/mnt/sdcard".
func (c StorageConfig) MountPointOrDefault() string {
	path := strings.TrimSpace(c.MountPoint)
	if path == "" {
		return defaultStorageMountPoint
	}
	return path
}

// DeviceOrDefault returns Device if non-empty, else "mmcblk2".
func (c StorageConfig) DeviceOrDefault() string {
	dev := strings.TrimSpace(c.Device)
	if dev == "" {
		return defaultStorageDevice
	}
	return dev
}

// MinCardFreeMBOrDefault returns MinCardFreeMB if positive, else 64.
func (c StorageConfig) MinCardFreeMBOrDefault() int {
	if c.MinCardFreeMB <= 0 {
		return defaultStorageMinCardFreeMB
	}
	return c.MinCardFreeMB
}

// MigrateWatermarksOrDefault returns the (start, stop) free-space
// percentages for eMMC→SD migration. Defaults: start below 10%, stop at
// 50%. An inverted or out-of-range pair falls back to the defaults so a
// bad config cannot disable or loop the migrator.
func (c StorageConfig) MigrateWatermarksOrDefault() (int, int) {
	start, stop := c.MigrateStartFreePct, c.MigrateStopFreePct
	if start <= 0 {
		start = defaultStorageMigrateStartFreePct
	}
	if stop <= 0 {
		stop = defaultStorageMigrateStopFreePct
	}
	if start >= stop || stop > 95 {
		return defaultStorageMigrateStartFreePct, defaultStorageMigrateStopFreePct
	}
	return start, stop
}

// LogConfig controls local runtime log retention.
type LogConfig struct {
	LLMHTTPRetentionDays int `toml:"llm_http_retention_days,omitempty"`
}

// LLMHTTPRetentionDaysOrDefault returns LLMHTTPRetentionDays if positive, else 7.
func (c LogConfig) LLMHTTPRetentionDaysOrDefault() int {
	if c.LLMHTTPRetentionDays <= 0 {
		return defaultLLMHTTPLogRetentionDays
	}
	return c.LLMHTTPRetentionDays
}

// OTAConfig controls OTA update behavior.
type OTAConfig struct {
	GitHubProxyURL string `toml:"github_proxy_url,omitempty"`
}

// GitHubProxyURLOrDefault returns GitHubProxyURL if non-empty and valid, else empty string.
func (c OTAConfig) GitHubProxyURLOrDefault() string {
	return strings.TrimSpace(c.GitHubProxyURL)
}

// Validate checks if the GitHub proxy URL is valid when configured
func (c OTAConfig) Validate() error {
	proxyURL := strings.TrimSpace(c.GitHubProxyURL)
	if proxyURL == "" {
		return nil // Empty is valid (no proxy)
	}

	// Parse the URL
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("ota.github_proxy_url: invalid URL: %w", err)
	}

	// Must have a scheme
	if parsed.Scheme == "" {
		return fmt.Errorf("ota.github_proxy_url: must be an absolute URL with scheme (e.g., https://example.com/)")
	}

	// Must be HTTPS
	if parsed.Scheme != "https" {
		return fmt.Errorf("ota.github_proxy_url: must use https, got %s", parsed.Scheme)
	}

	// Must have a host
	if parsed.Host == "" {
		return fmt.Errorf("ota.github_proxy_url: missing host")
	}

	return nil
}

// ModelProvider defines a model service provider configuration.
type ModelProvider struct {
	Type    string `toml:"type"` // Provider type: openai, kimi, ollama, etc.
	APIKey  string `toml:"api_key,omitempty"`
	BaseURL string `toml:"base_url,omitempty"`
}

type Config struct {
	ModelProviders             map[string]ModelProvider `toml:"model_providers,omitempty"` // Named model provider configurations
	TTSProviders               map[string]TTSProvider   `toml:"tts_providers,omitempty"`   // Named TTS provider configurations
	STTProviders               map[string]STTProvider   `toml:"stt_providers,omitempty"`   // Named STT provider configurations
	Model                      ModelConfig              `toml:"model"`
	TTS                        TTSConfig                `toml:"tts,omitempty"`
	STT                        STTConfig                `toml:"stt,omitempty"`
	HID                        HIDConfig                `toml:"hid"`
	Device                     DeviceConfig             `toml:"device,omitempty"`
	Audio                      AudioConfig              `toml:"audio,omitempty"`
	AudioArchive               AudioArchiveConfig       `toml:"audio_archive,omitempty"`
	Storage                    StorageConfig            `toml:"storage,omitempty"`
	VoiceNotifications         VoiceNotificationsConfig `toml:"voice_notifications,omitempty"`
	Log                        LogConfig                `toml:"log,omitempty"`
	OTA                        OTAConfig                `toml:"ota,omitempty"`
	Search                     SearchConfig             `toml:"search,omitempty"`
	EnvironmentBridge          EnvironmentBridgeConfig  `toml:"-"` // Only set via CLI flags, never from config file
	Benchmark                  BenchmarkConfig          `toml:"-"` // Only set via CLI flags, never from config file
	LiveActivity               LiveActivityConfig       `toml:"live_activity,omitempty"`
	Locale                     string                   `toml:"locale,omitempty"`
	Instruction                string                   `toml:"custom_instruction,omitempty"`
	AdditionalPrompt           string                   `toml:"additional_prompt,omitempty"`
	InputMode                  string                   `toml:"input_mode,omitempty"`   // "text" or "stt"
	TriggerMode                string                   `toml:"trigger_mode,omitempty"` // "manual", "wakeup"
	VADBackend                 string                   `toml:"vad_backend,omitempty"`  // "rknn", "cpu"
	VADModelPath               string                   `toml:"vad_model_path,omitempty"`
	VADHelperPath              string                   `toml:"vad_helper_path,omitempty"`
	VADSpeechThreshold         float64                  `toml:"vad_speech_threshold,omitempty"`
	SilenceMs                  int                      `toml:"silence_ms,omitempty"`
	MinSpeechMs                int                      `toml:"min_speech_ms,omitempty"`
	VoiceFollowupEnabled       *bool                    `toml:"voice_followup_enabled,omitempty"`
	VoiceFollowupTimeoutMs     int                      `toml:"voice_followup_timeout_ms,omitempty"`
	VoiceFirstTurnTimeoutMs    int                      `toml:"voice_first_turn_timeout_ms,omitempty"`
	VoiceMaxTurns              int                      `toml:"voice_max_turns,omitempty"`
	VoiceInterruptOnWakeup     *bool                    `toml:"voice_interrupt_on_wakeup,omitempty"`
	VoiceStreamingTTSEnabled   *bool                    `toml:"voice_streaming_tts_enabled,omitempty"`
	VoiceToolCallSpeech        *bool                    `toml:"voice_tool_call_speech,omitempty"`
	VoiceProgressSpeechEnabled *bool                    `toml:"voice_progress_speech_enabled,omitempty"`
	VoiceMaxResponseTokens     int                      `toml:"voice_max_response_tokens,omitempty"`
	LoadAllTools               bool                     `toml:"load_all_tools,omitempty"`
	MaxIterations              int                      `toml:"max_iterations,omitempty"`
	TerminationPolicy          TerminationPolicyConfig  `toml:"termination_policy,omitempty"`
	ForceSimpleLoop            bool                     `toml:"-"`
	ScreenshotKeepN            int                      `toml:"screenshot_keep_n,omitempty"`
	ScreenshotPruneInterval    int                      `toml:"screenshot_prune_interval,omitempty"`
	ScreenStableTimeoutMs      int                      `toml:"screen_stable_timeout_ms,omitempty"`
	ScreenStableMs             int                      `toml:"screen_stable_ms,omitempty"`
	ScreenStableDiffThreshold  float64                  `toml:"screen_stable_diff_threshold,omitempty"`
	DefaultPlatform            string                   `toml:"default_platform,omitempty"` // "ios", "android", "mac"
	SkillsDirs                 []string                 `toml:"skills_dirs"`
	BundledSkillsDir           string                   `toml:"bundled_skills_dir,omitempty"`
	SkillMergeModel            SkillMergeModel          `toml:"-"`
	Telemetry                  TelemetryConfig          `toml:"telemetry,omitempty"`
	ConfigDir                  string                   `toml:"-"`
}

func (c Config) TerminationPolicyOrDefault() TerminationPolicyConfig {
	return c.TerminationPolicy.resolved()
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

type LiveActivityConfig struct {
	Enabled *bool `toml:"enabled,omitempty"`
}

type TTSConfig struct {
	Provider    string  `toml:"provider"` // "minimax", "minimax-cn", "fish-audio", "alicloud", "volcengine"
	APIKey      string  `toml:"api_key,omitempty"`
	Model       string  `toml:"model,omitempty"`
	VoiceID     string  `toml:"voice_id,omitempty"`
	Emotion     string  `toml:"emotion,omitempty"`
	Speed       float64 `toml:"speed,omitempty"`
	ReferenceID string  `toml:"reference_id,omitempty"` // Fish Audio voice reference ID

	// ActiveProviderRecord is runtime-only: it records which
	// [tts_providers.<name>] record the Provider field referred to before resolution
	// replaced the reference with the provider TYPE.
	//
	// Without it the two resolution steps disagree. Load time resolves the
	// reference and rewrites Provider to the type; speak time re-resolves by
	// type, and with two records of the same type (two accounts of one service,
	// the whole point of named records) the type scan can return the other one.
	// The result is speaking with the wrong account's key while the config page
	// shows the right record selected.
	ActiveProviderRecord string `toml:"-"`
}

type STTConfig struct {
	Provider        string `toml:"provider"` // "openai-whisper", "tencent-asr" (legacy: "openai", "tencent", "tencent_asr")
	APIKey          string `toml:"api_key,omitempty"`
	Model           string `toml:"model,omitempty"`
	BaseURL         string `toml:"base_url,omitempty"`
	Language        string `toml:"language,omitempty"` // "zh" (Chinese) or "en" (English)
	AppID           string `toml:"app_id,omitempty"`
	SecretID        string `toml:"secret_id,omitempty"`
	SecretKey       string `toml:"secret_key,omitempty"`
	Region          string `toml:"region,omitempty"`
	EngineModelType string `toml:"engine_model_type,omitempty"` // Deprecated: use Language instead
}

type AudioConfig struct {
	Socket          string `toml:"socket,omitempty"`
	SampleRate      int    `toml:"sample_rate,omitempty"`
	Channels        int    `toml:"channels,omitempty"`
	BitWidth        int    `toml:"bit_width,omitempty"`
	PlaybackBackend string `toml:"playback_backend,omitempty"`
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

const (
	AudioPlaybackBackendAuto         = "auto"
	AudioPlaybackBackendAudioService = "audio_service"
	AudioPlaybackBackendLocal        = "local"
)

func normalizeAudioPlaybackBackend(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", AudioPlaybackBackendAuto:
		return AudioPlaybackBackendAuto, nil
	case AudioPlaybackBackendAudioService, "audio-service", "audioservice":
		return AudioPlaybackBackendAudioService, nil
	case AudioPlaybackBackendLocal, "pc", "desktop":
		return AudioPlaybackBackendLocal, nil
	default:
		return "", fmt.Errorf("invalid audio.playback_backend: %s (expected auto, audio_service, or local)", value)
	}
}

func (a AudioConfig) PlaybackBackendOrDefault() string {
	backend, err := normalizeAudioPlaybackBackend(a.PlaybackBackend)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(a.PlaybackBackend))
	}
	return backend
}

func (c Config) AudioPlaybackBackendOrDefault() string {
	backend, err := normalizeAudioPlaybackBackend(c.Audio.PlaybackBackend)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(c.Audio.PlaybackBackend))
	}
	if backend != AudioPlaybackBackendAuto {
		return backend
	}
	if c.HID.InputBackendADB() || c.EnvironmentBridge.Enabled {
		return AudioPlaybackBackendLocal
	}
	return AudioPlaybackBackendAudioService
}

type HIDConfig struct {
	KeyboardDevice        string `toml:"keyboard_device,omitempty"`
	KeyboardLayout        string `toml:"keyboard_layout,omitempty"`
	MouseDevice           string `toml:"mouse_device,omitempty"`
	AndroidKeyboardDevice string `toml:"android_keyboard_device,omitempty"`
	TouchscreenDevice     string `toml:"touchscreen_device,omitempty"`
	FrameSocket           string `toml:"frame_socket,omitempty"`
	// PointerMode selects the default gesture surface and hid.usb2 key profile.
	// Android uses the touchscreen surface while still exposing MouseDevice as
	// a separate absolute mouse with a visible cursor.
	PointerMode string `toml:"pointer_mode,omitempty"`
	// InputBackend selects the low-level input path for keyboard/touch tools:
	// "hid" writes USB HID reports, "adb" sends Android adb shell input commands.
	InputBackend string `toml:"input_backend,omitempty"`
}

type DeviceConfig struct {
	Backend    string `toml:"backend,omitempty"`
	DeviceType string `toml:"device_type,omitempty"`
}

func (d DeviceConfig) BackendOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(d.Backend)) {
	case "", "hdmi", "hardware":
		return "hdmi"
	default:
		return strings.ToLower(strings.TrimSpace(d.Backend))
	}
}

func normalizeDeviceType(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ios":
		return "iOS", true
	case "android":
		return "Android", true
	case "macos", "mac", "darwin":
		return "macOS", true
	case "windows", "win":
		return "windows", true
	case "linux":
		return "linux", true
	default:
		return "", false
	}
}

func (d DeviceConfig) DeviceTypeOrDefault() string {
	if deviceType, ok := normalizeDeviceType(d.DeviceType); ok {
		return deviceType
	}
	return defaultDeviceType
}

func (d DeviceConfig) PlatformOrDefault() string {
	return deviceTypePlatform(d.DeviceTypeOrDefault())
}

func (d DeviceConfig) PointerModeOrDefault() string {
	if d.PlatformOrDefault() == "android" {
		return "touchscreen"
	}
	return "absolute"
}

func deviceTypePlatform(deviceType string) string {
	switch normalized, _ := normalizeDeviceType(deviceType); normalized {
	case "iOS":
		return "ios"
	case "Android":
		return "android"
	case "macOS":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return "ios"
	}
}

func deviceTypeFromPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios", "iphone", "ipad", "ipados":
		return "iOS"
	case "android":
		return "Android"
	case "mac", "macos", "darwin":
		return "macOS"
	case "windows", "win":
		return "windows"
	case "linux":
		return "linux"
	default:
		return ""
	}
}

func inferredDeviceTypeFromLegacyConfig(hid HIDConfig, defaultPlatform string) string {
	if deviceType := deviceTypeFromPlatform(defaultPlatform); deviceType != "" {
		return deviceType
	}
	if strings.ToLower(strings.TrimSpace(hid.PointerMode)) == "touchscreen" {
		return "Android"
	}
	return defaultDeviceType
}

func (c Config) DeviceTypeOrDefault() string {
	if strings.TrimSpace(c.Device.DeviceType) != "" {
		return c.Device.DeviceTypeOrDefault()
	}
	return inferredDeviceTypeFromLegacyConfig(c.HID, c.DefaultPlatform)
}

func (c Config) DevicePlatformOrDefault() string {
	return deviceTypePlatform(c.DeviceTypeOrDefault())
}

func (c Config) PointerModeOrDefault() string {
	return DeviceConfig{DeviceType: c.DeviceTypeOrDefault()}.PointerModeOrDefault()
}

func (c Config) HIDConfigForDevice() HIDConfig {
	hid := c.HID
	hid.PointerMode = c.PointerModeOrDefault()
	return hid
}

// OverrideDeviceType applies a process-local device type override and derives
// dependent device settings from the canonical value.
func (c *Config) OverrideDeviceType(value string) error {
	if c == nil {
		return errors.New("cannot override device type on nil config")
	}
	deviceType, ok := normalizeDeviceType(value)
	if !ok {
		return fmt.Errorf("invalid device type override: %s (expected iOS, Android, macOS, windows, or linux)", value)
	}
	c.Device.DeviceType = deviceType
	c.HID.PointerMode = DeviceConfig{DeviceType: deviceType}.PointerModeOrDefault()
	return nil
}

func (h HIDConfig) KeyboardDeviceOrDefault() string {
	if h.KeyboardDevice != "" {
		return h.KeyboardDevice
	}
	return defaultKeyboardDevice
}

func (h HIDConfig) KeyboardLayoutOrDefault() string {
	layout, _ := normalizeKeyboardLayout(h.KeyboardLayout)
	return layout
}

func (h HIDConfig) MouseDeviceOrDefault() string {
	if h.MouseDevice != "" {
		return h.MouseDevice
	}
	return defaultMouseDevice
}

func (h HIDConfig) AndroidKeyboardDeviceOrDefault() string {
	if h.AndroidKeyboardDevice != "" {
		return h.AndroidKeyboardDevice
	}
	return defaultAndroidKeyboardDevice
}

func (h HIDConfig) TouchscreenDeviceOrDefault() string {
	if h.TouchscreenDevice != "" {
		return h.TouchscreenDevice
	}
	return defaultTouchscreenDevice
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

func (h HIDConfig) InputBackendOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(h.InputBackend)) {
	case "adb":
		return "adb"
	default:
		return "hid"
	}
}

func (h HIDConfig) InputBackendADB() bool {
	return h.InputBackendOrDefault() == "adb"
}

type ModelConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	BaseURL  string `toml:"-"`
	APIKey   string `toml:"api_key,omitempty"`
	// Temperature is a pointer so nil (unset) is distinct from an explicit 0.0.
	// Unset means the effective value is resolved at runtime from model metadata
	// (see applyModelTemperatureDefault); an explicit value, including 0, is
	// always honored and sent to the provider.
	Temperature       *float64 `toml:"temperature,omitempty"`
	MaxResponseTokens int      `toml:"max_response_tokens,omitempty"`
	LogRawHTTP        bool     `toml:"log_raw_http,omitempty"`
	ReasoningEffort   string   `toml:"reasoning_effort,omitempty"`
	// These override static model metadata; zero means use the registry/fallback.
	ContextWindow        int      `toml:"context_window,omitempty"`
	ModelMaxOutputTokens int      `toml:"model_max_output_tokens,omitempty"`
	Responses            []string `toml:"responses,omitempty"`
}

// AgentConfig is used internally by the runtime prompt builder.
type AgentConfig struct {
	Instruction      string
	AdditionalPrompt string
	Locale           string
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
	applyDeviceConfigDefaults(&cfg, metadata)

	// Upgrade the legacy voice shapes to named records. This must run after the
	// defaults pass above -- that pass zeroes [tts]/[stt] when the file declares
	// no provider, and migrating first would mint a record out of DefaultConfig
	// for a device that never configured voice at all.
	migrateLegacyVoiceProviders(&cfg, metadata)

	// Expand the voice provider references. Neither call fails: voice is
	// optional at runtime (a TTS init failure is a warning and the agent still
	// starts), so a stale reference must not stop the device from booting. The
	// config page runs Config.ValidateVoiceProviders on save for strict checks.
	resolveTTSProvider(&cfg)
	resolveSTTProvider(&cfg)

	// Apply provider references to model configurations. This must run before
	// the base_url whitelist below: until the reference is expanded, Provider
	// still holds a [model_providers] section name rather than a provider type, so
	// the whitelist would compare against the wrong value in both directions.
	if err := applyRuntimeModelProviders(&cfg); err != nil {
		return Config{}, err
	}

	// base_url belongs to [model_providers.*]. Drop provider-record values for
	// provider types whose model builders pin their own endpoint.
	clearNonAllowedModelBaseURL(&cfg.Model)

	applyRuntimeModelTemperatureDefaults(&cfg)
	applyRuntimeModelReasoningEffortDefaults(&cfg)
	applyRuntimeInstructionDefault(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyDeviceConfigDefaults(cfg *Config, metadata toml.MetaData) {
	if cfg == nil {
		return
	}
	deviceTypeConfigured := metadata.IsDefined("device", "device_type") && strings.TrimSpace(cfg.Device.DeviceType) != ""
	if !deviceTypeConfigured {
		cfg.Device.DeviceType = inferredDeviceTypeFromLegacyConfig(cfg.HID, cfg.DefaultPlatform)
	} else if deviceType, ok := normalizeDeviceType(cfg.Device.DeviceType); ok {
		cfg.Device.DeviceType = deviceType
	}
	cfg.HID.PointerMode = cfg.PointerModeOrDefault()
}

// applyRuntimeModelTemperatureDefaults resolves the sampling temperature for
// the model when the user has not set it. The default is sourced
// from the model's metadata (some models, e.g. Kimi K3, require a fixed
// temperature) and falls back to defaultModelTemperature. An explicit
// model.temperature always takes precedence. This is only called in
// LoadRuntimeConfig; LoadResolvedConfig (config editor) keeps temperature unset
// so the editor displays empty and saves without baking defaults into agent.toml.
func applyRuntimeModelTemperatureDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	applyModelTemperatureDefault(&cfg.Model)
}

func applyModelTemperatureDefault(m *ModelConfig) {
	if m == nil || m.Temperature != nil {
		return
	}
	if spec, ok := LookupModelSpec(m.Provider, m.Model); ok && spec.DefaultTemperature != nil {
		// Copy the value rather than aliasing the registry pointer.
		temp := *spec.DefaultTemperature
		m.Temperature = &temp
		return
	}
	temp := defaultModelTemperature
	m.Temperature = &temp
}

// applyRuntimeModelReasoningEffortDefaults resolves reasoning_effort for the
// model when the user has not set it (empty string). The default is
// sourced from the model's metadata; forced-reasoning models (e.g. Kimi K3)
// pin a lighter effort to keep streaming responsive. Unlike temperature there
// is no global fallback: unknown models stay in auto mode (empty). An explicit
// model.reasoning_effort always takes precedence. Like the temperature defaults,
// this only runs in LoadRuntimeConfig; LoadResolvedConfig (config editor) keeps
// reasoning_effort as-is so the editor does not bake a default into agent.toml.
func applyRuntimeModelReasoningEffortDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	applyModelReasoningEffortDefault(&cfg.Model)
}

func applyModelReasoningEffortDefault(m *ModelConfig) {
	if m == nil || strings.TrimSpace(m.ReasoningEffort) != "" {
		return
	}
	if spec, ok := LookupModelSpec(m.Provider, m.Model); ok && spec.DefaultReasoningEffort != nil {
		m.ReasoningEffort = *spec.DefaultReasoningEffort
	}
}

// applyRuntimeModelProviders resolves provider references in model configurations.
// If model.provider refers to a named provider in [model_providers], apply that provider's
// configuration. Otherwise, treat it as a direct provider type (backward compatibility).
func applyRuntimeModelProviders(cfg *Config) error {
	if cfg == nil {
		return nil
	}

	if err := resolveModelProvider(cfg, &cfg.Model); err != nil {
		return fmt.Errorf("model: %w", err)
	}

	return nil
}

// resolveModelProvider resolves a single model's provider reference.
func resolveModelProvider(cfg *Config, m *ModelConfig) error {
	if m == nil {
		return nil
	}

	providerRef := strings.TrimSpace(m.Provider)
	if providerRef == "" {
		return errors.New("provider is required")
	}

	// Check if this is a reference to a named provider in [model_providers]
	if cfg.ModelProviders != nil {
		if provider, exists := cfg.ModelProviders[providerRef]; exists {
			return applyProviderToModel(provider, providerRef, m)
		}
	}

	// Not a reference, treat as direct provider type (backward compatibility)
	return nil
}

// applyProviderToModel applies a provider configuration to a model config.
func applyProviderToModel(provider ModelProvider, originalRef string, m *ModelConfig) error {
	providerType := strings.TrimSpace(provider.Type)
	if providerType == "" {
		return fmt.Errorf("provider %q has no provider type specified", originalRef)
	}

	// Replace the reference with the actual provider type
	m.Provider = providerType

	// api_key may be overridden on [model]. base_url belongs to the selected
	// provider record so switching providers also switches the endpoint.
	if m.APIKey == "" && provider.APIKey != "" {
		m.APIKey = provider.APIKey
	}
	m.BaseURL = provider.BaseURL

	return nil
}

func applyRuntimeOptionalProviderDefaults(cfg *Config, metadata toml.MetaData) {
	if cfg == nil {
		return
	}

	if !metadata.IsDefined("tts", "provider") || strings.TrimSpace(cfg.TTS.Provider) == "" {
		cfg.TTS = TTSConfig{}
	} else {
		// Normalizing lowercases, so it must not touch a [tts_providers] record
		// name: record names come from the config page and may carry capitals,
		// and a lowercased name would stop matching its own record. Bare
		// provider types still normalize, which is what folds the minimax-ws
		// alias. resolveTTSProvider normalizes the resolved type afterwards.
		if _, isRef := cfg.TTSProviders[strings.TrimSpace(cfg.TTS.Provider)]; !isRef {
			cfg.TTS.Provider = normalizeTTSProvider(cfg.TTS.Provider)
		}
		if cfg.TTS.Provider != defaultTTSProvider {
			clearDefaultTTSProviderFields(cfg, metadata)
		}
	}
	if !metadata.IsDefined("stt", "provider") || strings.TrimSpace(cfg.STT.Provider) == "" {
		cfg.STT = STTConfig{}
	} else if !usesDefaultSTTModel(cfg.STT.Provider) && !metadata.IsDefined("stt", "model") {
		cfg.STT.Model = ""
	}
}

func clearNonAllowedModelBaseURL(m *ModelConfig) {
	if m == nil {
		return
	}
	definition, ok := lookupModelProviderDefinition(m.Provider)
	if !ok || !definition.allowsCustomBaseURL {
		m.BaseURL = ""
	}
}

func normalizeTTSProvider(provider string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(provider)); normalized {
	case "minimax-ws":
		return defaultTTSProvider
	default:
		return normalized
	}
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
	canonicalProvider, ok := canonicalSTTProviderType(provider)
	return ok && canonicalProvider == defaultSTTProvider
}

// LoadResolvedConfig loads the TOML config file at path over the canonical
// defaults and returns the effective values used by config-editing surfaces.
// The path must identify a file, not a config directory. Missing TOML files are
// treated as "all defaults" so first-boot config pages can render before
// agent.toml has been created.
func LoadResolvedConfig(path string) (Config, error) {
	exists, err := inspectConfigFilePath(path)
	if err != nil {
		return Config{}, err
	}

	cfg := DefaultConfig()
	var metadata toml.MetaData
	if exists {
		if metadata, err = decodeConfigFile(path, &cfg); err != nil {
			return Config{}, err
		}
		applyDeviceConfigDefaults(&cfg, metadata)
	} else {
		cfg.HID.PointerMode = cfg.PointerModeOrDefault()
	}

	applyRuntimeInstructionDefault(&cfg)

	// Upgrade flat voice credentials to named records. This backs
	// `agent config --format=json`, which is what the config page reads through,
	// so it has to run here as well as in LoadRuntimeConfig: without it a flat
	// config reaches the page as flat fields with no record, leaving the key
	// invisible and un-editable.
	//
	// References are deliberately NOT resolved here. The page edits the
	// reference, so it must come back as the name it wrote.
	migrateLegacyVoiceProviders(&cfg, metadata)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func inspectConfigFilePath(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("config path is required")
	}
	if !strings.HasSuffix(path, ".toml") {
		return false, fmt.Errorf("config path must end with .toml: %q", path)
	}

	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read config: %w", err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return false, fmt.Errorf("resolve config symlink target %q: %w", path, err)
		}
	}

	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return false, fmt.Errorf("config path must be a regular file, got directory: %q", path)
		}
		return false, fmt.Errorf("config path must be a regular file: %q", path)
	}
	return true, nil
}

func applyRuntimeInstructionDefault(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Instruction) == "" {
		cfg.Instruction = defaultInstruction
	}
}

func decodeConfigFile(path string, cfg *Config) (toml.MetaData, error) {
	var metadata toml.MetaData

	// Determine format by file extension
	if strings.HasSuffix(path, ".toml") {
		var err error
		if metadata, err = toml.DecodeFile(path, cfg); err != nil {
			return toml.MetaData{}, fmt.Errorf("decode TOML config: %w", err)
		}
		if metadata.IsDefined("providers") {
			return toml.MetaData{}, errors.New("[providers] is unsupported; use [model_providers]")
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
	if !needsModel {
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
	// Validate providers. Iterate in sorted order so a config with several
	// broken sections reports the same error every run.
	providerNames := make([]string, 0, len(c.ModelProviders))
	for name := range c.ModelProviders {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		if err := validateModelProvider(name, c.ModelProviders[name]); err != nil {
			return err
		}
	}

	if err := validateModelProviderRef("model", c.ModelProviders, c.Model.Provider); err != nil {
		return err
	}

	locale := strings.TrimSpace(c.Locale)
	switch locale {
	case "", localeSimplifiedChinese, localeEnglishUS:
	default:
		return fmt.Errorf("invalid locale: %s (expected zh-CN or en-US)", c.Locale)
	}

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
	case "hdmi":
	default:
		return fmt.Errorf("invalid device.backend: %s (expected hdmi)", c.Device.Backend)
	}
	if _, ok := normalizeDeviceType(c.Device.DeviceType); !ok {
		return fmt.Errorf("invalid device.device_type: %s (expected iOS, Android, macOS, windows, or linux)", c.Device.DeviceType)
	}
	if _, err := normalizeAudioPlaybackBackend(c.Audio.PlaybackBackend); err != nil {
		return err
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
	// Validate input_mode
	if strings.TrimSpace(c.InputMode) != "" {
		mode := strings.ToLower(strings.TrimSpace(c.InputMode))
		switch mode {
		case "text":
		case "stt":
			if strings.TrimSpace(c.STT.Provider) == "" {
				return errors.New("stt.provider is required when input_mode=stt")
			}
		case "audio":
			return fmt.Errorf("invalid input_mode: %s (audio mode has been removed; use stt instead)", c.InputMode)
		default:
			return fmt.Errorf("invalid input_mode: %s (expected text or stt)", c.InputMode)
		}

		// Validate TTS config if not in text mode
		if mode == "stt" && strings.TrimSpace(c.TTS.Provider) == "" {
			return errors.New("tts.provider is required when input_mode=stt")
		}

		if mode == "stt" {
			if c.Audio.SampleRate != 0 && c.Audio.SampleRate < 8000 {
				return fmt.Errorf("audio.sample_rate must be at least 8000 when set, got %d", c.Audio.SampleRate)
			}
			if c.Audio.Channels != 0 && c.Audio.Channels != 1 {
				return fmt.Errorf("audio.channels must be 1 when input_mode=stt, got %d", c.Audio.Channels)
			}
			if c.Audio.BitWidth != 0 && c.Audio.BitWidth != 16 {
				return fmt.Errorf("audio.bit_width must be 16 when input_mode=stt, got %d", c.Audio.BitWidth)
			}
		}
	}

	if strings.TrimSpace(c.TriggerMode) != "" {
		triggerMode := strings.ToLower(strings.TrimSpace(c.TriggerMode))
		if triggerMode != "manual" && triggerMode != "wakeup" {
			return fmt.Errorf("invalid trigger_mode: %s (expected manual or wakeup)", c.TriggerMode)
		}
		if triggerMode == "wakeup" && c.InputModeOrDefault() != "stt" {
			return fmt.Errorf("incompatible trigger_mode %q with input_mode %q: wakeup requires input_mode stt", c.TriggerMode, c.InputMode)
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
	if c.VoiceNotifications.MaxPending < 0 {
		return fmt.Errorf("voice_notifications.max_pending must be >= 0, got %d", c.VoiceNotifications.MaxPending)
	}
	if c.VoiceNotifications.ResponseTail.MaxItems < 0 || c.VoiceNotifications.ResponseTail.MaxItems > 1 {
		return fmt.Errorf("voice_notifications.response_tail.max_items must be 0 or 1, got %d", c.VoiceNotifications.ResponseTail.MaxItems)
	}
	if c.VoiceNotifications.ResponseTail.MaxTextChars < 0 {
		return fmt.Errorf("voice_notifications.response_tail.max_text_chars must be >= 0, got %d", c.VoiceNotifications.ResponseTail.MaxTextChars)
	}
	if c.VoiceNotifications.Expiration.DefaultTTLSeconds < 0 {
		return fmt.Errorf("voice_notifications.expiration.default_ttl_seconds must be >= 0, got %d", c.VoiceNotifications.Expiration.DefaultTTLSeconds)
	}
	for code, seconds := range c.VoiceNotifications.Expiration.CodeTTLSeconds {
		if strings.TrimSpace(code) == "" {
			return errors.New("voice_notifications.expiration.code_ttl_seconds contains an empty code")
		}
		if seconds < 0 {
			return fmt.Errorf("voice_notifications.expiration.code_ttl_seconds.%s must be >= 0, got %d", code, seconds)
		}
	}
	if c.Log.LLMHTTPRetentionDays < 0 {
		return fmt.Errorf("log.llm_http_retention_days must be >= 0, got %d", c.Log.LLMHTTPRetentionDays)
	}
	if err := c.Storage.MonitorConfig().Validate(); err != nil {
		return err
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
	if err := c.TerminationPolicy.Validate(); err != nil {
		return err
	}

	if _, ok := normalizeKeyboardLayout(c.HID.KeyboardLayout); !ok {
		return fmt.Errorf("invalid hid.keyboard_layout: %s (expected %s)", c.HID.KeyboardLayout, keyboardLayoutValuesText())
	}
	switch strings.ToLower(strings.TrimSpace(c.HID.PointerMode)) {
	case "", "absolute", "touchscreen":
	default:
		return fmt.Errorf("invalid hid.pointer_mode: %s (expected absolute or touchscreen)", c.HID.PointerMode)
	}
	switch strings.ToLower(strings.TrimSpace(c.HID.InputBackend)) {
	case "", "hid", "adb":
	default:
		return fmt.Errorf("invalid hid.input_backend: %s (expected hid or adb)", c.HID.InputBackend)
	}

	if err := c.Telemetry.Validate(); err != nil {
		return err
	}
	if err := c.LiveActivity.Validate(); err != nil {
		return err
	}
	if err := c.OTA.Validate(); err != nil {
		return err
	}

	return nil
}

// isKnownProviderType reports whether the value names a built-in model
// provider type (as opposed to a [model_providers] section name).
func isKnownProviderType(providerType string) bool {
	_, ok := lookupModelProviderDefinition(providerType)
	return ok
}

// validateModelProvider validates a single model provider configuration.
func validateModelProvider(name string, p ModelProvider) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("provider name cannot be empty")
	}

	providerType := strings.TrimSpace(p.Type)
	if providerType == "" {
		return fmt.Errorf("model_providers.%s: provider type is required", name)
	}

	if !isKnownProviderType(providerType) {
		return fmt.Errorf("model_providers.%s: unsupported provider type %q", name, providerType)
	}

	return nil
}

// validateModelProviderRef checks that a model's provider field resolves: it
// must either name a [model_providers] section or be a built-in provider type. A
// typo, or a reference left behind after the section was deleted, otherwise
// passes validation and only fails later when the model client is built.
// section is the config section being validated, e.g. "model".
func validateModelProviderRef(section string, providers map[string]ModelProvider, provider string) error {
	ref := strings.TrimSpace(provider)
	if ref == "" {
		return nil
	}
	if _, exists := providers[ref]; exists {
		return nil
	}
	if isKnownProviderType(ref) {
		return nil
	}
	if len(providers) == 0 {
		return fmt.Errorf("%s.provider: unknown provider %q", section, ref)
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Errorf("%s.provider: unknown provider %q (not a provider type, and no [model_providers.%s] section; configured: %s)",
		section, ref, ref, strings.Join(names, ", "))
}

func (c Config) LocaleOrDefault() string {
	locale := strings.TrimSpace(c.Locale)
	if locale == "" {
		return defaultLocale
	}
	return locale
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

func (l LiveActivityConfig) Validate() error {
	return nil
}

func (l LiveActivityConfig) EnabledOrDefault() bool {
	if l.Enabled != nil {
		return *l.Enabled
	}
	return true
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

func (c Config) VoiceFollowupEnabledOrDefault() bool {
	if c.VoiceFollowupEnabled != nil {
		return *c.VoiceFollowupEnabled
	}
	return false
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

func (c Config) VoiceProgressSpeechEnabledOrDefault() bool {
	if c.VoiceProgressSpeechEnabled != nil {
		return *c.VoiceProgressSpeechEnabled
	}
	return true
}

func (c Config) VoiceMaxResponseTokensOrDefault() int {
	if c.VoiceMaxResponseTokens > 0 {
		return c.VoiceMaxResponseTokens
	}
	return defaultVoiceMaxResponseTokens
}

func (c Config) ScreenshotPruningOrDefault() executor.ScreenshotPruningConfig {
	return executor.ScreenshotPruningConfig{
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
