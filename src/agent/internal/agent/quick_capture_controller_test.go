package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type stubQuickCapturer struct {
	mu      sync.Mutex
	calls   int
	id      string
	err     error
	started chan struct{}
	release chan struct{}
}

func (c *stubQuickCapturer) Capture(context.Context) (string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.started != nil {
		close(c.started)
	}
	if c.release != nil {
		<-c.release
	}
	return c.id, c.err
}

func (c *stubQuickCapturer) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestQuickCaptureControllerRunsCapture(t *testing.T) {
	capturer := &stubQuickCapturer{id: "mem_1"}
	controller := NewQuickCaptureController(capturer, nil)
	controller.spawn = func(run func()) { run() }

	if err := controller.Trigger(); err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if capturer.callCount() != 1 {
		t.Fatalf("capture calls = %d, want 1", capturer.callCount())
	}
}

func TestQuickCaptureControllerRejectsConcurrentTrigger(t *testing.T) {
	capturer := &stubQuickCapturer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := NewQuickCaptureController(capturer, nil)
	runDone := make(chan struct{})
	controller.spawn = func(run func()) {
		go func() {
			run()
			close(runDone)
		}()
	}

	if err := controller.Trigger(); err != nil {
		t.Fatalf("first Trigger() error = %v", err)
	}
	<-capturer.started
	if err := controller.Trigger(); !errors.Is(err, ErrQuickCaptureBusy) {
		t.Fatalf("second Trigger() error = %v, want ErrQuickCaptureBusy", err)
	}
	close(capturer.release)
	<-runDone

	controller.mu.Lock()
	running := controller.running
	controller.mu.Unlock()
	if running {
		t.Fatal("controller still marked running after capture completed")
	}
}

func TestQuickCaptureControllerReleasesSingleFlightAfterFailure(t *testing.T) {
	capturer := &stubQuickCapturer{err: errors.New("vision failed")}
	controller := NewQuickCaptureController(capturer, nil)
	controller.spawn = func(run func()) { run() }

	if err := controller.Trigger(); err != nil {
		t.Fatalf("first Trigger() error = %v", err)
	}
	if err := controller.Trigger(); err != nil {
		t.Fatalf("second Trigger() error = %v", err)
	}
	if capturer.callCount() != 2 {
		t.Fatalf("capture calls = %d, want 2", capturer.callCount())
	}
}

func TestQuickCaptureControllerUnavailable(t *testing.T) {
	controller := NewQuickCaptureController(nil, nil)
	if err := controller.Trigger(); !errors.Is(err, ErrQuickCaptureUnavailable) {
		t.Fatalf("Trigger() error = %v, want ErrQuickCaptureUnavailable", err)
	}

	var nilController *QuickCaptureController
	if err := nilController.Trigger(); !errors.Is(err, ErrQuickCaptureUnavailable) {
		t.Fatalf("nil Trigger() error = %v, want ErrQuickCaptureUnavailable", err)
	}
}
