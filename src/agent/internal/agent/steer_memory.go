package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/llms"
	langmemory "github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
)

type steerConversationAppender interface {
	AppendSteerMessage(ctx context.Context, input string, steer RunSteerMessage) error
	AppendSteerOnly(ctx context.Context, steer RunSteerMessage) error
}

type steerConversationStatus interface {
	HasSteerMessages() bool
	SteerMessages() []RunSteerMessage
}

type steerConversationMemory struct {
	inner   schema.Memory
	history schema.ChatMessageHistory

	mu            sync.Mutex
	inputAppended bool
	steerAppended bool
	steers        []RunSteerMessage
}

func newSteerConversationMemory(inner schema.Memory, history schema.ChatMessageHistory) schema.Memory {
	if inner == nil || history == nil {
		return inner
	}
	return &steerConversationMemory{inner: inner, history: history}
}

func (m *steerConversationMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *steerConversationMemory) MemoryVariables(ctx context.Context) []string {
	return m.inner.MemoryVariables(ctx)
}

func (m *steerConversationMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	return m.inner.LoadMemoryVariables(ctx, inputs)
}

func (m *steerConversationMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	m.mu.Lock()
	inputAppended := m.inputAppended
	m.mu.Unlock()
	if !inputAppended {
		return m.inner.SaveContext(ctx, inputs, outputs)
	}

	output, err := langmemory.GetInputValue(outputs, "")
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.history.AddAIMessage(ctx, output); err != nil {
		return fmt.Errorf("append assistant output after steering: %w", err)
	}
	return pruneSteerConversationWindow(ctx, m.inner)
}

func (m *steerConversationMemory) Clear(ctx context.Context) error {
	m.mu.Lock()
	m.inputAppended = false
	m.steerAppended = false
	m.steers = nil
	m.mu.Unlock()
	return m.inner.Clear(ctx)
}

func (m *steerConversationMemory) AppendSteerMessage(ctx context.Context, input string, steer RunSteerMessage) error {
	content := strings.TrimSpace(steer.Content)
	if content == "" {
		content = "(empty steering message)"
	}
	steer.Content = content

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.inputAppended {
		if err := m.history.AddUserMessage(ctx, input); err != nil {
			return fmt.Errorf("append current user input before steering: %w", err)
		}
		m.inputAppended = true
	}
	if err := m.history.AddUserMessage(ctx, content); err != nil {
		return fmt.Errorf("append steering message: %w", err)
	}
	m.steerAppended = true
	m.steers = append(m.steers, steer)
	return pruneSteerConversationWindow(ctx, m.inner)
}

// AppendSteerOnly appends a steer message without re-appending the input.
// Used when the input is already in the context manager (e.g., during tool
// execution or LLM thinking period). This method tracks the steer for
// persistence and event emission.
func (m *steerConversationMemory) AppendSteerOnly(ctx context.Context, steer RunSteerMessage) error {
	content := strings.TrimSpace(steer.Content)
	if content == "" {
		content = "(empty steering message)"
	}
	steer.Content = content

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.history.AddUserMessage(ctx, content); err != nil {
		return fmt.Errorf("append steering message: %w", err)
	}
	m.steerAppended = true
	m.steers = append(m.steers, steer)
	return pruneSteerConversationWindow(ctx, m.inner)
}

func (m *steerConversationMemory) HasSteerMessages() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.steerAppended
}

func (m *steerConversationMemory) SteerMessages() []RunSteerMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RunSteerMessage(nil), m.steers...)
}

func pruneSteerConversationWindow(ctx context.Context, mem schema.Memory) error {
	switch typed := mem.(type) {
	case *steerConversationMemory:
		return pruneSteerConversationWindow(ctx, typed.inner)
	case *langmemory.ConversationWindowBuffer:
		messages, err := typed.ChatHistory.Messages(ctx)
		if err != nil {
			return err
		}
		limit := typed.ConversationWindowSize * 2
		if limit <= 0 || len(messages) <= limit {
			return nil
		}
		return typed.ChatHistory.SetMessages(ctx, append([]llms.ChatMessage(nil), messages[len(messages)-limit:]...))
	default:
		return nil
	}
}
