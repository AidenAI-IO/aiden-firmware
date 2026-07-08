package context_manager

import "context"

// PromptCachePartHint marks one message part that should receive provider
// cache_control when explicit prompt caching is enabled.
type PromptCachePartHint struct {
	MessageIndex int `json:"message_index"`
	PartIndex    int `json:"part_index"`
}

// PromptCacheHints carries cache breakpoints produced by ContextManager conversion.
type PromptCacheHints struct {
	EphemeralParts []PromptCachePartHint
}

func (h PromptCacheHints) ShouldCache(messageIndex, partIndex int) bool {
	for _, hint := range h.EphemeralParts {
		if hint.MessageIndex == messageIndex && hint.PartIndex == partIndex {
			return true
		}
	}
	return false
}

type promptCacheHintsContextKey struct{}

func WithPromptCacheHints(ctx context.Context, hints PromptCacheHints) context.Context {
	if len(hints.EphemeralParts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, promptCacheHintsContextKey{}, hints)
}

func PromptCacheHintsFromContext(ctx context.Context) (PromptCacheHints, bool) {
	if ctx == nil {
		return PromptCacheHints{}, false
	}
	hints, ok := ctx.Value(promptCacheHintsContextKey{}).(PromptCacheHints)
	return hints, ok
}
