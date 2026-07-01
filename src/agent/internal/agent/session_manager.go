package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// SessionManager owns the runtime session lifecycle for a run. BeginRun handles
// run-start session boundary detection/rotation, and CommitRun persists the
// completed turn into the active session projection.
type SessionManager interface {
	BeginRun(ctx context.Context, req SessionBeginRequest) (SessionBeginResult, error)
	CommitRun(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error)
}

// SessionBeginRequest contains the run data needed before prompt construction.
type SessionBeginRequest struct {
	AgentName    string
	Input        string
	Turn         TurnInput
	RuntimeID    string
	EpisodeID    string
	RequestID    string
	RunID        string
	CurrentHints CurrentEnvironmentHints
}

// SessionBeginResult contains session state computed before prompt construction.
type SessionBeginResult struct {
	Boundary sessionBoundaryTelemetry
}

type sessionBoundaryTelemetry struct {
	Decision             string
	Reason               string
	Rotated              bool
	PendingRecallCounter *atomic.Int64
}

// SessionCommitRequest contains the run data needed to update session memory.
type SessionCommitRequest struct {
	AgentName string
	Input     string
	Output    string
	Steers    []RunSteerMessage
	Metrics   *RunMetrics
	RuntimeID string
	EpisodeID string
	RequestID string
	RunID     string
}

// SessionCommitResult contains the session snapshot exposed on RunResult.
type SessionCommitResult struct {
	Memory []MessageRecord
}

type memoryManagerSessionManager struct {
	memories       *MemoryManager
	episodeContext func() BoundaryEpisodeContext
}

func newMemoryManagerSessionManager(
	memories *MemoryManager,
	episodeContext func() BoundaryEpisodeContext,
) SessionManager {
	return memoryManagerSessionManager{memories: memories, episodeContext: episodeContext}
}

func (m memoryManagerSessionManager) BeginRun(ctx context.Context, req SessionBeginRequest) (SessionBeginResult, error) {
	if err := ctx.Err(); err != nil {
		return SessionBeginResult{}, err
	}
	turn := normalizeTurnInput(req.Turn)
	if turn.InputText == "" {
		turn = NewTextTurnInput(req.Input, nil)
	}
	boundary := m.handleSessionBoundary(turn.InputText)
	if m.memories != nil {
		agentName := req.AgentName
		if agentName == "" {
			agentName = "default"
		}
		meta := SessionEventMetadata{
			RuntimeID: req.RuntimeID,
			EpisodeID: req.EpisodeID,
			RequestID: req.RequestID,
			RunID:     req.RunID,
		}
		if err := m.memories.AppendSessionEvent(ctx, agentName, sessionEventFromTurnInput(turn), meta); err != nil {
			return SessionBeginResult{}, err
		}
	}
	return SessionBeginResult{Boundary: boundary}, nil
}

func (m memoryManagerSessionManager) handleSessionBoundary(input string) sessionBoundaryTelemetry {
	var telemetry sessionBoundaryTelemetry
	if m.memories == nil || m.memories.storageDir == "" {
		return telemetry
	}
	cfg := m.memories.extraction
	if !cfg.SessionBoundaryEnabled {
		return telemetry
	}
	events, err := loadLastNSessionEvents(m.memories, 20)
	if err != nil {
		if m.memories.logger != nil {
			m.memories.logger.Warn("[memory] session boundary load failed: %v", err)
		}
		return telemetry
	}
	boundaryCfg := BoundaryConfig{
		ShortGapSeconds:            cfg.SessionBoundaryShortGapSeconds,
		LongGapSeconds:             cfg.SessionBoundaryLongGapSeconds,
		SmallSessionEventThreshold: DefaultBoundaryConfig().SmallSessionEventThreshold,
		ContinueScoreThreshold:     DefaultBoundaryConfig().ContinueScoreThreshold,
	}
	now := time.Now().UTC()
	episodeCtx := BoundaryEpisodeContext{}
	if m.episodeContext != nil {
		episodeCtx = m.episodeContext()
	}
	boundary, reason := ClassifyTurnBoundary(events, input, now, boundaryCfg, episodeCtx)
	telemetry.Decision = boundary
	telemetry.Reason = reason

	if boundary != BoundaryNew || len(events) == 0 {
		// No rotation happens here (either a "continue" decision, or "new" with
		// no prior events to archive). Log at Debug so the decision and its
		// reason are observable on every turn without spamming Info — otherwise
		// a wrongly-kept session is silent and only diagnosable by code archaeology.
		if m.memories.logger != nil {
			m.memories.logger.Debug("[memory] session boundary: decision=%s reason=%s (no rotation)", boundary, reason)
		}
		return telemetry
	}

	rotation, err := m.memories.RotateSessionEventsDetailed()
	if err != nil {
		if m.memories.logger != nil {
			m.memories.logger.Warn("[memory] session rotation failed: %v", err)
		}
		return telemetry
	}
	if rotation.ArchiveDir != "" {
		telemetry.Rotated = true
	}
	if m.memories.logger != nil {
		logProminentSessionStart(m.memories.logger, rotation, reason)
	}
	return telemetry
}

func logProminentSessionStart(logger *Logger, rotation SessionRotationResult, reason string) {
	if logger == nil || rotation.ActiveSessionID == "" {
		return
	}
	logger.Info("========================================")
	logger.Info("NEW SESSION STARTED")
	logger.Info("Session ID: %s", rotation.ActiveSessionID)
	logger.Info("Reason: %s", reason)
	if rotation.ClosedSessionID != "" {
		logger.Info("Closed Session ID: %s", rotation.ClosedSessionID)
	}
	if rotation.ArchiveDir != "" {
		logger.Info("Archive: %s", filepath.Base(rotation.ArchiveDir))
	}
	logger.Info("========================================")
}

func (m memoryManagerSessionManager) CommitRun(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error) {
	if m.memories == nil {
		return SessionCommitResult{}, nil
	}
	agentName := req.AgentName
	if agentName == "" {
		agentName = "default"
	}
	lastPromptTokens := 0
	if req.Metrics != nil {
		lastPromptTokens = req.Metrics.LastPromptTokens
	}
	if floor := m.memories.ConsumePromptTokenFloor(); floor > lastPromptTokens {
		lastPromptTokens = floor
	}
	m.memories.SetLastPromptTokens(lastPromptTokens)

	meta := SessionEventMetadata{
		RuntimeID: req.RuntimeID,
		EpisodeID: req.EpisodeID,
		RequestID: req.RequestID,
		RunID:     req.RunID,
	}
	records := []MessageRecord{{Role: "ai", Content: req.Output}}
	if len(records) > 0 {
		if err := m.memories.AppendMessagesWithMetadata(ctx, agentName, records, meta); err != nil {
			return SessionCommitResult{}, err
		}
	}

	memorySnapshot, err := m.memories.Snapshot(ctx, agentName)
	if err != nil {
		return SessionCommitResult{}, err
	}
	m.memories.RequestMaintenance()
	return SessionCommitResult{Memory: memorySnapshot}, nil
}

func loadLastNSessionEvents(manager *MemoryManager, n int) ([]SessionEvent, error) {
	if manager == nil || strings.TrimSpace(manager.storageDir) == "" || n <= 0 {
		return nil, nil
	}
	session := NewSessionMemoryStore(filepath.Join(manager.storageDir, "session"), manager.extraction.SummaryMaxChunks)
	fl := NewFileLock(manager.storageDir)
	if err := fl.Lock(manager.lockTimeout); err != nil {
		return nil, fmt.Errorf("lock for boundary session events: %w", err)
	}
	defer fl.Unlock()
	result, err := session.readActiveEventsRepairingTruncatedTail()
	if err != nil {
		if isPathNotExistError(err) {
			return nil, nil
		}
		return nil, err
	}
	manager.logSessionEventsRepair("boundary", result)
	events := result.events
	if len(events) <= n {
		return events, nil
	}
	return append([]SessionEvent(nil), events[len(events)-n:]...), nil
}

func recentEpisodeContext(plane MemoryPlane) BoundaryEpisodeContext {
	var ctx BoundaryEpisodeContext
	fs, ok := plane.(*FilesystemMemoryPlane)
	if !ok || fs == nil || fs.episodes == nil {
		return ctx
	}
	index, err := fs.episodes.loadIndex()
	if err != nil {
		return ctx
	}
	for _, entry := range index.Episodes {
		if entry.Status == "running" {
			ctx.HasRunning = true
			return ctx
		}
	}
	return ctx
}
