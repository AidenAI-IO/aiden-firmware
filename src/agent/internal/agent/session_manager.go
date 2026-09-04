package agent

import (
	"context"
	"sync/atomic"
)

// SessionManager owns the runtime session lifecycle for a run.
//
// Session rotation is driven solely by context compaction: when the transcript
// outgrows the model's context window, the Compactor writes the compacted span
// to a chunk and starts a new ContextManager revision. There is no separate
// heuristic that decides a user turn opens a "new session".
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
	// PendingRecallCounter counts session-chunk recalls made during the run so
	// the Episode can record whether the agent consulted compressed history.
	PendingRecallCounter *atomic.Int64
}

// SessionCommitRequest contains the run data reported when a run finishes.
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

type SessionCommitResult struct{}

type memoryManagerSessionManager struct {
	memories *MemoryManager
}

func newMemoryManagerSessionManager(memories *MemoryManager) SessionManager {
	return memoryManagerSessionManager{memories: memories}
}

func (m memoryManagerSessionManager) BeginRun(ctx context.Context, req SessionBeginRequest) (SessionBeginResult, error) {
	if err := ctx.Err(); err != nil {
		return SessionBeginResult{}, err
	}
	// The ContextManager transcript is the conversation record; a run begin has
	// no separate session projection to update.
	return SessionBeginResult{PendingRecallCounter: &atomic.Int64{}}, nil
}

func (m memoryManagerSessionManager) CommitRun(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error) {
	if err := ctx.Err(); err != nil {
		return SessionCommitResult{}, err
	}
	// Long-term memory tools write through their own store during the run; the
	// profile is rebuilt (debounced) so a turn that saved memories is reflected.
	if m.memories != nil {
		m.memories.RequestProfileRebuild()
	}
	return SessionCommitResult{}, nil
}
