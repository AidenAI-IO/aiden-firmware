package tts

import (
	"context"
	"errors"
	"runtime"
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

func TestProviderManagerSwitchToDoesNotWaitForOldSession(t *testing.T) {
	initial := &blockingProvider{
		name:    "initial",
		started: make(chan *blockingSession, 1),
		closed:  make(chan struct{}),
	}
	replacement := &blockingProvider{
		name:    "replacement",
		started: make(chan *blockingSession, 1),
	}
	manager := NewProviderManagerWithFactory(initial, nil, func(ProviderConfig) (TTSProvider, error) {
		return replacement, nil
	})

	session, err := manager.Holder().BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	waitForBlockingSession(t, initial.started)

	switchDone := make(chan error, 1)
	go func() {
		switchDone <- manager.SwitchTo(ProviderConfig{Provider: "replacement"})
	}()

	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatalf("SwitchTo() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SwitchTo() waited for the old session to close")
	}
	if got := manager.Current(); got != "replacement" {
		t.Fatalf("Current() = %q, want replacement", got)
	}
	replacementSession, err := manager.Holder().BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("replacement BeginStream() error = %v", err)
	}
	waitForBlockingSession(t, replacement.started)
	if err := replacementSession.Close(); err != nil {
		t.Fatalf("replacement session.Close() error = %v", err)
	}
	select {
	case <-initial.closed:
		t.Fatal("old provider closed while its session was still active")
	default:
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	select {
	case <-initial.closed:
	case <-time.After(time.Second):
		t.Fatal("old provider was not closed after its session drained")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("manager.Close() error = %v", err)
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

func TestProviderManagerCloseContextReturnsOnTimeout(t *testing.T) {
	provider := &blockingProvider{
		name:    "current",
		started: make(chan *blockingSession, 1),
		closed:  make(chan struct{}),
	}
	manager := NewProviderManager(provider, nil)
	session, err := manager.Holder().BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	waitForBlockingSession(t, provider.started)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = manager.CloseContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext() error = %v, want context deadline exceeded", err)
	}
	if got := manager.Current(); got != "" {
		t.Fatalf("Current() = %q after timed-out close, want empty", got)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() after session drain error = %v", err)
	}
	select {
	case <-provider.closed:
	case <-time.After(time.Second):
		t.Fatal("provider was not closed after the session drained")
	}
}

func TestProviderManagerRepeatedCloseTimeoutsReuseWaiter(t *testing.T) {
	provider := &blockingProvider{
		name:    "current",
		started: make(chan *blockingSession, 1),
		closed:  make(chan struct{}),
	}
	manager := NewProviderManager(provider, nil)
	session, err := manager.Holder().BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	waitForBlockingSession(t, provider.started)

	before := runtime.NumGoroutine()
	for range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		err := manager.CloseContext(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("CloseContext() error = %v, want context deadline exceeded", err)
		}
	}
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 4 {
		t.Fatalf("repeated CloseContext timeouts retained %d goroutines, want at most 4", delta)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("manager.Close() after session drain error = %v", err)
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
