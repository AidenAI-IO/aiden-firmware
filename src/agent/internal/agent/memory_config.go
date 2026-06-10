package agent

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type MemoryExtractionConfig struct {
	TagCandidates   []string `yaml:"tag_candidates"`
	EntitySuffixes  []string `yaml:"entity_suffixes"`
	// HotWindowEvents is the hard limit on uncompressed SessionEvent
	// count. Compression triggers when event count exceeds this threshold,
	// regardless of token usage. Acts as a safety valve for cold-start sessions
	// with large restored history or when token metrics are unavailable.
	HotWindowEvents int `yaml:"hot_window_events"`
	// ContextWindow is the fallback context window in tokens used by the
	// memory manager's compression trigger when the active model is unknown
	// to the model_specs registry. The runtime normally derives the window
	// from ModelResolver.Spec(); this value only kicks in for unrecognised
	// models so behaviour stays sane instead of disabling compression.
	ContextWindow     int `yaml:"context_window"`
	CompressAtPercent int `yaml:"compress_at_percent"`
	SummaryMaxChunks  int `yaml:"summary_max_chunks"`
	// ReserveTokens is the token headroom kept free below the model's context
	// window. Compression triggers once prompt tokens exceed
	// contextWindow - ReserveTokens, OR when the percentage threshold is met.
	// Reserve is clamped to at most half the active window so small-window
	// models stay sane.
	ReserveTokens int `yaml:"reserve_tokens"`
	// KeepRecentTokens is the approximate token budget of recent events kept
	// hot (uncompressed) when a token-based cut point is taken. Clamped to fit
	// alongside ReserveTokens inside the active window. Borrowed from pi's
	// keepRecentTokens.
	KeepRecentTokens int `yaml:"keep_recent_tokens"`
}

const (
	defaultReserveTokens    = 8192
	defaultKeepRecentTokens = 20000
)

// DefaultMemoryExtractionConfig returns the default memory extraction
// configuration with sensible defaults for token budgets, compression
// thresholds, and entity extraction patterns.
func DefaultMemoryExtractionConfig() MemoryExtractionConfig {
	return MemoryExtractionConfig{
		TagCandidates: []string{
			"报销", "支付", "付款", "提交", "登录", "验证码",
			"发票", "项目编码", "风险", "确认", "开发板", "agent",
		},
		EntitySuffixes:             []string{"App", "app", "APP"},
		HotWindowEvents: defaultHotWindowEvents,
		ContextWindow:              32000,
		CompressAtPercent:          50,
		SummaryMaxChunks:           10,
		ReserveTokens:              defaultReserveTokens,
		KeepRecentTokens:           defaultKeepRecentTokens,
	}
}

// LoadMemoryExtractionConfig loads memory extraction configuration from a YAML
// file in the specified config directory. Falls back to defaults when the file
// is missing or invalid, and applies sanity checks to all loaded values.
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
		cfg.HotWindowEvents = defaultHotWindowEvents
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
	if cfg.ReserveTokens <= 0 {
		cfg.ReserveTokens = defaultReserveTokens
	}
	if cfg.KeepRecentTokens <= 0 {
		cfg.KeepRecentTokens = defaultKeepRecentTokens
	}
	return cfg
}
