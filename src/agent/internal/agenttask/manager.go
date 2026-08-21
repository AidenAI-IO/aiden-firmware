package agenttask

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusCreated    Status = "created"
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusCancelling Status = "cancelling"
	StatusCancelled  Status = "cancelled"
	StatusFailed     Status = "failed"
	StatusCompleted  Status = "completed"
)

const (
	defaultQueueSize = 64
	maxResultRunes   = 8000
)

type Task struct {
	ID                string      `json:"id"`
	Prompt            string      `json:"prompt"`
	Status            Status      `json:"status"`
	Result            string      `json:"result,omitempty"`
	Error             string      `json:"error,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
	StartedAt         *time.Time  `json:"started_at,omitempty"`
	CompletedAt       *time.Time  `json:"completed_at,omitempty"`
	PendingUserAction *UserAction `json:"pending_user_action,omitempty"`
}

type UserAction struct {
	Reason          string `json:"reason"`
	Details         string `json:"details"`
	SuggestedAction string `json:"suggested_action,omitempty"`
}

type userActionHandlerContextKey struct{}

func WithUserActionHandler(ctx context.Context, handler func(UserAction)) context.Context {
	return context.WithValue(ctx, userActionHandlerContextKey{}, handler)
}

func UserActionHandlerFromContext(ctx context.Context) func(UserAction) {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(userActionHandlerContextKey{}).(func(UserAction))
	return h
}

// Runner is the narrow boundary between task orchestration and an agent
// implementation. The manager does not depend on the legacy agent package.
type Runner interface {
	Run(context.Context, string) (string, error)
}

type entry struct {
	task           Task
	cancel         context.CancelFunc
	nextPrompt     string
	actionNotified bool
	resumeQueued   bool
}

// Manager serializes background work and keeps create, cancel, and query
// operations independent from task execution latency.
type Manager struct {
	runner Runner
	now    func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan string
	wg     sync.WaitGroup

	mu              sync.RWMutex
	tasks           map[string]*entry
	terminal        []Task
	terminalChanged chan struct{}
	actionChanged   chan struct{}
	closed          bool
}

func NewManager(runner Runner) *Manager {
	return newManager(runner, defaultQueueSize, time.Now)
}

func newManager(runner Runner, queueSize int, now func() time.Time) *Manager {
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		runner:          runner,
		now:             now,
		ctx:             ctx,
		cancel:          cancel,
		queue:           make(chan string, queueSize),
		tasks:           make(map[string]*entry),
		terminalChanged: make(chan struct{}, 1),
		actionChanged:   make(chan struct{}, 1),
	}
	m.wg.Add(1)
	go m.worker()
	return m
}

func (m *Manager) Create(prompt string) (Task, error) {
	if m == nil {
		return Task{}, errors.New("agent task manager is unavailable")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Task{}, errors.New("task is required")
	}
	now := m.now().UTC()
	task := Task{
		ID:        "task_" + uuid.NewString(),
		Prompt:    prompt,
		Status:    StatusCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Task{}, errors.New("agent task manager is closed")
	}
	task.Status = StatusQueued
	m.tasks[task.ID] = &entry{task: task}
	select {
	case m.queue <- task.ID:
		m.mu.Unlock()
		return task, nil
	default:
		delete(m.tasks, task.ID)
		m.mu.Unlock()
		return Task{}, errors.New("agent task queue is full")
	}
}

func (m *Manager) Query(taskID string) (Task, bool) {
	if m == nil {
		return Task{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return Task{}, false
	}
	return item.task, true
}

func (m *Manager) Cancel(taskID string) (Task, error) {
	if m == nil {
		return Task{}, errors.New("agent task manager is unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	m.mu.Lock()
	item, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return Task{}, errors.New("agent task not found")
	}
	var cancel context.CancelFunc
	switch item.task.Status {
	case StatusCreated, StatusQueued:
		m.finishLocked(item, StatusCancelled, "", "")
	case StatusRunning:
		if item.task.PendingUserAction != nil {
			m.finishLocked(item, StatusCancelled, "", "")
			break
		}
		item.task.Status = StatusCancelling
		item.task.UpdatedAt = m.now().UTC()
		cancel = item.cancel
	case StatusCancelling, StatusCancelled, StatusFailed, StatusCompleted:
		// Cancellation is idempotent for tasks already stopping or terminal.
	}
	task := item.task
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return task, nil
}

func (m *Manager) Continue(taskID, userMessage string) (Task, error) {
	if m == nil {
		return Task{}, errors.New("agent task manager is unavailable")
	}
	taskID, userMessage = strings.TrimSpace(taskID), strings.TrimSpace(userMessage)
	if userMessage == "" {
		return Task{}, errors.New("user_message is required")
	}
	m.mu.Lock()
	item, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return Task{}, errors.New("agent task not found")
	}
	if item.task.Status != StatusRunning || item.task.PendingUserAction == nil {
		m.mu.Unlock()
		return Task{}, errors.New("agent task is not waiting for user action")
	}
	select {
	case m.queue <- taskID:
		item.task.PendingUserAction = nil
		item.actionNotified = false
		item.nextPrompt = userMessage
		item.resumeQueued = true
		item.task.UpdatedAt = m.now().UTC()
		task := item.task
		m.mu.Unlock()
		return task, nil
	default:
		m.mu.Unlock()
		return Task{}, errors.New("agent task queue is full")
	}
}

func (m *Manager) TerminalNotifications() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.terminalChanged
}

func (m *Manager) UserActionNotifications() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.actionChanged
}

func (m *Manager) DrainUserActionTasks() []Task {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Task
	for _, item := range m.tasks {
		if item.task.Status == StatusRunning && item.task.PendingUserAction != nil && !item.actionNotified {
			item.actionNotified = true
			result = append(result, item.task)
		}
	}
	return result
}

// RestoreUserActionTasks makes user-action notifications eligible for delivery
// again when a foreground session ends before it could tell the user.
func (m *Manager) RestoreUserActionTasks(tasks []Task) {
	if m == nil || len(tasks) == 0 {
		return
	}
	ids := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		ids[task.ID] = struct{}{}
	}
	m.mu.Lock()
	changed := false
	for id := range ids {
		if item, ok := m.tasks[id]; ok && item.task.Status == StatusRunning && item.task.PendingUserAction != nil {
			item.actionNotified = false
			changed = true
		}
	}
	if changed {
		select {
		case m.actionChanged <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
}

// PendingUserActionTasks returns all running tasks waiting for user action and
// makes them eligible for notification in a future foreground session.
func (m *Manager) PendingUserActionTasks() []Task {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Task
	for _, item := range m.tasks {
		if item.task.Status == StatusRunning && item.task.PendingUserAction != nil {
			item.actionNotified = false
			result = append(result, item.task)
		}
	}
	return result
}

func (m *Manager) DrainTerminalTasks() []Task {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := append([]Task(nil), m.terminal...)
	m.terminal = nil
	return result
}

// RestoreTerminalTasks returns updates to the front of the delivery queue when
// a foreground session ends before it can inject them.
func (m *Manager) RestoreTerminalTasks(tasks []Task) {
	if m == nil || len(tasks) == 0 {
		return
	}
	m.mu.Lock()
	m.terminal = append(append([]Task(nil), tasks...), m.terminal...)
	m.signalTerminalLocked()
	m.mu.Unlock()
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, item := range m.tasks {
		switch item.task.Status {
		case StatusCreated, StatusQueued:
			m.finishLocked(item, StatusCancelled, "", "")
		case StatusRunning:
			if item.task.PendingUserAction != nil {
				m.finishLocked(item, StatusCancelled, "", "")
				continue
			}
			item.task.Status = StatusCancelling
			item.task.UpdatedAt = m.now().UTC()
			if item.cancel != nil {
				item.cancel()
			}
		}
	}
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case taskID := <-m.queue:
			m.runTask(taskID)
		}
	}
}

func (m *Manager) runTask(taskID string) {
	m.mu.Lock()
	item, ok := m.tasks[taskID]
	if !ok || (item.task.Status != StatusQueued && (item.task.Status != StatusRunning || item.task.PendingUserAction != nil)) {
		m.mu.Unlock()
		return
	}
	if m.runner == nil {
		m.finishLocked(item, StatusFailed, "", "background agent is unavailable")
		m.mu.Unlock()
		return
	}
	taskCtx, cancel := context.WithCancel(m.ctx)
	startedAt := m.now().UTC()
	item.cancel = cancel
	item.resumeQueued = false
	item.task.Status = StatusRunning
	item.task.StartedAt = &startedAt
	item.task.UpdatedAt = startedAt
	prompt := item.nextPrompt
	if prompt == "" {
		prompt = item.task.Prompt
	}
	item.nextPrompt = ""
	m.mu.Unlock()

	result, err := m.runner.Run(WithUserActionHandler(taskCtx, func(action UserAction) {
		m.mu.Lock()
		if current, exists := m.tasks[taskID]; exists && current.task.Status == StatusRunning {
			current.task.PendingUserAction = &action
			current.actionNotified = false
			current.task.UpdatedAt = m.now().UTC()
			select {
			case m.actionChanged <- struct{}{}:
			default:
			}
		}
		m.mu.Unlock()
	}), prompt)
	cancel()

	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok = m.tasks[taskID]
	if !ok {
		return
	}
	item.cancel = nil
	if item.task.Status == StatusCancelled || item.task.Status == StatusFailed || item.task.Status == StatusCompleted {
		return
	}
	if item.resumeQueued {
		return
	}
	if item.task.PendingUserAction != nil {
		return
	}
	if item.task.Status == StatusCancelling || errors.Is(err, context.Canceled) {
		m.finishLocked(item, StatusCancelled, "", "")
		return
	}
	if err != nil {
		m.finishLocked(item, StatusFailed, "", err.Error())
		return
	}
	m.finishLocked(item, StatusCompleted, limitText(result, maxResultRunes), "")
}

func (m *Manager) finishLocked(item *entry, status Status, result, taskError string) {
	now := m.now().UTC()
	item.task.Status = status
	item.task.Result = result
	item.task.Error = taskError
	item.task.UpdatedAt = now
	item.task.CompletedAt = &now
	m.terminal = append(m.terminal, item.task)
	m.signalTerminalLocked()
}

func (m *Manager) signalTerminalLocked() {
	select {
	case m.terminalChanged <- struct{}{}:
	default:
	}
}

func limitText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
