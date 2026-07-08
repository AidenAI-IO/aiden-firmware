package executor

import (
	"context"
	"testing"

	"aiden-agent/internal/agent/context_manager"

	"github.com/tmc/langchaingo/llms"
)

type cacheHintCapturingModel struct {
	messages []llms.MessageContent
	hints    context_manager.PromptCacheHints
	hasHints bool
}

func (m *cacheHintCapturingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return "", nil
}

func (m *cacheHintCapturingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.messages = messages
	m.hints, m.hasHints = context_manager.PromptCacheHintsFromContext(ctx)
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "ok"}},
	}, nil
}

func TestGenerateContentPassesPromptCacheHints(t *testing.T) {
	manager := context_manager.NewContextManager()
	manager.AppendMessage(context_manager.Message{
		Role: context_manager.MessageRoleSystem,
		PromptSections: []context_manager.PromptSection{
			{Text: "stable prefix", CacheEphemeral: true},
			{Text: "dynamic suffix"},
		},
	})
	manager.AppendMessage(context_manager.Message{
		Role:    context_manager.MessageRoleUser,
		Content: "hello",
	})
	model := &cacheHintCapturingModel{}
	executor := NewLLMExecutor(model, manager)

	if _, err := executor.GenerateContent(context.Background()); err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if !model.hasHints || !model.hints.ShouldCache(0, 0) {
		t.Fatalf("model prompt cache hints = %#v, hasHints=%v; want message 0 part 0", model.hints.EphemeralParts, model.hasHints)
	}
	if len(model.messages) != 2 || len(model.messages[0].Parts) != 2 {
		t.Fatalf("model messages = %#v, want split system prompt and user message", model.messages)
	}
}
