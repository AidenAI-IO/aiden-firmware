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
	Model        ModelConfig  `toml:"model"`
	HID          HIDConfig    `toml:"hid"`
	Instruction  string       `toml:"instruction"`
	MaxIterations int         `toml:"max_iterations,omitempty"`
	SkillsDirs   []string     `toml:"skills_dirs"`
	ConfigDir    string       `toml:"-"`
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
	if strings.TrimSpace(c.Model.Provider) == "" {
		return errors.New("model.provider is required")
	}
	if strings.TrimSpace(c.Model.Model) == "" && strings.ToLower(c.Model.Provider) != "fake" {
		return errors.New("model.model is required")
	}
	return nil
}
