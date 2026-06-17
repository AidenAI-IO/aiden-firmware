package agent

import (
	"context"
	"strings"

	"github.com/tmc/langchaingo/llms"
	langmemory "github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
)

type conversationMessagePlannerMemory struct {
	inner schema.Memory
}

func newConversationMessagePlannerMemory(inner schema.Memory) schema.Memory {
	if inner == nil {
		return inner
	}
	return &conversationMessagePlannerMemory{inner: inner}
}

func (m *conversationMessagePlannerMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *conversationMessagePlannerMemory) MemoryVariables(ctx context.Context) []string {
	return m.inner.MemoryVariables(ctx)
}

func (m *conversationMessagePlannerMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	values, err := m.inner.LoadMemoryVariables(ctx, inputs)
	if err != nil {
		return nil, err
	}
	if values == nil {
		return values, nil
	}
	if key := m.inner.GetMemoryKey(ctx); key != "" {
		if _, ok := values[key]; ok {
			values[key] = ""
		}
	}
	return values, nil
}

func (m *conversationMessagePlannerMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *conversationMessagePlannerMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

func conversationHistoryMessageContents(ctx context.Context, history *langmemory.ChatMessageHistory, maxMessages int) ([]llms.MessageContent, error) {
	if history == nil {
		return nil, nil
	}
	messages, err := history.Messages(ctx)
	if err != nil {
		return nil, err
	}
	if maxMessages > 0 && len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	result := make([]llms.MessageContent, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.GetContent())
		if content == "" {
			continue
		}
		if converted, ok := messageContentFromChatMessage(message.GetType(), content); ok {
			result = append(result, converted)
		}
	}
	return result, nil
}

func messageContentFromChatMessage(role llms.ChatMessageType, content string) (llms.MessageContent, bool) {
	switch role {
	case llms.ChatMessageTypeHuman, llms.ChatMessageTypeAI, llms.ChatMessageTypeSystem, llms.ChatMessageTypeGeneric:
		return llms.TextParts(role, content), true
	default:
		return llms.MessageContent{}, false
	}
}
