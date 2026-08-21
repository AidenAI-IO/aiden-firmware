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
