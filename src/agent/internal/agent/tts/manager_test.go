package tts

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderManagerSwitchToAfterCloseIsRejected(t *testing.T) {
	var factoryCalls atomic.Int32
	manager := NewProviderManagerWithFactory(nil, nil, func(ProviderConfig) (TTSProvider, error) {
		factoryCalls.Add(1)
		return &blockingProvider{name: "after-close", started: make(chan *blockingSession, 1)}, nil
	})

	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	err := manager.SwitchTo(ProviderConfig{Provider: "after-close"})
	if !errors.Is(err, ErrProviderManagerClosed) {
		t.Fatalf("SwitchTo() after Close() error = %v, want ErrProviderManagerClosed", err)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("provider factory calls after Close() = %d, want 0", got)
	}
	if got := manager.Current(); got != "" {
		t.Fatalf("Current() after rejected switch = %q, want empty", got)
	}
}

func TestProviderManagerCloseSerializesConcurrentSwitchTo(t *testing.T) {
	initial := &blockingProvider{
		name:    "initial",
		started: make(chan *blockingSession, 1),
		closed:  make(chan struct{}),
	}
	var factoryCalls atomic.Int32
	manager := NewProviderManagerWithFactory(initial, nil, func(ProviderConfig) (TTSProvider, error) {
		factoryCalls.Add(1)
		return &blockingProvider{name: "replacement", started: make(chan *blockingSession, 1)}, nil
	})

	session, err := manager.Holder().BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	waitForBlockingSession(t, initial.started)

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	waitForManagerCurrent(t, manager, "")

	switchDone := make(chan error, 1)
	go func() {
		switchDone <- manager.SwitchTo(ProviderConfig{Provider: "replacement"})
	}()

	select {
	case <-switchDone:
		t.Fatal("SwitchTo() returned while Close() was still waiting for an active session")
	case <-time.After(50 * time.Millisecond):
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("manager.Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager.Close() did not return after the active session closed")
	}
	select {
	case switchErr := <-switchDone:
		if !errors.Is(switchErr, ErrProviderManagerClosed) {
			t.Fatalf("concurrent SwitchTo() error = %v, want ErrProviderManagerClosed", switchErr)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchTo() did not return after Close() completed")
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("provider factory calls during Close() = %d, want 0", got)
	}
	if got := manager.Current(); got != "" {
		t.Fatalf("Current() after Close() and rejected switch = %q, want empty", got)
	}
	select {
	case <-initial.closed:
	default:
		t.Fatal("initial provider was not closed")
	}
}

func waitForManagerCurrent(t *testing.T, manager *ProviderManager, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if manager.Current() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Current() = %q, want %q", manager.Current(), want)
}
