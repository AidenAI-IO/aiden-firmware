package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Model            ModelConfig `toml:"model"`
	ModelText        ModelConfig `toml:"model_text,omitempty"` // Override for STT-then-text mode
	TTS              TTSConfig   `toml:"tts,omitempty"`
	STT              STTConfig   `toml:"stt,omitempty"`
	HID              HIDConfig   `toml:"hid"`
	Audio            AudioConfig `toml:"audio,omitempty"`
	Proxy            ProxyConfig `toml:"proxy,omitempty"`
	Instruction      string      `toml:"instruction"`
	AdditionalPrompt string      `toml:"additional_prompt,omitempty"`
	InputMode        string      `toml:"input_mode,omitempty"`   // "text", "audio", "stt"
	TriggerMode      string      `toml:"trigger_mode,omitempty"` // "manual", "wakeup"
	EnergyThreshold  int         `toml:"energy_threshold,omitempty"`
	SilenceMs        int         `toml:"silence_ms,omitempty"`
	MinSpeechMs      int         `toml:"min_speech_ms,omitempty"`
	MaxIterations    int         `toml:"max_iterations,omitempty"`
	SkillsDirs       []string    `toml:"skills_dirs"`
	ConfigDir        string      `toml:"-"`
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
	Provider        string `toml:"provider"` // "openai", "tencent"
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
	Instruction string
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

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if err := c.Proxy.Validate(); err != nil {
		return err
	}

	if strings.TrimSpace(c.Model.Provider) == "" {
		return errors.New("model.provider is required")
	}
	if strings.TrimSpace(c.Model.Model) == "" && strings.ToLower(c.Model.Provider) != "fake" {
		return errors.New("model.model is required")
	}

	// Validate input_mode
	if c.InputMode != "" {
		mode := strings.ToLower(c.InputMode)
		if mode != "text" && mode != "audio" && mode != "stt" {
			return fmt.Errorf("invalid input_mode: %s (expected text, audio, or stt)", c.InputMode)
		}

		// Validate STT config if in stt mode
		if mode == "stt" {
			if c.STT.Provider == "" {
				return errors.New("stt.provider is required when input_mode=stt")
			}
		}

		// Validate TTS config if not in text mode
		if mode != "text" && c.TTS.Provider == "" {
			return errors.New("tts.provider is required when input_mode is audio or stt")
		}
	}

	return nil
}

// InputModeOrDefault returns the input mode or "text" as default
func (c Config) InputModeOrDefault() string {
	if c.InputMode == "" {
		return "text"
	}
	return strings.ToLower(c.InputMode)
}

// TriggerModeOrDefault returns the trigger mode or "manual" as default
func (c Config) TriggerModeOrDefault() string {
	if c.TriggerMode == "" {
		return "manual"
	}
	return strings.ToLower(c.TriggerMode)
}
