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
	DefaultAgent string                 `toml:"default_agent"`
	Model        ModelConfig            `toml:"model"`
	HID          HIDConfig              `toml:"hid"`
	SkillsDirs   []string               `toml:"skills_dirs"`
	Agents       map[string]AgentConfig `toml:"agents"`
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
	Token       string   `toml:"token,omitempty"`
	TokenEnv    string   `toml:"token_env,omitempty"`
	Temperature float64  `toml:"temperature,omitempty"`
	MaxTokens   int      `toml:"max_tokens,omitempty"`
	Responses   []string `toml:"responses,omitempty"`
}

type TTSConfig struct {
	Provider string  `toml:"provider"`
	APIKey   string  `toml:"api_key,omitempty"`
	Model    string  `toml:"model,omitempty"`
	VoiceID  string  `toml:"voice_id,omitempty"`
	Emotion  string  `toml:"emotion,omitempty"`
	Speed    float64 `toml:"speed,omitempty"`
}

type STTConfig struct {
	Provider        string `toml:"provider"`
	APIKey          string `toml:"api_key,omitempty"`
	Model           string `toml:"model,omitempty"`
	BaseURL         string `toml:"base_url,omitempty"`
	SecretID        string `toml:"secret_id,omitempty"`
	SecretKey       string `toml:"secret_key,omitempty"`
	Region          string `toml:"region,omitempty"`
	EngineModelType string `toml:"engine_model_type,omitempty"`
}

type AgentConfig struct {
	Description        string       `toml:"description,omitempty"`
	Instruction        string       `toml:"instruction"`
	DefaultSkills      []string     `toml:"default_skills,omitempty"`
	Children           []string     `toml:"children,omitempty"`
	Memory             MemoryConfig `toml:"memory,omitempty"`
	MaxIterations      int          `toml:"max_iterations,omitempty"`
	ASRMode            string       `toml:"asr_mode,omitempty"`
	HidBinary          string       `toml:"hid_binary,omitempty"`
	FrameServiceSocket string       `toml:"frame_service_socket,omitempty"`
	AdditionalPrompt   string       `toml:"additional_prompt,omitempty"`
	EnergyThreshold    int          `toml:"energy_threshold,omitempty"`
	SilenceMs          int          `toml:"silence_ms,omitempty"`
	MinSpeechMs        int          `toml:"min_speech_ms,omitempty"`
	ModelText          *ModelConfig `toml:"model_text,omitempty"`
	TTS                *TTSConfig   `toml:"tts,omitempty"`
	STT                *STTConfig   `toml:"stt,omitempty"`
}

type MemoryConfig struct {
	Type       string `toml:"type,omitempty"`
	WindowSize int    `toml:"window_size,omitempty"`
	MemoryKey  string `toml:"memory_key,omitempty"`
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
	if c.DefaultAgent == "" {
		return errors.New("default_agent is required")
	}
	if strings.TrimSpace(c.Model.Provider) == "" {
		return errors.New("model.provider is required")
	}
	if strings.TrimSpace(c.Model.Model) == "" && strings.ToLower(c.Model.Provider) != "fake" {
		return errors.New("model.model is required")
	}
	if len(c.Agents) == 0 {
		return errors.New("at least one agent is required")
	}
	if _, ok := c.Agents[c.DefaultAgent]; !ok {
		return fmt.Errorf("default_agent %q not found", c.DefaultAgent)
	}

	for name, agentCfg := range c.Agents {
		for _, child := range agentCfg.Children {
			if _, ok := c.Agents[child]; !ok {
				return fmt.Errorf("agent %q references unknown child agent %q", name, child)
			}
		}
	}

	return nil
}
