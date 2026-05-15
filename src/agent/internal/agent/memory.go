package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/llms"
	langmemory "github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
)

type MemoryHandle struct {
	Memory  schema.Memory
	History *langmemory.ChatMessageHistory
}

type MemoryManager struct {
	mu         sync.Mutex
	handles    map[string]*MemoryHandle
	storageDir string
}

type MessageRecord struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewMemoryManager(storageDir ...string) *MemoryManager {
	manager := &MemoryManager{handles: map[string]*MemoryHandle{}}
	if len(storageDir) > 0 {
		manager.storageDir = storageDir[0]
	}
	return manager
}

func (m *MemoryManager) Get(agentName string, cfg MemoryConfig) (*MemoryHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if handle, ok := m.handles[agentName]; ok {
		return handle, nil
	}

	memoryKey := cfg.MemoryKey
	if memoryKey == "" {
		memoryKey = "history"
	}

	history := langmemory.NewChatMessageHistory()
	options := []langmemory.ConversationBufferOption{
		langmemory.WithChatHistory(history),
		langmemory.WithInputKey("input"),
		langmemory.WithOutputKey("output"),
		langmemory.WithMemoryKey(memoryKey),
	}

	handle := &MemoryHandle{History: history}
	switch cfg.Type {
	case "", "buffer":
		handle.Memory = langmemory.NewConversationBuffer(options...)
	case "window":
		windowSize := cfg.WindowSize
		if windowSize <= 0 {
			windowSize = 6
		}
		handle.Memory = langmemory.NewConversationWindowBuffer(windowSize, options...)
	default:
		return nil, fmt.Errorf("unsupported memory type %q", cfg.Type)
	}

	if err := m.loadPersistedMessages(history, agentName); err != nil {
		return nil, err
	}

	m.handles[agentName] = handle
	return handle, nil
}

func (m *MemoryManager) Snapshot(ctx context.Context, agentName string) ([]MessageRecord, error) {
	m.mu.Lock()
	handle, ok := m.handles[agentName]
	m.mu.Unlock()
	if !ok {
		return nil, nil
	}

	messages, err := handle.History.Messages(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]MessageRecord, 0, len(messages))
	for _, message := range messages {
		result = append(result, MessageRecord{
			Role:    string(message.GetType()),
			Content: message.GetContent(),
		})
	}
	return result, nil
}

func (m *MemoryManager) Clear(ctx context.Context, agentName string) error {
	m.mu.Lock()
	handle, ok := m.handles[agentName]
	m.mu.Unlock()
	if !ok {
		return m.removePersisted(agentName)
	}
	if err := handle.Memory.Clear(ctx); err != nil {
		return err
	}
	return m.removePersisted(agentName)
}

func (m *MemoryManager) Save(ctx context.Context, agentName string) error {
	records, err := m.Snapshot(ctx, agentName)
	if err != nil {
		return err
	}
	return m.persistSnapshot(agentName, records)
}

func (m *MemoryManager) loadPersistedMessages(history *langmemory.ChatMessageHistory, agentName string) error {
	if m.storageDir == "" {
		return nil
	}

	data, err := os.ReadFile(m.memoryPath(agentName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read persisted memory for %q: %w", agentName, err)
	}

	var records []MessageRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("decode persisted memory for %q: %w", agentName, err)
	}

	messages := make([]llms.ChatMessage, 0, len(records))
	for _, record := range records {
		messages = append(messages, messageFromRecord(record))
	}
	if err := history.SetMessages(context.Background(), messages); err != nil {
		return fmt.Errorf("restore persisted memory for %q: %w", agentName, err)
	}
	return nil
}

func (m *MemoryManager) persistSnapshot(agentName string, records []MessageRecord) error {
	if m.storageDir == "" {
		return nil
	}
	if err := os.MkdirAll(m.storageDir, 0o755); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory snapshot for %q: %w", agentName, err)
	}

	path := m.memoryPath(agentName)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write memory snapshot for %q: %w", agentName, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace memory snapshot for %q: %w", agentName, err)
	}
	return nil
}

func (m *MemoryManager) removePersisted(agentName string) error {
	if m.storageDir == "" {
		return nil
	}
	if err := os.Remove(m.memoryPath(agentName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove persisted memory for %q: %w", agentName, err)
	}
	return nil
}

func (m *MemoryManager) memoryPath(agentName string) string {
	return filepath.Join(m.storageDir, memoryFileName(agentName))
}

func memoryFileName(agentName string) string {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		agentName = "default"
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r
		}
		if r >= '0' && r <= '9' {
			return r
		}
		switch r {
		case '-', '_', '.':
			return r
		default:
			return '_'
		}
	}, agentName)
	return safe + ".json"
}

func messageFromRecord(record MessageRecord) llms.ChatMessage {
	switch record.Role {
	case string(llms.ChatMessageTypeAI):
		return llms.AIChatMessage{Content: record.Content}
	case string(llms.ChatMessageTypeSystem):
		return llms.SystemChatMessage{Content: record.Content}
	case string(llms.ChatMessageTypeFunction):
		return llms.FunctionChatMessage{Content: record.Content}
	case string(llms.ChatMessageTypeTool):
		return llms.ToolChatMessage{Content: record.Content}
	case string(llms.ChatMessageTypeGeneric):
		return llms.GenericChatMessage{Content: record.Content, Role: record.Role}
	case string(llms.ChatMessageTypeHuman):
		fallthrough
	default:
		return llms.HumanChatMessage{Content: record.Content}
	}
}
