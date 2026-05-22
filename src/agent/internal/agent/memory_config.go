package agent

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type MemoryExtractionConfig struct {
	TagCandidates   []string `yaml:"tag_candidates"`
	EntitySuffixes  []string `yaml:"entity_suffixes"`
	HotWindowEvents int      `yaml:"hot_window_events"`
	// ContextWindow is the fallback context window in tokens used by the
	// memory manager's compression trigger when the active model is unknown
	// to the model_specs registry. The runtime normally derives the window
	// from ModelResolver.Spec(); this value only kicks in for unrecognised
	// models so behaviour stays sane instead of disabling compression.
	ContextWindow     int `yaml:"context_window"`
	CompressAtPercent int `yaml:"compress_at_percent"`
}

func DefaultMemoryExtractionConfig() MemoryExtractionConfig {
	return MemoryExtractionConfig{
		TagCandidates: []string{
			"报销", "支付", "付款", "提交", "登录", "验证码",
			"发票", "项目编码", "风险", "确认", "开发板", "agent",
		},
		EntitySuffixes:    []string{"App", "app", "APP"},
		HotWindowEvents:   20,
		ContextWindow:     32000,
		CompressAtPercent: 50,
	}
}

func LoadMemoryExtractionConfig(configDir string) MemoryExtractionConfig {
	cfg := DefaultMemoryExtractionConfig()
	if configDir == "" {
		return cfg
	}
	path := filepath.Join(configDir, "memory", "extraction.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = yaml.Unmarshal(data, &cfg)
	if cfg.HotWindowEvents <= 0 {
		cfg.HotWindowEvents = 20
	}
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = 32000
	}
	if cfg.CompressAtPercent <= 0 || cfg.CompressAtPercent > 100 {
		cfg.CompressAtPercent = 50
	}
	return cfg
}
