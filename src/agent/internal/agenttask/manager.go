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
	ID          string     `json:"id"`
	Prompt      string     `json:"prompt"`
	Status      Status     `json:"status"`
	Result      string     `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Runner is the narrow boundary between task orchestration and an agent
// implementation. The manager does not depend on the legacy agent package.
type Runner interface {
	Run(context.Context, string) (string, error)
}

type entry struct {
	task   Task
	cancel context.CancelFunc
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

func (m *Manager) TerminalNotifications() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.terminalChanged
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
	if !ok || item.task.Status != StatusQueued {
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
	item.task.Status = StatusRunning
	item.task.StartedAt = &startedAt
	item.task.UpdatedAt = startedAt
	prompt := item.task.Prompt
	m.mu.Unlock()

	result, err := m.runner.Run(taskCtx, prompt)
	cancel()

	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok = m.tasks[taskID]
	if !ok {
		return
	}
	item.cancel = nil
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
