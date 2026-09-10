package agenttask

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	started chan string
	release chan struct{}
	result  string
	err     error
}

func (r *fakeRunner) Run(ctx context.Context, prompt string) (string, error) {
	if r.started != nil {
		r.started <- prompt
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.err
}

func TestManagerCompletesQueuedTask(t *testing.T) {
	runner := &fakeRunner{started: make(chan string, 1), release: make(chan struct{}), result: "done"}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()

	task, err := manager.Create("operate the device")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusQueued {
		t.Fatalf("initial status = %q, want queued", task.Status)
	}
	select {
	case got := <-runner.started:
		if got != task.Prompt {
			t.Fatalf("runner input = %q, want %q", got, task.Prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	close(runner.release)
	waitForStatus(t, manager, task.ID, StatusCompleted)

	terminal := waitForTerminalTasks(t, manager)
	if len(terminal) != 1 || terminal[0].ID != task.ID || terminal[0].Result != "done" {
		t.Fatalf("terminal tasks = %+v", terminal)
	}
}

func TestManagerCancelsRunningTask(t *testing.T) {
	runner := &fakeRunner{started: make(chan string, 1), release: make(chan struct{})}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()

	task, err := manager.Create("long task")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	cancelled, err := manager.Cancel(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelling {
		t.Fatalf("cancel status = %q, want cancelling", cancelled.Status)
	}
	waitForStatus(t, manager, task.ID, StatusCancelled)
}

func TestManagerMarksRunnerFailure(t *testing.T) {
	manager := newManager(&fakeRunner{err: errors.New("boom")}, 4, time.Now)
	defer manager.Close()
	task, err := manager.Create("fail")
	if err != nil {
		t.Fatal(err)
	}
	got := waitForStatus(t, manager, task.ID, StatusFailed)
	if got.Error != "boom" {
		t.Fatalf("error = %q, want boom", got.Error)
	}
}

func TestManagerCancelsQueuedTaskWithoutRunningIt(t *testing.T) {
	runner := &fakeRunner{started: make(chan string, 2), release: make(chan struct{})}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()

	first, err := manager.Create("first")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	second, err := manager.Create("second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, second.ID, StatusCancelled)
	close(runner.release)
	waitForStatus(t, manager, first.ID, StatusCompleted)

	select {
	case prompt := <-runner.started:
		t.Fatalf("cancelled queued task unexpectedly ran: %q", prompt)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestManagerOutstandingTracksInFlightAndUndeliveredWork(t *testing.T) {
	runner := &fakeRunner{started: make(chan string, 2), release: make(chan struct{})}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()

	first, err := manager.Create("first")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	second, err := manager.Create("second")
	if err != nil {
		t.Fatal(err)
	}

	// Work in flight is outstanding, newest first, so a caller checking before
	// starting something new sees the duplicate it just created.
	outstanding := manager.Outstanding()
	if len(outstanding) != 2 || outstanding[0].ID != second.ID || outstanding[1].ID != first.ID {
		t.Fatalf("outstanding = %+v, want %s then %s", outstanding, second.ID, first.ID)
	}

	// A finished task stays outstanding until its result has actually been
	// handed to the foreground: the caller must not start the same work again
	// while the result is still on its way.
	close(runner.release)
	waitForStatus(t, manager, first.ID, StatusCompleted)
	waitForStatus(t, manager, second.ID, StatusCompleted)
	if outstanding := manager.Outstanding(); len(outstanding) != 2 {
		t.Fatalf("outstanding before delivery = %+v, want both results", outstanding)
	}

	manager.DrainTerminalTasks()
	if outstanding := manager.Outstanding(); len(outstanding) != 0 {
		t.Fatalf("outstanding after delivery = %+v, want none", outstanding)
	}
}

func TestManagerRestoresUndeliveredTerminalTasks(t *testing.T) {
	manager := newManager(&fakeRunner{result: "done"}, 4, time.Now)
	defer manager.Close()
	task, err := manager.Create("task")
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, task.ID, StatusCompleted)
	terminal := waitForTerminalTasks(t, manager)
	manager.RestoreTerminalTasks(terminal)
	restored := waitForTerminalTasks(t, manager)
	if len(restored) != 1 || restored[0].ID != task.ID {
		t.Fatalf("restored tasks = %+v", restored)
	}
}

type userActionRunner struct {
	started chan string
}

func (r *userActionRunner) Run(ctx context.Context, prompt string) (string, error) {
	r.started <- prompt
	if prompt == "open the app" {
		handler := UserActionHandlerFromContext(ctx)
		if handler == nil {
			return "", errors.New("user action handler missing")
		}
		handler(UserAction{Reason: "authentication", Details: "Login is required", SuggestedAction: "Sign in on the device"})
	}
	return "done", nil
}

func TestManagerPausesAndContinuesAfterUserAction(t *testing.T) {
	runner := &userActionRunner{started: make(chan string, 2)}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()

	task, err := manager.Create("open the app")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case prompt := <-runner.started:
		if prompt != task.Prompt {
			t.Fatalf("first prompt = %q, want %q", prompt, task.Prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}

	var paused Task
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		paused, _ = manager.Query(task.ID)
		if paused.Status == StatusRunning && paused.PendingUserAction != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if paused.Status != StatusRunning || paused.PendingUserAction == nil {
		t.Fatalf("paused task = %+v", paused)
	}
	if _, err := manager.Continue(task.ID, "用户已完成登录并回到首页"); err != nil {
		t.Fatal(err)
	}
	select {
	case prompt := <-runner.started:
		if prompt != "用户已完成登录并回到首页" {
			t.Fatalf("continuation prompt = %q", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("task did not resume")
	}
	completed := waitForStatus(t, manager, task.ID, StatusCompleted)
	if completed.Result != "done" || completed.PendingUserAction != nil {
		t.Fatalf("completed task = %+v", completed)
	}
}

func TestManagerCancelsPausedUserActionTask(t *testing.T) {
	runner := &userActionRunner{started: make(chan string, 1)}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()
	task, err := manager.Create("open the app")
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	waitForPendingUserAction(t, manager, task.ID)
	if _, err := manager.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, task.ID, StatusCancelled)
}

// A task that paused for a user action and then reached a terminal state must
// not carry the stale action. Otherwise the foreground is told to perform an
// action for finished work, and session teardown misclassifies the terminal
// update as an action request, which silently drops it.
func TestManagerCancelledPausedTaskClearsPendingUserAction(t *testing.T) {
	runner := &userActionRunner{started: make(chan string, 1)}
	manager := newManager(runner, 4, time.Now)
	defer manager.Close()
	task, err := manager.Create("open the app")
	if err != nil {
		t.Fatal(err)
	}
	<-runner.started
	waitForPendingUserAction(t, manager, task.ID)
	cancelled, err := manager.Cancel(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.PendingUserAction != nil {
		t.Fatalf("cancel snapshot still carries pending user action: %+v", cancelled.PendingUserAction)
	}
	terminal := waitForTerminalTasks(t, manager)
	if len(terminal) != 1 || terminal[0].Status != StatusCancelled {
		t.Fatalf("terminal = %+v", terminal)
	}
	if terminal[0].PendingUserAction != nil {
		t.Fatalf("terminal task still carries pending user action: %+v", terminal[0].PendingUserAction)
	}
	if queried, _ := manager.Query(task.ID); queried.PendingUserAction != nil {
		t.Fatalf("queried task still carries pending user action: %+v", queried.PendingUserAction)
	}
	// A restored terminal update must stay terminal and stay deliverable.
	manager.RestoreTerminalTasks(terminal)
	restored := waitForTerminalTasks(t, manager)
	if len(restored) != 1 || restored[0].ID != task.ID {
		t.Fatalf("restored = %+v", restored)
	}
	if restored[0].PendingUserAction != nil {
		t.Fatalf("restored task carries pending user action: %+v", restored[0].PendingUserAction)
	}
}

func TestManagerTerminalStateDoesNotConsume(t *testing.T) {
	manager := newManager(&fakeRunner{result: "done"}, 4, time.Now)
	defer manager.Close()
	task, err := manager.Create("task")
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, task.ID, StatusCompleted)
	pending, seq := manager.TerminalState()
	if len(pending) != 1 || pending[0].ID != task.ID {
		t.Fatalf("pending = %+v", pending)
	}
	if seq != 1 {
		t.Fatalf("sequence = %d, want 1", seq)
	}
	// Peeking must leave the update for the foreground session to drain.
	if again, _ := manager.TerminalState(); len(again) != 1 {
		t.Fatalf("second peek = %+v", again)
	}
	if terminal := waitForTerminalTasks(t, manager); len(terminal) != 1 {
		t.Fatalf("terminal = %+v", terminal)
	}
	pending, seq = manager.TerminalState()
	if len(pending) != 0 {
		t.Fatalf("pending after drain = %+v", pending)
	}
	if seq != 1 {
		t.Fatalf("sequence after drain = %d, want 1", seq)
	}
}

func TestManagerSignalsWakeAndSequencePerTerminalTask(t *testing.T) {
	manager := newManager(&fakeRunner{result: "done"}, 4, time.Now)
	defer manager.Close()
	if seq := manager.TerminalSequence(); seq != 0 {
		t.Fatalf("initial sequence = %d, want 0", seq)
	}
	task, err := manager.Create("task")
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, task.ID, StatusCompleted)
	select {
	case <-manager.WakeNotifications():
	case <-time.After(time.Second):
		t.Fatal("terminal task did not signal a standby wake")
	}
	if seq := manager.TerminalSequence(); seq != 1 {
		t.Fatalf("sequence = %d, want 1", seq)
	}
	// Draining the wake signal must not steal the foreground's drain signal.
	terminal := waitForTerminalTasks(t, manager)
	if len(terminal) != 1 || terminal[0].ID != task.ID {
		t.Fatalf("terminal = %+v", terminal)
	}
}

func TestManagerWakeSignalCoalescesWithoutAdvancingForDelivery(t *testing.T) {
	manager := newManager(&fakeRunner{result: "done"}, 4, time.Now)
	defer manager.Close()
	for _, prompt := range []string{"first", "second"} {
		if _, err := manager.Create(prompt); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if manager.TerminalSequence() == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if seq := manager.TerminalSequence(); seq != 2 {
		t.Fatalf("sequence = %d, want 2", seq)
	}
	select {
	case <-manager.WakeNotifications():
	case <-time.After(time.Second):
		t.Fatal("terminal tasks did not signal a standby wake")
	}
	// One coalesced signal is enough: a second read must not report new work.
	select {
	case <-manager.WakeNotifications():
		t.Fatal("wake signal did not coalesce")
	default:
	}
}

func waitForPendingUserAction(t *testing.T, manager *Manager, taskID string) Task {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if task, ok := manager.Query(taskID); ok && task.PendingUserAction != nil {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	task, _ := manager.Query(taskID)
	t.Fatalf("task did not pause for user action: %+v", task)
	return Task{}
}

func waitForStatus(t *testing.T, manager *Manager, taskID string, want Status) Task {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if task, ok := manager.Query(taskID); ok && task.Status == want {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	task, _ := manager.Query(taskID)
	t.Fatalf("task status = %q, want %q", task.Status, want)
	return Task{}
}

func waitForTerminalTasks(t *testing.T, manager *Manager) []Task {
	t.Helper()
	select {
	case <-manager.TerminalNotifications():
		return manager.DrainTerminalTasks()
	case <-time.After(time.Second):
		t.Fatal("terminal task notification timeout")
		return nil
	}
}
