package agent

import "context"

// SessionCommitter persists the durable session projection for a completed run.
type SessionCommitter interface {
	CommitRun(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error)
}

// SessionCommitRequest contains the run data needed to update session memory.
type SessionCommitRequest struct {
	AgentName string
	Input     string
	Output    string
	Steers    []RunSteerMessage
	Metrics   *RunMetrics
}

// SessionCommitResult contains the session snapshot exposed on RunResult.
type SessionCommitResult struct {
	Memory []MessageRecord
}

type memoryManagerSessionCommitter struct {
	memories *MemoryManager
}

func newMemoryManagerSessionCommitter(memories *MemoryManager) SessionCommitter {
	return memoryManagerSessionCommitter{memories: memories}
}

func (c memoryManagerSessionCommitter) CommitRun(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error) {
	if c.memories == nil {
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
	c.memories.SetLastPromptTokens(lastPromptTokens)

	if len(req.Steers) > 0 {
		if err := c.memories.AppendMessages(ctx, agentName, steeredExchangeRecords(req.Input, req.Steers, req.Output)); err != nil {
			return SessionCommitResult{}, err
		}
	} else {
		if err := c.memories.AppendExchange(ctx, agentName, req.Input, req.Output); err != nil {
			return SessionCommitResult{}, err
		}
	}

	memorySnapshot, err := c.memories.Snapshot(ctx, agentName)
	if err != nil {
		return SessionCommitResult{}, err
	}
	if err := c.memories.SaveSnapshot(ctx, agentName, memorySnapshot); err != nil {
		return SessionCommitResult{}, err
	}
	c.memories.RequestMaintenance()
	return SessionCommitResult{Memory: memorySnapshot}, nil
}
