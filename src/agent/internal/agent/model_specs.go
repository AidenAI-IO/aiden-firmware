package agent

import (
	"aiden-agent/internal/agent/model"
	"strings"
)

// modelSpecRegistry covers the chat models referenced from this repo (overlay
// agent.toml, configuration docs, common OpenRouter routes used in dev/staging)
// plus the current mainstream multimodal + tool-calling models. Numbers reflect
// public model cards; when in doubt prefer the smaller advertised window so we
// err on the side of triggering compression earlier instead of overflowing the
// prompt. Models are keyed by both their provider-prefixed canonical id and the
// bare model name so configs that drop the provider prefix (e.g. the openai
// provider pointed at a proxy with model = "gpt-5.4") still resolve.
//
// Only models that support both image input and tool/function calling are
// listed here, since those are the capabilities the agent relies on.
var modelSpecRegistry = map[string]model.ModelSpec{
	// OpenAI GPT-5.x family (vision + tool calling). ContextWindow is the total
	// advertised window; compaction logic uses (ContextWindow - MaxOutput) for
	// the actual input budget.
	"openai/gpt-5.5":      {ContextWindow: 1_050_000, MaxOutput: 128_000},
	"gpt-5.5":             {ContextWindow: 1_050_000, MaxOutput: 128_000},
	"openai/gpt-5.5-pro":  {ContextWindow: 1_050_000, MaxOutput: 128_000},
	"gpt-5.5-pro":         {ContextWindow: 1_050_000, MaxOutput: 128_000},
	"openai/gpt-5.4":      {ContextWindow: 1_050_000, MaxOutput: 128_000},
	"gpt-5.4":             {ContextWindow: 1_050_000, MaxOutput: 128_000},
	"openai/gpt-5.4-mini": {ContextWindow: 400_000, MaxOutput: 128_000},
	"gpt-5.4-mini":        {ContextWindow: 400_000, MaxOutput: 128_000},
	"openai/gpt-5.4-nano": {ContextWindow: 400_000, MaxOutput: 128_000},
	"gpt-5.4-nano":        {ContextWindow: 400_000, MaxOutput: 128_000},

	// Anthropic Claude 4.x family (vision + tool calling).
	"anthropic/claude-opus-4.8":   {ContextWindow: 1_000_000, MaxOutput: 64_000},
	"claude-opus-4.8":             {ContextWindow: 1_000_000, MaxOutput: 64_000},
	"anthropic/claude-sonnet-4.6": {ContextWindow: 1_000_000, MaxOutput: 64_000},
	"claude-sonnet-4.6":           {ContextWindow: 1_000_000, MaxOutput: 64_000},
	"anthropic/claude-haiku-4.5":  {ContextWindow: 200_000, MaxOutput: 64_000},
	"claude-haiku-4.5":            {ContextWindow: 200_000, MaxOutput: 64_000},

	// Google Gemini 3.5 family (vision + tool calling).
	"google/gemini-3.5-pro":   {ContextWindow: 1_048_576, MaxOutput: 65_536},
	"gemini-3.5-pro":          {ContextWindow: 1_048_576, MaxOutput: 65_536},
	"google/gemini-3.5-flash": {ContextWindow: 1_048_576, MaxOutput: 65_536},
	"gemini-3.5-flash":        {ContextWindow: 1_048_576, MaxOutput: 65_536},

	// Moonshot Kimi K3 (vision + tool calling). Reached via the Moonshot
	// OpenAI-compatible endpoint (bare model name) or the OpenRouter route.
	"moonshotai/kimi-k3": {ContextWindow: 1_048_576, MaxOutput: 131_072},
	"kimi-k3":            {ContextWindow: 1_048_576, MaxOutput: 131_072},

	// Existing entries retained for back-compat with dev/staging configs.
	"anthropic/claude-3.5-sonnet":  {ContextWindow: 200_000, MaxOutput: 8_192},
	"anthropic/claude-3.7-sonnet":  {ContextWindow: 200_000, MaxOutput: 8_192},
	"bytedance-seed/seed-2.0-lite": {ContextWindow: 128_000, MaxOutput: 8_192},
	"openai/gpt-4o":                {ContextWindow: 128_000, MaxOutput: 16_384},
	"openai/gpt-4o-mini":           {ContextWindow: 128_000, MaxOutput: 16_384},
}

// LookupModelSpec returns the spec for the given (provider, model). It tries
// the full canonical id first (e.g. "google/gemini-3.5-flash") and then falls
// back to the bare model name without a provider prefix so configurations that
// drop the prefix still resolve. The boolean is false when the model is
// unknown; callers must then fall back to a configured default.
func LookupModelSpec(provider, modelName string) (model.ModelSpec, bool) {
	key := strings.ToLower(strings.TrimSpace(modelName))
	if key == "" {
		return model.ModelSpec{}, false
	}
	if spec, ok := modelSpecRegistry[key]; ok {
		return spec, true
	}
	if i := strings.LastIndex(key, "/"); i >= 0 && i+1 < len(key) {
		if spec, ok := modelSpecRegistry[key[i+1:]]; ok {
			return spec, true
		}
	}
	// Provider-prefixed retry for entries registered without a provider.
	if provider != "" {
		full := strings.ToLower(strings.TrimSpace(provider)) + "/" + key
		if spec, ok := modelSpecRegistry[full]; ok {
			return spec, true
		}
	}
	return model.ModelSpec{}, false
}
