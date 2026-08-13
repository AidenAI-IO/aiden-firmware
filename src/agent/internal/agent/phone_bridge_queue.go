package agent

import (
	"errors"
	"fmt"
	"strings"
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

// ErrCommandExists indicates a queued command or retained result already uses
// the requested ID.
var ErrCommandExists = errors.New("phone bridge command already exists")

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
		return fmt.Errorf("command %s already exists: %w", cmd.ID, ErrCommandExists)
	}
	if _, exists := q.results[cmd.ID]; exists {
		return fmt.Errorf("command %s already has a retained result: %w", cmd.ID, ErrCommandExists)
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

// Cancel removes a queued or in-flight command that should no longer be
// executed, usually because the caller stopped waiting for its result.
func (q *CommandQueue) Cancel(commandID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.commands[commandID]; !exists {
		return false
	}
	delete(q.commands, commandID)
	q.removeCommandIDLocked(commandID)
	if q.logger != nil {
		q.logger.Info("phone-bridge-queue: canceled command %s", commandID)
	}
	return true
}

// Poll retrieves up to `limit` queued commands and marks them as in-flight
func (q *CommandQueue) Poll(platform string, limit int) []BridgeCommand {
	return q.PollForPhone(platform, "", limit)
}

// PollForPhone retrieves queued commands for a platform and optional phone ID.
// Commands without a phone_id remain compatible and can be picked up by any
// matching platform client.
func (q *CommandQueue) PollForPhone(platform, phoneID string, limit int) []BridgeCommand {
	return q.PollForPhoneMatching(platform, phoneID, limit, nil)
}

// PollForPhoneMatching retrieves queued commands accepted by the optional
// predicate and marks only those commands as in-flight. Rejected commands stay
// queued so a later foreground or broader-capability poll can execute them.
func (q *CommandQueue) PollForPhoneMatching(platform, phoneID string, limit int, allowed func(BridgeCommand) bool) []BridgeCommand {
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

		if !q.matchesPlatform(&cmd.Command, platform) {
			continue
		}
		if !q.matchesPhoneID(&cmd.Command, phoneID) {
			continue
		}
		if allowed != nil && !allowed(cmd.Command) {
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
		q.logger.Info("phone-bridge-queue: polled %d command(s) for platform=%s phone_id=%s", len(commands), platform, phoneID)
	}

	return commands
}

// matchesPlatform checks if a command is relevant for the given platform
func (q *CommandQueue) matchesPlatform(_ *BridgeCommand, _ string) bool {
	return true
}

func (q *CommandQueue) matchesPhoneID(cmd *BridgeCommand, phoneID string) bool {
	commandPhoneID := strings.TrimSpace(cmd.PhoneID)
	if commandPhoneID == "" {
		return true
	}
	return commandPhoneID == strings.TrimSpace(phoneID)
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
	q.removeCommandIDLocked(resp.ID)

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
			q.removeCommandIDLocked(id)
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
					q.removeCommandIDLocked(id)
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

// removeCommandIDLocked removes a command ID from the ordered slice.
// The caller must hold q.mu.
func (q *CommandQueue) removeCommandIDLocked(id string) {
	for i, cmdID := range q.commandIDs {
		if cmdID == id {
			q.commandIDs = append(q.commandIDs[:i], q.commandIDs[i+1:]...)
			return
		}
	}
}
