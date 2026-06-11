package agent

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type MemoryExtractionConfig struct {
	TagCandidates  []string `yaml:"tag_candidates"`
	EntitySuffixes []string `yaml:"entity_suffixes"`
	// HotWindowEvents is the target size of the retained hot window when
	// compaction falls back to an event-count cut.
	HotWindowEvents int `yaml:"hot_window_events"`
	// CountCompressAfterEvents is the event-count trigger used only when token
	// metrics are unavailable. Keep it above HotWindowEvents so count-based
	// compaction has hysteresis instead of producing tiny chunks every turn.
	CountCompressAfterEvents int `yaml:"count_compress_after_events"`
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
	// SessionBoundaryEnabled controls voice multi-task session detection.
	// When true, each new user_input is classified as "continue" or "new";
	// "new" rotates events.jsonl into a pending session so the current turn
	// starts with a clean hot window.
	SessionBoundaryEnabled bool `yaml:"session_boundary_enabled"`
	// SessionBoundaryShortGapSeconds is the gap below which a new turn is
	// treated as a continuation regardless of lexical signals.
	SessionBoundaryShortGapSeconds int `yaml:"session_boundary_short_gap_seconds"`
	// SessionBoundaryLongGapSeconds is the gap above which a new turn is
	// treated as a fresh session regardless of lexical signals.
	SessionBoundaryLongGapSeconds int `yaml:"session_boundary_long_gap_seconds"`

	countCompressAfterEventsConfigured bool
	SessionBoundaryEnabledConfigured   bool `yaml:"-"`
}

type rawMemoryExtractionConfig struct {
	TagCandidates                  *[]string `yaml:"tag_candidates"`
	EntitySuffixes                 *[]string `yaml:"entity_suffixes"`
	HotWindowEvents                *int      `yaml:"hot_window_events"`
	CountCompressAfterEvents       *int      `yaml:"count_compress_after_events"`
	ContextWindow                  *int      `yaml:"context_window"`
	CompressAtPercent              *int      `yaml:"compress_at_percent"`
	SummaryMaxChunks               *int      `yaml:"summary_max_chunks"`
	ReserveTokens                  *int      `yaml:"reserve_tokens"`
	KeepRecentTokens               *int      `yaml:"keep_recent_tokens"`
	SessionBoundaryEnabled         *bool     `yaml:"session_boundary_enabled"`
	SessionBoundaryShortGapSeconds *int      `yaml:"session_boundary_short_gap_seconds"`
	SessionBoundaryLongGapSeconds  *int      `yaml:"session_boundary_long_gap_seconds"`
}

const (
	defaultReserveTokens            = 8192
	defaultKeepRecentTokens         = 20000
	defaultCountCompressAfterEvents = defaultHotWindowEvents * 2
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
		EntitySuffixes:                 []string{"App", "app", "APP"},
		HotWindowEvents:                defaultHotWindowEvents,
		CountCompressAfterEvents:       defaultCountCompressAfterEvents,
		ContextWindow:                  32000,
		CompressAtPercent:              50,
		SummaryMaxChunks:               10,
		ReserveTokens:                  defaultReserveTokens,
		KeepRecentTokens:               defaultKeepRecentTokens,
		SessionBoundaryEnabled:         true,
		SessionBoundaryShortGapSeconds: DefaultBoundaryConfig().ShortGapSeconds,
		SessionBoundaryLongGapSeconds:  DefaultBoundaryConfig().LongGapSeconds,
	}
}

func normalizeMemoryExtractionConfig(cfg MemoryExtractionConfig) MemoryExtractionConfig {
	if cfg.HotWindowEvents <= 0 {
		cfg.HotWindowEvents = defaultHotWindowEvents
	}
	if !cfg.countCompressAfterEventsConfigured &&
		(cfg.CountCompressAfterEvents == defaultCountCompressAfterEvents || cfg.CountCompressAfterEvents <= cfg.HotWindowEvents) {
		cfg.CountCompressAfterEvents = cfg.HotWindowEvents * 2
	} else if cfg.CountCompressAfterEvents <= cfg.HotWindowEvents {
		cfg.CountCompressAfterEvents = cfg.HotWindowEvents * 2
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
	if !cfg.SessionBoundaryEnabledConfigured {
		cfg.SessionBoundaryEnabled = true
	}
	defaultBoundary := DefaultBoundaryConfig()
	if cfg.SessionBoundaryShortGapSeconds <= 0 {
		cfg.SessionBoundaryShortGapSeconds = defaultBoundary.ShortGapSeconds
	}
	if cfg.SessionBoundaryLongGapSeconds <= cfg.SessionBoundaryShortGapSeconds {
		cfg.SessionBoundaryLongGapSeconds = defaultBoundary.LongGapSeconds
	}
	if cfg.SessionBoundaryLongGapSeconds <= cfg.SessionBoundaryShortGapSeconds {
		cfg.SessionBoundaryLongGapSeconds = cfg.SessionBoundaryShortGapSeconds + defaultBoundary.LongGapSeconds
	}
	return cfg
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

	var raw rawMemoryExtractionConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return cfg
	}
	if raw.TagCandidates != nil {
		cfg.TagCandidates = *raw.TagCandidates
	}
	if raw.EntitySuffixes != nil {
		cfg.EntitySuffixes = *raw.EntitySuffixes
	}
	if raw.HotWindowEvents != nil {
		cfg.HotWindowEvents = *raw.HotWindowEvents
	}
	if raw.CountCompressAfterEvents != nil {
		cfg.CountCompressAfterEvents = *raw.CountCompressAfterEvents
		cfg.countCompressAfterEventsConfigured = true
	}
	if raw.ContextWindow != nil {
		cfg.ContextWindow = *raw.ContextWindow
	}
	if raw.CompressAtPercent != nil {
		cfg.CompressAtPercent = *raw.CompressAtPercent
	}
	if raw.SummaryMaxChunks != nil {
		cfg.SummaryMaxChunks = *raw.SummaryMaxChunks
	}
	if raw.ReserveTokens != nil {
		cfg.ReserveTokens = *raw.ReserveTokens
	}
	if raw.KeepRecentTokens != nil {
		cfg.KeepRecentTokens = *raw.KeepRecentTokens
	}
	if raw.SessionBoundaryEnabled != nil {
		cfg.SessionBoundaryEnabled = *raw.SessionBoundaryEnabled
		cfg.SessionBoundaryEnabledConfigured = true
	}
	if raw.SessionBoundaryShortGapSeconds != nil {
		cfg.SessionBoundaryShortGapSeconds = *raw.SessionBoundaryShortGapSeconds
	}
	if raw.SessionBoundaryLongGapSeconds != nil {
		cfg.SessionBoundaryLongGapSeconds = *raw.SessionBoundaryLongGapSeconds
	}
	return normalizeMemoryExtractionConfig(cfg)
}
