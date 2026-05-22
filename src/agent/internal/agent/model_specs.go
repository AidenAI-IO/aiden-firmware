package agent

import "strings"

// ModelSpec describes static capabilities of a chat completion model that the
// runtime needs at decision time (e.g. when to compress session history).
//
// ContextWindow is the model's total context window in tokens (input + output).
// MaxOutput is the per-request output cap the model advertises; 0 means unknown.
type ModelSpec struct {
	ContextWindow int
	MaxOutput     int
}

// modelSpecRegistry covers the chat models referenced from this repo (overlay
// agent.toml, configuration docs, common OpenRouter routes used in dev/staging)
// plus a couple of common siblings. Numbers reflect public model cards; when in
// doubt prefer the smaller advertised window so we err on the side of
// triggering compression earlier instead of overflowing the prompt.
var modelSpecRegistry = map[string]ModelSpec{
	"google/gemini-3.5-flash":      {ContextWindow: 1_000_000, MaxOutput: 8_192},
	"google/gemini-3.5-pro":        {ContextWindow: 1_000_000, MaxOutput: 8_192},
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
func LookupModelSpec(provider, model string) (ModelSpec, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return ModelSpec{}, false
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
	return ModelSpec{}, false
}
