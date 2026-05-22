package agent

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type MemoryExtractionConfig struct {
	TagCandidates     []string `yaml:"tag_candidates"`
	EntitySuffixes    []string `yaml:"entity_suffixes"`
	HotWindowEvents   int      `yaml:"hot_window_events"`
	ContextWindow     int      `yaml:"context_window"`
	CompressAtPercent int      `yaml:"compress_at_percent"`
	SummaryMaxChunks  int      `yaml:"summary_max_chunks"`
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
		SummaryMaxChunks:  10,
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
	if cfg.SummaryMaxChunks <= 0 {
		cfg.SummaryMaxChunks = 10
	}
	return cfg
}
