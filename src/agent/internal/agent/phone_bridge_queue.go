package agent

import (
	"fmt"
	"sync"
	"time"
)

const (
	// CommandQueueTTL is the maximum time a command stays in the queue
	CommandQueueTTL = 5 * time.Minute

	// ResultRetentionTTL is how long completed results are kept
	ResultRetentionTTL = 2 * time.Minute

	// CommandInFlightTimeout is the max time between poll and result submission
	CommandInFlightTimeout = 30 * time.Second

	// CleanupInterval is how often the background cleanup runs
	CleanupInterval = 1 * time.Minute

	// MaxRetries is the number of times a command can be retried after timeout
	MaxRetries = 3
)

// CommandStatus represents the lifecycle state of a command
type CommandStatus string

const (
	StatusQueued    CommandStatus = "queued"
	StatusInFlight  CommandStatus = "in_flight"
	StatusCompleted CommandStatus = "completed"
	StatusTimeout   CommandStatus = "timeout"
	StatusExpired   CommandStatus = "expired"
)

// QueuedCommand wraps a BridgeCommand with queue metadata
type QueuedCommand struct {
	Command    BridgeCommand `json:"command"`
	Status     CommandStatus `json:"status"`
	QueuedAt   time.Time     `json:"queued_at"`
	InFlightAt *time.Time    `json:"in_flight_at,omitempty"`
	ExpireAt   time.Time     `json:"expire_at"`
	RetryCount int           `json:"retry_count"`
	MaxRetries int           `json:"max_retries"`
}

// CommandResult holds the execution result of a command
type CommandResult struct {
	Response    BridgeCommandResponse `json:"response"`
	CompletedAt time.Time             `json:"completed_at"`
	TTL         time.Duration         `json:"-"`
}

// CommandQueue manages pending commands and their results
type CommandQueue struct {
	mu         sync.RWMutex
	commands   map[string]*QueuedCommand
	results    map[string]*CommandResult
	commandIDs []string // maintains insertion order for FIFO polling
	logger     *Logger
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// NewCommandQueue creates a new command queue and starts the cleanup goroutine
func NewCommandQueue(logger *Logger) *CommandQueue {
	q := &CommandQueue{
		commands: make(map[string]*QueuedCommand),
		results:  make(map[string]*CommandResult),
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
	q.wg.Add(1)
	go q.cleanupLoop()
	return q
}

// Stop halts the cleanup goroutine
func (q *CommandQueue) Stop() {
	close(q.stopCh)
	q.wg.Wait()
}

// Enqueue adds a command to the queue
func (q *CommandQueue) Enqueue(cmd BridgeCommand) error {
	if cmd.ID == "" {
		return fmt.Errorf("command ID must not be empty")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.commands[cmd.ID]; exists {
		return fmt.Errorf("command %s already exists", cmd.ID)
	}

	now := time.Now()
	q.commands[cmd.ID] = &QueuedCommand{
		Command:    cmd,
		Status:     StatusQueued,
		QueuedAt:   now,
		ExpireAt:   now.Add(CommandQueueTTL),
		MaxRetries: MaxRetries,
	}
	q.commandIDs = append(q.commandIDs, cmd.ID)

	if q.logger != nil {
		q.logger.Info("phone-bridge-queue: enqueued command %s (type=%s)", cmd.ID, cmd.Type)
	}

	return nil
}

// Get retrieves a queued command by ID (for status checking after enqueue)
func (q *CommandQueue) Get(commandID string) *QueuedCommand {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.commands[commandID]
}

// Poll retrieves up to `limit` queued commands and marks them as in-flight
func (q *CommandQueue) Poll(platform string, limit int) []BridgeCommand {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	var commands []BridgeCommand
	now := time.Now()

	for _, cmdID := range q.commandIDs {
		cmd, exists := q.commands[cmdID]
		if !exists || cmd.Status != StatusQueued {
			continue
		}

		// Platform filtering
		if !q.matchesPlatform(&cmd.Command, platform) {
			continue
		}

		// Mark as in-flight
		cmd.Status = StatusInFlight
		inFlightAt := now
		cmd.InFlightAt = &inFlightAt

		commands = append(commands, cmd.Command)

		if len(commands) >= limit {
			break
		}
	}

	if q.logger != nil && len(commands) > 0 {
		q.logger.Info("phone-bridge-queue: polled %d command(s) for platform=%s", len(commands), platform)
	}

	return commands
}

// matchesPlatform checks if a command is relevant for the given platform
func (q *CommandQueue) matchesPlatform(cmd *BridgeCommand, platform string) bool {
	// If no platform-specific fields, command applies to all platforms
	if len(cmd.IOSURLs) == 0 && len(cmd.AndroidPackages) == 0 {
		return true
	}

	switch platform {
	case "ios":
		return len(cmd.IOSURLs) > 0
	case "android":
		return len(cmd.AndroidPackages) > 0
	default:
		return true
	}
}

// SubmitResult records the execution result and removes the command from the queue
func (q *CommandQueue) SubmitResult(resp BridgeCommandResponse) error {
	if resp.ID == "" {
		return fmt.Errorf("response ID must not be empty")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	_, exists := q.commands[resp.ID]
	if !exists {
		return fmt.Errorf("command %s not found", resp.ID)
	}

	// Remove from commands map
	delete(q.commands, resp.ID)

	// Remove from commandIDs slice
	for i, id := range q.commandIDs {
		if id == resp.ID {
			q.commandIDs = append(q.commandIDs[:i], q.commandIDs[i+1:]...)
			break
		}
	}

	// Store result
	q.results[resp.ID] = &CommandResult{
		Response:    resp,
		CompletedAt: time.Now(),
		TTL:         ResultRetentionTTL,
	}

	if q.logger != nil {
		ok := resp.Error == nil
		q.logger.Info("phone-bridge-queue: result submitted for command %s (ok=%v)", resp.ID, ok)
	}

	return nil
}

// QueryResult retrieves the result or status of a command
func (q *CommandQueue) QueryResult(commandID string) (*CommandResult, CommandStatus) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	// Check results first
	if result, ok := q.results[commandID]; ok {
		return result, StatusCompleted
	}

	// Check pending commands
	if cmd, ok := q.commands[commandID]; ok {
		return nil, cmd.Status
	}

	// Not found means expired or never existed
	return nil, StatusExpired
}

// cleanupLoop runs periodically to expire old commands and results
func (q *CommandQueue) cleanupLoop() {
	defer q.wg.Done()

	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-q.stopCh:
			return
		case <-ticker.C:
			q.cleanup()
		}
	}
}

// cleanup removes expired commands and results
func (q *CommandQueue) cleanup() {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	expiredCount := 0
	timeoutCount := 0

	// Clean expired commands
	for id, cmd := range q.commands {
		// Remove commands past their TTL
		if now.After(cmd.ExpireAt) {
			delete(q.commands, id)
			q.removeCommandID(id)
			expiredCount++
			continue
		}

		// Handle in-flight timeouts
		if cmd.Status == StatusInFlight && cmd.InFlightAt != nil {
			if now.Sub(*cmd.InFlightAt) > CommandInFlightTimeout {
				if cmd.RetryCount < cmd.MaxRetries {
					// Retry: requeue
					cmd.Status = StatusQueued
					cmd.InFlightAt = nil
					cmd.RetryCount++
					timeoutCount++
					if q.logger != nil {
						q.logger.Info("phone-bridge-queue: command %s timed out in-flight, retry %d/%d",
							id, cmd.RetryCount, cmd.MaxRetries)
					}
				} else {
					// Max retries exceeded
					cmd.Status = StatusTimeout
					delete(q.commands, id)
					q.removeCommandID(id)
					timeoutCount++
					if q.logger != nil {
						q.logger.Error("phone-bridge-queue: command %s exceeded max retries, dropped", id)
					}
				}
			}
		}
	}

	// Clean expired results
	for id, result := range q.results {
		if now.Sub(result.CompletedAt) > result.TTL {
			delete(q.results, id)
			expiredCount++
		}
	}

	if q.logger != nil && (expiredCount > 0 || timeoutCount > 0) {
		q.logger.Info("phone-bridge-queue: cleanup removed %d expired, %d timed out", expiredCount, timeoutCount)
	}
}

// removeCommandID removes a command ID from the ordered slice
func (q *CommandQueue) removeCommandID(id string) {
	for i, cmdID := range q.commandIDs {
		if cmdID == id {
			q.commandIDs = append(q.commandIDs[:i], q.commandIDs[i+1:]...)
			return
		}
	}
}
