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

func (s SearchConfig) ProviderOrDefault() string {
	if strings.TrimSpace(s.Provider) != "" {
		return strings.ToLower(strings.TrimSpace(s.Provider))
	}
	return "duckduckgo"
}

type Config struct {
	Model                    ModelConfig  `toml:"model"`
	ModelText                ModelConfig  `toml:"model_text,omitempty"` // Override for STT-then-text mode
	TTS                      TTSConfig    `toml:"tts,omitempty"`
	STT                      STTConfig    `toml:"stt,omitempty"`
	HID                      HIDConfig    `toml:"hid"`
	Audio                    AudioConfig  `toml:"audio,omitempty"`
	Proxy                    ProxyConfig  `toml:"proxy,omitempty"`
	Search                   SearchConfig `toml:"search,omitempty"`
	Instruction              string       `toml:"instruction"`
	AdditionalPrompt         string       `toml:"additional_prompt,omitempty"`
	InputMode                string       `toml:"input_mode,omitempty"`   // "text", "audio", "stt"
	TriggerMode              string       `toml:"trigger_mode,omitempty"` // "manual", "wakeup"
	VADBackend               string       `toml:"vad_backend,omitempty"`  // "rknn", "cpu"
	VADModelPath             string       `toml:"vad_model_path,omitempty"`
	VADHelperPath            string       `toml:"vad_helper_path,omitempty"`
	VADSpeechThreshold       float64      `toml:"vad_speech_threshold,omitempty"`
	SilenceMs                int          `toml:"silence_ms,omitempty"`
	MinSpeechMs              int          `toml:"min_speech_ms,omitempty"`
	VoiceSessionEnabled      *bool        `toml:"voice_session_enabled,omitempty"`
	VoiceFollowupTimeoutMs   int          `toml:"voice_followup_timeout_ms,omitempty"`
	VoiceFirstTurnTimeoutMs  int          `toml:"voice_first_turn_timeout_ms,omitempty"`
	VoiceMaxTurns            int          `toml:"voice_max_turns,omitempty"`
	VoiceInterruptOnWakeup   *bool        `toml:"voice_interrupt_on_wakeup,omitempty"`
	VoiceStreamingTTSEnabled *bool        `toml:"voice_streaming_tts_enabled,omitempty"`
	VoiceToolCallSpeech      *bool        `toml:"voice_tool_call_speech,omitempty"`
	VoiceMaxResponseTokens   int          `toml:"voice_max_response_tokens,omitempty"`
	MaxIterations            int          `toml:"max_iterations,omitempty"`
	SkillsDirs               []string     `toml:"skills_dirs"`
	ConfigDir                string       `toml:"-"`
}

type TTSConfig struct {
	Provider string  `toml:"provider"` // "minimax"
	APIKey   string  `toml:"api_key,omitempty"`
	Model    string  `toml:"model,omitempty"`
	VoiceID  string  `toml:"voice_id,omitempty"`
	Emotion  string  `toml:"emotion,omitempty"`
	Speed    float64 `toml:"speed,omitempty"`
}

type STTConfig struct {
	Provider        string `toml:"provider"` // "openai", "openai-whisper", "openrouter", "tencent"
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

type ProxyConfig struct {
	HTTPProxy  string `toml:"http_proxy,omitempty"`
	HTTPSProxy string `toml:"https_proxy,omitempty"`
	AllProxy   string `toml:"all_proxy,omitempty"`
	NoProxy    string `toml:"no_proxy,omitempty"`
}

const DefaultNoProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"

func (p ProxyConfig) HasProxyURL() bool {
	return strings.TrimSpace(p.HTTPProxy) != "" ||
		strings.TrimSpace(p.HTTPSProxy) != "" ||
		strings.TrimSpace(p.AllProxy) != ""
}

func (p ProxyConfig) WithDefaults() ProxyConfig {
	if strings.TrimSpace(p.NoProxy) == "" {
		p.NoProxy = DefaultNoProxy
	}
	return p
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
	return "/run/audio_service/audio_service.sock"
}

func (a AudioConfig) SampleRateOrDefault() int {
	if a.SampleRate > 0 {
		return a.SampleRate
	}
	return 16000
}

func (a AudioConfig) ChannelsOrDefault() int {
	if a.Channels > 0 {
		return a.Channels
	}
	return 1
}

func (a AudioConfig) BitWidthOrDefault() int {
	if a.BitWidth > 0 {
		return a.BitWidth
	}
	return 16
}

type HIDConfig struct {
	KeyboardDevice string `toml:"keyboard_device,omitempty"`
	MouseDevice    string `toml:"mouse_device,omitempty"`
	FrameSocket    string `toml:"frame_socket,omitempty"`
}

func (h HIDConfig) KeyboardDeviceOrDefault() string {
	if h.KeyboardDevice != "" {
		return h.KeyboardDevice
	}
	return "/dev/hidg0"
}

func (h HIDConfig) MouseDeviceOrDefault() string {
	if h.MouseDevice != "" {
		return h.MouseDevice
	}
	return "/dev/hidg1"
}

func (h HIDConfig) FrameSocketOrDefault() string {
	if h.FrameSocket != "" {
		return h.FrameSocket
	}
	return "/tmp/frame_service.sock"
}

type ModelConfig struct {
	Provider    string   `toml:"provider"`
	Model       string   `toml:"model"`
	BaseURL     string   `toml:"base_url,omitempty"`
	APIKey      string   `toml:"api_key,omitempty"`
	TokenEnv    string   `toml:"token_env,omitempty"`
	Temperature float64  `toml:"temperature,omitempty"`
	MaxTokens   int      `toml:"max_tokens,omitempty"`
	Responses   []string `toml:"responses,omitempty"`
}

// AgentConfig is used internally by the runtime prompt builder.
type AgentConfig struct {
	Instruction      string
	AdditionalPrompt string
}

// MemoryConfig is used internally by the memory manager.
type MemoryConfig struct {
	Type       string
	WindowSize int
	MemoryKey  string
}

func LoadConfigFromDir(configDir string) (Config, error) {
	configPath := filepath.Join(configDir, "agent.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Fallback to .json for backward compatibility
		configPath = filepath.Join(configDir, "agent.json")
	}

	cfg, err := LoadConfig(configPath)
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

	return cfg, nil
}

func LoadConfig(path string) (Config, error) {
	var cfg Config

	// Determine format by file extension
	if strings.HasSuffix(path, ".toml") {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return Config{}, fmt.Errorf("decode TOML config: %w", err)
		}
	} else {
		_, err := os.Stat(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		return Config{}, fmt.Errorf("JSON format is deprecated, please use TOML format: %s", path)
	}

	cfg.Proxy = cfg.Proxy.WithDefaults()

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if err := c.Proxy.Validate(); err != nil {
		return err
	}

	switch c.Search.ProviderOrDefault() {
	case "duckduckgo":
	case "tavily":
		if strings.TrimSpace(c.Search.APIKey) == "" {
			return errors.New("search.api_key is required when search.provider=tavily")
		}
	default:
		return fmt.Errorf("invalid search.provider: %s (expected duckduckgo or tavily)", c.Search.Provider)
	}

	if strings.TrimSpace(c.Model.Provider) == "" {
		return errors.New("model.provider is required")
	}
	if strings.TrimSpace(c.Model.Model) == "" && strings.ToLower(c.Model.Provider) != "fake" {
		return errors.New("model.model is required")
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
		effectiveInputMode := strings.ToLower(strings.TrimSpace(c.InputMode))
		if effectiveInputMode == "" {
			effectiveInputMode = "text"
		}
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

	return nil
}

// InputModeOrDefault returns the input mode or "text" as default
func (c Config) InputModeOrDefault() string {
	mode := strings.TrimSpace(c.InputMode)
	if mode == "" {
		return "text"
	}
	return strings.ToLower(mode)
}

// TriggerModeOrDefault returns the trigger mode or "manual" as default
func (c Config) TriggerModeOrDefault() string {
	mode := strings.TrimSpace(c.TriggerMode)
	if mode == "" {
		return "manual"
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
	return 6 * time.Second
}

func (c Config) VoiceFirstTurnTimeoutOrDefault() time.Duration {
	if c.VoiceFirstTurnTimeoutMs > 0 {
		return time.Duration(c.VoiceFirstTurnTimeoutMs) * time.Millisecond
	}
	return 10 * time.Second
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
	return 400
}
