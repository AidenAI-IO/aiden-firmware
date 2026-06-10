package agent

import (
	"bufio"
	"bytes"
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

const defaultLockTimeout = 10 * time.Second

// MemoryHandle wraps a langchain memory instance and its chat history.
type MemoryHandle struct {
	Memory  schema.Memory
	History *langmemory.ChatMessageHistory
}

// SummarizeFn generates a summary string from a list of session events.
// It is called during session compression to produce a human-readable
// summary of the events being archived into a chunk.
type SummarizeFn func(ctx context.Context, events []SessionEvent) string

// StructuredSummarizeFn generates typed chunk metadata from a list of session
// events. It augments, but does not replace, the plain text summary so older
// data and prompt rendering remain compatible.
type StructuredSummarizeFn func(ctx context.Context, events []SessionEvent) ChunkStructuredSummary

// ContextWindowFn returns the current model's context window in tokens. The
// memory manager calls it on every compression decision so model swaps take
// effect at runtime without restart. Implementations should return 0 when the
// window is unknown; callers fall back to the yaml-configured default.
type ContextWindowFn func() int

// MemoryManager maintains session memory, handling compression, chunk management,
// and long-term profile generation. It coordinates between in-memory chat history
// and persistent filesystem storage.
type MemoryManager struct {
	mu                    sync.Mutex
	handles               map[string]*MemoryHandle
	eventCount            map[string]int
	storageDir            string
	extraction            MemoryExtractionConfig
	lastPromptTokens      int
	summarizeFn           SummarizeFn
	structuredSummarizeFn StructuredSummarizeFn
	profileFn             ProfileFn
	contextWindowFn       ContextWindowFn
	profileDebouncer      *ProfileDebouncer
	lockTimeout           time.Duration
	logger                *Logger

	maintenanceMu      sync.Mutex
	maintenanceRunning bool
	maintenancePending bool
}

const defaultHotWindowEvents = 30

// MessageRecord represents a single message in the conversation history with
// its role and content.
type MessageRecord struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SessionEvent represents a single event in the session event stream, capturing
// conversation turns, tool calls, and system events with metadata.
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

// MemoryManagerOption configures a MemoryManager instance.
type MemoryManagerOption func(*MemoryManager)

// WithExtractionConfig sets the memory extraction configuration.
func WithExtractionConfig(cfg MemoryExtractionConfig) MemoryManagerOption {
	return func(m *MemoryManager) { m.extraction = normalizeMemoryExtractionConfig(cfg) }
}

// WithSummarizeFn sets the plain-text summarization function.
func WithSummarizeFn(fn SummarizeFn) MemoryManagerOption {
	return func(m *MemoryManager) { m.summarizeFn = fn }
}

// WithStructuredSummarizeFn sets the structured summarization function.
func WithStructuredSummarizeFn(fn StructuredSummarizeFn) MemoryManagerOption {
	return func(m *MemoryManager) { m.structuredSummarizeFn = fn }
}

// WithProfileFn sets the long-term profile generation function.
func WithProfileFn(fn ProfileFn) MemoryManagerOption {
	return func(m *MemoryManager) { m.profileFn = fn }
}

// WithMemoryProfileDebouncer sets the profile rebuild debouncer.
func WithMemoryProfileDebouncer(d *ProfileDebouncer) MemoryManagerOption {
	return func(m *MemoryManager) { m.profileDebouncer = d }
}

// WithMemoryLogger sets the logger for memory operations.
func WithMemoryLogger(logger *Logger) MemoryManagerOption {
	return func(m *MemoryManager) { m.logger = logger }
}

// WithContextWindowFn lets the runtime supply the active model's context
// window dynamically. When the callback returns a positive value it overrides
// the yaml-configured ContextWindow for compression decisions. A zero return
// means "unknown, use the yaml default". The yaml value remains the fallback.
func WithContextWindowFn(fn ContextWindowFn) MemoryManagerOption {
	return func(m *MemoryManager) { m.contextWindowFn = fn }
}

// NewMemoryManager creates a new MemoryManager with the specified storage
// directory and options.
func NewMemoryManager(storageDir string, opts ...MemoryManagerOption) *MemoryManager {
	manager := &MemoryManager{
		handles:     map[string]*MemoryHandle{},
		eventCount:  map[string]int{},
		extraction:  DefaultMemoryExtractionConfig(),
		lockTimeout: defaultLockTimeout,
	}
	if storageDir != "" {
		manager.storageDir = storageDir
	}
	for _, opt := range opts {
		opt(manager)
	}
	return manager
}

// SetLastPromptTokens updates the token count from the most recent prompt.
func (m *MemoryManager) SetLastPromptTokens(tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPromptTokens = tokens
}

// LastPromptTokens returns the token count from the most recent prompt.
func (m *MemoryManager) LastPromptTokens() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPromptTokens
}

// Get retrieves or creates a memory handle for the specified agent.
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

// Snapshot returns the current conversation history as message records.
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

// ClearSession clears the in-memory session and removes persisted session data.
func (m *MemoryManager) ClearSession(ctx context.Context, agentName string) error {
	m.mu.Lock()
	handle, ok := m.handles[agentName]
	if ok {
		if err := handle.Memory.Clear(ctx); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()

	return m.removeSessionPersisted(agentName)
}

// ClearAll clears all memory including session, long-term, and episodic data.
func (m *MemoryManager) ClearAll(ctx context.Context, agentName string) error {
	m.mu.Lock()
	handle, ok := m.handles[agentName]
	if ok {
		if err := handle.Memory.Clear(ctx); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()

	return m.removeAllPersisted(agentName)
}

// Save persists the current memory snapshot and triggers maintenance.
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

// SaveSnapshot persists a given snapshot of message records.
func (m *MemoryManager) SaveSnapshot(ctx context.Context, agentName string, records []MessageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.persistSnapshot(agentName, records)
}

// RequestMaintenance schedules asynchronous memory maintenance.
func (m *MemoryManager) RequestMaintenance() {
	if m.storageDir == "" {
		return
	}

	m.maintenanceMu.Lock()
	if m.maintenanceRunning {
		m.maintenancePending = true
		m.maintenanceMu.Unlock()
		return
	}
	m.maintenanceRunning = true
	m.maintenanceMu.Unlock()

	go m.maintenanceLoop()
}

// WaitMaintenance blocks until maintenance completes or context is cancelled.
func (m *MemoryManager) WaitMaintenance(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		m.maintenanceMu.Lock()
		running := m.maintenanceRunning
		m.maintenanceMu.Unlock()
		if !running {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *MemoryManager) maintenanceLoop() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		err := m.maintainFilesystemMemory(ctx)
		cancel()
		if err != nil && m.logger != nil {
			m.logger.Error("[memory] async maintenance failed: %v", err)
		}

		m.maintenanceMu.Lock()
		if !m.maintenancePending {
			m.maintenanceRunning = false
			m.maintenanceMu.Unlock()
			return
		}
		m.maintenancePending = false
		m.maintenanceMu.Unlock()
	}
}

// AppendExchange appends a user input and assistant output pair to the session.
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
		// Hot-window boundary markers are NOT stored here. They are synthetic
		// prompt-construction artifacts and must never enter the persistable
		// ChatMessageHistory: Snapshot() reads history verbatim and
		// appendSessionEvents() writes records by index, so a stored marker
		// would desync eventCount from the real session events and get
		// persisted or cause duplicate appends. Markers are injected at
		// prompt-build time instead (see hotWindowBoundaryMemory).
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

	fl := NewFileLock(m.storageDir)
	if err := fl.Lock(m.lockTimeout); err != nil {
		return fmt.Errorf("lock for loading memory %q: %w", agentName, err)
	}
	defer fl.Unlock()

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
	fl := NewFileLock(m.storageDir)
	if err := fl.Lock(m.lockTimeout); err != nil {
		return nil, false, fmt.Errorf("lock for loading session events %q: %w", agentName, err)
	}
	defer fl.Unlock()

	file, err := os.Open(m.sessionEventsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read session events for %q: %w", agentName, err)
	}
	defer file.Close()

	var records []MessageRecord
	validData := make([]byte, 0)
	repairedTruncatedTail := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0), 1<<20)
	for scanner.Scan() {
		line := bytes.Trim(scanner.Bytes(), "\x00 \t\r\n")
		if len(line) == 0 {
			continue
		}
		var event SessionEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if isTruncatedJSONLineError(err) {
				repairedTruncatedTail = true
				break
			}
			return nil, false, fmt.Errorf("decode session event for %q: %w", agentName, err)
		}
		validData = append(validData, line...)
		validData = append(validData, '\n')
		record, ok := messageRecordFromSessionEvent(event)
		if ok {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("scan session events for %q: %w", agentName, err)
	}
	if repairedTruncatedTail {
		if err := writeFileAtomic(m.sessionEventsPath(), validData, 0o644); err != nil {
			if m.logger != nil {
				m.logger.Warn("[memory] failed to repair truncated session events for %q: %v", agentName, err)
			}
		} else if m.logger != nil {
			m.logger.Warn("[memory] repaired truncated session events for %q", agentName)
		}
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

	fl := NewFileLock(m.storageDir)
	if err := fl.Lock(m.lockTimeout); err != nil {
		return fmt.Errorf("lock for persisting memory %q: %w", agentName, err)
	}
	defer fl.Unlock()

	records = sanitizeMessageRecords(records)
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

func sanitizeMessageRecords(records []MessageRecord) []MessageRecord {
	out := make([]MessageRecord, len(records))
	for i, record := range records {
		record.Content = stripScreenshotData(record.Content)
		out[i] = record
	}
	return out
}

func (m *MemoryManager) removeSessionPersisted(agentName string) error {
	if m.storageDir == "" {
		return nil
	}
	if err := os.Remove(m.memoryPath(agentName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove persisted memory for %q: %w", agentName, err)
	}
	if err := os.RemoveAll(filepath.Join(m.storageDir, "session")); err != nil {
		return fmt.Errorf("remove session memory for %q: %w", agentName, err)
	}
	m.mu.Lock()
	m.eventCount[agentName] = 0
	m.mu.Unlock()
	return nil
}

func (m *MemoryManager) removeAllPersisted(agentName string) error {
	if m.storageDir == "" {
		return nil
	}

	fl := NewFileLock(m.storageDir)
	if err := fl.Lock(m.lockTimeout); err != nil {
		return fmt.Errorf("lock for removing memory %q: %w", agentName, err)
	}
	defer fl.Unlock()

	if err := os.Remove(m.memoryPath(agentName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove persisted memory for %q: %w", agentName, err)
	}
	for _, path := range []string{
		filepath.Join(m.storageDir, "session"),
		filepath.Join(m.storageDir, "long_term"),
		filepath.Join(m.storageDir, "device"),
		filepath.Join(m.storageDir, "episodes"),
		filepath.Join(m.storageDir, "lifecycle"),
	} {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove filesystem memory path %q for %q: %w", path, agentName, err)
		}
	}
	m.mu.Lock()
	m.eventCount[agentName] = 0
	m.mu.Unlock()
	return nil
}

func (m *MemoryManager) maintainFilesystemMemory(ctx context.Context) error {
	if m.storageDir == "" {
		return nil
	}
	session := NewSessionMemoryStore(filepath.Join(m.storageDir, "session"), m.extraction.SummaryMaxChunks)
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
	contextWindow := m.effectiveContextWindow()
	if m.logger != nil {
		m.logger.Info("[memory] compression triggered: events=%d, prompt_tokens=%d, context_window=%d, threshold=%d%%",
			len(events), promptTokens, contextWindow, m.extraction.CompressAtPercent)
	}

	plan := m.planCompaction(events, contextWindow)
	if !plan.ok {
		return nil
	}

	compactEvents := append([]SessionEvent(nil), events[:plan.cutIndex]...)
	hotEvents := append([]SessionEvent(nil), events[plan.cutIndex:]...)

	// History summary covers everything compacted out. When splitting a turn,
	// the prefix (turn start → cut) is summarized separately and merged so the
	// retained suffix keeps the context of the half-finished turn.
	historyEvents := compactEvents
	var turnPrefixEvents []SessionEvent
	if plan.isSplitTurn {
		historyEvents = append([]SessionEvent(nil), events[:plan.turnStartIndex]...)
		turnPrefixEvents = append([]SessionEvent(nil), events[plan.turnStartIndex:plan.cutIndex]...)
	}

	summary, structured := m.buildEventSummary(ctx, historyEvents)
	if plan.isSplitTurn && len(turnPrefixEvents) > 0 {
		prefixSummary := m.buildTurnPrefixSummary(ctx, turnPrefixEvents)
		if strings.TrimSpace(prefixSummary) != "" {
			if strings.TrimSpace(summary) == "" {
				summary = "No prior history."
			}
			summary = summary + "\n\n---\n\nTurn Context (split turn):\n" + prefixSummary
			// Keep the structured summary's primary text consistent with the
			// merged plain summary so downstream renders agree.
			if strings.TrimSpace(structured.Summary) != "" {
				structured.Summary = summary
			}
			// INTENTIONAL DUPLICATION: The prefix summary is written to two places:
			// 1. Merged into the chunk's summary field (persisted to disk for recall)
			// 2. Prepended as a system_event in the hot window (live context for LLM)
			// This ensures both historical retrieval and immediate prompt context
			// include the turn context, preventing the hot window from opening on
			// a dangling assistant/tool result without the user input that triggered it.
			hotEvents = prependTurnPrefixContext(hotEvents, prefixSummary)
		}
	}

	cutMeta := ChunkCutMetadata{
		FirstKeptEventID:   firstNonEmptyEventID(events, plan.cutIndex),
		TokensBefore:       sumSessionEventTokens(events),
		KeptTokensEstimate: sumSessionEventTokens(hotEvents),
		IsSplitTurn:        plan.isSplitTurn,
	}
	if plan.isSplitTurn {
		cutMeta.TurnStartEventID = firstNonEmptyEventID(events, plan.turnStartIndex)
	}

	chunkSummary, err := session.compressEvents(ctx, compactEvents, CompressOption{
		Summary:    summary,
		Structured: structured,
		Tags:       m.extraction.extractMemoryTags(compactEvents),
		Entities:   m.extraction.extractMemoryEntities(compactEvents),
		CutMeta:    cutMeta,
	})
	if err != nil {
		if m.logger != nil {
			m.logger.Error("[memory] compression failed: %v", err)
		}
		return err
	}
	if m.logger != nil {
		m.logger.Info("[memory] chunk created: id=%s, compacted=%d, kept=%d, split_turn=%t, mode=%s",
			chunkSummary.ID, len(compactEvents), len(hotEvents), plan.isSplitTurn, plan.mode)
	}
	if err := session.replaceEvents(hotEvents); err != nil {
		return err
	}

	// Update lastPromptTokens to the estimated size of the hot window after
	// compression. This prevents spurious re-compression: the maintenanceLoop
	// checks maintenancePending and may run again immediately; if we left
	// lastPromptTokens at the pre-compression high value, shouldCompress would
	// continue returning true and trigger redundant compaction rounds.
	m.SetLastPromptTokens(cutMeta.KeptTokensEstimate)

	longTerm := NewLongTermMemoryStore(filepath.Join(m.storageDir, "long_term"), WithLifecycleDir(filepath.Join(m.storageDir, "lifecycle")), WithStoreProfileFn(m.profileFn), WithProfileDebouncer(m.profileDebouncer))
	longTerm.RequestProfileRebuild()
	if m.logger != nil {
		m.logger.Info("[memory] profile.md regenerated")
	}
	return nil
}

// compactionPlan describes the chosen split of the event stream.
type compactionPlan struct {
	ok             bool
	cutIndex       int  // first kept event index; events[:cutIndex] are compacted
	isSplitTurn    bool // cut falls inside a turn
	turnStartIndex int  // user_input opening the split turn (valid when isSplitTurn)
	mode           string
}

// planCompaction decides where to split events. It prefers a token-based cut
// point honouring the reserve/keep-recent budgets; when no token-driven cut is
// warranted (e.g. a few small events tripped the percentage trigger) it falls
// back to the legacy count-based hot window so compaction still makes progress.
func (m *MemoryManager) planCompaction(events []SessionEvent, contextWindow int) compactionPlan {
	// clampTokenBudgets couples reserve and keep-recent against the window, but
	// the cut location only depends on keep-recent: reserve is response headroom
	// consumed by shouldCompress's trigger check, not by where we split. We take
	// the clamped keep-recent so both call sites share one clamping rule.
	_, keepRecent := clampTokenBudgets(m.reserveTokens(), m.keepRecentTokens(), contextWindow)
	cut := findSessionCutPoint(events, 0, len(events), keepRecent)
	if cut.HasCut && cut.FirstKeptIndex > 0 && cut.FirstKeptIndex < len(events) {
		return compactionPlan{
			ok:             true,
			cutIndex:       cut.FirstKeptIndex,
			isSplitTurn:    cut.IsSplitTurn,
			turnStartIndex: cut.TurnStartIndex,
			mode:           "token",
		}
	}

	// Count-based fallback: keep roughly the most recent maxEvents events, but
	// snap the cut to a legal boundary so the hot window never opens on a
	// forbidden event (tool_result/system). The raw count index is only a
	// target; buildSessionCutPoint then derives the same split-turn metadata and
	// leading-state merge the token path uses.
	maxEvents := m.hotWindowEvents()
	keepCount := maxEvents
	if keepCount > len(events) {
		keepCount = len(events) / 2
	}
	if keepCount < 4 {
		keepCount = 4
	}
	if keepCount >= len(events) {
		return compactionPlan{ok: false}
	}
	rawCutIndex := len(events) - keepCount
	snapped := snapToLegalCutAtOrBefore(events, 0, len(events), rawCutIndex)
	if snapped < 0 {
		// No legal cut anywhere; compaction can't proceed without orphaning.
		return compactionPlan{ok: false}
	}
	cp := buildSessionCutPoint(events, 0, snapped)
	if !cp.HasCut {
		return compactionPlan{ok: false}
	}
	return compactionPlan{
		ok:             true,
		cutIndex:       cp.FirstKeptIndex,
		isSplitTurn:    cp.IsSplitTurn,
		turnStartIndex: cp.TurnStartIndex,
		mode:           "count",
	}
}

func (m *MemoryManager) reserveTokens() int {
	if m.extraction.ReserveTokens > 0 {
		return m.extraction.ReserveTokens
	}
	return defaultReserveTokens
}

func (m *MemoryManager) keepRecentTokens() int {
	if m.extraction.KeepRecentTokens > 0 {
		return m.extraction.KeepRecentTokens
	}
	return defaultKeepRecentTokens
}

func (m *MemoryManager) hotWindowEvents() int {
	if m.extraction.HotWindowEvents > 0 {
		return m.extraction.HotWindowEvents
	}
	return defaultHotWindowEvents
}

func (m *MemoryManager) countCompressAfterEvents() int {
	hotWindow := m.hotWindowEvents()
	threshold := m.extraction.CountCompressAfterEvents
	if threshold <= hotWindow {
		threshold = hotWindow * 2
	}
	return threshold
}

// buildEventSummary runs the structured → plain → local fallback cascade over a
// set of events and returns the resulting plain summary plus any structured
// summary. Empty input yields an empty summary so split-turn callers can detect
// "no prior history".
func (m *MemoryManager) buildEventSummary(ctx context.Context, events []SessionEvent) (string, ChunkStructuredSummary) {
	if len(events) == 0 {
		return "", ChunkStructuredSummary{}
	}
	summary := summarizeSessionEvents(events)
	structured := ChunkStructuredSummary{}
	usedStructured := false
	if m.structuredSummarizeFn != nil {
		if m.logger != nil {
			m.logger.Info("[memory] generating structured LLM summary for %d events", len(events))
		}
		if llmStructured := m.structuredSummarizeFn(ctx, events); strings.TrimSpace(llmStructured.Summary) != "" {
			structured = llmStructured
			structured.Summary = strings.TrimSpace(structured.Summary)
			usedStructured = true
			summary = structured.Summary
			if m.logger != nil {
				m.logger.Info("[memory] structured LLM summary generated: %s", truncateForLog(summary, 80))
			}
		} else if m.logger != nil {
			m.logger.Info("[memory] structured LLM summary failed, falling back to plain summarizer")
		}
	}
	if !usedStructured && m.summarizeFn != nil {
		if m.logger != nil {
			m.logger.Info("[memory] generating LLM summary for %d events", len(events))
		}
		if llmSummary := m.summarizeFn(ctx, events); llmSummary != "" {
			summary = llmSummary
			if m.logger != nil {
				m.logger.Info("[memory] LLM summary generated: %s", truncateForLog(summary, 80))
			}
		} else if m.logger != nil {
			m.logger.Info("[memory] LLM summary failed, using fallback")
		}
	}
	return summary, structured
}

// buildTurnPrefixSummary summarizes the prefix of a split turn. It prefers the
// plain summarizer (the structured prompt is tuned for whole-history context)
// and falls back to the local heuristic summarizer.
func (m *MemoryManager) buildTurnPrefixSummary(ctx context.Context, events []SessionEvent) string {
	if len(events) == 0 {
		return ""
	}
	if m.summarizeFn != nil {
		if s := m.summarizeFn(ctx, events); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return summarizeSessionEvents(events)
}

// prependTurnPrefixContext inserts a synthetic system event carrying the
// split-turn prefix summary at the head of the hot window, so the retained
// events do not begin with a dangling assistant/tool result.
func prependTurnPrefixContext(hotEvents []SessionEvent, prefixSummary string) []SessionEvent {
	ctxEvent := SessionEvent{
		EventID: "evt_split_" + strconvTimeID(time.Now().UTC()),
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Type:    "system_event",
		Role:    "system",
		Content: "Turn Context (split turn):\n" + prefixSummary,
	}
	return append([]SessionEvent{ctxEvent}, hotEvents...)
}

func (m *MemoryManager) shouldCompress(eventCount int) bool {
	lastPromptTokens := m.LastPromptTokens()
	contextWindow := m.effectiveContextWindow()
	if lastPromptTokens > 0 && contextWindow > 0 {
		// Reuse clampTokenBudgets so the reserve ceiling (never more than half
		// the window) stays defined in one place. Only the reserve half matters
		// for the trigger decision; keep-recent governs the cut location and is
		// consumed by planCompaction instead.
		reserve, _ := clampTokenBudgets(m.reserveTokens(), m.keepRecentTokens(), contextWindow)
		if lastPromptTokens >= contextWindow-reserve {
			return true
		}
		ratio := float64(lastPromptTokens) / float64(contextWindow)
		threshold := float64(m.extraction.CompressAtPercent) / 100.0
		if ratio >= threshold {
			return true
		}
		// Token data available but thresholds not reached - don't compress
		return false
	}
	// Event count fallback: only used when token data unavailable (cold start,
	// unknown model, or before first LLM call).
	return eventCount > m.countCompressAfterEvents()
}

// effectiveContextWindow returns the context window in tokens that should be
// used for compression decisions. It prefers the resolver-supplied callback
// (so model swaps take effect at runtime); a zero from the callback means
// "unknown" and falls back to the yaml-configured default.
func (m *MemoryManager) effectiveContextWindow() int {
	if m.contextWindowFn != nil {
		if w := m.contextWindowFn(); w > 0 {
			return w
		}
	}
	return m.extraction.ContextWindow
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

func (m *MemoryManager) hasCompressedHistory() bool {
	if m.storageDir == "" {
		return false
	}
	summaryPath := filepath.Join(m.storageDir, "session", "summary.md")
	_, err := os.Stat(summaryPath)
	return err == nil
}

// HasCompressedHistory reports whether earlier conversation history has been
// compressed into summaries, meaning the live chat history is only a hot
// window. Callers use this to decide whether to bracket the hot window with
// boundary markers at prompt-build time.
func (m *MemoryManager) HasCompressedHistory() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasCompressedHistory()
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

func isTruncatedJSONLineError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unexpected end of JSON input")
}

func sessionEventFromRecord(record MessageRecord, ts time.Time, offset int) SessionEvent {
	// Strip screenshot base64 payloads before they reach events.jsonl so the
	// hot-window token estimate (which doesn't parse JSON) never sees a several-KB
	// base64 string that it would count as pure ASCII (chars/4). Keeps
	// width/height/format/size metadata intact. This is the primary defense against
	// screenshot data inflating the session events; SessionMemoryStore.AppendEvent
	// is the secondary path for direct writes that bypass this conversion.
	content := stripScreenshotData(record.Content)

	event := SessionEvent{
		EventID: "evt_" + strconv.FormatInt(ts.UnixNano(), 10) + "_" + strconv.Itoa(offset),
		Ts:      ts.Format(time.RFC3339Nano),
		Content: content,
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
