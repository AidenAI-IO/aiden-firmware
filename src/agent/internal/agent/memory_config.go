package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MemoryExtractionConfig holds tunables for the memory planes that survive
// outside the conversation transcript. Conversation compaction is configured on
// the runtime (context_compaction_threshold), not here.
type MemoryExtractionConfig struct {
	// TagCandidates and EntitySuffixes drive the lightweight, non-LLM tag and
	// entity extraction applied to a run's user input when recording an Episode.
	TagCandidates  []string `yaml:"tag_candidates"`
	EntitySuffixes []string `yaml:"entity_suffixes"`
	// ContextWindow is the fallback context window in tokens used when the
	// active model is unknown to the model_specs registry. The runtime normally
	// derives the window from ModelResolver.Spec(); this value only kicks in for
	// unrecognised models.
	ContextWindow int `yaml:"context_window"`
	// EpisodeMemoryIdleDelaySeconds is the idle time in seconds before starting
	// Episode Memory extraction in the background. Default is 300 (5 minutes).
	EpisodeMemoryIdleDelaySeconds int `yaml:"episode_memory_idle_delay_seconds"`
}

type rawMemoryExtractionConfig struct {
	TagCandidates                 *[]string `yaml:"tag_candidates"`
	EntitySuffixes                *[]string `yaml:"entity_suffixes"`
	ContextWindow                 *int      `yaml:"context_window"`
	EpisodeMemoryIdleDelaySeconds *int      `yaml:"episode_memory_idle_delay_seconds"`
}

const defaultMemoryFallbackContextWindow = 32000

// DefaultMemoryExtractionConfig returns the default memory configuration.
func DefaultMemoryExtractionConfig() MemoryExtractionConfig {
	return MemoryExtractionConfig{
		TagCandidates: []string{
			"报销", "支付", "付款", "提交", "登录", "验证码",
			"发票", "项目编码", "风险", "确认", "开发板", "agent",
		},
		EntitySuffixes:                []string{"App", "app", "APP"},
		ContextWindow:                 defaultMemoryFallbackContextWindow,
		EpisodeMemoryIdleDelaySeconds: int(defaultMemoryWorkerIdleDelay / time.Second),
	}
}

func normalizeMemoryExtractionConfig(cfg MemoryExtractionConfig) MemoryExtractionConfig {
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = defaultMemoryFallbackContextWindow
	}
	if cfg.EpisodeMemoryIdleDelaySeconds <= 0 {
		cfg.EpisodeMemoryIdleDelaySeconds = int(defaultMemoryWorkerIdleDelay / time.Second)
	}
	return cfg
}

// LoadMemoryExtractionConfig loads memory configuration from a YAML file in the
// specified config directory. Falls back to defaults when the file is missing or
// invalid. Unknown keys are ignored, so configs written by older builds (which
// carried session-compaction knobs) still load.
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
	if raw.ContextWindow != nil {
		cfg.ContextWindow = *raw.ContextWindow
	}
	if raw.EpisodeMemoryIdleDelaySeconds != nil {
		cfg.EpisodeMemoryIdleDelaySeconds = *raw.EpisodeMemoryIdleDelaySeconds
	}
	return normalizeMemoryExtractionConfig(cfg)
}

// EpisodeMemoryIdleDelayOrDefault returns the configured idle delay duration
// for Episode Memory extraction, or the default 5 minutes if not configured.
func (cfg MemoryExtractionConfig) EpisodeMemoryIdleDelayOrDefault() time.Duration {
	return time.Duration(normalizeMemoryExtractionConfig(cfg).EpisodeMemoryIdleDelaySeconds) * time.Second
}

// extractTagsFromText returns the configured tag candidates present in content.
func (cfg MemoryExtractionConfig) extractTagsFromText(content string) []string {
	tags := make([]string, 0)
	for _, candidate := range cfg.TagCandidates {
		if strings.Contains(content, candidate) {
			tags = append(tags, candidate)
		}
	}
	return tags
}

// extractEntitiesFromText returns names in content ending with a configured
// entity suffix, for example "蓝海报销App".
func (cfg MemoryExtractionConfig) extractEntitiesFromText(content string) []string {
	var entities []string
	for _, suffix := range cfg.EntitySuffixes {
		if suffix == "" {
			continue
		}
		searchStart := 0
		for {
			idx := strings.Index(content[searchStart:], suffix)
			if idx < 0 {
				break
			}
			end := searchStart + idx + len(suffix)
			start := entityStart(content[:end])
			entity := cleanEntityName(content[start:end])
			if entity != "" {
				entities = append(entities, entity)
			}
			searchStart = end
		}
	}
	return entities
}

func entityStart(prefix string) int {
	runes := []rune(prefix)
	start := len(runes)
	for start > 0 {
		r := runes[start-1]
		if strings.ContainsRune(" \t\n\r，。,.、；;：:\"'（）()[]【】", r) {
			break
		}
		start--
		if len(runes)-start >= 16 {
			break
		}
	}
	return len(string(runes[:start]))
}

func cleanEntityName(entity string) string {
	entity = strings.Trim(entity, " ，。,.、；;：:\"'（）()[]【】")
	for _, marker := range []string{"处理", "打开", "使用", "登录", "进入", "关于", "在"} {
		if idx := strings.LastIndex(entity, marker); idx >= 0 {
			entity = entity[idx+len(marker):]
		}
	}
	return strings.Trim(entity, " ，。,.、；;：:\"'（）()[]【】")
}
