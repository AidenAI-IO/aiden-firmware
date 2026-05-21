package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
	langmemory "github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
)

type MemoryHandle struct {
	Memory  schema.Memory
	History *langmemory.ChatMessageHistory
}

// SummarizeFn generates a summary string from a list of session events.
// It is called during session compression to produce a human-readable
// summary of the events being archived into a chunk.
type SummarizeFn func(ctx context.Context, events []SessionEvent) string

type MemoryManager struct {
	mu               sync.Mutex
	handles          map[string]*MemoryHandle
	eventCount       map[string]int
	storageDir       string
	extraction       MemoryExtractionConfig
	lastPromptTokens int
	summarizeFn      SummarizeFn
	profileFn        ProfileFn
	logger           *Logger
}

const defaultMemoryHotWindowEvents = 20

type MessageRecord struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type SessionEvent struct {
	EventID    string `json:"event_id"`
	Ts         string `json:"ts"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Source     string `json:"source,omitempty"`
	Content    string `json:"content"`
	AppName    string `json:"app_name,omitempty"`
	RiskLevel  string `json:"risk_level,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type MemoryManagerOption func(*MemoryManager)

func WithExtractionConfig(cfg MemoryExtractionConfig) MemoryManagerOption {
	return func(m *MemoryManager) { m.extraction = cfg }
}

func WithSummarizeFn(fn SummarizeFn) MemoryManagerOption {
	return func(m *MemoryManager) { m.summarizeFn = fn }
}

func WithProfileFn(fn ProfileFn) MemoryManagerOption {
	return func(m *MemoryManager) { m.profileFn = fn }
}

func WithMemoryLogger(logger *Logger) MemoryManagerOption {
	return func(m *MemoryManager) { m.logger = logger }
}

func NewMemoryManager(storageDir string, opts ...MemoryManagerOption) *MemoryManager {
	manager := &MemoryManager{
		handles:    map[string]*MemoryHandle{},
		eventCount: map[string]int{},
		extraction: DefaultMemoryExtractionConfig(),
	}
	if storageDir != "" {
		manager.storageDir = storageDir
	}
	for _, opt := range opts {
		opt(manager)
	}
	return manager
}

func (m *MemoryManager) SetLastPromptTokens(tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPromptTokens = tokens
}

func (m *MemoryManager) LastPromptTokens() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPromptTokens
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
	defer m.mu.Unlock()

	handle, ok := m.handles[agentName]
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
	if ok {
		if err := handle.Memory.Clear(ctx); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()

	if !ok {
		return m.removePersisted(agentName)
	}
	return m.removePersisted(agentName)
}

func (m *MemoryManager) Save(ctx context.Context, agentName string) error {
	records, err := m.Snapshot(ctx, agentName)
	if err != nil {
		return err
	}
	if err := m.persistSnapshot(agentName, records); err != nil {
		return err
	}
	return m.maintainFilesystemMemory(ctx)
}

func (m *MemoryManager) AppendExchange(ctx context.Context, agentName string, input string, output string) error {
	if m.storageDir == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.sessionEventsPath()), 0o755); err != nil {
		return fmt.Errorf("create session memory directory: %w", err)
	}
	file, err := os.OpenFile(m.sessionEventsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open session events for %q: %w", agentName, err)
	}
	defer file.Close()

	now := time.Now().UTC()
	encoder := json.NewEncoder(file)
	records := []MessageRecord{
		{Role: string(llms.ChatMessageTypeHuman), Content: input},
		{Role: string(llms.ChatMessageTypeAI), Content: output},
	}
	for i, record := range records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		event := sessionEventFromRecord(record, now, m.eventCount[agentName]+i)
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("append session exchange for %q: %w", agentName, err)
		}
	}
	m.eventCount[agentName] += len(records)
	return nil
}

func (m *MemoryManager) loadPersistedMessages(history *langmemory.ChatMessageHistory, agentName string) error {
	if m.storageDir == "" {
		return nil
	}
	if records, ok, err := m.loadSessionMessageRecords(agentName); err != nil {
		return err
	} else if ok {
		messages := make([]llms.ChatMessage, 0, len(records))
		for _, record := range records {
			messages = append(messages, messageFromRecord(record))
		}
		if err := history.SetMessages(context.Background(), messages); err != nil {
			return fmt.Errorf("restore session events for %q: %w", agentName, err)
		}
		m.eventCount[agentName] = len(records)
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
	m.eventCount[agentName] = len(records)
	return nil
}

func (m *MemoryManager) loadSessionMessageRecords(agentName string) ([]MessageRecord, bool, error) {
	file, err := os.Open(m.sessionEventsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read session events for %q: %w", agentName, err)
	}
	defer file.Close()

	var records []MessageRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0), 1<<20)
	for scanner.Scan() {
		var event SessionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, false, fmt.Errorf("decode session event for %q: %w", agentName, err)
		}
		record, ok := messageRecordFromSessionEvent(event)
		if ok {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("scan session events for %q: %w", agentName, err)
	}
	return records, true, nil
}

func (m *MemoryManager) persistSnapshot(agentName string, records []MessageRecord) error {
	if m.storageDir == "" {
		return nil
	}
	if err := os.MkdirAll(m.storageDir, 0o755); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	if err := m.appendSessionEvents(agentName, records); err != nil {
		return err
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
	for _, path := range []string{
		filepath.Join(m.storageDir, "session"),
		filepath.Join(m.storageDir, "long_term"),
		filepath.Join(m.storageDir, "lifecycle"),
	} {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove filesystem memory path %q for %q: %w", path, agentName, err)
		}
	}
	m.eventCount[agentName] = 0
	return nil
}

func (m *MemoryManager) maintainFilesystemMemory(ctx context.Context) error {
	if m.storageDir == "" {
		return nil
	}
	session := NewSessionMemoryStore(filepath.Join(m.storageDir, "session"))
	if _, err := os.Stat(session.eventsPath()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat session events: %w", err)
	}
	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}

	if !m.shouldCompress(len(events)) {
		return nil
	}

	promptTokens := m.LastPromptTokens()
	if m.logger != nil {
		m.logger.Info("[memory] compression triggered: events=%d, prompt_tokens=%d, context_window=%d, threshold=%d%%",
			len(events), promptTokens, m.extraction.ContextWindow, m.extraction.CompressAtPercent)
	}

	hotWindow := m.extraction.HotWindowEvents
	if hotWindow <= 0 {
		hotWindow = defaultMemoryHotWindowEvents
	}
	keepCount := hotWindow
	if keepCount > len(events) {
		keepCount = len(events) / 2
	}
	if keepCount < 4 {
		keepCount = 4
	}
	if keepCount >= len(events) {
		return nil
	}

	compactCount := len(events) - keepCount
	compactEvents := append([]SessionEvent(nil), events[:compactCount]...)
	hotEvents := append([]SessionEvent(nil), events[compactCount:]...)

	summary := summarizeSessionEvents(compactEvents)
	if m.summarizeFn != nil {
		if m.logger != nil {
			m.logger.Info("[memory] generating LLM summary for %d events", len(compactEvents))
		}
		if llmSummary := m.summarizeFn(ctx, compactEvents); llmSummary != "" {
			summary = llmSummary
			if m.logger != nil {
				m.logger.Info("[memory] LLM summary generated: %s", truncateForLog(summary, 80))
			}
		} else if m.logger != nil {
			m.logger.Info("[memory] LLM summary failed, using fallback")
		}
	}

	chunkSummary, err := session.compressEvents(ctx, compactEvents, CompressOption{
		Summary:  summary,
		Tags:     m.extraction.extractMemoryTags(compactEvents),
		Entities: m.extraction.extractMemoryEntities(compactEvents),
	})
	if err != nil {
		if m.logger != nil {
			m.logger.Error("[memory] compression failed: %v", err)
		}
		return err
	}
	if m.logger != nil {
		m.logger.Info("[memory] chunk created: id=%s, events=%d, keep=%d", chunkSummary.ID, compactCount, keepCount)
	}
	if err := session.replaceEvents(hotEvents); err != nil {
		return err
	}

	longTerm := NewLongTermMemoryStore(filepath.Join(m.storageDir, "long_term"), WithLifecycleDir(filepath.Join(m.storageDir, "lifecycle")), WithStoreProfileFn(m.profileFn))
	if err := longTerm.RegenerateProfileMD(ctx); err != nil {
		if m.logger != nil {
			m.logger.Error("[memory] profile.md regeneration failed: %v", err)
		}
		return err
	}
	if m.logger != nil {
		m.logger.Info("[memory] profile.md regenerated")
	}
	return nil
}

func (m *MemoryManager) shouldCompress(eventCount int) bool {
	lastPromptTokens := m.LastPromptTokens()
	if lastPromptTokens > 0 && m.extraction.ContextWindow > 0 {
		ratio := float64(lastPromptTokens) / float64(m.extraction.ContextWindow)
		threshold := float64(m.extraction.CompressAtPercent) / 100.0
		if ratio >= threshold {
			return true
		}
	}
	hotWindow := m.extraction.HotWindowEvents
	if hotWindow <= 0 {
		hotWindow = defaultMemoryHotWindowEvents
	}
	return eventCount > hotWindow
}

func summarizeSessionEvents(events []SessionEvent) string {
	parts := make([]string, 0, 3)
	for _, event := range events {
		content := strings.TrimSpace(event.Content)
		if content == "" {
			continue
		}
		parts = append(parts, truncateForLog(content, 80))
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Compressed %d session events.", len(events))
	}
	return strings.Join(parts, " / ")
}

func (cfg MemoryExtractionConfig) extractMemoryTags(events []SessionEvent) []string {
	seen := map[string]bool{}
	var tags []string
	for _, event := range events {
		for _, tag := range cfg.extractTagsFromText(event.Content) {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

func (cfg MemoryExtractionConfig) extractTagsFromText(content string) []string {
	tags := make([]string, 0)
	for _, candidate := range cfg.TagCandidates {
		if strings.Contains(content, candidate) {
			tags = append(tags, candidate)
		}
	}
	return tags
}

func (cfg MemoryExtractionConfig) extractMemoryEntities(events []SessionEvent) []string {
	seen := map[string]bool{}
	var entities []string
	for _, event := range events {
		if event.AppName != "" && !seen[event.AppName] {
			seen[event.AppName] = true
			entities = append(entities, event.AppName)
		}
		for _, entity := range cfg.extractEntitiesFromText(event.Content) {
			if !seen[entity] {
				seen[entity] = true
				entities = append(entities, entity)
			}
		}
	}
	return entities
}

func (cfg MemoryExtractionConfig) extractEntitiesFromText(content string) []string {
	var entities []string
	for _, suffix := range cfg.EntitySuffixes {
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

func (m *MemoryManager) memoryPath(agentName string) string {
	return filepath.Join(m.storageDir, memoryFileName(agentName))
}

func (m *MemoryManager) sessionEventsPath() string {
	return filepath.Join(m.storageDir, "session", "events.jsonl")
}

func (m *MemoryManager) appendSessionEvents(agentName string, records []MessageRecord) error {
	path := m.sessionEventsPath()
	start := m.eventCount[agentName]
	if _, err := os.Stat(path); os.IsNotExist(err) {
		start = 0
	} else if err != nil {
		return fmt.Errorf("stat session events for %q: %w", agentName, err)
	}
	if start >= len(records) {
		m.eventCount[agentName] = len(records)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session memory directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open session events for %q: %w", agentName, err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	now := time.Now().UTC()
	for i, record := range records[start:] {
		event := sessionEventFromRecord(record, now, start+i)
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("append session event for %q: %w", agentName, err)
		}
	}
	m.eventCount[agentName] = len(records)
	return nil
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

func sessionEventFromRecord(record MessageRecord, ts time.Time, offset int) SessionEvent {
	event := SessionEvent{
		EventID: "evt_" + strconv.FormatInt(ts.UnixNano(), 10) + "_" + strconv.Itoa(offset),
		Ts:      ts.Format(time.RFC3339Nano),
		Content: record.Content,
	}
	switch record.Role {
	case string(llms.ChatMessageTypeAI):
		event.Type = "assistant_output"
		event.Role = "assistant"
	case string(llms.ChatMessageTypeTool), string(llms.ChatMessageTypeFunction):
		event.Type = "tool_result"
		event.Role = "tool"
	case string(llms.ChatMessageTypeSystem):
		event.Type = "system_event"
		event.Role = "system"
	case string(llms.ChatMessageTypeHuman):
		fallthrough
	default:
		event.Type = "user_input"
		event.Role = "user"
	}
	return event
}

func messageRecordFromSessionEvent(event SessionEvent) (MessageRecord, bool) {
	switch event.Role {
	case "assistant":
		return MessageRecord{Role: string(llms.ChatMessageTypeAI), Content: event.Content}, true
	case "tool":
		return MessageRecord{Role: string(llms.ChatMessageTypeTool), Content: event.Content}, true
	case "system":
		return MessageRecord{Role: string(llms.ChatMessageTypeSystem), Content: event.Content}, true
	case "user":
		return MessageRecord{Role: string(llms.ChatMessageTypeHuman), Content: event.Content}, true
	default:
		return MessageRecord{}, false
	}
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
